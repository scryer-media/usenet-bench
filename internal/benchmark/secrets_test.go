package benchmark

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactSecretReplacesEveryOccurrence(t *testing.T) {
	got := string(RedactSecret([]byte("password = hunter22\nagain hunter22"), "hunter22"))
	if strings.Contains(got, "hunter22") || strings.Count(got, RedactedSecret) != 2 {
		t.Fatalf("redacted = %q", got)
	}
	if got := string(RedactSecret([]byte("unchanged"), "")); got != "unchanged" {
		t.Fatalf("an empty secret changed the record: %q", got)
	}
}

func TestScrubSecretOverwritesInPlaceAndSkipsDownloads(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config", "sabnzbd.ini")
	download := filepath.Join(root, "downloads", "payload.bin")
	for _, path := range []string{config, download} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("before hunter22 after"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := ScrubSecret(root, filepath.Join(root, "downloads"), "hunter22"); err != nil {
		t.Fatal(err)
	}
	scrubbed, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(scrubbed) != "before ******** after" {
		t.Fatalf("scrubbed config = %q, want a same-length mask", scrubbed)
	}
	info, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("scrub changed the file mode to %o", info.Mode().Perm())
	}
	untouched, err := os.ReadFile(download)
	if err != nil {
		t.Fatal(err)
	}
	if string(untouched) != "before hunter22 after" {
		t.Fatalf("scrub rewrote a download: %q", untouched)
	}
	if err := ScrubSecret(root, "", "abc"); err == nil {
		t.Fatal("a three-byte secret should be refused")
	}
}
