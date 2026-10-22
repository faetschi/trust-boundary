//go:build windows

package runner

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockJournal takes an exclusive, nonblocking byte-range lock on the journal (the Windows
// counterpart of flock LOCK_EX|LOCK_NB): tbound-run.exe drives trials from the Windows host.
func lockJournal(f *os.File) error {
	var ol windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrJournalLocked
	}
	return fmt.Errorf("lock journal: %w", err)
}

// unlockJournal releases the lock. Closing the handle would release it too.
func unlockJournal(f *os.File) error {
	var ol windows.Overlapped
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol); err != nil {
		return fmt.Errorf("unlock journal: %w", err)
	}
	return nil
}
