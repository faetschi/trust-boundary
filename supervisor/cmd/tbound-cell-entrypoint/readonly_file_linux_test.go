//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The positive case (a file on a read-only mount) needs a ro mount; it is exercised by the guest's command cell.
func TestReadOnlyFileRefusesAFileOnAWritableMount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bash")
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := requireReadOnlyFile(f); err == nil {
		t.Fatal("a file on a writable mount was accepted")
	}
}
