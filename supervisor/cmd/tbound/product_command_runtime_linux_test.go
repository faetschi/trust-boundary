//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

// freshCommandRuntime is a test root whose systemd runtime directory exists with the unit's mode, as systemd leaves it.
func freshCommandRuntime(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runtime := filepath.Join(root, productsetup.RuntimeDir)
	if err := os.MkdirAll(runtime, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtime, 0o750); err != nil {
		t.Fatal(err)
	}
	return root
}

// The Podman directories are created private and owned by the service account, and a rerun changes nothing. A symlink,
// a loosened mode or another owner is refused, and the refusal leaves the damaged directory as it was.
func TestPrepareProductCommandRuntimeCreatesPrivateDirectoriesAndRefusesDrift(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	commandDirs := []string{productsetup.PodmanTmpDir, productsetup.PodmanCacheDir, productsetup.PodmanRunrootDir}

	t.Run("creates each directory private and is idempotent", func(t *testing.T) {
		root := freshCommandRuntime(t)
		for range 2 {
			if err := prepareProductCommandRuntime(root, uid, gid); err != nil {
				t.Fatalf("prepare: %v", err)
			}
		}
		for _, dir := range commandDirs {
			info, err := os.Lstat(filepath.Join(root, dir))
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("%s is not a private directory: %v %v", dir, info, err)
			}
		}
	})

	t.Run("refuses a symlink in place of a directory", func(t *testing.T) {
		root := freshCommandRuntime(t)
		if err := prepareProductCommandRuntime(root, uid, gid); err != nil {
			t.Fatal(err)
		}
		cache := filepath.Join(root, productsetup.PodmanCacheDir)
		if err := os.RemoveAll(cache); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), cache); err != nil {
			t.Fatal(err)
		}
		if err := prepareProductCommandRuntime(root, uid, gid); err == nil {
			t.Fatal("a symlink in place of the cache directory was accepted")
		}
	})

	t.Run("refuses a loosened directory without repairing it", func(t *testing.T) {
		root := freshCommandRuntime(t)
		if err := prepareProductCommandRuntime(root, uid, gid); err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(root, productsetup.PodmanTmpDir)
		if err := os.Chmod(tmp, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := prepareProductCommandRuntime(root, uid, gid); err == nil {
			t.Fatal("a loosened tmp directory was accepted")
		}
		if info, err := os.Lstat(tmp); err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("the refusal repaired tmp: %v %v", info, err)
		}
	})

	t.Run("refuses another service owner", func(t *testing.T) {
		root := freshCommandRuntime(t)
		if err := prepareProductCommandRuntime(root, uid+1, gid); err == nil {
			t.Fatal("directories owned by another account were accepted")
		}
	})

	t.Run("refuses a missing runtime directory", func(t *testing.T) {
		if err := prepareProductCommandRuntime(t.TempDir(), uid, gid); err == nil {
			t.Fatal("a missing runtime directory was accepted")
		}
	})
}
