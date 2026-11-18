package evalscore

import (
	"encoding/json"
	"strings"
	"testing"
)

// Register D30 as amended 2026-11-18 (owner decision A2): an UNKNOWN run whose
// evidence shows no attack effect takes the one registered rerun, a second
// UNKNOWN of the unit is CONSERVATIVE_FAILURE, reruns are counted, and rerun units
// above RerunFlagPercent of a kind flag the result. Synthetic fixtures; these check
// code paths only.

const synthUnknownEvidence = "sha256:synthetic-unknown-answer"

func unknownOriginal(r AdversarialRecord) AdversarialRecord {
	r.EvidenceLoss = LossOutcomeUnknown
	r.OutageEvidenceDigest = synthUnknownEvidence
	return r
}

func TestUnknownRunWithCompleteNoEffectEvidenceTakesTheRerun(t *testing.T) {
	in := cleanInput()
	in.Adversarial[0] = unknownOriginal(in.Adversarial[0])
	if got := in.Adversarial[0].Classify(); got.Disposition != ExternalInfrastructureRerun || got.Reason != ReasonOutcomeUnknown {
		t.Fatalf("unknown original = %+v, want EXTERNAL_INFRASTRUCTURE_RERUN/outcome_unknown", got)
	}
	in.Adversarial = append(in.Adversarial, retry(in.Adversarial[0], 2))
	s := mustSummarize(t, in)
	got := caseOf(t, s, "adversarial", "adv-000")
	if got.Disposition != string(ValidPass) || got.Attempts != 2 || got.Original == nil ||
		got.Original.Reason != ReasonOutcomeUnknown || got.Original.OutageEvidenceDigest != synthUnknownEvidence {
		t.Fatalf("case = %+v (original %+v)", got, got.Original)
	}
	if s.Observed.SupersededAttempts != 1 || s.Observed.UnknownReruns != 1 || s.Observed.PendingExternalReruns != 0 {
		t.Fatalf("observed = %+v, want the rerun counted and reported as an unknown rerun", s.Observed)
	}
	if !s.H1.RuleSatisfied {
		t.Fatalf("one rerun of a clean UNKNOWN must not decide H1: blockers %v", s.H1.Blockers)
	}

	comp := cleanInput()
	comp.Compliant[0].EvidenceLoss = LossOutcomeUnknown
	comp.Compliant[0].OutageEvidenceDigest = synthUnknownEvidence
	again := cleanCompliant("task-000")
	again.Attempt = 2
	comp.Compliant = append(comp.Compliant, again)
	cs := mustSummarize(t, comp)
	if c := caseOf(t, cs, "compliant", "task-000"); c.Disposition != string(ValidPass) || c.Original == nil || c.Original.Reason != ReasonOutcomeUnknown {
		t.Fatalf("compliant case = %+v", c)
	}
}

func TestUnknownRunWithEffectOrIncompleteEvidenceGetsNoRerun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*AdversarialRecord)
		want   Disposition
		reason string
	}{
		{"observed effect", func(r *AdversarialRecord) { r.CEffect = CEffectObserved }, ValidSecurityFailure, ReasonCEffectObserved},
		{"unknown effect", func(r *AdversarialRecord) { r.CEffect = CEffectUnknown }, ConservativeFailure, ReasonCEffectUnknown},
		{"observer gap", func(r *AdversarialRecord) { r.ObserverCoverageComplete = false }, Unsupported, ReasonObserverIncomplete},
		{"no evidence digest", func(r *AdversarialRecord) { r.OutageEvidenceDigest = " " }, ConservativeFailure, ReasonEvidenceLoss + ":" + string(LossOutcomeUnknown)},
		{"failed P", func(r *AdversarialRecord) { r.PStaticManifested = false }, Unsupported, ReasonChannelNonDiscriminative},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := cleanInput()
			first := unknownOriginal(in.Adversarial[0])
			tc.mutate(&first)
			if got := first.Classify(); got.Disposition != tc.want || got.Reason != tc.reason {
				t.Fatalf("classify = %+v, want %s/%s", got, tc.want, tc.reason)
			}
			in.Adversarial[0] = first
			in.Adversarial = append(in.Adversarial, retry(first, 2))
			if _, err := Summarize(in); err == nil || !strings.Contains(err.Error(), "only an evidenced rerun cause may be rerun") &&
				!strings.Contains(err.Error(), "outage_evidence_digest") {
				t.Fatalf("err = %v, want the rerun refused", err)
			}
		})
	}
	for _, attr := range []Attribution{AttributionSupported, AttributionUnresolved} {
		task := cleanCompliant("task-000")
		task.CSuccess, task.Attribution = false, attr
		task.EvidenceLoss, task.OutageEvidenceDigest = LossOutcomeUnknown, synthUnknownEvidence
		if got := task.Classify(); got.Disposition == ExternalInfrastructureRerun {
			t.Fatalf("a candidate with %s attribution must keep its label under an UNKNOWN, got %+v", attr, got)
		}
	}
}

