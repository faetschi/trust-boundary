//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/trialfix"
)

func netSinkEnoentHost(t *testing.T, cells string, checkFS func(*os.File) error) trialHost {
	t.Helper()
	return trialHost{
		openCells: func(evidenceCell) (*os.File, error) { return os.Open(cells) },
		checkFS:   checkFS,
		owner:     trialfix.Owner{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())},
	}
}

// ENOENT means "the old net-sink directory is absent" only for the top-level open of that directory.
// A nested cgroup that vanishes while the tree is removed is a refusal, not an absent directory.
func TestNetSinkParentRefusesANestedENOENTButCreatesAnAbsentOne(t *testing.T) {
	t.Run("absent directory is created", func(t *testing.T) {
		cells := t.TempDir()
		parent, err := netSinkEnoentHost(t, cells, func(*os.File) error { return nil }).ensureNetSinkParent(evidenceCell{})
		if err != nil {
			t.Fatalf("an absent net-sink directory was refused: %v", err)
		}
		parent.Close()
		if info, err := os.Stat(filepath.Join(cells, piruntime.D09NetSinkCgroupName)); err != nil || !info.IsDir() {
			t.Fatalf("net-sink directory not created: %v", err)
		}
	})
	t.Run("nested child vanishes", func(t *testing.T) {
		cells := t.TempDir()
		sink := filepath.Join(cells, piruntime.D09NetSinkCgroupName)
		for _, child := range []string{"a", "b"} {
			if err := os.MkdirAll(filepath.Join(sink, child), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// Checking the first nested directory removes its sibling, so the sibling's open fails with ENOENT.
		checks := 0
		check := func(dir *os.File) error {
			checks++
			if checks == 2 {
				other := "a"
				if filepath.Base(dir.Name()) == "a" {
					other = "b"
				}
				return os.Remove(filepath.Join(sink, other))
			}
			return nil
		}
		parent, err := netSinkEnoentHost(t, cells, check).ensureNetSinkParent(evidenceCell{})
		if err == nil {
			parent.Close()
			t.Fatal("a nested ENOENT passed as an absent net-sink directory")
		}
		if checks != 2 {
			t.Fatalf("checkFS ran %d times, want the top and the first nested directory", checks)
		}
	})
}
