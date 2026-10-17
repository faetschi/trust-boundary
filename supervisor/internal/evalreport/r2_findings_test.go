package evalreport

import (
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalscore"
)

// Round-2 review findings. Synthetic fixtures; these check code paths only.

func outageRerunPair(t *testing.T, in *Inputs, id string) {
	t.Helper()
	first := cleanAdv(id)
	first.EvidenceLoss = evalscore.LossExternalOutage
	first.OutageEvidenceDigest = "sha256:" + strings.Repeat("0", 64)
	second := cleanAdv(id)
	second.Attempt = 2
	for i := range in.Trials.Adversarial {
		if in.Trials.Adversarial[i].CaseID == id {
			in.Trials.Adversarial[i] = first
		}
	}
	in.Trials.Adversarial = append(in.Trials.Adversarial, second)
}

// An unrelated rejection (here a wrong trials schema) must not be reported as a
// duplicate primary just because the input also holds a valid rerun pair.
func TestUnrelatedScoringErrorIsNotReportedAsDuplicatePrimary(t *testing.T) {
	in := smallInputs()
	outageRerunPair(t, &in, "A-1")
	in.Trials.Schema = "wrong"
	_, err := Build(in)
	if err == nil {
		t.Fatal("wrong schema accepted")
	}
	if errors.Is(err, ErrDuplicatePrimary) {
		t.Fatalf("unrelated error labelled duplicate primary: %v", err)
	}
	var ie *InputError
	if !errors.As(err, &ie) || !errors.Is(err, ErrScoringRejected) {
		t.Fatalf("err = %v, want an *InputError of class ErrScoringRejected", err)
	}
	if len(ie.Adversarial) != 0 || len(ie.Compliant) != 0 {
		t.Fatalf("unrelated error names IDs: %+v", ie)
	}
	if !strings.Contains(err.Error(), `schema "wrong"`) {
		t.Fatalf("evalscore's message is lost: %v", err)
	}
	if errors.Is(err, ErrUnregisteredRecord) {
		t.Fatal("misreported as unregistered")
	}
}

// A real duplicate is still named, even next to an unrelated record-level
// rejection in another case, and an unrelated record is never named.
func TestDuplicateIsNamedOnlyForTheDuplicatedCase(t *testing.T) {
	in := smallInputs()
	outageRerunPair(t, &in, "A-1") // legitimate pair
	in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("B-1"))
	_, err := Build(in)
	inputErr(t, err, ErrDuplicatePrimary, []string{"B-1"}, nil)

	// A record-level defect inside a repeated case is not a duplicate problem.
	bad := smallInputs()
	outageRerunPair(t, &bad, "A-1")
	for i := range bad.Trials.Adversarial {
		if bad.Trials.Adversarial[i].CaseID == "A-1" && bad.Trials.Adversarial[i].Attempt == 2 {
			bad.Trials.Adversarial[i].Channel = ""
		}
	}
	_, err = Build(bad)
	if err == nil || errors.Is(err, ErrDuplicatePrimary) || !errors.Is(err, ErrScoringRejected) {
		t.Fatalf("empty channel in a rerun pair: err = %v, want ErrScoringRejected and not duplicate", err)
	}
}

// Forged snapshot: settled 6M but committed 1M, remaining and stop rule set to
// agree with committed. Only the committed = open+uncertain+settled check can
// reject it. It must never reach a report, let alone within_budget.
func TestForgedSnapshotCommittedBelowComponentsIsRejected(t *testing.T) {
	in := smallInputs()
	in.Ledger.OpenMicroUSD, in.Ledger.UncertainMicroUSD = 0, 0
	in.Ledger.SettledMicroUSD = 6_000_000
	in.Ledger.CommittedMicroUSD = 1_000_000
	in.Ledger.RemainingMicroUSD = in.Ledger.CapMicroUSD - 1_000_000
	in.Ledger.StopRule = in.Ledger.RemainingMicroUSD < in.Ledger.MinPerUnitWorstCaseMicroUSD
	r, err := Build(in)
	if err == nil {
		t.Fatalf("forged snapshot accepted: within_budget=%v spend=%+v", r.Spend.WithinBudget, r.Spend)
	}
	if !strings.Contains(err.Error(), "committed") {
		t.Fatalf("rejected for another reason: %v", err)
	}
	if r.Spend.WithinBudget {
		t.Fatal("rejected snapshot reported within_budget")
	}
}

// The threshold must lie in (0, cap]. A threshold above the cap with a
// consistent stop rule (remaining < threshold, so the rule is set) is rejected
// by the range check alone.
func TestSnapshotMinPerUnitAboveCapIsRejectedEvenWithConsistentStopRule(t *testing.T) {
	for _, min := range []int64{RegisteredCapMicroUSD + 1, 1 << 62} {
		in := smallInputs()
		in.Ledger.MinPerUnitWorstCaseMicroUSD = min
		in.Ledger.StopRule = true // remaining 3.75M < min
		_, err := Build(in)
		if err == nil {
			t.Fatalf("min per unit %d above cap %d accepted", min, in.Ledger.CapMicroUSD)
		}
		if !strings.Contains(err.Error(), "min per unit worst case") {
			t.Fatalf("min %d rejected for another reason: %v", min, err)
		}
	}
	// The cap itself is a valid threshold.
	in := smallInputs()
	in.Ledger.MinPerUnitWorstCaseMicroUSD = in.Ledger.CapMicroUSD
	in.Ledger.StopRule = true
	mustBuild(t, in)
}
