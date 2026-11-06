package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/usdledger"
)

// realLedger writes a three-record ledger (genesis, reserve, settle) and returns its
// config object with the genesis checkpoint and the matching final state.
func realLedger(t *testing.T, e *env) (object map[string]any, path string, genesisHead, finalHead string) {
	t.Helper()
	path = e.path("usd-ledger")
	cfg := usdledger.Config{LedgerID: "research-test", CapMicroUSD: evalreport.RegisteredCapMicroUSD,
		MinPerUnitWorstCaseMicroUSD: 20000, AllowGenesis: true}
	l, err := usdledger.Open(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := l.Snapshot()
	genesisHead = snap.HeadSHA256
	if err := l.Reserve("unit-1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := l.Settle("unit-1", 400); err != nil {
		t.Fatal(err)
	}
	snap, _ = l.Snapshot()
	finalHead = snap.HeadSHA256
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	object = ledgerObject(func(o map[string]any) {
		o["path"], o["cap_micro_usd"] = path, evalreport.RegisteredCapMicroUSD
		o["checkpoint"] = map[string]any{"records": 1, "head_sha256": genesisHead}
		o["final"] = map[string]any{"records": 3, "head_sha256": finalHead}
	})
	return object, path, genesisHead, finalHead
}

func TestReportBindsTheVerifiedLedgerAndRefusesADisagreeingFinal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("usdledger.Verify needs Linux file locking")
	}
	e := newEnv(t)
	if _, err := e.cliCfg(fakeWire(newFake(e)), "run"); err != nil {
		t.Fatal(err)
	}
	object, path, _, finalHead := realLedger(t, e)
	e.cfg["ledger"] = object
	e.write()
	out, err := e.cliCfg(nil, "report", "--out", e.path("report"))
	if err != nil || line(out, "ledger") != "research-test" {
		t.Fatalf("report over a real ledger: %v\n%s", err, out)
	}
	report, _ := os.ReadFile(filepath.Join(e.path("report"), "report.json"))
	if !strings.Contains(string(report), finalHead) || !strings.Contains(string(report), `"settled_micro_usd":400`) {
		t.Fatalf("the report does not carry the ledger's final state:\n%s", report)
	}

	for name, tc := range map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"final records": {func(o map[string]any) {
			o["final"] = map[string]any{"records": 2, "head_sha256": finalHead}
		}, "ledger.final.records"},
		"final head": {func(o map[string]any) {
			o["final"] = map[string]any{"records": 3, "head_sha256": strings.Repeat("b", 64)}
		}, "ledger.final.head_sha256"},
		"checkpoint head": {func(o map[string]any) {
			o["checkpoint"] = map[string]any{"records": 1, "head_sha256": strings.Repeat("b", 64)}
		}, "ledger.checkpoint"},
		"cap": {func(o map[string]any) { o["cap_micro_usd"] = evalreport.RegisteredCapMicroUSD + 1 }, "ledger.checkpoint"},
	} {
		bad := ledgerObject(func(o map[string]any) {
			for k, v := range object {
				o[k] = v
			}
			tc.mutate(o)
		})
		e.cfg["ledger"] = bad
		e.write()
		if _, err := e.cliCfg(nil, "report", "--out", e.path("report-"+name)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, tc.want, err)
		}
	}

	// A ledger that grew past the recorded final state no longer matches it.
	l, err := usdledger.Open(path, usdledger.Config{LedgerID: "research-test", CapMicroUSD: evalreport.RegisteredCapMicroUSD,
		MinPerUnitWorstCaseMicroUSD: 20000})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Reserve("unit-2", 1000); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	e.cfg["ledger"] = object
	e.write()
	if _, err := e.cliCfg(nil, "report", "--out", e.path("report-grown")); err == nil || !strings.Contains(err.Error(), "ledger.final.records") {
		t.Fatalf("a grown ledger matched the old final state: %v", err)
	}
}
