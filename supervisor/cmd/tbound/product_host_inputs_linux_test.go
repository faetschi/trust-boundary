//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

// The owner rule of an installed input is the caller's set of allowed UIDs, not the caller's own UID, so
// a root-run setup and a service-run start decide alike.
func TestProductHostInputOwnerRulesTakeTheAllowedUIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	writeHostFile(t, path, "content")
	self := uint32(os.Geteuid())
	other := self + 1
	file, err := readProtectedHostFile(path, 64, []uint32{0, self})
	if err != nil || string(file.Raw) != "content" || file.UID != self || file.Mode != 0o600 {
		t.Fatalf("an input owned by an allowed UID: %+v %v", file, err)
	}
	for name, owners := range map[string][]uint32{"foreign owner set": {0, other}, "empty owner set": nil} {
		if _, err := readProtectedHostFile(path, 64, owners); err == nil || !strings.Contains(err.Error(), "protected bounded regular file") {
			t.Errorf("%s: an input owned by %d was read: %v", name, self, err)
		}
	}
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedHostFile(path, 64, []uint32{self}); err == nil {
		t.Error("a group-writable input was read")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedHostFile(path, 3, []uint32{self}); err == nil {
		t.Error("an input over its bound was read")
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedHostFile(link, 64, []uint32{self}); err == nil {
		t.Error("a symlink was followed")
	}
}

// The shared check holds every file input to the owner set it is given. A process that owns the files but
// is not in the set (a third user running doctor, or a setup that expects root-owned inputs) is refused.
func TestProductHostInputChecksHoldEveryInputToTheOwnerSet(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns the files in the owner set {root}")
	}
	h := newAdminHost(t, privateGovernedTestRoot(t))
	registration, err := productsession.LoadRegistration(h.reg, h.sig, h.pub)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(h.pub)
	if err != nil {
		t.Fatal(err)
	}
	inputs := productsetup.HostInputs{PolicyDigests: registrationSummary(registration).PolicyDigests,
		GatePolicyPaths: map[string]string{"alpha": h.policyFile("alpha"), "beta": h.policyFile("beta")},
		GitPath:         h.gitFile(), GitDigest: h.gitDigest, XattrCalibrationPath: h.calibrationFile(), PublicKey: key, PublicKeyPath: h.pub,
		Owners: []uint32{0, uint32(os.Geteuid())}}
	for _, c := range checkProductHostInputs(inputs) {
		if c.Status != productsetup.Pass {
			t.Fatalf("an input owned by an allowed UID was refused: %+v", c)
		}
	}
	inputs.Owners = []uint32{0}
	failed := map[string]bool{}
	for _, c := range checkProductHostInputs(inputs) {
		failed[c.Name] = c.Status == productsetup.Fail
	}
	for _, name := range adminHostInputNames[:5] {
		if !failed[name] {
			t.Errorf("%s was accepted although its owner is outside the owner set", name)
		}
	}
	if failed["input registration public key"] {
		t.Error("the key is held to its content, not to its file owner")
	}
}
