//go:build linux

package effectoracle

import (
	"encoding/binary"
	"errors"
	"fmt"
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
	addErr          func(path string) error // runs before a watch is installed; a non-nil error fails the call
	afterAdd        func(path string)       // runs after a watch is installed, before the observer inspects the directory
	dropNames       map[string]bool         // records for these names never reach the reader; set before Start
	mute            atomic.Bool             // discard kernel records, as a dead instrument would
	prependOverflow atomic.Bool             // precede the next batch of records with IN_Q_OVERFLOW
	failRead        atomic.Bool             // fail the next read with EIO
}

func (f *faultyInotify) AddWatch(path string, mask uint32) (int, error) {
	if f.addErr != nil {
		if err := f.addErr(path); err != nil {
			return -1, err
		}
	}
	wd, err := f.inner.AddWatch(path, mask)
	if err == nil && f.afterAdd != nil {
		f.afterAdd(path)
	}
	return wd, err
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
		body := p[inotifyHeaderSize : inotifyHeaderSize+n]
		if f.dropNames != nil {
			body = dropNamed(body, f.dropNames)
		}
		if f.prependOverflow.CompareAndSwap(true, false) {
			out := make([]byte, inotifyHeaderSize+len(body))
			putOverflowHeader(out[:inotifyHeaderSize])
			copy(out[inotifyHeaderSize:], body)
			return copy(p, out), nil
		}
		return copy(p, body), nil
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

// populateBeforeWatch creates entries in d that exist before d's watch is
// installed, so the kernel generates no events for them.
func populateBeforeWatch(d string) error {
	if err := os.WriteFile(filepath.Join(d, "f.txt"), []byte("created before the watch"), 0o600); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(d, "e"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "e", "h.txt"), []byte("nested, created before the watch"), 0o600)
}

