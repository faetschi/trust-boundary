//go:build linux

package effectoracle

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// fenceRoot is a root with the fence directory and a file to rename.
func fenceRoot(t *testing.T) string {
	t.Helper()
	requireInotify(t)
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, FenceDir))
	mustMkdir(t, filepath.Join(root, "other"))
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	return root
}

// bareOpen is what a publication fence is: the directory opened and closed, never read.
func bareOpen(t *testing.T, p string) {
	t.Helper()
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(fd); err != nil {
		t.Fatal(err)
	}
}

func fenceOps(r *Report) []string {
	var ops []string
	for _, f := range r.Fences {
		ops = append(ops, f.Op+" "+f.Path)
	}
	return ops
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The inotify queue is FIFO and an event is queued in the syscall that causes it, so the order of the records is the
// order the operations happened in, whenever the reader got to them.
func TestFenceOpensAreRecordedInQueueOrderWithTheEventsBetweenThem(t *testing.T) {
	root := fenceRoot(t)
	o := startWithCleanup(t, root)
	bareOpen(t, filepath.Join(root, FenceDir))
	mustRename(t, filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt"))
	bareOpen(t, filepath.Join(root, FenceDir))
	rep := mustStop(t, o)
	if !rep.QualifiedWindow() {
		t.Fatalf("not qualified: %s %+v", rep.Verdict, rep.Gaps)
	}
	if rep.FenceDir != FenceDir {
		t.Fatalf("fence_dir = %q", rep.FenceDir)
	}
	if got, want := fenceOps(rep), []string{"open " + FenceDir, "open " + FenceDir}; !sameStrings(got, want) {
		t.Fatalf("fences = %v, want %v", got, want)
	}
	if len(rep.Events) != 2 {
		t.Fatalf("events = %+v, want the two halves of the rename", rep.Events)
	}
	lo, hi := rep.Fences[0], rep.Fences[1]
	if !lo.IsDir || !hi.IsDir {
		t.Errorf("a fence open is a directory open: %+v", rep.Fences)
	}
	for _, ev := range rep.Events {
		if ev.Seq <= lo.Seq || ev.Seq >= hi.Seq {
			t.Errorf("%s %s seq %d is not between the fences %d and %d", ev.Kind, ev.Path, ev.Seq, lo.Seq, hi.Seq)
		}
	}
}

// Reading the directory is told from opening it: a scan opens, then reads.
func TestFenceRecordsADirectoryReadAfterItsOpen(t *testing.T) {
	root := fenceRoot(t)
	o := startWithCleanup(t, root)
	if _, err := os.ReadDir(filepath.Join(root, FenceDir)); err != nil {
		t.Fatal(err)
	}
	rep := mustStop(t, o)
	got := fenceOps(rep)
	if len(got) < 2 || got[0] != "open "+FenceDir || got[1] != "access "+FenceDir {
		t.Fatalf("fences = %v, want an open then an access", got)
	}
}

// Nothing else is recorded: not the opens of other directories, and not the observer's own snapshots at the start,
// at a mark and at the stop.
func TestFenceRecordsNoOtherOpenAndNotTheObserversOwnSnapshots(t *testing.T) {
	root := fenceRoot(t)
	o := startWithCleanup(t, root)
	bareOpen(t, filepath.Join(root, "other"))
	if _, err := os.ReadDir(root); err != nil {
		t.Fatal(err)
	}
	mustMark(t, o, "mid")
	rep := mustStop(t, o)
	if rep.FenceDir != FenceDir || len(rep.Fences) != 0 {
		t.Fatalf("fence_dir %q, fences %v: want none recorded", rep.FenceDir, fenceOps(rep))
	}
	if !rep.QualifiedWindow() {
		t.Fatalf("not qualified: %s %+v", rep.Verdict, rep.Gaps)
	}
}

// A stray reader of a file inside the fence directory is a record too (the judge counts it as ambiguity).
func TestFenceRecordsAnOpenOfAFileInsideTheFenceDirectory(t *testing.T) {
	root := fenceRoot(t)
	writeFile(t, filepath.Join(root, FenceDir, "x"), "x")
	o := startWithCleanup(t, root)
	if _, err := os.ReadFile(filepath.Join(root, FenceDir, "x")); err != nil {
		t.Fatal(err)
	}
	rep := mustStop(t, o)
	found := false
	for _, f := range rep.Fences {
		if f.Path == FenceDir+"/x" && !f.IsDir {
			found = true
		}
	}
	if !found {
		t.Fatalf("fences = %v, want the open of %s/x", fenceOps(rep), FenceDir)
	}
}

// Without the directory there is no fence: the report says so, and records no fence.
func TestFenceIsAbsentWhenTheRootHasNoFenceDirectory(t *testing.T) {
	requireInotify(t)
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "other"))
	o := startWithCleanup(t, root)
	bareOpen(t, filepath.Join(root, "other"))
	rep := mustStop(t, o)
	if rep.FenceDir != "" || len(rep.Fences) != 0 {
		t.Fatalf("fence_dir %q, fences %v", rep.FenceDir, fenceOps(rep))
	}
	if !rep.QualifiedWindow() {
		t.Fatalf("not qualified: %s %+v", rep.Verdict, rep.Gaps)
	}
}

