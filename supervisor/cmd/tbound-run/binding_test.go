package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/routematrix"
	"tbound/supervisor/internal/runner"
)

// bindingMutations change one attestation or collector input of a run and nothing the plan
// digest covers.
func bindingMutations() map[string]func(e *env) {
	proc := func(e *env) map[string]any { return e.cfg["proc"].(map[string]any) }
	return map[string]func(e *env){
		"receipt profile pin": func(e *env) {
			e.cfg["pins"].(map[string]string)["profile_hash"] = fixtureDigest("other-profile")
		},
		"receipt containment pin": func(e *env) {
			e.cfg["pins"].(map[string]string)["containment_profile_digest"] = fixtureDigest("other-containment")
		},
		"receipt source pin": func(e *env) {
			e.cfg["pins"].(map[string]string)["frozen_source_digest"] = fixtureDigest("other-source")
		},
		"route report": func(e *env) {
			route, err := json.Marshal(routematrix.Report{Total: 5, Complete: true,
				ByStatus: map[routematrix.Status]int{routematrix.StatusCovered: 5}})
			if err != nil {
				e.t.Fatal(err)
			}
			e.file("route.json", string(route))
		},
		"evq results file": func(e *env) {
			e.setEVQ(`{"obligations":[{"id":"EVQ-1","passed":true,"evidence_ref":"synthetic-artifact-1"},` +
				`{"id":"EVQ-2","passed":false,"evidence_ref":"synthetic-artifact-2"}]}`)
		},
		"admission policy pin": func(e *env) {
			e.cfg["admission_policy"].(map[string]string)["policy_digest"] = fixtureDigest("other-store-policy")
		},
		"v image pin":          func(e *env) { e.cfg["v_image_digest"] = fixtureDigest("other-cell-image") },
		"proc cell uid":        func(e *env) { proc(e)["cell_uid"] = 996 },
		"proc cell gid":        func(e *env) { proc(e)["cell_gid"] = 987 },
		"proc monitor seconds": func(e *env) { proc(e)["monitor_seconds"] = 61 },
		"proc teardown grace":  func(e *env) { proc(e)["teardown_grace_seconds"] = 31 },
		"proc sockets":         func(e *env) { proc(e)["authorized_sockets"] = []string{"/run/other.sock"} },
	}
}

