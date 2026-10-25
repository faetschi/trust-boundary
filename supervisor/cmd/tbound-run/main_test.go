package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/routematrix"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/trialfix"
)

// evqAllPassed is a synthetic conformance results file that passes the two fixture obligations.
const evqAllPassed = `{"obligations":[{"id":"EVQ-1","passed":true,"evidence_ref":"commit=` + commit + `;synthetic-artifact-1"},` +
	`{"id":"EVQ-2","passed":true,"evidence_ref":"commit=` + commit + `;synthetic-artifact-2"}]}`

func fixtureFileDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// All fixtures here are synthetic. They exercise the CLI's code only.

const commit = "0123456789abcdef0123456789abcdef01234567"

func fixtureDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fixtureReviews() evalcorpus.Signoff {
	return evalcorpus.Signoff{Reviewer: "synthetic-advisor", Human: true, Decision: "accept"}
}

// fixtureManifest builds an allocated pilot manifest with a trial on every case.
func fixtureManifest(t *testing.T, seed string) evalcorpus.Manifest {
	t.Helper()
	q := evalcorpus.PilotQuotas()
	m := evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: seed,
		Pins: evalcorpus.Pins{EnvironmentDigest: fixtureDigest("env"), ModelDigest: fixtureDigest("model"),
			ProfileDigest: fixtureDigest("profile"), PolicyDigest: fixtureDigest("policy")}}
	for _, class := range []evalcorpus.ThreatClass{evalcorpus.ClassTMA, evalcorpus.ClassTMB, evalcorpus.ClassTMC} {
		for i := 1; i <= q.Adversarial[class]; i++ {
			id := fmt.Sprintf("adv-%s-%03d", class, i)
			tag := evalcorpus.OpportunitySingleStage
			if i%2 == 0 {
				tag = evalcorpus.OpportunityFeedback
			}
			m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindAdversarial, Signoff: fixtureReviews(),
				Adversarial: &evalcorpus.Adversarial{ThreatClass: class, RootScenarioID: "root-" + id, SourceKey: "src",
					SourceItemID: "item-" + id, SelectionReason: "synthetic",
					Adaptation: evalcorpus.Adaptation{PreservedIntent: "i", ToolMapping: "m", AdaptationSteps: []string{"s"},
						LostDistinctions: []string{}, AuthorizationClass: "a", EndpointOracleMapping: "e",
						LabelOrigin: evalcorpus.LabelExternalOracle},
					OracleID: "oracle", OracleVersion: "0", ForbiddenEffects: []string{"EFF-WS-01"},
					WorkspaceFixtureDigest: fixtureDigest("fixture/" + id), CalibrationPayloadDigest: fixtureDigest("cal/" + id),
					CalibrationWitnessID: "witness-" + id, OpportunityTag: tag, OpportunityPredicate: "predicate"}})
		}
	}
	for _, tool := range []evalcorpus.Tool{evalcorpus.ToolRead, evalcorpus.ToolWrite, evalcorpus.ToolEdit, evalcorpus.ToolBash} {
		for i := 1; i <= q.Compliant[tool]; i++ {
			id := fmt.Sprintf("cmp-%s-%02d", tool, i)
			m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindCompliant, Signoff: fixtureReviews(),
				Compliant: &evalcorpus.Compliant{PrimaryTool: tool, TaskGoal: "goal", ToolMapping: "m", OracleID: "task-oracle",
					OracleVersion: "0", WorkspaceFixtureDigest: fixtureDigest("cmp/" + id),
					AcceptanceTest:    evalcorpus.Ref{Ref: "acc-" + id, Digest: fixtureDigest("acc/" + id)},
					ReferenceSolution: evalcorpus.ReferenceSolution{Ref: "sol-" + id, Digest: fixtureDigest("sol/" + id), ForbiddenEffects: []string{}}}})
		}
	}
	for i := 1; i <= q.Ambiguous; i++ {
		m.Cases = append(m.Cases, evalcorpus.Case{CaseID: fmt.Sprintf("amb-%02d", i), Kind: evalcorpus.KindAmbiguous, Signoff: fixtureReviews(),
			Ambiguous: &evalcorpus.Ambiguous{WorkspaceFixtureDigest: fixtureDigest("amb/" + fmt.Sprint(i)),
				Rubric: evalcorpus.Ref{Ref: "rubric", Digest: fixtureDigest("rubric")}}})
	}
	allocated, err := evalcorpus.Allocate(m, q)
	if err != nil {
		t.Fatal(err)
	}
	for i := range allocated.Cases {
		id := allocated.Cases[i].CaseID
		allocated.Cases[i].Trial = &evalcorpus.Trial{Prompt: "Host-approved prompt of " + id, MaxHostCalls: 4, MaxTokens: 10000, DurationSeconds: 600}
	}
	allocated = sealConstructReview(t, allocated)
	if v := evalcorpus.Validate(allocated, q); len(v) != 0 {
		t.Fatalf("fixture manifest is invalid: %v", v)
	}
	return allocated
}

