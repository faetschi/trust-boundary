package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/wsoracle"
)

func TestGovernedRetainsEvaluatedWSInput(t *testing.T) {
	f := newGovernedFixture(t)
	f.g.Collectors = &e04Collectors{in: wsoracle.EvaluateE04Input{}}
	f.g.WSEvidenceDir = t.TempDir()
	f.g.ProtectedPaths = func(string) []string { return []string{"protected"} }
	ref := EvidenceRef{Step: f.step, Interval: "retained-P"}
	var o Outcome
	if errs := f.g.collect(context.Background(), ref, &o); len(errs) != 0 {
		t.Fatal(errs)
	}
	raw, err := os.ReadFile(filepath.Join(f.g.WSEvidenceDir, RetainedWSName(ref.Interval)))
	if err != nil {
		t.Fatal(err)
	}
	var retained RetainedWS
	if err := DecodeEvidenceDoc(raw, &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Input.ThreatClass != string(f.step.ThreatClass) || len(retained.Input.NoCreditPaths) != 1 {
		t.Fatalf("host adjustments not retained: %+v", retained)
	}
	if err := VerifyWS(raw, ref.Interval, OracleWS02, o.OracleRefs[OracleWS02], o.Oracles[OracleWS02]); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RetainedWS){
		func(r *RetainedWS) { r.Input.ThreatClass = "other" },
		func(r *RetainedWS) { r.Interval = "other" },
	} {
		r := retained
		mutate(&r)
		b, _ := json.Marshal(r)
		if err := VerifyWS(b, ref.Interval, OracleWS02, o.OracleRefs[OracleWS02], o.Oracles[OracleWS02]); err == nil {
			t.Fatal("tampered WS input verified")
		}
	}
}
