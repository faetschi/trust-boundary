package corpusadmit

import (
	"fmt"
	"io/fs"

	"tbound/supervisor/internal/taskaccept"
)

// LoadAccept reads the acceptance artifact of digest from fsys and returns its parsed spec only
// if the bytes digest to exactly digest (the digest a compliant case pins as acceptance_test).
// It is what `tbound trial accept` and `tbound trial v-run` evaluate a post-run tree with.
func LoadAccept(fsys fs.FS, digest string) (taskaccept.AcceptSpec, error) {
	name, ok := AcceptArtifactName(digest)
	if !ok {
		return taskaccept.AcceptSpec{}, fmt.Errorf("acceptance test digest %q is not sha256 followed by 64 lowercase hex digits", digest)
	}
	raw, err := readArtifact(fsys, name, digest)
	if err != nil {
		return taskaccept.AcceptSpec{}, err
	}
	spec, err := taskaccept.ParseAccept(raw)
	if err != nil {
		return taskaccept.AcceptSpec{}, fmt.Errorf("artifact %s: %w", name, err)
	}
	return spec, nil
}

// LoadSolution reads the reference solution artifact of digest from fsys and returns its parsed spec
// only if the bytes digest to exactly digest (the digest a compliant case pins as reference_solution).
func LoadSolution(fsys fs.FS, digest string) (taskaccept.SolutionSpec, error) {
	name, ok := SolutionArtifactName(digest)
	if !ok {
		return taskaccept.SolutionSpec{}, fmt.Errorf("reference solution digest %q is not sha256 followed by 64 lowercase hex digits", digest)
	}
	raw, err := readArtifact(fsys, name, digest)
	if err != nil {
		return taskaccept.SolutionSpec{}, err
	}
	spec, err := taskaccept.ParseSolution(raw)
	if err != nil {
		return taskaccept.SolutionSpec{}, fmt.Errorf("artifact %s: %w", name, err)
	}
	return spec, nil
}
