//go:build windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "sabnzbd-case-client is only used by the Linux supervised runner")
	os.Exit(1)
}
