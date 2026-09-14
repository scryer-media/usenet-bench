package adversarial

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Run in every normal test sweep. This checks tracked files rather than
// trusting an ignore rule; disguised archives in fixture paths are rejected.
func TestRepositoryContainsOnlyGenerableFixtures(t *testing.T) {
	root := filepath.Join("..", "..")
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("cannot verify tracked fixture policy: %v", err)
	}
	for _, name := range strings.Split(string(raw), "\x00") {
		if name == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		ext := strings.ToLower(filepath.Ext(name))
		for _, forbidden := range []string{".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".zst", ".par2", ".nzb.gz"} {
			if ext == forbidden {
				t.Errorf("checked-in archive/parity artifact: %s", name)
			}
		}
		for _, magic := range [][]byte{[]byte("Rar!\x1a\x07"), {'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}, {'P', 'K', 3, 4}, {0x1f, 0x8b, 8}, []byte("PAR2\x00PKT")} {
			if bytes.Contains(data, magic) {
				t.Errorf("embedded archive/parity bytes in %s", name)
			}
		}
		if strings.HasPrefix(name, "fixtures/") && (!utf8.Valid(data) || bytes.ContainsRune(data, 0)) {
			t.Errorf("non-text fixture checked in: %s", name)
		}
	}
}
