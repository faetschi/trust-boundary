//go:build linux

package main

import (
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
