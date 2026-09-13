package generator

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/scryer-media/usenet-bench/internal/fixture"
)

type ToolchainLock struct {
	SchemaVersion int         `json:"schema_version"`
	Toolchains    []Toolchain `json:"toolchains"`
}

// Toolchain is a source-locked RARLAB command-line release. The hash is
// verified in Dockerfile before any executable is installed.
type Toolchain struct {
	ID       string `json:"id"`
	Image    string `json:"image"`
	Platform string `json:"platform"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Binary   string `json:"binary"`
}

// SevenZipToolchain pins the official 7-Zip Linux console release used to
// write the 7z fixture lane. Distribution p7zip forks are deliberately not
// used: they are a different codebase with a different container writer.
type SevenZipToolchain struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Image         string `json:"image"`
	Platform      string `json:"platform"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256"`
	Binary        string `json:"binary"`
	Version       string `json:"version"`
}

// PAR2Toolchain pins the open-source parity generator used only while
// materializing repair fixtures. It is not a benchmarked client dependency.
type PAR2Toolchain struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Image         string `json:"image"`
	Platform      string `json:"platform"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256"`
}

// PAR3Toolchain pins the official PAR3 reference implementation, par3cmdline,
// used only while materializing PAR3 repair fixtures. The project publishes no
// release tarballs, so the pin is one source revision and the digest of the
// archive GitHub serves for it.
type PAR3Toolchain struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Image         string `json:"image"`
	Platform      string `json:"platform"`
	URL           string `json:"url"`
	SHA256        string `json:"sha256"`
	Revision      string `json:"revision"`
}

func LoadPAR3Toolchain(path string) (PAR3Toolchain, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return PAR3Toolchain{}, fmt.Errorf("read PAR3 toolchain %s: %w", path, err)
	}
	var toolchain PAR3Toolchain
	if err := json.Unmarshal(contents, &toolchain); err != nil {
		return PAR3Toolchain{}, fmt.Errorf("decode PAR3 toolchain %s: %w", path, err)
	}
	if err := toolchain.Validate(); err != nil {
		return PAR3Toolchain{}, err
	}
	return toolchain, nil
}

func (t PAR3Toolchain) Validate() error {
	if t.SchemaVersion != 1 {
		return fmt.Errorf("PAR3 toolchain %q has unsupported schema version %d", t.ID, t.SchemaVersion)
	}
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Image) == "" || strings.TrimSpace(t.Platform) == "" {
		return fmt.Errorf("PAR3 toolchain must include id, image, and platform")
	}
	if len(t.Revision) != 40 || strings.Trim(t.Revision, "0123456789abcdef") != "" {
		return fmt.Errorf("PAR3 toolchain %q must pin a full lowercase source revision", t.ID)
	}
	parsed, err := url.Parse(t.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("PAR3 toolchain %q must use an https URL", t.ID)
	}
	if !strings.Contains(t.URL, t.Revision) {
		return fmt.Errorf("PAR3 toolchain %q downloads %s, which does not name revision %s", t.ID, t.URL, t.Revision)
	}
	if len(t.SHA256) != 64 || strings.Trim(t.SHA256, "0123456789abcdefABCDEF") != "" {
		return fmt.Errorf("PAR3 toolchain %q has invalid SHA-256", t.ID)
	}
	return nil
}

func (t PAR3Toolchain) ManifestID() fixture.ToolchainID {
	return fixture.ToolchainID{
		ID:       t.ID,
		Image:    t.Image,
		URL:      t.URL,
		SHA256:   t.SHA256,
		Platform: t.Platform,
		Binary:   "par3",
		Version:  t.Revision,
	}
}

func LoadToolchainLock(path string) (ToolchainLock, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return ToolchainLock{}, fmt.Errorf("read toolchain lock %s: %w", path, err)
	}
	var lock ToolchainLock
	if err := json.Unmarshal(contents, &lock); err != nil {
		return ToolchainLock{}, fmt.Errorf("decode toolchain lock %s: %w", path, err)
	}
	if lock.SchemaVersion != 2 || len(lock.Toolchains) == 0 {
		return ToolchainLock{}, fmt.Errorf("toolchain lock %s is empty or has unsupported schema", path)
	}
	ids := map[string]bool{}
	for _, toolchain := range lock.Toolchains {
		if err := toolchain.Validate(); err != nil {
			return ToolchainLock{}, err
		}
		if ids[toolchain.ID] {
			return ToolchainLock{}, fmt.Errorf("toolchain lock has duplicate id %q", toolchain.ID)
		}
		ids[toolchain.ID] = true
	}
	return lock, nil
}

