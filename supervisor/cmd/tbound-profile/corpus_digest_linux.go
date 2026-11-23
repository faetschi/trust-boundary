//go:build linux

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/panelaudit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/taskaccept"
)

// maxReportedViolations bounds the violations an error message lists.
const maxReportedViolations = 5

// checkProfile refuses a corpus quota profile the service config does not know
// (piruntime.D09ManagedPiCorpusQuotaProfile*).
func checkProfile(profile string) error {
	switch profile {
	case piruntime.D09ManagedPiCorpusQuotaProfileThesis, piruntime.D09ManagedPiCorpusQuotaProfilePilot,
		piruntime.D09ManagedPiCorpusQuotaProfileScreening:
		return nil
	}
	return fmt.Errorf("unknown quota profile %q: use %q, %q or %q", profile, piruntime.D09ManagedPiCorpusQuotaProfileThesis,
		piruntime.D09ManagedPiCorpusQuotaProfilePilot, piruntime.D09ManagedPiCorpusQuotaProfileScreening)
}

// quotasFor returns the quotas of a profile for m, from the D48 draw it pins in the artifact
// directory (corpusadmit.QuotasFor), as the corpus-mode service resolves them.
func quotasFor(profile string, m evalcorpus.Manifest, artifactsDir string) (evalcorpus.Quotas, error) {
	if err := checkProfile(profile); err != nil {
		return evalcorpus.Quotas{}, err
	}
	return corpusadmit.QuotasFor(profile, m, os.DirFS(artifactsDir))
}

// artifactsBeside is the corpus layout's artifact directory: artifacts/ beside the manifest.
func artifactsBeside(manifestPath string) string {
	return filepath.Join(filepath.Dir(manifestPath), "artifacts")
}

// openRegularFile opens path for reading and refuses anything but a regular file.
func openRegularFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return f, nil
}

// admitCorpus applies the admission the corpus-mode service applies to its pinned manifest,
// in the same order and with the same code: the strict loader, the digest,
// evalcorpus.Validate under the quotas of the profile, and corpusadmit.CheckTrials on every
// case. It returns the manifest and the digest the service config must pin as
// corpus_manifest_digest, and the quotas it was validated under. A nil ceilings skips only the
// comparison with the host ceilings, which are a property of the service config. The D48
// draw the manifest pins is read from artifactsDir.
func admitCorpus(path, profile, artifactsDir string, ceilings *corpusadmit.Ceilings) (evalcorpus.Manifest, string, evalcorpus.Quotas, error) {
	if err := checkProfile(profile); err != nil {
		return evalcorpus.Manifest{}, "", evalcorpus.Quotas{}, err
	}
	f, err := openRegularFile(path)
	if err != nil {
		return evalcorpus.Manifest{}, "", evalcorpus.Quotas{}, fmt.Errorf("corpus manifest: %w", err)
	}
	defer f.Close()
	manifest, err := evalcorpus.Load(f)
	if err != nil {
		return evalcorpus.Manifest{}, "", evalcorpus.Quotas{}, fmt.Errorf("corpus manifest refused: %w", err)
	}
	digest, err := evalcorpus.Digest(manifest)
	if err != nil {
		return evalcorpus.Manifest{}, "", evalcorpus.Quotas{}, fmt.Errorf("corpus manifest refused: %w", err)
	}
	quotas, err := quotasFor(profile, manifest, artifactsDir)
	if err != nil {
		return evalcorpus.Manifest{}, "", evalcorpus.Quotas{}, fmt.Errorf("corpus manifest refused: %w", err)
	}
	if violations := evalcorpus.Validate(manifest, quotas); len(violations) != 0 {
		var listed []string
		for i, v := range violations {
			if i == maxReportedViolations {
				listed = append(listed, fmt.Sprintf("... and %d more", len(violations)-i))
				break
			}
			listed = append(listed, v.Rule+": "+v.Detail)
		}
		return evalcorpus.Manifest{}, "", evalcorpus.Quotas{}, fmt.Errorf("corpus manifest refused: evalcorpus.Validate under the %s quotas: %d violation(s): %s",
			profile, len(violations), strings.Join(listed, "; "))
	}
	if err := corpusadmit.CheckTrials(manifest.Cases, ceilings); err != nil {
		return evalcorpus.Manifest{}, "", evalcorpus.Quotas{}, fmt.Errorf("corpus manifest refused: %w", err)
	}
	return manifest, digest, quotas, nil
}

