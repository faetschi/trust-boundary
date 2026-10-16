//go:build linux

package effectoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// modeBits are the mode bits recorded for each entry. The file type is recorded
// separately.
const modeBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// errNotRegular reports that a path expected to be a regular file is not one.
var errNotRegular = errors.New("effectoracle: not a regular file")

// unreadablePath is a path whose state the snapshot could not record in full.
type unreadablePath struct {
	path string // slash-relative
	err  error
}

// snapshotResult is the outcome of one walk. Entries appear in pre-order, with
// names sorted within each directory.
type snapshotResult struct {
	entries    []Entry
	concurrent bool
	unreadable []unreadablePath
}

// snapshot records every entry below root, excluding root itself. Symlinks are
// recorded with their targets and are never followed. Directories and regular
// files are opened with O_NOFOLLOW, and each directory is read through its own
// descriptor, so a path swapped for a symlink, a FIFO, or a file between its
// Lstat and its open is refused rather than followed or blocked on. Such a path,
// and any path that cannot be read, is recorded as an entry of type "unreadable"
// and listed in unreadable, and the walk goes on. concurrent is set when an
// entry vanished during the walk. The error is set only when root itself cannot
// be opened or read. beforeOpen, when non-nil, runs after each path is lstat'd
// and before it is opened; tests use it to swap paths mid-walk.
//
// A swap of an ancestor directory is not detected. Only the final component of
// each path is protected by O_NOFOLLOW.
func snapshot(root string, beforeOpen func(p string)) (snapshotResult, error) {
	var res snapshotResult
	f, err := openDir(root)
	if err != nil {
		return res, err
	}
	names, err := dirNames(f)
	_ = f.Close()
	if err != nil {
		return res, err
	}
	w := &walker{beforeOpen: beforeOpen, res: &res}
	w.children(root, "", names)
	return res, nil
}

// walker carries the state of one snapshot walk.
type walker struct {
	beforeOpen func(p string)
	res        *snapshotResult
}

// children records the named entries of the directory at abs, whose
// slash-relative path is rel ("" for the root). A subdirectory is recorded and
// then walked through its own descriptor.
func (w *walker) children(abs, rel string, names []string) {
	for _, name := range names {
		p := filepath.Join(abs, name)
		cr := name
		if rel != "" {
			cr = rel + "/" + name
		}
		info, err := os.Lstat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				w.res.concurrent = true
				continue
			}
			w.fail(Entry{Path: cr}, cr, err)
			continue
		}
		e := Entry{Path: cr, Mode: uint32(info.Mode() & modeBits)}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			e.UID, e.GID, e.Nlink = st.Uid, st.Gid, uint64(st.Nlink)
		}
		if w.beforeOpen != nil {
			w.beforeOpen(p)
		}
		switch {
		case info.IsDir():
			f, err := openDir(p)
			if errors.Is(err, fs.ErrNotExist) {
				w.res.concurrent = true
				continue
			}
			if err != nil {
				w.fail(e, cr, err)
				continue
			}
			sub, err := dirNames(f)
			_ = f.Close()
			e.Type = "dir"
			w.res.entries = append(w.res.entries, e)
			if err != nil {
				w.gap(cr, err)
				continue
			}
			w.children(p, cr, sub)
			continue
		case info.Mode().IsRegular():
			sum, err := hashRegular(p)
			if errors.Is(err, fs.ErrNotExist) {
				w.res.concurrent = true
				continue
			}
			if err != nil {
				w.fail(e, cr, err)
				continue
			}
			e.Type, e.Size, e.SHA256 = "file", info.Size(), sum
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if errors.Is(err, fs.ErrNotExist) {
				w.res.concurrent = true
				continue
			}
			if err != nil {
				w.fail(e, cr, err)
				continue
			}
			e.Type, e.Target = "symlink", target
		default:
			e.Type = "other"
		}
		w.res.entries = append(w.res.entries, e)
	}
}

// fail records an entry whose state could not be read in full.
func (w *walker) fail(e Entry, rel string, err error) {
	e.Type = "unreadable"
	w.res.entries = append(w.res.entries, e)
	w.gap(rel, err)
}

// gap lists a path whose contents or state the snapshot does not cover.
func (w *walker) gap(rel string, err error) {
	w.res.unreadable = append(w.res.unreadable, unreadablePath{path: rel, err: err})
}

// openDir opens p as a directory. A symlink at p is refused, not followed.
func openDir(p string) (*os.File, error) {
	fd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: p, Err: err}
	}
	return os.NewFile(uintptr(fd), p), nil
}

// dirNames returns the sorted names in an open directory, excluding . and ..
func dirNames(f *os.File) ([]string, error) {
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// listNames returns the sorted names in the directory at p. A symlink at p is
// refused.
func listNames(p string) ([]string, error) {
	f, err := openDir(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return dirNames(f)
}

// hashRegular returns the SHA-256 of a regular file. The file is opened without
// following symlinks and without blocking on a FIFO, and it is re-checked after
// opening.
func hashRegular(p string) (string, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errNotRegular
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// errnoName names the cause of err without a path, so that a report does not
// depend on where the root lives.
func errnoName(err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		return en.Error()
	}
	if errors.Is(err, errNotRegular) {
		return errNotRegular.Error()
	}
	return "unknown error"
}