func TestNewDirectoryRaceIsExplicitAndNestedWatchIsInstalled(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	d := filepath.Join(root, "d")
	inner, err := openKernelInotify()
	if err != nil {
		t.Fatalf("openKernelInotify: %v", err)
	}
	var populateErr error
	f := &faultyInotify{inner: inner, addErr: func(p string) error {
		if p == d {
			populateErr = populateBeforeWatch(d)
		}
		return nil
	}}
	o := startInjected(t, root, f, defaultCanaryTimeout)
	mustMkdir(t, d)
	markWithin(t, o, "after-race")
	writeFile(t, filepath.Join(d, "e", "g.txt"), "after the barrier")
	r := stopWithin(t, o)

	if populateErr != nil {
		t.Fatalf("populate before watch: %v", populateErr)
	}
	if !hasGap(r, GapSubdirRace, "d") {
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
	// The kernel's record for sub2 is lost, so no mkdir event reaches the reader
	// and no watch is installed on sub2 at creation. Only the rebuild that the
	// overflow triggers can watch it.
	f := &faultyInotify{inner: inner, dropNames: map[string]bool{"sub2": true}}
	o := startInjected(t, root, f, defaultCanaryTimeout)
	mustMkdir(t, filepath.Join(root, "sub2"))
	f.prependOverflow.Store(true)
	markWithin(t, o, "after-overflow")
	writeFile(t, filepath.Join(root, "sub", "after.txt"), "recorded after the rebuild")
	writeFile(t, filepath.Join(root, "sub2", "after.txt"), "only a rebuilt watch records this")
	r := stopWithin(t, o)

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
	if n := len(eventsAt(r, KindCreate, "sub2/after.txt")); n != 1 {
		t.Fatalf("the rebuild did not install a watch on sub2: %d create events; events=%+v", n, r.Events)
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
	writeFile(t, filepath.Join(outside, "elsewhere.txt"), "present before Start, outside the root")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	o := startWithCleanup(t, root)
	writeFile(t, filepath.Join(outside, "later.txt"), "written after Start, outside the root")
	r := stopWithin(t, o)

	e, ok := entryAt(markNamed(t, r, MarkStart), "link")
	if !ok || e.Type != "symlink" || e.Target != outside {
		t.Fatalf("symlink entry = %+v (present=%v), want symlink to %s", e, ok, outside)
	}
	if len(r.Events) != 0 {
		t.Fatalf("writes outside the root must not be recorded: %+v", r.Events)
	}
	for _, m := range r.Marks {
		for _, en := range m.Entries {
			if strings.HasPrefix(en.Path, "link/") {
				t.Fatalf("mark %q followed the symlink: entry %q", m.Name, en.Path)
			}
		}
	}
	js, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, name := range []string{"elsewhere.txt", "later.txt"} {
		if strings.Contains(string(js), name) {
			t.Fatalf("a file outside the root (%s) reached the report", name)
		}
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

// hangTimeout bounds each observer call in the hostile-filesystem tests, so a
// wedged snapshot or reader fails the test instead of hanging the suite.
const hangTimeout = 30 * time.Second

// withinTimeout runs fn and fails the test if it does not return in time.
func withinTimeout[T any](t *testing.T, what string, fn func() T) T {
	t.Helper()
	ch := make(chan T, 1)
	go func() { ch <- fn() }()
	select {
	case v := <-ch:
		return v
	case <-time.After(hangTimeout):
		t.Fatalf("%s did not return within %v", what, hangTimeout)
	}
	var zero T
	return zero
}

func markWithin(t *testing.T, o *Observer, name string) {
	t.Helper()
	if err := withinTimeout(t, "Mark "+name, func() error { return o.Mark(name) }); err != nil {
		t.Fatalf("Mark(%q) returned an error: %v", name, err)
	}
}

func stopWithin(t *testing.T, o *Observer) *Report {
	t.Helper()
	type result struct {
		r   *Report
		err error
	}
	res := withinTimeout(t, "Stop", func() result {
		r, err := o.Stop()
		return result{r, err}
	})
	if res.err != nil {
		t.Fatalf("Stop returned an error: %v", res.err)
	}
	return res.r
}

// requireNonRoot skips a test whose premise is permission bits. Root reads
// unreadable files, so such a test would verify nothing.
func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("skipping: running as root, so permission bits do not restrict reads and this test verifies nothing")
	}
}

// canaryFileNameFor names canary n, so a test can address an issued canary
// without reaching into the observer's state.
func canaryFileNameFor(o *Observer, n int) string {
	return fmt.Sprintf(".effectoracle-canary-%s-%d", o.nonce, n)
}

func hasGap(r *Report, kind, path string) bool {
	for _, g := range gapsOfKind(r, kind) {
		if g.Path == path {
			return true
		}
	}
	return false
}

// hookOnce returns a snapshot hook that calls replace once, for the path victim,
// and stores its error in errp. The hook runs on the Mark goroutine, so it does
// not call t.Fatal.
func hookOnce(victim string, replace func(p string) error, errp *error) func(string) {
	done := false
	return func(p string) {
		if p != victim || done {
			return
		}
		done = true
		*errp = replace(p)
	}
}

func replaceWithSymlink(p, target string) error {
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	return os.Symlink(target, p)
}

// assertUnreadableGap checks the verdict, the snapshot_unreadable gap for path,
// the unreadable mid-mark entry, and that the event before the fault survived.
func assertUnreadableGap(t *testing.T, r *Report, path string) {
	t.Helper()
	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost; gaps=%+v", r.Verdict, r.Gaps)
	}
	if !hasGap(r, GapSnapshotUnreadable, path) {
		t.Fatalf("no snapshot_unreadable gap for %q; gaps=%+v", path, r.Gaps)
	}
	if e, ok := entryAt(markNamed(t, r, "mid"), path); !ok || e.Type != "unreadable" {
		t.Fatalf("mid mark entry for %q = %+v (present=%v), want type unreadable", path, e, ok)
	}
	if n := len(eventsAt(r, KindCreate, "before.txt")); n != 1 {
		t.Fatalf("event recorded before the fault was lost: %d create events for before.txt", n)
	}
	if unobservedBeats(r) != 0 {
		t.Fatalf("the instrument stayed live, so every canary must be observed, heartbeats=%+v", r.Heartbeats)
	}
}

func TestUnreadableFileIsAGapAndDoesNotAbortTheSnapshot(t *testing.T) {
	requireInotify(t)
	requireNonRoot(t)
	root := t.TempDir()
	locked := filepath.Join(root, "locked.txt")
	writeFile(t, locked, "readable at Start")
	o := startWithCleanup(t, root)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })
	writeFile(t, filepath.Join(root, "before.txt"), "recorded before the fault")
	markWithin(t, o, "mid")
	r := stopWithin(t, o)
	assertUnreadableGap(t, r, "locked.txt")
}

