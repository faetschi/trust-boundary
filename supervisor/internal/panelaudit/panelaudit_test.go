package panelaudit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
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
		receipt: map[string]any{"schema": ReceiptSchema, "dry_run": false, "provider": "openrouter", "content_digest": content, "cases": 4, "complete_cases": 4, "seeds": map[string]any{"audit_sample": "7", "blind_ids_and_order": 7, "bootstrap": 7}},
		audit: map[string]any{"schema": AuditSchema, "dry_run": false, "provider": "openrouter", "content_digest": content,
			"cases": []any{signed("c2"), signed("c4"), signed("c1"), signed("c3")}, "selection": Selection{Seed: "7", Purpose: AuditPurpose, Method: "evalcorpus.SelectSeeded", SampleSize: AuditSampleSize, SelectedRoots: []string{}}}}
	for _, i := range []int{0, 1, 2, 3} {
		f.m.Cases[i].Signoff.Audited, f.m.Cases[i].Signoff.Resolution = true, "defensible under the frozen rubric"
	}

	f.receipt["live_run"] = true
	models := []Model{{ID: "google/gemini-2.5-pro", MaxTokens: 4000, PromptUSDPerMTok: 1, CompletionUSDPerMTok: 1}, {ID: "qwen/qwen3-235b-a22b-2507", MaxTokens: 4000, PromptUSDPerMTok: 1, CompletionUSDPerMTok: 1}, {ID: "mistralai/mistral-large-2411", MaxTokens: 4000, PromptUSDPerMTok: 1, CompletionUSDPerMTok: 1}}
	f.receipt["models"] = models
	f.receipt["calls_recorded"] = 24
	var outcomes []Outcome
	for _, c := range f.m.Cases {
		for _, model := range models {
			for _, phase := range []int{1, 2} {
				values := map[string]string{"threat_class": "none", "authorization": "ambiguous", "effect_ids": "none"}
				if phase == 2 {
					values = map[string]string{}
					for _, it := range Checklist {
						if _, na := NotApplicable[c.Kind][it.Key]; !na {
							values[it.Key] = "pass"
						}
					}
				}
				outcomes = append(outcomes, Outcome{CaseID: c.CaseID, Model: model.ID, Phase: phase, Values: values})
			}
		}
	}
	f.receipt["rating_outcomes"] = outcomes
	f.audit["live_run"] = true
	f.stats = map[string]any{"schema": StatsSchema, "content_digest": content, "dry_run": false, "live_run": true, "provider": "openrouter", "cases": 4, "complete_cases": 4, "incomplete_cases": []string{}, "missing_pairs": []any{}, "items": map[string]any{}}
	for _, it := range Checklist {
		f.stats["items"].(map[string]any)[it.Key] = ItemStats{Subjects: 4, Roots: 4, AC1: 1, AC1Defined: true, Kappa: 1, KappaDefined: true, Agreement: 1, Gate: GatePass}
	}

	f.stats["models"] = []string{models[0].ID, models[1].ID, models[2].ID}
	return f
}

