package evalreport

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/evalscore"
)

// Register D30 as amended 2026-11-18, items 8 and 9: the report lists every rerun with its cause and
// shows the result flags. Synthetic fixtures; these check code paths only.
func TestReportListsRerunsWithTheirCause(t *testing.T) {
	in := smallInputs()
	first := cleanAdv("A-1")
	first.EvidenceLoss, first.OutageEvidenceDigest = evalscore.LossOutcomeUnknown, "sha256:synthetic-answer"
	second := cleanAdv("A-1")
	second.Attempt = 2
	in.Trials.Adversarial[0] = first
	in.Trials.Adversarial = append(in.Trials.Adversarial, second)
	r := mustBuild(t, in)
	if len(r.Reruns) != 1 || r.Reruns[0].ID != "A-1" || r.Reruns[0].Original == nil ||
		r.Reruns[0].Original.Reason != evalscore.ReasonOutcomeUnknown || r.Observed.UnknownReruns != 1 {
		t.Fatalf("reruns = %+v, observed %+v", r.Reruns, r.Observed)
	}
	md := r.Markdown()
	for _, want := range []string{"## Reruns (D30 as amended 2026-11-18)", "adversarial A-1: 2 attempts", "(outcome_unknown)", "- result flags: none"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	if plain := mustBuild(t, smallInputs()); plain.Reruns != nil {
		t.Fatalf("a report without reruns lists %+v", plain.Reruns)
	}
}
