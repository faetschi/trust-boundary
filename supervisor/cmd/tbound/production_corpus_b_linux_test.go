//go:build linux

package main

import (
	"encoding/json"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/sessionrepo"
)

func TestCorpusRoleMatrixOfTheBRoles(t *testing.T) {
	corpus, manifest := scriptedTestCorpus(t, nil)
	for _, role := range []string{corpusRoleBCompliant, corpusRoleBAmbiguous} {
		if !validCorpusRole(role) {
			t.Errorf("%s is not a valid role", role)
		}
	}
	for _, c := range manifest.Cases {
		for role, kind := range map[string]evalcorpus.Kind{
			corpusRoleBCompliant: evalcorpus.KindCompliant, corpusRoleBAmbiguous: evalcorpus.KindAmbiguous,
		} {
			want := c.Kind == kind
			if got := corpus.roleAllowed(c.CaseID, role); got != want {
				t.Errorf("%s on a %s case: allowed=%t, want %t", role, c.Kind, got, want)
			}
			if got := corpus.roleServable(c.CaseID, role); got != want {
				t.Errorf("%s on a %s case: servable=%t, want %t", role, c.Kind, got, want)
			}
		}
	}
	for _, role := range []string{"b", "b-trajectory", "b-static-replay", "b-adaptive-replay", "B-compliant"} {
		if validCorpusRole(role) {
			t.Errorf("role %q is valid", role)
		}
	}
}

func TestBAndCAdmissionsOfOneCaseDoNotCollide(t *testing.T) {
	h := newCorpusServiceHarness(t)
	for _, pair := range [][2]string{{corpusRoleCompliant, corpusRoleBCompliant}, {corpusRoleAmbiguous, corpusRoleBAmbiguous}} {
		caseID := corpusCaseForRole(t, h.manifest, pair[0])
		if corpusAdmissionID(caseID, pair[0], 1) == corpusAdmissionID(caseID, pair[1], 1) {
			t.Fatalf("%s and %s share an admission id", pair[0], pair[1])
		}
		before := h.server.calls.Load()
		for i, role := range pair {
			response := roundTripPiServiceRequest(t, h.server.path, corpusRequest("tbr-bc-"+role, caseID, role, 1))
			if response.Status != "completed" || int(h.server.calls.Load()) != int(before)+i+1 {
				t.Fatalf("%s of %s was not admitted independently: %+v", role, caseID, response)
			}
			var receipt struct {
				HostAdmissionID string `json:"host_admission_id"`
			}
			if err := json.Unmarshal(response.Receipt, &receipt); err != nil || receipt.HostAdmissionID != caseID+"/"+role+"/1" {
				t.Fatalf("%s admission id = %+v, %v", role, receipt, err)
			}
		}
	}
	// A B role does not apply to a case of another kind.
	adversarial := corpusCaseForRole(t, h.manifest, corpusRoleTrajectory)
	if r := roundTripPiServiceRequest(t, h.server.path, corpusRequest("tbr-b-adv", adversarial, corpusRoleBCompliant, 1)); r.Status != "refused" || r.ErrorCode != "role-kind-mismatch" {
		t.Fatalf("b-compliant on an adversarial case: %+v", r)
	}
	compliant := corpusCaseForRole(t, h.manifest, corpusRoleCompliant)
	if r := roundTripPiServiceRequest(t, h.server.path, corpusRequest("tbr-b-amb", compliant, corpusRoleBAmbiguous, 1)); r.Status != "refused" || r.ErrorCode != "role-kind-mismatch" {
		t.Fatalf("b-ambiguous on a compliant case: %+v", r)
	}
}

func TestIntervalRefOfTheBRolesNamesConfigurationB(t *testing.T) {
	const caseID = "case-1"
	for role, want := range map[string]string{
		corpusRoleBCompliant: caseID + "|compliant|B|0#1", corpusRoleBAmbiguous: caseID + "|ambiguous|B|0#1",
		corpusRoleCompliant: caseID + "|compliant|C|0#1",
	} {
		ref, ok := corpusIntervalRef(corpusAdmissionID(caseID, role, 1), caseID)
		if !ok || ref != want {
			t.Errorf("%s: %q ok=%v, want %q", role, ref, ok, want)
		}
	}
	for role, step := range map[string]runner.Step{
		corpusRoleBCompliant: {CaseID: caseID, Configuration: runner.CfgB, Role: runner.RoleCompliant, Attempt: 1},
		corpusRoleBAmbiguous: {CaseID: caseID, Configuration: runner.CfgB, Role: runner.RoleAmbiguous, Attempt: 1},
	} {
		if ref, _ := corpusIntervalRef(corpusAdmissionID(caseID, role, 1), caseID); ref != runner.IntervalRef(step) {
			t.Errorf("%s: service %q differs from the runner %q", role, ref, runner.IntervalRef(step))
		}
		// the runner's governed role of the step is the service role
		if got := runner.GovernedRequestFor("plan", step).Role; got != role {
			t.Errorf("runner role of %s is %q", role, got)
		}
	}
}

