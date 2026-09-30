//go:build linux

package audit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func openJournalFiles(path string) (*os.File, *os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve audit journal path: %w", err)
	}
	directoryPath := filepath.Dir(filepath.Clean(absolute))
	directory, err := openJournalDirectory(directoryPath)
	if err != nil {
		return nil, nil, err
	}
	if err := checkPrivateDirectory(directory); err != nil {
		_ = directory.Close()
		return nil, nil, err
	}

	fileFD, err := syscall.Openat(int(directory.Fd()), filepath.Base(absolute), syscall.O_RDWR|syscall.O_APPEND|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		_ = directory.Close()
		if errors.Is(err, syscall.ELOOP) {
			return nil, nil, ErrJournalSymlink
		}
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fileFD), path)
	if file == nil {
		_ = syscall.Close(fileFD)
		_ = directory.Close()
		return nil, nil, errors.New("could not create journal file handle")
	}
	return file, directory, nil
}

// openJournalDirectory anchors traversal at / and opens each component
// relative to the already-validated directory handle. Every returned handle
// therefore refers to the same inode even if an ancestor name is replaced.
func openJournalDirectory(path string) (*os.File, error) {
	directoryFD, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(directoryFD), string(os.PathSeparator))
	if current == nil {
		_ = syscall.Close(directoryFD)
		return nil, errors.New("could not create root directory handle")
	}
	checkCurrent := func() error {
		info, err := current.Stat()
		if err != nil {
			return fmt.Errorf("stat audit path directory handle: %w", err)
		}
		return checkTrustedAncestor(info)
	}
	if err := checkCurrent(); err != nil {
		_ = current.Close()
		return nil, err
	}

	cleanPath := filepath.Clean(path)
	components := strings.Split(strings.TrimPrefix(cleanPath, string(os.PathSeparator)), string(os.PathSeparator))
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		nextFD, err := syscall.Openat(int(current.Fd()), component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			_ = current.Close()
			if errors.Is(err, syscall.ELOOP) {
				return nil, ErrJournalSymlink
			}
			return nil, fmt.Errorf("open audit path directory %q: %w", component, err)
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), component))
		if next == nil {
			_ = syscall.Close(nextFD)
			_ = current.Close()
			return nil, errors.New("could not create audit path directory handle")
		}
		current.Close()
		current = next
		if err := checkCurrent(); err != nil {
			_ = current.Close()
			return nil, err
		}
	}
	return current, nil
}

func checkTrustedAncestor(info os.FileInfo) error {
	if !info.IsDir() {
		return ErrInvalidJournalFile
	}
	uid, err := inodeOwner(info)
	if err != nil {
		return err
	}
	euid := uint32(syscall.Geteuid())
	if uid != 0 && uid != euid {
		return fmt.Errorf("%w: ancestor owner uid %d", ErrInsecureOwnership, uid)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: writable ancestor mode %04o", ErrInsecurePermissions, info.Mode().Perm())
	}
	return nil
}

func checkSupervisorOwnership(info os.FileInfo) error {
	uid, err := inodeOwner(info)
	if err != nil {
		return err
	}
	if uid != uint32(syscall.Geteuid()) {
		return fmt.Errorf("%w: owner uid %d, supervisor uid %d", ErrInsecureOwnership, uid, syscall.Geteuid())
	}
	return nil
}

func inodeOwner(info os.FileInfo) (uint32, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, fmt.Errorf("%w: inode ownership unavailable", ErrInsecureOwnership)
	}
	return stat.Uid, nil
}

func currentEffectiveUID() int {
	return syscall.Geteuid()
}

func acquireJournalLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrAlreadyOpen
	}
	return fmt.Errorf("flock journal: %w", err)
}

func releaseJournalLock(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock journal: %w", err)
	}
	return nil
}