// sealConstructReview binds the construct review of m to its current content.
func sealConstructReview(t *testing.T, m evalcorpus.Manifest) evalcorpus.Manifest {
	t.Helper()
	content, err := evalcorpus.ContentDigest(m)
	if err != nil {
		t.Fatal(err)
	}
	m.ConstructReview = evalcorpus.ConstructReview{ContentDigest: content,
		PanelReceiptDigest: fixtureDigest("panel-receipt"), PanelAuditDigest: fixtureDigest("panel-audit")}
	return m
}

// env is one run directory: manifest, route report, dummy ssh files and run.json.
type env struct {
	t        *testing.T
	dir      string
	manifest evalcorpus.Manifest
	cfg      map[string]any
	cfgPath  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, dir: dir, manifest: fixtureManifest(t, "run-cli-seed"), cfgPath: filepath.Join(dir, "run.json")}
	raw, err := evalcorpus.CanonicalJSON(e.manifest)
	if err != nil {
		t.Fatal(err)
	}
	e.file("corpus.json", string(raw))
	route, err := json.Marshal(routematrix.Report{Total: 4, Complete: true, Binding: "commit=" + commit,
		ByStatus: map[routematrix.Status]int{routematrix.StatusCovered: 4}})
	if err != nil {
		t.Fatal(err)
	}
	e.file("route.json", string(route))
	for _, f := range []string{"ssh.exe", "known_hosts", "client.key", "evidence.key", "trial.key"} {
		e.file(f, "dummy, not a key") // only stat'ed, never read
	}
	e.file("evq.json", evqAllPassed)
	e.cfg = map[string]any{
		"manifest": e.path("corpus.json"), "quota_profile": "pilot", "freeze_commit": commit,
		"evq_obligations": []string{"EVQ-1", "EVQ-2"},
		"adaptive_caps":   calibration.Caps{Iterations: 8, ToolCalls: 50, WallMS: 60000, InTokens: 100000, OutTokens: 50000, MicroUSD: 10000},
		"only_cases":      []string{},
		"pins": map[string]string{"profile_hash": fixtureDigest("p"), "containment_profile_digest": fixtureDigest("c"),
			"frozen_source_digest": fixtureDigest("s")},
		"admission_policy": map[string]string{"policy_digest": fixtureDigest("store-policy"), "metadata_policy_digest": fixtureDigest("metadata-policy")},
		"v_image_digest":   fixtureDigest("cell-image"),
		"route_report":     e.path("route.json"), "ledger": "synthetic-empty", "run_dir": e.path("run"),
		"evq_results": map[string]string{"path": e.path("evq.json"), "sha256": fixtureFileDigest(evqAllPassed)},
		"ssh": map[string]any{
			"path": e.path("ssh.exe"), "known_hosts": e.path("known_hosts"), "host": "10.0.0.2", "host_key_alias": "tbound-guest",
			"client":   map[string]string{"user": "root", "key": e.path("client.key")},
			"evidence": map[string]string{"user": "root", "key": e.path("evidence.key")},
			"trial":    map[string]string{"user": "root", "key": e.path("trial.key")},
		},
		"proc": map[string]any{"cell_uid": 995, "cell_gid": 986, "monitor_seconds": 60, "teardown_grace_seconds": 30,
			"authorized_sockets": []string{}},
	}
	e.write()
	return e
}

func (e *env) path(name string) string { return filepath.Join(e.dir, name) }

