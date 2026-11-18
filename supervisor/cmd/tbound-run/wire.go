package main

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
)

// wireFunc builds the executors of a plan. It is a seam: tests inject fakes, the
// binary uses wireSSH.
type wireFunc func(p plan) (runner.Executors, error)

// wireSSH wires Configuration C to the guest over three SSH identities:
//
//	Attest( ResetExecutor( GovernedC ) )          for C, and the same stack with Configuration P for P
//
// GovernedC drives `tbound serve --pi` with the client key and gets its evidence from
// the evidence key (E04, the publication journal and the EFF-WS-05/06 inputs, the NET sink, the PROC monitor,
// the transient-mutation watch); the reset wrapper calls `tbound trial reset` with the trial key; Attest adds what the host
// establishes (channel, E01 identity against the pins, route, EVQ, matched delivery) and
// refuses a delta on a C receipt. clockGuard, outermost on C and P, reads the guest clock over the
// evidence key before every adversarial step and refuses it when PROC could not compare the clocks.
//
// A compliant C step also gets its task result: AcceptExecutor calls `tbound trial accept` after
// the governed run. V is GovernedV over the trial key (`tbound trial v-run`, native Pi in a plain
// container, no Attest; see wire_v.go).
//
// B is the same stack with Configuration B (b-compliant and b-ambiguous: the real model, an allow-all gate
// and admit-all/v1 admission); a compliant B step also gets its task result from AcceptExecutor.
func wireSSH(p plan) (runner.Executors, error) {
	ts := p.cfg.transports()
	governed := runner.GovernedC{PlanDigest: p.sched.Digest, Transport: ts.client, Collectors: p.collectors(), ProtectedPaths: p.protectedPaths,
		ResendWindow: runner.DefaultResendWindow, ResendPause: runner.DefaultResendPause}
	var dispatchDir string
	if p.cfg.RunDir != "" {
		governed.WSEvidenceDir = filepath.Join(p.cfg.RunDir, "ws-evidence")
		dispatchDir = filepath.Join(p.cfg.RunDir, "dispatched")
	}
	reset := func(inner runner.Executor) runner.ResetExecutor {
		return runner.ResetExecutor{Inner: inner, Transport: ts.trial, DispatchDir: dispatchDir}
	}
	evidence := runner.EvidenceClient{Transport: ts.evidence}
	guard := func(x runner.Executor) runner.Executor {
		return clockGuard{Executor: x, sample: func(ctx context.Context) (clockSample, error) {
			return sampleGuestClock(ctx, evidence, p.cfg.Proc.CellUID, p.cfg.Proc.CellGID)
		}}
	}
	governedP := governed
	governedP.Configuration = runner.CfgP
	executorP := guard(p.attest(reset(governedP)))
	if p.sched.Config.Purpose == runner.PurposeConstructWitness {
		// A construct-witness run reaches the guest as Configuration P only, so no other executor is wired.
		return runner.Executors{P: executorP}, nil
	}
	governedB := governed
	governedB.Configuration = runner.CfgB
	accept := func(inner runner.Executor) runner.AcceptExecutor {
		x := runner.AcceptExecutor{FreezeCommit: p.sched.Config.FreezeCommit, Inner: inner, Transport: ts.trial,
			Expect: runner.AcceptExpect{ManifestDigest: p.digest, BinarySHA256: p.cfg.Pins.RunnerDigest, ImageDigest: p.cfg.VImageDigest,
				SpecDigest: p.acceptanceDigest}}
		if p.cfg.RunDir != "" {
			x.EvidenceDir = filepath.Join(p.cfg.RunDir, "accept-evidence")
		}
		return x
	}
	executors := runner.Executors{
		C: guard(p.attest(accept(reset(governed)))),
		P: executorP,
		B: p.attest(accept(reset(governedB))),
		V: p.governedV(ts.trial),
	}
	if p.cfg.CDH != nil {
		executors.CDH = map[runner.Configuration]runner.Executor{}
		for _, model := range p.cfg.CDH.Manifest.Models {
			g := governed
			g.Configuration = runner.CDHConfiguration(model)
			g.CDH = p.cfg.CDH
			executors.CDH[g.Configuration] = guard(p.attest(accept(reset(g))))
		}
	}
	return executors, nil
}

// collectors are the host-side evidence sources, all over the evidence key.
func (p plan) collectors() runner.CollectorSet { return p.collectorsOver(p.cfg.transports().evidence) }

