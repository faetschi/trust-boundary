package evalreport

import (
	"testing"

	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/usdledger"
)

// Reproductions of the four confirmed adversarial-review findings. Each failed
// before the fail-closed redesign. Synthetic fixtures; they verify code only.

// Finding 1: two identical records of an unregistered ID that both record an
// observed escape must not yield a passing report.
func TestReproUnregisteredDuplicateEscapeCannotPass(t *testing.T) {
	in := fullInputs()
	ghost := cleanAdv("ghost")
	ghost.CEffect = evalscore.CEffectObserved
	in.Trials.Adversarial = append(in.Trials.Adversarial, ghost, ghost)
	_, err := Build(in)
	inputErr(t, err, ErrUnregisteredRecord, []string{"ghost"}, nil)
}

// Finding 2: a duplicated registered record with an observed escape must not be
// withheld (which erased the escape and shrank the PVR denominator to 298).
func TestReproRegisteredDuplicateKeepsObservedEscape(t *testing.T) {
	in := fullInputs()
	in.Trials.Adversarial[7].CEffect = evalscore.CEffectObserved
	in.Trials.Adversarial = append(in.Trials.Adversarial, in.Trials.Adversarial[7])
	_, err := Build(in)
	inputErr(t, err, ErrDuplicatePrimary, []string{"adv-007"}, nil)
}

// Finding 3: the ledger binding must cover MinPerUnitWorstCaseMicroUSD.
func TestReproDigestBindsMinPerUnit(t *testing.T) {
	a, b := smallInputs(), smallInputs()
	b.Ledger.MinPerUnitWorstCaseMicroUSD++
	if mustBuild(t, a).Digest == mustBuild(t, b).Digest {
		t.Fatal("report digest ignores MinPerUnitWorstCaseMicroUSD")
	}
}

// Finding 4: a malformed snapshot with a negative balance must not report
// within budget.
func TestReproNegativeBalanceSnapshotRejected(t *testing.T) {
	in := smallInputs()
	in.Ledger = usdledger.Snapshot{
		LedgerID: "research-synthetic", CapMicroUSD: RegisteredCapMicroUSD, MinPerUnitWorstCaseMicroUSD: 2_583,
		SettledMicroUSD: 6_000_000, OpenMicroUSD: -2_000_000, CommittedMicroUSD: 4_000_000, RemainingMicroUSD: 1_000_000,
		Records: 1, HeadSHA256: synthHead,
	}
	r, err := Build(in)
	if err == nil {
		t.Fatalf("Build accepted a negative open balance: within_budget=%v", r.Spend.WithinBudget)
	}
}
