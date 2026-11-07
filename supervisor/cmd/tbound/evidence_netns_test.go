package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The host network namespace link is read from pid 1 and must be net:[inode].
func TestNetnsInodeInReadsPidOnesLink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "1", "ns")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "net")
	if err := os.Symlink("net:[4026531840]", link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if ino, err := netnsInodeIn(root); err != nil || ino != 4026531840 {
		t.Fatalf("ino=%d err=%v", ino, err)
	}
	_ = os.Remove(link)
	for _, bad := range []string{"net:[0]", "mnt:[4026531840]", "net:[x]", "net:[1"} {
		if err := os.Symlink(bad, link); err != nil {
			t.Skip(err)
		}
		if _, err := netnsInodeIn(root); err == nil {
			t.Errorf("%q accepted", bad)
		}
		_ = os.Remove(link)
	}
	if _, err := netnsInodeIn(t.TempDir()); err == nil {
		t.Error("an empty /proc was read")
	}
}
