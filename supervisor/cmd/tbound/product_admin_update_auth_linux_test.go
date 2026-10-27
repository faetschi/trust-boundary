//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

func TestProductAdminUpdateTakesOnlyACandidateTheSetupPublisherKeyAuthenticates(t *testing.T) {
	root := privateGovernedTestRoot(t)
	h := newAdminHost(t, root)
	if out, err := h.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	read := func(logical string) string { data, _ := os.ReadFile(h.path(logical)); return string(data) }
	pinned, err := os.ReadFile(filepath.Join(h.releaseKeys, "release-signing.pub"))
	if err != nil || read(productsetup.ReleasePublicKeyPath) != string(pinned) {
		t.Fatalf("setup did not pin the config's publisher key: %v", err)
	}

	next := filepath.Join(h.dir, "tbound-v2")
	if err := os.WriteFile(next, []byte("binary v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, signature := h.release(next)
	update := func(args ...string) (string, error) {
		return adminRun(append([]string{"product", "update", "--root", root}, args...)...)
	}
	for name, args := range map[string][]string{
		"no manifest":  {"--binary", next, "--signature", signature},
		"no signature": {"--binary", next, "--manifest", manifest},
	} {
		if _, err := update(args...); adminExit(err) != 2 {
			t.Errorf("%s: want a usage refusal, got %v", name, err)
		}
	}

	// A publisher other than the pinned one signs the same bytes.
	foreign := filepath.Join(h.dir, "foreign-keys")
	mustAdmin(t, "release", "keygen", "--out", foreign)
	forged := filepath.Join(t.TempDir(), "tbound-release-manifest.json")
	mustAdmin(t, "release", "manifest", "--out", forged, next)
	mustAdmin(t, "release", "sign", "--manifest", forged, "--key", filepath.Join(foreign, "release-signing.key"), "--out", forged+".sig")
	out, err := update("--binary", next, "--manifest", forged, "--signature", forged+".sig")
	if adminExit(err) != 2 || !strings.Contains(err.Error(), "pinned publisher key") || strings.Contains(out, "systemctl") || read(productsetup.BinaryPath) != "binary v1" {
		t.Fatalf("a candidate signed by another publisher was not refused before any service command: %v\n%s", err, out)
	}

	if out, err := update("--binary", next, "--manifest", manifest, "--signature", signature); err != nil || read(productsetup.BinaryPath) != "binary v2" {
		t.Fatalf("a candidate signed by the pinned publisher was refused: %v\n%s", err, out)
	}
	if out, _ := adminRun("product", "doctor", "--root", root); !strings.Contains(out, "[PASS] release key") || !strings.Contains(out, "fingerprint") { // the test root has no listening socket, so doctor exits non-zero
		t.Fatalf("doctor does not show the pinned publisher key:\n%s", out)
	}
}
