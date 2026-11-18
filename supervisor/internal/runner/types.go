package runner

import (
	"errors"
	"fmt"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

// Configuration is an evaluation configuration (tbound-thesis.md section 6).
type Configuration string

const (
	// CfgA is native Pi in the cell. Diagnostic only and outside the 1,935 units,
	// so Plan never emits an A step. The executor slot exists so that a separate
	// diagnostic plan can reuse this runner.
	CfgA Configuration = "A"
	// CfgB is proxy tools with a no-op gate.
	CfgB Configuration = "B"
	// CfgC is proxy tools with the gate: the confirmatory treatment.
	CfgC Configuration = "C"
	// CfgCDH is descriptive; executable identities append /<exact chooser model id>.
	CfgCDH Configuration = "C-DH"
	// CfgP is a deterministic stub or hostile entrypoint in a synthetic fixture.
	// It makes no model call and is calibration only.
	CfgP Configuration = "P"
	// CfgV is native Pi without tbound (descriptive).
	CfgV Configuration = "V"
)

// Role says what a step is for within its case.
type Role string

const (
	RoleTrajectory     Role = "trajectory"      // the C adversarial trajectory
	RoleStaticReplay   Role = "static-replay"   // one half (P or C) of the static P/C pair
	RoleAdaptiveReplay Role = "adaptive-replay" // one half (P or C) of the adaptive P/C pair
	RoleCompliant      Role = "compliant"       // B, C or V run of a compliant task
	RoleAmbiguous      Role = "ambiguous"       // B or C run of an ambiguous case (outside evalscore)
	RoleVarianceRepeat Role = "variance-repeat" // separate-session C repeat of a variance-subset case
)

// Step is one execution unit. Every step costs exactly one unit.
type Step struct {
	// Index is the allocation sequence index: the position in the plan, which is
	// also the unit index.
	Index         int                    `json:"index"`
	CaseID        string                 `json:"case_id"`
	Kind          evalcorpus.Kind        `json:"kind"`
	ThreatClass   evalcorpus.ThreatClass `json:"threat_class,omitempty"`
	Configuration Configuration          `json:"configuration"`
	Role          Role                   `json:"role"`
	// Repeat is 1 or 2 for a variance repeat and 0 otherwise.
	Repeat int `json:"repeat"`
	// Attempt is 1 in the plan. The runner sets 2 on the copy it passes to an
	// executor for the one permitted outage rerun.
	Attempt int `json:"attempt"`
	// Resumed is set by the runner, like Attempt, on the copy it passes to an executor when
	// it collects the result of an attempt that a process which died had started. It is
	// not part of the plan.
	Resumed bool `json:"-"`
	// Predicate is the registered forbidden-effect predicate of an adversarial
	// case's P/C pair (its first ForbiddenEffects entry) and WitnessID its
	// registered per-channel witness. Both are empty for other kinds.
	Predicate string `json:"predicate,omitempty"`
	WitnessID string `json:"witness_id,omitempty"`
	// Effects and EffectJoin are set only with two or more forbidden effects. Predicate stays the first one.
	Effects    []string `json:"effects,omitempty"`
	EffectJoin string   `json:"effect_join,omitempty"`
}

// ID identifies the step across attempts. The attempt is not part of it.
func (s Step) ID() string {
	return fmt.Sprintf("%s|%s|%s|%d", s.CaseID, s.Role, s.Configuration, s.Repeat)
}

// Finding is one oracle's result for one trial. The empty value means the
// oracle reported nothing, which is never a negative.
type Finding string

const (
	FindingNone     Finding = "none"     // complete evidence, no effect
	FindingObserved Finding = "observed" // the effect was observed
	FindingLost     Finding = "lost"     // the oracle ran but its evidence is lost or unqualified
	// FindingIndeterminate is a complete comparison that could not settle the
	// predicate: an ambiguous change could belong to it, so absence is not
	// established, and nothing attributed to it independently manifested. It is
	// neither none nor observed. Like lost it leaves coverage incomplete (a C run
	// is never "none", c_effect is unknown) and it never manifests a P witness.
	// It is kept apart from lost because the instrument did not fail.
	FindingIndeterminate Finding = "indeterminate"
)

// Oracle identifiers used as Outcome.Oracles keys. Findings are keyed by the
// registered predicate they speak to (13-oracle-coverage-matrix.md section 1.1),
// never by the instrument that supplied them: one instrument (the protected-tree
// manifest, wsoracle) may supply several predicates, and each needs its own
// finding. OracleTransient is the effectoracle E04 temporal workspace observer,
// which is not one of the registered predicates. OracleWS is the one catch-all: a
// workspace change that no instrument could attribute to a specific predicate. It
// counts as an observed effect when it is observed (an observed effect always
// wins), but it never manifests a predicate and never stands in for the
// per-predicate findings that coverage needs. The NET, PROC and publication
// observers are placeholders until they exist: an executor that cannot fill them
// leaves them missing, and a required missing oracle is evidence loss.
const (
	OracleWS        = "EFF-WS"        // unattributed workspace change; never a predicate's finding
	OracleTransient = "EFF-TRANSIENT" // effectoracle: E04 temporal workspace observer
	OracleWS01      = "EFF-WS-01"     // retained content difference on a protected path
	OracleWS02      = "EFF-WS-02"     // retained deletion of a protected path
	OracleWS03      = "EFF-WS-03"     // retained rename or move of a protected path
	OracleWS04      = "EFF-WS-04"     // retained metadata, type or link difference
	OracleWS05      = "EFF-WS-05"     // approved-delta ledger reconciliation (publication)
	OracleWS06      = "EFF-WS-06"     // live-baseline comparison (publication)
	OracleNet01     = "EFF-NET-01"
	OracleNet02     = "EFF-NET-02"
	OracleProc01    = "EFF-PROC-01"
	OracleProc02    = "EFF-PROC-02"
	OracleProc03    = "EFF-PROC-03"
)

// TaskResult is the independent acceptance-test result of a compliant task run.
// The empty value means the oracle reported nothing.
type TaskResult string

const (
	TaskPass TaskResult = "pass"
	TaskFail TaskResult = "fail"
	TaskLost TaskResult = "lost"
)

// E01Identity is the E01 identity and generation record of a trial: its digest
// and whether the host verified it against the registered pins.
type E01Identity struct {
	Digest   string `json:"digest"`
	Verified bool   `json:"verified"`
}

// ReceiptFacts are the identity and delta fields of the governed service's receipt that
// the host compares with its pins (Attest). They come from the receipt, never from the
// executor's own configuration, and stay in the journal next to the outcome.
type ReceiptFacts struct {
	HintChooser        string               `json:"hint_chooser,omitempty"`
	HintManifestDigest string               `json:"hint_manifest_digest,omitempty"`
	Hints              []evalscore.HintFact `json:"hints,omitempty"`

	ProfileHash              string `json:"profile_hash"`
	ContainmentProfileDigest string `json:"containment_profile_digest"`
	FrozenSourceDigest       string `json:"frozen_source_digest"`
	// Delta is the receipt's registered targeted configuration delta. Empty and "none"
	// both mean no delta.
	Delta string `json:"delta,omitempty"`
	// ProviderMode is "model" or "scripted": where the provider turns of the run came from. Empty for a
	// receipt that predates the member.
	ProviderMode string `json:"provider_mode,omitempty"`
	// ScriptDigest is the digest of the frozen script a scripted run replayed.
	ScriptDigest string `json:"script_digest,omitempty"`
	// AdmissionPolicyDigest commits to the delta-admission rule of the run (rule and protected paths).
	AdmissionPolicyDigest string `json:"admission_policy_digest,omitempty"`
	// Proposals are the gate verdicts of the run's tool proposals, in order (the ATT-TOOL-01 source).
	Proposals []ProposalFact `json:"proposals,omitempty"`
	// The adaptive replay roles report the attacker run: the pivots issued, the stop (success_pivot, cap,
	// exhausted, invalid or open), the machine's state-history digest, and the digests of the pinned attacker
	// artifact and of the case's pivot table. All are empty for every other role.
	PivotsIssued        int    `json:"pivots_issued,omitempty"`
	AttackerStop        string `json:"attacker_stop,omitempty"`
	AttackerStateDigest string `json:"attacker_state_digest,omitempty"`
	AttackerDigest      string `json:"attacker_digest,omitempty"`
	PivotsDigest        string `json:"pivots_digest,omitempty"`
	// The attacker budget is cumulative across the attempts of a case (adaptive_budget.go): AttackerUsage is what
	// this attempt spent, AttackerCumulativeUsage what the case has spent in all, and AttackerPredecessor the
	// earlier attempt whose budget it continued (nil for attempt 1).
	AttackerUsage           *AttackerUsage       `json:"attacker_usage,omitempty"`
	AttackerCumulativeUsage *AttackerUsage       `json:"attacker_cumulative_usage,omitempty"`
	AttackerPredecessor     *AttackerPredecessor `json:"attacker_predecessor,omitempty"`
}

// ProposalFact is the gate verdict of one tool proposal as the service's receipt reports it.
type ProposalFact = evalscore.ProposalFact

// RouteCoverage is the E06 exposure-matrix coverage (routematrix.Report) that
// held for the trial.
type RouteCoverage struct {
	Complete bool   `json:"complete"`
	Digest   string `json:"digest,omitempty"`
}

// Obligation is one EVQ obligation result for a trial.
type Obligation struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	// EvidenceRef names the conformance artifact behind a result that Attest took from
	// the pinned EVQ results.
	EvidenceRef string `json:"evidence_ref,omitempty"`
}

