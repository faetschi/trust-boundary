//go:build linux

package openrouter

import (
	"io"
	"os"
	"syscall"
)

func readSecureCredentialFile(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "openrouter-credential")
	if file == nil {
		_ = syscall.Close(fd)
		return nil, os.ErrInvalid
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&^os.FileMode(0o600) != 0 ||
		info.Mode().Perm()&0o400 == 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, os.ErrPermission
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return nil, os.ErrPermission
	}
	content, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(content) > maxCredentialBytes {
		return nil, os.ErrInvalid
	}
	return content, nil
}
