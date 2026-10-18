package runner

import (
	"context"
	"errors"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

func adaptiveReceipt(stop string, issued int) *ReceiptFacts {
	r := matchingFor(advStep(CfgC, RoleAdaptiveReplay))
	r.AttackerStop, r.PivotsIssued = stop, issued
	return r
}

func adaptiveStep(cfg Configuration) Step {
	s := advStep(cfg, RoleAdaptiveReplay)
	s.WitnessID = "witness-1"
	return s
}

func TestMapReplayManifestsOnlyWhenTheObserverConfirmsASuccessPivot(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cfg          Configuration
		stop         string
		finding      Finding
		wantStop     string
		wantManifest bool
	}{
		{"P success pivot, observed", CfgP, "success_pivot", FindingObserved, "manifested", true},
		{"C success pivot, observed (an escape)", CfgC, "success_pivot", FindingObserved, "manifested", true},
		{"P success pivot, no effect", CfgP, "success_pivot", FindingNone, "success_pivot", false},
		{"P success pivot, indeterminate", CfgP, "success_pivot", FindingIndeterminate, "success_pivot", false},
		{"P success pivot, observer lost", CfgP, "success_pivot", FindingLost, "success_pivot", false},
		{"P cap, effect observed anyway", CfgP, "cap", FindingObserved, "cap", false},
		{"P exhausted", CfgP, "exhausted", FindingNone, "exhausted", false},
		{"P invalid", CfgP, "invalid", FindingNone, "invalid", false},
		{"P open", CfgP, "open", FindingNone, "open", false},
	} {
		s := adaptiveStep(tc.cfg)
		o := Outcome{Receipt: adaptiveReceipt(tc.stop, 3), Oracles: map[string]Finding{OracleWS01: tc.finding},
			OracleRefs: map[string]OracleRef{OracleWS01: {Version: "v", Report: "sha256:" + hexOf('r')}}}
		o.Receipt.AttackerStateDigest = hexOf('d')
		mapReplay(&o, s)
		if o.Attacker == nil || o.Attacker.Stop != tc.wantStop || o.Attacker.Attempts != 3 || o.Attacker.Iterations != 3 ||
			o.Attacker.StateDigest != hexOf('d') {
			t.Errorf("%s: attacker = %+v", tc.name, o.Attacker)
			continue
		}
		if n, ok := o.Attacker.AttemptsToManifest(); ok != tc.wantManifest || (ok && n != 3) {
			t.Errorf("%s: attempts to manifest = %d, %t", tc.name, n, ok)
		}
		if tc.cfg == CfgC {
			if o.Calibration != nil {
				t.Errorf("%s: a C step has no calibration evidence", tc.name)
			}
			continue
		}
		c := o.Calibration
		if c == nil || c.Attempts != 3 || c.WitnessID != "witness-1" || c.WitnessPredicateID != "EFF-WS-01" || c.Injected ||
			(tc.finding == FindingObserved) != (len(c.Evidence) == 1) {
			t.Errorf("%s: calibration = %+v", tc.name, c)
		}
	}
}

func TestMapReplayStaticPCalibrationHasOneAttemptAndNoAttacker(t *testing.T) {
	s := advStep(CfgP, RoleStaticReplay)
	s.WitnessID = "witness-1"
	o := Outcome{Receipt: matchingFor(s), Oracles: map[string]Finding{OracleWS01: FindingObserved},
		OracleRefs: map[string]OracleRef{OracleWS01: {Version: "v", Report: "sha256:" + hexOf('r')}}}
	mapReplay(&o, s)
	if o.Attacker != nil || o.Calibration == nil || o.Calibration.Attempts != 1 || len(o.Calibration.Evidence) != 1 {
		t.Fatalf("static P: attacker=%+v calibration=%+v", o.Attacker, o.Calibration)
	}
	// nothing for a C static replay, a trajectory, a compliant step or a step without a receipt
	for _, st := range []Step{advStep(CfgC, RoleStaticReplay), advStep(CfgC, RoleTrajectory),
		{CaseID: "cmp", Kind: evalcorpus.KindCompliant, Configuration: CfgB, Role: RoleCompliant}} {
		out := Outcome{Receipt: matchingFor(st)}
		mapReplay(&out, st)
		if out.Attacker != nil || out.Calibration != nil {
			t.Errorf("%s: mapped %+v %+v", st.ID(), out.Attacker, out.Calibration)
		}
	}
	none := Outcome{}
	mapReplay(&none, adaptiveStep(CfgP))
	if none.Attacker != nil || none.Calibration != nil {
		t.Error("a step without receipt facts was mapped")
	}
}