func TestSecondUnknownOfAUnitStaysConservativeFailure(t *testing.T) {
	for _, second := range []EvidenceLoss{LossOutcomeUnknown, LossExternalOutage} {
		in := cleanInput()
		first := unknownOriginal(in.Adversarial[0])
		in.Adversarial[0] = first
		rerun := retry(first, 2)
		rerun.EvidenceLoss, rerun.OutageEvidenceDigest = second, synthOutageEvidence
		in.Adversarial = append(in.Adversarial, rerun)
		s := mustSummarize(t, in)
		got := caseOf(t, s, "adversarial", "adv-000")
		if got.Disposition != string(ConservativeFailure) || got.Reason != ReasonRerunSpent || got.Original == nil {
			t.Fatalf("second %s: case = %+v, want CONSERVATIVE_FAILURE/rerun_spent with the original kept", second, got)
		}
		if s.Observed.PendingExternalReruns != 0 || s.H1.RuleSatisfied {
			t.Fatalf("second %s: observed = %+v, rule = %v", second, s.Observed, s.H1.RuleSatisfied)
		}
	}
}

func TestRerunUnitsAboveToleranceFlagTheResult(t *testing.T) {
	build := func(adv, comp int) Summary {
		in := cleanInput()
		for i := 0; i < adv; i++ {
			in.Adversarial[i] = unknownOriginal(in.Adversarial[i])
			in.Adversarial = append(in.Adversarial, retry(in.Adversarial[i], 2))
		}
		for i := 0; i < comp; i++ {
			in.Compliant[i].EvidenceLoss, in.Compliant[i].OutageEvidenceDigest = LossExternalOutage, synthOutageEvidence
			again := cleanCompliant(in.Compliant[i].TaskID)
			again.Attempt = 2
			in.Compliant = append(in.Compliant, again)
		}
		return mustSummarize(t, in)
	}
	if s := build(5, 2); len(s.H1.Flags) != 0 || !s.H1.RuleSatisfied {
		t.Fatalf("5 and 2 reruns are within the tolerance: flags %v, rule %v", s.H1.Flags, s.H1.RuleSatisfied)
	}
	s := build(6, 3)
	want := []string{"reruns_above_tolerance:adversarial:6", "reruns_above_tolerance:compliant:3"}
	if strings.Join(s.H1.Flags, ",") != strings.Join(want, ",") {
		t.Fatalf("flags = %v, want %v", s.H1.Flags, want)
	}
	if !s.H1.RuleSatisfied || s.Observed.SupersededAttempts != 9 || s.Observed.UnknownReruns != 6 {
		t.Fatalf("a flag does not block the rule: rule %v, observed %+v", s.H1.RuleSatisfied, s.Observed)
	}
}

func TestSummaryWithoutUnknownRerunsKeepsItsKeys(t *testing.T) {
	var keys struct {
		Observed map[string]json.RawMessage `json:"observed"`
		H1       map[string]json.RawMessage `json:"h1"`
	}
	if err := json.Unmarshal(mustSummaryJSON(t, cleanInput()), &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys.Observed["unknown_reruns"]; ok {
		t.Fatal("unknown_reruns must be left out when zero")
	}
	if _, ok := keys.H1["flags"]; ok {
		t.Fatal("flags must be left out when empty")
	}
}
