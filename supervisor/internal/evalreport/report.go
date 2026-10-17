// Package evalreport assembles the thesis results-chapter report from three
// already-checked inputs: the evalcorpus manifest, the trial records scored by
// evalscore, and a usdledger snapshot. It adds a coverage check, per-stratum
// counts, a spend check, and a SHA-256 digest that binds the three inputs.
// Scoring is not re-implemented: AC-Security, AC-Friction, rule_satisfied and
// every rate are evalscore.Summarize output, copied unchanged.
//
// Assumptions (each is an open thesis decision, marked "ASSUMPTION:" where it
// is applied):
//
//   - The caller has loaded the manifest with evalcorpus.Load and checked it
//     with evalcorpus.Validate. Build records the manifest digest and the case
//     coverage; it does not validate the manifest.
//   - "Exactly one scored primary trial" means one terminal evalscore record per
//     case. A case with several attempts counts once only when every earlier
//     attempt is an evidenced external-outage rerun (or an invalid record, which
//     evalscore already poisons). Any other repeat, including a repeated
//     (case, attempt), is a duplicate: all records of that case are withheld from
//     scoring and the case is listed under Coverage.Duplicate.
//   - Ambiguous cases have no evalscore record kind, so they are counted as
//     unscored and are not part of the coverage requirement.
//   - Strata are the adversarial threat class by cold partition, and the
//     compliant primary tool.
//   - Spend is checked against the registered US$5 cap and the 1,935 planned
//     units. Permitted external reruns may exceed 1,935 units, so exceeding it
//     is flagged but does not by itself make WithinBudget false.
//
// Digest: the report digest is "sha256:" + hex(SHA-256(DigestDomain || 0x00 ||
// canonical JSON of the report without its digest field)). Canonical JSON here
// means compact JSON with object keys sorted (Go byte order), numbers as Go
// formats them, and no HTML escaping. It is deterministic but not RFC 8785 for
// exponent-form numbers or non-BMP key names. The report contains no
// timestamps. Manifest case order is part of evalcorpus's manifest digest;
// record order is not part of the trials digest.
//
// Tests use synthetic fixtures. They verify code only and are not evidence
// about G1, H1, or any thesis claim.
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

	// RegisteredCapMicroUSD is the research cap (US$5) and PlannedUnits the planned
	// execution-unit ledger (tbound-thesis.md section 7.5, "Planned execution ledger").
	RegisteredCapMicroUSD int64 = 5_000_000
	PlannedUnits                = 1935
)

// Inputs are the three artifacts a report binds.
type Inputs struct {
	Manifest evalcorpus.Manifest
	Trials   evalscore.Input
	Ledger   usdledger.Snapshot
}

// Bindings tie the report to its inputs.
type Bindings struct {
	ManifestDigest string `json:"manifest_digest"`
	TrialsDigest   string `json:"trials_digest"`
	LedgerID       string `json:"ledger_id"`
	LedgerHead     string `json:"ledger_head_sha256"`
	LedgerRecords  uint64 `json:"ledger_records"`
}

// KindCoverage compares one kind of manifest case with its trial records.
type KindCoverage struct {
	Registered        int      `json:"registered"`
	Scored            int      `json:"scored"`
	Missing           []string `json:"missing"`
	Extra             []string `json:"extra"`
	Duplicate         []string `json:"duplicate"`
	ManifestDuplicate []string `json:"manifest_duplicate"`
}

// Coverage is the case-coverage check.
type Coverage struct {
	Exact             bool         `json:"exact"`
	Adversarial       KindCoverage `json:"adversarial"`
	Compliant         KindCoverage `json:"compliant"`
	UnscoredAmbiguous int          `json:"unscored_ambiguous"`
	WithheldRecords   int          `json:"withheld_records"`
}

// Stratum holds the counts of one manifest stratum.
type Stratum struct {
	Key            string                      `json:"key"`
	Kind           string                      `json:"kind"`
	Registered     int                         `json:"registered"`
	Scored         int                         `json:"scored"`
	Missing        int                         `json:"missing"`
	Duplicate      int                         `json:"duplicate"`
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

// Spend compares the ledger with the registered cap and planned units.
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
	Schema         string                    `json:"schema"`
	Bindings       Bindings                  `json:"bindings"`
	Coverage       Coverage                  `json:"coverage"`
	Strata         []Stratum                 `json:"strata"`
	Configurations evalscore.ConfigSummary   `json:"configurations"`
	Calibration    Calibration               `json:"calibration"`
	Observed       evalscore.ObservedCounts  `json:"observed"`
	InvalidRecords []evalscore.InvalidRecord `json:"invalid_records"`
	Clauses        evalscore.ClausesSummary  `json:"clauses"`
	Metrics        evalscore.MetricsSummary  `json:"metrics"`
	H1             evalscore.H1Summary       `json:"h1"`
	Spend          Spend                     `json:"spend"`
	Flags          []string                  `json:"flags"`
	Digest         string                    `json:"digest"`
}
