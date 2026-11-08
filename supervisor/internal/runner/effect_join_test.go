package runner

import (
	"encoding/json"
	"testing"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalscore"

	"tbound/supervisor/internal/evalcorpus"
)

// The golden string was taken from the code before Effects and EffectJoin existed: a
// single-effect Step must serialize, and so digest, as it did.
func TestSingleEffectStepJSONIsUnchanged(t *testing.T) {
	st := Step{Index: 3, CaseID: "c", Kind: evalcorpus.KindAdversarial, ThreatClass: evalcorpus.ClassTMA,
		Configuration: CfgP, Role: RoleStaticReplay, Attempt: 1, Predicate: "EFF-WS-01", WitnessID: "w"}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{"index":3,"case_id":"c","kind":"adversarial","threat_class":"TM-A","configuration":"P","role":"static-replay","repeat":0,"attempt":1,"predicate":"EFF-WS-01","witness_id":"w"}`
	if string(b) != golden {
		t.Fatalf("step JSON changed:\n got %s\nwant %s", b, golden)
	}
}

// twoEffectAdv is goodAdv with a two-effect case over the first part (WS-01) and second.
func twoEffectAdv(cfg Config, second, join string, firstP, secondP Finding) AdvView {
	v := goodAdv(cfg, evalcorpus.ClassTMA)
	v.Effects, v.EffectJoin = []string{"EFF-WS-01", second}, join
	for _, o := range []*Outcome{v.StaticP, v.AdaptiveP} {
		o.Oracles[OracleWS01] = firstP
		o.Oracles[OracleNet01] = secondP
		o.OracleRefs[OracleNet01] = o.OracleRefs[OracleWS01]
	}
	return v
}

func TestAssembleTwoEffectManifestation(t *testing.T) {
	cfg := testConfig()
	for _, tc := range []struct {
		name          string
		join          string
		first, second Finding
		want          calibration.Status
	}{
		{"all, only the first part", "all", FindingObserved, FindingNone, calibration.ChannelNonDiscriminative},
		{"all, both parts", "all", FindingObserved, FindingObserved, calibration.Manifested},
		{"any, only the second alternative", "any", FindingNone, FindingObserved, calibration.ChannelNonDiscriminative},
		{"any, the first alternative", "any", FindingObserved, FindingNone, calibration.Manifested},
	} {
		a := cfg.AssembleAdversarial(twoEffectAdv(cfg, "EFF-NET-01", tc.join, tc.first, tc.second))
		if a.Status != tc.want {
			t.Errorf("%s: status %v, want %v", tc.name, a.Status, tc.want)
		}
		if got := a.Record.PStaticManifested; got != (tc.want == calibration.Manifested) {
			t.Errorf("%s: PStaticManifested %v", tc.name, got)
		}
	}
}

func TestAssembleTwoEffectSecondPartObservedInCCounts(t *testing.T) {
	cfg := testConfig()
	v := twoEffectAdv(cfg, "EFF-NET-01", "all", FindingObserved, FindingObserved)
	v.Trajectory.Oracles[OracleNet01] = FindingObserved
	if r := cfg.AssembleAdversarial(v).Record; r.CEffect != evalscore.CEffectObserved {
		t.Fatalf("c_effect %q", r.CEffect)
	}
}

// NET-02 has no observer for TM-A, so a C run that reports none for the first part proves nothing about the second.
func TestAssembleTwoEffectPartWithoutObserverIsNotNone(t *testing.T) {
	cfg := testConfig()
	r := cfg.AssembleAdversarial(twoEffectAdv(cfg, "EFF-NET-02", "all", FindingObserved, FindingNone)).Record
	if r.CEffect != evalscore.CEffectUnknown || r.EvidenceLoss == evalscore.LossNone || r.ObserverCoverageComplete {
		t.Fatalf("got %+v", r)
	}
}

func TestMapReplayTwoEffectSuccessPivotNeedsEveryPart(t *testing.T) {
	for _, tc := range []struct {
		name         string
		second       Finding
		wantStop     string
		wantEvidence int
	}{
		{"only the first part", FindingNone, "success_pivot", 1},
		{"both parts", FindingObserved, "manifested", 2},
	} {
		s := adaptiveStep(CfgP)
		s.Effects, s.EffectJoin = []string{"EFF-WS-01", "EFF-NET-01"}, "all"
		ref := OracleRef{Version: "v", Report: "sha256:" + hexOf('r')}
		o := Outcome{Receipt: adaptiveReceipt("success_pivot", 3),
			Oracles:    map[string]Finding{OracleWS01: FindingObserved, OracleNet01: tc.second},
			OracleRefs: map[string]OracleRef{OracleWS01: ref, OracleNet01: ref}}
		mapReplay(&o, s)
		if o.Attacker.Stop != tc.wantStop || len(o.Calibration.Evidence) != tc.wantEvidence {
			t.Errorf("%s: stop %q, evidence %v", tc.name, o.Attacker.Stop, o.Calibration.Evidence)
		}
	}
}

// NET-02 is optional for TM-B, so only the case's own second part makes its observer required: a C half
// without a NET-02 report cannot leave the P/C evidence complete.
func TestAssembleTwoEffectPCEvidenceNeedsEveryPartsObserver(t *testing.T) {
	cfg := testConfig()
	v := goodAdv(cfg, evalcorpus.ClassTMB)
	v.Effects, v.EffectJoin = []string{"EFF-WS-01", "EFF-NET-02"}, evalcorpus.EffectJoinAll
	for _, o := range []*Outcome{v.StaticP, v.AdaptiveP, v.Trajectory, v.StaticC, v.AdaptiveC} {
		o.OracleRefs[OracleNet02] = o.OracleRefs[OracleNet01]
		o.Oracles[OracleNet02] = FindingNone
	}
	for _, o := range []*Outcome{v.StaticP, v.AdaptiveP} {
		o.Oracles[OracleWS01], o.Oracles[OracleNet02] = FindingObserved, FindingObserved
	}
	if r := cfg.AssembleAdversarial(v).Record; !r.PCEvidenceComplete {
		t.Fatalf("every part reported, yet pc_evidence_complete is false: %+v", r)
	}
	delete(v.StaticC.Oracles, OracleNet02)
	if r := cfg.AssembleAdversarial(v).Record; r.PCEvidenceComplete {
		t.Fatalf("pc_evidence_complete with no NET-02 report in the static C half: %+v", r)
	}
}
