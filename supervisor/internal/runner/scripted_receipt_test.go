package runner

import (
	"context"
	"errors"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

func TestGovernedRoleOfIsConfigurationAware(t *testing.T) {
	for _, tc := range []struct {
		cfg  Configuration
		role Role
		want string
	}{
		{CfgC, RoleStaticReplay, "static-replay"}, {CfgP, RoleStaticReplay, "p-static-replay"},
		{CfgC, RoleAdaptiveReplay, "adaptive-replay"}, {CfgP, RoleAdaptiveReplay, "p-adaptive-replay"},
		{CfgC, RoleTrajectory, "trajectory"}, {CfgC, RoleCompliant, "compliant"},
		{CfgP, RoleTrajectory, ""}, {CfgP, RoleCompliant, ""}, {CfgB, RoleStaticReplay, ""}, {CfgV, RoleCompliant, ""},
	} {
		if got := governedRoleOf(Step{Configuration: tc.cfg, Role: tc.role}); got != tc.want {
			t.Errorf("%s/%s maps to %q, want %q", tc.cfg, tc.role, got, tc.want)
		}
	}
	for _, role := range []string{"p-static-replay", "p-adaptive-replay"} {
		found := false
		for _, r := range GovernedRoles() {
			found = found || r == role
		}
		if !found {
			t.Errorf("GovernedRoles lacks %q", role)
		}
	}
}

func TestGovernedServesPWithTheSameExecutor(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	var p, c Step
	for _, st := range s.Steps {
		if st.Kind == evalcorpus.KindAdversarial && st.Role == RoleStaticReplay {
			if st.Configuration == CfgP && p.CaseID == "" {
				p = st
			}
			if st.Configuration == CfgC && c.CaseID == "" {
				c = st
			}
		}
	}
	gp := GovernedC{PlanDigest: s.Digest, Configuration: CfgP, Transport: &fakeTransport{}}
	req, err := gp.Request(p)
	if err != nil || req.Role != "p-static-replay" || req.TaskID != p.CaseID {
		t.Fatalf("P request = %+v, %v", req, err)
	}
	if _, err := gp.Request(c); err == nil {
		t.Fatal("a P executor accepted a C step")
	}
	gc := GovernedC{PlanDigest: s.Digest, Transport: &fakeTransport{}}
	if _, err := gc.Request(p); err == nil {
		t.Fatal("the default (C) executor accepted a P step")
	}
	if _, err := gc.Request(c); err != nil {
		t.Fatal(err)
	}
	if err := gp.Prepare(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	// P and C of one case are distinct admissions because their roles differ
	pr, _ := gp.Request(p)
	cr, _ := gc.Request(Step{CaseID: p.CaseID, Kind: p.Kind, Configuration: CfgC, Role: RoleStaticReplay, Attempt: 1})
	if pr.Role == cr.Role {
		t.Fatalf("P and C share the role %q", pr.Role)
	}
}

func TestGovernedReceiptCarriesProviderModeScriptDeltaPolicyAndProposals(t *testing.T) {
	f := newGovernedFixture(t)
	r := f.receipt(true)
	r["provider_mode"] = "scripted"
	r["script_digest"] = "sha256:" + hexOf('s')
	r["delta"] = "admit-all/v1"
	r["admission_policy_digest"] = "sha256:" + hexOf('p')
	r["proposals"] = []map[string]string{
		{"tool": "write", "verdict": "ALLOW", "reason_code": "policy_rule_allow"},
		{"tool": "bash", "verdict": "DENY", "reason_code": "policy_rule_deny"},
	}
	f.respond("completed", "", r, 0)
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil {
		t.Fatal(err)
	}
	rc := o.Receipt
	if rc == nil || rc.ProviderMode != "scripted" || rc.ScriptDigest != "sha256:"+hexOf('s') || rc.Delta != "admit-all/v1" ||
		rc.AdmissionPolicyDigest != "sha256:"+hexOf('p') || len(rc.Proposals) != 2 ||
		rc.Proposals[1] != (ProposalFact{Tool: "bash", Verdict: "DENY", ReasonCode: "policy_rule_deny"}) {
		t.Fatalf("receipt facts = %+v", rc)
	}
	if !o.ToolAttempt {
		t.Fatal("a denied proposal is a tool attempt (ATT-TOOL-01)")
	}
	// a delta-admission denial of an allowed proposal is listed as a deny with its reason (ATT-TOOL-01)
	h := newGovernedFixture(t)
	r3 := h.receipt(true)
	r3["proposals"] = []map[string]string{
		{"tool": "write", "verdict": "DENY", "reason_code": "scope_protected_path"},
		{"tool": "write", "verdict": "ALLOW", "reason_code": "policy_rule_allow"},
	}
	h.respond("completed", "", r3, 0)
	if o, err := h.g.Execute(context.Background(), h.step); err != nil || !o.ToolAttempt ||
		o.Receipt.Proposals[0] != (ProposalFact{Tool: "write", Verdict: "DENY", ReasonCode: "scope_protected_path"}) {
		t.Fatalf("admission denial: attempt=%t receipt=%+v err=%v", o.ToolAttempt, o.Receipt, err)
	}
	// no denied proposal, no tool attempt
	g := newGovernedFixture(t)
	r2 := g.receipt(true)
	r2["proposals"] = []map[string]string{{"tool": "read", "verdict": "ALLOW", "reason_code": "policy_rule_allow"}}
	g.respond("completed", "", r2, 0)
	if o, err := g.g.Execute(context.Background(), g.step); err != nil || o.ToolAttempt {
		t.Fatalf("allowed proposals only: attempt=%t err=%v", o.ToolAttempt, err)
	}
}

func attestRun(a Attest, s Step, mutate func(*ReceiptFacts)) (Outcome, error) {
	r := matchingFor(s)
	if mutate != nil {
		mutate(r)
	}
	a.Inner = &stubInner{out: Outcome{Receipt: r, E01: E01Identity{Digest: "d"}}}
	return a.Execute(context.Background(), s)
}

func TestAttestRequiresTheProviderModeOfEveryRole(t *testing.T) {
	a := newAttest(nil)
	allRoles := []Role{RoleTrajectory, RoleStaticReplay, RoleAdaptiveReplay, RoleCompliant, RoleAmbiguous, RoleVarianceRepeat}
	for _, cfg := range []Configuration{CfgC, CfgP} {
		for _, role := range allRoles {
			if cfg == CfgP && role != RoleStaticReplay && role != RoleAdaptiveReplay {
				continue
			}
			s := advStep(cfg, role)
			scripted := role == RoleStaticReplay || role == RoleAdaptiveReplay
			out, err := attestRun(a, s, nil)
			if err != nil || !out.E01.Verified || out.MatchedDelivery != scripted {
				t.Errorf("%s/%s with the right provider: %+v %v", cfg, role, out, err)
			}
			wrong := "scripted"
			if scripted {
				wrong = "model"
			}
			// an absent member, the other provider and an unknown value are all refused, adaptive included
			for name, mode := range map[string]string{"absent": "", "the other provider": wrong, "unknown value": "replayed"} {
				out, err := attestRun(a, s, func(r *ReceiptFacts) { r.ProviderMode = mode })
				if !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified || out.MatchedDelivery {
					t.Errorf("%s/%s with %s provider_mode: %+v %v", cfg, role, name, out, err)
				}
			}
		}
	}
	// a role outside the set has no provider mode and is refused
	out, err := attestRun(a, advStep(CfgC, Role("bogus")), func(r *ReceiptFacts) { r.ProviderMode = "model" })
	if !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified {
		t.Errorf("unsupported role: %+v %v", out, err)
	}
}

func TestAttestComparesTheScriptAndAdmissionPolicyDigestsOfAScriptedReceipt(t *testing.T) {
	for _, cfg := range []Configuration{CfgC, CfgP} {
		s := advStep(cfg, RoleStaticReplay)
		for name, mutate := range map[string]func(*ReceiptFacts){
			"missing script digest":    func(r *ReceiptFacts) { r.ScriptDigest = "" },
			"wrong script digest":      func(r *ReceiptFacts) { r.ScriptDigest = "sha256:" + hexOf('x') },
			"missing admission policy": func(r *ReceiptFacts) { r.AdmissionPolicyDigest = "" },
			"wrong admission policy":   func(r *ReceiptFacts) { r.AdmissionPolicyDigest = "sha256:" + hexOf('x') },
			"the other configuration's policy": func(r *ReceiptFacts) {
				r.AdmissionPolicyDigest = map[Configuration]string{CfgC: testPolicyP, CfgP: testPolicyC}[cfg]
			},
		} {
			out, err := attestRun(newAttest(nil), s, mutate)
			if !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified || out.MatchedDelivery || !hasOutcome(out) {
				t.Errorf("%s/%s: %+v %v", cfg, name, out, err)
			}
		}
		// no expectation source, or an unknown case, fails closed
		for name, a := range map[string]Attest{
			"no script source": func() Attest { a := newAttest(nil); a.ScriptDigest = nil; return a }(),
			"no policy source": func() Attest { a := newAttest(nil); a.AdmissionPolicyDigest = nil; return a }(),
		} {
			if out, err := attestRun(a, s, nil); !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified || out.MatchedDelivery {
				t.Errorf("%s/%s: %+v %v", cfg, name, out, err)
			}
		}
		unknown := s
		unknown.CaseID = "adv-x"
		if out, err := attestRun(newAttest(nil), unknown, nil); !errors.Is(err, ErrReceiptPolicy) || out.MatchedDelivery {
			t.Errorf("%s: unknown case: %+v %v", cfg, out, err)
		}
	}
	// a model role needs neither digest
	if out, err := attestRun(newAttest(nil), advStep(CfgC, RoleTrajectory), nil); err != nil || !out.E01.Verified {
		t.Errorf("model role: %+v %v", out, err)
	}
}
