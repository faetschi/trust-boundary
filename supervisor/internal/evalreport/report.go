// Package evalreport assembles the thesis results-chapter report from three
// already-checked inputs: the evalcorpus manifest, the trial records scored by
// evalscore, and a usdledger snapshot. It adds a coverage check, per-stratum
// counts, a spend check, and a SHA-256 digest that binds the three inputs.
// Scoring is not re-implemented: AC-Security, AC-Friction, rule_satisfied and
// every rate are evalscore.Summarize output, copied unchanged.
//
// Fail-closed input handling: Build never filters, withholds or re-scores
// records. Every record goes to evalscore.Summarize, so every observed effect
// and every invalid record is retained (intention to test; observed-effect
// precedence, thesis sections 7.2.1 and 7.5). Build returns an error, and no
// report, when
//
//   - a record's ID is not a registered case of its kind (ErrUnregisteredRecord);
//   - a case has repeated primary records that evalscore does not permit, that
//     is, anything other than earlier attempts that are evidenced
//     external-outage reruns (ErrDuplicatePrimary; the rule itself is
//     evalscore's, it is not copied here);
//   - evalscore rejects the records for any other reason (ErrScoringRejected, with
//     evalscore's error as the cause); or
//   - the ledger snapshot is malformed (see checkSnapshot).
//
// The typed errors are *InputError values. The first two list the offending IDs. A
// registered case with no record is not an error: the report is produced with
// Coverage.Exact false, and H1.RuleSatisfied is forced to false whenever
// coverage is not exact. That is a fail-closed guard on the evalscore value,
// not a second scoring rule.
//
// Assumptions (each is an open thesis decision, marked "ASSUMPTION:" where it
// is applied):
//
//   - The caller has loaded the manifest with evalcorpus.Load and checked it
//     with evalcorpus.Validate. Build records the manifest digest and the case
//     coverage; it does not validate the manifest.
//   - Ambiguous cases have no evalscore record kind, so they are counted as
//     unscored and are not part of the coverage requirement. A trial record
//     whose ID is an ambiguous case is unregistered.
//   - Strata are the adversarial threat class by cold partition, and the
//     compliant primary tool.
//   - Spend is checked against the registered US$10 cap and the planned units of
//     the manifest (PlannedUnits), and the ledger unit total against the planned provider
//     calls (plannedLedgerUnits: the research route books one unit per provider call).
//     Permitted external reruns may exceed them, so exceeding them is flagged but does not by itself make WithinBudget false.
//
// No pooling (D48): top-up and TM-C cases (sample_part topup and tmc) never enter
// the H1 figures. Build scores them on their own and reports them in Separate,
// one stratum per sample part, threat class and partition; every H1 field,
// coverage and the strata are computed over the other cases. A manifest without
// such cases gives the same report, and the same digest, as before.
//   - The ledger snapshot may show committed spend above the cap (a recorded
//     overage), so RemainingMicroUSD may be negative; every other money and
//     unit field must be non-negative.
//
// Digest: the report digest is "sha256:" + hex(SHA-256(DigestDomain || 0x00 ||
// canonical JSON of the report without its digest field)). Canonical JSON here
// means compact JSON with object keys sorted (Go byte order), numbers as Go
// formats them, and no HTML escaping. It is deterministic but not RFC 8785 for
// exponent-form numbers or non-BMP key names. The report contains no
// timestamps. Manifest case order is part of evalcorpus's manifest digest;
// record order is not part of the trials digest. The ledger is bound twice:
// by ID, head and record count, and by Bindings.LedgerSnapshotDigest, a digest
// of the canonical JSON of the whole usdledger.Snapshot, so a change to any
// snapshot field, including MinPerUnitWorstCaseMicroUSD, changes the report
// digest.
//
// Tests use synthetic fixtures. They verify code only and are not evidence
// about G1, H1, or any thesis claim.
package evalreport

import (
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/usdledger"
)

