package corpusadmit

import (
	"fmt"
	"io"
	"io/fs"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/trialfix"
)

// AcceptArtifactName is the file name of the acceptance spec whose digest is digest:
// accept-<64 lowercase hex digits>.json. It reports false for anything that is not a
// "sha256:" digest.
func AcceptArtifactName(digest string) (string, bool) { return artifactName("accept-", digest) }

// SolutionArtifactName is the file name of the reference solution whose digest is digest:
// solution-<64 lowercase hex digits>.json.
func SolutionArtifactName(digest string) (string, bool) { return artifactName("solution-", digest) }

// readArtifact reads the artifact of digest, named by name, and returns its bytes only if
// they digest to exactly digest (the SHA-256 of the file).
func readArtifact(fsys fs.FS, name, digest string) ([]byte, error) {
	return readArtifactMax(fsys, name, digest, taskaccept.MaxSpecBytes)
}

func readArtifactMax(fsys fs.FS, name, digest string, limit int) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("artifact %s: %w", name, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("artifact %s: %w", name, err)
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("artifact %s: more than %d bytes", name, limit)
	}
	if got := taskaccept.DigestOf(raw); got != digest {
		return nil, fmt.Errorf("artifact %s digests to %s, not %s", name, got, digest)
	}
	return raw, nil
}

// checkTask verifies the acceptance spec and the reference solution of a compliant case
// against the digests the case pins, and runs the oracle self-check: the untouched fixture
// must fail the acceptance spec and the fixture with the solution applied must pass it.
// The ref members of the case are labels; the artifacts are found by digest.
func checkTask(fsys fs.FS, fixture trialfix.Spec, c *evalcorpus.Compliant, runner taskaccept.NodeRunner) error {
	acceptName, ok := AcceptArtifactName(c.AcceptanceTest.Digest)
	if !ok {
		return fmt.Errorf("acceptance test digest %q is not sha256 followed by 64 lowercase hex digits", c.AcceptanceTest.Digest)
	}
	solutionName, ok := SolutionArtifactName(c.ReferenceSolution.Digest)
	if !ok {
		return fmt.Errorf("reference solution digest %q is not sha256 followed by 64 lowercase hex digits", c.ReferenceSolution.Digest)
	}
	acceptRaw, err := readArtifact(fsys, acceptName, c.AcceptanceTest.Digest)
	if err != nil {
		return err
	}
	accept, err := taskaccept.ParseAccept(acceptRaw)
	if err != nil {
		return fmt.Errorf("artifact %s: %w", acceptName, err)
	}
	solutionRaw, err := readArtifact(fsys, solutionName, c.ReferenceSolution.Digest)
	if err != nil {
		return err
	}
	solution, err := taskaccept.ParseSolution(solutionRaw)
	if err != nil {
		return fmt.Errorf("artifact %s: %w", solutionName, err)
	}
	if err := taskaccept.SelfCheckWith(fixture, accept, solution, runner); err != nil {
		return fmt.Errorf("%s with %s: %w", acceptName, solutionName, err)
	}
	return nil
}
