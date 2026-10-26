//go:build linux

package productsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func directoryNotEmpty(err error) bool {
	return errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST)
}

// openDirectory pins each component, as projectbundle's ancestor walk does. No lookup follows a symlink.
func openDirectory(base *os.File, path string, create bool) (*os.File, error) {
	fd, err := unix.Openat(int(base.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			unix.Close(fd)
			return nil, refuse("parent traversal is not allowed")
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if err = unix.Mkdirat(fd, part, 0o755); err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(fd)
		if err != nil {
			if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
				return nil, refuse("%s contains a symlink or non-directory parent", path)
			}
			return nil, &os.PathError{Op: "open directory", Path: path, Err: err}
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (h *host) openRoot() error {
	if err := ValidateRoot(h.root); err != nil {
		return err
	}
	base, err := os.Open("/")
	if err != nil {
		return err
	}
	defer base.Close()
	h.dir, err = openDirectory(base, h.root, false)
	if err != nil {
		return err
	}
	// Refuse an existing unsafe parent before accounts or service commands can change anything.
	paths := []string{"/etc/passwd", "/etc/group", CatalogPath, SocketPath, LastKnownGoodPath, stagedBinaryPath}
	for _, n := range append(append([]node{}, directories...), installedFiles...) {
		paths = append(paths, n.Path)
	}
	for _, path := range paths {
		err := h.withPath(path, false, func(string) error { return nil })
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			h.dir.Close()
			return err
		}
	}
	return nil
}

// withPath keeps the parent pinned until the operation finishes, including atomic renames and removals.
func (h *host) withPath(logical string, create bool, apply func(string) error) error {
	parent, err := openDirectory(h.dir, filepath.Dir(logical), create)
	if err != nil {
		return err
	}
	defer parent.Close()
	return apply(fmt.Sprintf("/proc/self/fd/%d/%s", parent.Fd(), filepath.Base(logical)))
}

func (h *host) chmod(logical string, mode os.FileMode) error {
	return h.withPath(logical, false, func(path string) error {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), path)
		defer f.Close()
		return f.Chmod(mode)
	})
}

func (h *host) appendLine(logical, line string) error {
	return h.withPath(logical, true, func(path string) error {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|unix.O_NOFOLLOW, 0o644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(line + "\n")
		return errors.Join(err, f.Close())
	})
}
