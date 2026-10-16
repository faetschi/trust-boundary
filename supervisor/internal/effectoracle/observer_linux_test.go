//go:build linux

package effectoracle

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// requireInotify skips the calling test, and says why, when the kernel does
// not provide inotify. A missing facility skips a test; it never fails one.
func requireInotify(t *testing.T) {
	t.Helper()
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		t.Skipf("skipping: inotify is unavailable here (inotify_init1: %v); these tests verify nothing without it", err)
	}
	_ = syscall.Close(fd)
}

// faultyInotify wraps the kernel implementation and injects the faults that a
// real kernel reaches only under load or at limits. One switch controls one
// fault, so each test exercises exactly one failure path.
type faultyInotify struct {
	inner           inotifyAPI
	addErr          func(path string) error
	mute            atomic.Bool // discard kernel records, as a dead instrument would
	prependOverflow atomic.Bool // precede the next batch of records with IN_Q_OVERFLOW
	failRead        atomic.Bool // fail the next read with EIO
}

func (f *faultyInotify) AddWatch(path string, mask uint32) (int, error) {
	if f.addErr != nil {
		if err := f.addErr(path); err != nil {
			return -1, err
		}
	}
	return f.inner.AddWatch(path, mask)
}

func (f *faultyInotify) RmWatch(wd int) error { return f.inner.RmWatch(wd) }

func (f *faultyInotify) Close() error { return f.inner.Close() }

func (f *faultyInotify) Read(p []byte) (int, error) {
	if f.failRead.CompareAndSwap(true, false) {
		return 0, syscall.EIO
	}
	for {
		n, err := f.inner.Read(p[inotifyHeaderSize:])
		if err != nil {
			return 0, err
		}
		if f.mute.Load() {
			continue
		}
		if f.prependOverflow.CompareAndSwap(true, false) {
			putOverflowHeader(p[:inotifyHeaderSize])
			return inotifyHeaderSize + n, nil
		}
		return copy(p, p[inotifyHeaderSize:inotifyHeaderSize+n]), nil
	}
}

// putOverflowHeader writes the record the kernel queues when it drops events:
// watch descriptor -1, IN_Q_OVERFLOW, and no name.
func putOverflowHeader(h []byte) {
	binary.NativeEndian.PutUint32(h[0:4], ^uint32(0))
	binary.NativeEndian.PutUint32(h[4:8], syscall.IN_Q_OVERFLOW)
	binary.NativeEndian.PutUint32(h[8:12], 0)
	binary.NativeEndian.PutUint32(h[12:16], 0)
}

// startWithCleanup starts an observer on root through the public API and stops
// it when the test ends. Stop is idempotent, so an explicit Stop is safe.
func startWithCleanup(t *testing.T, root string) *Observer {
	t.Helper()
	o, err := Start(root)
	if err != nil {
		t.Fatalf("Start(%s): %v", root, err)
	}
	t.Cleanup(func() { _, _ = o.Stop() })
	return o
}

// startInjected starts an observer over an injected kernel surface.
func startInjected(t *testing.T, root string, src inotifyAPI, timeout time.Duration) *Observer {
	t.Helper()
	o, err := startWith(root, src, timeout)
	if err != nil {
		t.Fatalf("startWith(%s): %v", root, err)
	}
	t.Cleanup(func() { _, _ = o.Stop() })
	return o
}

func mustMark(t *testing.T, o *Observer, name string) {
	t.Helper()
	if err := o.Mark(name); err != nil {
		t.Fatalf("Mark(%q): %v", name, err)
	}
}

func mustStop(t *testing.T, o *Observer) *Report {
	t.Helper()
	r, err := o.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	return r
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatalf("rename %s -> %s: %v", from, to, err)
	}
}

func mustRemove(t *testing.T, p string) {
	t.Helper()
	if err := os.Remove(p); err != nil {
		t.Fatalf("remove %s: %v", p, err)
	}
}

