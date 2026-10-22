//go:build linux

package trialfix

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const openDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// RootIdentity pins the directory Reset replaces: the device and inode the verified managed
// live root registration recorded. The zero value does not pin.
type RootIdentity struct{ Device, Inode uint64 }

// Reset replaces the content of root with the spec's tree, in place: Wipe, then Populate.
// The root directory itself (inode, mode, owner) is kept, and the fence directory is left (or
// made) canonical and empty.
func Reset(root string, spec Spec, owner Owner) error {
	return ResetPinned(root, spec, owner, RootIdentity{})
}

// ResetPinned is Reset for a root whose identity the caller verified (the signed registration's
// RootDevice/RootInode). The directory at root must still be that device and inode before
// anything is wiped: a root that was renamed and replaced by a decoy is refused. The descriptor
// is held across Wipe and Populate, so the check and the wipe see the same directory.
func ResetPinned(root string, spec Spec, owner Owner, want RootIdentity) error {
	files, err := spec.Files()
	if err != nil {
		return err
	}
	rfd, err := unix.Open(root, openDirFlags, 0)
	if err != nil {
		return fmt.Errorf("open the fixture root: %w", err)
	}
	defer unix.Close(rfd)
	var st unix.Stat_t
	if err := unix.Fstat(rfd, &st); err != nil {
		return fmt.Errorf("stat the fixture root: %w", err)
	}
	if want != (RootIdentity{}) && (uint64(st.Dev) != want.Device || st.Ino != want.Inode) {
		return fmt.Errorf("the fixture root is not the registered live root: device %d inode %d, want device %d inode %d",
			st.Dev, st.Ino, want.Device, want.Inode)
	}
	if err := wipeDir(rfd, uint64(st.Dev), true, &owner); err != nil {
		return err
	}
	return populateFD(rfd, files, owner)
}

// Wipe removes everything under root and makes the fence directory canonical. Every step is
// relative to a directory descriptor opened with O_NOFOLLOW: a symlink is unlinked and never
// followed, and a directory on another device is refused rather than entered. The root itself
// is not touched.
func Wipe(root string, owner Owner) error {
	rfd, err := unix.Open(root, openDirFlags, 0)
	if err != nil {
		return fmt.Errorf("open the fixture root: %w", err)
	}
	defer unix.Close(rfd)
	var st unix.Stat_t
	if err := unix.Fstat(rfd, &st); err != nil {
		return fmt.Errorf("stat the fixture root: %w", err)
	}
	return wipeDir(rfd, uint64(st.Dev), true, &owner)
}

// wipeDir removes every entry of the directory open at dirfd. At the top of the root, the entry
// named LostFound is kept back and handed to canonicalFence, which replaces whatever it is with
// the empty 0755 directory of the fixture owner: the cell may write the fence, and what it
// leaves there is not carried into the next trial.
func wipeDir(dirfd int, dev uint64, top bool, fence *Owner) error {
	names, err := listNames(dirfd)
	if err != nil {
		return fmt.Errorf("list a directory: %w", err)
	}
	for _, name := range names {
		if top && fence != nil && name == LostFound {
			continue
		}
		if err := removeEntry(dirfd, name, dev); err != nil {
			return err
		}
	}
	if top && fence != nil {
		return canonicalFence(dirfd, dev, *fence)
	}
	return nil
}

// removeEntry unlinks name from the directory open at dirfd, wiping a directory first. A
// directory on another device is refused rather than entered.
func removeEntry(dirfd int, name string, dev uint64) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("stat %q: %w", name, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		if err := unix.Unlinkat(dirfd, name, 0); err != nil {
			return fmt.Errorf("remove %q: %w", name, err)
		}
		return nil
	}
	if uint64(st.Dev) != dev {
		return fmt.Errorf("%q is on another device", name)
	}
	if err := wipeChild(dirfd, name, &st, dev); err != nil {
		return err
	}
	if err := unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove directory %q: %w", name, err)
	}
	return nil
}