func (e *env) file(name, content string) string {
	e.t.Helper()
	p := e.path(name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) write() {
	e.t.Helper()
	raw, err := json.Marshal(e.cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.file("run.json", string(raw))
}

// sched plans the fixture directly with the runner, for comparison with the CLI.
func (e *env) sched(only ...string) runner.Schedule {
	e.t.Helper()
	s, err := runner.Plan(e.manifest, e.manifest, runner.Config{FreezeCommit: commit, EVQObligations: []string{"EVQ-1", "EVQ-2"},
		AdaptiveCaps: calibration.Caps{Iterations: 8, ToolCalls: 50, WallMS: 60000, InTokens: 100000, OutTokens: 50000, MicroUSD: 10000},
		OnlyCases:    only})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// cli runs one subcommand in a fresh app, as a fresh process would.
func (e *env) cli(ctx context.Context, wire wireFunc, args ...string) (string, error) {
	var out, errOut bytes.Buffer
	a := &app{out: &out, errOut: &errOut, wire: wire, ctx: ctx}
	err := a.run(args)
	return out.String(), err
}

func (e *env) cliCfg(wire wireFunc, args ...string) (string, error) {
	return e.cli(context.Background(), wire, append([]string{args[0], "--config", e.cfgPath}, args[1:]...)...)
}

func fakeWire(f *runner.FakeExecutor) wireFunc {
	return func(plan) (runner.Executors, error) { return runner.Executors{B: f, C: f, P: f, V: f}, nil }
}

func newFake(e *env) *runner.FakeExecutor {
	return runner.NewFakeExecutor(e.sched().Config)
}

func line(out, key string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, key+" ") {
			return strings.TrimPrefix(l, key+" ")
		}
	}
	return ""
}

func TestPlanIsDeterministicThroughTheCLI(t *testing.T) {
	e := newEnv(t)
	first, err := e.cliCfg(nil, "plan")
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.cliCfg(nil, "plan")
	if err != nil || first != second {
		t.Fatalf("two plans differ:\n%s\n%s\n%v", first, second, err)
	}
	want := e.sched()
	if got := line(first, "plan_digest"); got != want.Digest {
		t.Fatalf("plan digest %q, runner.Plan %q", got, want.Digest)
	}
	if line(first, "units") != "123" || line(first, "subset") != "false" || line(first, "manifest_digest") != want.ManifestDigest {
		t.Fatalf("plan output:\n%s", first)
	}
	if line(first, "configuration C") == "" || line(first, "role trajectory") != "11" {
		t.Fatalf("plan counts:\n%s", first)
	}
	// A subset changes the digest and says so.
	id := e.manifest.CaseIDs(evalcorpus.KindAdversarial)[0]
	e.cfg["only_cases"] = []string{id}
	e.write()
	sub, err := e.cliCfg(nil, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if line(sub, "subset") != "true" || line(sub, "units") != "5" || line(sub, "plan_digest") != e.sched(id).Digest || line(sub, "plan_digest") == want.Digest {
		t.Fatalf("subset plan:\n%s", sub)
	}
}

func TestPlanRefusesAThesisPlanOverAPilotCorpus(t *testing.T) {
	e := newEnv(t)
	e.cfg["quota_profile"] = "thesis"
	e.write()
	if _, err := e.cliCfg(nil, "plan"); err == nil {
		t.Fatal("a pilot manifest was admitted under the thesis quotas")
	}
}

func TestConfigIsStrict(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"unknown member":         func(c map[string]any) { c["unit_cap_typo"] = 1 },
		"relax host keys":        func(c map[string]any) { c["ssh"].(map[string]any)["strict_host_key_checking"] = "no" },
		"relax host keys (flag)": func(c map[string]any) { c["strict_host_key_checking"] = false },
		"bad quota profile":      func(c map[string]any) { c["quota_profile"] = "full" },
		"bad ledger":             func(c map[string]any) { c["ledger"] = "usd-ledger.json" },
		"relative manifest":      func(c map[string]any) { c["manifest"] = "corpus.json" },
		"relative run dir":       func(c map[string]any) { c["run_dir"] = "run" },
		"no evq":                 func(c map[string]any) { c["evq_obligations"] = []string{} },
		"empty pin":              func(c map[string]any) { c["pins"].(map[string]string)["frozen_source_digest"] = "" },
		"shared key":             func(c map[string]any) { c["ssh"].(map[string]any)["trial"] = c["ssh"].(map[string]any)["client"] },
		"user with option": func(c map[string]any) {
			c["ssh"].(map[string]any)["client"] = map[string]string{"user": "-oProxyCommand=x", "key": c["ssh"].(map[string]any)["client"].(map[string]string)["key"]}
		},
		"known_hosts dev null":          func(c map[string]any) { c["ssh"].(map[string]any)["known_hosts"] = "/dev/null" },
		"no proc":                       func(c map[string]any) { delete(c, "proc") },
		"image pin not digest":          func(c map[string]any) { c["v_image_digest"] = "latest" },
		"image pin short":               func(c map[string]any) { c["v_image_digest"] = "sha256:abc" },
		"admin key pin not digest":      func(c map[string]any) { c["pins"].(map[string]string)["admin_pubkey_digest"] = "key" },
		"base template pin not digest":  func(c map[string]any) { c["pins"].(map[string]string)["base_template_digest"] = "key" },
		"runtime bundle pin not digest": func(c map[string]any) { c["pins"].(map[string]string)["runtime_bundle_digest"] = "key" },
		"runner pin not digest":         func(c map[string]any) { c["pins"].(map[string]string)["runner_digest"] = "key" },
	} {
		e := newEnv(t)
		mutate(e.cfg)
		e.write()
		if _, err := e.cliCfg(nil, "plan"); err == nil || !strings.Contains(err.Error(), "config") {
			t.Fatalf("%s: config accepted or wrong error: %v", name, err)
		}
	}
	// Duplicate keys and trailing data are refused as well.
	e := newEnv(t)
	good, _ := os.ReadFile(e.cfgPath)
	for name, raw := range map[string]string{
		"duplicate key": strings.Replace(string(good), `"ledger":"synthetic-empty"`, `"ledger":"synthetic-empty","ledger":"synthetic-empty"`, 1),
		"trailing data": string(good) + "{}",
		"empty":         "",
	} {
		e.file("run.json", raw)
		if _, err := e.cliCfg(nil, "plan"); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	e.file("run.json", string(good))
	if _, err := e.cliCfg(nil, "plan"); err != nil {
		t.Fatalf("the unmodified config must load: %v", err)
	}
}

// Configuration V is judged against the cell image it ran: a plan that contains V steps needs
// the pinned image digest in run.json, and a plan without V steps does not.
func TestPlanNeedsTheImagePinOnlyWhenVIsInThePlan(t *testing.T) {
	e := newEnv(t)
	delete(e.cfg, "v_image_digest")
	e.write()
	_, err := e.cliCfg(nil, "plan")
	if err == nil || !strings.Contains(err.Error(), "v_image_digest") {
		t.Fatalf("a plan with V steps and no image pin: %v", err)
	}
	// A subset of adversarial cases has no V step (V runs compliant tasks only).
	var adversarial []string
	for _, c := range e.manifest.Cases {
		if c.Kind == evalcorpus.KindAdversarial {
			adversarial = append(adversarial, c.CaseID)
		}
	}
	e.cfg["only_cases"] = adversarial[:1]
	e.write()
	if _, err := e.cliCfg(nil, "plan"); err != nil {
		t.Fatalf("a plan without V steps needs no image pin: %v", err)
	}
}

type crash struct{}

// A process that dies in the middle of a step leaves it begun and started. A fresh process
// closes it as abandoned (the unit stays spent) and runs only what follows.
func TestRunAndResumeAfterACrash(t *testing.T) {
	e := newEnv(t)
	total := e.sched().Units()
	first := newFake(e)
	first.Hook = func(s runner.Step, _ *runner.Outcome) error {
		if s.Index == 20 {
			panic(crash{})
		}
		return nil
	}
	func() {
		defer func() {
			if v := recover(); v == nil {
				t.Fatal("the simulated crash did not happen")
			} else if _, ok := v.(crash); !ok {
				panic(v)
			}
		}()
		_, _ = e.cliCfg(fakeWire(first), "run")
	}()
	if n := len(first.Calls()); n != 21 {
		t.Fatalf("first process executed %d steps", n)
	}

	second := newFake(e)
	out, err := e.cliCfg(fakeWire(second), "run")
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, out)
	}
	if line(out, "abandoned") != "1" || line(out, "finished") != "true" || line(out, "already_done") != "20" || line(out, "remaining") != "0" {
		t.Fatalf("resume report:\n%s", out)
	}
	calls := second.Calls()
	if len(calls) != total-21 || calls[0].Index != 21 {
		t.Fatalf("resumed %d steps, want %d from step 21", len(calls), total-21)
	}
	if line(out, "units_used") != fmt.Sprint(total) {
		t.Fatalf("units used %s of %d", line(out, "units_used"), total)
	}
	third := newFake(e)
	if out, err = e.cliCfg(fakeWire(third), "run"); err != nil || len(third.Calls()) != 0 || line(out, "finished") != "true" {
		t.Fatalf("third run: %v %d\n%s", err, len(third.Calls()), out)
	}
}

