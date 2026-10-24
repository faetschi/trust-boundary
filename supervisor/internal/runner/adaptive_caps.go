package runner

import (
	"errors"
	"fmt"
	"os"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
)

// AdaptiveCapsFor returns the adaptive caps the plan is judged with. A manifest that pins an attacker artifact
// gets the caps of that artifact: they are the caps the service enforces, so the host never judges the adaptive
// runs against caps of its own. The artifact must load from artifactsDir (the same files the guest holds, with
// the digest the manifest pins) and fit the manifest (corpusadmit.CheckAttacker). configured, the run.json
// value, may be left zero; if it is set, it must equal the artifact's caps. A manifest without an attacker pin
// keeps the configured caps.
func AdaptiveCapsFor(m evalcorpus.Manifest, artifactsDir string, configured calibration.Caps) (calibration.Caps, error) {
	if m.Pins.AttackerDigest == "" {
		return configured, nil
	}
	if artifactsDir == "" {
		return calibration.Caps{}, errors.New("the manifest pins an attacker artifact: artifacts_dir must name the directory that holds it")
	}
	fsys := os.DirFS(artifactsDir)
	// This host-side plan has no signed service config: it checks the replay's own 2N+1 host calls.
	// The service requires the config's max_pre_effect_retries on top (corpusadmit.CheckTrials /
	// CheckAttacker with the admitted config).
	if err := corpusadmit.CheckAttacker(m, fsys, 0); err != nil {
		return calibration.Caps{}, fmt.Errorf("pins.attacker_digest: %w", err)
	}
	spec, err := corpusadmit.LoadAttacker(fsys, m.Pins.AttackerDigest)
	if err != nil {
		return calibration.Caps{}, fmt.Errorf("pins.attacker_digest: %w", err)
	}
	c := spec.CapsValue()
	derived := calibration.Caps{Scripted: c.Scripted, Iterations: int(c.Iterations), ToolCalls: int(c.ToolCalls), WallMS: int(c.WallMS),
		InTokens: int(c.InTokens), OutTokens: int(c.OutTokens), MicroUSD: c.MicroUSD}
	if configured != (calibration.Caps{}) && configured != derived {
		return calibration.Caps{}, fmt.Errorf("adaptive_caps %+v differ from the caps of the pinned attacker artifact %+v: leave adaptive_caps out", configured, derived)
	}
	return derived, nil
}
