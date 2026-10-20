//go:build linux

package piruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnableControllersOnServiceCgroupEnablesExactlyCPUMemoryPids(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"cgroup.procs": "", "cgroup.controllers": "cpuset cpu io memory pids\n", "cgroup.subtree_control": ""}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	unit, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unit.Close()
	if err := enableControllersOnServiceCgroup(unit, os.Geteuid()+1); err == nil {
		t.Fatal("a cgroup owned by another uid was written")
	}
	if got := readFake(t, filepath.Join(dir, "cgroup.subtree_control")); got != "" {
		t.Fatalf("controllers were written to a cgroup of another owner: %q", got)
	}
	if err := enableControllersOnServiceCgroup(unit, os.Geteuid()); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got := readFake(t, filepath.Join(dir, "cgroup.subtree_control"))
	if got != "+cpu +memory +pids" {
		t.Fatalf("subtree_control = %q, want exactly +cpu +memory +pids", got)
	}
	if err := enableControllersOnServiceCgroup(unit, os.Geteuid()); err != nil {
		t.Fatalf("second enable (already enabled) failed: %v", err)
	}
}

func TestEnableControllersOnServiceCgroupRefusesAMissingControllerOrAProcess(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"controller not delegated": {"cgroup.procs": "", "cgroup.controllers": "cpu memory\n", "cgroup.subtree_control": ""},
		"process in the parent":    {"cgroup.procs": "42\n", "cgroup.controllers": "cpu memory pids\n", "cgroup.subtree_control": ""},
	} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for file, content := range files {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		unit, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		err = enableControllersOnServiceCgroup(unit, os.Geteuid())
		_ = unit.Close()
		if err == nil || readFake(t, filepath.Join(dir, "cgroup.subtree_control")) != "" {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
}

// With Delegate=yes systemd chowns the unit cgroup to the service user, so the
// runner leaf must open relative to the unit cgroup instead of through a walk
// that demands root-owned ancestors.
func TestRunnerLeafOpensBelowAServiceOwnedUnitCgroup(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, D09ManagedPiRunnerCgroupLeaf), 0o755); err != nil {
		t.Fatal(err)
	}
	unit, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unit.Close()
	leaf, err := openManagedRunnerLeafBelow(unit, os.Geteuid())
	if err != nil {
		t.Fatalf("runner leaf below a service-owned unit cgroup was refused: %v", err)
	}
	_ = leaf.Close()
	if _, err := openManagedRunnerLeafBelow(unit, os.Geteuid()+1); err == nil {
		t.Fatal("runner leaf opened below a unit cgroup owned by another uid")
	}
	if err := os.Chmod(dir, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := openManagedRunnerLeafBelow(unit, os.Geteuid()); err == nil {
		t.Fatal("runner leaf opened below a group-writable unit cgroup")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, D09ManagedPiRunnerCgroupLeaf)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(dir, D09ManagedPiRunnerCgroupLeaf)); err != nil {
		t.Fatal(err)
	}
	if _, err := openManagedRunnerLeafBelow(unit, os.Geteuid()); err == nil {
		t.Fatal("a symlinked runner leaf was followed")
	}
}
