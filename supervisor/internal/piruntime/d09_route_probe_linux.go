//go:build linux

package piruntime

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"

	"tbound/supervisor/internal/cellview"
)

// OSRouteProbeFS is the real read-only surface of the route probe. Every call is a read: an open with
// O_RDONLY (and one byte read), a stat, an open of a directory. The route probe test keeps this file free
// of any call that writes.
type OSRouteProbeFS struct{}

const probeOpenFlags = syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NONBLOCK | syscall.O_NOCTTY

// OpenRead opens path O_RDONLY, reads at most one byte and discards it.
func (OSRouteProbeFS) OpenRead(path string) error {
	fd, err := syscall.Open(path, probeOpenFlags, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var one [1]byte
	_, err = syscall.Read(fd, one[:])
	return err
}

// Stat reads the metadata of path.
func (OSRouteProbeFS) Stat(path string) error {
	var st syscall.Stat_t
	return syscall.Stat(path, &st)
}

// List opens path as a directory and reads one batch of entries.
func (OSRouteProbeFS) List(path string) error {
	fd, err := syscall.Open(path, probeOpenFlags|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var batch [4096]byte
	_, err = syscall.ReadDirent(fd, batch[:])
	return err
}

// Lstat reads the metadata of path without following a final symbolic link (fstatat with
// AT_SYMLINK_NOFOLLOW). Landlock does not mediate either call.
func (OSRouteProbeFS) Lstat(path string) (cellview.Fact, error) {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return cellview.Fact{}, err
	}
	f := cellview.Fact{Mode: st.Mode & 0o7777, UID: st.Uid, GID: st.Gid, Size: st.Size}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		f.Kind = cellview.KindDir
	case syscall.S_IFREG:
		f.Kind = cellview.KindFile
	case syscall.S_IFLNK:
		f.Kind = cellview.KindLink
	default:
		f.Kind = cellview.KindOther
	}
	return f, nil
}

// ACL reports the POSIX ACLs of path: "" for none, "access", "default" or "access+default" for the ones it
// has, and cellview.ACLUnreadable when an attribute read fails for any reason but its absence (a file system
// without ACL support has no ACL to apply).
func (OSRouteProbeFS) ACL(path string) string {
	var present []string
	for _, kind := range []string{"access", "default"} {
		_, err := syscall.Getxattr(path, "system.posix_acl_"+kind, nil)
		switch {
		case err == nil:
			present = append(present, kind)
		case errors.Is(err, syscall.ENODATA), errors.Is(err, syscall.EOPNOTSUPP):
		default:
			return cellview.ACLUnreadable
		}
	}
	return strings.Join(present, "+")
}

// Identity is the effective user and group of the process and its supplementary groups.
func (OSRouteProbeFS) Identity() (euid, egid uint32, groups []uint32) {
	supplementary, err := syscall.Getgroups()
	if err == nil {
		for _, g := range supplementary {
			groups = append(groups, uint32(g))
		}
	}
	return uint32(syscall.Geteuid()), uint32(syscall.Getegid()), groups
}

// LSM is the security module context of the process (/proc/self/attr/current), "" if it cannot be read.
func (OSRouteProbeFS) LSM() string {
	data, err := os.ReadFile("/proc/self/attr/current")
	if err != nil {
		return ""
	}
	context := strings.Trim(string(data), " \t\r\n\x00")
	if len(context) > 128 {
		context = context[:128]
	}
	return context
}

// Overflow reads the ids the kernel reports for an owner or group the process's user namespace does not
// map. ok is false when either file cannot be read or does not hold an id.
func (OSRouteProbeFS) Overflow() (uid, gid uint32, ok bool) {
	read := func(name string) (uint32, bool) {
		data, err := os.ReadFile("/proc/sys/fs/" + name)
		if err != nil {
			return 0, false
		}
		id, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
		return uint32(id), err == nil
	}
	uid, uidOK := read("overflowuid")
	gid, gidOK := read("overflowgid")
	return uid, gid, uidOK && gidOK
}
