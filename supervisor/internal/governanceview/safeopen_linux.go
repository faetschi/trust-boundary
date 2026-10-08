//go:build linux

package governanceview

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openRegularNoFollow(path string) (*os.File, error) {
	lstat, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if lstat.Mode()&os.ModeSymlink != 0 || !lstat.Mode().IsRegular() {
		return nil, errors.New("observation document must be a regular non-symlink file")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open observation document returned no file")
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat opened observation document: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(lstat, opened) {
		_ = file.Close()
		return nil, errors.New("observation document changed while opening")
	}
	return file, nil
}
