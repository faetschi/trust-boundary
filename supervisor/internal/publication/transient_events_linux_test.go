//go:build linux

package publication

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/runner"
)

// What publication does to the live root, as the inotify observer sees it. The host's EFF-TRANSIENT judge
// (runner.judgeTransient) explains a ledger operation only by the exact events runner.PublicationEventSequence names,
// once each and in the order publication applied them. Publication applies every object by rename from or to its
// private artifact directory (a sibling of the live root that is not watched), so each rename is seen with one side
// only, under a cookie of its own:
//
//	create            moved_to on the path
//	directory create  moved_to on the path, a directory
//	overwrite         moved_to on the path, then moved_from on the path (the displaced file; RENAME_EXCHANGE)
//	delete            moved_from on the path
//
// Publication never writes, creates, truncates, unlinks or changes the attributes of a live path in place, and never
// renames within the live root, so any other event, any repeat, and any cookie two events share is not publication's.
// If publication changes how it applies an object, this test fails and the judge's rules must change with it.
func TestPublishRenamesAreTheOnlyLiveRootEvents(t *testing.T) {
	base := map[string]string{"over.txt": "old-value", "gone.txt": "remove-me", "move.txt": "rename-me"}
	middle := map[string]string{"over.txt": "middle-value", "gone.txt": "remove-me", "move.txt": "rename-me", "ephemeral.txt": "transient"}
	final := map[string]string{"over.txt": "final-value", "newdir/moved.txt": "rename-me", "created.txt": "new-value"}
	// The live root has the directory a file system makes at its root, lost+found: publication's fence.
	fence := []string{effectoracle.FenceDir}
	f := newFixture(t, base, base, middle, final, fence, fence, []string{effectoracle.FenceDir, "newdir"})
	repo := f.acquire(1)
	defer repo.Close()
	observer, err := effectoracle.Start(filepath.Join(f.root, "live"))
	if err != nil {
		t.Skipf("inotify is unavailable here (%v): this test verifies nothing without it", err)
	}
	result, err := repo.Publish(context.Background(), f.request, func(b CommitBinding) (Authorization, error) {
		return Authorization{DecisionID: "d", BindingDigest: b.BindingDigest}, nil
	}, nil)
	if err != nil || result.Status != StatusSucceeded {
		observer.Abort()
		t.Fatalf("Publish: %v %+v", err, result)
	}
	report, err := observer.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if !report.QualifiedWindow() {
		t.Fatalf("the window is not qualified: verdict %s gaps %+v", report.Verdict, report.Gaps)
	}
	to, from := effectoracle.KindMovedTo, effectoracle.KindMovedFrom

	// Every operation type, each with the events the table above gives it (written out here, not derived).
	byPath := map[string][]string{}
	for _, ev := range report.Events {
		byPath[ev.Path] = append(byPath[ev.Path], ev.Kind)
	}
	want := map[string][]string{
		"created.txt":      {to},       // create
		"newdir":           {to},       // directory create
		"newdir/moved.txt": {to},       // create inside the created directory
		"over.txt":         {to, from}, // overwrite
		"gone.txt":         {from},     // delete
		"move.txt":         {from},     // delete (the source of a move)
	}
	if !reflect.DeepEqual(byPath, want) {
		t.Errorf("events by path = %v, want %v (all events %+v)", byPath, want, report.Events)
	}
	for _, ev := range report.Events {
		if ev.IsDir != (ev.Path == "newdir") {
			t.Errorf("%s %s: is_dir = %v", ev.Kind, ev.Path, ev.IsDir)
		}
	}

	// The judge's sequence is exactly the events, in order: this is what a ledger operation explains.
	seq, ok := runner.PublicationEventSequence(result.Ledger)
	if !ok {
		t.Fatalf("the ledger has an operation with no known events: %+v", result.Ledger)
	}
	var got []runner.PublicationEvent
	for _, ev := range report.Events {
		got = append(got, runner.PublicationEvent{Path: ev.Path, Kind: ev.Kind, IsDir: ev.IsDir})
	}
	if !reflect.DeepEqual(got, seq) {
		t.Errorf("events in order = %v\nledger sequence  = %v", got, seq)
	}

	// Every rename has a kernel cookie, and none is shared: each rename's other side is in the unwatched artifact
	// directory. A shared cookie would be a rename between two paths of the live root.
	seen := map[uint32]string{}
	for _, ev := range report.Events {
		if ev.Cookie == 0 {
			t.Errorf("%s %s has no cookie", ev.Kind, ev.Path)
		}
		if other, dup := seen[ev.Cookie]; dup {
			t.Errorf("%s %s shares cookie %d with %s", ev.Kind, ev.Path, ev.Cookie, other)
		}
		seen[ev.Cookie] = ev.Kind + " " + ev.Path
	}

	// The events fall inside the transaction's apply window, which is placed by order: publication opened the fence
	// directory read-only just before it applied and just after, the observer records both opens in its queue (the
	// scans of the tree publication also makes open the directory and read it, which tells them apart), and every event
	// is between the two however late the reader took it.
	evidence := result.Evidence
	if evidence == nil || evidence.FenceDir != effectoracle.FenceDir || evidence.FenceBursts != 2 {
		t.Fatalf("publication did not fence its apply twice: %+v", evidence)
	}
	if report.FenceDir != effectoracle.FenceDir {
		t.Fatalf("the observer did not watch the fence directory: %q", report.FenceDir)
	}
	// A fence (effectoracle.FenceRoot) is an unbroken alternation of 2*FenceBurst opens and reads of the directory in records
	// with consecutive sequence numbers. The scans of the tree that publication also makes open and read the directory
	// too, but leave a few records at most: the longest alternation that is not a fence stays well below.
	type run struct{ first, last uint64 }
	var runs []run
	var cur run
	var prev effectoracle.Fence
	length, longest := 0, 0
	end := func() {
		if length >= 2*effectoracle.FenceBurst {
			runs = append(runs, cur)
		} else if length > longest {
			longest = length
		}
		length = 0
	}
	for _, fc := range report.Fences {
		if length > 0 && (fc.Seq != prev.Seq+1 || fc.Op == prev.Op) {
			end()
		}
		if length == 0 {
			cur.first = fc.Seq
		}
		cur.last, prev = fc.Seq, fc
		length++
	}
	end()
	if len(runs) != 2 {
		t.Fatalf("alternations of %d records = %+v, want publication's two (fences %+v)", 2*effectoracle.FenceBurst, runs, report.Fences)
	}
	t.Logf("fences %+v; the longest alternation that is not a fence: %d records; all records %d", runs, longest, len(report.Fences))
	for _, ev := range report.Events {
		if ev.Seq <= runs[0].last || ev.Seq >= runs[1].first {
			t.Errorf("%s %s seq %d is outside the fences %+v", ev.Kind, ev.Path, ev.Seq, runs)
		}
	}

	// Every ledger path appears, and no other.
	ledger := map[string]bool{}
	for _, e := range result.Ledger {
		for _, ch := range e.Changes {
			ledger[ch.Path] = true
		}
	}
	for path := range want {
		if !ledger[path] {
			t.Errorf("%s is not in the publication ledger %v", path, ledger)
		}
	}
	if len(ledger) != len(want) {
		t.Errorf("ledger paths %v, want %d", ledger, len(want))
	}
}
