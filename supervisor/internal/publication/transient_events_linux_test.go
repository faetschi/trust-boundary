//go:build linux

package publication

import (
	"context"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/effectoracle"
)

// What publication does to the live root, as the inotify observer sees it. The host's EFF-TRANSIENT judge
// (runner.judgeTransient) explains a ledger path only by these events: an object that exists afterwards is a moved_to
// on its path, replacing one also moves the old object away (moved_from on the same path), a removed or moved-away
// object is a moved_from. Publication never writes, creates, truncates, unlinks or changes the attributes of a live
// path in place, so any such event on a ledger path is not publication's. If publication changes how it applies an
// object, this test fails and the judge's rules must change with it.
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
	got := map[string]map[string]bool{}
	for _, ev := range report.Events {
		if got[ev.Path] == nil {
			got[ev.Path] = map[string]bool{}
		}
		got[ev.Path][ev.Kind] = true
	}
	want := map[string]map[string]bool{
		"created.txt":      {effectoracle.KindMovedTo: true},
		"over.txt":         {effectoracle.KindMovedTo: true, effectoracle.KindMovedFrom: true},
		"gone.txt":         {effectoracle.KindMovedFrom: true},
		"move.txt":         {effectoracle.KindMovedFrom: true},
		"newdir":           {effectoracle.KindMovedTo: true},
		"newdir/moved.txt": {effectoracle.KindMovedTo: true},
	}
	for path, kinds := range want {
		for kind := range kinds {
			if !got[path][kind] {
				t.Errorf("%s: no %s event (events %v)", path, kind, got)
			}
		}
	}
	for path, kinds := range got {
		for kind := range kinds {
			if !want[path][kind] {
				t.Errorf("%s: unexpected %s event (events %v)", path, kind, got)
			}
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
