//go:build !linux

package audit

import "os"

func openJournalFiles(string) (*os.File, *os.File, error) {
	return nil, nil, ErrLockUnsupported
}

func acquireJournalLock(*os.File) error {
	return ErrLockUnsupported
}

func releaseJournalLock(*os.File) error {
	return nil
}

func checkSupervisorOwnership(os.FileInfo) error {
	return ErrLockUnsupported
}

func openJournalDirectory(string) (*os.File, error) {
	return nil, ErrLockUnsupported
}

func currentEffectiveUID() int {
	return -1
}
