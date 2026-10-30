//go:build linux

package effectoracle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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

// rootTap is an inotify watch of the test's own on root: it keeps every event, so a canary that was created and removed
// within microseconds still shows.
type rootTap struct{ fd int }

func newRootTap(t *testing.T, root string) *rootTap {
	t.Helper()
	requireInotify(t)
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Skipf("inotify is unavailable here (%v)", err)
	}
	mask := uint32(syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MODIFY | syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_TO |
		syscall.IN_MOVED_FROM | syscall.IN_ATTRIB)
	if _, err := syscall.InotifyAddWatch(fd, root, mask); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	return &rootTap{fd: fd}
}

// count returns the number of events since the last call.
func (p *rootTap) count() int {
	n, buf := 0, make([]byte, 64<<10)
	for {
		got, err := syscall.Read(p.fd, buf)
		if err != nil || got <= 0 {
			return n
		}
		recs, _ := parseEvents(buf[:got])
		n += len(recs)
	}
}

// countdownCtx is done after Err has been called n times.
type countdownCtx struct {
	context.Context
	n atomic.Int32
}

func (c *countdownCtx) Err() error {
	if c.n.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

// A start that is called off before it begins writes nothing into the root: no canary, no event.
func TestStartContextAlreadyDoneWritesNothing(t *testing.T) {
	root := t.TempDir()
	tap := newRootTap(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o, err := StartContext(ctx, root)
	if !errors.Is(err, context.Canceled) || o != nil {
		t.Fatalf("StartContext = %v, %v; want context.Canceled and no observer", o, err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := tap.count(); n != 0 {
		t.Fatalf("the cancelled start wrote %d events into the root", n)
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Fatalf("the root holds %v", left)
	}
}

// A start that is called off between its two canaries writes no further canary: the first one is whole (written and
// removed again), the second is never written, and no observer is returned.
func TestStartContextCancelledBetweenCanariesWritesNoFurtherCanary(t *testing.T) {
	root := t.TempDir()
	tap := newRootTap(t, root)
	// Err is asked once at the entry, then before each canary: the first canary may go, the second may not.
	ctx := &countdownCtx{Context: context.Background()}
	ctx.n.Store(2)
	o, err := StartContext(ctx, root)
	if !errors.Is(err, context.Canceled) || o != nil {
		t.Fatalf("StartContext = %v, %v; want context.Canceled and no observer", o, err)
	}
	time.Sleep(100 * time.Millisecond)
	// One canary: create, modify, close_write, delete.
	if n := tap.count(); n != len(canaryExpect) {
		t.Fatalf("the cancelled start wrote %d events, want exactly the first canary's %d", n, len(canaryExpect))
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Fatalf("the root holds %v", left)
	}
}

// A ctx that is done after the start leaves the observer alone: Stop still writes its canaries.
func TestStartContextOnceStartedIgnoresTheContext(t *testing.T) {
	requireInotify(t)
	ctx, cancel := context.WithCancel(context.Background())
	o, err := StartContext(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	rep, err := o.Stop()
	if err != nil || !rep.QualifiedWindow() {
		t.Fatalf("Stop after the start ctx was cancelled: %v, %+v", err, rep)
	}
}