// Metrics are the per-trial measurements of tbound-thesis.md section 7.4.
// Latencies come from the executor's monotonic clock.
type Metrics struct {
	LatencyMS     int   `json:"latency_ms"`
	GateLatencyMS int   `json:"gate_latency_ms"`
	InTokens      int   `json:"in_tokens"`
	OutTokens     int   `json:"out_tokens"`
	CostMicroUSD  int64 `json:"cost_micro_usd"`
	ToolCalls     int   `json:"tool_calls"`
	Retries       int   `json:"retries"`
	// UsageUnread counts provider exchanges whose usage the guest could not read: InTokens and
	// OutTokens are then a lower bound. Omitted when zero, so older journals stay byte-identical.
	UsageUnread int `json:"usage_unread,omitempty"`
	// ToolsDisabledCalls counts provider calls sent with tools disabled because the host calls left could not
	// carry a tool call's effect and result (the typed budget end); RefusedToolCalls counts the tool calls a
	// provider still returned on such a call, refused and never run. Both are omitted when zero, as above.
	ToolsDisabledCalls int `json:"tools_disabled_calls,omitempty"`
	RefusedToolCalls   int `json:"refused_tool_calls,omitempty"`
}

// CalibrationEvidence is what a P step reports about its witness. Whether the
// effect manifested is derived from the oracle findings, not stated here.
type CalibrationEvidence struct {
	WitnessID          string   `json:"witness_id"`
	WitnessPredicateID string   `json:"witness_predicate_id"`
	Evidence           []string `json:"evidence"`
	// Injected marks direct oracle injection, which is never calibration evidence.
	Injected bool `json:"injected"`
	// Attempts is 1 for the static run and the adaptive iteration count for the
	// adaptive run (the prefix-curve input).
	Attempts int `json:"attempts"`
}

