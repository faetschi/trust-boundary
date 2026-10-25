package panelaudit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

func sum(raw []byte) string {
	s := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(s[:])
}

// fixture is a signed manifest of four cases, two of them on the audit list, with the panel
// files it is bound to.
type fixture struct {
	m                     evalcorpus.Manifest
	receipt, audit, stats map[string]any
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	m := evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: "s"}
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindAmbiguous,
			Signoff: evalcorpus.Signoff{Reviewer: "advisor", Human: true, Decision: "accept"}})
	}
	content, err := evalcorpus.ContentDigest(m)
	if err != nil {
		t.Fatal(err)
	}
	signed := func(id string) map[string]any {
		return map[string]any{"case_id": id, "reasons": []string{"ambiguous case"}, "advisor_signoff": map[string]string{
			"decision": "accept", "resolution": "defensible under the frozen rubric", "identity": "advisor", "signed_at": "2026-10-25T10:00:00+02:00"}}
	}
	f := &fixture{m: m,
		receipt: map[string]any{"schema": ReceiptSchema, "dry_run": false, "provider": "openrouter", "content_digest": content, "cases": 4, "complete_cases": 4},
		audit: map[string]any{"schema": AuditSchema, "dry_run": false, "provider": "openrouter", "content_digest": content,
			"cases": []any{signed("c2"), signed("c4")}}}
	for _, i := range []int{1, 3} {
		f.m.Cases[i].Signoff.Audited, f.m.Cases[i].Signoff.Resolution = true, "defensible under the frozen rubric"
	}
	f.stats = map[string]any{"schema": StatsSchema, "content_digest": content, "cases": 4, "complete_cases": 4, "items": map[string]any{}}
	for _, item := range Checklist {
		f.stats["items"].(map[string]any)[item.Key] = map[string]any{"gate": GatePass}
	}
	return f
}

// check seals the construct review of the manifest to the two documents and verifies.
func (f *fixture) check(t *testing.T) error {
	t.Helper()
	r, a := marshal(t, f.receipt), marshal(t, f.audit)
	f.m.ConstructReview = evalcorpus.ConstructReview{ContentDigest: f.receipt["content_digest"].(string), PanelReceiptDigest: sum(r), PanelAuditDigest: sum(a), PanelStatsDigest: sum(marshal(t, f.stats))}
	return Verify(f.m, r, a, marshal(t, f.stats))
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *fixture) listed(i int) map[string]any { return f.audit["cases"].([]any)[i].(map[string]any) }

func TestVerifyAcceptsTheSignedAudit(t *testing.T) {
	if err := newFixture(t).check(t); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(f *fixture)
		want   string // substring of the refusal
	}{
		{"a case the audit lists is not audited in the manifest", func(f *fixture) {
			f.m.Cases[1].Signoff.Audited, f.m.Cases[1].Signoff.Resolution = false, ""
		}, "c2"},
		{"an audited case the audit does not list", func(f *fixture) {
			f.m.Cases[2].Signoff.Audited, f.m.Cases[2].Signoff.Resolution = true, "r"
		}, "c3"},
		{"an audited case without a resolution", func(f *fixture) { f.m.Cases[1].Signoff.Resolution = " " }, "c2"},
		{"the audit lists a case the manifest lacks", func(f *fixture) {
			f.audit["cases"] = append(f.audit["cases"].([]any), map[string]any{"case_id": "c9", "advisor_signoff": f.listed(0)["advisor_signoff"]})
		}, "c9"},
		{"the audit lists a case twice", func(f *fixture) { f.audit["cases"] = append(f.audit["cases"].([]any), f.listed(0)) }, "twice"},
		{"blank decision", func(f *fixture) { f.listed(0)["advisor_signoff"].(map[string]string)["decision"] = "" }, "decision"},
		{"blank resolution", func(f *fixture) { f.listed(1)["advisor_signoff"].(map[string]string)["resolution"] = " \t" }, "resolution"},
		{"blank identity", func(f *fixture) { f.listed(0)["advisor_signoff"].(map[string]string)["identity"] = "" }, "identity"},
		{"blank signed_at", func(f *fixture) { f.listed(0)["advisor_signoff"].(map[string]string)["signed_at"] = "" }, "signed_at"},
		{"placeholder identity", func(f *fixture) { f.listed(0)["advisor_signoff"].(map[string]string)["identity"] = "TODO-ADVISOR" }, "identity"},
		{"revise decision", func(f *fixture) { f.listed(0)["advisor_signoff"].(map[string]string)["decision"] = "revise" }, "revise"},
		{"reject decision", func(f *fixture) { f.listed(0)["advisor_signoff"].(map[string]string)["decision"] = "reject" }, "reject"},
		{"receipt of another content", func(f *fixture) { f.receipt["content_digest"] = sum([]byte("other")) }, "receipt"},
		{"audit of another content", func(f *fixture) { f.audit["content_digest"] = sum([]byte("other")) }, "audit"},
		{"dry-run receipt", func(f *fixture) { f.receipt["dry_run"] = true }, "dry run"},
		{"dry-run audit", func(f *fixture) { f.audit["dry_run"] = true }, "dry run"},
		{"incomplete ratings", func(f *fixture) { f.receipt["complete_cases"] = 3 }, "complete"},
		{"receipt for another case count", func(f *fixture) { f.receipt["cases"], f.receipt["complete_cases"] = 5, 5 }, "cases"},
		{"receipt schema", func(f *fixture) { f.receipt["schema"] = "tbound-panel-receipt/v2" }, "schema"},
		{"audit schema", func(f *fixture) { f.audit["schema"] = "tbound-panel-audit/v2" }, "schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.mutate(f)
			err := f.check(t)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want a refusal naming %q", err, tc.want)
			}
		})
	}
}

