package benchmark

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RedactedSecret stands in for a credential in any record the harness writes.
const RedactedSecret = "<redacted>"

// RedactSecret removes a credential from a record before it is written or
// hashed. Hashing the redacted form keeps a rendered configuration's digest
// the same whichever account it ran under, so two rigs measuring one provider
// with different logins still record identical configurations.
func RedactSecret(content []byte, secret string) []byte {
	if secret == "" {
		return content
	}
	return bytes.ReplaceAll(content, []byte(secret), []byte(RedactedSecret))
}

// scrubFileLimit bounds the files ScrubSecret opens. Client configuration,
// state databases and logs are far smaller; anything larger is payload.
const scrubFileLimit = 256 << 20

// minimumScrubbedSecret keeps a trivially short password from turning the
// scrub into a rewrite of every file that happens to contain those bytes.
const minimumScrubbedSecret = 4

// ScrubSecret overwrites every occurrence of a credential in the files under
// root, skipping the directory named skip. A client writes the provider
// password into its configuration, its state database and sometimes its log,
// and those files are kept as run evidence; for a real provider they must not
// keep the password. Each occurrence is overwritten in place with the same
// number of bytes, so a database or length-prefixed file keeps its layout and
// stays readable for diagnosis.
func ScrubSecret(root, skip, secret string) error {
	if len(secret) < minimumScrubbedSecret {
		return fmt.Errorf("refusing to scrub a secret shorter than %d bytes", minimumScrubbedSecret)
	}
	needle := []byte(secret)
	mask := bytes.Repeat([]byte{'*'}, len(needle))
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skip != "" && filepath.Clean(path) == filepath.Clean(skip) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > scrubFileLimit {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(contents, needle) {
			return nil
		}
		scrubbed := bytes.ReplaceAll(contents, needle, mask)
		if err := os.WriteFile(path, scrubbed, info.Mode().Perm()); err != nil {
			return fmt.Errorf("scrub the provider password from %s: %w", path, err)
		}
		return nil
	})
}