// check seals the construct review of the manifest to the two documents and verifies.
func (f *fixture) check(t *testing.T) error {
	t.Helper()
	r, a := marshal(t, f.receipt), marshal(t, f.audit)
	content, _ := evalcorpus.ContentDigest(f.m)
	f.m.ConstructReview = evalcorpus.ConstructReview{ContentDigest: content, PanelReceiptDigest: sum(r), PanelAuditDigest: sum(a), PanelStatsDigest: sum(marshal(t, f.stats))}
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
			f.audit["cases"] = f.audit["cases"].([]any)[:3]
		}, "c3"},
		{"an audited case without a resolution", func(f *fixture) { f.m.Cases[1].Signoff.Resolution = " " }, "c2"},
		{"the audit lists a case the manifest lacks", func(f *fixture) {
			f.audit["cases"] = append(f.audit["cases"].([]any), map[string]any{"case_id": "c9", "reasons": []string{"ambiguous case"}, "advisor_signoff": f.listed(0)["advisor_signoff"]})
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
			content, _ := evalcorpus.ContentDigest(f.m)
			f.m.ConstructReview = evalcorpus.ConstructReview{ContentDigest: content, PanelReceiptDigest: sum(r), PanelAuditDigest: sum(bad), PanelStatsDigest: sum(marshal(t, f.stats))}
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
			st := f.stats["items"].(map[string]any)["intent"].(ItemStats)
			st.Gate = verdict
			f.stats["items"].(map[string]any)["intent"] = st
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

func TestVerifyRefusesUnverifiableAuditSelection(t *testing.T) {
	f := newFixture(t)
	f.audit["selection"] = map[string]any{"seed": "7", "purpose": "tbound-panel/unanimous-audit", "method": "evalcorpus.SelectSeeded", "sample_size": 1, "eligible_roots": []string{"c1", "c3"}, "selected_roots": []string{"c1"}}
	if err := f.check(t); err == nil || !strings.Contains(err.Error(), "selection") {
		t.Fatalf("undersized invented sample accepted: %v", err)
	}
}

func TestSelectAuditUsesWholeRootsAndRegisteredSample(t *testing.T) {
	for _, n := range []int{0, 3, 80} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var cases []evalcorpus.Case
			var roots []string
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("root-%03d", i)
				roots = append(roots, id)
				cases = append(cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindCompliant})
			}
			got, err := SelectAudit(cases, nil, "7", AuditSampleSize)
			want, _ := evalcorpus.SelectSeeded("7", AuditPurpose, roots, min(60, n))
			if err != nil || !slices.Equal(got.SelectedRoots, want) {
				t.Fatalf("%+v %v; want %v", got, err, want)
			}
		})
	}
	cases := []evalcorpus.Case{
		{CaseID: "a", Kind: evalcorpus.KindCompliant, MeaningRecord: &evalcorpus.MeaningRecord{RootID: "shared"}},
		{CaseID: "b", Kind: evalcorpus.KindCompliant, MeaningRecord: &evalcorpus.MeaningRecord{RootID: "shared"}},
	}
	got, err := SelectAudit(cases, map[string][]string{"b": {"unclear on intent"}}, "7", AuditSampleSize)
	if err != nil || len(got.EligibleRoots) != 0 || len(got.SelectedRoots) != 0 {
		t.Fatalf("non-unanimous root selected: %+v %v", got, err)
	}
}

func TestVerifySelectionTampering(t *testing.T) {
	for _, field := range []string{"seed", "purpose", "method", "eligible_roots", "selected_roots", "sample_size"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			raw := marshal(t, f.audit["selection"])
			var sel map[string]any
			json.Unmarshal(raw, &sel)
			switch field {
			case "eligible_roots", "selected_roots":
				sel[field] = []string{"invented"}
			case "sample_size":
				sel[field] = 59
			default:
				sel[field] = "other"
			}
			f.audit["selection"] = sel
			if err := f.check(t); err == nil || !strings.Contains(err.Error(), "selection") {
				t.Fatalf("tampered %s: %v", field, err)
			}
		})
	}
}

func TestVerifyRejectsForgedCompletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
	}{
		{"zero statistics declared pass", func(f *fixture) {
			f.stats["items"].(map[string]any)["intent"] = ItemStats{Subjects: 4, Roots: 4, Gate: GatePass, AC1: 0, AC1Defined: true, Kappa: 0, Agreement: 0, KappaDefined: true}
		}},
		{"undefined kappa declared pass", func(f *fixture) {
			f.stats["items"].(map[string]any)["intent"] = ItemStats{Subjects: 4, Roots: 4, Gate: GatePass, AC1: 1, AC1Defined: false, Kappa: 1, Agreement: 1, KappaDefined: false}
		}},
		{"missing statistic", func(f *fixture) { f.stats["items"].(map[string]any)["intent"] = map[string]any{"gate": GatePass} }},
		{"dropped mandatory ambiguous case", func(f *fixture) {
			f.audit["cases"] = f.audit["cases"].([]any)[1:]
			f.m.Cases[1].Signoff.Audited = false
			f.m.Cases[1].Signoff.Resolution = ""
		}},
		{"empty model panel", func(f *fixture) { f.receipt["models"] = []any{}; f.receipt["calls_recorded"] = 0 }},
		{"fake provider", func(f *fixture) { f.receipt["provider"] = "fake" }},
		{"missing dry run marker", func(f *fixture) { delete(f.receipt, "dry_run") }},
		{"missing live run marker", func(f *fixture) { delete(f.receipt, "live_run") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.change(f)
			if err := f.check(t); err == nil {
				t.Fatal("accepted forged completion")
			}
		})
	}
}

