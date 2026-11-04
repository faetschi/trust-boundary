//go:build !linux

package usdledger

import "os"

// openLedgerFile fails closed on platforms without the exclusive flock that the
// single-writer rule requires.
func openLedgerFile(string, bool) (*os.File, bool, error) {
	return nil, false, ErrUnsupported
}

// openLedgerFileShared fails closed like openLedgerFile.
func openLedgerFileShared(string) (*os.File, error) { return nil, ErrUnsupported }
