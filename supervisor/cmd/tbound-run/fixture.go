package main

import (
	"fmt"
	"os"

	"tbound/supervisor/internal/trialfix"
)

// fixtureDigest prints the digest a corpus manifest pins for a tbound-fixture/v1 spec.
func (a *app) fixtureDigest(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("fixture spec: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("fixture spec: %s is not a regular file", path)
	}
	spec, err := trialfix.Parse(f)
	if err != nil {
		return err
	}
	digest, err := trialfix.Digest(spec)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.out, digest)
	return err
}