func TestVerifyRequiresEveryEvidenceMember(t *testing.T) {
	for _, doc := range []string{"receipt", "audit", "stats", "item"} {
		f := newFixture(t)
		var fields map[string]any
		switch doc {
		case "receipt":
			fields = f.receipt
		case "audit":
			fields = f.audit
		case "stats":
			fields = f.stats
		case "item":
			json.Unmarshal(marshal(t, f.stats["items"].(map[string]any)["intent"]), &fields)
		}
		for field := range fields {
			t.Run(doc+"/"+field, func(t *testing.T) {
				fresh := newFixture(t)
				switch doc {
				case "receipt":
					delete(fresh.receipt, field)
				case "audit":
					delete(fresh.audit, field)
				case "stats":
					delete(fresh.stats, field)
				case "item":
					var item map[string]any
					json.Unmarshal(marshal(t, fresh.stats["items"].(map[string]any)["intent"]), &item)
					delete(item, field)
					fresh.stats["items"].(map[string]any)["intent"] = item
				}
				if err := fresh.check(t); err == nil {
					t.Fatal("accepted missing", doc, field)
				}
			})
		}
	}
}
func TestVerifyCoverageAndFamilies(t *testing.T) {
	for _, name := range []string{"missing outcome", "duplicate outcome", "wrong case", "wrong model", "wrong phase", "missing checklist", "missing blind value", "too few calls", "same family", "excluded family", "moving alias", "low agreement", "fake stats", "fake audit", "not live"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			out := f.receipt["rating_outcomes"].([]Outcome)
			models := f.receipt["models"].([]Model)
			switch name {
			case "missing outcome":
				f.receipt["rating_outcomes"] = out[1:]
			case "duplicate outcome":
				out[1] = out[0]
			case "wrong case":
				out[0].CaseID = "unknown"
			case "wrong model":
				out[0].Model = "unknown"
			case "wrong phase":
				out[0].Phase = 3
			case "missing checklist":
				delete(out[1].Values, "intent")
			case "missing blind value":
				delete(out[0].Values, "authorization")
			case "too few calls":
				f.receipt["calls_recorded"] = 0
			case "same family":
				models[1].ID = "google/gemma-3-27b-it"
			case "excluded family":
				models[1].ID = "openai/gpt-4.1"
			case "moving alias":
				models[1].ID = "qwen/qwen-latest"
			case "low agreement":
				f.stats["items"].(map[string]any)["intent"] = ItemStats{Subjects: 4, Roots: 4, AC1: 0.9, AC1Defined: true, Kappa: 0.9, KappaDefined: true, Agreement: 0.89, Gate: GatePass}
			case "fake stats":
				f.stats["provider"] = "fake"
			case "fake audit":
				f.audit["provider"] = "fake"
			case "not live":
				f.receipt["live_run"] = false
			}
			if err := f.check(t); err == nil {
				t.Fatal("accepted", name)
			}
		})
	}
}

