//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