func TestUnreadableSubdirectoryIsAGapAndItsSiblingsStayRecorded(t *testing.T) {
	requireInotify(t)
	requireNonRoot(t)
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	mustMkdir(t, locked)
	writeFile(t, filepath.Join(locked, "hidden.txt"), "inside the unreadable directory")
	o := startWithCleanup(t, root)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	writeFile(t, filepath.Join(root, "before.txt"), "recorded before the fault")
	markWithin(t, o, "mid")
	r := stopWithin(t, o)
	assertUnreadableGap(t, r, "locked")
}

func TestFileSwappedForAFIFOMidWalkIsAGapAndNeverBlocks(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	victim := filepath.Join(root, "victim.txt")
	writeFile(t, victim, "regular when the walk lstat'd it")
	o := startWithCleanup(t, root)
	writeFile(t, filepath.Join(root, "before.txt"), "recorded before the fault")
	var swapErr error
	o.snapHook = hookOnce(victim, func(p string) error {
		if err := os.Remove(p); err != nil {
			return err
		}
		return syscall.Mkfifo(p, 0o600)
	}, &swapErr)
	markWithin(t, o, "mid")
	r := stopWithin(t, o)
	if swapErr != nil {
		t.Fatalf("swap to FIFO: %v", swapErr)
	}
	assertUnreadableGap(t, r, "victim.txt")
}

func TestFileSwappedForASymlinkMidWalkIsAGapAndIsNotFollowed(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	victim := filepath.Join(root, "victim.txt")
	writeFile(t, victim, "regular when the walk lstat'd it")
	target := filepath.Join(t.TempDir(), "target.txt")
	writeFile(t, target, "outside the root")
	o := startWithCleanup(t, root)
	writeFile(t, filepath.Join(root, "before.txt"), "recorded before the fault")
	var swapErr error
	o.snapHook = hookOnce(victim, func(p string) error {
		if err := os.Remove(p); err != nil {
			return err
		}
		return os.Symlink(target, p)
	}, &swapErr)
	markWithin(t, o, "mid")
	r := stopWithin(t, o)
	if swapErr != nil {
		t.Fatalf("swap to symlink: %v", swapErr)
	}
	assertUnreadableGap(t, r, "victim.txt")
}

func TestDirectorySwappedForAnOutsideSymlinkMidWalkIsAGap(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	mustMkdir(t, sub)
	writeFile(t, filepath.Join(sub, "inside.txt"), "inside the root")
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "outside.txt"), "outside the root")
	o := startWithCleanup(t, root)
	var swapErr error
	o.snapHook = hookOnce(sub, func(p string) error { return replaceWithSymlink(p, outside) }, &swapErr)
	writeFile(t, filepath.Join(root, "before.txt"), "recorded before the fault")
	markWithin(t, o, "mid")
	r := stopWithin(t, o)
	if swapErr != nil {
		t.Fatalf("swap to symlink: %v", swapErr)
	}
	for _, e := range markNamed(t, r, "mid").Entries {
		if strings.HasPrefix(e.Path, "sub/") {
			t.Fatalf("snapshot listed %q under a directory swapped for an outside symlink", e.Path)
		}
	}
	if js, err := r.JSON(); err != nil || strings.Contains(string(js), "outside.txt") {
		t.Fatalf("outside contents reached the report (err=%v)", err)
	}
	assertUnreadableGap(t, r, "sub")
}

