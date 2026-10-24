//go:build linux

package taskaccept

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

// RootFS is a read-only fs.FS over a directory that is held by a descriptor. It is the view
// `tbound trial accept` and `tbound trial v-run` evaluate a post-run tree through.
//
// Nothing in it follows a symbolic link, and nothing leaves the root: every lookup is one
// openat2 relative to the held descriptor with RESOLVE_NO_SYMLINKS, RESOLVE_BENEATH,
// RESOLVE_NO_MAGICLINKS and RESOLVE_NO_XDEV, so a link anywhere on the path, a ".." and a
// mount point are refused by the kernel instead of being checked by name. A symbolic link is
// a non-regular entry at its own path (Stat and ReadDir report it with fs.ModeSymlink). The
// root is held, so renaming it or replacing its path with a link afterwards changes nothing
// about what is read.
type RootFS struct{ fd int }

// RootIdentity pins the directory a RootFS is opened on: the device and inode the verified
// managed live root registration recorded. The zero value does not pin.
type RootIdentity struct{ Device, Inode uint64 }

// ErrRootIdentityMismatch is returned when the directory at the path is not the pinned tree:
// the root was renamed and a decoy now sits where it was.
var ErrRootIdentityMismatch = errors.New("the tree root is not the registered live root")

// OpenRoot opens the directory at path as the root of a RootFS. The final component must not
// be a symbolic link.
func OpenRoot(path string) (*RootFS, error) { return OpenRootPinned(path, RootIdentity{}) }

// OpenRootPinned is OpenRoot for a tree whose identity the caller verified (the signed
// registration's RootDevice/RootInode): the opened directory must be that device and inode.
func OpenRootPinned(path string, want RootIdentity) (*RootFS, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open the tree root: %w", err)
	}
	if want != (RootIdentity{}) {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("stat the tree root: %w", err)
		}
		if uint64(st.Dev) != want.Device || st.Ino != want.Inode {
			unix.Close(fd)
			return nil, fmt.Errorf("%w: device %d inode %d, want device %d inode %d",
				ErrRootIdentityMismatch, st.Dev, st.Ino, want.Device, want.Inode)
		}
	}
	return &RootFS{fd: fd}, nil
}

// Close releases the root descriptor.
func (r *RootFS) Close() error { return unix.Close(r.fd) }

const resolveContained = unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV

// openat opens name relative to the root. O_NONBLOCK keeps a planted FIFO from blocking the
// open; it is cleared again once the file is known to be a regular file or a directory.
func (r *RootFS) openat(dirfd int, name string, flags int) (int, error) {
	how := unix.OpenHow{Flags: uint64(flags | unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOCTTY), Resolve: resolveContained}
	for {
		fd, err := unix.Openat2(dirfd, name, &how)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return fd, err
	}
}

func (r *RootFS) open(name string, flags int) (int, error) {
	if !fs.ValidPath(name) {
		return -1, fs.ErrInvalid
	}
	return r.openat(r.fd, name, flags)
}

// Open implements fs.FS for a regular file or a directory.
func (r *RootFS) Open(name string) (fs.File, error) {
	fd, err := r.open(name, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFREG && kind != unix.S_IFDIR {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "open", Path: name, Err: errors.New("not a regular file or directory")}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}

// Stat implements fs.StatFS. It does not follow a link in the last component either.
func (r *RootFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		var st unix.Stat_t
		if err := unix.Fstat(r.fd, &st); err != nil {
			return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
		}
		return statInfo{name: ".", st: st}, nil
	}
	dir, base := splitParent(name)
	dfd, err := r.open(dir, unix.O_DIRECTORY)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	defer unix.Close(dfd)
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return statInfo{name: base, st: st}, nil
}

// ReadDir implements fs.ReadDirFS: the entries of a directory in name order, each as
// lstat reports it.
func (r *RootFS) ReadDir(name string) ([]fs.DirEntry, error) {
	fd, err := r.open(name, unix.O_DIRECTORY)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	dir := os.NewFile(uintptr(fd), name)
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	sort.Strings(names)
	entries := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, &fs.PathError{Op: "readdir", Path: name + "/" + n, Err: err}
		}
		entries = append(entries, fs.FileInfoToDirEntry(statInfo{name: n, st: st}))
	}
	return entries, nil
}

// splitParent splits a valid non-root path into its directory ("." for a top-level name)
// and last element.
func splitParent(name string) (dir, base string) {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[:i], name[i+1:]
		}
	}
	return ".", name
}

// statInfo is an fs.FileInfo over an lstat result.
type statInfo struct {
	name string
	st   unix.Stat_t
}

func (s statInfo) Name() string       { return s.name }
func (s statInfo) Size() int64        { return s.st.Size }
func (s statInfo) ModTime() time.Time { return time.Unix(int64(s.st.Mtim.Sec), int64(s.st.Mtim.Nsec)) }
func (s statInfo) IsDir() bool        { return s.Mode().IsDir() }
func (s statInfo) Sys() any           { return &s.st }

// Mode maps the lstat mode the way os.FileInfo does.
func (s statInfo) Mode() fs.FileMode {
	mode := fs.FileMode(s.st.Mode & 0o777)
	switch s.st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= fs.ModeDir
	case unix.S_IFLNK:
		mode |= fs.ModeSymlink
	case unix.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case unix.S_IFSOCK:
		mode |= fs.ModeSocket
	case unix.S_IFCHR:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFBLK:
		mode |= fs.ModeDevice
	}
	if s.st.Mode&unix.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if s.st.Mode&unix.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if s.st.Mode&unix.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}
