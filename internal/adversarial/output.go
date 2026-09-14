package adversarial

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"time"
)

// OutputSafetyError is a forbidden side effect, not a compatibility mismatch.
type OutputSafetyError struct{ Reason string }

func (e *OutputSafetyError) Error() string { return e.Reason }
func unsafeOutput(format string, args ...any) error {
	return &OutputSafetyError{fmt.Sprintf(format, args...)}
}

// boundedWalk reads directory entries in batches. WalkDir/ReadDir(path) load
// an entire attacker-controlled directory before a callback can enforce a cap.
func boundedWalk(root *os.Root, limits Limits, visit func(string, fs.DirEntry, error) error) error {
	if limits.Entries < 0 || limits.Entries > DefaultLimits().Entries || limits.WrittenBytes < 0 || limits.WrittenBytes > DefaultLimits().WrittenBytes {
		return fmt.Errorf("invalid output inspection limits")
	}
	deadline := time.Now().Add(10 * time.Second)
	var count int64
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > 64 {
			return unsafeOutput("output depth budget exceeded")
		}
		f, err := root.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			if time.Now().After(deadline) {
				return fmt.Errorf("output inspection deadline exceeded")
			}
			entries, readErr := f.ReadDir(64)
			for _, entry := range entries {
				count++
				if count > limits.Entries {
					return unsafeOutput("output entry budget exceeded")
				}
				name := path.Join(dir, entry.Name())
				if err := visit(name, entry, nil); err != nil {
					return err
				}
				if entry.IsDir() {
					if err := walk(name, depth+1); err != nil {
						return err
					}
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	return walk(".", 0)
}

// InspectOutput applies safety postconditions even when byte acceptance is
// policy-dependent. Rejection requires an empty final output tree.
func InspectOutput(dir string, limits Limits, rejected bool) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	var bytes int64
	return boundedWalk(root, limits, func(name string, d fs.DirEntry, _ error) error {
		if rejected {
			return unsafeOutput("rejected submission left committed output: %q", name)
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return unsafeOutput("output contains link or special entry: %q", name)
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return unsafeOutput("output restored privileged metadata: %q", name)
		}
		if info.IsDir() {
			return nil
		}
		f, err := openEvidence(root, name)
		if err != nil {
			return err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil {
			return err
		}
		if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return unsafeOutput("output changed during inspection: %q", name)
		}
		linked, err := hasMultipleLinks(f)
		if err != nil {
			return err
		}
		if linked {
			return unsafeOutput("output contains hardlink: %q", name)
		}
		if opened.Size() < 0 || opened.Size() > limits.WrittenBytes-bytes {
			return unsafeOutput("output byte budget exceeded")
		}
		bytes += opened.Size()
		return nil
	})
}
