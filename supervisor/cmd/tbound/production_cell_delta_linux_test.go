//go:build linux

package main

import (
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

func TestCellDeltaNamesAreTheManifestNames(t *testing.T) {
	if piruntime.D09CellDeltaNetSink != evalcorpus.PDeltaNetSink || piruntime.D09CellDeltaNoReap != evalcorpus.PDeltaNoReap {
		t.Fatalf("the cell delta names drifted from the manifest names: %q %q", piruntime.D09CellDeltaNetSink, piruntime.D09CellDeltaNoReap)
	}
}

// Only the P half runs a relaxed command cell. The paired C, its variance and adaptive replays,
// the compliant and ambiguous roles and B never get a relaxed template, whatever the case registers.
func TestCommandCellDeltaIsSelectedByRoleAndPinnedDeltaOnly(t *testing.T) {
	pRoles := []string{corpusRolePStaticReplay, corpusRolePAdaptiveReplay}
	for _, tc := range []struct{ pDelta, want string }{
		{evalcorpus.PDeltaNetSink, piruntime.D09CellDeltaNetSink}, {evalcorpus.PDeltaNoReap, piruntime.D09CellDeltaNoReap},
		{evalcorpus.PDeltaNetSinkAdmitAll, piruntime.D09CellDeltaNetSink},
		{evalcorpus.PDeltaAdmitAll, ""}, {evalcorpus.PDeltaNone, ""}, {"", ""},
	} {
		for _, role := range pRoles {
			delta, err := commandCellDeltaFor(role, tc.pDelta)
			if err != nil || delta.Name() != tc.want {
				t.Errorf("%s under %q: cell delta %q (%v), want %q", role, tc.pDelta, delta.Name(), err, tc.want)
			}
		}
		// admit-all/v1, none and the absent member relax no cell for any role.
		if tc.want == "" {
			for _, role := range []string{corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleCompliant} {
				if delta, err := commandCellDeltaFor(role, tc.pDelta); err != nil || delta.Relaxed() {
					t.Errorf("%s under %q: %+v %v", role, tc.pDelta, delta, err)
				}
			}
		}
	}
	for _, role := range []string{corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleAdaptiveReplay, corpusRoleVariance1, corpusRoleVariance2,
		corpusRoleCompliant, corpusRoleAmbiguous, corpusRoleBCompliant, corpusRoleBAmbiguous, "static-none", ""} {
		for _, pDelta := range []string{evalcorpus.PDeltaNetSink, evalcorpus.PDeltaNoReap, evalcorpus.PDeltaNetSinkAdmitAll} {
			if delta, err := commandCellDeltaFor(role, pDelta); err == nil || delta.Relaxed() {
				t.Errorf("role %q was given the relaxed cell %s: %+v %v", role, pDelta, delta, err)
			}
		}
	}
	for _, near := range []string{"net-sink/v2", "admit-all+net-sink/v1", "net-sink+admit-all/v2", "net-sink+no-reap/v1"} {
		if _, err := commandCellDeltaFor(corpusRolePStaticReplay, near); err == nil {
			t.Errorf("the unregistered delta %q selected a cell", near)
		}
	}
}

// A case that registers net-sink, no-reap or net-sink+admit-all is still run by its C roles with the full control package.
func TestBindingGivesEveryNonPRoleTheSignedBaseCell(t *testing.T) {
	for _, pDelta := range []string{evalcorpus.PDeltaNetSink, evalcorpus.PDeltaNoReap, evalcorpus.PDeltaNetSinkAdmitAll} {
		_, manifest := scriptedTestCorpus(t, nil)
		caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
		corpus, _ := scriptedTestCorpus(t, func(m *evalcorpus.Manifest) {
			for i := range m.Cases {
				if m.Cases[i].CaseID == caseID {
					m.Cases[i].Trial.PDelta = pDelta
					m.Cases[i].Adversarial.CalibrationPayloadDigest, _ = evalcorpus.CalibrationPayloadDigest(m.Cases[i].Trial.Script, pDelta)
				}
			}
		})
		checked := 0
		for _, role := range []string{corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleVariance1} {
			if !corpus.roleServable(caseID, role) {
				continue
			}
			binding, err := corpus.bindingFor(caseID, role)
			if err != nil {
				t.Fatalf("%s/%s: %v", caseID, role, err)
			}
			cell, err := commandCellDeltaFor(binding.role, binding.delta)
			if err != nil || cell.Relaxed() {
				t.Errorf("%s of a %s case runs a relaxed cell: %+v %v", role, pDelta, cell, err)
			}
			if binding.delta != evalcorpus.PDeltaNone || binding.scope.Rule == sessionrepo.AdmissionScopeRulePromoteAll {
				t.Errorf("%s of a %s case binds delta %q scope %+v", role, pDelta, binding.delta, binding.scope)
			}
			checked++
		}
		if checked == 0 {
			t.Fatal("no C role was checked")
		}
		binding, err := corpus.bindingFor(caseID, corpusRolePStaticReplay)
		if err != nil {
			t.Fatal(err)
		}
		if cell, err := commandCellDeltaFor(binding.role, binding.delta); err != nil || cell.Name() != evalcorpus.PDeltaCell(pDelta) {
			t.Errorf("p-static-replay of a %s case runs cell %q (%v)", pDelta, cell.Name(), err)
		}
	}
}

// net-sink+admit-all/v1 is both halves: the P binding runs the net-sink/v1 cell (the same template digest)
// under promote-all admission, and the service reports the admit-all admission policy digest that the
// host expects for it. Its C roles keep the base cell and the control package.
func TestCombinedDeltaPBindingIsTheNetSinkCellUnderPromoteAll(t *testing.T) {
	_, manifest := scriptedTestCorpus(t, nil)
	caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	corpus, _ := scriptedTestCorpus(t, func(m *evalcorpus.Manifest) {
		for i := range m.Cases {
			if m.Cases[i].CaseID == caseID {
				m.Cases[i].Trial.PDelta = evalcorpus.PDeltaNetSinkAdmitAll
				m.Cases[i].Adversarial.CalibrationPayloadDigest, _ = evalcorpus.CalibrationPayloadDigestOf(m.Cases[i].Trial.Script,
					m.Cases[i].Trial.Pivots, evalcorpus.PDeltaNetSinkAdmitAll)
			}
		}
	})
	trial := corpus.trials[caseID]
	policy, metadata := corpusTestDigest("sp"), corpusTestDigest("mp")
	admitAll, _ := evalcorpus.AdmissionPolicyDigest(policy, metadata, true, nil)
	control, _ := evalcorpus.AdmissionPolicyDigest(policy, metadata, false, trial.ProtectedPaths)
	netSink, _ := piruntime.ParseD09CellDelta(piruntime.D09CellDeltaNetSink)
	profile := corpusTestDigest("containment profile")
	wantTemplate, err := netSink.TemplateDigest(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !corpus.roleServable(caseID, corpusRolePStaticReplay) {
		t.Fatal("the P static replay of a net-sink+admit-all/v1 case is not served")
	}
	binding, err := corpus.bindingFor(caseID, corpusRolePStaticReplay)
	if err != nil {
		t.Fatal(err)
	}
	cell, err := commandCellDeltaFor(binding.role, binding.delta)
	if err != nil || cell.Name() != piruntime.D09CellDeltaNetSink || !cell.NetSink() || cell.NoReap() || cell.NetworkMode() != "host" {
		t.Fatalf("P cell %+v (%v), want the net-sink/v1 cell", cell, err)
	}
	if template, err := cell.TemplateDigest(profile); err != nil || template != wantTemplate {
		t.Errorf("P cell template %q (%v), want the net-sink/v1 template %q", template, err, wantTemplate)
	}
	if binding.delta != evalcorpus.PDeltaNetSinkAdmitAll || binding.scope.Rule != sessionrepo.AdmissionScopeRulePromoteAll {
		t.Errorf("P binds delta %q scope %+v, want net-sink+admit-all/v1 under promote-all", binding.delta, binding.scope)
	}
	if got, err := binding.admissionPolicyDigest(policy, metadata); err != nil || got != admitAll {
		t.Errorf("P admission policy digest %q (%v), want the admit-all form %q", got, err, admitAll)
	}
	c, err := corpus.bindingFor(caseID, corpusRoleStaticReplay)
	if err != nil {
		t.Fatal(err)
	}
	if cell, err := commandCellDeltaFor(c.role, c.delta); err != nil || cell.Relaxed() || c.delta != evalcorpus.PDeltaNone {
		t.Errorf("C binds delta %q cell %+v (%v)", c.delta, cell, err)
	}
	if got, err := c.admissionPolicyDigest(policy, metadata); err != nil || got != control {
		t.Errorf("C admission policy digest %q (%v), want the control package %q", got, err, control)
	}
}

func TestPStaticReplayIsServedForBothCellDeltas(t *testing.T) {
	for _, pDelta := range []string{evalcorpus.PDeltaNetSink, evalcorpus.PDeltaNoReap, evalcorpus.PDeltaNetSinkAdmitAll} {
		if !implementedPDelta(pDelta) {
			t.Errorf("%s is not implemented", pDelta)
		}
	}
	if implementedPDelta("net-sink/v2") || implementedPDelta("") || implementedPDelta("admit-all+net-sink/v1") {
		t.Error("an unregistered delta is implemented")
	}
}

func tolerantSettlement() sessionrepo.CommandSettlement {
	return sessionrepo.CommandSettlement{LeaseID: "lease-1", ViewID: "view-1", ExitObserved: true, SurvivorsTolerated: true,
		EvidenceClass: piruntime.D09CommandRunnerProfile + ":" + piruntime.D09NoReapSettlementEvidence}
}

func closedSettlement() sessionrepo.CommandSettlement {
	return sessionrepo.CommandSettlement{LeaseID: "lease-1", ViewID: "view-1", ExitObserved: true, ProcessScopeEmpty: true,
		WritersStopped: true, MountDetached: true, EvidenceClass: piruntime.D09CommandRunnerProfile + ":settled"}
}

func verifyWith(t *testing.T, survivorsTolerated bool, retained, candidate sessionrepo.CommandSettlement) error {
	t.Helper()
	runner := newProductionCommandRunner(nil)
	runner.survivorsTolerated = survivorsTolerated
	runner.receipts["view-1"] = retained
	runner.leases["view-1"] = productionCommandLease{leaseID: "lease-1"}
	return runner.VerifySettlement(candidate, "view-1", "lease-1")
}

// The tolerance for descendants that stay alive exists for a no-reap lease and nothing else.
func TestSurvivorToleranceHoldsOnlyForANoReapLease(t *testing.T) {
	tolerant, closed := tolerantSettlement(), closedSettlement()
	if err := verifyWith(t, true, tolerant, tolerant); err != nil {
		t.Fatalf("a no-reap lease refused its tolerated settlement: %v", err)
	}
	if err := verifyWith(t, false, tolerant, tolerant); err == nil {
		t.Error("a lease that is not no-reap accepted a settlement with survivors")
	}
	if err := verifyWith(t, true, closed, closed); err == nil {
		t.Error("a no-reap lease accepted a settlement that claims its cell was fully reaped")
	}
	if err := verifyWith(t, false, closed, closed); err != nil {
		t.Errorf("an ordinary lease refused a closed settlement: %v", err)
	}
	for name, mutate := range map[string]func(*sessionrepo.CommandSettlement){
		"survivors with an empty scope":      func(s *sessionrepo.CommandSettlement) { s.ProcessScopeEmpty = true },
		"survivors with stopped writers":     func(s *sessionrepo.CommandSettlement) { s.WritersStopped = true },
		"survivors with a detached mount":    func(s *sessionrepo.CommandSettlement) { s.MountDetached = true },
		"survivors without an observed exit": func(s *sessionrepo.CommandSettlement) { s.ExitObserved = false },
	} {
		bad := tolerantSettlement()
		mutate(&bad)
		if err := verifyWith(t, true, bad, bad); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// A candidate that differs from the retained receipt in the tolerance flag is not that receipt.
	candidate := tolerantSettlement()
	candidate.SurvivorsTolerated = false
	if err := verifyWith(t, true, tolerant, candidate); err == nil {
		t.Error("a candidate without the tolerance flag matched a tolerated receipt")
	}
}

func TestSettlementAdmissionShapes(t *testing.T) {
	if (sessionrepo.CommandSettlement{LeaseID: "l", ViewID: "v", ExitObserved: true}).Admitted() {
		t.Error("an unsettled settlement without the tolerance flag was admitted")
	}
	if !tolerantSettlement().Admitted() || !closedSettlement().Admitted() {
		t.Error("the two registered settlement shapes must be admitted")
	}
	both := closedSettlement()
	both.SurvivorsTolerated = true
	if both.Admitted() {
		t.Error("a settlement that claims to be closed and to have survivors was admitted")
	}
}