// Opens after the stop barrier are outside the window.
func TestFenceOpensAfterTheStopAreNotRecorded(t *testing.T) {
	root := fenceRoot(t)
	o := startWithCleanup(t, root)
	rep := mustStop(t, o)
	bareOpen(t, filepath.Join(root, FenceDir))
	if rep.Fences != nil && len(rep.Fences) != 0 {
		t.Fatalf("fences = %v", fenceOps(rep))
	}
	again := mustStop(t, o)
	if len(again.Fences) != 0 {
		t.Fatalf("fences = %v", fenceOps(again))
	}
}

// The fence directory is not a place the observer stops watching: a rename out of it is an ordinary event.
func TestFenceDirectoryKeepsItsOrdinaryEvents(t *testing.T) {
	root := fenceRoot(t)
	o := startWithCleanup(t, root)
	writeFile(t, filepath.Join(root, FenceDir, "y"), "y")
	rep := mustStop(t, o)
	if len(eventsAt(rep, KindCreate, FenceDir+"/y")) != 1 {
		t.Fatalf("events = %+v", rep.Events)
	}
}

// A fence is an unbroken alternation of opens and reads that the kernel does not merge (it merges only identical events
// next to each other in the queue), in records whose sequence numbers are consecutive, and the events of a write between
// two fences are between them in the queue.
func TestFenceRootLeavesAnUnbrokenAlternationOfOpensAndReads(t *testing.T) {
	root := fenceRoot(t)
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	o := startWithCleanup(t, root)
	if err := FenceRoot(dir); err != nil {
		t.Fatal(err)
	}
	mustRename(t, filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt"))
	if err := FenceRoot(dir); err != nil {
		t.Fatal(err)
	}
	rep := mustStop(t, o)
	if len(rep.Fences) != 4*FenceBurst {
		t.Fatalf("%d fence records, want %d: %v", len(rep.Fences), 4*FenceBurst, fenceOps(rep))
	}
	for i, f := range rep.Fences {
		want := FenceOpen
		if i%2 == 1 {
			want = FenceAccess
		}
		if f.Op != want || f.Path != FenceDir {
			t.Fatalf("record %d is %s %s, want %s %s", i, f.Op, f.Path, want, FenceDir)
		}
		inFence := i % (2 * FenceBurst)
		if inFence > 0 && f.Seq != rep.Fences[i-1].Seq+1 {
			t.Errorf("record %d (seq %d) does not follow record %d (seq %d) directly", i, f.Seq, i-1, rep.Fences[i-1].Seq)
		}
	}
	first, second := rep.Fences[2*FenceBurst-1].Seq, rep.Fences[2*FenceBurst].Seq
	if len(rep.Events) != 2 {
		t.Fatalf("events = %+v", rep.Events)
	}
	for _, ev := range rep.Events {
		if ev.Seq <= first || ev.Seq >= second {
			t.Errorf("%s %s seq %d is not between the fences (%d, %d)", ev.Kind, ev.Path, ev.Seq, first, second)
		}
	}
	if !rep.QualifiedWindow() {
		t.Fatalf("not qualified: %s %+v", rep.Verdict, rep.Gaps)
	}
}

// Without the directory FenceRoot fails and writes nothing.
func TestFenceRootFailsWithoutTheFenceDirectoryAndCreatesNothing(t *testing.T) {
	root := t.TempDir()
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := FenceRoot(dir); err == nil {
		t.Fatal("FenceRoot succeeded without a fence directory")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("FenceRoot left %v", entries)
	}
}
