package evalscore

import (
	"encoding/json"
	"strings"
	"testing"
)

// The original attempt of a rerun unit is kept and reported (owner decision
// 2026-10-17). Synthetic fixtures; these check code paths only.

func TestRerunOriginalIsRetainedInSummaryOutput(t *testing.T) {
	in := cleanInput()
	in.Adversarial[0] = outageOriginal(in.Adversarial[0])
	in.Adversarial = append(in.Adversarial, retry(in.Adversarial[0], 2))
	s := mustSummarize(t, in)

	got := caseOf(t, s, "adversarial", "adv-000")
	if got.Original == nil {
		t.Fatal("original attempt missing from the case outcome")
	}
	want := OriginalAttempt{
		Attempt: 1, Disposition: string(ExternalInfrastructureRerun), Reason: ReasonExternalOutage,
		OutageEvidenceDigest: synthOutageEvidence,
	}
	if *got.Original != want {
		t.Fatalf("original = %+v, want %+v", *got.Original, want)
	}
	raw := string(mustSummaryJSON(t, in))
	if !strings.Contains(raw, `"original": {`) || !strings.Contains(raw, synthOutageEvidence) {
		t.Fatalf("summary JSON does not carry the original: %s", raw)
	}
	// A unit without a rerun carries no original key.
	var plain struct {
		Cases []map[string]any `json:"cases"`
	}
	if err := json.Unmarshal(mustSummaryJSON(t, in), &plain); err != nil {
		t.Fatal(err)
	}
	for _, c := range plain.Cases {
		_, has := c["original"]
		if has != (c["id"] == "adv-000") {
			t.Fatalf("case %v: original key present = %v", c["id"], has)
		}
	}

	// Compliant side.
	cin := cleanInput()
	task := cleanCompliant("task-000")
	task.EvidenceLoss = LossExternalOutage
	task.OutageEvidenceDigest = synthOutageEvidence
	cin.Compliant[0] = task
	again := cleanCompliant("task-000")
	again.Attempt = 2
	cin.Compliant = append(cin.Compliant, again)
	cs := mustSummarize(t, cin)
	if c := caseOf(t, cs, "compliant", "task-000"); c.Original == nil || c.Attempts != 2 || c.Disposition != string(ValidPass) {
		t.Fatalf("compliant case = %+v", c)
	}
}