// A journal is bound to the attestation and collector inputs it was started under. Resume,
// rerun, export and report under other inputs are refused, because they would mix
// attestation criteria within one result set.
func TestJournalIsBoundToTheAttestationInputs(t *testing.T) {
	for name, mutate := range bindingMutations() {
		e := newEnv(t)
		before, err := e.cliCfg(nil, "plan")
		if err != nil {
			t.Fatal(err)
		}
		first := newFake(e)
		if _, err := e.cliCfg(fakeWire(first), "run", "--max-steps", "3"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		step := e.sched().Steps[0].ID()

		mutate(e)
		e.write()
		after, err := e.cliCfg(nil, "plan")
		if err != nil {
			t.Fatalf("%s: the mutated config must still be a valid plan: %v", name, err)
		}
		if line(before, "plan_digest") != line(after, "plan_digest") {
			t.Fatalf("%s: the registered plan digest must not move", name)
		}
		second := newFake(e)
		for _, args := range [][]string{
			{"run"}, {"rerun", "--step", step}, {"export", "--out", e.path("t.json")}, {"report", "--out", e.path("rep")},
		} {
			if _, err := e.cliCfg(fakeWire(second), args...); !errors.Is(err, runner.ErrPlanMismatch) {
				t.Fatalf("%s: %s under changed attestation inputs: %v", name, args[0], err)
			}
		}
		if len(second.Calls()) != 0 {
			t.Fatalf("%s: a refused resume reached the executor", name)
		}
		if line(before, "run_binding") == "" || line(before, "run_binding") == line(after, "run_binding") {
			t.Fatalf("%s: run_binding %q -> %q", name, line(before, "run_binding"), line(after, "run_binding"))
		}
	}
}

func TestJournalResumesUnderUnchangedAttestationInputs(t *testing.T) {
	e := newEnv(t)
	if _, err := e.cliCfg(fakeWire(newFake(e)), "run", "--max-steps", "3"); err != nil {
		t.Fatal(err)
	}
	// Rewriting the same settings does not change the binding.
	e.cfg["proc"].(map[string]any)["authorized_sockets"] = []string{}
	e.write()
	out, err := e.cliCfg(fakeWire(newFake(e)), "run")
	if err != nil || line(out, "finished") != "true" {
		t.Fatalf("resume: %v\n%s", err, out)
	}
	if _, err := e.cliCfg(nil, "export", "--out", e.path("t.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRunBindingIsOrderIndependentForSockets(t *testing.T) {
	e := newEnv(t)
	e.cfg["proc"].(map[string]any)["authorized_sockets"] = []string{"/run/a.sock", "/run/b.sock"}
	e.write()
	a, err := e.cliCfg(nil, "plan")
	if err != nil {
		t.Fatal(err)
	}
	e.cfg["proc"].(map[string]any)["authorized_sockets"] = []string{"/run/b.sock", "/run/a.sock"}
	e.write()
	b, err := e.cliCfg(nil, "plan")
	if err != nil || !strings.Contains(a, "run_binding sha256:") || line(a, "run_binding") != line(b, "run_binding") {
		t.Fatalf("socket order changed the binding:\n%s\n%s\n%v", a, b, err)
	}
}

// setEVQ rewrites the EVQ results file and re-pins it in run.json.
func (e *env) setEVQ(content string) {
	e.t.Helper()
	e.file("evq.json", content)
	e.cfg["evq_results"] = map[string]string{"path": e.path("evq.json"), "sha256": fixtureFileDigest(content)}
	e.write()
}

// The EVQ results file is pinned by digest: a changed file, a wrong pin, a missing section and
// a malformed document are all refused before anything runs.
func TestEVQResultsPinIsEnforced(t *testing.T) {
	for name, mutate := range map[string]func(e *env){
		"file changed after pinning": func(e *env) { e.file("evq.json", strings.Replace(evqAllPassed, `"passed":true`, `"passed":false`, 1)) },
		"wrong pin":                  func(e *env) { e.cfg["evq_results"].(map[string]string)["sha256"] = fixtureDigest("other") },
		"pin without prefix": func(e *env) {
			e.cfg["evq_results"].(map[string]string)["sha256"] = strings.TrimPrefix(fixtureFileDigest(evqAllPassed), "sha256:")
		},
		"relative path":   func(e *env) { e.cfg["evq_results"].(map[string]string)["path"] = "evq.json" },
		"missing section": func(e *env) { delete(e.cfg, "evq_results") },
		"missing file":    func(e *env) { e.cfg["evq_results"].(map[string]string)["path"] = e.path("absent.json") },
		"malformed":       func(e *env) { e.setEVQ(`{"obligations":[{"id":"EVQ-1","passed":true}]}`) },
		"unknown member":  func(e *env) { e.setEVQ(`{"obligations":[{"id":"EVQ-1","passed":false,"evidence_ref":"","x":1}]}`) },
	} {
		e := newEnv(t)
		mutate(e)
		e.write()
		if _, err := e.cliCfg(nil, "plan"); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

type evqProbe struct{ out runner.Outcome }

func (evqProbe) Prepare(context.Context, runner.Step) error { return nil }
func (p evqProbe) Execute(context.Context, runner.Step) (runner.Outcome, error) {
	return p.out, nil
}

// The wired attestation takes EVQ from the pinned file only: an obligation the file fails or
// does not name is not passed, whatever the receipt says, and the export says evq_passed false.
func TestAttestWiringUsesThePinnedEVQResults(t *testing.T) {
	e := newEnv(t)
	e.setEVQ(`{"obligations":[{"id":"EVQ-1","passed":true,"evidence_ref":"synthetic-artifact-1"}]}`)
	p, err := loadPlan(mustConfig(t, e))
	if err != nil {
		t.Fatal(err)
	}
	pins := p.cfg.Pins
	inner := evqProbe{runner.Outcome{E01: runner.E01Identity{Digest: "d"},
		Receipt: &runner.ReceiptFacts{ProfileHash: pins.ProfileHash, ContainmentProfileDigest: pins.ContainmentProfileDigest,
			FrozenSourceDigest: pins.FrozenSourceDigest, ProviderMode: "model"}}}
	var step runner.Step
	for _, st := range p.sched.Steps {
		if st.Configuration == runner.CfgC && st.Kind == evalcorpus.KindAdversarial && st.Role == runner.RoleTrajectory {
			step = st
			break
		}
	}
	out, err := p.attest(inner).Execute(context.Background(), step)
	if err != nil || !out.E01.Verified {
		t.Fatalf("attest: %+v %v", out, err)
	}
	want := []runner.Obligation{{ID: "EVQ-1", Passed: true, EvidenceRef: "synthetic-artifact-1"}, {ID: "EVQ-2", Passed: false}}
	if len(out.EVQ) != 2 || out.EVQ[0] != want[0] || out.EVQ[1] != want[1] {
		t.Fatalf("EVQ %+v, want %+v", out.EVQ, want)
	}
}

func TestPlanPDeltaIsTheManifestsRegisteredDelta(t *testing.T) {
	p := plan{manifest: evalcorpus.Manifest{Cases: []evalcorpus.Case{
		{CaseID: "plain", Trial: &evalcorpus.Trial{}},
		{CaseID: "relaxed", Trial: &evalcorpus.Trial{PDelta: evalcorpus.PDeltaAdmitAll}},
		{CaseID: "no-trial"},
	}}}
	for id, want := range map[string]string{"plain": "none", "relaxed": "admit-all/v1"} {
		if got, ok := p.pDelta(id); !ok || got != want {
			t.Errorf("%s: delta %q ok=%v, want %q", id, got, ok, want)
		}
	}
	for _, id := range []string{"no-trial", "unknown"} {
		if _, ok := p.pDelta(id); ok {
			t.Errorf("%s: a case without a trial has a delta", id)
		}
	}
}

func TestAdmissionPolicyPinsAreRequired(t *testing.T) {
	for name, mutate := range map[string]func(e *env){
		"missing section": func(e *env) { delete(e.cfg, "admission_policy") },
		"missing metadata": func(e *env) {
			delete(e.cfg["admission_policy"].(map[string]string), "metadata_policy_digest")
		},
		"malformed policy": func(e *env) { e.cfg["admission_policy"].(map[string]string)["policy_digest"] = "sha256:abc" },
	} {
		e := newEnv(t)
		mutate(e)
		e.write()
		if _, err := e.cliCfg(nil, "plan"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The wired attestation expects the script digest of the manifest and the admission policy digest of the
// configuration, and grants matched delivery only to a receipt that carries both.
func TestAttestWiringComparesTheScriptAndAdmissionPolicyDigests(t *testing.T) {
	script := []evalcorpus.ScriptTurn{{Tool: "write", Arguments: []byte(`{"content":"x","path":"note.txt"}`)}}
	p := plan{cfg: runConfig{Pins: runner.ReceiptPins{ProfileHash: "sha256:a", ContainmentProfileDigest: "sha256:b", FrozenSourceDigest: "sha256:c"},
		EVQObligations:  []string{"EVQ-1"},
		AdmissionPolicy: admissionPolicyPins{PolicyDigest: fixtureDigest("store-policy"), MetadataPolicyDigest: fixtureDigest("metadata-policy")}},
		manifest: evalcorpus.Manifest{Cases: []evalcorpus.Case{
			{CaseID: "relaxed", Trial: &evalcorpus.Trial{Script: script, PDelta: evalcorpus.PDeltaAdmitAll, ProtectedPaths: []string{"task.txt"}}},
			{CaseID: "plain", Trial: &evalcorpus.Trial{Script: script, ProtectedPaths: []string{"task.txt"}}},
			{CaseID: "unscripted", Trial: &evalcorpus.Trial{}},
		}}}
	wantScript, _ := evalcorpus.ScriptDigest(script)
	control, _ := evalcorpus.AdmissionPolicyDigest(fixtureDigest("store-policy"), fixtureDigest("metadata-policy"), false, []string{"task.txt"})
	relaxed, _ := evalcorpus.AdmissionPolicyDigest(fixtureDigest("store-policy"), fixtureDigest("metadata-policy"), true, nil)
	if control == relaxed {
		t.Fatal("the control package and promote-all share a digest")
	}
	for _, tc := range []struct {
		caseID string
		cfg    runner.Configuration
		want   string
	}{{"relaxed", runner.CfgC, control}, {"relaxed", runner.CfgP, relaxed}, {"plain", runner.CfgP, control}, {"plain", runner.CfgC, control}} {
		if got, ok := p.admissionPolicyDigest(tc.caseID, tc.cfg); !ok || got != tc.want {
			t.Errorf("%s/%s: %q ok=%v, want %q", tc.caseID, tc.cfg, got, ok, tc.want)
		}
	}
	if got, ok := p.scriptDigest("plain"); !ok || got != wantScript {
		t.Errorf("script digest %q ok=%v, want %q", got, ok, wantScript)
	}
	for _, id := range []string{"unscripted", "unknown"} {
		if _, ok := p.scriptDigest(id); ok {
			t.Errorf("%s has a script digest", id)
		}
		if _, ok := p.admissionPolicyDigest(id, runner.CfgC); id == "unknown" && ok {
			t.Errorf("%s has an admission policy digest", id)
		}
	}
	pins := p.cfg.Pins
	step := runner.Step{CaseID: "relaxed", Kind: evalcorpus.KindAdversarial, Configuration: runner.CfgP, Role: runner.RoleStaticReplay,
		Attempt: 1, Predicate: "EFF-WS-01"}
	receipt := func(mutate func(*runner.ReceiptFacts)) *runner.ReceiptFacts {
		r := &runner.ReceiptFacts{ProfileHash: pins.ProfileHash, ContainmentProfileDigest: pins.ContainmentProfileDigest,
			FrozenSourceDigest: pins.FrozenSourceDigest, ProviderMode: "scripted", ScriptDigest: wantScript,
			Delta: evalcorpus.PDeltaAdmitAll, AdmissionPolicyDigest: relaxed}
		if mutate != nil {
			mutate(r)
		}
		return r
	}
	p.evq = runner.EVQResults{{ID: "EVQ-1", Passed: true, EvidenceRef: "r"}}
	run := func(r *runner.ReceiptFacts) (runner.Outcome, error) {
		return p.attest(evqProbe{runner.Outcome{E01: runner.E01Identity{Digest: "d"}, Receipt: r}}).Execute(context.Background(), step)
	}
	if out, err := run(receipt(nil)); err != nil || !out.MatchedDelivery {
		t.Fatalf("the correct scripted receipt: %+v %v", out, err)
	}
	for name, mutate := range map[string]func(*runner.ReceiptFacts){
		"control digest in a P receipt": func(r *runner.ReceiptFacts) { r.AdmissionPolicyDigest = control },
		"another script":                func(r *runner.ReceiptFacts) { r.ScriptDigest = fixtureDigest("other") },
		"no provider mode":              func(r *runner.ReceiptFacts) { r.ProviderMode = "" },
	} {
		if out, err := run(receipt(mutate)); !errors.Is(err, runner.ErrReceiptPolicy) || out.MatchedDelivery || out.E01.Verified {
			t.Errorf("%s: %+v %v", name, out, err)
		}
	}
}
