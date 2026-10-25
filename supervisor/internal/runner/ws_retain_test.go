package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/wsoracle"
)

func TestRetainedWSOverOneMiBRoundTrips(t *testing.T) {
	var entries []wsoracle.Entry
	for i := 0; i < 900; i++ {
		entries = append(entries, wsoracle.Entry{Path: fmt.Sprintf("file-%04d", i), Type: wsoracle.TypeFile,
			Mode: 0o644, Nlink: 1, Ino: uint64(i + 2), Size: 1, SHA256: strings.Repeat("a", 64)})
	}
	manifest := func(name string) wsoracle.Manifest {
		return wsoracle.Manifest{Label: name, Complete: true, Dev: 1,
			Root: &wsoracle.Entry{Type: wsoracle.TypeDir, Mode: 0o755, Nlink: 2, Ino: 1}, Entries: entries}
	}
	in := wsoracle.EvaluateE04Input{ThreatClass: "TM-B", Baseline: manifest(wsoracle.PointBaseline), Scope: wsoracle.E04Scope{Whole: true}}
	for _, name := range []string{wsoracle.PointPostMaterialization, wsoracle.PointPostSettlement, wsoracle.PointPostTeardown,
		wsoracle.PointPrePublication, wsoracle.PointPostPublication, wsoracle.PointFinalCleanup} {
		in.Points = append(in.Points, wsoracle.E04Point{Name: name, Manifest: manifest(name), LedgerPrefixDigest: wsoracle.LedgerPrefixDigest(nil, 0)})
	}
	rep := wsoracle.EvaluateE04(in)
	if len(rep.Gaps) != 0 || OraclesFromE04(rep)[OracleWS02] != FindingNone {
		t.Fatalf("invalid test input: gaps=%v verdicts=%v", rep.Gaps, rep.Verdicts)
	}
	digest, err := rep.Digest()
	if err != nil {
		t.Fatal(err)
	}
	dir, interval := t.TempDir(), "large-workspace"
	if err := RetainWS(dir, interval, in); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, RetainedWSName(interval)))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 1<<20 || len(raw) > 1536<<10 {
		t.Fatalf("want retention just over 1 MiB, got %d bytes", len(raw))
	}
	t.Logf("retained %d bytes", len(raw))
	if err := VerifyWS(raw, interval, OracleWS02, OracleRef{Version: wsoracle.OracleVersion, Report: digest}, FindingNone); err != nil {
		t.Fatal(err)
	}
}

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
	ambiguous := bytes.Replace(raw, []byte(`"interval":`), []byte(`"interval":"foreign","Interval":`), 1)
	if err := VerifyWS(ambiguous, ref.Interval, OracleWS02, o.OracleRefs[OracleWS02], o.Oracles[OracleWS02]); err == nil {
		t.Fatal("case-folded interval members verified")
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
