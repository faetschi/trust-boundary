package corpusadmit

import (
	"fmt"
	"io/fs"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/evalcorpus"
)

// AttackerArtifactName is the file name of the attacker artifact whose digest is digest:
// attacker-<64 lowercase hex digits>.json. It reports false for anything that is not a "sha256:" digest.
func AttackerArtifactName(digest string) (string, bool) { return artifactName("attacker-", digest) }

// LoadAttacker reads the attacker artifact of digest from fsys and returns it only if its bytes digest to
// exactly digest, parse strictly (attacker.ParseSpec) and validate (attacker.Spec.Validate): five classes,
// positive caps, and a feedback policy that covers the whole public projection.
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
// library size) and one for the final text turn.
func CheckAttacker(m evalcorpus.Manifest, fsys fs.FS) error {
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
	if caps := spec.CapsValue(); caps.Iterations > 0 && uint64(caps.Iterations) < issuable {
		issuable = uint64(caps.Iterations)
	}
	for _, c := range m.Cases {
		if c.Trial == nil || c.Trial.Pivots == nil {
			continue
		}
		for id := range c.Trial.Pivots {
			if !steps[id] {
				return fmt.Errorf("case %q: pivot %q is not a step of the pinned attacker library", c.CaseID, id)
			}
		}
		if c.Trial.MaxHostCalls < issuable+1 {
			return fmt.Errorf("case %q: max_host_calls %d is below the %d host calls of the adaptive replay", c.CaseID, c.Trial.MaxHostCalls, issuable+1)
		}
	}
	return nil
}

// CheckManifestArtifacts is CheckArtifacts plus CheckAttacker: every artifact the manifest pins, per
// case and manifest-wide. It is what `tbound-profile corpus-digest --artifacts` applies.
func CheckManifestArtifacts(m evalcorpus.Manifest, fsys fs.FS) error {
	if err := CheckArtifacts(m.Cases, fsys); err != nil {
		return err
	}
	return CheckAttacker(m, fsys)
}
