//go:build linux

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"tbound/supervisor/internal/guestevidence"
	"testing"
	"time"
)

func TestConstructFIFORejectedWithoutWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for name, read := range map[string]func() error{
		"run-config": func() error { _, err := readConstructConfig(path); return err },
		"evidence":   func() error { _, err := readConstructEvidence(constructFS{root}, "fifo"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- read() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("FIFO accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("FIFO read blocked")
			}
		})
	}
}

func readConstructEvidence(set fs.FS, name string) ([]byte, error) {
	return readConstructFile(set, name, guestevidence.MaxBytes)
}