func eventsAt(r *Report, kind, path string) []Event {
	var out []Event
	for _, e := range r.Events {
		if e.Kind == kind && e.Path == path {
			out = append(out, e)
		}
	}
	return out
}

func gapsOfKind(r *Report, kind string) []Gap {
	var out []Gap
	for _, g := range r.Gaps {
		if g.Kind == kind {
			out = append(out, g)
		}
	}
	return out
}

func markNamed(t *testing.T, r *Report, name string) Mark {
	t.Helper()
	for _, m := range r.Marks {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("report has no mark %q; marks=%+v", name, r.Marks)
	return Mark{}
}

func entryAt(m Mark, path string) (Entry, bool) {
	for _, e := range m.Entries {
		if e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

func transientsOf(r *Report, kind, path string) []Transient {
	var out []Transient
	for _, tr := range r.TransientMutations() {
		if tr.Kind == kind && tr.Path == path {
			out = append(out, tr)
		}
	}
	return out
}

func unobservedBeats(r *Report) int {
	n := 0
	for _, h := range r.Heartbeats {
		if !h.Observed {
			n++
		}
	}
	return n
}

func TestCreateThenDeleteIsTransientAndInvisibleToEndpoints(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	o := startWithCleanup(t, root)
	p := filepath.Join(root, "f.txt")
	writeFile(t, p, "transient")
	mustRemove(t, p)
	r := mustStop(t, o)

	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok; gaps=%+v heartbeats=%+v", r.Verdict, r.Gaps, r.Heartbeats)
	}
	start, stop := markNamed(t, r, MarkStart), markNamed(t, r, MarkStop)
	if _, ok := entryAt(start, "f.txt"); ok {
		t.Fatal("start mark unexpectedly contains f.txt")
	}
	if _, ok := entryAt(stop, "f.txt"); ok {
		t.Fatal("stop mark unexpectedly contains f.txt, so endpoints would not agree")
	}
	creates := eventsAt(r, KindCreate, "f.txt")
	deletes := eventsAt(r, KindDelete, "f.txt")
	if len(creates) != 1 || len(deletes) != 1 {
		t.Fatalf("creates=%d deletes=%d, want 1 and 1; events=%+v", len(creates), len(deletes), r.Events)
	}
	got := transientsOf(r, TransientCreatedThenDeleted, "f.txt")
	if len(got) != 1 {
		t.Fatalf("created_then_deleted candidates for f.txt = %+v, want exactly one", got)
	}
	if want := []uint64{creates[0].Seq, deletes[0].Seq}; !reflect.DeepEqual(got[0].EventSeqs, want) {
		t.Fatalf("EventSeqs = %v, want %v", got[0].EventSeqs, want)
	}
	if len(got[0].ObservedAtMarks) != 0 {
		t.Fatalf("no mark ran between create and delete, got ObservedAtMarks=%v", got[0].ObservedAtMarks)
	}
}

func TestCreateThenDeleteIsObservedAtAMarkThatRanInBetween(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	o := startWithCleanup(t, root)
	p := filepath.Join(root, "f.txt")
	writeFile(t, p, "present at mid")
	mustMark(t, o, "mid")
	mustRemove(t, p)
	r := mustStop(t, o)

	if _, ok := entryAt(markNamed(t, r, "mid"), "f.txt"); !ok {
		t.Fatal("mid mark should have captured f.txt while it existed")
	}
	got := transientsOf(r, TransientCreatedThenDeleted, "f.txt")
	if len(got) != 1 || !reflect.DeepEqual(got[0].ObservedAtMarks, []string{"mid"}) {
		t.Fatalf("created_then_deleted = %+v, want one candidate observed at mid", got)
	}
}

func TestModifyThenRevertIsTransientAndEndpointsAgree(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	p := filepath.Join(root, "f.txt")
	writeFile(t, p, "A")
	o := startWithCleanup(t, root)
	writeFile(t, p, "B")
	mustMark(t, o, "mid")
	writeFile(t, p, "A")
	r := mustStop(t, o)

	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok; gaps=%+v", r.Verdict, r.Gaps)
	}
	start, stop := markNamed(t, r, MarkStart), markNamed(t, r, MarkStop)
	startEntry, okStart := entryAt(start, "f.txt")
	stopEntry, okStop := entryAt(stop, "f.txt")
	if !okStart || !okStop || startEntry != stopEntry {
		t.Fatalf("endpoint comparison must agree: start=%+v stop=%+v", startEntry, stopEntry)
	}
	midEntry, _ := entryAt(markNamed(t, r, "mid"), "f.txt")
	if midEntry == startEntry {
		t.Fatal("mid mark should have captured the modified content")
	}
	got := transientsOf(r, TransientReverted, "f.txt")
	if len(got) != 1 {
		t.Fatalf("reverted candidates for f.txt = %+v, want exactly one", got)
	}
	if !reflect.DeepEqual(got[0].ObservedAtMarks, []string{"mid"}) {
		t.Fatalf("ObservedAtMarks = %v, want [mid]", got[0].ObservedAtMarks)
	}
	if len(got[0].EventSeqs) == 0 {
		t.Fatal("reverted candidate carries no event sequence numbers")
	}
}

func TestAttributeChangeThatIsRevertedIsTransient(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	p := filepath.Join(root, "g.txt")
	writeFile(t, p, "G")
	o := startWithCleanup(t, root)
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// inotify merges an identical event that is still unread at the queue tail,
	// so a barrier keeps the two IN_ATTRIB events distinct and the count exact.
	mustMark(t, o, "between-chmods")
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatalf("chmod back: %v", err)
	}
	r := mustStop(t, o)

	if e1, e2 := mustEntry(t, r, MarkStart, "g.txt"), mustEntry(t, r, MarkStop, "g.txt"); e1 != e2 {
		t.Fatalf("mode endpoints differ: %+v vs %+v", e1, e2)
	}
	if n := len(eventsAt(r, KindAttrib, "g.txt")); n != 2 {
		t.Fatalf("attrib events = %d, want 2", n)
	}
	if got := transientsOf(r, TransientReverted, "g.txt"); len(got) != 1 {
		t.Fatalf("reverted candidates = %+v, want one", got)
	}
}