// Schema and digest domains.
const (
	Schema             = "tbound-eval-report/v1"
	DigestDomain       = "tbound-eval-report/v1"
	TrialsDigestDomain = "tbound-eval-trials/v1"
	LedgerDigestDomain = "tbound-eval-ledger/v1"

	// RegisteredCapMicroUSD is the research cap (US$10).
	RegisteredCapMicroUSD int64 = 10_000_000

	// LegacyPlannedUnits is the pre-D48 planned execution-unit ledger (tbound-thesis.md
	// section 7.5, B and C only per the D58 amendment): 299x5 + 100x2 + 20x2 + 50x2. D53 item 4 withdrew it; it stays the
	// total of a manifest that pins no D48 draw, so those plans and reports keep their digests.
	LegacyPlannedUnits = 1835

	LegacyPlannedUnitsWithV = LegacyPlannedUnits + 100

	// The planned units of one case, as runner.Plan schedules them: an adversarial case
	// runs the C trajectory and the static and adaptive P/C replays, a compliant task B and C
	// (D58 amendment: no V), an ambiguous case B and C, and a variance case two more C repeats.
	AdversarialUnits = 5
	CompliantUnits   = 2
	CompliantUnitsV  = 3
	AmbiguousUnits   = 2
	VarianceUnits    = 2
)

// PlannedUnits is the registered planned-unit total of an allocated manifest, computed
// from its final counts (D53 item 4): the main sample, the top-up as drawn, the compliant
// set (with Configuration V when planV is on, D61 item 3), the ambiguous cases as passed, and the variance repeats. D53 item 4 does not name
// the TM-C set; TM-C cases in the manifest are planned like every adversarial case
// (runner.Plan), so they are counted here and the total equals the plan. A manifest that
// pins no D48 draw keeps LegacyPlannedUnits.
func PlannedUnits(m evalcorpus.Manifest, planV bool) int {
	if m.Pins.DrawDigest == "" {
		if planV {
			return LegacyPlannedUnitsWithV
		}
		return LegacyPlannedUnits
	}
	units := 0
	for _, c := range m.Cases {
		units += caseUnits(c, planV)
	}
	return units
}

// caseUnits is the number of execution units that runner.Plan schedules for one case.
func caseUnits(c evalcorpus.Case, planV bool) int {
	switch c.Kind {
	case evalcorpus.KindAdversarial:
		if c.Adversarial != nil && c.Adversarial.VarianceSubset {
			return AdversarialUnits + VarianceUnits
		}
		return AdversarialUnits
	case evalcorpus.KindCompliant:
		if planV {
			return CompliantUnitsV
		}
		return CompliantUnits
	case evalcorpus.KindAmbiguous:
		return AmbiguousUnits
	}
	return 0
}

// plannedLedgerUnits is the most ledger units a normal run books. The research route books one
// ledger unit per provider call, so a planned execution unit may book up to its case's
// max_host_calls. It is never below PlannedUnits, so no report that was unflagged becomes flagged.
func plannedLedgerUnits(m evalcorpus.Manifest, planV bool) int {
	calls := 0
	for _, c := range m.Cases {
		perUnit := 1 // a case without a trial books at least one call per unit
		if c.Trial != nil {
			perUnit = max(1, int(min(c.Trial.MaxHostCalls, 1<<24))) // clamp: no overflow
		}
		calls += caseUnits(c, planV) * perUnit
	}
	return max(PlannedUnits(m, planV), calls)
}

// Inputs are the three artifacts a report binds.
type Inputs struct {
	Manifest evalcorpus.Manifest
	Trials   evalscore.Input
	Ledger   usdledger.Snapshot
	// AttributionsDigest is the digest of the pinned attribution file and
	// AttributionRubricDigest the rubric that file names. Both are empty when
	// the trials carry no attribution input, and neither is empty otherwise.
	AttributionsDigest      string
	AttributionRubricDigest string
	// D48 gives the scenario and mechanism results (Report.Groups); nil leaves them out.
	D48   *D48Inputs
	PlanV bool
}

// Bindings tie the report to its inputs.
type Bindings struct {
	ManifestDigest string `json:"manifest_digest"`
	TrialsDigest   string `json:"trials_digest"`
	LedgerID       string `json:"ledger_id"`
	LedgerHead     string `json:"ledger_head_sha256"`
	LedgerRecords  uint64 `json:"ledger_records"`

	// LedgerSnapshotDigest is the domain-separated digest of the canonical JSON of
	// the entire usdledger.Snapshot.
	LedgerSnapshotDigest string `json:"ledger_snapshot_digest"`

	// AttributionsDigest and AttributionRubricDigest bind the attribution input
	// of the friction candidates (empty when there is none).
	AttributionsDigest      string `json:"attributions_digest"`
	AttributionRubricDigest string `json:"attribution_rubric_digest"`
}

// KindCoverage compares one kind of manifest case with its trial records.
type KindCoverage struct {
	Registered        int      `json:"registered"`
	Scored            int      `json:"scored"`
	Missing           []string `json:"missing"`
	ManifestDuplicate []string `json:"manifest_duplicate"`
}

