//go:build !linux

package runner

import "os"

// lockJournal fails closed where the Linux flock implementation is unavailable.
func lockJournal(*os.File) error { return ErrJournalLockUnsupported }

// unlockJournal is never reached when lockJournal failed.
func unlockJournal(*os.File) error { return nil }
