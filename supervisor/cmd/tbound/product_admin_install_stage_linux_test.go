//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProductAdminInstallScriptAsRootKeepsItsStageOutOfTMPDIRAndConsumesNothingSubstitutedThere(t *testing.T) {
	skipUnlessInstallScriptRuns(t)
	f := newReleaseFixture(t)
	genuine, err := os.ReadFile(f.tarGz)
	if err != nil {
		t.Fatal(err)
	}
	f.writeArchive("malicious")
	malicious := filepath.Join(f.dir, "malicious.tar.gz")
	if err := os.Rename(f.tarGz, malicious); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.tarGz, genuine, 0o644); err != nil {
		t.Fatal(err)
	}
	userTmp := filepath.Join(f.dir, "usertmp")
	if err := os.Mkdir(userTmp, 0o755); err != nil {
		t.Fatal(err)
	}
	// TMPDIR sits below a directory another user owns. Once the release was verified, that user renames the
	// stage and puts a directory of their own, holding a different archive and a copy of the public key, at its path.
	swap := `export TMPDIR='` + userTmp + `'; eval "$(declare -f verify_signature | sed '1s/verify_signature/real_verify_signature/')"; ` +
		`verify_signature() { real_verify_signature "$@"; for s in "$TMPDIR"/tbound-install.*; do [ -d "$s" ] || continue; mv "$s" "$s.moved"; mkdir "$s"; cp '` + malicious + `' "$s/$2"; cp "$s.moved/.tbound-publisher.pub" "$s/"; done; }`
	f.asRoot, f.shellSetup = true, everythingIsRootOwned+"; "+swap
	prefix := filepath.Join(f.dir, "home", "opt", "tbound", "prefix") // its parent does not exist yet

	out, _, err := f.install("--public-key", f.pub, "--verifier", f.verifier(), "--prefix", prefix)
	if err != nil {
		t.Fatalf("a signed root install failed: %v\n%s", err, out)
	}
	if got := installedMarker(t, prefix); got != "genuine" {
		t.Fatalf("a root install extracted %q, the archive that replaced its stage after the signature was verified\n%s", got, out)
	}
	if entries, _ := os.ReadDir(userTmp); len(entries) != 0 {
		t.Fatalf("a root install put its stage below TMPDIR: %v", entries)
	}
}
