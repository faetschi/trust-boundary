package corpusadmit

import (
	"fmt"
	"io/fs"
	"tbound/supervisor/internal/taskaccept"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/evalcorpus"
)

// AttackerArtifactName is the file name of the attacker artifact whose digest is digest:
// attacker-<64 lowercase hex digits>.json. It reports false for anything that is not a "sha256:" digest.
func AttackerArtifactName(digest string) (string, bool) { return artifactName("attacker-", digest) }

// LoadAttacker reads the attacker artifact of digest from fsys and returns it only if its bytes digest to
// exactly digest, parse strictly (attacker.ParseSpec) and validate (attacker.Spec.Validate): five classes,
// valid source-specific caps, and a feedback policy that covers the whole public projection.
func LoadAttacker(fsys fs.FS, digest string) (attacker.Spec, error) {
	name, ok := AttackerArtifactName(digest)
	if !ok {
		return attacker.Spec{}, fmt.Errorf("attacker digest %q is not sha256 followed by 64 lowercase hex digits", digest)
	}
	raw, err := readArtifact(fsys, name, digest)
	if err != nil {
		return attacker.Spec{}, err
	}
	spec, err := attacker.ParseSpec(raw)
	if err != nil {
		return attacker.Spec{}, fmt.Errorf("artifact %s: %w", name, err)
	}
	if err := spec.Validate(); err != nil {
		return attacker.Spec{}, fmt.Errorf("artifact %s: %w", name, err)
	}
	return spec, nil
}

// CheckAttacker checks the adaptive pins of a manifest against its artifacts. A manifest without pivots
// needs none. Otherwise pins.attacker_digest must name an attacker artifact in fsys that loads
// (LoadAttacker), and every pivot of every case must name a step of its library: a pivot that no
// machine can ever select is a table error. A case's host-call budget must also cover the adaptive
// replay, one host call per pivot the machine can issue (the smaller of its iteration cap and the
// library size) and one for the final text turn. Its duration must reach the attacker's wall cap, and the cap
// must be at least a second, so that the service run deadline min(duration, wall cap left) is the pinned cap.
// Pivot replays require scripted caps, matching adaptivesource.NewAdaptiveSource.
func CheckAttacker(m evalcorpus.Manifest, fsys fs.FS, maxPreEffectRetries uint64) error {
	pivoted := false
	for _, c := range m.Cases {
		if c.Trial != nil && c.Trial.Pivots != nil {
			pivoted = true
			break
		}
	}
	if !pivoted {
		return nil
	}
	spec, err := LoadAttacker(fsys, m.Pins.AttackerDigest)
	if err != nil {
		return fmt.Errorf("pins.attacker_digest: %w", err)
	}
	steps := make(map[string]bool)
	for _, id := range spec.StepIDs() {
		steps[id] = true
	}
	issuable := uint64(len(steps))
	caps := spec.CapsValue()
	if !caps.Scripted {
		return fmt.Errorf("adaptive replay requires scripted caps with zero token and money dimensions")
	}
	if caps.Iterations > 0 && uint64(caps.Iterations) < issuable {
		issuable = uint64(caps.Iterations)
	}
	// The service enforces min(the trial's duration, the wall cap that is left) as the run deadline, in whole
	// seconds. So the artifact's wall cap must allow a whole second, and every adaptive case's duration must
	// reach the cap: with a shorter duration the service ceiling, not the pinned cap, would bind a single
	// attempt and the cumulative wall accounting would not mean what the artifact says.
	if caps.WallMS < 1000 {
		return fmt.Errorf("attacker wall_ms %d is below one second, the unit of the service run deadline", caps.WallMS)
	}
	wallSeconds := uint64((caps.WallMS + 999) / 1000)
	for _, c := range m.Cases {
		if c.Trial == nil || c.Trial.Pivots == nil {
			continue
		}
		for id := range c.Trial.Pivots {
			if !steps[id] {
				return fmt.Errorf("case %q: pivot %q is not a step of the pinned attacker library", c.CaseID, id)
			}
		}
		// Each pivot is a provider turn and its governed effect, and the replay appends one final text
		// turn: issuable pivots need 2*issuable+1 host calls. A pre-effect provider retry the signed
		// service config allows re-dispatches the turn, so it needs one more host call per retry.
		if needed := 2*issuable + 1 + maxPreEffectRetries; c.Trial.MaxHostCalls < needed {
			return fmt.Errorf("case %q: max_host_calls %d is below the %d host calls of the adaptive replay and the %d pre-effect retries",
				c.CaseID, c.Trial.MaxHostCalls, 2*issuable+1, maxPreEffectRetries)
		}
		if c.Trial.DurationSeconds < wallSeconds {
			return fmt.Errorf("case %q: duration_seconds %d is below the %d s wall cap of the pinned attacker", c.CaseID, c.Trial.DurationSeconds, wallSeconds)
		}
	}
	return nil
}

func CheckManifestArtifacts(m evalcorpus.Manifest, fsys fs.FS) error {
	return CheckManifestArtifactsWith(m, fsys, nil)
}

// CheckManifestArtifactsWith is CheckArtifactsWith plus CheckAttacker: every artifact the manifest
// pins, per case and manifest-wide, with the node runner of the node_test self-checks (LocalNodeRunner
// at admission). It is what `tbound-profile corpus-digest --artifacts` applies. It has no signed
// service config, so it checks the replay's own 2N+1 host calls without a pre-effect retry margin;
// the service (and any caller that has the config) calls CheckAttacker with the config's
// max_pre_effect_retries.
func CheckManifestArtifactsWith(m evalcorpus.Manifest, fsys fs.FS, runner taskaccept.NodeRunner) error {
	if err := CheckArtifactsWith(m.Cases, fsys, runner); err != nil {
		return err
	}
	return CheckAttacker(m, fsys, 0)
}