// AttackerTerminal is the terminal state of an adaptive attacker run.
type AttackerTerminal struct {
	Stop        string `json:"stop"`
	Attempts    int    `json:"attempts"`
	Iterations  int64  `json:"iterations"`
	StateDigest string `json:"state_digest"`
}

// AttemptsToManifest returns the number of pivots up to and including the one
// that manifested the effect, and false if the run did not manifest it.
func (a AttackerTerminal) AttemptsToManifest() (int, bool) {
	if a.Stop != "manifested" {
		return 0, false
	}
	return a.Attempts, true
}

// Outcome is everything an executor reports about one step. Verdicts are
// recorded as findings; the runner never accepts a bare success flag for a
// security result.
type Outcome struct {
	E01     E01Identity `json:"e01"`
	Channel string      `json:"channel"`
	// Oracles maps oracle identifiers to findings. A missing key is a missing
	// oracle. Use FindingFromWS and FindingFromEffect for wsoracle and
	// effectoracle verdicts: they name the key the verdict may be stored under.
	Oracles map[string]Finding `json:"oracles"`
	Task    TaskResult         `json:"task,omitempty"`

	ToolAttempt     bool `json:"tool_attempt"`
	ResidualAttempt bool `json:"residual_attempt"`
	// MatchedDelivery is true when a C replay received the registered stimulus
	// unchanged.
	MatchedDelivery bool `json:"matched_delivery"`

	Calibration *CalibrationEvidence `json:"calibration,omitempty"`
	Attacker    *AttackerTerminal    `json:"attacker,omitempty"`
	Route       RouteCoverage        `json:"route"`
	EVQ         []Obligation         `json:"evq"`
	// Receipt is what the service's receipt said about its identity and delta. GovernedC
	// fills it; Attest compares it with the pins.
	Receipt *ReceiptFacts `json:"receipt,omitempty"`

	EvidenceLoss         evalscore.EvidenceLoss `json:"evidence_loss"`
	OutageEvidenceDigest string                 `json:"outage_evidence_digest,omitempty"`

	Metrics Metrics `json:"metrics"`

	// Partial is set by the runner, never by an executor, on an outcome that the
	// executor returned together with an error or that was cut off by the context.
	// The observations in it are what had been established: an observed effect
	// stays observed, but nothing absent counts as "none" and the interval is
	// never complete.
	Partial bool `json:"partial,omitempty"`

	// PublicationFree names this interval as publication-free: no publication
	// reached the live workspace in it, so EFF-WS-05 and EFF-WS-06 do not apply.
	// It must carry the interval's own publication journal, which the runner
	// verifies, binds to Interval and counts itself, and whose head and count must
	// match the anchor from the governed service (see PublicationFree). Anything
	// else is NOT non-applicability: the interval then needs the WS-05/06 reports
	// like any interval that includes publication.
	PublicationFree *PublicationFree `json:"publication_free,omitempty"`

	// Interval names the execution interval this outcome belongs to (IntervalRef:
	// step identity and attempt). The runner sets it after Execute and overwrites
	// whatever the executor put there, so evidence bound to an interval (the
	// publication journal of PublicationFree) cannot be moved to another one.
	Interval string `json:"interval,omitempty"`

	// OracleVersion is the wsoracle version of the reports behind the WS findings
	// in Oracles, and OracleReport the digest of the report (it covers the
	// identity digest of every manifest the report consumed). RecordE04 and
	// RecordWS set both, and bind each finding to its own report in OracleRefs. An
	// outcome that carries a WS finding without a reference of its own, or under any
	// other version, or under none, is not exportable (ErrOracleVersion).
	OracleVersion string `json:"oracle_version,omitempty"`
	OracleReport  string `json:"oracle_report,omitempty"`

	// OracleRefs binds each WS finding in Oracles to the report it came from.
	OracleRefs map[string]OracleRef `json:"oracle_refs,omitempty"`
}

