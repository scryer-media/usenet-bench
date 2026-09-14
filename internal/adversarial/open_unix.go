//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package adversarial

import (
	"os"
	"syscall"
)

// Nonblocking prevents a regular-file-to-FIFO swap from hanging the observer.
// Root confines traversal; NOFOLLOW rejects replacement links at the leaf.
func openEvidence(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
