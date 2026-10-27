//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

func TestProductAdminAsksACandidateBinaryForItsInstallationFormat(t *testing.T) {
	if got := strings.TrimSpace(mustAdmin(t, "product", "installation-format")); got != strconv.Itoa(productsetup.InstallationFormat) {
		t.Fatalf("`product installation-format` printed %q, want %d", got, productsetup.InstallationFormat)
	}
	dir := t.TempDir()
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	current := script("current", "exec env TBOUND_ADMIN_HELPER=1 '"+os.Args[0]+"' -test.run='^TestProductAdminHelperProcess$' -- \"$@\"")
	if got, err := productAdminBinaryFormat(current); err != nil || got != productsetup.InstallationFormat {
		t.Fatalf("a current binary reported format %d, %v", got, err)
	}
	if got, err := productAdminBinaryFormat(script("legacy", "echo 'invalid product command or flags' >&2; exit 2")); err != nil || got != productsetup.LegacyInstallationFormat {
		t.Fatalf("a binary that does not know the command is the legacy format, got %d, %v", got, err)
	}
	if _, err := productAdminBinaryFormat(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a binary that cannot start must not be taken for the legacy format")
	}
	if _, err := productAdminBinaryFormat(script("garbage", "echo not-a-number")); err == nil {
		t.Fatal("an unreadable format answer was accepted")
	}
}

func TestProductAdminChecksCandidateFormatsOnTheRealRootOnly(t *testing.T) {
	if productAdminEnv("/", false, false, false, io.Discard, io.Discard).BinaryFormat == nil {
		t.Fatal("update and rollback on the real root would not check the installation format")
	}
	if productAdminEnv(t.TempDir(), false, false, false, io.Discard, io.Discard).BinaryFormat != nil {
		t.Fatal("a test root cannot run its fixture binaries, so it must not check them")
	}
}

// The marker is built with a format verb so that this test binary holds no well-formed one of its own.
const markerLine = "tbound-product-installation-format=%d;"

// candidateFormat asks the real seam for the format of a file.
func candidateFormat(t *testing.T, path string) (int, error) {
	t.Helper()
	return productAdminBinaryFormat(path)
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
