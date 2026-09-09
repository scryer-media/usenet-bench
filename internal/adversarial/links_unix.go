//go:build unix

package adversarial

import (
	"fmt"
	"os"
	"syscall"
)

func hasMultipleLinks(f *os.File) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("link count unavailable")
	}
	return stat.Nlink > 1, nil
}
