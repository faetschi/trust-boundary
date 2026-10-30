//go:build !linux

package researchroute

import "errors"

// readOwnedFile fails closed on platforms without the descriptor-relative open
// and ownership checks that the research route requires.
func readOwnedFile(string, int64, uint32) ([]byte, error) {
	return nil, errors.Join(ErrConfigFile, errors.New("the research route needs Linux file ownership checks"))
}