func mustEntry(t *testing.T, r *Report, mark, path string) Entry {
	t.Helper()
	e, ok := entryAt(markNamed(t, r, mark), path)
	if !ok {
		t.Fatalf("mark %q has no entry %q", mark, path)
	}
	return e
}

func TestRenameMovesTheEntryWithoutATransientCandidate(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	writeFile(t, a, "moved")
	o := startWithCleanup(t, root)
	mustRename(t, a, filepath.Join(root, "b.txt"))
	r := mustStop(t, o)

	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok", r.Verdict)
	}
	if len(eventsAt(r, KindMovedFrom, "a.txt")) != 1 || len(eventsAt(r, KindMovedTo, "b.txt")) != 1 {
		t.Fatalf("missing moved_from a.txt or moved_to b.txt; events=%+v", r.Events)
	}
	if _, ok := entryAt(markNamed(t, r, MarkStart), "a.txt"); !ok {
		t.Fatal("start mark should contain a.txt")
	}
	if _, ok := entryAt(markNamed(t, r, MarkStop), "b.txt"); !ok {
		t.Fatal("stop mark should contain b.txt")
	}
	if got := r.TransientMutations(); len(got) != 0 {
		t.Fatalf("a rename with retained endpoints must produce no transient, got %+v", got)
	}
}

