//go:build linux

package main

import (
	"encoding/json"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

// corpusCaseForRole returns a case of the manifest that the role applies to.
func corpusCaseForRole(t *testing.T, manifest evalcorpus.Manifest, role string) string {
	t.Helper()
	for _, c := range manifest.Cases {
		switch role {
		case corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleAdaptiveReplay, corpusRolePStaticReplay, corpusRolePAdaptiveReplay:
			if c.Kind == evalcorpus.KindAdversarial {
				return c.CaseID
			}
		case corpusRoleVariance1, corpusRoleVariance2:
			if c.Kind == evalcorpus.KindAdversarial && c.Adversarial.VarianceSubset {
				return c.CaseID
			}
		case corpusRoleCompliant:
			if c.Kind == evalcorpus.KindCompliant {
				return c.CaseID
			}
		case corpusRoleAmbiguous:
			if c.Kind == evalcorpus.KindAmbiguous {
				return c.CaseID
			}
		}
	}
	t.Fatalf("no case for role %s", role)
	return ""
}

// corpusTestRoles are the roles this build serves; the two adaptive replay roles wait for the matched
// scripted attacker and are refused as role-unavailable.
var corpusTestRoles = []string{corpusRoleTrajectory, corpusRoleStaticReplay, corpusRolePStaticReplay,
	corpusRoleVariance1, corpusRoleVariance2, corpusRoleCompliant, corpusRoleAmbiguous}

func TestCorpusRoleSetIsClosed(t *testing.T) {
	for _, role := range append([]string{corpusRoleAdaptiveReplay, corpusRolePAdaptiveReplay}, corpusTestRoles...) {
		if !validCorpusRole(role) {
			t.Errorf("role %q is not valid", role)
		}
	}
	for _, role := range []string{"", "variance", "variance-3", "Trajectory", "static_replay", "trajectory ", "p", "baseline", "variance-repeat"} {
		if validCorpusRole(role) {
			t.Errorf("role %q is valid", role)
		}
	}
}

func TestCorpusServiceAdmitsEachRoleExactlyOnce(t *testing.T) {
	h := newCorpusServiceHarness(t)
	for i, role := range corpusTestRoles {
		caseID := corpusCaseForRole(t, h.manifest, role)
		request := corpusRequest("tbr-role-"+role, caseID, role, 1)
		response := roundTripPiServiceRequest(t, h.server.path, request)
		if response.Status != "completed" || int(h.server.calls.Load()) != i+1 {
			t.Fatalf("role %s was not admitted: %+v calls=%d", role, response, h.server.calls.Load())
		}
		var receipt struct {
			HostAdmissionID string `json:"host_admission_id"`
			TaskID          string `json:"task_id"`
		}
		if err := json.Unmarshal(response.Receipt, &receipt); err != nil || receipt.HostAdmissionID != caseID+"/"+role+"/1" || receipt.TaskID != caseID {
			t.Fatalf("role %s admission id = %+v, %v", role, receipt, err)
		}
		// The same request replays its terminal; a new request id for the used admission is refused.
		if replay := roundTripPiServiceRequest(t, h.server.path, request); replay.Status != "completed" || int(h.server.calls.Load()) != i+1 {
			t.Fatalf("role %s retry did not replay: %+v calls=%d", role, replay, h.server.calls.Load())
		}
		reused := corpusRequest("tbr-role-again-"+role, caseID, role, 1)
		if refused := roundTripPiServiceRequest(t, h.server.path, reused); refused.Status != "refused" || refused.ErrorCode != "admission-already-used" {
			t.Fatalf("role %s admission was reusable: %+v", role, refused)
		}
	}
	if got := h.journalRecords(t); got != 2*len(corpusTestRoles) {
		t.Fatalf("request journal has %d records, want %d", got, 2*len(corpusTestRoles))
	}
}

func TestCorpusServiceRefusesARoleThatDoesNotMatchTheCaseKind(t *testing.T) {
	h := newCorpusServiceHarness(t)
	adversarial := corpusCaseForRole(t, h.manifest, corpusRoleTrajectory)
	variance := corpusCaseForRole(t, h.manifest, corpusRoleVariance1)
	compliant := corpusCaseForRole(t, h.manifest, corpusRoleCompliant)
	ambiguous := corpusCaseForRole(t, h.manifest, corpusRoleAmbiguous)
	nonVariance := ""
	for _, c := range h.manifest.Cases {
		if c.Kind == evalcorpus.KindAdversarial && !c.Adversarial.VarianceSubset {
			nonVariance = c.CaseID
			break
		}
	}
	if nonVariance == "" {
		t.Fatal("no non-variance adversarial case in the test manifest")
	}
	for name, request := range map[string]piServiceRequest{
		"trajectory on compliant":      corpusRequest("m1", compliant, corpusRoleTrajectory, 1),
		"static-replay on ambiguous":   corpusRequest("m2", ambiguous, corpusRoleStaticReplay, 1),
		"adaptive-replay on compliant": corpusRequest("m3", compliant, corpusRoleAdaptiveReplay, 1),
		"variance-1 on compliant":      corpusRequest("m4", compliant, corpusRoleVariance1, 1),
		"variance-2 on non-variance":   corpusRequest("m5", nonVariance, corpusRoleVariance2, 1),
		"variance-1 on non-variance":   corpusRequest("m6", nonVariance, corpusRoleVariance1, 1),
		"compliant on adversarial":     corpusRequest("m7", adversarial, corpusRoleCompliant, 1),
		"compliant on variance":        corpusRequest("m8", variance, corpusRoleCompliant, 1),
		"ambiguous on adversarial":     corpusRequest("m9", adversarial, corpusRoleAmbiguous, 1),
		"ambiguous on compliant":       corpusRequest("m10", compliant, corpusRoleAmbiguous, 1),
	} {
		response := roundTripPiServiceRequest(t, h.server.path, request)
		if response.Status != "refused" || response.ErrorCode != "role-kind-mismatch" {
			t.Errorf("%s: %+v", name, response)
		}
	}
	for name, request := range map[string]piServiceRequest{
		"no role":      {RequestID: "r1", TaskID: compliant, Attempt: 1},
		"unknown role": corpusRequest("r2", compliant, "variance-3", 1),
		"p role":       corpusRequest("r3", compliant, "p", 1),
	} {
		if response := roundTripPiServiceRequest(t, h.server.path, request); response.Status != "refused" {
			t.Errorf("%s accepted: %+v", name, response)
		}
	}
	if h.server.calls.Load() != 0 || h.journalRecords(t) != 0 {
		t.Fatalf("a refused role reached the runner or the journal: calls=%d records=%d", h.server.calls.Load(), h.journalRecords(t))
	}
	// The mismatch consumed nothing: the right role of the same case is still admitted.
	if response := roundTripPiServiceRequest(t, h.server.path, corpusRequest("m1", compliant, corpusRoleCompliant, 1)); response.Status != "completed" {
		t.Fatalf("a refused request id was consumed: %+v", response)
	}
}

func TestCorpusServiceAdmitsDifferentRolesOfOneCaseIndependently(t *testing.T) {
	h := newCorpusServiceHarness(t)
	caseID := corpusCaseForRole(t, h.manifest, corpusRoleVariance1)
	roles := []string{corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleVariance1, corpusRoleVariance2}
	for i, role := range roles {
		response := roundTripPiServiceRequest(t, h.server.path, corpusRequest("tbr-indep-"+role, caseID, role, 1))
		if response.Status != "completed" || int(h.server.calls.Load()) != i+1 {
			t.Fatalf("role %s of %s was not admitted independently: %+v calls=%d", role, caseID, response, h.server.calls.Load())
		}
	}
	// Attempt 2 of one role does not touch the other roles' admissions.
	if response := roundTripPiServiceRequest(t, h.server.path, corpusRequest("tbr-indep-rerun", caseID, corpusRoleTrajectory, 2)); response.Status != "completed" {
		t.Fatalf("attempt 2 of trajectory refused: %+v", response)
	}
	h.catalog.mu.Lock()
	defer h.catalog.mu.Unlock()
	workflows := map[string]bool{}
	for _, role := range roles {
		hash, ok := h.catalog.byHostAdmissionID[caseID+"/"+role+"/1"]
		if !ok {
			t.Fatalf("no admission for %s/%s/1", caseID, role)
		}
		workflows[h.catalog.entries[hash].WorkflowID] = true
	}
	if len(workflows) != len(roles) || len(h.catalog.byHostAdmissionID) != len(roles)+1 {
		t.Fatalf("one workflow per admission expected: %d workflows, %d admissions", len(workflows), len(h.catalog.byHostAdmissionID))
	}
}

func TestSingleTaskServiceRefusesARole(t *testing.T) {
	fresh := corpusTestServiceConfig("")
	fresh.WorkflowAdmissionMode, fresh.CorpusManifestPath, fresh.CorpusManifestDigest = "fresh", "", ""
	catalog := &piServiceWorkflowCatalog{}
	peer := piServicePeerCredentials{PID: 1, UID: 1000, GID: 1000}
	if code := catalog.screenRequest(fresh, peer, piServiceRequest{RequestID: "r", TaskID: "t", Prompt: "p", Role: "trajectory"}); code == "" {
		t.Fatal("a single-task service accepted a role")
	}
	if err := checkPiServiceClientRequest(fresh, piServiceRequest{RequestID: "r", TaskID: "t", Prompt: "p", Role: "trajectory"}); err == nil {
		t.Fatal("a single-task client request carried a role")
	}
	config, _ := installCorpusTestManifest(t, corpusTestManifest(t))
	for _, request := range []piServiceRequest{
		{RequestID: "r", TaskID: "c", Attempt: 1},
		{RequestID: "r", TaskID: "c", Attempt: 1, Role: "variance-3"},
	} {
		if err := checkPiServiceClientRequest(config, request); err == nil {
			t.Errorf("corpus client request without a valid role accepted: %+v", request)
		}
		if err := validatePiServiceRequest(request); err == nil {
			t.Errorf("wire request without a valid role accepted: %+v", request)
		}
	}
	if err := validatePiServiceRequest(piServiceRequest{RequestID: "r", TaskID: "c", Role: "trajectory", Prompt: "p"}); err == nil {
		t.Error("a role without an attempt was accepted")
	}
}
