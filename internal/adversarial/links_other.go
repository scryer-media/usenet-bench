//go:build !unix && !windows

package adversarial

import (
	"fmt"
	"os"
)

func hasMultipleLinks(f *os.File) (bool, error) {
	return false, fmt.Errorf("output link verification unavailable on this platform")
}
