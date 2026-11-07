//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/researchprofile"
	"tbound/supervisor/internal/researchroute"
	"tbound/supervisor/internal/runner"
)

// preregistrationDomain labels the record digest. The pre-registration is a timestamped,
// digest-pinned repo freeze commit that the owner records; this record is the
// reviewable summary of what that freeze pins.
const preregistrationDomain = "tbound-preregistration/v1"

const maxRunnerConfigBytes = 1 << 20

var (
	freezeCommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	reviewerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
)

// preregistrationRecord is the pre-registration record. Every field except RecordDigest is
// bound by RecordDigest. An absent research profile or ledger configuration is recorded as
// the empty string, which is distinct from every digest.
type preregistrationRecord struct {
	Domain       string `json:"domain"`
	FreezeCommit string `json:"freeze_commit"`
	// CorpusManifestDigest is evalcorpus.Digest of the manifest, as pinned in the service config.
	CorpusManifestDigest string `json:"corpus_manifest_digest"`
	QuotaProfile         string `json:"quota_profile"`
	// RunnerPlanDigest is the digest of runner.Plan over the manifest, its seeded allocation and
	// the runner config. It binds the normalized config and every planned step.
	RunnerPlanDigest string `json:"runner_plan_digest"`
	// PlanSteps is the number of steps that plan holds. PlannedUnits is the registered number of
	// units (evalreport.PlannedUnits); under the thesis profile the two must agree.
	PlanSteps     int `json:"plan_steps"`
	PlannedUnits  int `json:"planned_units"`
	RerunHeadroom int `json:"rerun_headroom"`
	// EvalscoreSchema is evalscore.SchemaVersion.
	EvalscoreSchema            string `json:"evalscore_schema"`
	ResearchProfileDigest      string `json:"research_profile_digest"`
	ResearchLedgerConfigDigest string `json:"research_ledger_config_digest"`
	// Reviewers are names only, sorted.
	Reviewers    []string `json:"reviewers"`
	RecordDigest string   `json:"record_digest,omitempty"`
}