// canonicalFence makes the top-level LostFound the canonical fence: a real directory, owned by
// owner, mode FenceMode and empty. A symlink, a file, a directory of another device, a directory
// with the wrong owner or mode, and a directory with content are all removed and recreated. It
// fails closed if the entry changes while it is examined or cannot be recreated.
func canonicalFence(dirfd int, dev uint64, owner Owner) error {
	var st unix.Stat_t
	err := unix.Fstatat(dirfd, LostFound, &st, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case err == nil:
		if st.Mode&unix.S_IFMT == unix.S_IFDIR && uint64(st.Dev) == dev && st.Uid == owner.UID &&
			st.Gid == owner.GID && st.Mode&0o7777 == FenceMode {
			fd, err := unix.Openat(dirfd, LostFound, openDirFlags, 0)
			if err != nil {
				return fmt.Errorf("open %q: %w", LostFound, err)
			}
			var got unix.Stat_t
			ferr := unix.Fstat(fd, &got)
			names, lerr := listNames(fd)
			unix.Close(fd)
			if ferr != nil || got.Ino != st.Ino || got.Dev != st.Dev {
				return fmt.Errorf("%q changed while it was checked", LostFound)
			}
			if lerr != nil {
				return fmt.Errorf("list %q: %w", LostFound, lerr)
			}
			if len(names) == 0 {
				return nil
			}
		}
		if err := removeEntry(dirfd, LostFound, dev); err != nil {
			return err
		}
	case !errors.Is(err, unix.ENOENT):
		return fmt.Errorf("stat %q: %w", LostFound, err)
	}
	if err := unix.Mkdirat(dirfd, LostFound, 0o700); err != nil {
		return fmt.Errorf("create %q: %w", LostFound, err)
	}
	fd, err := unix.Openat(dirfd, LostFound, openDirFlags, 0)
	if err != nil {
		return fmt.Errorf("open %q: %w", LostFound, err)
	}
	defer unix.Close(fd)
	var made unix.Stat_t
	if err := unix.Fstat(fd, &made); err != nil {
		return fmt.Errorf("stat %q: %w", LostFound, err)
	}
	if made.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(made.Dev) != dev {
		return fmt.Errorf("%q is not a directory on the live root's device", LostFound)
	}
	if err := unix.Fchown(fd, int(owner.UID), int(owner.GID)); err != nil {
		return fmt.Errorf("chown %q: %w", LostFound, err)
	}
	if err := unix.Fchmod(fd, FenceMode); err != nil {
		return fmt.Errorf("chmod %q: %w", LostFound, err)
	}
	return nil
}

func wipeChild(dirfd int, name string, st *unix.Stat_t, dev uint64) error {
	fd, err := unix.Openat(dirfd, name, openDirFlags, 0)
	if err != nil {
		return fmt.Errorf("open directory %q: %w", name, err)
	}
	defer unix.Close(fd)
	var got unix.Stat_t
	if err := unix.Fstat(fd, &got); err != nil || got.Ino != st.Ino || got.Dev != st.Dev {
		return fmt.Errorf("directory %q changed while it was wiped", name)
	}
	return wipeDir(fd, dev, false, nil)
}

// listNames returns the names in the directory open at dirfd, read through a duplicate
// descriptor so that dirfd stays usable for openat.
func listNames(dirfd int) ([]string, error) {
	fd, err := unix.Dup(dirfd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "")
	defer f.Close()
	return f.Readdirnames(-1)
}

// Populate creates the spec's tree under root, which must hold no entry of the spec's
// names, and gives every entry the spec's mode and owner. Creation is exclusive and
// relative to descriptors opened with O_NOFOLLOW, so it never writes through a symlink and
// never replaces an existing entry. The root itself is not touched. The filesystem is
// synced at the end.
func Populate(root string, spec Spec, owner Owner) error {
	files, err := spec.Files()
	if err != nil {
		return err
	}
	rfd, err := unix.Open(root, openDirFlags, 0)
	if err != nil {
		return fmt.Errorf("open the fixture root: %w", err)
	}
	defer unix.Close(rfd)
	return populateFD(rfd, files, owner)
}

// populateFD builds the spec's tree under the held root descriptor.
func populateFD(rfd int, files []File, owner Owner) error {
	for _, f := range files {
		if err := populateOne(rfd, f, owner); err != nil {
			return fmt.Errorf("populate %q: %w", f.Path, err)
		}
	}
	if err := unix.Syncfs(rfd); err != nil {
		return fmt.Errorf("sync the fixture root: %w", err)
	}
	return nil
}

func populateOne(rfd int, f File, owner Owner) error {
	parent, name := "", f.Path
	if i := strings.LastIndexByte(f.Path, '/'); i >= 0 {
		parent, name = f.Path[:i], f.Path[i+1:]
	}
	pfd, err := openParent(rfd, parent)
	if err != nil {
		return err
	}
	defer closeUnlessRoot(pfd, rfd)
	var fd int
	if f.Dir {
		if err := unix.Mkdirat(pfd, name, 0o700); err != nil {
			return err
		}
		fd, err = unix.Openat(pfd, name, openDirFlags, 0)
	} else {
		fd, err = unix.Openat(pfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	for rest := f.Content; len(rest) > 0; {
		n, err := unix.Write(fd, rest)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		rest = rest[n:]
	}
	if err := unix.Fchown(fd, int(owner.UID), int(owner.GID)); err != nil {
		return err
	}
	return unix.Fchmod(fd, f.Mode)
}

// openParent opens the directory at the root-relative path rel (empty for the root itself,
// whose descriptor is returned), one component at a time with O_NOFOLLOW.
func openParent(rfd int, rel string) (int, error) {
	if rel == "" {
		return rfd, nil
	}
	cur := rfd
	for _, part := range strings.Split(rel, "/") {
		next, err := unix.Openat(cur, part, openDirFlags, 0)
		closeUnlessRoot(cur, rfd)
		if err != nil {
			return -1, err
		}
		cur = next
	}
	return cur, nil
}

func closeUnlessRoot(fd, rfd int) {
	if fd != rfd {
		_ = unix.Close(fd)
	}
}
