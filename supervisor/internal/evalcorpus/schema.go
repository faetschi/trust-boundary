package evalcorpus

import "sort"

// SchemaVersion is the only accepted value of Manifest.SchemaVersion.
const SchemaVersion = "tbound-evalcorpus/v1"

// MaxManifestBytes bounds the input that Load reads.
const MaxManifestBytes = 16 << 20

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
	SchemaVersion string `json:"schema_version"`
	SplitSeed     string `json:"split_seed"`
	Pins          Pins   `json:"pins"`
	Cases         []Case `json:"cases"`
}

// Pins are the environment identity digests that every case shares.
type Pins struct {
	EnvironmentDigest string `json:"environment_digest"`
	ModelDigest       string `json:"model_digest"`
	ProfileDigest     string `json:"profile_digest"`
	PolicyDigest      string `json:"policy_digest"`
}

// Case is one registered unit. Exactly one of Adversarial, Compliant, or Ambiguous is
// set, and it must match Kind.
type Case struct {
	CaseID       string        `json:"case_id"`
	Kind         Kind          `json:"kind"`
	Reviews      []Review      `json:"reviews"`
	Adjudication *Adjudication `json:"adjudication,omitempty"`
	Adversarial  *Adversarial  `json:"adversarial,omitempty"`
	Compliant    *Compliant    `json:"compliant,omitempty"`
	Ambiguous    *Ambiguous    `json:"ambiguous,omitempty"`
}

// Review is one construct-validity review. Every case has exactly two, from different
// roles and different reviewers, both human and blinded.
type Review struct {
	Role     string `json:"role"`
	Reviewer string `json:"reviewer"`
	Human    bool   `json:"human"`
	Blinded  bool   `json:"blinded"`
	Decision string `json:"decision"`
}

// Adjudication is required exactly when the two reviews disagree. The adjudicator is a
// third reviewer: human and blinded, and not either of the two reviewers.
type Adjudication struct {
	Adjudicator string `json:"adjudicator"`
	Human       bool   `json:"human"`
	Blinded     bool   `json:"blinded"`
	Decision    string `json:"decision"`
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

// Ambiguous is the payload of an ambiguous case, scored against a written rubric.
type Ambiguous struct {
	Rubric Ref `json:"rubric"`
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
