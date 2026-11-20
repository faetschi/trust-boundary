package corpusadmit

import (
	"fmt"
	"io/fs"
	"strings"
	"tbound/supervisor/internal/taskaccept"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/trialfix"
)

const digestPrefix = "sha256:"

// FixtureArtifactName is the file name of the fixture spec whose digest is digest:
// fixture-<64 lowercase hex digits>.json. It reports false for anything that is not a
// "sha256:" digest, so a name never carries a path.
func FixtureArtifactName(digest string) (string, bool) { return artifactName("fixture-", digest) }

func artifactName(prefix, digest string) (string, bool) {
	hex, ok := strings.CutPrefix(digest, digestPrefix)
	if !ok || len(hex) != 64 || strings.Trim(hex, "0123456789abcdef") != "" {
		return "", false
	}
	return prefix + hex + ".json", true
}

// FixtureDigest is the workspace_fixture_digest of a case, whatever its kind. It reports
// false when the case lacks the payload of its kind.
func FixtureDigest(c evalcorpus.Case) (string, bool) {
	switch {
	case c.Kind == evalcorpus.KindAdversarial && c.Adversarial != nil:
		return c.Adversarial.WorkspaceFixtureDigest, true
	case c.Kind == evalcorpus.KindCompliant && c.Compliant != nil:
		return c.Compliant.WorkspaceFixtureDigest, true
	case c.Kind == evalcorpus.KindAmbiguous && c.Ambiguous != nil:
		return c.Ambiguous.WorkspaceFixtureDigest, true
	}
	return "", false
}

// LoadFixture reads and parses the fixture artifact of digest from fsys and returns its
// spec only if the spec digests to exactly digest.
func LoadFixture(fsys fs.FS, digest string) (trialfix.Spec, error) {
	name, ok := FixtureArtifactName(digest)
	if !ok {
		return trialfix.Spec{}, fmt.Errorf("fixture digest %q is not sha256 followed by 64 lowercase hex digits", digest)
	}
	f, err := fsys.Open(name)
	if err != nil {
		return trialfix.Spec{}, fmt.Errorf("fixture artifact %s: %w", name, err)
	}
	defer f.Close()
	spec, err := trialfix.Parse(f)
	if err != nil {
		return trialfix.Spec{}, fmt.Errorf("fixture artifact %s: %w", name, err)
	}
	if got, err := trialfix.Digest(spec); err != nil || got != digest {
		return trialfix.Spec{}, fmt.Errorf("fixture artifact %s digests to %s, not %s", name, got, digest)
	}
	return spec, nil
}

// CheckArtifactsWith returns the first case whose workspace fixture artifact is missing,
// unreadable, or does not recompute to the case's workspace_fixture_digest. The artifacts
// are fixture-<hex>.json files in fsys (see FixtureArtifactName). Cases may share a fixture.
//
// A compliant case additionally needs accept-<hex>.json and solution-<hex>.json, whose bytes
// digest to the case's acceptance_test.digest and reference_solution.digest, and the pair
// must pass taskaccept.SelfCheckWith against the case's fixture (see checkTask). The runner runs
// the node_test self-checks; with a nil runner a node_test acceptance spec is refused.
func CheckArtifactsWith(cases []evalcorpus.Case, fsys fs.FS, runner taskaccept.NodeRunner) error {
	for _, c := range cases {
		digest, ok := FixtureDigest(c)
		if !ok {
			return fmt.Errorf("case %q has no workspace fixture digest", c.CaseID)
		}
		fixture, err := LoadFixture(fsys, digest)
		if err != nil {
			return fmt.Errorf("case %q: %w", c.CaseID, err)
		}
		if c.Kind == evalcorpus.KindCompliant {
			if err := checkTask(fsys, fixture, c.Compliant, runner); err != nil {
				return fmt.Errorf("case %q: %w", c.CaseID, err)
			}
		}
	}
	return nil
}
