//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"tbound/supervisor/internal/researchroute"
)

// runResearchLedgerGenesis is the one-time, explicit creation of the research
// USD ledger. The service never creates the ledger itself, so a deleted ledger
// cannot reset the budget.
func runResearchLedgerGenesis(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile research-ledger-genesis", flag.ContinueOnError)
	flags.SetOutput(stderr)
	paths := researchroute.DefaultPaths()
	flags.StringVar(&paths.Profile, "profile", paths.Profile, "frozen research profile JSON (root-owned)")
	flags.StringVar(&paths.LedgerConfig, "ledger-config", paths.LedgerConfig, "research ledger configuration JSON (root-owned)")
	flags.StringVar(&paths.Ledger, "ledger", paths.Ledger, "ledger file to create")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	for name, path := range map[string]string{"--profile": paths.Profile, "--ledger-config": paths.LedgerConfig, "--ledger": paths.Ledger} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%s must be a canonical absolute path", name)
		}
	}
	return researchLedgerGenesis(paths, stdout)
}

// researchLedgerGenesis creates the ledger at paths.Ledger and reports its
// genesis anchor as one JSON line.
func researchLedgerGenesis(paths researchroute.Paths, stdout io.Writer) error {
	snapshot, err := researchroute.Genesis(paths)
	if err != nil {
		return err
	}
	profile, err := researchroute.LoadProfileDigest(paths)
	if err != nil {
		return err
	}
	report, err := json.Marshal(struct {
		LedgerID      string `json:"ledger_id"`
		CapMicroUSD   int64  `json:"cap_micro_usd"`
		Records       uint64 `json:"records"`
		HeadSHA256    string `json:"head_sha256"`
		ProfileDigest string `json:"profile_digest"`
		LedgerPath    string `json:"ledger_path"`
	}{snapshot.LedgerID, snapshot.CapMicroUSD, snapshot.Records, snapshot.HeadSHA256, profile, paths.Ledger})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", report)
	return err
}
