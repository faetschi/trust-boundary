//go:build !linux

package usdledger

import (
	"errors"
	"io/fs"
	"os"
)

// openLedgerFile fails closed on platforms without the exclusive flock that the
// single-writer rule requires.
func openLedgerFile(string, bool) (*os.File, bool, error) {
	return nil, false, ErrUnsupported
}

// openLedgerFileShared opens a ledger copy read-only for Verify and Peek. There is
// no writer here (the writer is Linux-only), so no lock is taken.
func openLedgerFileShared(path string, _ bool) (*os.File, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrMissingLedger
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalidLedgerFile
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err == nil && !os.SameFile(info, opened) {
		err = ErrLedgerReplaced
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
