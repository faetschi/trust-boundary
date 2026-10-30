//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

const (
	rtRunID   = "run-route-1"
	rtTarget  = "/home/node/.profile"
	rtControl = "src/release.txt"
)

var (
	rtSource = geDigest("source")
	rtBytes  = []byte("version=2.4.0\nstatus=draft\n")
)

// rtResult is the result the cell returns for each probe method.
func rtResult(method string) sessionrepo.CommandResult {
	switch method {
	case guestevidence.RouteMethodControl:
		return sessionrepo.CommandResult{ExitCode: 0, ExitObserved: true, Stdout: rtBytes}
	case guestevidence.RouteMethodAbsolute:
		return sessionrepo.CommandResult{ExitCode: 1, ExitObserved: true, Stderr: []byte("cat: " + rtTarget + ": Permission denied\n")}
	case guestevidence.RouteMethodTraversal:
		return sessionrepo.CommandResult{ExitCode: 1, ExitObserved: true, Stderr: []byte("cat: " + guestevidence.RouteTraversalOperand(rtTarget) + ": Permission denied\n")}
	}
	return sessionrepo.CommandResult{ExitCode: 1, ExitObserved: true, Stderr: []byte("cat: " + guestevidence.RouteProbeLink + ": Permission denied\n")}
}

func rtReceipt(t *testing.T, method string, result sessionrepo.CommandResult) piruntime.D09CommandReceipt {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	cmd := guestevidence.RouteProbeCommands(rtTarget, rtControl)[method]
	return piruntime.D09CommandReceipt{
		SchemaVersion: "tbound-d09-command-receipt/v1", ContainerID: "container-" + method,
		ImageDigest: geDigest("image"), TemplateDigest: rtTemplateDigest(t), ResultDigest: piruntime.DigestBytes(encoded),
		ReceiptDigest: geDigest("receipt " + method), ExitCode: result.ExitCode,
		CommandSpec: sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", cmd}},
		Proof: piruntime.D09CommandProof{CapEff: "0000000000000000", NoNewPrivs: true, Seccomp: 2, LandlockTSYNC: true, CgroupV2: true,
			ConsumerMountsVerified: true, RawSourceFDsClosed: true, CommandPath: "/bin/bash", NoExternalRoutes: true,
			LandlockRulesetDigest: geDigest("ruleset")},
	}
}

type rtEnv struct {
	audit    string
	response string
	control  string
	opts     routeProbeOptions
	world    bindWorld
}

