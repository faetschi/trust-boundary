//go:build linux

package sessionrepo

func scanReuseGrouped(store *Store) (scanReuseResult, error) {
	verified, err := store.VerifyEvidence()
	if err != nil {
		return scanReuseResult{}, err
	}
	return scanReuseResult{verified.Chain, verified.Artifact, verified.Bundle}, nil
}