func TestVerifyCannotDropChecklistReview(t *testing.T) {
	for _, verdict := range []string{"fail", "unclear"} {
		t.Run(verdict, func(t *testing.T) {
			f := newFixture(t)
			// A mandatory sibling keeps this root out of the random pool even if c1 is dropped.
			f.m.Cases[0].Kind = evalcorpus.KindCompliant
			for _, i := range []int{0, 1} {
				f.m.Cases[i].MeaningRecord = &evalcorpus.MeaningRecord{RootID: "shared"}
			}
			out := f.receipt["rating_outcomes"].([]Outcome)
			first := true
			for i := range out {
				if out[i].CaseID == "c1" {
					if out[i].Phase == 1 {
						out[i].Values["authorization"] = "allowed"
					} else {
						out[i].Values["sensitivity"] = "pass"
						if first {
							out[i].Values["intent"] = verdict
							first = false
						}
					}
				}
			}
			content, _ := evalcorpus.ContentDigest(f.m)
			f.receipt["content_digest"] = content
			f.audit["content_digest"] = content
			f.stats["content_digest"] = content
			for _, ac := range f.audit["cases"].([]any) {
				a := ac.(map[string]any)
				if a["case_id"] == "c1" {
					a["reasons"] = ReviewReasons(f.m.Cases[0], out)
				}
			}
			if err := f.check(t); err != nil {
				t.Fatal("complete mandatory audit refused", err)
			}
			f.audit["cases"] = append(f.audit["cases"].([]any)[:2], f.audit["cases"].([]any)[3:]...)
			f.m.Cases[0].Signoff.Audited = false
			f.m.Cases[0].Signoff.Resolution = ""
			if err := f.check(t); err == nil || !strings.Contains(err.Error(), "mandatory") {
				t.Fatal("dropped checklist audit", err)
			}
		})
	}
}

func TestVerifyNumericRanges(t *testing.T) {
	for _, field := range []string{"kappa low", "kappa high", "agreement low", "agreement high", "no subjects", "no roots"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			st := f.stats["items"].(map[string]any)["intent"].(ItemStats)
			switch field {
			case "kappa low":
				st.Kappa = -2
			case "kappa high":
				st.Kappa = 2
			case "agreement low":
				st.Agreement = -1
			case "agreement high":
				st.Agreement = 2
			case "no subjects":
				st.Subjects = 0
			case "no roots":
				st.Roots = 0
			}
			f.stats["items"].(map[string]any)["intent"] = st
			if err := f.check(t); err == nil || !strings.Contains(err.Error(), "numeric") {
				t.Fatalf("invalid numeric statistics: %v", err)
			}
		})
	}
}
func TestVerifySelectionRejectsEmptyUnselectedAudit(t *testing.T) {
	var m evalcorpus.Manifest
	for i := 0; i < 80; i++ {
		m.Cases = append(m.Cases, evalcorpus.Case{CaseID: fmt.Sprintf("c%02d", i), Kind: evalcorpus.KindCompliant})
	}
	models := []string{"m1", "m2", "m3"}
	var receipt Receipt
	receipt.Seeds.AuditSample = "7"
	for _, c := range m.Cases {
		for _, model := range models {
			receipt.Outcomes = append(receipt.Outcomes, Outcome{1, c.CaseID, model, map[string]string{"threat_class": "none", "authorization": "allowed", "effect_ids": "none"}}, Outcome{2, c.CaseID, model, map[string]string{"intent": "pass", "class_or_difficulty": "pass", "tool_mapping": "pass", "effect_label": "pass", "expected_route": "pass", "sensitivity": "pass"}})
		}
	}
	selection, err := SelectAudit(m.Cases, nil, "7", AuditSampleSize)
	if err != nil {
		t.Fatal(err)
	}
	audit := Audit{Selection: selection}
	for _, c := range m.Cases {
		if slices.Contains(selection.SelectedRoots, c.CaseID) {
			audit.Cases = append(audit.Cases, AuditCase{CaseID: c.CaseID, Reasons: []string{RandomSampleReason}})
		}
	}
	if err := verifySelection(m, audit, receipt); err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Cases {
		if !slices.Contains(selection.SelectedRoots, c.CaseID) {
			audit.Cases = append(audit.Cases, AuditCase{CaseID: c.CaseID})
			break
		}
	}
	if err := verifySelection(m, audit, receipt); err == nil {
		t.Fatal("accepted an extra audit with no rating-derived reason or sample membership")
	}
}

func TestVerifyRejectsStatsFromDifferentPanel(t *testing.T) {
	f := newFixture(t)
	f.stats["models"] = []string{"google/gemini-2.5-pro", "qwen/qwen3-235b-a22b-2507", "meta-llama/llama-3.3-70b-instruct"}
	if err := f.check(t); err == nil {
		t.Fatal("accepted statistics from a different model panel")
	}
}