func TestDirectorySwappedForAnOutsideSymlinkDuringWatchInstallIsAGap(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	mustMkdir(t, sub)
	outside := t.TempDir()
	inner, err := openKernelInotify()
	if err != nil {
		t.Fatalf("openKernelInotify: %v", err)
	}
	var swapErr error
	swapped := false
	f := &faultyInotify{inner: inner, afterAdd: func(p string) {
		if p == sub && !swapped {
			swapped = true
			swapErr = replaceWithSymlink(p, outside)
		}
	}}
	o := startInjected(t, root, f, defaultCanaryTimeout)
	writeFile(t, filepath.Join(outside, "outside.txt"), "outside the root, never watched")
	r := stopWithin(t, o)
	if swapErr != nil {
		t.Fatalf("swap to symlink: %v", swapErr)
	}
	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost; gaps=%+v", r.Verdict, r.Gaps)
	}
	if !hasGap(r, GapWatchError, "sub") {
		t.Fatalf("no watch_error gap for sub after it was swapped during watch installation; gaps=%+v", r.Gaps)
	}
	for _, e := range r.Events {
		if strings.Contains(e.Path, "outside.txt") {
			t.Fatalf("outside directory was observed through the root: %+v", e)
		}
	}
}

func TestMkdirAloneKeepsTheVerdictOK(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	o := startWithCleanup(t, root)
	mustMkdir(t, filepath.Join(root, "d"))
	markWithin(t, o, "after-mkdir")
	r := stopWithin(t, o)

	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok; gaps=%+v", r.Verdict, r.Gaps)
	}
	if creates := eventsAt(r, KindCreate, "d"); len(creates) != 1 || !creates[0].IsDir {
		t.Fatalf("want one create event for directory d; events=%+v", r.Events)
	}
}

func TestChildCreatedAfterAnEmptyDirectoryWatchIsInstalledKeepsTheVerdictOK(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	d := filepath.Join(root, "d")
	o := startWithCleanup(t, root)
	mustMkdir(t, d)
	markWithin(t, o, "after-mkdir")
	writeFile(t, filepath.Join(d, "f.txt"), "created after the watch")
	r := stopWithin(t, o)

	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok; gaps=%+v", r.Verdict, r.Gaps)
	}
	if n := len(eventsAt(r, KindCreate, "d/f.txt")); n != 1 {
		t.Fatalf("create of d/f.txt: %d events, want 1; events=%+v", n, r.Events)
	}
}

func TestFileCreatedBeforeNewDirectoryWatchIsEvidenceLost(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	d := filepath.Join(root, "d")
	inner, err := openKernelInotify()
	if err != nil {
		t.Fatalf("openKernelInotify: %v", err)
	}
	var writeErr error
	f := &faultyInotify{inner: inner, addErr: func(p string) error {
		if p == d {
			// Created before the watch on d exists, so no event is generated.
			writeErr = os.WriteFile(filepath.Join(d, "early.txt"), []byte("before the watch"), 0o600)
		}
		return nil
	}}
	o := startInjected(t, root, f, defaultCanaryTimeout)
	mustMkdir(t, d)
	markWithin(t, o, "after-mkdir")
	r := stopWithin(t, o)

	if writeErr != nil {
		t.Fatalf("write before watch: %v", writeErr)
	}
	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost; gaps=%+v", r.Verdict, r.Gaps)
	}
	if !hasGap(r, GapSubdirRace, "d") {
		t.Fatalf("no subdir_race gap for d; gaps=%+v", r.Gaps)
	}
}

func TestEntryDeletedAfterNewDirectoryWatchIsEvidenceLost(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	d := filepath.Join(root, "d")
	early := filepath.Join(d, "early.txt")
	inner, err := openKernelInotify()
	if err != nil {
		t.Fatalf("openKernelInotify: %v", err)
	}
	var writeErr, removeErr error
	f := &faultyInotify{inner: inner,
		addErr: func(p string) error {
			if p == d {
				writeErr = os.WriteFile(early, []byte("before the watch"), 0o600)
			}
			return nil
		},
		// The entry is removed after the watch exists but before the directory is
		// enumerated. The enumeration misses it, and only the delete event reveals it.
		afterAdd: func(p string) {
			if p == d {
				removeErr = os.Remove(early)
			}
		},
	}
	o := startInjected(t, root, f, defaultCanaryTimeout)
	mustMkdir(t, d)
	markWithin(t, o, "after-mkdir")
	r := stopWithin(t, o)

	if writeErr != nil || removeErr != nil {
		t.Fatalf("setup: write=%v remove=%v", writeErr, removeErr)
	}
	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost; gaps=%+v events=%+v", r.Verdict, r.Gaps, r.Events)
	}
	if !hasGap(r, GapSubdirRace, "d/early.txt") {
		t.Fatalf("no subdir_race gap for the unseen entry d/early.txt; gaps=%+v", r.Gaps)
	}
}