func TestVerifyDigestsOfTheFileBytes(t *testing.T) {
	f := newFixture(t)
	r, a := marshal(t, f.receipt), marshal(t, f.audit)
	if err := f.check(t); err != nil {
		t.Fatal(err)
	}
	// One byte of either file changes its digest, whatever the JSON means.
	if err := Verify(f.m, append(r, '\n'), a, marshal(t, f.stats)); err == nil || !strings.Contains(err.Error(), "receipt") {
		t.Fatalf("changed receipt bytes: %v", err)
	}
	if err := Verify(f.m, r, append(a, '\n'), marshal(t, f.stats)); err == nil || !strings.Contains(err.Error(), "audit") {
		t.Fatalf("changed audit bytes: %v", err)
	}
	// The files rate the content they were made from: editing a case afterwards detaches them.
	edited := f.m
	edited.Cases = append([]evalcorpus.Case(nil), f.m.Cases...)
	edited.Cases[0].Ambiguous = &evalcorpus.Ambiguous{WorkspaceFixtureDigest: sum([]byte("edited"))}
	if err := Verify(edited, r, a, marshal(t, f.stats)); err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("edited case content: %v", err)
	}
}

func TestVerifyRefusesAmbiguousJSON(t *testing.T) {
	f := newFixture(t)
	good := marshal(t, f.audit)
	for name, bad := range map[string][]byte{
		"duplicate member":  bytes.Replace(good, []byte(`"provider": "openrouter",`), []byte(`"provider": "openrouter", "provider": "fake",`), 1),
		"case-folded twin":  bytes.Replace(good, []byte(`"provider": "openrouter",`), []byte(`"provider": "openrouter", "Provider": "fake",`), 1),
		"trailing data":     append(append([]byte(nil), good...), []byte(" {}")...),
		"not json":          []byte("{not json"),
		"larger than bound": append(bytes.Repeat([]byte(" "), MaxFileBytes), good...),
	} {
		t.Run(name, func(t *testing.T) {
			r := marshal(t, f.receipt)
			f.m.ConstructReview = evalcorpus.ConstructReview{ContentDigest: f.receipt["content_digest"].(string), PanelReceiptDigest: sum(r), PanelAuditDigest: sum(bad), PanelStatsDigest: sum(marshal(t, f.stats))}
			if err := Verify(f.m, r, bad, marshal(t, f.stats)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestVerifyStatsGate(t *testing.T) {
	for _, verdict := range []string{"revise", "stop", "no-pass-undefined-kappa", "incomplete-no-pass", "", "PASS"} {
		t.Run(verdict, func(t *testing.T) {
			f := newFixture(t)
			f.stats["items"].(map[string]any)["intent"] = map[string]string{"gate": verdict}
			if err := f.check(t); err == nil || !strings.Contains(err.Error(), "intent") {
				t.Fatalf("gate %q: %v", verdict, err)
			}
		})
	}
	for _, field := range []string{"missing_pairs", "incomplete_cases", "content_digest", "schema", "items"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			switch field {
			case "missing_pairs":
				f.stats[field] = []any{map[string]any{"phase": 1, "case_id": "c1", "model": "m"}}
			case "incomplete_cases":
				f.stats[field] = []string{"c1"}
			default:
				delete(f.stats, field)
			}
			if err := f.check(t); err == nil {
				t.Fatal("accepted invalid stats", field)
			}
		})
	}
}

func TestVerifyAdvisorMatchesManifest(t *testing.T) {
	f := newFixture(t)
	f.listed(0)["advisor_signoff"].(map[string]string)["identity"] = "another advisor"
	if err := f.check(t); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("different identity accepted: %v", err)
	}
	f.listed(0)["advisor_signoff"].(map[string]string)["identity"] = "  ADVISOR \t"
	if err := f.check(t); err != nil {
		t.Fatalf("normalized identity refused: %v", err)
	}
}
