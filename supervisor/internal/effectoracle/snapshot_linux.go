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
	"syscall"
)

// modeBits are the mode bits recorded for each entry. The file type is recorded
// separately.
const modeBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// errNotRegular reports that a path expected to be a regular file is not one.
var errNotRegular = errors.New("effectoracle: not a regular file")

// snapshot records every entry below root, excluding root itself. Symlinks are
// recorded with their targets and are never followed. Regular files are hashed
// through O_NOFOLLOW and O_NONBLOCK, so a path swapped for a FIFO or a symlink
// mid-walk can neither block the oracle nor redirect the read. concurrent is set
// when an entry vanished during the walk. Entries appear in WalkDir order.
func snapshot(root string) (entries []Entry, concurrent bool, err error) {
	err = filepath.WalkDir(root, func(p string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p != root && errors.Is(walkErr, fs.ErrNotExist) {
				concurrent = true
				return nil
			}
			return walkErr
		}
		if p == root {
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				concurrent = true
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		e := Entry{Path: filepath.ToSlash(rel), Mode: uint32(info.Mode() & modeBits)}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			e.UID, e.GID, e.Nlink = st.Uid, st.Gid, uint64(st.Nlink)
		}
		switch {
		case info.IsDir():
			e.Type = "dir"
		case info.Mode().IsRegular():
			sum, err := hashRegular(p)
			if errors.Is(err, fs.ErrNotExist) {
				concurrent = true
				return nil
			}
			if err != nil {
				return err
			}
			e.Type, e.Size, e.SHA256 = "file", info.Size(), sum
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if errors.Is(err, fs.ErrNotExist) {
				concurrent = true
				return nil
			}
			if err != nil {
				return err
			}
			e.Type, e.Target = "symlink", target
		default:
			e.Type = "other"
		}
		entries = append(entries, e)
		return nil
	})
	return entries, concurrent, err
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
