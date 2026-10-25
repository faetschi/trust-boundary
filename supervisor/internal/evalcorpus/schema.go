package evalcorpus

import (
	"encoding/json"
	"sort"
)

// SchemaVersion is the only accepted value of Manifest.SchemaVersion.
const SchemaVersion = "tbound-evalcorpus/v1"

// MaxManifestBytes bounds the input that Load reads.
const MaxManifestBytes = 16 << 20

const (
	// MaxTrialPromptBytes bounds Trial.Prompt.
	MaxTrialPromptBytes = 16 << 10
	// MaxTrialNumber is the largest Trial budget: 2^53-1, the largest integer that every
	// RFC 8785 implementation represents exactly.
	MaxTrialNumber uint64 = 1<<53 - 1
)

// Kind names one of the three corpus populations.
type Kind string

const (
	KindAdversarial Kind = "adversarial"
	KindCompliant   Kind = "compliant"
	KindAmbiguous   Kind = "ambiguous"
)

// ThreatClass is a registered adversarial threat class (tbound-thesis.md section 7.5).
type ThreatClass string

const (
	ClassTMA ThreatClass = "TM-A"
	ClassTMB ThreatClass = "TM-B"
	ClassTMC ThreatClass = "TM-C"
)

// Tool is one of the four governed tools. Compliant tasks are stratified by primary tool.
type Tool string

const (
	ToolRead  Tool = "read"
	ToolWrite Tool = "write"
	ToolEdit  Tool = "edit"
	ToolBash  Tool = "bash"
)

// Partition is the cold or non-cold membership of an adversarial case.
type Partition string

const (
	PartitionCold    Partition = "cold"
	PartitionNonCold Partition = "non_cold"
)

// LabelOrigin records where an adversarial label came from. Source labels are inherited
// only where equivalence is demonstrated; otherwise the frozen external oracle supplies
// the label.
type LabelOrigin string

const (
	LabelSourceInherited LabelOrigin = "source_inherited"
	LabelExternalOracle  LabelOrigin = "external_oracle"
)

// OpportunityTag is the pre-freeze tag of an adversarial case's adaptive opportunity.
type OpportunityTag string

const (
	OpportunitySingleStage OpportunityTag = "single_stage"
	OpportunityFeedback    OpportunityTag = "feedback_opportunity"
)

// Manifest is the corpus manifest. Every digest is "sha256:" followed by 64 lowercase
// hexadecimal digits. Case order is part of the canonical form.
type Manifest struct {
	SchemaVersion   string          `json:"schema_version"`
	SplitSeed       string          `json:"split_seed"`
	Pins            Pins            `json:"pins"`
	ConstructReview ConstructReview `json:"construct_review"`
	Cases           []Case          `json:"cases"`
}

// ConstructReview binds the manifest to the D35-amended construct review: the content the
// blinded AI panel rated, and the three files of that panel run that the advisor's sign-offs rest
// on. ContentDigest is evalcorpus.ContentDigest of this manifest, so writing the sign-offs and
// this block into the manifest does not change it. The panel digests are the "sha256:" of the
// bytes of the run's receipt.json and of the advisor-signed audit.json; `tbound-profile
// corpus-digest` checks all three files against them.
type ConstructReview struct {
	ContentDigest      string `json:"content_digest"`
	PanelReceiptDigest string `json:"panel_receipt_digest"`
	PanelAuditDigest   string `json:"panel_audit_digest"`
	PanelStatsDigest   string `json:"panel_stats_digest"`
}

// Pins are the environment identity digests that every case shares.
type Pins struct {
	EnvironmentDigest string `json:"environment_digest"`
	ModelDigest       string `json:"model_digest"`
	ProfileDigest     string `json:"profile_digest"`
	PolicyDigest      string `json:"policy_digest"`
	// AttackerDigest pins the adaptive attacker artifact (attacker-<hex>.json, package attacker): the
	// pivot library, seed, caps and feedback policy that C and P share. It is required when any case carries
	// pivots and absent otherwise.
	AttackerDigest string `json:"attacker_digest,omitempty"`
}

