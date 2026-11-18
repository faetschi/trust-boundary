package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/vrun"
)

func TestWiringOfAcceptAndV(t *testing.T) {
	e := newEnv(t)
	route := runner.RouteCoverage{Complete: true, Digest: "route-digest"}
	fixture := "sha256:" + strings.Repeat("f", 64)
	p := plan{cfg: mustConfig(t, e), sched: e.sched(), route: route, manifest: evalcorpus.Manifest{Cases: []evalcorpus.Case{
		{CaseID: "cmp-1", Kind: evalcorpus.KindCompliant, Compliant: &evalcorpus.Compliant{WorkspaceFixtureDigest: fixture}},
		{CaseID: "amb-1", Kind: evalcorpus.KindAmbiguous, Ambiguous: &evalcorpus.Ambiguous{WorkspaceFixtureDigest: fixture}},
	}}}
	ex, err := wireSSH(p)
	if err != nil {
		t.Fatal(err)
	}
	// C: Attest over the task result over the reset over the governed run, all trial calls on
	// the trial transport.
	attest, ok := unguarded(ex.C).(runner.Attest)
	if !ok {
		t.Fatalf("C is %T, want the Attest decorator", ex.C)
	}
	accept, ok := attest.Inner.(runner.AcceptExecutor)
	if !ok {
		t.Fatalf("C inner is %T, want the accept executor", attest.Inner)
	}
	reset, ok := accept.Inner.(runner.ResetExecutor)
	if !ok {
		t.Fatalf("accept inner is %T, want the reset wrapper", accept.Inner)
	}
	if _, ok := reset.Inner.(runner.GovernedC); !ok {
		t.Fatalf("reset inner is %T, want GovernedC", reset.Inner)
	}
	trial := p.cfg.transports().trial
	if !reflect.DeepEqual(accept.Transport, runner.Transport(trial)) || !reflect.DeepEqual(reset.Transport, runner.Transport(trial)) {
		t.Fatalf("accept and reset must use the trial transport")
	}
	if err := (runner.ResetExecutor{Transport: accept.Transport}).Validate(); err != nil {
		t.Fatalf("the accept transport is not the trial command: %v", err)
	}
	// P keeps its stack and gets no task call.
	if pa, ok := unguarded(ex.P).(runner.Attest); !ok {
		t.Fatalf("P is %T", ex.P)
	} else if _, ok := pa.Inner.(runner.ResetExecutor); !ok {
		t.Fatalf("P inner is %T, want the reset wrapper", pa.Inner)
	}
	// V: GovernedV on the trial transport, not behind Attest, carrying the route and the
	// manifest's fixture digest.
	v, ok := ex.V.(runner.GovernedV)
	if !ok {
		t.Fatalf("V is %T, want GovernedV", ex.V)
	}
	if !reflect.DeepEqual(v.Transport, runner.Transport(trial)) || v.Route != route {
		t.Fatalf("V = %+v", v)
	}
	if v.ImageDigest != fixtureDigest("cell-image") || v.ImageDigest != p.cfg.VImageDigest {
		t.Fatalf("V image pin = %q, want the run.json pin %q", v.ImageDigest, p.cfg.VImageDigest)
	}
	if got, ok := v.FixtureDigest("cmp-1"); !ok || got != fixture {
		t.Fatalf("fixture digest = %q, %v", got, ok)
	}
	if got, ok := v.FixtureDigest("amb-1"); !ok || got != fixture {
		t.Fatalf("fixture digest of an ambiguous case = %q, %v", got, ok)
	}
	if _, ok := v.FixtureDigest("nope"); ok {
		t.Fatal("a fixture digest for an unknown case")
	}
	if ex.B == nil {
		t.Fatal("B is wired (increment D)")
	}
}

type vDocTransport struct{ raw []byte }

func (v vDocTransport) Run(context.Context, []string) ([]byte, []byte, int, error) {
	return v.raw, nil, 0, nil
}

// The run.json image pin is what makes a V outcome E01-verified: the run's document must name
// the pinned image and the manifest's fixture.
func TestWiredVIsVerifiedOnlyAgainstTheRunConfigImagePin(t *testing.T) {
	e := newEnv(t)
	fixture := "sha256:" + strings.Repeat("f", 64)
	p := plan{cfg: mustConfig(t, e), manifest: evalcorpus.Manifest{Cases: []evalcorpus.Case{
		{CaseID: "cmp-1", Kind: evalcorpus.KindCompliant, Compliant: &evalcorpus.Compliant{WorkspaceFixtureDigest: fixture}},
	}}}
	run := func(image string) bool {
		raw, err := json.Marshal(vrun.Doc{Schema: vrun.Schema, CaseID: "cmp-1", ImageDigest: image, FixtureDigest: fixture,
			StartedNS: 1, FinishedNS: 2, UsageEvents: 1})
		if err != nil {
			t.Fatal(err)
		}
		g := p.governedV(vDocTransport{raw: raw}).(runner.GovernedV)
		out, err := g.Execute(context.Background(), runner.Step{Configuration: runner.CfgV, Kind: evalcorpus.KindCompliant, CaseID: "cmp-1"})
		if err != nil {
			t.Fatal(err)
		}
		return out.E01.Verified
	}
	if !run(p.cfg.VImageDigest) {
		t.Error("a document of the pinned image is not verified")
	}
	if run(fixtureDigest("another-image")) {
		t.Error("a document of another image is verified")
	}
}
