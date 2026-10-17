//go:build linux

package piruntime

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The fake service cgroup is a temp directory carrying the regular files that
// cgroupfs would expose. cgroupfs fabricates the control files on mkdir and
// forces mode 0755, so the mkdirat seam emulates both.
type fakeServiceCgroup struct {
	dir    string
	parent *os.File
	uid    int
	mkdirs []uint32
}

func writeFakeControlFiles(t *testing.T, dir string, withSubtree bool) {
	t.Helper()
	files := map[string]string{"cgroup.procs": "", "cgroup.controllers": "cpu memory pids\n"}
	if withSubtree {
		files["cgroup.subtree_control"] = ""
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newFakeServiceCgroup(t *testing.T, cellsHasSubtreeControl bool) *fakeServiceCgroup {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeControlFiles(t, dir, true)
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	fake := &fakeServiceCgroup{dir: dir, parent: parent, uid: os.Geteuid()}
	original := cellsCgroupMkdirat
	cellsCgroupMkdirat = func(fd int, name string, mode uint32) error {
		fake.mkdirs = append(fake.mkdirs, mode)
		if err := syscall.Mkdirat(fd, name, mode); err != nil {
			return err
		}
		cells := filepath.Join(dir, name)
		if err := os.Chmod(cells, 0o755); err != nil {
			return err
		}
		writeFakeControlFiles(t, cells, cellsHasSubtreeControl)
		return nil
	}
	t.Cleanup(func() { cellsCgroupMkdirat = original })
	return fake
}

func (f *fakeServiceCgroup) cells() string { return filepath.Join(f.dir, "cells") }

func (f *fakeServiceCgroup) precreateCells(t *testing.T, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(f.cells(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.cells(), mode); err != nil {
		t.Fatal(err)
	}
	writeFakeControlFiles(t, f.cells(), true)
}

func readFake(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func requireControllersEnabled(t *testing.T, path string) {
	t.Helper()
	got := readFake(t, path)
	for _, name := range []string{"cpu", "memory", "pids"} {
		if !hasCgroupController([]byte(got), name) {
			t.Fatalf("%s lacks %s: %q", path, name, got)
		}
	}
}

func TestEnsureCellsCgroupCreatesAbsentCells(t *testing.T) {
	fake := newFakeServiceCgroup(t, true)
	if err := ensureCellsCgroup(fake.parent, fake.uid); err != nil {
		t.Fatalf("ensureCellsCgroup: %v", err)
	}
	if len(fake.mkdirs) != 1 || fake.mkdirs[0] != 0o755 {
		t.Fatalf("mkdirat calls = %o, want exactly one 0755", fake.mkdirs)
	}
	info, err := os.Lstat(fake.cells())
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o755 {
		t.Fatalf("cells not a 0755 directory: %v %v", info, err)
	}
	requireControllersEnabled(t, filepath.Join(fake.dir, "cgroup.subtree_control"))
	requireControllersEnabled(t, filepath.Join(fake.cells(), "cgroup.subtree_control"))
}

func TestEnsureCellsCgroupAcceptsExistingValidCells(t *testing.T) {
	fake := newFakeServiceCgroup(t, true)
	fake.precreateCells(t, 0o755)
	if err := ensureCellsCgroup(fake.parent, fake.uid); err != nil {
		t.Fatalf("ensureCellsCgroup: %v", err)
	}
	requireControllersEnabled(t, filepath.Join(fake.cells(), "cgroup.subtree_control"))
}

func TestEnsureCellsCgroupRefusesSymlinkCells(t *testing.T) {
	fake := newFakeServiceCgroup(t, true)
	target := t.TempDir()
	writeFakeControlFiles(t, target, true)
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, fake.cells()); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := ensureCellsCgroup(fake.parent, fake.uid); err == nil {
		t.Fatal("symlink cells was admitted")
	}
	if got := readFake(t, filepath.Join(target, "cgroup.subtree_control")); got != "" {
		t.Fatalf("controllers were written through the symlink: %q", got)
	}
}

func TestEnsureCellsCgroupRefusesFileCells(t *testing.T) {
	fake := newFakeServiceCgroup(t, true)
	if err := os.WriteFile(fake.cells(), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureCellsCgroup(fake.parent, fake.uid); err == nil {
		t.Fatal("regular-file cells was admitted")
	}
}

func TestEnsureCellsCgroupRefusesWrongModeOwnerOrDevice(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o775, 0o777, 0o750} {
		fake := newFakeServiceCgroup(t, true)
		fake.precreateCells(t, mode)
		if err := ensureCellsCgroup(fake.parent, fake.uid); err == nil {
			t.Fatalf("cells mode %o was admitted", mode)
		}
		if got := readFake(t, filepath.Join(fake.cells(), "cgroup.subtree_control")); got != "" {
			t.Fatalf("controllers written into refused cells %o: %q", mode, got)
		}
	}
	fake := newFakeServiceCgroup(t, true)
	if err := ensureCellsCgroup(fake.parent, fake.uid+1); err == nil {
		t.Fatal("parent owned by a different uid than the service uid was admitted")
	}
	if _, err := os.Lstat(fake.cells()); err == nil {
		t.Fatal("cells created under a wrong-owner parent")
	}
	good := &syscall.Stat_t{Uid: 991, Dev: 7, Mode: syscall.S_IFDIR | 0o755}
	if err := validateCellsDirectoryStat(good, 991, 7); err != nil {
		t.Fatalf("valid stat refused: %v", err)
	}
	for name, mutate := range map[string]func(*syscall.Stat_t){
		"owner":    func(s *syscall.Stat_t) { s.Uid = 0 },
		"mode":     func(s *syscall.Stat_t) { s.Mode = syscall.S_IFDIR | 0o700 },
		"setgid":   func(s *syscall.Stat_t) { s.Mode |= syscall.S_ISGID },
		"device":   func(s *syscall.Stat_t) { s.Dev = 8 },
		"not-dir":  func(s *syscall.Stat_t) { s.Mode = syscall.S_IFREG | 0o755 },
		"symlinkt": func(s *syscall.Stat_t) { s.Mode = syscall.S_IFLNK | 0o755 },
	} {
		stat := *good
		mutate(&stat)
		if err := validateCellsDirectoryStat(&stat, 991, 7); err == nil {
			t.Fatalf("%s mismatch was admitted", name)
		}
	}
}

func TestEnsureCellsCgroupRefusesParentWithProcesses(t *testing.T) {
	fake := newFakeServiceCgroup(t, true)
	if err := os.WriteFile(filepath.Join(fake.dir, "cgroup.procs"), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCellsCgroup(fake.parent, fake.uid); err == nil {
		t.Fatal("service parent holding a process was admitted")
	}
	if _, err := os.Lstat(fake.cells()); err == nil {
		t.Fatal("cells was created under a populated parent")
	}
	if got := readFake(t, filepath.Join(fake.dir, "cgroup.subtree_control")); got != "" {
		t.Fatalf("controllers were enabled on a populated parent: %q", got)
	}
}

func TestEnsureCellsCgroupRefusesCellsWithProcesses(t *testing.T) {
	fake := newFakeServiceCgroup(t, true)
	fake.precreateCells(t, 0o755)
	if err := os.WriteFile(filepath.Join(fake.cells(), "cgroup.procs"), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCellsCgroup(fake.parent, fake.uid); err == nil {
		t.Fatal("populated cells was admitted")
	}
}

func TestEnsureCellsCgroupRefusesWhenControllerEnablementFails(t *testing.T) {
	t.Run("cells subtree_control unavailable", func(t *testing.T) {
		fake := newFakeServiceCgroup(t, false)
		err := ensureCellsCgroup(fake.parent, fake.uid)
		if err == nil || !strings.Contains(err.Error(), "controller") {
			t.Fatalf("missing cells subtree_control was admitted or mis-reported: %v", err)
		}
	})
	t.Run("parent lacks delegated memory", func(t *testing.T) {
		fake := newFakeServiceCgroup(t, true)
		if err := os.WriteFile(filepath.Join(fake.dir, "cgroup.controllers"), []byte("cpu pids\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureCellsCgroup(fake.parent, fake.uid); err == nil {
			t.Fatal("parent without the memory controller was admitted")
		}
		if _, err := os.Lstat(fake.cells()); err == nil {
			t.Fatal("cells was created although the parent cannot delegate controllers")
		}
	})
	t.Run("parent subtree_control unavailable", func(t *testing.T) {
		fake := newFakeServiceCgroup(t, true)
		if err := os.Remove(filepath.Join(fake.dir, "cgroup.subtree_control")); err != nil {
			t.Fatal(err)
		}
		if err := ensureCellsCgroup(fake.parent, fake.uid); err == nil {
			t.Fatal("unreadable parent subtree_control was admitted")
		}
	})
}
