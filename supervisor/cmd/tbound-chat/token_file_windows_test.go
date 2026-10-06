//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPairingMetadataRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing with spaces")
	if err := os.WriteFile(path, []byte("existing user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnerOnlyTokenFile(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("existing metadata was overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing user data" {
		t.Fatal("existing destination changed")
	}
}

func TestPairingMetadataCreatesProtectedNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new pairing with spaces")
	if err := writeOwnerOnlyTokenFile(path, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 65 {
		t.Fatal("new protected metadata file was not completely written")
	}
}
