//go:build linux

package usdledger

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// afterFlockHook is a test seam that runs after the lock is taken and before
// the path is compared with the locked file. It is nil outside tests.
var afterFlockHook func(path string)

// openLedgerFile opens the ledger at path with an exclusive nonblocking flock held
// until the handle is closed. When create is true and path is absent, it creates
// the file exclusively and reports created. When create is false and path is
// absent, it returns ErrMissingLedger and creates nothing. O_NOFOLLOW makes a
// symlink at path fail closed. After the lock is taken, path must still name the
// locked file. A file renamed into place while this call waited is refused.
func openLedgerFile(path string, create bool) (*os.File, bool, error) {
	flags := os.O_RDWR | os.O_APPEND | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	var file *os.File
	var err error
	created := false
	if create {
		file, err = os.OpenFile(path, flags|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			created = true
		} else if errors.Is(err, fs.ErrExist) {
			file, err = os.OpenFile(path, flags, 0)
		}
	} else {
		file, err = os.OpenFile(path, flags, 0)
	}
	if err != nil {
		switch {
		case errors.Is(err, syscall.ELOOP):
			return nil, false, ErrInvalidLedgerFile
		case !create && errors.Is(err, fs.ErrNotExist):
			return nil, false, ErrMissingLedger
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
	if afterFlockHook != nil {
		afterFlockHook(path)
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
	// The lock belongs to the inode we opened. Writing through it is only correct
	// while path still names that inode. Lstat does not follow a symlink.
	pathInfo, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, ErrLedgerReplaced
		}
		return nil, false, err
	}
	if !os.SameFile(info, pathInfo) {
		_ = file.Close()
		return nil, false, ErrLedgerReplaced
	}
	return file, created, nil
}

// openLedgerFileShared opens the ledger read-only with a shared nonblocking
// flock, so an active writer yields ErrLocked and nothing is created or written.
// The symlink and replacement checks match openLedgerFile.
func openLedgerFileShared(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		switch {
		case errors.Is(err, syscall.ELOOP):
			return nil, ErrInvalidLedgerFile
		case errors.Is(err, fs.ErrNotExist):
			return nil, ErrMissingLedger
		}
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("flock ledger: %w", err)
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = ErrInvalidLedgerFile
	}
	var pathInfo os.FileInfo
	if err == nil {
		pathInfo, err = os.Lstat(path)
	}
	if err == nil && !os.SameFile(info, pathInfo) {
		err = ErrLedgerReplaced
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