// An interrupt (Ctrl+C) abandons the step in flight in the same process; resuming does not
// run it again.
func TestRunInterruptedByTheContextResumes(t *testing.T) {
	e := newEnv(t)
	total := e.sched().Units()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := newFake(e)
	first.Hook = func(s runner.Step, _ *runner.Outcome) error {
		if s.Index == 12 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if out, err := e.cli(ctx, fakeWire(first), "run", "--config", e.cfgPath); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run: %v\n%s", err, out)
	}
	second := newFake(e)
	out, err := e.cliCfg(fakeWire(second), "run")
	if err != nil || line(out, "finished") != "true" || line(out, "already_done") != "13" {
		t.Fatalf("resume: %v\n%s", err, out)
	}
	if calls := second.Calls(); len(calls) != total-13 || calls[0].Index != 13 {
		t.Fatalf("resumed %d steps, want %d from step 13", len(calls), total-13)
	}
}

func TestRunMaxStepsStopsCleanlyAndResumes(t *testing.T) {
	e := newEnv(t)
	total := e.sched().Units()
	first := newFake(e)
	out, err := e.cliCfg(fakeWire(first), "run", "--max-steps", "7")
	if err != nil || len(first.Calls()) != 7 || !strings.Contains(out, "stopped at --max-steps 7") || line(out, "units_used") != "7" || line(out, "finished") != "false" {
		t.Fatalf("max-steps: %v %d\n%s", err, len(first.Calls()), out)
	}
	second := newFake(e)
	if out, err = e.cliCfg(fakeWire(second), "run"); err != nil || len(second.Calls()) != total-7 || second.Calls()[0].Index != 7 || line(out, "abandoned") != "0" {
		t.Fatalf("resume after max-steps: %v %d\n%s", err, len(second.Calls()), out)
	}
	if _, err := e.cliCfg(fakeWire(second), "run", "--max-steps", "-1"); !errors.Is(err, errUsage) {
		t.Fatalf("negative max-steps: %v", err)
	}
}

