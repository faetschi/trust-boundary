//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fakeCgroupRmdir has the kernel's rmdir semantics for a fake cgroup tree: the control files vanish
// with the directory, a child directory makes it busy.
func fakeCgroupRmdir(parentFD int, name string) error {
	fd, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), name)
	entries, err := dir.ReadDir(-1)
	if err != nil {
		_ = dir.Close()
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			_ = dir.Close()
			return syscall.EBUSY
		}
	}
	for _, entry := range entries {
		if err := syscall.Unlinkat(int(dir.Fd()), entry.Name()); err != nil {
			_ = dir.Close()
			return err
		}
	}
	_ = dir.Close()
	return unlinkatDir(parentFD, name)
}

func writeFakeCgroup(t *testing.T, dir, procs, populated string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"cgroup.procs": procs, "cgroup.events": "populated " + populated + "\nfrozen 0\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func withFakeCgroupRmdir(t *testing.T) {
	t.Helper()
	original, originalWait := cgroupRemoveDir, cgroupRemoveBusyWait
	cgroupRemoveDir, cgroupRemoveBusyWait = fakeCgroupRmdir, 300*time.Millisecond
	t.Cleanup(func() { cgroupRemoveDir, cgroupRemoveBusyWait = original, originalWait })
}

func openParent(t *testing.T, path string) *os.File {
	t.Helper()
	parent, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	return parent
}

// podman's `--rm` cleanup removes the container's libpod-<id> child cgroup asynchronously, so a
// settled scope can still hold empty descendants when the host removes it.
func TestSettledScopeWithEmptyDescendantCgroupsIsRemoved(t *testing.T) {
	withFakeCgroupRmdir(t)
	parent := t.TempDir()
	scope := filepath.Join(parent, "pi-wf-abc-e1")
	writeFakeCgroup(t, scope, "", "0")
	writeFakeCgroup(t, filepath.Join(scope, "libpod-0123"), "", "0")
	writeFakeCgroup(t, filepath.Join(scope, "libpod-0123", "container"), "", "0")
	writeFakeCgroup(t, filepath.Join(scope, "libpod-4567"), "", "0")
	if err := removeSettledCgroup(context.Background(), int(openParent(t, parent).Fd()), "pi-wf-abc-e1"); err != nil {
		t.Fatalf("a settled scope with empty descendants was not removed: %v", err)
	}
	if _, err := os.Lstat(scope); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scope still exists: %v", err)
	}
}

func TestPopulatedDescendantIsNeverRemoved(t *testing.T) {
	for name, files := range map[string][2]string{
		"process listed": {"4242\n", "0"},
		"populated flag": {"", "1"},
	} {
		withFakeCgroupRmdir(t)
		parent := t.TempDir()
		scope := filepath.Join(parent, "scope")
		writeFakeCgroup(t, scope, "", "0")
		writeFakeCgroup(t, filepath.Join(scope, "libpod-live"), files[0], files[1])
		if err := removeSettledCgroup(context.Background(), int(openParent(t, parent).Fd()), "scope"); !errors.Is(err, ErrCgroupScopeRefused) {
			t.Fatalf("%s: removal of a populated descendant = %v, want refusal", name, err)
		}
		if _, err := os.Lstat(filepath.Join(scope, "libpod-live")); err != nil {
			t.Fatalf("%s: the populated cgroup was removed: %v", name, err)
		}
	}
	withFakeCgroupRmdir(t)
	parent := t.TempDir()
	scope := filepath.Join(parent, "scope")
	writeFakeCgroup(t, scope, "", "0")
	if err := os.MkdirAll(filepath.Join(scope, "libpod-unknown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeSettledCgroup(context.Background(), int(openParent(t, parent).Fd()), "scope"); err == nil {
		t.Fatal("a descendant that reports no cgroup state was removed")
	}
}

func TestSymlinkInsideTheScopeIsNotFollowed(t *testing.T) {
	withFakeCgroupRmdir(t)
	outside := filepath.Join(t.TempDir(), "outside")
	writeFakeCgroup(t, outside, "", "0")
	parent := t.TempDir()
	scope := filepath.Join(parent, "scope")
	writeFakeCgroup(t, scope, "", "0")
	if err := os.Symlink(outside, filepath.Join(scope, "link")); err != nil {
		t.Fatal(err)
	}
	_ = removeSettledCgroup(context.Background(), int(openParent(t, parent).Fd()), "scope")
	if _, err := os.Lstat(filepath.Join(outside, "cgroup.procs")); err != nil {
		t.Fatalf("the symlink target outside the scope was touched: %v", err)
	}
}

func TestScopeRemovalRetriesABriefEBUSYAndGivesUpWithinItsBound(t *testing.T) {
	withFakeCgroupRmdir(t)
	parent := t.TempDir()
	writeFakeCgroup(t, filepath.Join(parent, "scope"), "", "0")
	busy := 2
	cgroupRemoveDir = func(parentFD int, name string) error {
		if busy > 0 {
			busy--
			return syscall.EBUSY
		}
		return fakeCgroupRmdir(parentFD, name)
	}
	if err := removeSettledCgroup(context.Background(), int(openParent(t, parent).Fd()), "scope"); err != nil {
		t.Fatalf("a brief EBUSY was not retried: %v", err)
	}
	writeFakeCgroup(t, filepath.Join(parent, "stuck"), "", "0")
	cgroupRemoveDir = func(int, string) error { return syscall.EBUSY }
	started := time.Now()
	if err := removeSettledCgroup(context.Background(), int(openParent(t, parent).Fd()), "stuck"); !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("a permanent EBUSY returned %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the EBUSY retry ran %v, longer than its bound", elapsed)
	}
}

func TestScopeRemovalRefusesBadNames(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"", ".", "..", "a/b"} {
		if err := removeSettledCgroup(context.Background(), int(openParent(t, parent).Fd()), name); !errors.Is(err, ErrCgroupScopeRefused) {
			t.Fatalf("name %q = %v", name, err)
		}
	}
}