func LoadPAR2Toolchain(path string) (PAR2Toolchain, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return PAR2Toolchain{}, fmt.Errorf("read PAR2 toolchain %s: %w", path, err)
	}
	var toolchain PAR2Toolchain
	if err := json.Unmarshal(contents, &toolchain); err != nil {
		return PAR2Toolchain{}, fmt.Errorf("decode PAR2 toolchain %s: %w", path, err)
	}
	if err := toolchain.Validate(); err != nil {
		return PAR2Toolchain{}, err
	}
	return toolchain, nil
}

func LoadSevenZipToolchain(path string) (SevenZipToolchain, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return SevenZipToolchain{}, fmt.Errorf("read 7-Zip toolchain %s: %w", path, err)
	}
	var toolchain SevenZipToolchain
	if err := json.Unmarshal(contents, &toolchain); err != nil {
		return SevenZipToolchain{}, fmt.Errorf("decode 7-Zip toolchain %s: %w", path, err)
	}
	if err := toolchain.Validate(); err != nil {
		return SevenZipToolchain{}, err
	}
	return toolchain, nil
}

func (t SevenZipToolchain) Validate() error {
	if t.SchemaVersion != 1 {
		return fmt.Errorf("7-Zip toolchain %q has unsupported schema version %d", t.ID, t.SchemaVersion)
	}
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Image) == "" || strings.TrimSpace(t.Platform) == "" {
		return fmt.Errorf("7-Zip toolchain must include id, image, and platform")
	}
	if strings.TrimSpace(t.Version) == "" {
		return fmt.Errorf("7-Zip toolchain %q must declare the upstream version it installs", t.ID)
	}
	binary := strings.TrimSpace(t.Binary)
	if binary == "" || binary == "." || binary == ".." || strings.ContainsAny(binary, "/\\") {
		return fmt.Errorf("7-Zip toolchain %q has invalid binary %q", t.ID, t.Binary)
	}
	parsed, err := url.Parse(t.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("7-Zip toolchain %q must use an https URL", t.ID)
	}
	if len(t.SHA256) != 64 || strings.Trim(t.SHA256, "0123456789abcdefABCDEF") != "" {
		return fmt.Errorf("7-Zip toolchain %q has invalid SHA-256", t.ID)
	}
	return nil
}

func (t SevenZipToolchain) ManifestID() fixture.ToolchainID {
	return fixture.ToolchainID{
		ID:       t.ID,
		Image:    t.Image,
		URL:      t.URL,
		SHA256:   t.SHA256,
		Platform: t.Platform,
		Binary:   t.Binary,
		Version:  t.Version,
	}
}

func (t PAR2Toolchain) Validate() error {
	if t.SchemaVersion != 1 {
		return fmt.Errorf("PAR2 toolchain %q has unsupported schema version %d", t.ID, t.SchemaVersion)
	}
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Image) == "" || strings.TrimSpace(t.Platform) == "" {
		return fmt.Errorf("PAR2 toolchain must include id, image, and platform")
	}
	parsed, err := url.Parse(t.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("PAR2 toolchain %q must use an https URL", t.ID)
	}
	if len(t.SHA256) != 64 || strings.Trim(t.SHA256, "0123456789abcdefABCDEF") != "" {
		return fmt.Errorf("PAR2 toolchain %q has invalid SHA-256", t.ID)
	}
	return nil
}

func (t Toolchain) Validate() error {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Image) == "" || strings.TrimSpace(t.Platform) == "" {
		return fmt.Errorf("toolchain must include id, image, and platform")
	}
	binary := strings.TrimSpace(t.Binary)
	if binary == "" || binary == "." || binary == ".." || strings.ContainsAny(binary, "/\\") {
		return fmt.Errorf("toolchain %q has invalid archive binary %q", t.ID, t.Binary)
	}
	parsed, err := url.Parse(t.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("toolchain %q must use an https URL", t.ID)
	}
	if len(t.SHA256) != 64 || strings.Trim(t.SHA256, "0123456789abcdefABCDEF") != "" {
		return fmt.Errorf("toolchain %q has invalid SHA-256", t.ID)
	}
	return nil
}

func (l ToolchainLock) Find(id string) (Toolchain, bool) {
	for _, toolchain := range l.Toolchains {
		if toolchain.ID == id {
			return toolchain, true
		}
	}
	return Toolchain{}, false
}

func (t Toolchain) ManifestID() fixture.ToolchainID {
	return fixture.ToolchainID{
		ID:       t.ID,
		Image:    t.Image,
		URL:      t.URL,
		SHA256:   t.SHA256,
		Platform: t.Platform,
		Binary:   t.Binary,
	}
}