func TestNewDirectoryRaceIsExplicitAndNestedWatchIsInstalled(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	o := startWithCleanup(t, root)
	d := filepath.Join(root, "d")
	mustMkdir(t, d)
	writeFile(t, filepath.Join(d, "f.txt"), "created immediately")
	mustMkdir(t, filepath.Join(d, "e"))
	writeFile(t, filepath.Join(d, "e", "h.txt"), "nested, created immediately")
	mustMark(t, o, "after-race")
	writeFile(t, filepath.Join(d, "e", "g.txt"), "after the barrier")
	r := mustStop(t, o)

	raced := false
	for _, g := range gapsOfKind(r, GapSubdirRace) {
		if g.Path == "d" {
			raced = true
		}
	}
	if !raced {
		t.Fatalf("no subdir_race gap for d; gaps=%+v", r.Gaps)
	}
	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost after a directory race", r.Verdict)
	}
	if unobservedBeats(r) != 0 {
		t.Fatal("the instrument stayed live through the race, so every canary must be observed")
	}
	mid := markNamed(t, r, "after-race")
	for _, want := range []string{"d", "d/f.txt", "d/e", "d/e/h.txt"} {
		if _, ok := entryAt(mid, want); !ok {
			t.Errorf("retained state: mark after-race lacks %q", want)
		}
	}
	if n := len(eventsAt(r, KindCreate, "d/e/g.txt")); n != 1 {
		t.Fatalf("create under the nested directory after the race: %d events, want 1; events=%+v", n, r.Events)
	}
}

func TestDirectoryRenameRemapsTheSubtreeWithAnExplicitGap(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	d := filepath.Join(root, "d")
	mustMkdir(t, d)
	writeFile(t, filepath.Join(d, "x.txt"), "inside")
	o := startWithCleanup(t, root)
	mustRename(t, d, filepath.Join(root, "e"))
	mustMark(t, o, "after-rename")
	writeFile(t, filepath.Join(root, "e", "y.txt"), "after the rename")
	r := mustStop(t, o)

	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost for a directory move", r.Verdict)
	}
	for _, e := range r.Events {
		if strings.HasPrefix(e.Path, "d/") {
			t.Fatalf("event %+v maps into the old directory after the rename", e)
		}
	}
	if n := len(eventsAt(r, KindCreate, "e/y.txt")); n != 1 {
		t.Fatalf("create under the new name: %d events, want 1; events=%+v", n, r.Events)
	}
	mid := markNamed(t, r, "after-rename")
	if _, ok := entryAt(mid, "e/x.txt"); !ok {
		t.Fatal("mark after-rename lacks e/x.txt")
	}
	if _, ok := entryAt(mid, "d/x.txt"); ok {
		t.Fatal("mark after-rename still contains d/x.txt")
	}
}

func TestCanariesAreFilteredAndReportedAsHeartbeats(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	o := startWithCleanup(t, root)
	mustMark(t, o, "checkpoint")
	r := mustStop(t, o)

	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok", r.Verdict)
	}
	if len(r.Events) != 0 {
		t.Fatalf("canary traffic leaked into the workspace log: %+v", r.Events)
	}
	if len(r.Heartbeats) != 6 {
		t.Fatalf("heartbeats = %d, want 6 (pre and post for start, checkpoint, stop)", len(r.Heartbeats))
	}
	if unobservedBeats(r) != 0 {
		t.Fatalf("unobserved heartbeats: %+v", r.Heartbeats)
	}
	js, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(string(js), ".effectoracle-canary") {
		t.Fatal("canary file names leaked into the report")
	}
	if got := r.TransientMutations(); len(got) != 0 {
		t.Fatalf("canaries produced transient candidates: %+v", got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("canary left files behind: %v", entries)
	}
	for _, m := range r.Marks {
		if len(m.Entries) != 0 {
			t.Fatalf("mark %q recorded canary entries: %+v", m.Name, m.Entries)
		}
	}
}

