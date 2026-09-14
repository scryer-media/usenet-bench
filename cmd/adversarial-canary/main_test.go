package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordExecutionIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution.canary")
	if err := recordExecution(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "adversarial command canary executed\n" {
		t.Fatal(string(raw), err)
	}
	if err := recordExecution(path); err == nil {
		t.Fatal("second execution overwrote the first canary")
	}
}
