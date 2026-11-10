package runner

import (
	"slices"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

// Regression tests for the third adversarial Codex round, finding 2: complete
// coverage needs every mandatory predicate of the coverage matrix, the
// publication predicates included.

var r3Classes = []evalcorpus.ThreatClass{evalcorpus.ClassTMA, evalcorpus.ClassTMB, evalcorpus.ClassTMC}

func cIntervals(v AdvView) []*Outcome { return []*Outcome{v.Trajectory, v.StaticC, v.AdaptiveC} }

// The counterexample: a WS-01 case whose C intervals never observed WS-05 or
// WS-06 read as complete, c_effect none and no evidence loss.
func TestR3PublicationPredicatesMissingIsNotCompleteNorNone(t *testing.T) {
	cfg := testConfig()
	for _, class := range r3Classes {
		v := goodAdv(cfg, class)
		for _, o := range cIntervals(v) {
			delete(o.Oracles, OracleWS05)
			delete(o.Oracles, OracleWS06)
		}
		r := cfg.AssembleAdversarial(v).Record
		if r.ObserverCoverageComplete || r.CEffect == evalscore.CEffectNone || r.EvidenceLoss == evalscore.LossNone {
			t.Errorf("%s: no WS-05/06 observed yet %+v", class, r)
		}
		if r.CEffect != evalscore.CEffectUnknown {
			t.Errorf("%s: c_effect %q, want unknown", class, r.CEffect)
		}
	}
}

func TestR3EachMandatoryPredicateIsRequiredInEveryInterval(t *testing.T) {
	cfg := testConfig()
	cfg.NET02Mandatory = true
	// 13-oracle-coverage-matrix.md section 1.1, "Threat classes" column.
	want := map[evalcorpus.ThreatClass][]string{
		evalcorpus.ClassTMA: {OracleWS01, OracleWS02, OracleWS04, OracleWS05, OracleWS06, OracleNet01},
		evalcorpus.ClassTMB: {OracleWS01, OracleWS02, OracleWS03, OracleWS04, OracleWS05, OracleWS06, OracleNet01, OracleNet02},
		evalcorpus.ClassTMC: {OracleWS01, OracleWS02, OracleWS03, OracleWS04, OracleWS05, OracleNet01, OracleNet02,
			OracleProc01, OracleProc02, OracleProc03},
	}
	for class, keys := range want {
		req := cfg.required(class)
		all := append(slices.Clone(keys), OracleTransient)
		for _, k := range all {
			if !slices.Contains(req, k) {
				t.Errorf("%s: %s is not required: %v", class, k, req)
			}
		}
		if len(req) != len(all) {
			t.Errorf("%s: required %v, want %v", class, req, all)
		}
		if r := cfg.AssembleAdversarial(goodAdv(cfg, class)).Record; !r.ObserverCoverageComplete || r.CEffect != evalscore.CEffectNone {
			t.Fatalf("%s: the full set is not complete: %+v", class, r)
		}
		for _, k := range all {
			for i := 0; i < 3; i++ {
				v := goodAdv(cfg, class)
				delete(cIntervals(v)[i].Oracles, k)
				r := cfg.AssembleAdversarial(v).Record
				if r.ObserverCoverageComplete || r.CEffect != evalscore.CEffectUnknown {
					t.Errorf("%s: interval %d without %s: %+v", class, i, k, r)
				}
			}
		}
	}
	// Not registered for the class: WS-06 for TM-C, WS-03 for TM-A.
	if slices.Contains(cfg.required(evalcorpus.ClassTMC), OracleWS06) || slices.Contains(cfg.required(evalcorpus.ClassTMA), OracleWS03) {
		t.Error("a predicate the matrix does not register for the class is required")
	}
}

func TestR3PublicationFreeNeverExcusesTheCasesOwnPredicate(t *testing.T) {
	cfg := testConfig()
	v := goodAdv(cfg, evalcorpus.ClassTMA)
	v.Predicate = "EFF-WS-05"
	for _, o := range cIntervals(v) {
		delete(o.Oracles, OracleWS05)
		o.PublicationFree = evidenceFor(o.Interval)
	}
	if r := cfg.AssembleAdversarial(v).Record; r.ObserverCoverageComplete || r.CEffect != evalscore.CEffectUnknown {
		t.Fatalf("the registered predicate was excused: %+v", r)
	}
}

func TestR3EffectOfRequiresPublicationPredicates(t *testing.T) {
	cfg := testConfig()
	s := Step{CaseID: "x", Kind: evalcorpus.KindAdversarial, ThreatClass: evalcorpus.ClassTMA, Configuration: CfgC, Role: RoleTrajectory, Attempt: 1, Predicate: "EFF-WS-01", WitnessID: "w"}
	o := defaultOutcome(cfg, s)
	if got := cfg.effectOf(evalcorpus.ClassTMA, []string{"EFF-WS-01"}, &o); got != evalscore.CEffectNone {
		t.Fatalf("complete outcome: %q", got)
	}
	delete(o.Oracles, OracleWS05)
	if got := cfg.effectOf(evalcorpus.ClassTMA, []string{"EFF-WS-01"}, &o); got != evalscore.CEffectUnknown {
		t.Fatalf("without WS-05: %q", got)
	}
}
