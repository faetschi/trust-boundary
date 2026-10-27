//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

// The marker is built with a format verb so that this test binary holds no well-formed one of its own.
const markerLine = "tbound-product-installation-format=%d;"

// candidateFormat asks the real seam for the format of a file.
func candidateFormat(t *testing.T, path string) (int, error) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return productsetup.ScanInstallationFormat(f)
}

func TestProductAdminReadsACandidateFormatWithoutExecutingIt(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "executed")
	for name, tc := range map[string]struct {
		marker string
		want   int
	}{
		"a marker of the current format": {fmt.Sprintf(markerLine, productsetup.InstallationFormat), productsetup.InstallationFormat},
		"a marker of the legacy format":  {fmt.Sprintf(markerLine, productsetup.LegacyInstallationFormat), productsetup.LegacyInstallationFormat},
		"no marker is the legacy format": {"", productsetup.LegacyInstallationFormat},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+sentinel+"'\n# "+tc.marker+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got, err := candidateFormat(t, path); err != nil || got != tc.want {
			t.Errorf("%s: format %d, %v, want %d", name, got, err, tc.want)
		}
		if _, err := os.Lstat(sentinel); err == nil {
			t.Fatalf("%s: the candidate was executed to learn its format", name)
		}
	}
}

func TestProductAdminBinaryCarriesItsInstallationFormat(t *testing.T) {
	if got, err := candidateFormat(t, os.Args[0]); err != nil || got != productsetup.InstallationFormat {
		t.Fatalf("this binary declares format %d, %v, want %d: the linker dropped the marker, or the marker and InstallationFormat differ", got, err, productsetup.InstallationFormat)
	}
}

func TestProductAdminChecksCandidateFormatsOnTheRealRootOnly(t *testing.T) {
	if productAdminEnv("/", false, false, false, io.Discard, io.Discard).BinaryFormat == nil {
		t.Fatal("update and rollback on the real root would not check the installation format")
	}
	if productAdminEnv(t.TempDir(), false, false, false, io.Discard, io.Discard).BinaryFormat != nil {
		t.Fatal("a test root holds fixture binaries without the marker, so it must not check them")
	}
}