// Case is one registered unit. Exactly one of Adversarial, Compliant, or Ambiguous is
// set, and it must match Kind.
type Case struct {
	CaseID      string       `json:"case_id"`
	Kind        Kind         `json:"kind"`
	Signoff     Signoff      `json:"signoff"`
	Adversarial *Adversarial `json:"adversarial,omitempty"`
	Compliant   *Compliant   `json:"compliant,omitempty"`
	Ambiguous   *Ambiguous   `json:"ambiguous,omitempty"`
	// MeaningRecord is the D37 benchmark-adaptation record. It is present on every case
	// adapted from an established benchmark item (case_origin "established") or authored
	// from a pinned taxonomy ("derived"), and absent on legacy authored cases. When
	// present, Validate requires its fields (rule meaning.*).
	MeaningRecord *MeaningRecord `json:"meaning_record,omitempty"`
	// Trial is the host-approved governed-run input of the case. It is optional in the
	// schema and required by the governed service (cmd/tbound corpus admission).
	Trial *Trial `json:"trial,omitempty"`
}

// CaseOrigin records how a case was sourced under D37.
type CaseOrigin string

const (
	// OriginEstablished is a case adapted from a pinned, established benchmark item.
	OriginEstablished CaseOrigin = "established"
	// OriginDerived is a case authored from one distinct mechanism of a pinned taxonomy
	// (OWASP Agentic Top 10, MITRE ATT&CK). Not used by the established-only pilot.
	OriginDerived CaseOrigin = "derived"
)

// MeaningRecord is the D37 meaning record kept for every benchmark-adapted case, across
// all three kinds. It names the pinned upstream item and the transformations applied so a
// reviewer can check that intent was preserved. For adversarial cases several fields echo
// Adversarial/Adaptation; the record is the one self-contained provenance home that
// compliant and ambiguous cases also have.
type MeaningRecord struct {
	CaseOrigin         CaseOrigin  `json:"case_origin"`
	BenchmarkKey       string      `json:"benchmark_key"`
	BenchmarkRevision  string      `json:"benchmark_revision"`
	BenchmarkDigest    string      `json:"benchmark_digest"`
	SourceItemID       string      `json:"source_item_id"`
	RootID             string      `json:"root_id"`
	License            string      `json:"license"`
	Redistribution     string      `json:"redistribution"`
	PreservedIntent    string      `json:"preserved_intent"`
	TaskUnit           string      `json:"task_unit"`
	AuthorizationClass string      `json:"authorization_class"`
	Transformations    []string    `json:"transformations"`
	LostDistinctions   []string    `json:"lost_distinctions"`
	ToolMapping        string      `json:"tool_mapping"`
	OracleMapping      string      `json:"oracle_mapping"`
	LabelOrigin        LabelOrigin `json:"label_origin"`
}

// Trial is what the governed service runs for a case: the host-approved prompt and the
// budgets of the one workflow it admits. The service takes both from the frozen,
// digest-pinned manifest and never from a client. The budgets are non-negative integers
// at most MaxTrialNumber; the service additionally caps each at its signed ceiling.
//
// The remaining members exist on adversarial cases only. Script is the frozen stimulus that
// the static replay roles (static-replay, p-static-replay) feed through the governed service
// in place of a model; PDelta is the registered targeted configuration delta of the P half of
// the pair (empty means none) and PDeltaJustification says why that delta is the targeted one;
// ProtectedPaths are the paths whose change the control package's delta admission refuses.
// The calibration payload digest of the case commits to Script, Pivots and PDelta (CalibrationPayloadDigest).
type Trial struct {
	Prompt              string       `json:"prompt"`
	MaxHostCalls        uint64       `json:"max_host_calls"`
	MaxTokens           uint64       `json:"max_tokens"`
	DurationSeconds     uint64       `json:"duration_seconds"`
	Script              []ScriptTurn `json:"script,omitempty"`
	PDelta              string       `json:"p_delta,omitempty"`
	PDeltaJustification string       `json:"p_delta_justification,omitempty"`
	ProtectedPaths      []string     `json:"protected_paths,omitempty"`
	// Pivots maps a step id of the attacker library to the concrete provider turn (one tool call) the adaptive
	// replay roles issue for that step in this case. The attacker machine chooses the step from the denial
	// feedback it saw; the case supplies only what the step means here.
	Pivots map[string]ScriptTurn `json:"pivots,omitempty"`
}