func TestSilentInstrumentFailsTheCanaryAndIsUnqualified(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	inner, err := openKernelInotify()
	if err != nil {
		t.Fatalf("openKernelInotify: %v", err)
	}
	f := &faultyInotify{inner: inner}
	f.mute.Store(true)
	o := startInjected(t, root, f, 100*time.Millisecond)
	writeFile(t, filepath.Join(root, "silent.txt"), "a dead instrument records nothing")
	r := mustStop(t, o)

	if r.Verdict != VerdictInstrumentUnqualified {
		t.Fatalf("verdict = %q, want instrument_unqualified; heartbeats=%+v", r.Verdict, r.Heartbeats)
	}
	if len(r.Events) != 0 || len(r.Gaps) != 0 {
		t.Fatalf("a silent instrument records no events and no gap, but got events=%+v gaps=%+v", r.Events, r.Gaps)
	}
	if unobservedBeats(r) != len(r.Heartbeats) {
		t.Fatalf("every canary should be unobserved, heartbeats=%+v", r.Heartbeats)
	}
}

func TestWatchLimitIsExplicitAndSiblingDirectoriesStayCovered(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	blocked := filepath.Join(root, "a")
	covered := filepath.Join(root, "b")
	mustMkdir(t, blocked)
	mustMkdir(t, covered)
	inner, err := openKernelInotify()
	if err != nil {
		t.Fatalf("openKernelInotify: %v", err)
	}
	f := &faultyInotify{inner: inner, addErr: func(p string) error {
		if p == blocked {
			return syscall.ENOSPC
		}
		return nil
	}}
	o := startInjected(t, root, f, defaultCanaryTimeout)
	writeFile(t, filepath.Join(covered, "x.txt"), "covered")
	r := mustStop(t, o)

	found := false
	for _, g := range gapsOfKind(r, GapWatchLimit) {
		if g.Path == "a" && g.Detail == syscall.ENOSPC.Error() {
			found = true
		}
	}
	if !found {
		t.Fatalf("no watch_limit gap for a with ENOSPC detail; gaps=%+v", r.Gaps)
	}
	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost", r.Verdict)
	}
	if n := len(eventsAt(r, KindCreate, "b/x.txt")); n != 1 {
		t.Fatalf("sibling directory lost coverage: %d create events for b/x.txt", n)
	}
}

func TestQueueOverflowIsEvidenceLostAndWatchesSurvive(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "sub"))
	inner, err := openKernelInotify()
	if err != nil {
		t.Fatalf("openKernelInotify: %v", err)
	}
	f := &faultyInotify{inner: inner}
	o := startInjected(t, root, f, defaultCanaryTimeout)
	f.prependOverflow.Store(true)
	mustMark(t, o, "after-overflow")
	writeFile(t, filepath.Join(root, "sub", "after.txt"), "recorded after the rebuild")
	r := mustStop(t, o)

	if n := len(gapsOfKind(r, GapOverflow)); n != 1 {
		t.Fatalf("overflow gaps = %d, want 1; gaps=%+v", n, r.Gaps)
	}
	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost", r.Verdict)
	}
	if unobservedBeats(r) != 0 {
		t.Fatalf("canaries must still be observed after the rebuild, heartbeats=%+v", r.Heartbeats)
	}
	if n := len(eventsAt(r, KindCreate, "sub/after.txt")); n != 1 {
		t.Fatalf("event after overflow not recorded: %d; events=%+v", n, r.Events)
	}
}

func TestReadFailureIsEvidenceLostAndADeadReaderFailsFast(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	inner, err := openKernelInotify()
	if err != nil {
		t.Fatalf("openKernelInotify: %v", err)
	}
	f := &faultyInotify{inner: inner}
	o := startInjected(t, root, f, 200*time.Millisecond)
	f.failRead.Store(true)
	mustMark(t, o, "after-read-failure")
	r := mustStop(t, o)

	if len(gapsOfKind(r, GapReadError)) == 0 {
		t.Fatalf("read failure left no read_error gap; gaps=%+v", r.Gaps)
	}
	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost", r.Verdict)
	}
	if unobservedBeats(r) == 0 {
		t.Fatal("a stopped reader must leave at least one unobserved canary")
	}
}

