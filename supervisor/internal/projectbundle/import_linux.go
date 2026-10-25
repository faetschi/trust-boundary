//go:build linux

package projectbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Import validates the whole bundle stream r and creates the project at dest. dest must not exist
// and its parent must be a private directory (owned by the caller, no group or other access). The
// project is written into a temporary sibling with modes 0600 (0700 for executables and
// directories), fsynced, and renamed into place only after the stream, every size and every digest
// were verified. Any error leaves nothing at dest. A crash can leave a ".tbound-import-*" sibling
// that no later import reuses.
func Import(r io.Reader, dest string, limits Limits) (Manifest, string, error) {
	if err := limits.validate(); err != nil {
		return Manifest{}, "", err
	}
	dest = filepath.Clean(dest)
	parent := filepath.Dir(dest)
	if err := checkPrivateParent(parent); err != nil {
		return Manifest{}, "", err
	}
	if _, err := os.Lstat(dest); err == nil {
		return Manifest{}, "", fmt.Errorf("projectbundle: destination %s already exists", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, "", err
	}
	staging, err := os.MkdirTemp(parent, ".tbound-import-")
	if err != nil {
		return Manifest{}, "", err
	}
	installed := false
	defer func() {
		if !installed {
			os.RemoveAll(staging)
		}
	}()

	headerBytes, err := readFramed(r, BundleFormat)
	if err != nil {
		return Manifest{}, "", err
	}
	manifest, err := ParseManifest(headerBytes, limits)
	if err != nil {
		return Manifest{}, "", err
	}
	dirs := []string{staging}
	for _, e := range manifest.Entries {
		full := filepath.Join(staging, filepath.FromSlash(e.Path))
		if e.Type == TypeDir {
			if err := os.Mkdir(full, 0o700); err != nil {
				return Manifest{}, "", err
			}
			if err := os.Chmod(full, 0o700); err != nil {
				return Manifest{}, "", err
			}
			dirs = append(dirs, full)
			continue
		}
		if err := importFile(r, full, e); err != nil {
			return Manifest{}, "", err
		}
	}
	if err := requireEOF(r); err != nil {
		return Manifest{}, "", err
	}
	for _, dir := range dirs {
		if err := syncDir(dir); err != nil {
			return Manifest{}, "", err
		}
	}
	if err := unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, dest, unix.RENAME_NOREPLACE); err != nil {
		return Manifest{}, "", &os.PathError{Op: "install", Path: dest, Err: err}
	}
	installed = true
	if err := syncDir(parent); err != nil {
		return Manifest{}, "", fmt.Errorf("projectbundle: %s was installed but the parent directory could not be synced: %w", dest, err)
	}
	return manifest, digestOf(headerBytes), nil
}

func importFile(r io.Reader, full string, e Entry) error {
	mode := os.FileMode(0o600)
	if e.Executable {
		mode = 0o700
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, e.Size))
	if err != nil {
		return err
	}
	if n != e.Size {
		return invalid("truncated stream: %q ends after %d of %d bytes", e.Path, n, e.Size)
	}
	if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return invalid("content of %q does not match its sha256", e.Path)
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	return f.Sync()
}

func checkPrivateParent(parent string) error {
	var st unix.Stat_t
	if err := unix.Lstat(parent, &st); err != nil {
		return fmt.Errorf("projectbundle: destination parent %s: %w", parent, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("projectbundle: destination parent %s is not a directory", parent)
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0o077 != 0 {
		return fmt.Errorf("projectbundle: destination parent %s must be owned by the caller with no group or other access", parent)
	}
	return nil
}