func TestMoveInOfAPopulatedDirectoryIsEvidenceLost(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	outside := t.TempDir()
	src := filepath.Join(outside, "pkg")
	mustMkdir(t, src)
	writeFile(t, filepath.Join(src, "x.txt"), "moved in with content")
	o := startWithCleanup(t, root)
	mustRename(t, src, filepath.Join(root, "pkg"))
	markWithin(t, o, "after-move")
	r := stopWithin(t, o)

	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost; gaps=%+v", r.Verdict, r.Gaps)
	}
	if !hasGap(r, GapSubdirRace, "pkg") {
		t.Fatalf("no subdir_race gap for the moved-in directory; gaps=%+v", r.Gaps)
	}
}

func TestMoveInOfAnEmptyDirectoryKeepsTheVerdictOK(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	outside := t.TempDir()
	src := filepath.Join(outside, "pkg")
	mustMkdir(t, src)
	o := startWithCleanup(t, root)
	mustRename(t, src, filepath.Join(root, "pkg"))
	markWithin(t, o, "after-move")
	r := stopWithin(t, o)

	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok; gaps=%+v", r.Verdict, r.Gaps)
	}
	if n := len(gapsOfKind(r, GapSubdirRace)); n != 0 {
		t.Fatalf("empty move-in recorded %d subdir_race gaps", n)
	}
}

func TestMovingAWatchedDirectoryAwayWithAPendingChildIsEvidenceLost(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	outside := t.TempDir()
	d := filepath.Join(root, "d")
	mustMkdir(t, d)
	o := startWithCleanup(t, root)
	writeFile(t, filepath.Join(d, "x"), "created in the watched directory")
	mustRename(t, d, filepath.Join(outside, "d"))
	r := stopWithin(t, o)

	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost; gaps=%+v", r.Verdict, r.Gaps)
	}
	if !hasGap(r, GapDirMovedOut, "d") {
		t.Fatalf("no dir_moved_out gap for d; gaps=%+v", r.Gaps)
	}
	if n := len(eventsAt(r, KindCreate, "d/x")); n != 1 {
		t.Fatalf("create of d/x: %d events, want 1; events=%+v", n, r.Events)
	}
}

func TestExternalWriteToAnIssuedCanaryNameIsLoggedAsAnAnomaly(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	o := startWithCleanup(t, root)
	// Canary 1 was written and removed before Start returned. A process that
	// learned its name now creates it.
	name := canaryFileNameFor(o, 1)
	writeFile(t, filepath.Join(root, name), "a foreign process learned the name")
	r := stopWithin(t, o)

	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost; gaps=%+v", r.Verdict, r.Gaps)
	}
	if n := len(eventsAt(r, KindCreate, name)); n != 1 {
		t.Fatalf("foreign create of an issued canary name: %d events, want 1; events=%+v", n, r.Events)
	}
	if len(gapsOfKind(r, GapCanaryAnomaly)) == 0 {
		t.Fatalf("no canary_anomaly gap; gaps=%+v", r.Gaps)
	}
	if unobservedBeats(r) != 0 {
		t.Fatalf("the oracle's own canaries must still be observed, heartbeats=%+v", r.Heartbeats)
	}
}