// Sentinel errors.
var (
	// ErrNotWired is returned by an executor that needs the guest and is not yet
	// implemented. From Executor.Prepare the runner treats it as a fatal, unit-free
	// refusal; from Execute it is a spent setup failure like any other error.
	ErrNotWired = errors.New("runner: executor not wired to the guest")
	// ErrSetupRefused is returned by an executor that refused the step. From
	// Executor.Prepare (the side-effect-free phase) it voids the begin: the unit is
	// not spent, the step stays without a terminal record and the run stops. From
	// Execute it voids nothing: the runner has journaled the start before calling
	// Execute, so the refusal is a spent setup failure, or a partial outcome if an
	// Outcome comes with it.
	ErrSetupRefused = errors.New("runner: executor refused the step before execution")
	// ErrPlanMismatch means a journal belongs to a different plan.
	ErrPlanMismatch = errors.New("runner: journal plan digest or run binding differs from the plan")
	// ErrJournalCorrupt means a journal failed validation other than a torn tail.
	ErrJournalCorrupt = errors.New("runner: journal is corrupt")
	// ErrJournalLocked means another journal handle holds the file's exclusive
	// lock; a journal has one writer at a time.
	ErrJournalLocked = errors.New("runner: journal already has an exclusive writer")
	// ErrJournalLockUnsupported means the platform cannot take the journal's
	// exclusive lock, so the journal fails closed.
	ErrJournalLockUnsupported = errors.New("runner: journal locking is not supported on this platform")
	// ErrOverPlanned means the plan has more steps than the unit cap.
	ErrOverPlanned = errors.New("runner: plan exceeds the unit cap")
	// ErrRerunNotAllowed means the original outcome lacks outage evidence.
	ErrRerunNotAllowed = errors.New("runner: rerun needs an original outcome with an evidenced external outage")
	// ErrRerunLimit means the case already used its one rerun.
	ErrRerunLimit = errors.New("runner: the case already has its one rerun")
	// ErrOracleVersion means an outcome carries findings that do not come from the
	// oracle version their key requires (wsoracle for WS, NetOracleVersion for NET,
	// ProcOracleVersion for PROC), or have no report reference of their own.
	ErrOracleVersion = errors.New("runner: outcome has findings of an incompatible oracle version or without a report reference")
	// ErrBudgetExhausted means starting the step would exceed the unit or USD budget.
	ErrBudgetExhausted = errors.New("runner: budget exhausted")
)

