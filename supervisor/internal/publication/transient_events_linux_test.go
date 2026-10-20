//go:build linux

package publication

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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
	f := newFixture(t, base, base, middle, final, nil, nil, []string{"newdir"})
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

	// The events fall inside the transaction's apply window on the monotonic clock the evidence carries.
	evidence := result.Evidence
	if evidence == nil || evidence.ApplyStartMonoNS <= 0 || evidence.ApplyEndMonoNS < evidence.ApplyStartMonoNS || report.StartMonoNS <= 0 {
		t.Fatalf("no apply window: evidence %+v, report start %d", evidence, report.StartMonoNS)
	}
	for _, ev := range report.Events {
		at := report.StartMonoNS + ev.AtNS
		if at < evidence.ApplyStartMonoNS-int64(time.Millisecond) || at > evidence.ApplyEndMonoNS+int64(time.Second) {
			t.Errorf("%s %s at %d is outside the apply window [%d, %d]", ev.Kind, ev.Path, at, evidence.ApplyStartMonoNS, evidence.ApplyEndMonoNS)
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
