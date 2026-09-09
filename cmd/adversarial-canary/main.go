// adversarial-canary is an inert command-execution detector for isolated runs.
package main

import (
	"fmt"
	"os"
)

func main() {
	path := os.Getenv("ADVERSARIAL_EXECUTION_CANARY")
	if path == "" {
		path = "/scratch/execution.canary"
	}
	if err := recordExecution(path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func recordExecution(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString("adversarial command canary executed\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
