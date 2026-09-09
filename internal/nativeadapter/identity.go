package nativeadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/benchmark"
)

// Ambient runtime injection variables are excluded. Explicit adapter overrides
// remain allowed and are included in the rendered configuration audit.
func effectiveNativeEnvironment(spec productSpec) []string {
	return nativeEnvironmentForOS(runtime.GOOS, os.LookupEnv, spec.Environment)
}

func nativeEnvironmentForOS(goos string, lookup func(string) (string, bool), overrides []string) []string {
	allowed := []string{"PATH", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TMP", "TEMP", "TMPDIR", "LANG", "LC_ALL", "TZ"}
	values := map[string]string{}
	put := func(key, value string) {
		if goos == "windows" {
			key = strings.ToUpper(key)
		}
		values[key] = value
	}
	for _, key := range allowed {
		if value, ok := lookup(key); ok {
			put(key, value)
		}
	}
	for _, entry := range overrides {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			put(key, value)
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

type softwareSnapshot struct {
	Entry             string            `json:"entry_sha256"`
	Sources           map[string]string `json:"source_sha256"`
	Helpers           map[string]string `json:"helper_sha256"`
	EnvironmentSHA256 string            `json:"environment_sha256"`
	Links             map[string]string `json:"resolved_links"`
	Coverage          string            `json:"coverage"`
}

func nativeSoftwareIdentity(cfg Config, spec productSpec) (string, string, error) {
	snapshot, err := snapshotNativeSoftware(cfg, spec)
	if err != nil {
		return "", "", err
	}
	if cfg.ConfigDir != "" {
		raw, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			return "", "", err
		}
		if err = writeNewFile(filepath.Join(cfg.ConfigDir, "software-identity.json"), raw); err != nil {
			return "", "", err
		}
	}
	return "sha256:" + benchmark.EvidenceDigest(snapshot), "sha256:" + benchmark.EvidenceDigest(snapshot.Helpers), nil
}

func snapshotNativeSoftware(cfg Config, spec productSpec) (softwareSnapshot, error) {
	snapshot := softwareSnapshot{Sources: map[string]string{}, Helpers: map[string]string{}, Links: map[string]string{}, Coverage: "entry, application bundle, file arguments, resolved helpers and declared runtime roots; OS libraries are host provenance"}
	if err := validateIdentityPaths(cfg.IdentityPaths); err != nil {
		return snapshot, err
	}
	if len(spec.Command) == 0 {
		return snapshot, fmt.Errorf("missing native executable")
	}
	program, err := exec.LookPath(spec.Command[0])
	if err != nil {
		return snapshot, err
	}
	program, err = filepath.Abs(program)
	if err != nil {
		return snapshot, err
	}
	snapshot.Entry, err = hashSoftwareFile(program)
	if err != nil {
		return snapshot, err
	}
	env := effectiveNativeEnvironment(spec)
	rawEnv, _ := json.Marshal(env)
	snapshot.EnvironmentSHA256 = benchmark.EvidenceDigest(string(canonicalizeSandboxPaths(cfg, rawEnv)))
	roots := append([]string(nil), cfg.IdentityPaths...)
	for p := filepath.Dir(program); p != filepath.Dir(p); p = filepath.Dir(p) {
		if strings.EqualFold(filepath.Ext(p), ".app") {
			roots = append(roots, p)
			break
		}
	}
	base := strings.ToLower(filepath.Base(program))
	if (strings.Contains(base, "python") || strings.HasPrefix(base, "pypy") || base == "node" || base == "node.exe") && len(cfg.IdentityPaths) == 0 {
		return snapshot, fmt.Errorf("interpreted clients require NATIVE_IDENTITY_PATHS covering application source and interpreter/site-packages runtime")
	}
	args := cfg.LaunchCommand
	if len(args) > 0 {
		args = args[1:]
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") || strings.Contains(arg, "{{") {
			continue
		}
		name := arg
		if !filepath.IsAbs(name) {
			name = filepath.Join(cfg.WorkingDir, name)
		}
		name, err = filepath.Abs(name)
		if err != nil {
			return snapshot, err
		}
		info, err := os.Stat(name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		root := name
		if strings.EqualFold(filepath.Ext(name), ".py") {
			root = filepath.Dir(name)
		}
		roots = append(roots, root)
	}
	for _, name := range []string{"unrar", "7zz", "7z", "7za", "par2"} {
		// Clients differ in bundle-vs-PATH selection. Bind both candidates so
		// a bundled copy cannot hide a change to the PATH helper another client
		// actually launches; this is an inventory, not a process-use assertion.
		for _, pathOnly := range []bool{false, true} {
			key := name
			resolved, err := resolveIdentityHelper(program, name, env)
			if pathOnly {
				key += "-path"
				resolved, err = resolveIdentityPATH(name, env)
			}
			if err != nil {
				snapshot.Helpers[key] = "unavailable"
				continue
			}
			digest, err := hashSoftwareFile(resolved)
			if err != nil {
				return snapshot, err
			}
			snapshot.Helpers[key] = resolved + "=" + digest
			roots = append(roots, resolved)
		}
	}
	visited := map[string]bool{}
	var total int64
	entries := 0
	for len(roots) > 0 {
		root := roots[0]
		roots = roots[1:]
		absolute, err := filepath.Abs(root)
		if err != nil {
			return snapshot, err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return snapshot, err
		}
		if filepath.Dir(resolved) == resolved {
			return snapshot, fmt.Errorf("software identity root resolves to a filesystem root")
		}
		if absolute != resolved {
			snapshot.Links[absolute] = resolved
		}
		if visited[resolved] {
			continue
		}
		visited[resolved] = true
		err = filepath.WalkDir(resolved, func(p string, e os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			entries++
			if entries > 100000 || len(roots) > 50000 {
				return fmt.Errorf("software identity inventory exceeds entry limits")
			}
			if e.IsDir() {
				if e.Name() == ".git" || e.Name() == "__pycache__" {
					return filepath.SkipDir
				}
				return nil
			}
			if e.Type()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(p)
				if err != nil {
					return err
				}
				snapshot.Links[p] = target
				roots = append(roots, target)
				return nil
			}
			if !e.Type().IsRegular() {
				return fmt.Errorf("nonregular entry in software identity: %s", p)
			}
			if _, ok := snapshot.Sources[p]; ok {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			if len(snapshot.Sources) >= 50000 || total > 4<<30 || len(roots) > 50000 {
				return fmt.Errorf("software identity inventory exceeds file/byte limits")
			}
			digest, err := hashSoftwareFile(p)
			if err != nil {
				return err
			}
			snapshot.Sources[p] = digest
			return nil
		})
		if err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}

func hashSoftwareFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || before.Size() > 4<<30 {
		return "", fmt.Errorf("invalid software identity file")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, before.Size()+1))
	if err != nil {
		return "", err
	}
	after, err := f.Stat()
	if err != nil || n != before.Size() || after.Size() != before.Size() || after.ModTime() != before.ModTime() {
		return "", fmt.Errorf("software changed while fingerprinting")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func resolveIdentityHelper(program, name string, env []string) (string, error) {
	if p, ok := bundledUnpacker(runtime.GOOS, filepath.Dir(program), name); ok {
		return filepath.Abs(p)
	}
	return resolveIdentityPATH(name, env)
}

func resolveIdentityPATH(name string, env []string) (string, error) {
	var path, extensions string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(key, "PATH") {
			path = value
		}
		if strings.EqualFold(key, "PATHEXT") {
			extensions = value
		}
	}
	suffixes := []string{""}
	if runtime.GOOS == "windows" {
		if extensions == "" {
			extensions = ".COM;.EXE;.BAT;.CMD"
		}
		suffixes = append(suffixes, strings.Split(strings.ToLower(extensions), ";")...)
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		for _, ext := range suffixes {
			candidate := filepath.Join(dir, name+ext)
			info, err := os.Stat(candidate)
			if err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode()&0111 != 0) {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("helper %s unavailable in effective environment", name)
}
