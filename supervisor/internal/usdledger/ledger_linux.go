//go:build linux

package usdledger

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openLedgerFile creates path exclusively when it is absent and otherwise opens
// the existing file. Either way the returned handle holds an exclusive
// nonblocking flock until it is closed. O_NOFOLLOW makes a symlink at path fail
// closed.
func openLedgerFile(path string) (*os.File, bool, error) {
	flags := os.O_RDWR | os.O_APPEND | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	file, err := os.OpenFile(path, flags|os.O_CREATE|os.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		file, err = os.OpenFile(path, flags, 0)
	}
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, false, ErrInvalidLedgerFile
		}
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, ErrLocked
		}
		return nil, false, fmt.Errorf("flock ledger: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, false, ErrInvalidLedgerFile
	}
	return file, created, nil
}
