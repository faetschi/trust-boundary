//go:build linux

package researchroute

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// readOwnedFile reads a configuration file that must be owned by uid (root in
// production), be a regular file that is not group- or world-writable, and sit
// in a directory with the same owner and the same permission rule. Every path
// component is opened with O_NOFOLLOW, so a symlink anywhere fails closed. The
// file must be non-empty and at most maximum bytes.
func readOwnedFile(path string, maximum int64, uid uint32) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || maximum <= 0 {
		return nil, fmt.Errorf("%w: %q is not a canonical absolute path", ErrConfigFile, path)
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "research-root")
	defer func() { _ = current.Close() }()
	components := strings.Split(strings.TrimPrefix(filepath.Dir(path), "/"), "/")
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := syscall.Openat(int(current.Fd()), component,
			syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, fmt.Errorf("%w: open directory of %s: %w", ErrConfigFile, path, openErr)
		}
		_ = current.Close()
		current = os.NewFile(uintptr(next), "research-dir")
	}
	parentInfo, err := current.Stat()
	if err != nil {
		return nil, err
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != uid || parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%w: directory of %s must be owned by uid %d and not group- or world-writable", ErrConfigFile, path, uid)
	}
	fileFD, err := syscall.Openat(int(current.Fd()), filepath.Base(path), syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %w", ErrConfigFile, path, err)
	}
	file := os.NewFile(uintptr(fileFD), path)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uid || info.Mode().Perm()&0o022 != 0 ||
		info.Size() <= 0 || info.Size() > maximum {
		return nil, fmt.Errorf("%w: %s must be a non-empty regular file of at most %d bytes owned by uid %d and not group- or world-writable",
			ErrConfigFile, path, maximum, uid)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errors.Join(ErrConfigFile, err)
	}
	return data, nil
}
