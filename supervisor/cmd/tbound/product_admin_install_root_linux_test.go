//go:build linux

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func skipUnlessInstallScriptRuns(t *testing.T) {
	t.Helper()
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("install.sh supports amd64 and arm64")
	}
}

func writeFileOrFatal(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProductAdminInstallScriptAsRootNeverWritesThroughAnArchiveSuppliedMarker(t *testing.T) {
	skipUnlessInstallScriptRuns(t)
	f := newReleaseFixture(t)
	victim := filepath.Join(f.dir, "victim")
	writeFileOrFatal(t, victim, "precious\n")
	f.symlinks = map[string]string{".tbound-install": victim}
	f.writeArchive("malicious")
	f.publishSums()
	f.asRoot, f.shellSetup = true, everythingIsRootOwned

	out, prefix, err := f.install()
	if got, _ := os.ReadFile(victim); string(got) != "precious\n" {
		t.Fatalf("a checksum-only root install wrote through the archive's marker link: %q\n%s", got, out)
	}
	if err != nil {
		t.Fatalf("the install must discard the archive's marker and go on: %v\n%s", err, out)
	}
	info, statErr := os.Lstat(filepath.Join(prefix, ".tbound-install"))
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		t.Fatalf("the installed marker is not a fresh regular 0644 file: %v %v", info, statErr)
	}
	if got, _ := os.ReadFile(filepath.Join(prefix, ".tbound-install")); !strings.HasPrefix(string(got), "name=tbound\n") {
		t.Fatalf("the installed marker is not the installer's: %q", got)
	}
}