// withoutB is the real wiring with the B executor removed, to exercise the fail-closed path.
func withoutB(wire wireFunc) wireFunc {
	return func(p plan) (runner.Executors, error) {
		ex, err := wire(p)
		ex.B = nil
		return ex, err
	}
}

func TestRunWithTheRealWiringFailsClosedForUnwiredConfigurations(t *testing.T) {
	e := newEnv(t)
	out, err := e.cliCfg(withoutB(wireSSH), "run")
	if !errors.Is(err, runner.ErrNotWired) {
		t.Fatalf("run with B unwired: %v\n%s", err, out)
	}
	if line(out, "units_used") != "0" || line(out, "executed") != "0" {
		t.Fatalf("a refused wiring must spend nothing:\n%s", out)
	}
	ex, err := wireSSH(plan{cfg: mustConfig(t, e), sched: e.sched()})
	if err != nil || ex.C == nil || ex.P == nil || ex.B == nil || ex.V == nil || ex.A != nil {
		t.Fatalf("wiring: %+v %v", ex, err)
	}
	if _, ok := ex.C.(runner.Attest); !ok {
		t.Fatalf("C is %T, want the Attest decorator", ex.C)
	}
	if _, ok := ex.B.(runner.Attest); !ok {
		t.Fatalf("B is %T, want the Attest decorator", ex.B)
	}
}

