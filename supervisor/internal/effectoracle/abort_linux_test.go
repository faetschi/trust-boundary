//go:build linux

package effectoracle

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// Abort ends the observation without writing anything into the root: no stop canary, no stop snapshot, and no report.
func TestAbortWritesNothingIntoTheRootAndGivesNoReport(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := Start(root)
	if err != nil {
		t.Fatal(err)
	}
	var canaries atomic.Int32
	o.canaryHook = func(string) { canaries.Add(1) }
	o.Abort()
	o.Abort() // idempotent
	if canaries.Load() != 0 {
		t.Fatalf("Abort wrote %d canaries", canaries.Load())
	}
	if left, _ := os.ReadDir(root); len(left) != 1 {
		t.Fatalf("the root holds %v after Abort", left)
	}
	if rep, err := o.Stop(); !errors.Is(err, ErrStopped) || rep != nil {
		t.Fatalf("Stop after Abort = %v, %v; want ErrStopped and no report", rep, err)
	}
	if err := o.Mark("late"); !errors.Is(err, ErrStopped) {
		t.Fatalf("Mark after Abort = %v", err)
	}
}

// Abort after Stop leaves the report as it was.
func TestAbortAfterStopKeepsTheReport(t *testing.T) {
	requireInotify(t)
	o, err := Start(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := o.Stop()
	if err != nil {
		t.Fatal(err)
	}
	o.Abort()
	if again, err := o.Stop(); err != nil || again != rep {
		t.Fatalf("Stop after Abort-after-Stop = %v, %v", again, err)
	}
}
