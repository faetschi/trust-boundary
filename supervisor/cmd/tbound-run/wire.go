package main

import (
	"time"

	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
)

// wireFunc builds the executors of a plan. It is a seam: tests inject fakes, the
// binary uses wireSSH.
type wireFunc func(p plan) (runner.Executors, error)

// wireSSH wires Configuration C to the guest over three SSH identities:
//
//	Attest( ResetExecutor( GovernedC ) )
//
// GovernedC drives `tbound serve --pi` with the client key and gets its evidence from
// the evidence key (E04 and publication journal, the NET sink, the PROC monitor); the
// reset wrapper calls `tbound trial reset` with the trial key; Attest adds what the host
// establishes (channel, E01 identity against the pins, route, EVQ, matched delivery) and
// refuses a delta on a C receipt.
//
// P, B and V have no executor yet: they stay nil, so Executors.Check (selfcheck, run)
// reports ErrNotWired for any plan that contains them. Nothing is filtered or skipped.
func wireSSH(p plan) (runner.Executors, error) {
	ts := p.cfg.transports()
	governed := runner.GovernedC{PlanDigest: p.sched.Digest, Transport: ts.client, Collectors: p.collectors()}
	return runner.Executors{C: p.attest(runner.ResetExecutor{Inner: governed, Transport: ts.trial})}, nil
}

// collectors are the host-side evidence sources, all over the evidence key.
func (p plan) collectors() runner.CollectorSet {
	ev := p.cfg.transports().evidence
	ssh := runner.NewSSHCollectors(ev)
	proc := &procoracle.SSHCollector{
		Client:          runner.EvidenceClient{Transport: ev},
		CellUID:         p.cfg.Proc.CellUID,
		CellGID:         p.cfg.Proc.CellGID,
		MonitorDuration: time.Duration(p.cfg.Proc.MonitorSeconds) * time.Second,
		TeardownGrace:   time.Duration(p.cfg.Proc.TeardownGraceSecs) * time.Second,
		Policy:          procoracle.Policy{AuthorizedSockets: p.cfg.Proc.AuthorizedSockets},
	}
	return runner.CollectorSet{E04From: ssh, JournalFrom: ssh, NetFrom: netoracle.NewCollector(ev), ProcFrom: proc}
}

// attest wraps an executor with the host-side attestation of this run.
func (p plan) attest(inner runner.Executor) runner.Attest {
	return runner.Attest{
		Inner: inner, Pins: p.cfg.Pins, EVQ: p.cfg.EVQObligations, Route: p.route,
		// TODO(increment B): read the manifest's Trial.p_delta once evalcorpus has the
		// field. Until then no case has a registered P delta, so every P receipt is refused.
		PDelta: func(string) (string, bool) { return "", false },
	}
}
