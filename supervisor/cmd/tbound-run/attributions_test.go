package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/runner"
)

// attributionDoc is a valid attribution file holding the given entries.
func attributionDoc(mutate func(doc map[string]any), entries ...map[string]any) map[string]any {
	doc := map[string]any{"schema": attributionsSchema, "rubric_digest": fixtureDigest("rubric"), "entries": entries}
	if mutate != nil {
		mutate(doc)
	}
	return doc
}

func attributionOf(task, attribution string, evidence ...string) map[string]any {
	digests := []string{}
	for _, e := range evidence {
		digests = append(digests, fixtureDigest(e))
	}
	return map[string]any{"task_id": task, "attribution": attribution, "evidence_digests": digests, "note": "synthetic"}
}

// pinAttributions writes doc, pins it in run.json and returns the pin.
func (e *env) pinAttributions(doc any) pinnedFile {
	e.t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		e.t.Fatal(err)
	}
	pin := pinnedFile{Path: e.file("attributions.json", string(raw)), SHA256: fixtureFileDigest(string(raw))}
	e.cfg["attributions"] = pin
	e.write()
	return pin
}

func TestLoadAttributionsIsStrict(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  any
		want string
	}{
		"unknown top-level key":      {attributionDoc(func(d map[string]any) { d["reviewer"] = "x" }), "reviewer"},
		"unknown entry key":          {attributionDoc(nil, map[string]any{"task_id": "t", "attribution": "unresolved", "evidence_digests": []string{}, "note": "", "extra": 1}), "extra"},
		"bad schema":                 {attributionDoc(func(d map[string]any) { d["schema"] = "tbound-attributions/v2" }), "schema"},
		"bad rubric digest":          {attributionDoc(func(d map[string]any) { d["rubric_digest"] = "abc" }), "rubric_digest"},
		"bad enum":                   {attributionDoc(nil, attributionOf("t1", "maybe", "ev")), "maybe"},
		"not_applicable is no value": {attributionDoc(nil, attributionOf("t1", "not_applicable", "ev")), "not_applicable"},
		"supported needs evidence":   {attributionDoc(nil, attributionOf("t1", "supported")), "evidence"},
		"unsupported needs evidence": {attributionDoc(nil, attributionOf("t1", "unsupported")), "evidence"},
		"bad evidence digest": {attributionDoc(nil, map[string]any{"task_id": "t1", "attribution": "supported",
			"evidence_digests": []string{"nope"}, "note": ""}), "evidence digest"},
		"duplicate task": {attributionDoc(nil, attributionOf("t1", "supported", "ev"), attributionOf("t1", "unresolved")), "duplicate"},
		"no task id":     {attributionDoc(nil, attributionOf("", "unresolved")), "task_id"},
	} {
		e := newEnv(t)
		if _, err := loadAttributions(e.pinAttributions(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, tc.want, err)
		}
	}
	e := newEnv(t)
	pin := e.pinAttributions(attributionDoc(nil, attributionOf("t1", "supported", "ev1"), attributionOf("t2", "unresolved")))
	set, err := loadAttributions(pin)
	if err != nil || set.byTask["t1"] != evalscore.AttributionSupported || set.byTask["t2"] != evalscore.AttributionUnresolved ||
		set.rubricDigest != fixtureDigest("rubric") || set.digest != pin.SHA256 {
		t.Fatalf("good file: %+v, %v", set, err)
	}
	// Trailing data after the document is refused as well.
	raw, _ := os.ReadFile(pin.Path)
	trailing := string(raw) + "{}"
	if _, err := loadAttributions(pinnedFile{Path: e.file("trailing.json", trailing), SHA256: fixtureFileDigest(trailing)}); err == nil {
		t.Error("trailing data accepted")
	}
}

func TestConfigRefusesABadAttributionsPin(t *testing.T) {
	for name, tc := range map[string]struct {
		pin  func(path string) map[string]string
		want string
	}{
		"relative path": {func(string) map[string]string {
			return map[string]string{"path": "attributions.json", "sha256": fixtureDigest("x")}
		}, "attributions"},
		"no digest": {func(path string) map[string]string { return map[string]string{"path": path} }, "attributions"},
		"digest mismatch": {func(path string) map[string]string {
			return map[string]string{"path": path, "sha256": fixtureDigest("other")}
		}, "hashes to"},
	} {
		e := newEnv(t)
		pin := e.pinAttributions(attributionDoc(nil, attributionOf("t1", "unresolved")))
		e.cfg["attributions"] = tc.pin(pin.Path)
		e.write()
		if _, err := e.cliCfg(nil, "plan"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, tc.want, err)
		}
	}
}

