//go:build windows

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
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode observation document path: %w", err)
	}
	handle, err := syscall.CreateFile(
		name,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, errors.New("open observation document returned no file")
	}
	closeOnError := func(reason error) (*os.File, error) {
		_ = file.Close()
		return nil, reason
	}
	var handleInfo syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &handleInfo); err != nil {
		return closeOnError(fmt.Errorf("inspect opened observation document: %w", err))
	}
	if handleInfo.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 || handleInfo.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return closeOnError(errors.New("observation document is a reparse point or directory"))
	}
	opened, err := file.Stat()
	if err != nil {
		return closeOnError(fmt.Errorf("stat opened observation document: %w", err))
	}
	if !opened.Mode().IsRegular() || !os.SameFile(lstat, opened) {
		return closeOnError(errors.New("observation document changed while opening"))
	}
	return file, nil
}
