package main

import (
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/runner"
)

// governedV is the executor of Configuration V, native Pi without the gate: `tbound trial v-run`
// over the trial key. It resets nothing (every run has its own fresh fixture copy on the guest)
// and has no receipt, so Attest does not wrap it; the route coverage is the run's, and the
// document's fixture digest is checked against the manifest's. The document's image digest is
// checked against the run.json pin (v_image_digest, required when V is in the plan): only then
// is a V outcome E01-verified.
func (p plan) governedV(trial runner.Transport) runner.Executor {
	return runner.GovernedV{Transport: trial, ImageDigest: p.cfg.VImageDigest, Route: p.route, FixtureDigest: p.fixtureDigest}
}

// fixtureDigest is the workspace_fixture_digest the manifest gives a case.
func (p plan) fixtureDigest(caseID string) (string, bool) {
	for _, c := range p.manifest.Cases {
		if c.CaseID == caseID {
			return corpusadmit.FixtureDigest(c)
		}
	}
	return "", false
}
