package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ledgerObject(mutate func(map[string]any)) map[string]any {
	head := strings.Repeat("a", 64)
	o := map[string]any{
		"path": filepath.Join(os.TempDir(), "usd-ledger"), "ledger_id": "research-test", "cap_micro_usd": 5000000,
		"min_per_unit_worst_case_micro_usd": 20000,
		"checkpoint":                        map[string]any{"records": 1, "head_sha256": head},
		"final":                             map[string]any{"records": 3, "head_sha256": head},
	}
	if mutate != nil {
		mutate(o)
	}
	return o
}

func TestConfigAcceptsTheLedgerObjectForm(t *testing.T) {
	e := newEnv(t)
	e.cfg["ledger"] = ledgerObject(nil)
	e.write()
	if _, err := e.cliCfg(nil, "plan"); err != nil {
		t.Fatalf("ledger object refused: %v", err)
	}
}

func TestConfigRefusesABadLedgerObject(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"relative path":      {func(o map[string]any) { o["path"] = "ledger.jsonl" }, "ledger.path"},
		"missing final":      {func(o map[string]any) { delete(o, "final") }, "ledger.final"},
		"missing checkpoint": {func(o map[string]any) { delete(o, "checkpoint") }, "ledger.checkpoint"},
		"final without head": {func(o map[string]any) { o["final"] = map[string]any{"records": 3} }, "ledger.final"},
		"final head not hex": {func(o map[string]any) { o["final"] = map[string]any{"records": 3, "head_sha256": "abc"} }, "ledger.final"},
		"final below checkpoint": {func(o map[string]any) {
			o["checkpoint"] = map[string]any{"records": 5, "head_sha256": strings.Repeat("a", 64)}
		}, "ledger.final.records"},
		"no ledger id":   {func(o map[string]any) { o["ledger_id"] = "" }, "ledger_id"},
		"unknown member": {func(o map[string]any) { o["extra"] = 1 }, "ledger"},
	} {
		e := newEnv(t)
		e.cfg["ledger"] = ledgerObject(tc.mutate)
		e.write()
		if _, err := e.cliCfg(nil, "plan"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, tc.want, err)
		}
	}
	e := newEnv(t)
	delete(e.cfg, "ledger")
	e.write()
	if _, err := e.cliCfg(nil, "plan"); err == nil {
		t.Error("a config without a ledger member was accepted")
	}
}
