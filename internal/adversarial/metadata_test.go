package adversarial

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeclaredMetadataIsByteVerifiedNotIgnored(t *testing.T) {
	root := t.TempDir()
	payload, marker := []byte("payload"), []byte("directory-bound metadata")
	for name, data := range map[string][]byte{"payload.bin": payload, ".marker": marker} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	expected := map[string]string{"payload.bin": Digest(payload)}
	metadata := map[string]string{".marker": Digest(marker)}
	if err := VerifyOutputWithMetadata(root, expected, metadata, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".marker"), []byte("forged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOutputWithMetadata(root, expected, metadata, DefaultLimits()); err == nil {
		t.Fatal("metadata contents ignored")
	}
	if err := VerifyOutputWithMetadata(root, expected, map[string]string{"payload.bin": Digest(marker)}, DefaultLimits()); err == nil {
		t.Fatal("metadata shadowed payload")
	}
}
