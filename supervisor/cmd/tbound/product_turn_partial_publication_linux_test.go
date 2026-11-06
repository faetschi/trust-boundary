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
)

// Audit #13: aggregate publication FAILED may include a known partial apply, so a consumer must
// re-check the baseline. The product driver does so after every publication outcome
// (product_turn_linux.go, the MeasureProjectBaseline comparison): a project that is neither the
// published tree nor the admitted baseline ends the turn UNKNOWN and blocks the session. Live
// Finalize reports a fault-cut publication as UNKNOWN (only Recover yields FAILED), so this test
// drives the same partial state through the driver's UNKNOWN branch: object 0 applied, object 1 not.
func TestProductTurnDriverBlocksOnAPartialTwoFilePublication(t *testing.T) {
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
	if err == nil || turn.State != productsession.StateUnknown || !f.currentSession(t).Blocked {
		t.Fatalf("partial publication: turn=%+v err=%v", turn, err)
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
