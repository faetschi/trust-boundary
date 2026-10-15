//go:build !linux

package usdledger

import "os"

// openLedgerFile fails closed on platforms without the exclusive flock that the
// single-writer rule requires.
func openLedgerFile(string) (*os.File, bool, error) {
	return nil, false, ErrUnsupported
}