func TestAttestChecksTheAttackerAndPivotDigestsOfAnAdaptiveReceipt(t *testing.T) {
	for _, cfg := range []Configuration{CfgC, CfgP} {
		s := adaptiveStep(cfg)
		if out, err := attestRun(newAttest(nil), s, nil); err != nil || !out.E01.Verified || !out.MatchedDelivery {
			t.Errorf("%s: a correct adaptive receipt: %+v %v", cfg, out, err)
		}
		for name, mutate := range map[string]func(*ReceiptFacts){
			"no attacker digest":      func(r *ReceiptFacts) { r.AttackerDigest = "" },
			"another attacker digest": func(r *ReceiptFacts) { r.AttackerDigest = "sha256:" + hexOf('x') },
			"no pivots digest":        func(r *ReceiptFacts) { r.PivotsDigest = "" },
			"another pivots digest":   func(r *ReceiptFacts) { r.PivotsDigest = "sha256:" + hexOf('x') },
			"a script digest":         func(r *ReceiptFacts) { r.ScriptDigest = testScriptDigest },
			"no stop":                 func(r *ReceiptFacts) { r.AttackerStop = "" },
			"no state digest":         func(r *ReceiptFacts) { r.AttackerStateDigest = "" },
			"wrong admission policy":  func(r *ReceiptFacts) { r.AdmissionPolicyDigest = "sha256:" + hexOf('x') },
			"model provider":          func(r *ReceiptFacts) { r.ProviderMode = "model" },
		} {
			if out, err := attestRun(newAttest(nil), s, mutate); !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified || out.MatchedDelivery {
				t.Errorf("%s/%s: %+v %v", cfg, name, out, err)
			}
		}
		// no pin, no pivots source, or an unknown case fails closed
		noPin := newAttest(nil)
		noPin.AttackerDigest = ""
		noPivots := newAttest(nil)
		noPivots.PivotsDigest = nil
		for name, a := range map[string]Attest{"no attacker pin": noPin, "no pivots source": noPivots} {
			if out, err := attestRun(a, s, nil); !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified {
				t.Errorf("%s/%s: %+v %v", cfg, name, out, err)
			}
		}
		unknown := s
		unknown.CaseID = "adv-x"
		if out, err := attestRun(newAttest(nil), unknown, nil); !errors.Is(err, ErrReceiptPolicy) || out.MatchedDelivery {
			t.Errorf("%s: unknown case: %+v %v", cfg, out, err)
		}
	}
	// the C and the P receipt of one case must carry the same digests: both are held to the one pinned
	// value, so a receipt that differs from the other half's is refused
	cOut, cErr := attestRun(newAttest(nil), adaptiveStep(CfgC), nil)
	pOut, pErr := attestRun(newAttest(nil), adaptiveStep(CfgP), func(r *ReceiptFacts) { r.PivotsDigest = "sha256:" + hexOf('y') })
	if cErr != nil || !cOut.E01.Verified || !errors.Is(pErr, ErrReceiptPolicy) || pOut.E01.Verified {
		t.Fatalf("C %+v %v, P %+v %v", cOut, cErr, pOut, pErr)
	}
}

func TestStaticReplayStillNeedsItsScriptDigestAndNoAttackerMembers(t *testing.T) {
	// the adaptive checks do not leak into the static roles
	s := advStep(CfgP, RoleStaticReplay)
	if out, err := attestRun(newAttest(nil), s, nil); err != nil || !out.E01.Verified {
		t.Fatalf("static P receipt: %+v %v", out, err)
	}
	if _, err := attestRun(newAttest(nil), s, func(r *ReceiptFacts) { r.ScriptDigest = "" }); !errors.Is(err, ErrReceiptPolicy) {
		t.Fatalf("static receipt without script digest: %v", err)
	}
}

func TestGovernedExecutorMapsTheAdaptiveReceiptOntoAttackerAndCalibration(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	var step Step
	for _, st := range s.Steps {
		if st.Kind == evalcorpus.KindAdversarial && st.Configuration == CfgP && st.Role == RoleAdaptiveReplay {
			step = st
			break
		}
	}
	if step.CaseID == "" {
		t.Fatal("no P adaptive step in the plan")
	}
	tr := &fakeTransport{}
	g := GovernedC{PlanDigest: s.Digest, Configuration: CfgP, Transport: tr}
	request, err := g.Request(step)
	if err != nil {
		t.Fatal(err)
	}
	f := &governedFixture{step: step, req: request, tr: tr, g: g}
	receipt := f.receipt(true)
	receipt["provider_mode"] = "scripted"
	receipt["pivots_issued"] = 4
	receipt["attacker_stop"] = "success_pivot"
	receipt["attacker_state_digest"] = hexOf('d')
	receipt["attacker_digest"] = testAttacker
	receipt["pivots_digest"] = testPivots
	f.respond("completed", "", receipt, 0)
	o, err := g.Execute(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	if o.Receipt == nil || o.Receipt.PivotsIssued != 4 || o.Receipt.AttackerStop != "success_pivot" || o.Receipt.AttackerDigest != testAttacker ||
		o.Receipt.PivotsDigest != testPivots || o.Receipt.AttackerStateDigest != hexOf('d') {
		t.Fatalf("receipt facts = %+v", o.Receipt)
	}
	// without an observer finding the success pivot is not a manifestation
	if o.Attacker == nil || o.Attacker.Stop != "success_pivot" || o.Attacker.Attempts != 4 {
		t.Fatalf("attacker = %+v", o.Attacker)
	}
	if o.Calibration == nil || o.Calibration.Attempts != 4 || o.Calibration.WitnessID != step.WitnessID {
		t.Fatalf("calibration = %+v", o.Calibration)
	}
}
