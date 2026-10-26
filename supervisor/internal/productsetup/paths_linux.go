//go:build linux

package productsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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
	f, err := h.openFile(logical, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(mode)
}

func (h *host) appendLine(logical, line string) error {
	f, err := h.openFile(logical, unix.O_APPEND|unix.O_CREAT|unix.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return errors.Join(err, f.Close())
	}
	if !info.Mode().IsRegular() {
		return errors.Join(refuse("%s is not a regular file", logical), f.Close())
	}
	_, err = f.WriteString(line + "\n")
	return errors.Join(err, f.Close())
}

// openFile validates the inode that reads, writes and metadata changes will use.
func openFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, uint32(mode.Perm()))
	if errors.Is(err, unix.ELOOP) {
		return nil, refuse("%s is a symlink", path)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (!info.IsDir() && !info.Mode().IsRegular()) || (info.Mode().IsRegular() && st.Nlink != 1) {
			err = refuse("%s must be a directory or a single-link regular file", path)
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (h *host) openFile(logical string, flags int, mode os.FileMode) (*os.File, error) {
	var f *os.File
	err := h.withPath(logical, flags&unix.O_CREAT != 0, func(path string) error {
		var err error
		f, err = openFile(path, flags, mode)
		return err
	})
	return f, err
}

// openSource also pins external administrator-supplied sources before opening their leaf.
func openSource(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	base, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	defer base.Close()
	parent, err := openDirectory(base, filepath.Dir(absolute), false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return openFile(fmt.Sprintf("/proc/self/fd/%d/%s", parent.Fd(), filepath.Base(absolute)), unix.O_RDONLY, 0)
}
