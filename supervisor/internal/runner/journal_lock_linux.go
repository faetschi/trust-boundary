//go:build linux

package runner

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockJournal takes the exclusive, nonblocking advisory lock that makes a
// Journal the only writer of its file, in this process or another. It never
// waits: a held lock is ErrJournalLocked.
func lockJournal(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrJournalLocked
	}
	return fmt.Errorf("flock journal: %w", err)
}

// unlockJournal releases the lock. Closing the file would release it too.
func unlockJournal(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock journal: %w", err)
	}
	return nil
}
