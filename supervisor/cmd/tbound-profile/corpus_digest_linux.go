//go:build linux

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
)

// maxReportedViolations bounds the violations an error message lists.
const maxReportedViolations = 5

// quotasFor returns the registered quotas of a corpus quota profile. The profile names are
// the ones of the service config (piruntime.D09ManagedPiCorpusQuotaProfile*).
func quotasFor(profile string) (evalcorpus.Quotas, error) {
	switch profile {
	case piruntime.D09ManagedPiCorpusQuotaProfileThesis:
		return evalcorpus.ThesisQuotas(), nil
	case piruntime.D09ManagedPiCorpusQuotaProfilePilot:
		return evalcorpus.PilotQuotas(), nil
	}
	return evalcorpus.Quotas{}, fmt.Errorf("unknown quota profile %q: use %q or %q", profile,
		piruntime.D09ManagedPiCorpusQuotaProfileThesis, piruntime.D09ManagedPiCorpusQuotaProfilePilot)
}

// openRegularFile opens path for reading and refuses anything but a regular file.
func openRegularFile(path string) (*os.File, error) {
	f, err := os.Open(path)
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
// corpus_manifest_digest. A nil ceilings skips only the comparison with the host ceilings,
// which are a property of the service config.
func admitCorpus(path, profile string, ceilings *corpusadmit.Ceilings) (evalcorpus.Manifest, string, error) {
	quotas, err := quotasFor(profile)
	if err != nil {
		return evalcorpus.Manifest{}, "", err
	}
	f, err := openRegularFile(path)
	if err != nil {
		return evalcorpus.Manifest{}, "", fmt.Errorf("corpus manifest: %w", err)
	}
	defer f.Close()
	manifest, err := evalcorpus.Load(f)
	if err != nil {
		return evalcorpus.Manifest{}, "", fmt.Errorf("corpus manifest refused: %w", err)
	}
	digest, err := evalcorpus.Digest(manifest)
	if err != nil {
		return evalcorpus.Manifest{}, "", fmt.Errorf("corpus manifest refused: %w", err)
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
		return evalcorpus.Manifest{}, "", fmt.Errorf("corpus manifest refused: evalcorpus.Validate under the %s quotas: %d violation(s): %s",
			profile, len(violations), strings.Join(listed, "; "))
	}
	if err := corpusadmit.CheckTrials(manifest.Cases, ceilings); err != nil {
		return evalcorpus.Manifest{}, "", fmt.Errorf("corpus manifest refused: %w", err)
	}
	return manifest, digest, nil
}

// runCorpusDigest prints the digest of a manifest that the service would admit.
func runCorpusDigest(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound-profile corpus-digest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "corpus manifest JSON")
	profile := flags.String("quota-profile", piruntime.D09ManagedPiCorpusQuotaProfileThesis, "quota profile: thesis or pilot")
	var ceilings corpusadmit.Ceilings
	flags.Uint64Var(&ceilings.MaxHostCalls, "max-host-calls", 0, "service config workflow_max_host_calls (with --max-tokens and --duration-seconds)")
	flags.Uint64Var(&ceilings.MaxTokens, "max-tokens", 0, "service config workflow_max_tokens")
	flags.Uint64Var(&ceilings.DurationSeconds, "duration-seconds", 0, "service config workflow_duration_seconds")
	artifacts := flags.String("artifacts", "", "directory of the artifacts (fixture-<hex>.json; for compliant cases also accept-<hex>.json and solution-<hex>.json); when given, every case's artifacts must verify")
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
		checked = &ceilings
	} else if n != 0 {
		return errors.New("--max-host-calls, --max-tokens and --duration-seconds are given together or not at all")
	}
	manifest, digest, err := admitCorpus(*manifestPath, *profile, checked)
	if err != nil {
		return err
	}
	if *artifacts != "" {
		if err := corpusadmit.CheckArtifacts(manifest.Cases, os.DirFS(*artifacts)); err != nil {
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