// readPanelFile reads one panel file, a regular file of at most panelaudit.MaxFileBytes.
func readPanelFile(path string) ([]byte, error) {
	f, err := openRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, panelaudit.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > panelaudit.MaxFileBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, panelaudit.MaxFileBytes)
	}
	return raw, nil
}

// checkPanel verifies an optional supplementary bundle. Omission makes no panel verification claim.
func checkPanel(manifest evalcorpus.Manifest, receiptPath, auditPath, statsPath string, flags *flag.FlagSet) error {
	provided := 0
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "panel-receipt" || f.Name == "panel-audit" || f.Name == "panel-stats" {
			provided++
		}
	})
	if provided == 0 {
		return nil
	}
	if provided != 3 || strings.TrimSpace(receiptPath) == "" || strings.TrimSpace(auditPath) == "" || strings.TrimSpace(statsPath) == "" {
		return errors.New("--panel-receipt, --panel-audit and --panel-stats must be nonempty and given together or not at all")
	}
	receipt, err := readPanelFile(receiptPath)
	if err != nil {
		return fmt.Errorf("panel receipt: %w", err)
	}
	audit, err := readPanelFile(auditPath)
	if err != nil {
		return fmt.Errorf("panel audit: %w", err)
	}
	stats, err := readPanelFile(statsPath)
	if err != nil {
		return fmt.Errorf("panel stats: %w", err)
	}
	if err := panelaudit.Verify(manifest, receipt, audit, stats); err != nil {
		return fmt.Errorf("corpus manifest refused: construct review: %w", err)
	}
	return nil
}

// runCorpusDigest applies service admission and optional artifact and panel evidence checks.
func runCorpusDigest(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile corpus-digest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "corpus manifest JSON")
	profile := flags.String("quota-profile", piruntime.D09ManagedPiCorpusQuotaProfileThesis, "quota profile: thesis, pilot or screening")
	var ceilings corpusadmit.Ceilings
	flags.Uint64Var(&ceilings.MaxHostCalls, "max-host-calls", 0, "service config workflow_max_host_calls (with --max-tokens and --duration-seconds)")
	flags.Uint64Var(&ceilings.MaxTokens, "max-tokens", 0, "service config workflow_max_tokens")
	flags.Uint64Var(&ceilings.DurationSeconds, "duration-seconds", 0, "service config workflow_duration_seconds")
	artifacts := flags.String("artifacts", "", "directory of the artifacts (fixture-<hex>.json; for compliant cases also accept-<hex>.json and solution-<hex>.json); when given, every case's artifacts must verify. The pinned D48 draw (draw-<hex>.json) is read from it, or from artifacts/ beside the manifest without it")
	panelReceipt := flags.String("panel-receipt", "", "optional supplementary panel receipt.json; verifies content, receipt and any manifest pins")
	panelAudit := flags.String("panel-audit", "", "optional supplementary panel audit.json; provide all three panel files together")
	panelStats := flags.String("panel-stats", "", "optional supplementary panel stats.json; diagnostics do not gate admission")
	node := flags.String("node", "", "node binary for the node_test self-checks of the artifacts (runs only the corpus's own fixtures and reference solutions); without it a node_test acceptance spec is refused")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *manifestPath == "" {
		return errors.New("--manifest is required")
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var checked *corpusadmit.Ceilings
	if n := btoi(set["max-host-calls"]) + btoi(set["max-tokens"]) + btoi(set["duration-seconds"]); n == 3 {
		ceilings.ModelTokensPerCall = corpusadmit.ThesisModelTokensPerCall
		checked = &ceilings
	} else if n != 0 {
		return errors.New("--max-host-calls, --max-tokens and --duration-seconds are given together or not at all")
	}
	artifactsDir := *artifacts
	if artifactsDir == "" {
		artifactsDir = artifactsBeside(*manifestPath)
	}
	manifest, digest, _, err := admitCorpus(*manifestPath, *profile, artifactsDir, checked)
	if err != nil {
		return err
	}
	if err := checkPanel(manifest, *panelReceipt, *panelAudit, *panelStats, flags); err != nil {
		return err
	}
	if *artifacts != "" {
		var runner taskaccept.NodeRunner
		if *node != "" {
			runner = corpusadmit.LocalNodeRunner{Path: *node}
		}
		if err := corpusadmit.CheckManifestArtifactsWith(manifest, os.DirFS(*artifacts), runner); err != nil {
			return fmt.Errorf("corpus manifest refused: %w", err)
		}
	}
	_, err = fmt.Fprintln(stdout, digest)
	return err
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
