//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var productManagedRootStat = unix.Fstat

func validateProductManagedRoot(stateRoot, projectPath string, root *os.File) error {
	if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot ||
		!filepath.IsAbs(projectPath) || filepath.Clean(projectPath) != projectPath ||
		!strings.HasPrefix(projectPath, stateRoot+"/") {
		return errors.New("managed project exclusivity requires a project beneath the product state directory")
	}
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return fmt.Errorf("managed project exclusivity: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	components := strings.Split(strings.TrimPrefix(projectPath, "/"), "/")
	var st unix.Stat_t
	for i := -1; i < len(components); i++ {
		if i >= 0 {
			next, err := unix.Openat(fd, components[i], flags, 0)
			if err != nil {
				return fmt.Errorf("managed project exclusivity: open ancestor %q: %w", components[i], err)
			}
			_ = unix.Close(fd)
			fd = next
		}
		if err := productManagedRootStat(fd, &st); err != nil {
			return fmt.Errorf("managed project exclusivity: %w", err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != 0 && st.Uid != uint32(os.Geteuid()) || st.Mode&0o022 != 0 {
			return errors.New("managed project exclusivity requires root/service-owned directories without group or other write permission")
		}
	}
	var held unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &held); err != nil {
		return fmt.Errorf("managed project exclusivity: %w", err)
	}
	if held.Dev != st.Dev || held.Ino != st.Ino {
		return errors.New("managed project exclusivity: the opened root differs from the protected path")
	}
	return nil
}
