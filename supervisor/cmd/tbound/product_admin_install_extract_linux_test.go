//go:build linux

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordCalls wraps a command of install.sh in a shell function that appends its arguments to log.
func recordCalls(command, log string) string {
	return command + `() { printf '%s\n' "$*" >> '` + log + `'; command ` + command + ` "$@"; }`
}

func TestProductAdminInstallScriptAsRootTakesNoOwnerOrModeFromTheArchive(t *testing.T) {
	skipUnlessInstallScriptRuns(t)
	f := newReleaseFixture(t)
	f.loose = true
	f.writeArchive("loose")
	f.resign(f.keys)
	tarLog := filepath.Join(f.dir, "tar.log")
	// The umask is open, so only the installer's own handling keeps the archive's 0666 and 0777 out of the prefix.
	f.asRoot, f.shellSetup = true, everythingIsRootOwned+"; umask 000; "+recordCalls("tar", tarLog)

	out, prefix, err := f.install("--public-key", f.pub, "--verifier", f.verifier())
	if err != nil {
		t.Fatalf("a root install of an archive with loose headers failed: %v\n%s", err, out)
	}
	var loose []string
	if walkErr := filepath.WalkDir(prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if info, err := d.Info(); err == nil && info.Mode()&fs.ModeSymlink == 0 && info.Mode().Perm()&0o022 != 0 {
			loose = append(loose, path+" "+info.Mode().String())
		}
		return nil
	}); walkErr != nil {
		t.Fatal(walkErr)
	}
	if len(loose) != 0 {
		t.Fatalf("a root install kept modes that group or others can write from the archive: %v", loose)
	}
	calls, _ := os.ReadFile(tarLog)
	if !strings.Contains(string(calls), "--no-same-owner") || !strings.Contains(string(calls), "--no-same-permissions") {
		t.Fatalf("a root extraction must ignore the archive's owners and modes, tar ran as: %q", calls)
	}
}

func TestProductAdminInstallScriptAsRootCreatesTheMarkerAndThePinWithTheirFinalMode(t *testing.T) {
	skipUnlessInstallScriptRuns(t)
	f := newReleaseFixture(t)
	chmodLog := filepath.Join(f.dir, "chmod.log")
	f.asRoot, f.shellSetup = true, everythingIsRootOwned+"; umask 077; "+recordCalls("chmod", chmodLog)
	fingerprint := strings.TrimSpace(mustAdmin(t, "release", "fingerprint", "--public-key", f.pub))

	out, prefix, err := f.install("--public-key", f.pub, "--verifier", f.verifier(), "--key-fingerprint", fingerprint)
	if err != nil {
		t.Fatalf("a signed root install failed: %v\n%s", err, out)
	}
	for _, name := range []string{".tbound-install", ".tbound-publisher.pub"} {
		info, statErr := os.Lstat(filepath.Join(prefix, name))
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
			t.Fatalf("%s is not a regular 0644 file: %v %v", name, info, statErr)
		}
	}
	// A chmod by pathname follows a symlink that replaced the file after it was created.
	calls, _ := os.ReadFile(chmodLog)
	if strings.Contains(string(calls), ".tbound-install") || strings.Contains(string(calls), ".tbound-publisher.pub") {
		t.Fatalf("install.sh changed the mode of the marker or the pin by pathname:\n%s", calls)
	}
}