func TestSymlinkInsideRootIsRecordedAndNeverFollowed(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	o := startWithCleanup(t, root)
	writeFile(t, filepath.Join(outside, "elsewhere.txt"), "outside the root")
	r := mustStop(t, o)

	e, ok := entryAt(markNamed(t, r, MarkStart), "link")
	if !ok || e.Type != "symlink" || e.Target != outside {
		t.Fatalf("symlink entry = %+v (present=%v), want symlink to %s", e, ok, outside)
	}
	if len(r.Events) != 0 {
		t.Fatalf("writes outside the root must not be recorded: %+v", r.Events)
	}
}

func TestFIFOInsideRootIsSnapshottedWithoutBlocking(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	o := startWithCleanup(t, root)
	r := mustStop(t, o)

	e, ok := entryAt(markNamed(t, r, MarkStart), "pipe")
	if !ok || e.Type != "other" {
		t.Fatalf("fifo entry = %+v (present=%v), want type other", e, ok)
	}
}

func TestRootMustBeARealDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeFile(t, file, "not a directory")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := Start(file); !errors.Is(err, ErrRootNotDirectory) {
		t.Errorf("Start(file) error = %v, want ErrRootNotDirectory", err)
	}
	if _, err := Start(link); !errors.Is(err, ErrRootNotDirectory) {
		t.Errorf("Start(symlink) error = %v, want ErrRootNotDirectory", err)
	}
	if _, err := Start(filepath.Join(dir, "missing")); err == nil {
		t.Error("Start(missing) succeeded")
	}
}

func TestStopIsIdempotentAndMarkAfterStopIsRejected(t *testing.T) {
	requireInotify(t)
	o := startWithCleanup(t, t.TempDir())
	r1 := mustStop(t, o)
	r2, err := o.Stop()
	if err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if r1 != r2 {
		t.Fatal("second Stop returned a different report")
	}
	if err := o.Mark("late"); !errors.Is(err, ErrStopped) {
		t.Fatalf("Mark after Stop = %v, want ErrStopped", err)
	}
}

func TestEmptyAndReservedMarkNamesAreRejectedWithoutRecording(t *testing.T) {
	requireInotify(t)
	o := startWithCleanup(t, t.TempDir())
	for _, name := range []string{"", MarkStart, MarkStop} {
		if err := o.Mark(name); !errors.Is(err, ErrInvalidMarkName) {
			t.Errorf("Mark(%q) = %v, want ErrInvalidMarkName", name, err)
		}
	}
	r := mustStop(t, o)
	if len(r.Marks) != 2 {
		t.Fatalf("rejected names must not record marks; marks=%+v", r.Marks)
	}
}

// deterministicWorkload performs the same mutations in the same order. Each
// step ends at a barrier, so kernel coalescing cannot differ between runs.
func deterministicWorkload(t *testing.T, root string, o *Observer) {
	writeFile(t, filepath.Join(root, "a.txt"), "one")
	mustMark(t, o, "after-create")
	mustRename(t, filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt"))
	mustMark(t, o, "after-rename")
	writeFile(t, filepath.Join(root, "c.txt"), "two")
	mustRemove(t, filepath.Join(root, "b.txt"))
	mustMark(t, o, "after-delete")
}

func TestReportDigestIsReproducibleForIdenticalWorkloads(t *testing.T) {
	requireInotify(t)
	run := func(workload func(*testing.T, string, *Observer)) string {
		root := t.TempDir()
		o := startWithCleanup(t, root)
		workload(t, root, o)
		r := mustStop(t, o)
		if r.Verdict != VerdictOK {
			t.Fatalf("verdict = %q, want ok", r.Verdict)
		}
		d, err := r.Digest()
		if err != nil {
			t.Fatalf("Digest: %v", err)
		}
		return d
	}
	first := run(deterministicWorkload)
	second := run(deterministicWorkload)
	if first != second {
		t.Fatalf("identical workloads gave different digests: %s vs %s", first, second)
	}
	other := run(func(t *testing.T, root string, o *Observer) {
		writeFile(t, filepath.Join(root, "only.txt"), "different")
		mustMark(t, o, "after-create")
	})
	if other == first {
		t.Fatal("different workloads gave the same digest")
	}
}
