package main

import (
	"encoding/json"
	"os"
	"testing"

	"tbound/supervisor/internal/runner"
)

func TestScreeningReportListsEveryTaskWithItsEnd(t *testing.T) {
	e := screeningEnv(t)
	e.cfg["only_cases"] = []string{"cmp-read-01", "cmp-bash-25"}
	e.write()
	if out, err := e.cliCfg(fakeWire(newFake(e)), "run"); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	path := e.path("screening-report.json")
	if out, err := e.cliCfg(nil, "screening-report", "--out", path); err != nil {
		t.Fatalf("screening-report: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc screeningReportDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != screeningReportSchema || len(doc.Rows) != 2 || doc.Ends[runner.ScreeningAdmitted] != 2 {
		t.Fatalf("report %+v", doc)
	}
	for _, r := range doc.Rows {
		if r.BaseID != r.ScreeningID || r.Tool == "" || r.End != runner.ScreeningAdmitted {
			t.Errorf("row %+v", r)
		}
	}
	if _, err := e.cliCfg(nil, "screening-report", "--out", path); err == nil {
		t.Error("screening-report overwrote an existing report")
	}
}

func TestScreeningReportRefusesAStudyRun(t *testing.T) {
	e := newEnv(t)
	e.write()
	if _, err := e.cliCfg(nil, "screening-report", "--out", e.path("r.json")); err == nil {
		t.Fatal("a study run.json gave a screening report")
	}
}

func TestScreeningSuffixIsStripped(t *testing.T) {
	for in, want := range map[string]string{"cmp-read-01.s1": "cmp-read-01", "cmp-read-01.s12": "cmp-read-01", "cmp-read-01": "cmp-read-01", "a.sx": "a.sx"} {
		if got := screeningSuffix.ReplaceAllString(in, ""); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
