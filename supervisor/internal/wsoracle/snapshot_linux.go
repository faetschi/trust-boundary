package wsoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"syscall"
	"unsafe"
)

// readChunk is the buffer size used to hash regular files.
const readChunk = 64 << 10

// maxLinkTarget bounds the symlink target read by readlinkat.
const maxLinkTarget = 4096

// errTargetTooLong reports a symlink target longer than maxLinkTarget.
var errTargetTooLong = errors.New("link target too long")

// Snapshot walks root and returns its manifest. The walk opens each component
// beneath a directory descriptor with O_NOFOLLOW, and checks each object with
// fstat against its O_PATH descriptor. It never follows a symlink, never
// descends across a device boundary, and never blocks on a FIFO. Any entry that
// cannot be read in full is recorded as a gap and as an unreadable entry, and
// the walk continues with its siblings. A root that is missing, is not a
// directory, or is a symlink is recorded as a gap, and the manifest has no
// entries.
func Snapshot(root, label string) Manifest {
	m := Manifest{Label: label}
	rfd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		kind := GapRootUnavailable
		if errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) {
			kind = GapRootNotDirectory
		}
		m.Gaps = append(m.Gaps, Gap{Manifest: label, Kind: kind, Detail: errClass(err)})
		return m.seal()
	}
	defer syscall.Close(rfd)
	return snapshotFD(rfd, label)
}

