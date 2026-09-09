// Package adversarial generates hostile Usenet inputs without consuming any
// checked-in binary corpus or importing a client's implementation.
package adversarial

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

const SchemaVersion = 1
const RecipeVersion = "3"
const MaxArtifactBytes = 16 << 20
const MaxBundleBytes = 64 << 20

type Expectation string

const (
	Accept  Expectation = "accept"
	Reject  Expectation = "reject"
	Recover Expectation = "recover"
	Contain Expectation = "contain"
)

// Contain leaves acceptance policy open, but still requires all security
// observations. It is used for ambiguous or implementation-defined inputs.
type Case struct {
	DisplayName string      `json:"display_name"`
	ID          string      `json:"id"`
	Family      string      `json:"family"`
	Vector      string      `json:"vector"`
	Expectation Expectation `json:"expectation"`
	Recipe      string      `json:"recipe"`
	Variant     string      `json:"variant"`
}

type Limits struct {
	WallMilliseconds int64 `json:"wall_milliseconds"`
	CPUMilliseconds  int64 `json:"cpu_milliseconds"`
	MemoryBytes      int64 `json:"memory_bytes"`
	WrittenBytes     int64 `json:"written_bytes"`
	Entries          int64 `json:"entries"`
	FileDescriptors  int64 `json:"file_descriptors"`
	Connections      int64 `json:"connections"`
}

// Missing measurements cannot silently decode as zero and become a pass.
func (l *Limits) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	names := []string{"wall_milliseconds", "cpu_milliseconds", "memory_bytes", "written_bytes", "entries", "file_descriptors", "connections"}
	if len(fields) != len(names) {
		return fmt.Errorf("resource measurements require all seven named counters")
	}
	for _, name := range names {
		if raw, ok := fields[name]; !ok || string(raw) == "null" {
			return fmt.Errorf("missing resource measurement: %s", name)
		}
	}
	type plain Limits
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*l = Limits(decoded)
	return nil
}

func DefaultLimits() Limits {
	return Limits{60000, 30000, 512 << 20, 64 << 20, 4096, 256, 64}
}