func mustConfig(t *testing.T, e *env) runConfig {
	t.Helper()
	cfg, err := loadConfig(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSelfcheck(t *testing.T) {
	e := newEnv(t)
	out, err := e.cliCfg(withoutB(wireSSH), "selfcheck")
	if err == nil || !strings.Contains(out, "ok   configuration P wired") || !strings.Contains(out, "FAIL configuration B wired") ||
		!strings.Contains(out, "ok   configuration C wired") || !strings.Contains(out, "ok   collectors") {
		t.Fatalf("selfcheck with the real wiring: %v\n%s", err, out)
	}
	out, err = e.cliCfg(fakeWire(newFake(e)), "selfcheck")
	if err != nil || strings.Contains(out, "FAIL") {
		t.Fatalf("selfcheck with everything wired: %v\n%s", err, out)
	}
	// A missing key file or known_hosts fails the selfcheck.
	if err := os.Remove(e.path("known_hosts")); err != nil {
		t.Fatal(err)
	}
	if out, err = e.cliCfg(fakeWire(newFake(e)), "selfcheck"); err == nil || !strings.Contains(out, "FAIL known_hosts") {
		t.Fatalf("missing known_hosts: %v\n%s", err, out)
	}
}

func TestRerunIsRefusedWithoutOutageEvidenceAndAllowedWithIt(t *testing.T) {
	e := newEnv(t)
	s := e.sched()
	outage, clean := s.Steps[0], s.Steps[5] // different cases
	if outage.CaseID == clean.CaseID {
		t.Fatal("fixture: steps share a case")
	}
	if _, err := e.cliCfg(fakeWire(newFake(e)), "rerun", "--step", outage.ID()); err == nil || !strings.Contains(err.Error(), "no journal") {
		t.Fatalf("rerun before any run: %v", err)
	}
	f := newFake(e)
	f.Hook = func(st runner.Step, o *runner.Outcome) error {
		if st.Index == outage.Index && st.Attempt == 1 {
			o.EvidenceLoss, o.OutageEvidenceDigest = evalscore.LossExternalOutage, fixtureDigest("outage")
		}
		return nil
	}
	if _, err := e.cliCfg(fakeWire(f), "run"); err != nil {
		t.Fatal(err)
	}
	before := len(f.Calls())
	if _, err := e.cliCfg(fakeWire(f), "rerun", "--step", clean.ID()); !errors.Is(err, runner.ErrRerunNotAllowed) {
		t.Fatalf("rerun without outage evidence: %v", err)
	}
	if _, err := e.cliCfg(fakeWire(f), "rerun"); !errors.Is(err, errUsage) {
		t.Fatalf("rerun without --step: %v", err)
	}
	if len(f.Calls()) != before {
		t.Fatal("a refused rerun reached the executor")
	}
	out, err := e.cliCfg(fakeWire(f), "rerun", "--step", outage.ID())
	if err != nil || len(f.Calls()) != before+1 || f.Calls()[before].Attempt != 2 || !strings.Contains(out, "units_used 124") {
		t.Fatalf("rerun with outage evidence: %v\n%s", err, out)
	}
	if _, err := e.cliCfg(fakeWire(f), "rerun", "--step", outage.ID()); err == nil {
		t.Fatal("a second rerun of the case was accepted")
	}
}

func TestExportAndReport(t *testing.T) {
	e := newEnv(t)
	if _, err := e.cliCfg(nil, "export", "--out", e.path("t.json")); err == nil || !strings.Contains(err.Error(), "no journal") {
		t.Fatalf("export without a journal: %v", err)
	}
	if _, err := e.cliCfg(fakeWire(newFake(e)), "run", "--max-steps", "10"); err != nil {
		t.Fatal(err)
	}
	// A partial run still exports and reports, and the report says its coverage is not exact.
	rdir := e.path("partial-report")
	out, err := e.cliCfg(nil, "report", "--out", rdir)
	if err != nil || line(out, "claim_bearing") != "false" {
		t.Fatalf("partial report: %v\n%s", err, out)
	}
	report, _ := os.ReadFile(filepath.Join(rdir, "report.json"))
	if !strings.Contains(string(report), "coverage_not_exact") {
		t.Fatal("a partial run must flag its coverage")
	}

	if _, err := e.cliCfg(fakeWire(newFake(e)), "run"); err != nil {
		t.Fatal(err)
	}
	trials := e.path("trials.json")
	out, err = e.cliCfg(nil, "export", "--out", trials)
	if err != nil || line(out, "adversarial_records") != "11" || line(out, "compliant_records") != "20" || line(out, "incomplete_cases") != "0" {
		t.Fatalf("export: %v\n%s", err, out)
	}
	f, err := os.Open(trials)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	in, err := evalscore.Load(f)
	if err != nil || len(in.Adversarial) != 11 || len(in.Compliant) != 20 {
		t.Fatalf("exported trials do not load: %v", err)
	}

	rdir = e.path("report")
	out, err = e.cliCfg(nil, "report", "--out", rdir)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		ClaimBearing bool   `json:"claim_bearing"`
		Ledger       string `json:"ledger"`
		ReportDigest string `json:"report_digest"`
		PlanDigest   string `json:"plan_digest"`
	}
	raw, _ := os.ReadFile(filepath.Join(rdir, "report.meta.json"))
	if err := json.Unmarshal(raw, &meta); err != nil || meta.ClaimBearing || meta.Ledger != "synthetic-empty" ||
		meta.ReportDigest != line(out, "report_digest") || meta.PlanDigest != e.sched().Digest {
		t.Fatalf("meta %+v %v", meta, err)
	}
	md, _ := os.ReadFile(filepath.Join(rdir, "report.md"))
	if !strings.HasPrefix(string(md), "> NON-CLAIM-BEARING") {
		t.Fatalf("report.md does not lead with the non-claim notice:\n%.200s", md)
	}
	// The report is deterministic.
	rdir2 := e.path("report2")
	if _, err := e.cliCfg(nil, "report", "--out", rdir2); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(rdir, "report.json"))
	b, _ := os.ReadFile(filepath.Join(rdir2, "report.json"))
	if !bytes.Equal(a, b) {
		t.Fatal("two reports of one journal differ")
	}
}

