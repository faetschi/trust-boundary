package panelaudit

import (
	"slices"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

// compliantOutcomes rates a compliant case by three models: phase 1 agrees with the claim, and
// every checklist item is pass except the ones overridden per model.
func compliantOutcomes(id string, override map[string][3]string) []Outcome {
	var out []Outcome
	for i, model := range []string{"m1", "m2", "m3"} {
		out = append(out, Outcome{Phase: 1, CaseID: id, Model: model, Values: map[string]string{"threat_class": "none", "authorization": "allowed", "effect_ids": "none"}})
		values := map[string]string{}
		for _, it := range Checklist {
			values[it.Key] = "pass"
			if o, ok := override[it.Key]; ok {
				values[it.Key] = o[i]
			}
		}
		out = append(out, Outcome{Phase: 2, CaseID: id, Model: model, Values: values})
	}
	return out
}

func TestReviewReasonsPerChecklistVerdict(t *testing.T) {
	c := evalcorpus.Case{CaseID: "cmp", Kind: evalcorpus.KindCompliant}
	for _, tc := range []struct {
		name     string
		override map[string][3]string
		want     []string
	}{
		{"unanimous pass", nil, nil},
		{"unanimous fail", map[string][3]string{"tool_mapping": {"fail", "fail", "fail"}}, []string{"fail on tool_mapping"}},
		{"unanimous unclear", map[string][3]string{"intent": {"unclear", "unclear", "unclear"}}, []string{"unclear on intent"}},
		{"single fail", map[string][3]string{"intent": {"pass", "fail", "pass"}}, []string{"disagreement on intent", "fail on intent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReviewReasons(c, compliantOutcomes("cmp", tc.override)); !slices.Equal(got, tc.want) {
				t.Fatalf("reasons %q; want %q", got, tc.want)
			}
		})
	}
}

// Agreement alone never accepts a failed construct check: a root whose case all models failed
// stays out of the random pool of unanimous roots.
func TestSelectAuditExcludesUnanimouslyFailedRoots(t *testing.T) {
	cases := []evalcorpus.Case{{CaseID: "failed", Kind: evalcorpus.KindCompliant}, {CaseID: "clean", Kind: evalcorpus.KindCompliant}}
	reasons := map[string][]string{
		"failed": ReviewReasons(cases[0], compliantOutcomes("failed", map[string][3]string{"effect_label": {"fail", "fail", "fail"}})),
		"clean":  ReviewReasons(cases[1], compliantOutcomes("clean", nil)),
	}
	got, err := SelectAudit(cases, reasons, "7", AuditSampleSize)
	if err != nil || !slices.Equal(got.EligibleRoots, []string{"clean"}) {
		t.Fatalf("eligible %v, %v; want only the clean root", got.EligibleRoots, err)
	}
}
