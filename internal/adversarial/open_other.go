//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package adversarial

import "os"

// These platforms require a stopped, isolated process tree before inspection.
func openEvidence(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
