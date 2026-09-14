//go:build windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "adversarial-supervise requires Linux bubblewrap isolation")
	os.Exit(1)
}
