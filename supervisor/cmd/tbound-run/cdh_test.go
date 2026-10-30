package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runner"
)

func TestCDHCLIPlanExportReport(t *testing.T) {
	e := newEnv(t)
	f := newFake(e)
	if _, err := e.cliCfg(fakeWire(f), "run"); err != nil {
		t.Fatal(err)
	}
	baseline := e.path("baseline.json")
	if _, err := e.cliCfg(fakeWire(f), "export", "--out", baseline); err != nil {
		t.Fatal(err)
	}
	// Export was created here from synthetic outcomes; no provider or SSH was used.
	data, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatal(err)
	}
	m, err := providerbridge.NewCDHLocalManifest("synthetic/local", "http://127.0.0.1:8080/v1", "sha256:"+strings.Repeat("a", 64), .8, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	e.cfg["cdh"] = &runner.CDHConfig{Manifest: m, LiveAdversarialCases: []string{}}
	e.cfg["cdh_baseline"] = pinnedFile{Path: baseline, SHA256: fixtureFileDigest(string(data))}
	e.cfg["run_dir"] = e.path("cdh-run")
	e.write()
	wire := func(p plan) (runner.Executors, error) {
		f := runner.NewFakeExecutor(p.sched.Config)
		digest, _ := m.Digest()
		f.Hook = func(st runner.Step, o *runner.Outcome) error {
			o.Receipt = &runner.ReceiptFacts{HintChooser: strings.TrimPrefix(string(st.Configuration), "C-DH/"), HintManifestDigest: digest}
			return nil
		}
		x := runner.Executors{CDH: map[runner.Configuration]runner.Executor{}}
		for _, model := range m.Models {
			x.CDH[runner.CDHConfiguration(model)] = f
		}
		return x, nil
	}
	if out, err := e.cliCfg(wire, "plan"); err != nil || !strings.Contains(out, "C-DH/synthetic/local") {
		t.Fatalf("plan %s %v", out, err)
	}
	if _, err := e.cliCfg(wire, "run"); err != nil {
		t.Fatal(err)
	}
	out := e.path("cdh-trials.json")
	if _, err := e.cliCfg(wire, "export", "--out", out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(doc["cdh"], []byte("C-DH/synthetic/local")) {
		t.Fatal("missing separate chooser")
	}
	if _, err := e.cliCfg(wire, "report", "--out", e.path("cdh-report")); err != nil {
		t.Fatal(err)
	}
	e.cfg["cdh_baseline"] = pinnedFile{Path: baseline, SHA256: "sha256:" + strings.Repeat("b", 64)}
	e.write()
	if _, err := e.cliCfg(wire, "export", "--out", e.path("bad.json")); err == nil {
		t.Fatal("changed baseline accepted")
	}
}