// computeDigest returns sha256:<hex> over the domain label, a NUL byte and the compact JSON
// of the record without its RecordDigest. The field order of the struct is the canonical
// order, and every string in it is a digest, a commit, a schema name or a validated name.
func (r preregistrationRecord) computeDigest() (string, error) {
	r.RecordDigest = ""
	body, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(preregistrationDomain+"\x00"), body...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func runPreregistrationRecord(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile preregistration-record", flag.ContinueOnError)
	flags.SetOutput(stderr)
	commit := flags.String("freeze-commit", "", "40 lowercase hex digit git commit id of the freeze commit")
	manifestPath := flags.String("corpus-manifest", "", "corpus manifest JSON")
	panelReceipt := flags.String("panel-receipt", "", "optional supplementary panel receipt.json")
	panelAudit := flags.String("panel-audit", "", "optional supplementary panel audit.json; supply all three panel files together")
	panelStats := flags.String("panel-stats", "", "optional supplementary panel diagnostics stats.json")
	profile := flags.String("quota-profile", "", "quota profile: thesis or pilot")
	runnerConfigPath := flags.String("runner-config", "", "runner Config JSON (its freeze_commit must equal --freeze-commit)")
	researchProfilePath := flags.String("research-profile", "", "optional frozen research profile JSON")
	ledgerConfigPath := flags.String("research-ledger-config", "", "optional research ledger configuration JSON")
	schema := flags.String("evalscore-schema", evalscore.SchemaVersion, "evalscore trials schema (only the registered one is accepted)")
	reviewers := flags.String("reviewers", "owner", "comma-separated administrative metadata names, without signatures")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	for name, value := range map[string]string{"--freeze-commit": *commit, "--corpus-manifest": *manifestPath,
		"--quota-profile": *profile, "--runner-config": *runnerConfigPath, "--reviewers": *reviewers} {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if !freezeCommitRE.MatchString(*commit) {
		return errors.New("--freeze-commit must be a 40 digit lowercase hex git commit id")
	}
	if *schema != evalscore.SchemaVersion {
		return fmt.Errorf("--evalscore-schema %q is not the registered %q", *schema, evalscore.SchemaVersion)
	}
	names, err := parseReviewers(*reviewers)
	if err != nil {
		return err
	}
	cfg, err := loadRunnerConfig(*runnerConfigPath)
	if err != nil {
		return err
	}
	if cfg.FreezeCommit != *commit {
		return fmt.Errorf("the runner config freeze_commit %q is not --freeze-commit %q", cfg.FreezeCommit, *commit)
	}
	if cfg.Purpose == runner.PurposeConstructWitness {
		return errors.New("the runner config is a construct-witness run, which is non-confirmatory and cannot be pre-registered")
	}

	manifest, manifestDigest, err := admitCorpus(*manifestPath, *profile, nil)
	if err != nil {
		return err
	}
	if err := checkPanel(manifest, *panelReceipt, *panelAudit, *panelStats, flags); err != nil {
		return err
	}
	quotas, err := quotasFor(*profile)
	if err != nil {
		return err
	}
	// The allocation is derived from the manifest's own split seed (manifest.SplitSeed), so no
	// separate seed input exists. A validated manifest already carries it, which is checked.
	allocation, err := evalcorpus.Allocate(manifest, quotas)
	if err != nil {
		return fmt.Errorf("allocation: %w", err)
	}
	if allocated, err := evalcorpus.Digest(allocation); err != nil || allocated != manifestDigest {
		return errors.New("the manifest is not its own seeded allocation")
	}
	schedule, err := runner.Plan(manifest, allocation, cfg)
	if err != nil {
		return fmt.Errorf("runner plan: %w", err)
	}
	if schedule.ManifestDigest != manifestDigest {
		return errors.New("the runner plan is bound to another manifest digest")
	}
	if *profile == "thesis" && (schedule.Subset || schedule.Units() != evalreport.PlannedUnits) {
		return fmt.Errorf("a thesis plan must be the registered %d units without a subset; it has %d (subset %t)",
			evalreport.PlannedUnits, schedule.Units(), schedule.Subset)
	}

	rec := preregistrationRecord{
		Domain: preregistrationDomain, FreezeCommit: *commit, CorpusManifestDigest: manifestDigest, QuotaProfile: *profile,
		RunnerPlanDigest: schedule.Digest, PlanSteps: schedule.Units(), PlannedUnits: evalreport.PlannedUnits,
		RerunHeadroom: schedule.Config.RerunHeadroom, EvalscoreSchema: evalscore.SchemaVersion, Reviewers: names,
	}
	if *researchProfilePath != "" {
		if rec.ResearchProfileDigest, err = researchProfileDigest(*researchProfilePath); err != nil {
			return err
		}
	}
	if *ledgerConfigPath != "" {
		if rec.ResearchLedgerConfigDigest, err = researchLedgerConfigDigest(*ledgerConfigPath); err != nil {
			return err
		}
	}
	if rec.RecordDigest, err = rec.computeDigest(); err != nil {
		return err
	}
	encoded, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", encoded)
	return err
}

// parseReviewers returns the sorted, validated reviewer names. They are names only: a short
// token of letters, digits, space, dot, underscore and hyphen.
func parseReviewers(list string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	for _, part := range strings.Split(list, ",") {
		name := strings.TrimSpace(part)
		if !reviewerNameRE.MatchString(name) {
			return nil, fmt.Errorf("--reviewers: %q is not a plain reviewer name", name)
		}
		if key := strings.ToLower(name); seen[key] {
			return nil, fmt.Errorf("--reviewers: %q is listed twice", name)
		} else {
			seen[key] = true
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// loadRunnerConfig strictly decodes a runner.Config: no duplicate or unknown member and no
// trailing data.
func loadRunnerConfig(path string) (runner.Config, error) {
	f, err := openRegularFile(path)
	if err != nil {
		return runner.Config{}, fmt.Errorf("runner config: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRunnerConfigBytes+1))
	if err != nil {
		return runner.Config{}, fmt.Errorf("runner config: %w", err)
	}
	if len(data) > maxRunnerConfigBytes {
		return runner.Config{}, errors.New("runner config exceeds the size limit")
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return runner.Config{}, fmt.Errorf("runner config: %w", err)
	}
	var cfg runner.Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return runner.Config{}, fmt.Errorf("runner config: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return runner.Config{}, errors.New("runner config: trailing data")
	}
	return cfg, nil
}

// researchProfileDigest returns the digest of a frozen research profile, as the research
// route computes it. An unfrozen profile is refused: it cannot be pre-registered.
func researchProfileDigest(path string) (string, error) {
	f, err := openRegularFile(path)
	if err != nil {
		return "", fmt.Errorf("research profile: %w", err)
	}
	defer f.Close()
	profile, err := researchprofile.Load(f)
	if err != nil {
		return "", fmt.Errorf("research profile: %w", err)
	}
	if reason := profile.FrozenReason(); reason != nil {
		return "", fmt.Errorf("research profile is not frozen: %w", reason)
	}
	digest, err := profile.Digest()
	if err != nil {
		return "", fmt.Errorf("research profile: %w", err)
	}
	return "sha256:" + digest, nil
}

func researchLedgerConfigDigest(path string) (string, error) {
	f, err := openRegularFile(path)
	if err != nil {
		return "", fmt.Errorf("research ledger config: %w", err)
	}
	defer f.Close()
	config, err := researchroute.LoadLedgerConfig(f)
	if err != nil {
		return "", err
	}
	digest, err := config.Digest()
	if err != nil {
		return "", err
	}
	return "sha256:" + digest, nil
}
