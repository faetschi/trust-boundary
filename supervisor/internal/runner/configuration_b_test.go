package runner

import (
	"errors"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

var testPolicyB = "sha256:" + hexOf('b')

// attestWithB is newAttest with an admission policy source that knows the B rule for the compliant case.
func attestWithB() Attest {
	a := newAttest(nil)
	a.AdmissionPolicyDigest = func(id string, cfg Configuration) (string, bool) {
		if id != "cmp-1" {
			return "", false
		}
		if cfg == CfgB {
			return testPolicyB, true
		}
		return testPolicyC, true
	}
	return a
}

func cmpStep(cfg Configuration, role Role) Step {
	return Step{CaseID: "cmp-1", Kind: evalcorpus.KindCompliant, Configuration: cfg, Role: role, Attempt: 1}
}

func bReceipt(r *ReceiptFacts) {
	r.Delta, r.AdmissionPolicyDigest = BDelta, testPolicyB
}

func TestGovernedRoleOfConfigurationB(t *testing.T) {
	for _, tc := range []struct {
		role Role
		want string
	}{
		{RoleCompliant, "b-compliant"}, {RoleAmbiguous, "b-ambiguous"}, {RoleTrajectory, ""}, {RoleStaticReplay, ""},
		{RoleAdaptiveReplay, ""}, {RoleVarianceRepeat, ""},
	} {
		if got := governedRoleOf(Step{Configuration: CfgB, Role: tc.role}); got != tc.want {
			t.Errorf("B/%s maps to %q, want %q", tc.role, got, tc.want)
		}
	}
	// C keeps its own roles, so the two admission ids of one case differ.
	if governedRoleOf(cmpStep(CfgC, RoleCompliant)) != "compliant" || governedRoleOf(cmpStep(CfgB, RoleCompliant)) != "b-compliant" {
		t.Error("B and C share a governed role")
	}
	for _, role := range []string{"b-compliant", "b-ambiguous"} {
		found := false
		for _, r := range GovernedRoles() {
			found = found || r == role
		}
		if !found {
			t.Errorf("GovernedRoles lacks %q", role)
		}
	}
}

func TestGovernedServesBWithTheSameExecutor(t *testing.T) {
	g := GovernedC{PlanDigest: "plan", Configuration: CfgB, Transport: &fakeTransport{}}
	req, err := g.Request(cmpStep(CfgB, RoleCompliant))
	if err != nil || req.Role != "b-compliant" || req.TaskID != "cmp-1" {
		t.Fatalf("B request = %+v, %v", req, err)
	}
	if _, err := g.Request(cmpStep(CfgC, RoleCompliant)); err == nil {
		t.Fatal("a B executor accepted a C step")
	}
	if _, err := (GovernedC{PlanDigest: "plan", Transport: &fakeTransport{}}).Request(cmpStep(CfgB, RoleCompliant)); err == nil {
		t.Fatal("the default (C) executor accepted a B step")
	}
	cr, _ := GovernedC{PlanDigest: "plan", Transport: &fakeTransport{}}.Request(cmpStep(CfgC, RoleCompliant))
	if cr.Role == req.Role {
		t.Fatalf("B and C share the role %q", cr.Role)
	}
}

func TestAttestRulesOfConfigurationB(t *testing.T) {
	a := attestWithB()
	for _, role := range []Role{RoleCompliant, RoleAmbiguous} {
		s := cmpStep(CfgB, role)
		if out, err := attestRun(a, s, bReceipt); err != nil || !out.E01.Verified || out.MatchedDelivery {
			t.Errorf("%s with a correct B receipt: %+v %v", role, out, err)
		}
		for name, mutate := range map[string]func(*ReceiptFacts){
			"no delta":                  func(r *ReceiptFacts) { bReceipt(r); r.Delta = "" },
			"delta none":                func(r *ReceiptFacts) { bReceipt(r); r.Delta = "none" },
			"another delta":             func(r *ReceiptFacts) { bReceipt(r); r.Delta = "net-sink/v1" },
			"no admission policy":       func(r *ReceiptFacts) { bReceipt(r); r.AdmissionPolicyDigest = "" },
			"the control policy":        func(r *ReceiptFacts) { bReceipt(r); r.AdmissionPolicyDigest = testPolicyC },
			"scripted provider":         func(r *ReceiptFacts) { bReceipt(r); r.ProviderMode = "scripted" },
			"absent provider mode":      func(r *ReceiptFacts) { bReceipt(r); r.ProviderMode = "" },
			"identity differs from pin": func(r *ReceiptFacts) { bReceipt(r); r.ProfileHash = "sha256:" + hexOf('9') },
		} {
			if out, err := attestRun(a, s, mutate); out.E01.Verified || (name != "identity differs from pin" && !errors.Is(err, ErrReceiptPolicy)) {
				t.Errorf("%s with %s: %+v %v", role, name, out, err)
			}
		}
	}
	// no expectation source, or an unknown case, fails closed for B
	s := cmpStep(CfgB, RoleCompliant)
	none := attestWithB()
	none.AdmissionPolicyDigest = nil
	if out, err := attestRun(none, s, bReceipt); !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified {
		t.Errorf("no policy source: %+v %v", out, err)
	}
	unknown := s
	unknown.CaseID = "cmp-x"
	if out, err := attestRun(a, unknown, bReceipt); !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified {
		t.Errorf("unknown case: %+v %v", out, err)
	}
}

func TestAttestRefusesBsAdmissionOnACReceiptAndAcceptsOneWithoutIt(t *testing.T) {
	a := attestWithB()
	c := cmpStep(CfgC, RoleCompliant)
	if out, err := attestRun(a, c, nil); err != nil || !out.E01.Verified {
		t.Errorf("C receipt without digest: %+v %v", out, err)
	}
	if out, err := attestRun(a, c, func(r *ReceiptFacts) { r.AdmissionPolicyDigest = testPolicyC }); err != nil || !out.E01.Verified {
		t.Errorf("C receipt with the control digest: %+v %v", out, err)
	}
	for name, mutate := range map[string]func(*ReceiptFacts){
		"B's admission digest": func(r *ReceiptFacts) { r.AdmissionPolicyDigest = testPolicyB },
		"B's delta":            func(r *ReceiptFacts) { r.Delta = BDelta },
		"both":                 bReceipt,
	} {
		if out, err := attestRun(a, c, mutate); !errors.Is(err, ErrReceiptPolicy) || out.E01.Verified {
			t.Errorf("C receipt with %s: %+v %v", name, out, err)
		}
	}
}
