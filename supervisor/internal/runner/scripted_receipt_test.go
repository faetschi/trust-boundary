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
	// no denied proposal, no tool attempt
	g := newGovernedFixture(t)
	r2 := g.receipt(true)
	r2["proposals"] = []map[string]string{{"tool": "read", "verdict": "ALLOW", "reason_code": "policy_rule_allow"}}
	g.respond("completed", "", r2, 0)
	if o, err := g.g.Execute(context.Background(), g.step); err != nil || o.ToolAttempt {
		t.Fatalf("allowed proposals only: attempt=%t err=%v", o.ToolAttempt, err)
	}
}

func TestAttestChecksTheProviderModeOfTheRole(t *testing.T) {
	run := func(role Role, mode string) (Outcome, error) {
		r := matching()
		r.ProviderMode = mode
		in := &stubInner{out: Outcome{Receipt: r, E01: E01Identity{Digest: "d"}}}
		return newAttest(in).Execute(context.Background(), advStep(CfgC, role))
	}
	if out, err := run(RoleStaticReplay, "scripted"); err != nil || !out.E01.Verified || !out.MatchedDelivery {
		t.Fatalf("scripted static replay: %+v %v", out, err)
	}
	for name, tc := range map[string]struct {
		role Role
		mode string
	}{"model in a replay": {RoleStaticReplay, "model"}, "scripted trajectory": {RoleTrajectory, "scripted"}} {
		out, err := run(tc.role, tc.mode)
		if !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified || out.MatchedDelivery {
			t.Errorf("%s: %+v %v", name, out, err)
		}
	}
	// receipts of the model roles and older receipts without the member stay accepted
	if _, err := run(RoleTrajectory, "model"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(RoleStaticReplay, ""); err != nil {
		t.Fatal(err)
	}
}
