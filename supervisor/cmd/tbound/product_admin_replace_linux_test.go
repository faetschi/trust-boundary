//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

// replacementConfig signs a second registration over the same projects (another registration ID, so
// another digest) with the same key, and returns a setup config that installs it.
func (h *adminHost) replacementConfig() string {
	h.t.Helper()
	template := filepath.Join(h.dir, "template-2.json")
	args := adminInitArgs(template)
	args[slices.Index(args, "--registration-id")+1] = "reg-2"
	mustAdmin(h.t, args...)
	reg := filepath.Join(h.dir, "registration-2.json")
	mustAdmin(h.t, "product", "registration", "sign", "--registration", template, "--key", filepath.Join(h.keys, "registration.key"), "--out", reg)
	cfg := h.setupConfig()
	cfg.Registration, cfg.RegistrationSignature = reg, reg+".sig"
	data, err := json.Marshal(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	file := filepath.Join(h.dir, "setup-2.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		h.t.Fatal(err)
	}
	return file
}

// settleTurns abandons every unresolved turn of the installed catalog, as the service's restart
// reconciliation would, under the installed registration.
func (h *adminHost) settleTurns() {
	h.t.Helper()
	reg, err := productsession.LoadRegistration(h.path(productsetup.RegistrationPath), h.path(productsetup.RegistrationSignaturePath), h.path(productsetup.RegistrationPublicKeyPath))
	if err != nil {
		h.t.Fatal(err)
	}
	catalog, err := productsession.Open(h.path(productsetup.CatalogPath), reg)
	if err != nil {
		h.t.Fatal(err)
	}
	defer catalog.Close()
	for _, turn := range catalog.UnresolvedTurns() {
		if err := catalog.AbandonUnlinkedTurn(turn.ID); err != nil {
			h.t.Fatal(err)
		}
	}
}

func TestProductAdminSetupReplaceRefusesANewRegistrationWhileATurnIsUnresolved(t *testing.T) {
	root := privateGovernedTestRoot(t)
	h := newAdminHost(t, root)
	if out, err := h.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	adminCatalog(t, h, true)
	config := h.replacementConfig()
	installed, err := os.ReadFile(h.path(productsetup.RegistrationPath))
	if err != nil {
		t.Fatal(err)
	}
	before := adminTree(t, root)

	for _, extra := range [][]string{{"--replace"}, {"--replace", "--dry-run"}} {
		out, err := adminRun(append([]string{"product", "setup", "--config", config, "--root", root, "--binary", h.binary}, extra...)...)
		if adminExit(err) != 2 || !strings.Contains(err.Error(), "/turn-1 (admitted)") || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("setup %v replaced the registration under an unresolved turn, or did not name it: %v\n%s", extra, err, out)
		}
		if after := adminTree(t, root); after != before {
			t.Fatalf("setup %v changed the root:\nbefore:\n%s\nafter:\n%s", extra, before, after)
		}
	}
	if now, _ := os.ReadFile(h.path(productsetup.RegistrationPath)); !bytes.Equal(now, installed) {
		t.Fatal("the installed registration changed")
	}

	if out, err := h.setup("--replace"); err != nil {
		t.Fatalf("a replace that keeps the registration digest must not be fenced: %v\n%s", err, out)
	}

	h.settleTurns()
	if out, err := adminRun("product", "setup", "--config", config, "--root", root, "--binary", h.binary, "--replace"); err != nil {
		t.Fatalf("the replacement was refused once no turn was unsettled: %v\n%s", err, out)
	}
	if now, _ := os.ReadFile(h.path(productsetup.RegistrationPath)); bytes.Equal(now, installed) {
		t.Fatal("the registration was not replaced")
	}
	reg, err := productsession.LoadRegistration(h.path(productsetup.RegistrationPath), h.path(productsetup.RegistrationSignaturePath), h.path(productsetup.RegistrationPublicKeyPath))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := productsession.Open(h.path(productsetup.CatalogPath), reg)
	if err != nil {
		t.Fatalf("the service could not start on the replaced registration: %v", err)
	}
	_ = catalog.Close()
}
