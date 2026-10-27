package evalcorpus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// synthSeed is a test seed. It is not a registered seed.
const synthSeed = "synthetic-seed-1"

// smallQuotas are test-only counts with the same keys as ThesisQuotas. They are not the
// registered quotas, and the synthetic corpus built from them is not evidence.
func smallQuotas() Quotas {
	return Quotas{
		Adversarial: map[ThreatClass]int{ClassTMA: 4, ClassTMB: 4, ClassTMC: 3},
		Compliant:   map[Tool]int{ToolRead: 2, ToolWrite: 2, ToolEdit: 2, ToolBash: 2},
		Ambiguous:   2,
		Variance:    map[ThreatClass]int{ClassTMA: 1, ClassTMB: 1, ClassTMC: 1},
		ColdPercent: 20,
	}
}

func synthDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// synthManifest returns a synthetic manifest whose adversarial cases have no partition
// yet. Allocate assigns partitions and variance membership.
func synthManifest(q Quotas, seed string) Manifest {
	m := Manifest{
		SchemaVersion: SchemaVersion,
		SplitSeed:     seed,
		Pins: Pins{
			EnvironmentDigest: synthDigest("env"),
			ModelDigest:       synthDigest("model"),
			ProfileDigest:     synthDigest("profile"),
			PolicyDigest:      synthDigest("policy"),
		},
	}
	for _, class := range []ThreatClass{ClassTMA, ClassTMB, ClassTMC} {
		for i := 1; i <= q.Adversarial[class]; i++ {
			m.Cases = append(m.Cases, synthAdversarial(class, i))
		}
	}
	for _, tool := range []Tool{ToolRead, ToolWrite, ToolEdit, ToolBash} {
		for i := 1; i <= q.Compliant[tool]; i++ {
			m.Cases = append(m.Cases, synthCompliant(tool, i))
		}
	}
	for i := 1; i <= q.Ambiguous; i++ {
		m.Cases = append(m.Cases, synthAmbiguous(i))
	}
	return m
}

func synthSignoff() Signoff {
	return Signoff{Reviewer: "synthetic-advisor", Human: true, Decision: "accept"}
}

// sealed returns m with a ConstructReview that binds its current content. A test that mutates a
// manifest after allocated validates through validate, which seals again.
func sealed(m Manifest) Manifest {
	content, err := ContentDigest(m)
	if err != nil {
		panic(err)
	}
	m.ConstructReview = ConstructReview{ContentDigest: content,
		PanelReceiptDigest: synthDigest("panel-receipt"), PanelAuditDigest: synthDigest("panel-audit"), PanelStatsDigest: synthDigest("panel-stats")}
	return m
}

// validate is Validate of the sealed manifest, so a test of one rule is not failed by the stale
// content digest that its own mutation caused.
func validate(m Manifest, q Quotas) []Violation { return Validate(sealed(m), q) }

func synthAdversarial(class ThreatClass, i int) Case {
	id := fmt.Sprintf("SYNTH-ADV-%s-%03d", class, i)
	tag := OpportunitySingleStage
	if i%2 == 0 {
		tag = OpportunityFeedback
	}
	return Case{
		CaseID:  id,
		Kind:    KindAdversarial,
		Signoff: synthSignoff(),
		Adversarial: &Adversarial{
			ThreatClass:     class,
			RootScenarioID:  "SYNTH-ROOT-" + id,
			SourceKey:       "synthetic-source",
			SourceItemID:    "synthetic-item-" + id,
			SelectionReason: "synthetic selection",
			Adaptation: Adaptation{
				PreservedIntent:       "synthetic intent",
				ToolMapping:           "synthetic tool mapping",
				AdaptationSteps:       []string{"synthetic step"},
				LostDistinctions:      []string{},
				AuthorizationClass:    "synthetic-authorization",
				EndpointOracleMapping: "synthetic endpoint mapping",
				LabelOrigin:           LabelExternalOracle,
			},
			OracleID:                 "synthetic-oracle",
			OracleVersion:            "0",
			ForbiddenEffects:         []string{"SYNTH-EFF-01"},
			WorkspaceFixtureDigest:   synthDigest("fixture/" + id),
			CalibrationPayloadDigest: synthDigest("calibration/" + id),
			CalibrationWitnessID:     "synthetic-witness-" + id,
			OpportunityTag:           tag,
			OpportunityPredicate:     "synthetic-opportunity",
		},
	}
}

func synthCompliant(tool Tool, i int) Case {
	id := fmt.Sprintf("SYNTH-CMP-%s-%02d", tool, i)
	return Case{
		CaseID:  id,
		Kind:    KindCompliant,
		Signoff: synthSignoff(),
		Compliant: &Compliant{
			PrimaryTool:            tool,
			TaskGoal:               "synthetic task goal",
			ToolMapping:            "synthetic tool mapping",
			OracleID:               "synthetic-acceptance-oracle",
			OracleVersion:          "0",
			WorkspaceFixtureDigest: synthDigest("cmp-fixture/" + id),
			AcceptanceTest:         Ref{Ref: "synthetic-acceptance-" + id, Digest: synthDigest("acceptance/" + id)},
			ReferenceSolution: ReferenceSolution{
				Ref:              "synthetic-solution-" + id,
				Digest:           synthDigest("solution/" + id),
				ForbiddenEffects: []string{},
			},
		},
	}
}

func synthAmbiguous(i int) Case {
	id := fmt.Sprintf("SYNTH-AMB-%02d", i)
	return Case{
		CaseID:  id,
		Kind:    KindAmbiguous,
		Signoff: synthSignoff(),
		Ambiguous: &Ambiguous{WorkspaceFixtureDigest: synthDigest("amb-fixture/" + id),
			Rubric: Ref{Ref: "synthetic-rubric", Digest: synthDigest("rubric")}},
	}
}

// allocated runs Allocate and fails the test on error.
func allocated(t *testing.T, m Manifest, q Quotas) Manifest {
	t.Helper()
	out, err := Allocate(m, q)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	return sealed(out)
}

// findCase returns a pointer into m.Cases for the case with the given ID.
func findCase(t *testing.T, m *Manifest, id string) *Case {
	t.Helper()
	for i := range m.Cases {
		if m.Cases[i].CaseID == id {
			return &m.Cases[i]
		}
	}
	t.Fatalf("case %q not found", id)
	return nil
}

// idsWhere returns the IDs of adversarial cases in class for which keep is true, in
// manifest order.
func idsWhere(m Manifest, class ThreatClass, keep func(*Adversarial) bool) []string {
	var out []string
	for i := range m.Cases {
		a := m.Cases[i].Adversarial
		if a != nil && a.ThreatClass == class && keep(a) {
			out = append(out, m.Cases[i].CaseID)
		}
	}
	return out
}

func wantRule(t *testing.T, vs []Violation, rule string) {
	t.Helper()
	for _, v := range vs {
		if v.Rule == rule {
			return
		}
	}
	t.Errorf("want violation %q; got %v", rule, vs)
}

func wantNoRule(t *testing.T, vs []Violation, rule string) {
	t.Helper()
	for _, v := range vs {
		if v.Rule == rule {
			t.Errorf("unexpected violation %q: %v", rule, v)
		}
	}
}

func wantClean(t *testing.T, vs []Violation) {
	t.Helper()
	if len(vs) != 0 {
		t.Errorf("want no violations; got %v", vs)
	}
}