func TestExtraEventWhileACanaryIsOutstandingIsLoggedAsAnAnomaly(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	o := startWithCleanup(t, root)
	target := canaryFileNameFor(o, 3) // mark:checkpoint:pre
	o.canaryHook = func(name string) {
		if name == target {
			// Runs after the oracle's own create, write, and close, and before it
			// waits for its close_write. A foreign write lands in that window.
			writeFile(t, filepath.Join(root, name), "foreign write while outstanding")
		}
	}
	markWithin(t, o, "checkpoint")
	r := stopWithin(t, o)

	if r.Verdict != VerdictEvidenceLost {
		t.Fatalf("verdict = %q, want evidence_lost; gaps=%+v", r.Verdict, r.Gaps)
	}
	if len(gapsOfKind(r, GapCanaryAnomaly)) == 0 {
		t.Fatalf("no canary_anomaly gap for the extra event; gaps=%+v", r.Gaps)
	}
	if n := len(eventsAt(r, KindModify, target)); n == 0 {
		t.Fatalf("the extra modify was not logged; events=%+v", r.Events)
	}
	if unobservedBeats(r) != 0 {
		t.Fatalf("the oracle's own canary must still be observed, heartbeats=%+v", r.Heartbeats)
	}
}

func TestEventsAfterTheStopBarrierAreNotLogged(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	o := startWithCleanup(t, root)
	stopPost := canaryFileNameFor(o, 4) // mark:stop:post
	var foreignErr error
	o.canaryHook = func(name string) {
		if name == stopPost && foreignErr == nil {
			foreignErr = os.WriteFile(filepath.Join(root, "after-stop.txt"), []byte("after the barrier"), 0o600)
		}
	}
	r := stopWithin(t, o)

	if foreignErr != nil {
		t.Fatalf("foreign write: %v", foreignErr)
	}
	if n := len(eventsAt(r, KindCreate, "after-stop.txt")); n != 0 {
		t.Fatalf("an event after the post-Stop barrier entered the log: %+v", r.Events)
	}
	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok: the window ends at the barrier; gaps=%+v", r.Verdict, r.Gaps)
	}
}

func TestSubdirectoryRemovalIsRecordedByItsParentWithoutAGap(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	d := filepath.Join(root, "d")
	mustMkdir(t, d)
	o := startWithCleanup(t, root)
	mustRemove(t, d)
	markWithin(t, o, "after-rmdir")
	r := stopWithin(t, o)

	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok; the parent records the removal; gaps=%+v", r.Verdict, r.Gaps)
	}
	if n := len(eventsAt(r, KindDelete, "d")); n != 1 {
		t.Fatalf("delete of d: %d events, want 1; events=%+v", n, r.Events)
	}
}

func TestSameModeChmodIsTouchedUnchangedNotReverted(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	p := filepath.Join(root, "g.txt")
	writeFile(t, p, "G")
	o := startWithCleanup(t, root)
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	r := stopWithin(t, o)

	if n := len(eventsAt(r, KindAttrib, "g.txt")); n == 0 {
		t.Fatalf("chmod produced no attrib event, so the scenario did not occur; events=%+v", r.Events)
	}
	if got := transientsOf(r, TransientReverted, "g.txt"); len(got) != 0 {
		t.Fatalf("same-mode chmod reported as reverted: %+v", got)
	}
	got := transientsOf(r, TransientTouchedUnchanged, "g.txt")
	if len(got) != 1 || len(got[0].ObservedAtMarks) != 0 {
		t.Fatalf("touched_unchanged for g.txt = %+v, want one with no marks", got)
	}
}

func TestSameBytesRewriteIsTouchedUnchangedNotReverted(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	p := filepath.Join(root, "f.txt")
	writeFile(t, p, "A")
	o := startWithCleanup(t, root)
	writeFile(t, p, "A")
	markWithin(t, o, "mid")
	r := stopWithin(t, o)

	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %q, want ok; gaps=%+v", r.Verdict, r.Gaps)
	}
	if got := transientsOf(r, TransientReverted, "f.txt"); len(got) != 0 {
		t.Fatalf("identical-bytes rewrite reported as reverted: %+v", got)
	}
	if got := transientsOf(r, TransientTouchedUnchanged, "f.txt"); len(got) != 1 {
		t.Fatalf("touched_unchanged for f.txt = %+v, want one", got)
	}
}