// candidateRun runs the fixture to the end with a C failure on the first compliant task, which makes
// it a candidate obstruction, and returns that task and another one that is not.
func candidateRun(t *testing.T, e *env) (candidate, other string) {
	t.Helper()
	ids := e.manifest.CaseIDs(evalcorpus.KindCompliant)
	candidate, other = ids[0], ids[1]
	fake := newFake(e)
	fake.Hook = func(st runner.Step, o *runner.Outcome) error {
		if st.Role == runner.RoleCompliant && st.Configuration == runner.CfgC && st.CaseID == candidate {
			o.Task = runner.TaskFail
		}
		return nil
	}
	if _, err := e.cliCfg(fakeWire(fake), "run"); err != nil {
		t.Fatal(err)
	}
	return candidate, other
}

func exportedAttribution(t *testing.T, e *env, task string) evalscore.Attribution {
	t.Helper()
	trials := e.path("trials.json")
	if _, err := e.cliCfg(nil, "export", "--out", trials); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(trials)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	in, err := evalscore.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range in.Compliant {
		if r.TaskID == task {
			return r.Attribution
		}
	}
	t.Fatalf("task %s not exported", task)
	return ""
}

func TestExportTakesTheAttributionOfACandidate(t *testing.T) {
	e := newEnv(t)
	task, _ := candidateRun(t, e)
	if got := exportedAttribution(t, e, task); got != evalscore.AttributionUnresolved {
		t.Fatalf("without an attribution file the candidate is %q", got)
	}
	for _, want := range []evalscore.Attribution{evalscore.AttributionSupported, evalscore.AttributionUnsupported} {
		e.pinAttributions(attributionDoc(nil, attributionOf(task, string(want), "ev1")))
		if got := exportedAttribution(t, e, task); got != want {
			t.Fatalf("with a %s entry the candidate is %q", want, got)
		}
	}
}

func TestExportRefusesAnAttributionForANonCandidate(t *testing.T) {
	e := newEnv(t)
	task, other := candidateRun(t, e)
	for name, id := range map[string]string{"a task that passed in B and C": other, "an unknown task": "no-such-task"} {
		e.pinAttributions(attributionDoc(nil, attributionOf(task, "supported", "ev1"), attributionOf(id, "supported", "ev2")))
		if _, err := e.cliCfg(nil, "export", "--out", e.path("trials.json")); err == nil || !strings.Contains(err.Error(), id) {
			t.Errorf("%s: export accepted the entry or named another task: %v", name, err)
		}
		if _, err := e.cliCfg(nil, "report", "--out", e.path("report")); err == nil || !strings.Contains(err.Error(), id) {
			t.Errorf("%s: report accepted the entry or named another task: %v", name, err)
		}
	}
}

func TestReportCarriesTheAttributionDigests(t *testing.T) {
	e := newEnv(t)
	task, _ := candidateRun(t, e)
	digestsOf := func(dir string) (attributions, rubric string) {
		raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rep struct {
			Bindings struct {
				Attributions string `json:"attributions_digest"`
				Rubric       string `json:"attribution_rubric_digest"`
			} `json:"bindings"`
		}
		if err := json.Unmarshal(raw, &rep); err != nil {
			t.Fatal(err)
		}
		return rep.Bindings.Attributions, rep.Bindings.Rubric
	}
	if _, err := e.cliCfg(nil, "report", "--out", e.path("report-none")); err != nil {
		t.Fatal(err)
	}
	if a, r := digestsOf(e.path("report-none")); a != "" || r != "" {
		t.Fatalf("a report without an attribution file binds %q and %q", a, r)
	}
	pin := e.pinAttributions(attributionDoc(nil, attributionOf(task, "supported", "ev1")))
	if _, err := e.cliCfg(nil, "report", "--out", e.path("report-pinned")); err != nil {
		t.Fatal(err)
	}
	if a, r := digestsOf(e.path("report-pinned")); a != pin.SHA256 || r != fixtureDigest("rubric") {
		t.Fatalf("report binds %q and %q, want %q and %q", a, r, pin.SHA256, fixtureDigest("rubric"))
	}
}