type Artifact struct {
	Name   string `json:"name"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type Article struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// Fault applies to BODY/ARTICLE replies only. The responder's own connection,
// request, and time caps are separate from the product resource limits.
type Fault struct {
	Kind              string `json:"kind,omitempty"`
	ChunkBytes        int    `json:"chunk_bytes,omitempty"`
	DelayMilliseconds int    `json:"delay_milliseconds,omitempty"`
}

type Manifest struct {
	Contract        RecipeContract    `json:"contract"`
	SchemaVersion   int               `json:"schema_version"`
	RecipeVersion   string            `json:"recipe_version"`
	GoToolchain     string            `json:"go_toolchain"`
	Case            Case              `json:"case"`
	Limits          Limits            `json:"limits"`
	Files           []Artifact        `json:"files"`
	Articles        []Article         `json:"articles"`
	Fault           Fault             `json:"fault"`
	ExpectedOutputs map[string]string `json:"expected_outputs_sha256,omitempty"`
}

type Bundle struct {
	Manifest Manifest
	Data     map[string][]byte
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func Generate(id string) (Bundle, error) {
	var selected *Case
	for _, c := range Catalog() {
		if c.ID == id {
			copy := c
			selected = &copy
			break
		}
	}
	if selected == nil {
		return Bundle{}, fmt.Errorf("unknown case %q", id)
	}
	b := Bundle{Manifest: Manifest{SchemaVersion: SchemaVersion, RecipeVersion: RecipeVersion, GoToolchain: runtime.Version(), Case: *selected, Limits: DefaultLimits()}, Data: map[string][]byte{}}
	b.Manifest.Contract = contractFor(*selected)
	if err := build(&b); err != nil {
		return Bundle{}, fmt.Errorf("%s: %w", id, err)
	}
	names := make([]string, 0, len(b.Data))
	for name := range b.Data {
		names = append(names, name)
	}
	sort.Strings(names)
	total := 0
	for _, name := range names {
		data := b.Data[name]
		if !safeName(name) || len(data) > MaxArtifactBytes {
			return Bundle{}, fmt.Errorf("invalid generated artifact %q", name)
		}
		total += len(data)
		b.Manifest.Files = append(b.Manifest.Files, Artifact{name, len(data), Digest(data)})
	}
	if total > MaxBundleBytes {
		return Bundle{}, fmt.Errorf("bundle too large")
	}
	return b, nil
}

func safeName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Export requires a new leaf directory. Hostile logical filenames remain
// inside the generated bytes; they are never used as host paths.
func Export(parent string, b Bundle) (string, error) {
	if !safeName(b.Manifest.Case.ID) {
		return "", fmt.Errorf("unsafe case ID")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return "", err
	}
	defer root.Close()
	// Reserve the case name, then publish a complete staging directory. The
	// reservation prevents two generators from replacing each other's output.
	lock := "." + b.Manifest.Case.ID + ".lock"
	if err := writeNew(root, lock, nil); err != nil {
		return "", err
	}
	defer root.Remove(lock)
	if _, err := root.Lstat(b.Manifest.Case.ID); err == nil {
		return "", fmt.Errorf("bundle already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	stage := "." + b.Manifest.Case.ID + "-" + rand.Text()
	if err := root.Mkdir(stage, 0700); err != nil {
		return "", err
	}
	defer root.RemoveAll(stage)
	dir, err := root.OpenRoot(stage)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	for _, a := range b.Manifest.Files {
		if !safeName(a.Name) {
			return "", fmt.Errorf("unsafe artifact name")
		}
		if err := writeNew(dir, a.Name, b.Data[a.Name]); err != nil {
			return "", err
		}
	}
	raw, err := json.MarshalIndent(b.Manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeNew(dir, "manifest.json", append(raw, '\n')); err != nil {
		return "", err
	}
	if err := dir.Close(); err != nil {
		return "", err
	}
	if err := root.Rename(stage, b.Manifest.Case.ID); err != nil {
		return "", err
	}
	return filepath.Join(parent, b.Manifest.Case.ID), nil
}

func writeNew(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// Verify regenerates the recipe as well as checking artifact hashes. A changed
// manifest cannot silently change the expected behavior or bless changed bytes.
func Verify(path string) (Bundle, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return Bundle{}, err
	}
	defer root.Close()
	raw, err := readRegular(root, "manifest.json", 1<<20)
	if err != nil {
		return Bundle{}, err
	}
	var m Manifest
	if err := DecodeJSON(raw, &m); err != nil {
		return Bundle{}, err
	}
	expected, err := Generate(m.Case.ID)
	if err != nil {
		return Bundle{}, err
	}
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(expected.Manifest)
	if string(a) != string(b) {
		return Bundle{}, fmt.Errorf("manifest differs from recipe %s", m.Case.ID)
	}
	for _, artifact := range m.Files {
		data, err := readRegular(root, artifact.Name, MaxArtifactBytes)
		if err != nil {
			return Bundle{}, err
		}
		if len(data) != artifact.Size || Digest(data) != artifact.SHA256 {
			return Bundle{}, fmt.Errorf("artifact integrity mismatch: %s", artifact.Name)
		}
	}
	allowed := map[string]bool{"manifest.json": true}
	for _, a := range m.Files {
		allowed[a.Name] = true
	}
	directory, err := root.Open(".")
	if err != nil {
		return Bundle{}, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(len(allowed) + 1)
	if err != nil && err != io.EOF {
		return Bundle{}, err
	}
	if len(entries) != len(allowed) {
		return Bundle{}, fmt.Errorf("bundle inventory differs from manifest")
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || !entry.Type().IsRegular() {
			return Bundle{}, fmt.Errorf("undeclared bundle entry %q", entry.Name())
		}
	}
	return expected, nil
}

func readRegular(root *os.Root, name string, limit int64) ([]byte, error) {
	if !safeName(name) {
		return nil, fmt.Errorf("unsafe artifact name %q", name)
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("nonregular or oversized artifact %q", name)
	}
	f, err := openEvidence(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("artifact changed during inspection: %q", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("artifact exceeds limit")
	}
	return data, err
}