// collectorsOver is collectors over a given evidence transport (the seam of the observer-coverage test).
func (p plan) collectorsOver(ev runner.Transport) runner.CollectorSet {
	ssh := runner.NewSSHCollectors(ev)
	proc := &procoracle.SSHCollector{
		Client:          runner.EvidenceClient{Transport: ev},
		CellUID:         p.cfg.Proc.CellUID,
		CellGID:         p.cfg.Proc.CellGID,
		MonitorDuration: time.Duration(p.cfg.Proc.MonitorSeconds) * time.Second,
		TeardownGrace:   time.Duration(p.cfg.Proc.TeardownGraceSecs) * time.Second,
		DrainGrace:      time.Duration(p.cfg.Proc.DrainGraceSecs) * time.Second,
		Policy:          procoracle.Policy{AuthorizedSockets: p.cfg.Proc.AuthorizedSockets},
		InitLingers:     p.initLingers,
	}
	if p.cfg.RunDir != "" {
		proc.RetainDir = filepath.Join(p.cfg.RunDir, "proc-evidence")
	}
	// The transient observer is ended by the host when the request is over; the PROC monitor's time is only its cap.
	transient := &runner.TransientCollector{Client: runner.EvidenceClient{Transport: ev}, Duration: time.Duration(p.cfg.Proc.MonitorSeconds) * time.Second}
	if p.cfg.RunDir != "" {
		transient.RetainDir = filepath.Join(p.cfg.RunDir, "transient-evidence")
	}
	// The NET interval documents are kept next to the journal: their tags are drawn per interval and live nowhere else.
	net := netoracle.NewCollector(ev)
	// D49: the NET window ends after the PROC collector's proof plus the drain grace, or at the original horizon.
	net.Gate = proc
	net.Horizon = proc.MonitorDuration
	if p.cfg.RunDir != "" {
		net.RetainDir = filepath.Join(p.cfg.RunDir, "net-evidence")
	}
	return runner.CollectorSet{E04From: ssh, JournalFrom: ssh, PublicationFrom: ssh, NetFrom: net, ProcFrom: proc, TransientFrom: transient}
}

// attest wraps an executor with the host-side attestation of this run.
func (p plan) attest(inner runner.Executor) runner.Attest {
	var hintDigest string
	if p.cfg.CDH != nil {
		hintDigest, _ = p.cfg.CDH.Manifest.Digest()
	}
	return runner.Attest{
		HintManifestDigest: hintDigest,
		Inner:              inner, Pins: p.cfg.Pins.ReceiptPins, EVQ: p.cfg.EVQObligations, EVQResults: p.evq, Route: p.route,
		PDelta: p.pDelta, ScriptDigest: p.scriptDigest, AdmissionPolicyDigest: p.admissionPolicyDigest,
		AttackerDigest: p.manifest.Pins.AttackerDigest, PivotsDigest: p.pivotsDigest,
	}
}

// acceptanceDigest is the digest of a compliant case's pinned acceptance artifact, "" for any other case.
func (p plan) acceptanceDigest(caseID string) string {
	for _, c := range p.manifest.Cases {
		if c.CaseID == caseID && c.Compliant != nil {
			return c.Compliant.AcceptanceTest.Digest
		}
	}
	return ""
}

// pivotsDigest is the digest of the pivot table of a case, as the service computes it for the receipt of an
// adaptive replay.
func (p plan) pivotsDigest(caseID string) (string, bool) {
	for _, c := range p.manifest.Cases {
		if c.CaseID == caseID && c.Trial != nil && len(c.Trial.Pivots) != 0 {
			digest, err := evalcorpus.PivotsDigest(c.Trial.Pivots)
			return digest, err == nil
		}
	}
	return "", false
}

// pDelta is the registered targeted configuration delta of a case: the p_delta of its trial, "none"
// when absent. Only an adversarial case can register one.
func (p plan) pDelta(caseID string) (string, bool) {
	for _, c := range p.manifest.Cases {
		if c.CaseID == caseID {
			if c.Trial == nil {
				return "", false
			}
			return c.Trial.EffectivePDelta(), true
		}
	}
	return "", false
}

// initLingers is true for the P half of a case whose registered delta is no-reap/v1: its command cells keep their
// init alive after the command, so EFF-PROC-01 must not count that init as a surviving descendant. The delta is
// refused for every other role, and the C half of the same case must still see a cell that outlived its teardown.
func (p plan) initLingers(ref runner.EvidenceRef) bool {
	delta, ok := p.pDelta(ref.Step.CaseID)
	return ok && ref.Step.Configuration == runner.CfgP && delta == evalcorpus.PDeltaNoReap
}

// protectedPaths are the protected_paths of a case's trial, nil when it has none. The E04 verdicts of an
// adversarial trial, in P and in C, are scored over them and never credit the publication ledger for them.
func (p plan) protectedPaths(caseID string) []string {
	for _, c := range p.manifest.Cases {
		if c.CaseID == caseID && c.Trial != nil {
			return slices.Clone(c.Trial.ProtectedPaths)
		}
	}
	return nil
}

// scriptDigest is the digest of the frozen script of a case, as the service computes it for its receipt.
func (p plan) scriptDigest(caseID string) (string, bool) {
	for _, c := range p.manifest.Cases {
		if c.CaseID == caseID && c.Trial != nil && len(c.Trial.Script) != 0 {
			digest, err := evalcorpus.ScriptDigest(c.Trial.Script)
			return digest, err == nil
		}
	}
	return "", false
}

// admissionPolicyDigest is the admission_policy_digest the service must report for a case under a
// configuration: B and P under admit-all/v1 or net-sink+admit-all/v1 relax admission to promote-all, every other run keeps the
// manifest's protected paths (the control package).
func (p plan) admissionPolicyDigest(caseID string, cfg runner.Configuration) (string, bool) {
	for _, c := range p.manifest.Cases {
		if c.CaseID != caseID || c.Trial == nil {
			continue
		}
		promoteAll := cfg == runner.CfgB || (cfg == runner.CfgP && evalcorpus.PDeltaAdmitsAll(c.Trial.EffectivePDelta()))
		digest, err := evalcorpus.AdmissionPolicyDigest(p.cfg.AdmissionPolicy.PolicyDigest,
			p.cfg.AdmissionPolicy.MetadataPolicyDigest, promoteAll, c.Trial.ProtectedPaths)
		return digest, err == nil
	}
	return "", false
}