// rtJournal writes a command audit journal with one settled event per entry of receipts (in order) and a
// matching service response, and returns the options of a probe over them.
func rtJournal(t *testing.T, receipts []piruntime.D09CommandReceipt, mutateEvent func(*map[string]any)) rtEnv {
	t.Helper()
	// The journal opener refuses a path below a world-writable directory such as /tmp, so the journal lives
	// below the home directory.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory for a private audit journal")
	}
	dir, err := os.MkdirTemp(home, ".route-probe-test-")
	if err != nil {
		t.Skipf("cannot make a private audit directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	world := newBindWorld(t, dir)
	j, err := audit.Open(filepath.Join(dir, rtRunID+"-command-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(audit.Event{Kind: "production_session_started", ID: "start"}); err != nil {
		t.Fatal(err)
	}
	for i, r := range receipts {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		event := map[string]any{"schema_version": commandSettledSchema, "run_id": rtRunID, "profile_digest": world.profileDigest,
			"receipt": json.RawMessage(raw), "unread_member": i}
		if mutateEvent != nil {
			mutateEvent(&event)
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.Append(audit.Event{Kind: commandSettledKind, ID: "cmd-" + string(rune('a'+i)), Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	e := rtEnv{audit: dir, response: filepath.Join(t.TempDir(), "response.txt"), control: filepath.Join(t.TempDir(), "release.txt"), world: world}
	receipt := goodGEReceiptFor(world.profileDigest, world.templateDigest, rtSource)
	receipt["run_id"] = rtRunID
	resp, err := json.Marshal(map[string]any{"schema_version": "tbound-pi-service-response/v1", "request_id": "req-1", "status": "completed", "receipt": receipt})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.response, append(resp, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.control, rtBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	e.opts = routeProbeOptions{route: "bash-view", runID: rtRunID, auditRoot: dir, response: e.response, target: rtTarget, controlPath: rtControl, controlFile: e.control,
		pins: world.pins, template: world.files.template, hostProfile: world.files.hostProfile, hostProfileSig: world.files.hostProfileSig, hostProfileKey: world.files.hostProfileKey,
		registration: world.files.registration, registrationSig: world.files.registrationSig}
	return e
}

// goodGEReceiptFor is a completed non-claim-bearing receipt of a run under the given profile and template, of the pinned source.
func goodGEReceiptFor(profile, template, source string) map[string]any {
	return map[string]any{"schema_version": "tbound-production-pi-receipt/v1", "profile_hash": profile, "containment_profile_digest": template, "frozen_source_digest": source,
		"provider_exchange": true, "claim_bearing": false, "task_admitted": true, "turn_completed": true, "success": true,
		"tool_sequence": []string{"bash", "bash", "bash", "bash"}, "provider_turns": 5, "source_generation": "g0", "final_generation": "g0",
		"generation_count": 1, "settlement": "STOPPED", "exit_observed": true, "exit_code": 0, "descendants_settled": true}
}

func rtAllReceipts(t *testing.T) []piruntime.D09CommandReceipt {
	t.Helper()
	var out []piruntime.D09CommandReceipt
	for _, m := range append([]string{guestevidence.RouteMethodControl}, guestevidence.RouteMethods...) {
		out = append(out, rtReceipt(t, m, rtResult(m)))
	}
	return out
}

func rtEnvStub() routeProbeEnv {
	env := productionRouteProbeEnv()
	env.validateReceipt = func(piruntime.D09CommandReceipt) error { return nil }
	return env
}

func TestRouteProbeBuildsAnArtifactThatPasses(t *testing.T) {
	e := rtJournal(t, rtAllReceipts(t), nil)
	d, kind, err := probeRouteDenied(e.opts, rtEnvStub())
	if err != nil {
		t.Fatal(err)
	}
	if kind != guestevidence.KindRouteBashView || d.RunID != rtRunID || d.Journal.Records != 5 || len(d.Attempts) != 3 || d.Control.Outcome != "read" {
		t.Fatalf("artifact = %+v kind %s", d, kind)
	}
	for i, m := range guestevidence.RouteMethods {
		if d.Attempts[i].Method != m || d.Attempts[i].Outcome != "denied" || d.Attempts[i].ExitCode != 1 {
			t.Errorf("attempt %d = %+v", i, d.Attempts[i])
		}
	}
	if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), e.world.expect()); err != nil {
		t.Fatalf("the artifact is refused: %v", err)
	}
	// Commands of the run that are not part of the probe, and events of another run, are skipped.
	extra := rtAllReceipts(t)
	other := rtReceipt(t, guestevidence.RouteMethodAbsolute, rtResult(guestevidence.RouteMethodAbsolute))
	other.CommandSpec.Args[1] = "echo unrelated"
	extra = append(extra, other)
	e2 := rtJournal(t, extra, nil)
	if _, _, err := probeRouteDenied(e2.opts, rtEnvStub()); err != nil {
		t.Fatalf("an unrelated command broke the probe: %v", err)
	}
}

func TestRouteProbeRefusesWhatIsNotADenial(t *testing.T) {
	without := func(method string) []piruntime.D09CommandReceipt {
		var out []piruntime.D09CommandReceipt
		for _, r := range rtAllReceipts(t) {
			if r.CommandSpec.Args[1] != guestevidence.RouteProbeCommands(rtTarget, rtControl)[method] {
				out = append(out, r)
			}
		}
		return out
	}
	// A receipt for the method, but with the result of another command.
	wrongResult := func(method string, result sessionrepo.CommandResult) []piruntime.D09CommandReceipt {
		out := without(method)
		return append(out, rtReceipt(t, method, result))
	}
	for name, receipts := range map[string][]piruntime.D09CommandReceipt{
		"no control":               without(guestevidence.RouteMethodControl),
		"no absolute attempt":      without(guestevidence.RouteMethodAbsolute),
		"no traversal attempt":     without(guestevidence.RouteMethodTraversal),
		"no symlink attempt":       without(guestevidence.RouteMethodSymlink),
		"the target was read":      wrongResult(guestevidence.RouteMethodAbsolute, sessionrepo.CommandResult{ExitCode: 0, ExitObserved: true, Stdout: []byte("# canary\n")}),
		"denied but with output":   wrongResult(guestevidence.RouteMethodTraversal, sessionrepo.CommandResult{ExitCode: 1, ExitObserved: true, Stdout: []byte("x"), Stderr: rtResult(guestevidence.RouteMethodTraversal).Stderr}),
		"another error (ENOENT)":   wrongResult(guestevidence.RouteMethodAbsolute, sessionrepo.CommandResult{ExitCode: 1, ExitObserved: true, Stderr: []byte("cat: " + rtTarget + ": No such file or directory\n")}),
		"control read other bytes": wrongResult(guestevidence.RouteMethodControl, sessionrepo.CommandResult{ExitCode: 0, ExitObserved: true, Stdout: []byte("other\n")}),
		"control failed":           wrongResult(guestevidence.RouteMethodControl, sessionrepo.CommandResult{ExitCode: 1, ExitObserved: true, Stderr: []byte("cat: src/release.txt: Permission denied\n")}),
		"a command ran twice":      append(rtAllReceipts(t), rtReceipt(t, guestevidence.RouteMethodAbsolute, rtResult(guestevidence.RouteMethodAbsolute))),
	} {
		e := rtJournal(t, receipts, nil)
		if _, _, err := probeRouteDenied(e.opts, rtEnvStub()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRouteProbeRefusesWhatIsNotTheProductionCommandCell(t *testing.T) {
	for name, mutate := range map[string]func(*piruntime.D09CommandReceipt){
		"relaxed cell":         func(r *piruntime.D09CommandReceipt) { r.Proof.Delta = "net-sink/v1" },
		"capabilities present": func(r *piruntime.D09CommandReceipt) { r.Proof.CapEff = "0000000000000001" },
		"no no_new_privs":      func(r *piruntime.D09CommandReceipt) { r.Proof.NoNewPrivs = false },
		"no seccomp":           func(r *piruntime.D09CommandReceipt) { r.Proof.Seccomp = 0 },
		"no landlock":          func(r *piruntime.D09CommandReceipt) { r.Proof.LandlockTSYNC = false },
		"unverified mounts":    func(r *piruntime.D09CommandReceipt) { r.Proof.ConsumerMountsVerified = false },
		"another command path": func(r *piruntime.D09CommandReceipt) { r.Proof.CommandPath = "/bin/sh" },
		"external routes":      func(r *piruntime.D09CommandReceipt) { r.Proof.NoExternalRoutes = false },
		"another image":        func(r *piruntime.D09CommandReceipt) { r.ImageDigest = geDigest("another image") },
		"another ruleset":      func(r *piruntime.D09CommandReceipt) { r.Proof.LandlockRulesetDigest = geDigest("another ruleset") },
	} {
		receipts := rtAllReceipts(t)
		mutate(&receipts[2]) // one of the probe commands; the cell must be the same and confined for all of them
		if _, _, err := probeRouteDenied(rtJournal(t, receipts, nil).opts, rtEnvStub()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The receipt's own validator is the default: a receipt made by hand is not one.
	if _, _, err := probeRouteDenied(rtJournal(t, rtAllReceipts(t), nil).opts, productionRouteProbeEnv()); err == nil || !errors.Is(err, piruntime.ErrD09CommandReceiptInvalid) {
		t.Fatalf("a hand-made receipt was accepted: %v", err)
	}
}

func TestRouteProbeBindsTheRunAndTheJournal(t *testing.T) {
	good := rtJournal(t, rtAllReceipts(t), nil)
	for name, mutate := range map[string]func(*routeProbeOptions){
		"another run id":         func(o *routeProbeOptions) { o.runID = "run-other" },
		"relative audit root":    func(o *routeProbeOptions) { o.auditRoot = "audit" },
		"relative target":        func(o *routeProbeOptions) { o.target = "home/node/.profile" },
		"no control file":        func(o *routeProbeOptions) { o.controlFile = filepath.Join(filepath.Dir(o.controlFile), "none") },
		"no response":            func(o *routeProbeOptions) { o.response = filepath.Join(filepath.Dir(o.response), "none") },
		"direct-runtime":         func(o *routeProbeOptions) { o.route = "direct-runtime" },
		"unknown route":          func(o *routeProbeOptions) { o.route = "declared-read" },
		"the journal is missing": func(o *routeProbeOptions) { o.auditRoot = t.TempDir() },
	} {
		o := good.opts
		mutate(&o)
		if _, _, err := probeRouteDenied(o, rtEnvStub()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A profile in the journal that is not the response's.
	foreign := rtJournal(t, rtAllReceipts(t), func(ev *map[string]any) { (*ev)["profile_digest"] = geDigest("other profile") })
	if _, _, err := probeRouteDenied(foreign.opts, rtEnvStub()); err == nil {
		t.Error("events of another profile were accepted")
	}
	// Events of another run are not this run's attempts.
	otherRun := rtJournal(t, rtAllReceipts(t), func(ev *map[string]any) { (*ev)["run_id"] = "run-other" })
	if _, _, err := probeRouteDenied(otherRun.opts, rtEnvStub()); err == nil {
		t.Error("events of another run were accepted")
	}
	// A response that is not a successful run, or is for another run.
	for name, mutate := range map[string]func(map[string]any){
		"unsuccessful":  func(r map[string]any) { r["success"] = false },
		"claim bearing": func(r map[string]any) { r["claim_bearing"] = true },
		"another run":   func(r map[string]any) { r["run_id"] = "run-other" },
		"no run id":     func(r map[string]any) { delete(r, "run_id") },
	} {
		e := rtJournal(t, rtAllReceipts(t), nil)
		receipt := goodGEReceiptFor(e.world.profileDigest, e.world.templateDigest, rtSource)
		receipt["run_id"] = rtRunID
		mutate(receipt)
		resp, _ := json.Marshal(map[string]any{"schema_version": "tbound-pi-service-response/v1", "request_id": "r", "status": "completed", "receipt": receipt})
		if err := os.WriteFile(e.response, append(resp, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := probeRouteDenied(e.opts, rtEnvStub()); err == nil {
			t.Errorf("response %s: accepted", name)
		}
	}
}

func TestRouteProbeRefusesACorruptedJournal(t *testing.T) {
	e := rtJournal(t, rtAllReceipts(t), nil)
	path := filepath.Join(e.audit, rtRunID+"-command-audit.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Change one byte of a result digest inside the journal: the chain no longer verifies.
	i := bytes.Index(data, []byte("cmd-c"))
	if i < 0 {
		t.Fatal("marker not found")
	}
	data[i+4] = 'z'
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := probeRouteDenied(e.opts, rtEnvStub()); err == nil {
		t.Fatal("a journal with a broken hash chain was read")
	}
	// A truncated line is not a journal either.
	if err := os.WriteFile(path, data[:len(data)-7], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := probeRouteDenied(e.opts, rtEnvStub()); err == nil {
		t.Fatal("a truncated journal was read")
	}
}

func TestGuestEvidenceRouteDeniedCommandWritesOnlyValidArtifacts(t *testing.T) {
	// The command-line path: flags, the production environment and the writing of the artifact. A
	// hand-made journal carries no receipt the receipt validator accepts, so nothing is written.
	e := rtJournal(t, rtAllReceipts(t), nil)
	out := filepath.Join(t.TempDir(), "route.json")
	var stdout, stderr bytes.Buffer
	err := runGuestEvidence([]string{"route-denied", "--commit", geCommit, "--route", "bash-view", "--run-id", rtRunID, "--response", e.response,
		"--audit-root", e.audit, "--target", rtTarget, "--control-path", rtControl, "--control-file", e.control,
		"--run-config", e.world.runConfig, "--template", e.world.files.template, "--host-profile", e.world.files.hostProfile,
		"--host-profile-signature", e.world.files.hostProfileSig, "--host-profile-key", e.world.files.hostProfileKey,
		"--registration", e.world.files.registration, "--registration-signature", e.world.files.registrationSig, "--out", out}, &stdout, &stderr)
	if err == nil {
		t.Fatal("a journal of hand-made receipts produced an artifact")
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("an artifact was written for a refused probe")
	}
	// direct-runtime without its inputs says what it needs.
	err = runGuestEvidence([]string{"route-denied", "--commit", geCommit, "--route", "direct-runtime", "--out", out}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "needs --run-config") {
		t.Fatalf("direct-runtime: %v", err)
	}
}

// The route-probe run has a config, registration, template and signed profile of its own: the artifact states
// them, and they are bound to what the run shares with the pilot, not to the pilot's profile.
func TestRouteProbeIsBoundToTheRunsOwnProfile(t *testing.T) {
	e := rtJournal(t, rtAllReceipts(t), nil)
	d, kind, err := probeRouteDenied(e.opts, rtEnvStub())
	if err != nil {
		t.Fatal(err)
	}
	if d.ProfileHash != e.world.profileDigest || d.Binding.ProfileDigest != e.world.profileDigest || d.Binding.TemplateDigest != d.Cell.TemplateDigest ||
		d.Binding.BaseTemplateDigest != e.world.pins.BaseTemplateDigest || d.Binding.ImageDigest != d.Cell.ImageDigest {
		t.Fatalf("artifact = %+v", d)
	}
	if err := guestevidence.Validate(kind, mustEncodeGE(t, kind, d), e.world.expect()); err != nil {
		t.Fatalf("refused: %v", err)
	}
	for name, mutate := range map[string]func(*routeProbeOptions){
		"another base cell": func(o *routeProbeOptions) { o.pins.BaseTemplateDigest = geDigest("another base") },
		"another source":    func(o *routeProbeOptions) { o.pins.FrozenSourceDigest = geDigest("another source") },
		"another image":     func(o *routeProbeOptions) { o.pins.ImageDigest = geDigest("another image") },
		"another admin key": func(o *routeProbeOptions) { o.pins.AdminPubkeyDigest = geDigest("another key") },
		"no host profile":   func(o *routeProbeOptions) { o.hostProfile = "" },
		"no template":       func(o *routeProbeOptions) { o.template = "" },
	} {
		o := e.opts
		mutate(&o)
		if _, _, err := probeRouteDenied(o, rtEnvStub()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The receipt's template is the run's own: a run in another template is not the template the profile names.
	other := rtAllReceipts(t)
	for i := range other {
		other[i].TemplateDigest = geDigest("another template")
	}
	oe := rtJournal(t, other, nil)
	od, okind, err := probeRouteDenied(oe.opts, rtEnvStub())
	if err == nil {
		if err = guestevidence.Validate(okind, mustEncodeGE(t, okind, od), oe.world.expect()); err == nil {
			t.Error("commands in a cell of another template than the run's profile names were accepted")
		}
	}
}
