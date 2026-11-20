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
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/researchprofile"
	"tbound/supervisor/internal/researchroute"
	"tbound/supervisor/internal/runner"
)

const (
	maxRunnerConfigBytes = 1 << 20
	maxRubricBytes       = 16 << 20
	maxCorpusCheckBytes  = 64 << 20
)

var (
	freezeCommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	reviewerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
)

// runPreregistrationRecord writes the pre-registration record. The pre-registration is a timestamped, digest-pinned
// repo freeze commit that the owner records; the record (evalreport.Registration) is the reviewable summary of what
// that freeze pins. `tbound-run report` checks a run against it before it marks a report claim-bearing.
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
	runBinding := flags.String("run-binding", "", "the run_binding that `tbound-run plan` prints for the registered run.json (D49 drain grace, admission policy, receipt pins)")
	rubricPath := flags.String("attribution-rubric", "", "the frozen attribution rubric file; the run's attribution file must name its sha256 as rubric_digest")
	corpusCheckPath := flags.String("corpus-check", "", "the `tbound-profile corpus-check` report.json of this manifest, with no failed and no guest-only row")
	certificationPath := flags.String("certification", "", "the select.py certify output for the manifest's pinned main draw (required with a main draw, refused without)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	for name, value := range map[string]string{"--freeze-commit": *commit, "--corpus-manifest": *manifestPath,
		"--quota-profile": *profile, "--runner-config": *runnerConfigPath, "--reviewers": *reviewers,
		"--run-binding": *runBinding, "--attribution-rubric": *rubricPath, "--corpus-check": *corpusCheckPath} {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if !freezeCommitRE.MatchString(*commit) {
		return errors.New("--freeze-commit must be a 40 digit lowercase hex git commit id")
	}
	if !guestevidence.IsDigest(*runBinding) {
		return errors.New("--run-binding must be sha256:<64 hex>, as `tbound-run plan` prints it")
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
	// tbound-run plans from run.json with freeze_commit, evq_obligations, adaptive_caps, only_cases and purpose only
	// (runConfig.runnerConfig), so a record with any other runner field could match no run. C-DH is outside H1.
	if cfg.CDH != nil || cfg.NET02Mandatory || cfg.UnitCap != 0 || cfg.RerunHeadroom != 0 || cfg.NoRerunHeadroom {
		return errors.New("the runner config sets cdh, net02_mandatory, unit_cap, rerun_headroom or no_rerun_headroom, which no tbound-run plan sets")
	}

	manifest, manifestDigest, quotas, err := admitCorpus(*manifestPath, *profile, artifactsBeside(*manifestPath), nil)
	if err != nil {
		return err
	}
	if err := checkPanel(manifest, *panelReceipt, *panelAudit, *panelStats, flags); err != nil {
		return err
	}
	corpusCheck, err := corpusCheckDigest(*corpusCheckPath, manifest, manifestDigest)
	if err != nil {
		return err
	}
	certification, err := certificationDigest(*certificationPath, manifest, quotas, artifactsBeside(*manifestPath))
	if err != nil {
		return err
	}
	rubricData, err := readRegularBounded(*rubricPath, maxRubricBytes)
	if err != nil {
		return fmt.Errorf("attribution rubric: %w", err)
	}
	rubricSum := sha256.Sum256(rubricData)
	// The plan digest must be the one `tbound-run plan` computes: it takes the adaptive caps of a pinned attacker
	// artifact, so the record does too.
	if cfg.AdaptiveCaps, err = runner.AdaptiveCapsFor(manifest, artifactsBeside(*manifestPath), cfg.AdaptiveCaps); err != nil {
		return fmt.Errorf("corpus manifest refused: %w", err)
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
	// D53 item 4: the registered total is computed from the final counts, and a plan that is not exactly it is refused.
	planned := evalreport.PlannedUnits(allocation)
	if *profile == "thesis" && (schedule.Subset || schedule.Units() != planned) {
		return fmt.Errorf("a thesis plan must be the registered %d units without a subset; it has %d (subset %t)",
			planned, schedule.Units(), schedule.Subset)
	}

	rec := evalreport.Registration{
		Domain: evalreport.RegistrationDomain, FreezeCommit: *commit, CorpusManifestDigest: manifestDigest, QuotaProfile: *profile,
		RunnerPlanDigest: schedule.Digest, PlanSteps: schedule.Units(), PlannedUnits: planned,
		RerunHeadroom: schedule.Config.RerunHeadroom, EvalscoreSchema: evalscore.SchemaVersion,
		RunBinding: *runBinding, AttributionRubricDigest: "sha256:" + hex.EncodeToString(rubricSum[:]), CorpusCheckDigest: corpusCheck,
		CertificationDigest: certification, Reviewers: names,
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
	if rec.RecordDigest, err = rec.ComputeDigest(); err != nil {
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

// readRegularBounded reads a regular file of at most limit bytes.
func readRegularBounded(path string, limit int64) ([]byte, error) {
	f, err := openRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return data, nil
}

// certificationDigest checks the select.py certify output against the manifest's pinned main draw
// (evalreport.CheckCertification) and returns its digest. A manifest without a main draw has no certification.
func certificationDigest(path string, m evalcorpus.Manifest, q evalcorpus.Quotas, artifactsDir string) (string, error) {
	mainDraw := q.Draw != nil && !q.Draw.Pilot
	switch {
	case mainDraw && path == "":
		return "", errors.New("--certification is required: the manifest pins a main draw (D48 zero-event certification)")
	case !mainDraw && path != "":
		return "", errors.New("--certification needs a manifest that pins a main draw")
	case !mainDraw:
		return "", nil
	}
	name, _ := corpusadmit.DrawArtifactName(m.Pins.DrawDigest)
	draw, err := readRegularBounded(filepath.Join(artifactsDir, name), evalcorpus.MaxDrawBytes)
	if err != nil {
		return "", fmt.Errorf("certification: %w", err)
	}
	raw, err := readRegularBounded(path, maxRubricBytes)
	if err != nil {
		return "", fmt.Errorf("certification: %w", err)
	}
	c, err := evalreport.CheckCertification(draw, raw)
	if err != nil {
		return "", err
	}
	return c.Digest, nil
}

// corpusCheckDigest accepts a `tbound-profile corpus-check` report.json only for this manifest and only when it is
// clean: it passed, it has one case entry per manifest case, and every row of it, corpus-wide and per case, is pass
// or not-checked. A guest-only row is a guest witness still pending, so a report made without the imported guest
// evidence is refused. It returns the sha256 of the file.
func corpusCheckDigest(path string, m evalcorpus.Manifest, manifestDigest string) (string, error) {
	data, err := readRegularBounded(path, maxCorpusCheckBytes)
	if err != nil {
		return "", fmt.Errorf("corpus check: %w", err)
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return "", fmt.Errorf("corpus check: %w", err)
	}
	var r corpusadmit.ConstructReport
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return "", fmt.Errorf("corpus check: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("corpus check: trailing data")
	}
	if r.Schema != "tbound-construct-checks/v1" || r.ManifestDigest != manifestDigest {
		return "", fmt.Errorf("corpus check: schema %q for manifest %q is not a construct-check report of manifest %s", r.Schema, r.ManifestDigest, manifestDigest)
	}
	rows := slices.Clone(r.Checks)
	var ids []string
	for _, c := range r.Cases {
		rows = append(rows, c.Checks...)
		ids = append(ids, c.CaseID)
	}
	want := make([]string, 0, len(m.Cases))
	for _, c := range m.Cases {
		want = append(want, c.CaseID)
	}
	if slices.Sort(ids); !slices.Equal(ids, slices.Sorted(slices.Values(want))) {
		return "", fmt.Errorf("corpus check: its %d case entries are not the manifest's %d cases", len(ids), len(want))
	}
	var unclean []string
	for _, c := range rows {
		if c.Status != "pass" && c.Status != "not-checked" {
			unclean = append(unclean, c.Name+" "+c.Status)
		}
	}
	if !r.Passed || len(rows) == 0 || len(unclean) > 0 {
		return "", fmt.Errorf("corpus check: not clean (passed %t, %d rows, %d failed or guest-only: %v)", r.Passed, len(rows), len(unclean), unclean[:min(len(unclean), 5)])
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