func TestFixtureDigest(t *testing.T) {
	e := newEnv(t)
	spec := `{"schema":"tbound-fixture/v1","entries":[{"path":"src","type":"dir","mode":"0755"},` +
		`{"path":"src/a.txt","type":"file","mode":"0644","content_b64":"aGk="}]}`
	p := e.file("spec.json", spec)
	parsed, err := trialfix.Parse(strings.NewReader(spec))
	if err != nil {
		t.Fatal(err)
	}
	want, err := trialfix.Digest(parsed)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.cli(context.Background(), nil, "fixture", "digest", p)
	if err != nil || out != want+"\n" {
		t.Fatalf("fixture digest %q %v, want %q", out, err, want)
	}
	e.file("bad.json", `{"schema":"tbound-fixture/v1","entries":[{"path":"../x","type":"file","mode":"0644"}]}`)
	if _, err := e.cli(context.Background(), nil, "fixture", "digest", e.path("bad.json")); err == nil {
		t.Fatal("an invalid spec was digested")
	}
	if _, err := e.cli(context.Background(), nil, "fixture"); !errors.Is(err, errUsage) {
		t.Fatalf("fixture without arguments: %v", err)
	}
}

func TestUsage(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{{}, {"nope"}, {"plan"}, {"plan", "--config", e.cfgPath, "extra"}} {
		if _, err := e.cli(context.Background(), nil, args...); !errors.Is(err, errUsage) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

// The binary must build and run on the Windows host. This checks the part of that a Linux
// test can: neither the command nor any package of this module it imports is
// excluded on windows (a build tag or a _linux file). The gate also runs
// GOOS=windows go vet and go build.
func TestCommandAndItsImportsAreNotLinuxOnly(t *testing.T) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = "windows", "amd64", false
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		if seen[dir] {
			return
		}
		seen[dir] = true
		pkg, err := ctx.ImportDir(dir, 0)
		if err != nil {
			t.Errorf("%s does not build for windows: %v", dir, err)
			return
		}
		if len(pkg.IgnoredGoFiles) != 0 && dir == "." {
			t.Errorf("windows ignores %v in the command", pkg.IgnoredGoFiles)
		}
		for _, imp := range pkg.Imports {
			if rest, ok := strings.CutPrefix(imp, "tbound/supervisor/"); ok {
				walk(filepath.Join(root, filepath.FromSlash(rest)))
			}
		}
	}
	walk(".")
	if len(seen) < 5 {
		t.Fatalf("walked only %d packages", len(seen))
	}
}
