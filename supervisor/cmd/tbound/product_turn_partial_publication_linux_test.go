//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/publication"
)

// Audit #13: this is an UNKNOWN interruption test, not a test of the aggregate FAILED branch. A
// fault-cut publication makes Finalize return UNKNOWN with the injected error, so the driver stops at
// the "publication outcome is not established" branch before it compares the project with the committed
// baseline. Live Finalize never returns FAILED (only Recover does), and the driver has no seam to stub
// the publisher, so the FAILED branch plus the digest comparison that follows it are covered only by
// TestProductTurnDriverRecordsAnEditAfterPublicationAsUnknown, which shares that comparison.
func TestProductTurnDriverRecordsAnInterruptedTwoFilePublicationAsUnknown(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	baseline := f.measure(t)
	admission := f.submit(t, "turn-1", productDriverPromptA)
	f.env.publicationFaults = func(point string, index int) error {
		if point == "after-object-start-checkpoint" && index == 1 {
			return errors.New("interrupt before the second object")
		}
		return nil
	}
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{
		{ResponseID: "r-a", CallID: "call-a", Tool: "write", Arguments: json.RawMessage(`{"path":"a.txt","content":"final-a\n"}`)},
		{ResponseID: "r-b", CallID: "call-b", Tool: "write", Arguments: json.RawMessage(`{"path":"b.txt","content":"final-b\n"}`)},
		{ResponseID: "r-final", Text: productDriverAnswerA},
	}}
	_, turn, err := f.run(t, admission, script)
	if !errors.Is(err, publication.ErrInjectedFault) || turn.State != productsession.StateUnknown ||
		turn.UnknownReason != "the publication outcome is not established" || !f.currentSession(t).Blocked {
		t.Fatalf("interrupted publication: turn=%+v err=%v", turn, err)
	}
	if got, err := os.ReadFile(filepath.Join(f.projectDir, "a.txt")); err != nil || string(got) != "final-a\n" {
		t.Fatalf("object 0 was not applied: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(f.projectDir, "b.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object 1 exists: %v", err)
	}
	if f.measure(t) == baseline {
		t.Fatal("the project still measures as the admitted baseline after a partial apply")
	}
}
