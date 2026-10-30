//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

// setupWithoutKey writes a setup config that names no release_public_key.
func (h *adminHost) setupWithoutKey() string {
	h.t.Helper()
	cfg := h.setupConfig()
	cfg.ReleasePublicKey = ""
	data, err := json.Marshal(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	file := filepath.Join(h.dir, "setup-without-key.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		h.t.Fatal(err)
	}
	return file
}

func TestProductAdminSetupTakesAnExplicitBinaryOnlyWhenTheReleaseKeyAuthenticatesIt(t *testing.T) {
	root := privateGovernedTestRoot(t)
	h := newAdminHost(t, root)
	installed := func() bool { _, err := os.Lstat(h.path(productsetup.BinaryPath)); return err == nil }
	for _, extra := range [][]string{nil, {"--dry-run"}} {
		out, err := adminRun(append([]string{"product", "setup", "--config", h.config(false), "--root", root, "--binary", h.binary}, extra...)...)
		if adminExit(err) != 2 || !strings.Contains(err.Error(), "--manifest") || installed() {
			t.Fatalf("an unsigned --binary was not refused with %v: %v\n%s", extra, err, out)
		}
	}
	forged := filepath.Join(h.dir, "forged-keys")
	mustAdmin(t, "release", "keygen", "--out", forged)
	manifest := filepath.Join(t.TempDir(), "tbound-release-manifest.json")
	mustAdmin(t, "release", "manifest", "--out", manifest, h.binary)
	mustAdmin(t, "release", "sign", "--manifest", manifest, "--key", filepath.Join(forged, "release-signing.key"), "--out", manifest+".sig")
	if out, err := adminRun("product", "setup", "--config", h.config(false), "--root", root, "--binary", h.binary, "--manifest", manifest, "--signature", manifest+".sig"); adminExit(err) != 2 || installed() {
		t.Fatalf("a --binary signed by another publisher was not refused: %v\n%s", err, out)
	}
	if out, err := h.setup(); err != nil || !installed() {
		t.Fatalf("a --binary signed by the configured publisher was refused: %v\n%s", err, out)
	}
}

func TestProductAdminSetupWithoutAReleaseKeyInstallsOnlyItsOwnExecutable(t *testing.T) {
	root := privateGovernedTestRoot(t)
	h := newAdminHost(t, root)
	config := h.setupWithoutKey()
	out, err := adminRun("product", "setup", "--config", config, "--root", root, "--binary", h.binary)
	if adminExit(err) != 2 || !strings.Contains(err.Error(), "release_public_key") {
		t.Fatalf("a foreign --binary without a release key was not refused with the way out: %v\n%s", err, out)
	}
	if _, err := os.Lstat(h.path(productsetup.BinaryPath)); err == nil {
		t.Fatal("a refused setup installed the binary")
	}
	if out, err := adminRun("product", "setup", "--config", config, "--root", root); err != nil {
		t.Fatalf("setup refused to install its own executable: %v\n%s", err, out)
	}
	if _, err := os.Lstat(h.path(productsetup.BinaryPath)); err != nil {
		t.Fatalf("setup did not install its own executable: %v", err)
	}
}
