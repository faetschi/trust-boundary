package main

import (
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/runner"
)

func screeningEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.manifest = fixtureManifestFor(t, "run-cli-screening-seed", evalcorpus.Quotas{
		Adversarial: map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 0, evalcorpus.ClassTMB: 0, evalcorpus.ClassTMC: 0},
		Compliant:   map[evalcorpus.Tool]int{evalcorpus.ToolRead: 25, evalcorpus.ToolWrite: 25, evalcorpus.ToolEdit: 25, evalcorpus.ToolBash: 25},
		Variance:    map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 0, evalcorpus.ClassTMB: 0, evalcorpus.ClassTMC: 0},
	})
	raw, err := evalcorpus.CanonicalJSON(e.manifest)
	if err != nil {
		t.Fatal(err)
	}
	e.file("corpus.json", string(raw))
	e.cfg["quota_profile"], e.cfg["purpose"] = quotaScreening, string(runner.PurposeScreening)
	e.write()
	return e
}

func TestScreeningPlanIsOneBStepPerCompliantTask(t *testing.T) {
	e := screeningEnv(t)
	out, err := e.cliCfg(nil, "plan")
	if err != nil || line(out, "units") != "100" || line(out, "subset") != "true" || line(out, "configuration B") != "100" ||
		line(out, "configuration C") != "" || line(out, "role compliant") != "100" {
		t.Fatalf("screening plan: %v\n%s", err, out)
	}
	e.cfg["only_cases"] = []string{"cmp-read-01", "cmp-bash-25"}
	e.write()
	out, err = e.cliCfg(nil, "plan")
	if err != nil || line(out, "units") != "2" || line(out, "configuration B") != "2" {
		t.Fatalf("screening subset plan: %v\n%s", err, out)
	}
}

func TestScreeningRefusesTheWrongProfilePurposePairs(t *testing.T) {
	for name, change := range map[string]func(*env){
		"screening purpose, pilot profile":     func(e *env) { e.cfg["quota_profile"] = "pilot" },
		"screening purpose, thesis profile":    func(e *env) { e.cfg["quota_profile"] = "thesis" },
		"screening profile, study plan":        func(e *env) { delete(e.cfg, "purpose") },
		"screening profile, construct-witness": func(e *env) { e.cfg["purpose"] = string(runner.PurposeConstructWitness) },
	} {
		e := screeningEnv(t)
		change(e)
		e.write()
		if _, err := e.cliCfg(nil, "plan"); err == nil || !strings.Contains(err.Error(), "go together") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestScreeningRefusesAManifestWithOtherCases(t *testing.T) {
	e := newEnv(t)
	e.cfg["quota_profile"], e.cfg["purpose"] = quotaScreening, string(runner.PurposeScreening)
	e.write()
	if _, err := e.cliCfg(nil, "plan"); err == nil {
		t.Fatal("a manifest with adversarial and ambiguous cases was planned for screening")
	}
}

func TestScreeningRunDispatchesOnlyBAndHasNoExportOrReport(t *testing.T) {
	e := screeningEnv(t)
	p, err := loadPlan(mustConfig(t, e))
	if err != nil {
		t.Fatal(err)
	}
	f := runner.NewFakeExecutor(p.sched.Config)
	if _, err := e.cliCfg(fakeWire(f), "run"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls()) != 100 {
		t.Fatalf("%d dispatched steps, want 100", len(f.Calls()))
	}
	for _, c := range f.Calls() {
		if c.Configuration != runner.CfgB || c.Role != runner.RoleCompliant {
			t.Fatalf("the screening run dispatched %s/%s", c.Configuration, c.Role)
		}
	}
	if _, err := e.cliCfg(nil, "export", "--out", e.path("trials.json")); !errors.Is(err, runner.ErrNonConfirmatoryPurpose) {
		t.Fatalf("export: %v", err)
	}
	if _, err := e.cliCfg(nil, "report", "--out", e.path("report")); !errors.Is(err, runner.ErrNonConfirmatoryPurpose) {
		t.Fatalf("report: %v", err)
	}
}