// SnapshotDir is Snapshot of an open directory: the descriptor is the root, so a caller that holds a directory
// (a generation root, for example) needs no path to it. The descriptor is not closed. Snapshot refuses a root
// that is a symlink; a descriptor is whatever the caller opened, and a non-directory is a root_not_directory gap.
func SnapshotDir(dir *os.File, label string) Manifest {
	if dir == nil {
		m := Manifest{Label: label, Gaps: []Gap{{Manifest: label, Kind: GapRootUnavailable}}}
		return m.seal()
	}
	// A new open file description of the same directory: the caller's own has a read offset that a listing it
	// already made left at the end.
	fd, err := syscall.Openat(int(dir.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		kind := GapRootUnavailable
		if errors.Is(err, syscall.ENOTDIR) {
			kind = GapRootNotDirectory
		}
		m := Manifest{Label: label, Gaps: []Gap{{Manifest: label, Kind: kind, Detail: errClass(err)}}}
		return m.seal()
	}
	defer syscall.Close(fd)
	return snapshotFD(fd, label)
}

func snapshotFD(rfd int, label string) Manifest {
	m := Manifest{Label: label}
	var st syscall.Stat_t
	if err := syscall.Fstat(rfd, &st); err != nil {
		m.Gaps = append(m.Gaps, Gap{Manifest: label, Kind: GapRootUnavailable, Detail: errClass(err)})
		return m.seal()
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		m.Gaps = append(m.Gaps, Gap{Manifest: label, Kind: GapRootNotDirectory})
		return m.seal()
	}
	// The root's own metadata is part of the protected state. It is read from the
	// descriptor, so it is the metadata of the directory the walk lists.
	m.Dev = st.Dev
	m.Root = &Entry{
		Type:  TypeDir,
		Mode:  uint32(st.Mode) & 07777,
		UID:   st.Uid,
		GID:   st.Gid,
		Nlink: uint64(st.Nlink),
		Ino:   st.Ino,
	}
	w := &walker{m: &m, rootDev: st.Dev}
	w.walkDir(rfd, "")
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	return m.seal()
}

// seal sets Complete from the gap list and returns a copy of the manifest.
func (m *Manifest) seal() Manifest {
	m.Complete = len(m.Gaps) == 0
	return *m
}

// walker holds the state of one walk. rootDev is the device of the root, and an
// entry on another device is not descended.
type walker struct {
	m       *Manifest
	rootDev uint64
}

// walkDir records the entries of the directory open at dirfd. rel is the
// directory's root-relative path, and it is empty for the root.
func (w *walker) walkDir(dirfd int, rel string) {
	names, err := listNames(dirfd)
	if err != nil {
		w.gap(rel, GapUnreadable, errClass(err))
		return
	}
	for _, name := range names {
		w.walkEntry(dirfd, name, joinRel(rel, name))
	}
}

// walkEntry classifies one child by an O_PATH descriptor and then records it.
func (w *walker) walkEntry(dirfd int, name, rel string) {
	pfd, err := syscall.Openat(dirfd, name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		w.failEntry(rel, err)
		return
	}
	var st syscall.Stat_t
	err = syscall.Fstat(pfd, &st)
	syscall.Close(pfd)
	if err != nil {
		w.failEntry(rel, err)
		return
	}
	if st.Dev != w.rootDev {
		w.gap(rel, GapMountBoundary, "")
		w.unreadable(rel)
		return
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		w.walkDirEntry(dirfd, name, rel, &st)
	case syscall.S_IFREG:
		w.walkFileEntry(dirfd, name, rel, &st)
	case syscall.S_IFLNK:
		w.walkLinkEntry(dirfd, name, rel, &st)
	default:
		w.record(rel, TypeOther, &st, 0, "", "")
	}
}

func (w *walker) walkDirEntry(dirfd int, name, rel string, st *syscall.Stat_t) {
	dfd, err := syscall.Openat(dirfd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		w.failEntry(rel, err)
		return
	}
	defer syscall.Close(dfd)
	if !sameObject(dfd, st) {
		w.gap(rel, GapSwapped, "identity_changed")
		w.unreadable(rel)
		return
	}
	w.record(rel, TypeDir, st, 0, "", "")
	w.walkDir(dfd, rel)
}

func (w *walker) walkFileEntry(dirfd int, name, rel string, st *syscall.Stat_t) {
	// O_NONBLOCK keeps a FIFO swapped in after the O_PATH check from blocking
	// the open. fstat on the opened descriptor then rejects it.
	rfd, err := syscall.Openat(dirfd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		w.failEntry(rel, err)
		return
	}
	defer syscall.Close(rfd)
	if !sameObject(rfd, st) {
		w.gap(rel, GapSwapped, "identity_changed")
		w.unreadable(rel)
		return
	}
	sum, n, err := hashFD(rfd)
	if err != nil {
		w.gap(rel, GapUnreadable, errClass(err))
		w.unreadable(rel)
		return
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(rfd, &after); err != nil {
		w.gap(rel, GapChangedDuringRead, errClass(err))
		w.unreadable(rel)
		return
	}
	if after.Size != n {
		w.gap(rel, GapChangedDuringRead, "size_mismatch")
		w.unreadable(rel)
		return
	}
	w.record(rel, TypeFile, st, n, sum, "")
}

func (w *walker) walkLinkEntry(dirfd int, name, rel string, st *syscall.Stat_t) {
	target, err := readlinkat(dirfd, name)
	if err != nil {
		w.failEntry(rel, err)
		return
	}
	// The link must still be the object that was statted before the readlink.
	chk, err := syscall.Openat(dirfd, name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		w.failEntry(rel, err)
		return
	}
	same := sameObject(chk, st)
	syscall.Close(chk)
	if !same {
		w.gap(rel, GapSwapped, "identity_changed")
		w.unreadable(rel)
		return
	}
	w.record(rel, TypeSymlink, st, 0, "", target)
}

// record appends a complete entry built from the statted object.
func (w *walker) record(rel, typ string, st *syscall.Stat_t, size int64, sum, target string) {
	w.m.Entries = append(w.m.Entries, Entry{
		Path:   rel,
		Type:   typ,
		Mode:   uint32(st.Mode) & 07777,
		UID:    st.Uid,
		GID:    st.Gid,
		Nlink:  uint64(st.Nlink),
		Ino:    st.Ino,
		Size:   size,
		SHA256: sum,
		Target: target,
	})
}

// failEntry records an entry that could not be read in full.
func (w *walker) failEntry(rel string, err error) {
	kind := GapUnreadable
	if errors.Is(err, syscall.ENOENT) {
		kind = GapVanished
	}
	w.gap(rel, kind, errClass(err))
	w.unreadable(rel)
}

func (w *walker) gap(rel, kind, detail string) {
	w.m.Gaps = append(w.m.Gaps, Gap{Manifest: w.m.Label, Kind: kind, Path: rel, Detail: detail})
}

func (w *walker) unreadable(rel string) {
	w.m.Entries = append(w.m.Entries, Entry{Path: rel, Type: TypeUnreadable})
}

// sameObject reports whether the open descriptor fd refers to the object that
// st describes. The device, inode, and file type must all match.
func sameObject(fd int, st *syscall.Stat_t) bool {
	var got syscall.Stat_t
	if err := syscall.Fstat(fd, &got); err != nil {
		return false
	}
	return got.Dev == st.Dev && got.Ino == st.Ino && got.Mode&syscall.S_IFMT == st.Mode&syscall.S_IFMT
}

// listNames returns the sorted names in the directory open at dirfd. It reads
// through a duplicate descriptor, so dirfd stays usable for openat.
func listNames(dirfd int) ([]string, error) {
	fd, err := syscall.Dup(dirfd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "")
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// hashFD returns the SHA-256 and byte count of everything readable from fd.
func hashFD(fd int) (string, int64, error) {
	h := sha256.New()
	buf := make([]byte, readChunk)
	var total int64
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return "", 0, err
		}
		if n == 0 {
			break
		}
		h.Write(buf[:n])
		total += int64(n)
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}

// readlinkat returns the target of the symlink name in the directory at dirfd.
// The target is never followed.
func readlinkat(dirfd int, name string) (string, error) {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return "", err
	}
	buf := make([]byte, maxLinkTarget)
	n, _, errno := syscall.Syscall6(syscall.SYS_READLINKAT, uintptr(dirfd), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if errno != 0 {
		return "", errno
	}
	if n >= uintptr(len(buf)) {
		return "", errTargetTooLong
	}
	return string(buf[:n]), nil
}

// errClass maps an error to a short class. The class never contains a path.
func errClass(err error) string {
	switch {
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return "permission"
	case errors.Is(err, syscall.ENOENT):
		return "not_found"
	case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
		return "not_directory_or_symlink"
	case errors.Is(err, errTargetTooLong):
		return "target_too_long"
	default:
		return "io_error"
	}
}

func joinRel(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}