// Coverage is the case-coverage check.
type Coverage struct {
	Exact             bool         `json:"exact"`
	Adversarial       KindCoverage `json:"adversarial"`
	Compliant         KindCoverage `json:"compliant"`
	UnscoredAmbiguous int          `json:"unscored_ambiguous"`
}

// Stratum holds the counts of one manifest stratum.
type Stratum struct {
	Key            string                      `json:"key"`
	Kind           string                      `json:"kind"`
	Registered     int                         `json:"registered"`
	Scored         int                         `json:"scored"`
	Missing        int                         `json:"missing"`
	InvalidRecords int                         `json:"invalid_records"`
	Dispositions   evalscore.DispositionCounts `json:"dispositions"`
}

// Calibration sums the P/C calibration manifestations over all channels.
type Calibration struct {
	Pairs              int `json:"pairs"`
	StaticManifested   int `json:"static_manifested"`
	AdaptiveManifested int `json:"adaptive_manifested"`
	BothManifested     int `json:"both_manifested"`
	NonDiscriminative  int `json:"non_discriminative"`
}

// Spend compares the ledger with the registered cap. PlannedUnits counts planned execution
// units; UnitsOverPlanned compares TotalUnits (ledger units, one per provider call on the
// research route) with the planned provider calls, not with PlannedUnits.
type Spend struct {
	LedgerID              string `json:"ledger_id"`
	CapMicroUSD           int64  `json:"cap_micro_usd"`
	RegisteredCapMicroUSD int64  `json:"registered_cap_micro_usd"`
	OpenMicroUSD          int64  `json:"open_micro_usd"`
	UncertainMicroUSD     int64  `json:"uncertain_micro_usd"`
	SettledMicroUSD       int64  `json:"settled_micro_usd"`
	CommittedMicroUSD     int64  `json:"committed_micro_usd"`
	RemainingMicroUSD     int64  `json:"remaining_micro_usd"`
	OpenUnits             int    `json:"open_units"`
	UncertainUnits        int    `json:"uncertain_units"`
	SettledUnits          int    `json:"settled_units"`
	TotalUnits            int    `json:"total_units"`
	PlannedUnits          int    `json:"planned_units"`
	StopRule              bool   `json:"stop_rule"`
	Overspent             bool   `json:"overspent"`
	CapMatchesRegistered  bool   `json:"cap_matches_registered"`
	OverCap               bool   `json:"over_cap"`
	UnitsOverPlanned      bool   `json:"units_over_planned"`
	WithinBudget          bool   `json:"within_budget"`
}

// Report is the deterministic results-chapter report.
type Report struct {
	CDH                    []CDHReport               `json:"cdh,omitempty"`
	Schema                 string                    `json:"schema"`
	Bindings               Bindings                  `json:"bindings"`
	Coverage               Coverage                  `json:"coverage"`
	Strata                 []Stratum                 `json:"strata"`
	Separate               []Stratum                 `json:"separate,omitempty"`
	Groups                 *Groups                   `json:"groups,omitempty"`
	Configurations         evalscore.ConfigSummary   `json:"configurations"`
	PostDenialContinuation PostDenialContinuation    `json:"post_denial_continuation"`
	Calibration            Calibration               `json:"calibration"`
	Observed               evalscore.ObservedCounts  `json:"observed"`
	InvalidRecords         []evalscore.InvalidRecord `json:"invalid_records"`
	Clauses                evalscore.ClausesSummary  `json:"clauses"`
	Metrics                evalscore.MetricsSummary  `json:"metrics"`
	H1                     evalscore.H1Summary       `json:"h1"`
	// Reruns are the units that took, wait for, or lost their registered rerun (register D30 as amended
	// 2026-11-18), with the original attempt and its cause. Left out when empty.
	Reruns []evalscore.CaseOutcome `json:"reruns,omitempty"`
	// ProviderRetries are the adversarial C trajectories whose provider attempts were retried, each with its count of
	// tools-disabled calls (D56; D30 as amended, Accepted risk). Left out when empty, and the Markdown report prints
	// them only when there are any.
	ProviderRetries []evalscore.ProviderRetryTrajectory `json:"provider_retries,omitempty"`
	Spend           Spend                               `json:"spend"`
	PlanV           bool                                `json:"plan_v,omitempty"`
	Flags           []string                            `json:"flags"`
	Digest          string                              `json:"digest"`
}
