//go:build linux

package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Contract (audit #13, owner decision: document and pin current behaviour). When recovery fully
// observes a multi-object publication in which some objects were applied and later ones were not
// started, the objects are [SUCCEEDED, FAILED] and the aggregate status is FAILED with no error.
// Aggregate FAILED therefore MAY include a known partial apply: it is not proof of an unchanged
// baseline, and every consumer must re-check the baseline itself.
func TestRecoverReportsAggregateFailedForAKnownPartialApply(t *testing.T) {
	base := map[string]string{"a.txt": "before-a", "b.txt": "before-b"}
	final := map[string]string{"a.txt": "final-a", "b.txt": "final-b"}
	f := newFixture(t, base, base, final, final, nil, nil, nil)
	first := f.acquire(1)
	result, err := first.Publish(context.Background(), f.request, func(b CommitBinding) (Authorization, error) {
		return Authorization{DecisionID: "partial-apply", BindingDigest: b.BindingDigest}, nil
	}, func(point string, index int) error {
		// Object 0 is applied and checkpointed; object 1 is started and untouched.
		if point == "after-object-start-checkpoint" && index == 1 {
			return errors.New("interrupt before the second object")
		}
		return nil
	})
	if !errors.Is(err, ErrInjectedFault) || result.TransactionID == "" {
		t.Fatalf("the interruption was not reached: %+v %v", result, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := f.acquire(2)
	defer second.Close()
	got, err := second.Recover(f.live, result.TransactionID, nil)
	if err != nil || got.Status != StatusFailed || len(got.Objects) != 2 ||
		got.Objects[0].Status != ObjectSucceeded || got.Objects[1].Status != ObjectFailed {
		t.Fatalf("recovery of a known partial apply: %+v %v", got, err)
	}
	// The aggregate FAILED coexists with a changed live tree: the first file is final, the second baseline.
	a, errA := os.ReadFile(filepath.Join(f.root, "live", "a.txt"))
	b, errB := os.ReadFile(filepath.Join(f.root, "live", "b.txt"))
	if errA != nil || errB != nil || string(a) != "final-a" || string(b) != "before-b" {
		t.Fatalf("live tree after a known partial apply: %q %q %v %v", a, b, errA, errB)
	}
	// The terminal record is durable: a later recovery returns the same observation.
	again, err := second.Recover(f.live, result.TransactionID, nil)
	if err != nil || again.Status != StatusFailed || !reflect.DeepEqual(again.Objects, got.Objects) {
		t.Fatalf("repeated recovery: %+v %v", again, err)
	}
}