func TestBindingOfTheBRolesIsAdmitAllOnTheRealModelAndOnlyThatRoleSelectsIt(t *testing.T) {
	corpus, manifest := scriptedTestCorpus(t, nil)
	compliant := corpusCaseForRole(t, manifest, corpusRoleCompliant)
	ambiguous := corpusCaseForRole(t, manifest, corpusRoleAmbiguous)
	promoteAll := sessionrepo.AdmissionScope{Rule: sessionrepo.AdmissionScopeRulePromoteAll}
	policy, metadata := corpusTestDigest("policy"), corpusTestDigest("metadata")
	digests := map[string]string{}
	for _, tc := range []struct {
		caseID, role string
		relaxed      bool
	}{
		{compliant, corpusRoleCompliant, false}, {compliant, corpusRoleBCompliant, true},
		{ambiguous, corpusRoleAmbiguous, false}, {ambiguous, corpusRoleBAmbiguous, true},
	} {
		b, err := corpus.bindingFor(tc.caseID, tc.role)
		if err != nil {
			t.Fatalf("%s: %v", tc.role, err)
		}
		if b.providerMode() != providerModeModel || b.scripted() || b.scriptedFactory() != nil || b.scriptDigest() != "" {
			t.Errorf("%s: B and C use the real model, never a script: %+v", tc.role, b)
		}
		wantDelta, wantScope := evalcorpus.PDeltaNone, sessionrepo.AdmissionScope{}
		if tc.relaxed {
			wantDelta, wantScope = evalcorpus.PDeltaAdmitAll, promoteAll
		}
		if b.delta != wantDelta || b.scope.Rule != wantScope.Rule || len(b.scope.ProtectedPaths) != 0 {
			t.Errorf("%s: delta=%s scope=%+v", tc.role, b.delta, b.scope)
		}
		digest, err := b.admissionPolicyDigest(policy, metadata)
		if err != nil {
			t.Fatal(err)
		}
		digests[tc.role] = digest
		// the host recomputes the same digest from the manifest and the pins
		host, err := evalcorpus.AdmissionPolicyDigest(policy, metadata, tc.relaxed, nil)
		if err != nil || host != digest {
			t.Errorf("%s: service digest %s, host digest %s (%v)", tc.role, digest, host, err)
		}
	}
	if digests[corpusRoleCompliant] == digests[corpusRoleBCompliant] || digests[corpusRoleAmbiguous] == digests[corpusRoleBAmbiguous] {
		t.Error("B and C report the same admission policy digest")
	}
	// An authority binds the scope of the B role and installs no scripted factory.
	b, _ := corpus.bindingFor(compliant, corpusRoleBCompliant)
	var receipt productionPiReceipt
	if err := applyCorpusReceipt(&receipt, &b, true, policy, metadata, audit.Trace{}, nil); err != nil {
		t.Fatal(err)
	}
	if receipt.ProviderMode != "model" || receipt.Delta != evalcorpus.PDeltaAdmitAll || receipt.ScriptDigest != "" ||
		receipt.AdmissionPolicyDigest != digests[corpusRoleBCompliant] {
		t.Fatalf("B receipt = %+v", receipt)
	}
	c, _ := corpus.bindingFor(compliant, corpusRoleCompliant)
	var creceipt productionPiReceipt
	if err := applyCorpusReceipt(&creceipt, &c, true, policy, metadata, audit.Trace{}, nil); err != nil {
		t.Fatal(err)
	}
	if creceipt.Delta != evalcorpus.PDeltaNone || creceipt.AdmissionPolicyDigest != digests[corpusRoleCompliant] {
		t.Fatalf("C receipt = %+v", creceipt)
	}
	// the relaxation never follows from a P role of a B case: the B roles are not P roles
	if pCorpusRole(corpusRoleBCompliant) || !bCorpusRole(corpusRoleBAmbiguous) || bCorpusRole(corpusRoleCompliant) {
		t.Error("role classification")
	}
}