// ScriptTurn is one provider turn of the frozen stimulus: either a tool call (Tool and its
// Arguments, a JSON object) or the final assistant text (Text, last turn only).
type ScriptTurn struct {
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Text      string          `json:"text,omitempty"`
}

// The only sign-off decisions. Validate refuses any other value, and a case whose standing
// decision is DecisionReject makes the manifest non-admissible.
const (
	DecisionAccept = "accept"
	DecisionReject = "reject"
)

// Signoff is the D35-amended construct review of one case (amendment 2026-10-24): the advisor,
// a non-author human, signs the case's meaning record after the AI panel. Audited is true for every
// case on the panel's audit list; Resolution then records the advisor's resolution under the frozen
// panel rubric.
type Signoff struct {
	Reviewer   string `json:"reviewer"`
	Human      bool   `json:"human"`
	Author     bool   `json:"author"`
	Decision   string `json:"decision"`
	Audited    bool   `json:"audited"`
	Resolution string `json:"resolution,omitempty"`
}

// Ref names a referenced artifact together with its content digest.
type Ref struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

// Adversarial is the payload of an adversarial case unit.
type Adversarial struct {
	ThreatClass              ThreatClass    `json:"threat_class"`
	Partition                Partition      `json:"partition"`
	VarianceSubset           bool           `json:"variance_subset"`
	RootScenarioID           string         `json:"root_scenario_id"`
	SourceKey                string         `json:"source_key"`
	SourceItemID             string         `json:"source_item_id"`
	SelectionReason          string         `json:"selection_reason"`
	Adaptation               Adaptation     `json:"adaptation"`
	OracleID                 string         `json:"oracle_id"`
	OracleVersion            string         `json:"oracle_version"`
	ForbiddenEffects         []string       `json:"forbidden_effects"`
	WorkspaceFixtureDigest   string         `json:"workspace_fixture_digest"`
	CalibrationPayloadDigest string         `json:"calibration_payload_digest"`
	CalibrationWitnessID     string         `json:"calibration_witness_id"`
	OpportunityTag           OpportunityTag `json:"opportunity_tag"`
	OpportunityPredicate     string         `json:"opportunity_predicate"`
}

// Adaptation is the source-to-Pi mapping record for an adversarial case.
type Adaptation struct {
	PreservedIntent       string      `json:"preserved_intent"`
	ToolMapping           string      `json:"tool_mapping"`
	AdaptationSteps       []string    `json:"adaptation_steps"`
	LostDistinctions      []string    `json:"lost_distinctions"`
	AuthorizationClass    string      `json:"authorization_class"`
	EndpointOracleMapping string      `json:"endpoint_oracle_mapping"`
	LabelOrigin           LabelOrigin `json:"label_origin"`
	EquivalenceEvidence   string      `json:"equivalence_evidence,omitempty"`
}

// Compliant is the payload of a compliant task.
type Compliant struct {
	PrimaryTool            Tool              `json:"primary_tool"`
	TaskGoal               string            `json:"task_goal"`
	ToolMapping            string            `json:"tool_mapping"`
	OracleID               string            `json:"oracle_id"`
	OracleVersion          string            `json:"oracle_version"`
	WorkspaceFixtureDigest string            `json:"workspace_fixture_digest"`
	AcceptanceTest         Ref               `json:"acceptance_test"`
	ReferenceSolution      ReferenceSolution `json:"reference_solution"`
}

// ReferenceSolution is the witness route of a compliant task. ForbiddenEffects must be
// empty.
type ReferenceSolution struct {
	Ref              string   `json:"ref"`
	Digest           string   `json:"digest"`
	ForbiddenEffects []string `json:"forbidden_effects"`
}

// Ambiguous is the payload of an ambiguous case, scored against a written rubric. The workspace
// fixture is the tree the case starts from (internal/trialfix).
type Ambiguous struct {
	WorkspaceFixtureDigest string `json:"workspace_fixture_digest"`
	Rubric                 Ref    `json:"rubric"`
}

// CaseIDs returns the sorted case IDs of the given kind, including cold cases. Consumers
// that check trial records against the registered set can use it without importing this
// package's types.
func (m Manifest) CaseIDs(kind Kind) []string {
	var ids []string
	for i := range m.Cases {
		if m.Cases[i].Kind == kind {
			ids = append(ids, m.Cases[i].CaseID)
		}
	}
	sort.Strings(ids)
	return ids
}
