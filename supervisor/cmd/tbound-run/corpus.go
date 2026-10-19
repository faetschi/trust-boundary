package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/runner"
)

// plan is everything the subcommands derive from run.json without touching the guest.
type plan struct {
	cfg      runConfig
	manifest evalcorpus.Manifest // the admitted, allocated manifest
	digest   string              // evalcorpus.Digest of the manifest
	sched    runner.Schedule
	route    runner.RouteCoverage
	evq      runner.EVQResults // the pinned conformance results
}

func quotasFor(profile string) (evalcorpus.Quotas, error) {
	switch profile {
	case quotaThesis:
		return evalcorpus.ThesisQuotas(), nil
	case quotaPilot:
		return evalcorpus.PilotQuotas(), nil
	}
	return evalcorpus.Quotas{}, fmt.Errorf("unknown quota profile %q", profile)
}

// loadPlan admits the corpus the way the corpus-mode service and `tbound-profile
// preregistration-record` do (strict loader, evalcorpus.Validate under the quotas,
// corpusadmit.CheckTrials), allocates it from its own split seed and plans it.
func loadPlan(cfg runConfig) (plan, error) {
	quotas, err := quotasFor(cfg.QuotaProfile)
	if err != nil {
		return plan{}, err
	}
	f, err := os.Open(cfg.Manifest)
	if err != nil {
		return plan{}, fmt.Errorf("corpus manifest: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return plan{}, fmt.Errorf("corpus manifest: %s is not a regular file", cfg.Manifest)
	}
	manifest, err := evalcorpus.Load(f)
	if err != nil {
		return plan{}, fmt.Errorf("corpus manifest refused: %w", err)
	}
	digest, err := evalcorpus.Digest(manifest)
	if err != nil {
		return plan{}, fmt.Errorf("corpus manifest refused: %w", err)
	}
	if violations := evalcorpus.Validate(manifest, quotas); len(violations) != 0 {
		var listed []string
		for i, v := range violations {
			if i == 5 {
				listed = append(listed, fmt.Sprintf("... and %d more", len(violations)-i))
				break
			}
			listed = append(listed, v.Rule+": "+v.Detail)
		}
		return plan{}, fmt.Errorf("corpus manifest refused: %d violation(s) under the %s quotas: %s",
			len(violations), cfg.QuotaProfile, strings.Join(listed, "; "))
	}
	if err := corpusadmit.CheckTrials(manifest.Cases, nil); err != nil {
		return plan{}, fmt.Errorf("corpus manifest refused: %w", err)
	}
	if cfg.AdaptiveCaps, err = adaptiveCapsFor(manifest, cfg.Artifacts, cfg.AdaptiveCaps); err != nil {
		return plan{}, fmt.Errorf("corpus manifest refused: %w", err)
	}
	allocation, err := evalcorpus.Allocate(manifest, quotas)
	if err != nil {
		return plan{}, fmt.Errorf("allocation: %w", err)
	}
	if allocated, err := evalcorpus.Digest(allocation); err != nil || allocated != digest {
		return plan{}, errors.New("the manifest is not its own seeded allocation")
	}
	sched, err := runner.Plan(manifest, allocation, cfg.runnerConfig())
	if err != nil {
		return plan{}, fmt.Errorf("runner plan: %w", err)
	}
	if cfg.QuotaProfile == quotaThesis && (sched.Subset || sched.Units() != evalreport.PlannedUnits) {
		return plan{}, fmt.Errorf("a thesis plan must be the registered %d units without a subset; it has %d (subset %t)",
			evalreport.PlannedUnits, sched.Units(), sched.Subset)
	}
	if cfg.VImageDigest == "" && slices.ContainsFunc(sched.Steps, func(s runner.Step) bool { return s.Configuration == runner.CfgV }) {
		return plan{}, errors.New("config: v_image_digest is required: the plan has Configuration V steps, and a V outcome is E01-verified only against the pinned cell image")
	}
	route, err := loadRoute(cfg.RouteReport, cfg.FreezeCommit)
	if err != nil {
		return plan{}, err
	}
	evq, evqDigest, err := loadEVQResults(cfg.EVQResults)
	if err != nil {
		return plan{}, err
	}
	for _, r := range evq {
		if r.Passed && !evqEvidenceBoundTo(r, cfg.FreezeCommit) {
			return plan{}, fmt.Errorf("evq results: %s passed with evidence that is not bound to the freeze commit %s (%q)", r.ID, cfg.FreezeCommit, r.EvidenceRef)
		}
	}
	if sched.RunBinding, err = cfg.runBinding(route, evqDigest); err != nil {
		return plan{}, err
	}
	return plan{cfg: cfg, manifest: allocation, digest: digest, sched: sched, route: route, evq: evq}, nil
}

// adaptiveCapsFor returns the adaptive caps the plan is judged with. A manifest that pins an attacker artifact
// gets the caps of that artifact: they are the caps the service enforces, so the host never judges the adaptive
// runs against caps of its own. The artifact must load from artifactsDir (the same files the guest holds, with
// the digest the manifest pins) and fit the manifest (corpusadmit.CheckAttacker). configured, the run.json
// value, may be left zero; if it is set, it must equal the artifact's caps. A manifest without an attacker pin
// keeps the configured caps.
func adaptiveCapsFor(m evalcorpus.Manifest, artifactsDir string, configured calibration.Caps) (calibration.Caps, error) {
	if m.Pins.AttackerDigest == "" {
		return configured, nil
	}
	if artifactsDir == "" {
		return calibration.Caps{}, errors.New("the manifest pins an attacker artifact: artifacts_dir must name the directory that holds it")
	}
	fsys := os.DirFS(artifactsDir)
	if err := corpusadmit.CheckAttacker(m, fsys); err != nil {
		return calibration.Caps{}, fmt.Errorf("pins.attacker_digest: %w", err)
	}
	spec, err := corpusadmit.LoadAttacker(fsys, m.Pins.AttackerDigest)
	if err != nil {
		return calibration.Caps{}, fmt.Errorf("pins.attacker_digest: %w", err)
	}
	c := spec.CapsValue()
	derived := calibration.Caps{Iterations: int(c.Iterations), ToolCalls: int(c.ToolCalls), WallMS: int(c.WallMS),
		InTokens: int(c.InTokens), OutTokens: int(c.OutTokens), MicroUSD: c.MicroUSD}
	if configured != (calibration.Caps{}) && configured != derived {
		return calibration.Caps{}, fmt.Errorf("adaptive_caps %+v differ from the caps of the pinned attacker artifact %+v: leave adaptive_caps out", configured, derived)
	}
	return derived, nil
}
