package main

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/runner"
)

// TestMain also serves as the spy ssh of TestWitnessRunReachesOnlyConfigurationP: a child started with TBOUND_SPY_SSH_LOG
// set appends its argument vector to that file and exits 0. The real SSH transports then carry the run, and everything
// they are asked to do is on the record.
func TestMain(m *testing.M) {
	if log := os.Getenv("TBOUND_SPY_SSH_LOG"); log != "" {
		os.Exit(spySSH(log))
	}
	os.Exit(m.Run())
}

func spySSH(log string) int {
	argv, err := json.Marshal(os.Args[1:])
	if err != nil {
		return 3
	}
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 3
	}
	defer f.Close()
	if _, err := f.Write(append(argv, '\n')); err != nil {
		return 3
	}
	return 0
}

// witnessEnv is the fixture run configured as a construct-witness run.
func witnessEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.cfg["purpose"] = string(runner.PurposeConstructWitness)
	e.write()
	return e
}

// thesisEnv is the fixture run over a thesis-sized corpus under the thesis quotas.
func thesisEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.manifest = fixtureManifestFor(t, "run-cli-thesis-seed", evalcorpus.ThesisQuotas())
	raw, err := evalcorpus.CanonicalJSON(e.manifest)
	if err != nil {
		t.Fatal(err)
	}
	e.file("corpus.json", string(raw))
	e.cfg["quota_profile"] = "thesis"
	e.write()
	return e
}

// The thesis plan is the registered 1,935 units, so a subset of it is refused. A construct-witness run of the same corpus
// is a subset by design and loads: one static P replay per adversarial case, and no other configuration.
func TestThesisWitnessPlanLoadsAndAThesisSubsetDoesNot(t *testing.T) {
	e := thesisEnv(t)
	out, err := e.cliCfg(nil, "plan")
	if err != nil || line(out, "units") != "1935" || line(out, "subset") != "false" {
		t.Fatalf("the ordinary thesis plan: %v\n%s", err, out)
	}
	id := e.manifest.CaseIDs(evalcorpus.KindAdversarial)[0]
	e.cfg["only_cases"] = []string{id}
	e.write()
	if _, err := e.cliCfg(nil, "plan"); err == nil || !strings.Contains(err.Error(), "registered") {
		t.Fatalf("an ordinary thesis subset was accepted: %v", err)
	}
	adv := strconv.Itoa(len(e.manifest.CaseIDs(evalcorpus.KindAdversarial)))
	e.cfg["purpose"] = string(runner.PurposeConstructWitness)
	e.cfg["only_cases"] = []string{}
	e.write()
	out, err = e.cliCfg(nil, "plan")
	if err != nil || line(out, "subset") != "true" || line(out, "units") != adv || line(out, "configuration P") != adv || line(out, "configuration C") != "" {
		t.Fatalf("the witness plan of the thesis corpus: %v\n%s", err, out)
	}
	e.cfg["only_cases"] = []string{id}
	e.write()
	if _, err := e.cliCfg(nil, "plan"); err == nil {
		t.Fatal("a witness plan with only_cases was accepted")
	}
}

// The witness run reaches the guest only as configuration P. The real SSH transports carry it, and the spy records every
// invocation: each is a trial reset, a P serve, or the evidence collection of a P interval, and none is a model, C, B or
// V invocation.
func TestWitnessRunReachesOnlyConfigurationP(t *testing.T) {
	e := witnessEnv(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e.cfg["ssh"].(map[string]any)["path"] = exe
	e.write()
	spy := e.path("spy.log")
	t.Setenv("TBOUND_SPY_SSH_LOG", spy)
	out, err := e.cliCfg(wireSSH, "run")
	if err != nil || line(out, "planned_units") != "11" || line(out, "finished") != "true" {
		t.Fatalf("witness run: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(spy)
	if err != nil {
		t.Fatalf("the spy saw no invocation: %v", err)
	}
	var resets, serves, evidence int
	for _, rec := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var argv []string
		if err := json.Unmarshal([]byte(rec), &argv); err != nil || len(argv) == 0 {
			t.Fatalf("spy record %q: %v", rec, err)
		}
		remote := argv[len(argv)-1]
		switch {
		case strings.Contains(remote, "'trial' 'reset'"):
			resets++
		case strings.Contains(remote, "'serve' '--pi'") && strings.Contains(remote, "'--role' 'p-static-replay'"):
			serves++
		case strings.Contains(remote, "'evidence' '"):
			evidence++
		default:
			t.Fatalf("a non-P invocation reached a transport: %s", remote)
		}
	}
	// Each P step resets its trial twice: a check in Prepare and the reset in Execute (ResetExecutor).
	if resets != 2*11 || serves != 11 || evidence == 0 {
		t.Fatalf("spy saw %d resets, %d P serves and %d evidence calls, want 22, 11 and some", resets, serves, evidence)
	}
}

// The spy only sees dispatched steps, and a witness plan dispatches only P. This checks what wireSSH builds.
func TestWitnessRunWiresOnlyConfigurationP(t *testing.T) {
	e := witnessEnv(t)
	p, err := loadPlan(mustConfig(t, e))
	if err != nil {
		t.Fatal(err)
	}
	if p.sched.Config.Purpose != runner.PurposeConstructWitness {
		t.Fatalf("the plan's purpose is %q", p.sched.Config.Purpose)
	}
	ex, err := wireSSH(p)
	if err != nil {
		t.Fatal(err)
	}
	if ex.P == nil || ex.A != nil || ex.B != nil || ex.C != nil || ex.V != nil || len(ex.CDH) != 0 {
		t.Fatalf("a witness run wires P=%t A=%t B=%t C=%t V=%t CDH=%d; want configuration P alone",
			ex.P != nil, ex.A != nil, ex.B != nil, ex.C != nil, ex.V != nil, len(ex.CDH))
	}
}

// The witness journal is not a study result: export and report refuse it, and the study plan cannot resume it.
func TestWitnessRunIsNotExportedOrResumedAsAStudy(t *testing.T) {
	e := witnessEnv(t)
	f := newFake(e)
	if _, err := e.cliCfg(fakeWire(f), "run"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls() {
		if c.Configuration != runner.CfgP || c.Role != runner.RoleStaticReplay {
			t.Fatalf("the witness run dispatched %s/%s", c.Configuration, c.Role)
		}
	}
	if _, err := e.cliCfg(nil, "export", "--out", e.path("trials.json")); !errors.Is(err, runner.ErrNonConfirmatoryPurpose) {
		t.Fatalf("export: %v", err)
	}
	if _, err := e.cliCfg(nil, "report", "--out", e.path("report")); !errors.Is(err, runner.ErrNonConfirmatoryPurpose) {
		t.Fatalf("report: %v", err)
	}
	delete(e.cfg, "purpose")
	e.write()
	if _, err := e.cliCfg(fakeWire(newFake(e)), "run"); !errors.Is(err, runner.ErrPlanMismatch) {
		t.Fatalf("the study plan resumed the witness journal: %v", err)
	}
}
