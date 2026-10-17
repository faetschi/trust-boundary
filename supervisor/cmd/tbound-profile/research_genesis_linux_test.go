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

	"tbound/supervisor/internal/researchroute"
	"tbound/supervisor/internal/usdledger"
)

const genesisProfileJSON = `{
  "schema_version": 1,
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "deepseek/deepseek-v4.1-flash",
  "routing": {"order": ["example-provider"], "allow_fallbacks": false, "data_collection": "deny",
              "quantizations": [], "require_parameters": true, "zdr": true},
  "input_micro_usd_per_1m_tokens": 150000,
  "output_micro_usd_per_1m_tokens": 600000,
  "max_price_micro_usd_per_1m_tokens_in": null,
  "max_price_micro_usd_per_1m_tokens_out": null,
  "max_input_tokens": 20000,
  "max_output_tokens": 2000,
  "max_requests_per_unit": 5,
  "temperature_milli": 0,
  "seed": 7,
  "reasoning": {"enabled": false},
  "timeout_ms": 5000,
  "frozen_at": "2026-10-17T00:00:00Z"
}`

const genesisLedgerConfigJSON = `{"schema_version":1,"ledger_id":"research-genesis-test","cap_micro_usd":5000000,"min_per_unit_worst_case_micro_usd":20000,"reserve_basis":"unit"}`

func genesisPaths(t *testing.T, profile string) researchroute.Paths {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := researchroute.Paths{
		Profile: filepath.Join(dir, "research-profile.json"), LedgerConfig: filepath.Join(dir, "research-ledger.json"),
		Ledger: filepath.Join(dir, "usd-ledger"), Key: filepath.Join(dir, "openrouter.key"), OwnerUID: uint32(os.Getuid()),
	}
	for path, content := range map[string]string{paths.Profile: profile, paths.LedgerConfig: genesisLedgerConfigJSON} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func TestResearchLedgerGenesisCreatesTheLedgerOnceAndPrintsTheAnchor(t *testing.T) {
	paths := genesisPaths(t, genesisProfileJSON)
	var stdout bytes.Buffer
	if err := researchLedgerGenesis(paths, &stdout); err != nil {
		t.Fatalf("genesis: %v", err)
	}
	var report struct {
		LedgerID      string `json:"ledger_id"`
		CapMicroUSD   int64  `json:"cap_micro_usd"`
		Records       uint64 `json:"records"`
		HeadSHA256    string `json:"head_sha256"`
		ProfileDigest string `json:"profile_digest"`
		LedgerPath    string `json:"ledger_path"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("genesis report is not JSON: %v: %s", err, stdout.String())
	}
	if report.LedgerID != "research-genesis-test" || report.CapMicroUSD != 5_000_000 || report.Records != 1 ||
		len(report.HeadSHA256) != 64 || len(report.ProfileDigest) != 64 || report.LedgerPath != paths.Ledger {
		t.Fatalf("genesis report differs: %+v", report)
	}
	info, err := os.Stat(paths.Ledger)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger file missing or not 0600: %v %v", info, err)
	}
	if err := researchLedgerGenesis(paths, &stdout); !errors.Is(err, researchroute.ErrLedgerExists) {
		t.Fatalf("second genesis: %v", err)
	}
	ledger, err := usdledger.Open(paths.Ledger, usdledger.Config{LedgerID: "research-genesis-test", CapMicroUSD: 5_000_000, MinPerUnitWorstCaseMicroUSD: 20_000})
	if err != nil {
		t.Fatalf("created ledger does not reopen: %v", err)
	}
	_ = ledger.Close()
}

func TestResearchLedgerGenesisRefusesAnUnfrozenProfileAndCreatesNothing(t *testing.T) {
	paths := genesisPaths(t, strings.Replace(genesisProfileJSON, `"seed": 7`, `"seed": null`, 1))
	if err := researchLedgerGenesis(paths, &bytes.Buffer{}); err == nil {
		t.Fatal("genesis ran for an unfrozen profile")
	}
	if _, err := os.Lstat(paths.Ledger); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unfrozen profile created a ledger: %v", err)
	}
}

func TestResearchLedgerGenesisSubcommandIsRegisteredAndTakesNoPositionalArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"research-ledger-genesis", "unexpected"}, &stdout, &stderr); err == nil {
		t.Fatal("positional argument accepted")
	}
	err := run([]string{"research-ledger-genesis", "--profile", "relative/profile.json"}, &stdout, &stderr)
	if err == nil || strings.Contains(err.Error(), "unknown tbound-profile subcommand") {
		t.Fatalf("subcommand unregistered or accepted a relative path: %v", err)
	}
	if !strings.Contains(usageText, "research-ledger-genesis") {
		t.Fatal("usage does not document the genesis subcommand")
	}
}
