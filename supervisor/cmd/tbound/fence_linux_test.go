//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/wsoracle"
)

// TestTrialResetRefusesADigestOfAnotherDirectory: the digest after a reset must be the digest of
// the registered inode the reset held, not of whatever is at the path.
func TestTrialResetRefusesADigestOfAnotherDirectory(t *testing.T) {
	e := newTrialTestEnv(t)
	e.snapshotDirFn = func(dir *os.File, label string) wsoracle.Manifest {
		m := wsoracle.SnapshotDir(dir, label)
		if m.Root != nil {
			m.Root.Ino++
		}
		return m
	}
	_, _, err := e.run(t, "reset", trialTestCase)
	if err == nil || !strings.Contains(err.Error(), "not the registered live root") {
		t.Fatalf("reset accepted a digest of another directory: %v", err)
	}
}

// TestTrialRootIdentityRefusesADecoyAtTheRegisteredPath: the pending check reports the
// registration's device and inode and refuses when the path no longer is that directory.
func TestTrialRootIdentityRefusesADecoyAtTheRegisteredPath(t *testing.T) {
	root := t.TempDir()
	var st syscall.Stat_t
	if err := syscall.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	reg := piruntime.D09ManagedLiveRootRegistration{RootPath: root, RootDevice: uint64(st.Dev), RootInode: st.Ino}
	id, err := trialRootIdentity(reg, root)
	if err != nil || id.Device != uint64(st.Dev) || id.Inode != st.Ino {
		t.Fatalf("trialRootIdentity = %+v, %v; want the registration's identity", id, err)
	}
	decoy := filepath.Join(filepath.Dir(root), "decoy")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(decoy, root); err != nil {
		t.Fatal(err)
	}
	if _, err := trialRootIdentity(reg, root); err == nil || !strings.Contains(err.Error(), "not the registered live root") {
		t.Fatalf("trialRootIdentity accepted a decoy: %v", err)
	}
	// The tree that really is the registered inode is still the registered root under its new name,
	// and the identity reported is the registration's.
	if id, err := trialRootIdentity(reg, root+"-old"); err != nil || id.Inode != st.Ino {
		t.Fatalf("trialRootIdentity of the real root under its new name: %+v, %v", id, err)
	}
}