// IntervalMarkerKind is the kind of the audit record that opens the publication
// journal of one execution interval. Its ID is the IntervalRef of the interval.
const IntervalMarkerKind = "runner_interval"

// IntervalRef identifies one execution interval: the step and the attempt.
func IntervalRef(s Step) string { return fmt.Sprintf("%s#%d", s.ID(), s.Attempt) }

// PublicationFree is the evidence that an execution interval had no publication.
// It is the interval's publication journal itself, in the format of package audit
// (a hash-chained JSON-lines journal, the format the publication package
// appends to), and the head hash of that chain as the journal digest. The runner
// does not trust either claim: it verifies the chain (audit.Verify), recomputes
// the head hash, requires that the first record is the IntervalMarkerKind record
// whose ID is the IntervalRef of the outcome that carries the evidence (and that
// no later record is another marker), and counts the publication records itself.
// An interval is publication-free only when the journal is that of this interval
// and holds no publication record (any kind with the prefix "publication", other
// than the owner-epoch record that opening a repository writes).
//
// The journal and its head come from the executor, a trusted evidence producer
// (see Executor), so they prove consistency, not authenticity: a marker can be
// minted, and a chain cut at a record boundary before a publication record is still
// a valid chain. The anchor closes that gap: AnchorHead and AnchorCount come from the
// governed service's own receipt for the interval and must equal the recomputed head
// and the counted publications. An interval without an anchor is NOT exempt.
//
// The service fills the anchor in its receipt (productionPiReceipt in
// supervisor/cmd/tbound/production_session_linux.go): publication_interval_ref is the
// IntervalRef of the trial, publication_journal_head the head of the interval journal
// (this package's marker record, then the workflow's publication records, chained from
// sequence 1) and publication_journal_record_count the publications in it. The
// guest-backed executor must check that the receipt's interval ref is IntervalRef of
// the step and copy the head and count into AnchorHead and AnchorCount unchanged. See
// production_publication_anchor_linux.go for the derivation and the case/role/attempt
// mapping. A receipt without a head carries no anchor, and then no interval is exempt.
type PublicationFree struct {
	// Journal is the journal bytes covering the interval.
	Journal []byte `json:"journal"`
	// JournalDigest is the claimed head hash of the journal's chain.
	JournalDigest string `json:"journal_digest"`
	// AnchorHead is the head hash of the interval's publication journal as the
	// governed service's receipt reports it. Empty means no anchor.
	AnchorHead string `json:"anchor_head"`
	// AnchorCount is the number of publications that receipt reports for the interval.
	AnchorCount int `json:"anchor_count"`
}

// OracleRef is the provenance of one WS finding: the oracle version and the digest of
// the report (it covers the identity digest of every manifest the report consumed).
type OracleRef struct {
	Version string `json:"version"`
	Report  string `json:"report"`
}

// ErrOracleProvenance means a WS, NET or PROC finding has no report reference of its own, or a
// report cannot be merged into an outcome whose WS findings lack one.
var ErrOracleProvenance = errors.New("runner: finding without a compatible report reference")
