//go:build linux

package projectbundle

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type BuildOptions struct {
	// Exclude lists canonical relative paths to leave out, matched by whole path components.
	Exclude []string
	Limits  Limits
}

// BuildResult is what the client shows before it sends a bundle.
type BuildResult struct {
	Manifest Manifest
	Digest   string
	Included []string
	Excluded []string
}

// Build scans the original project at root and writes a tbound-project-bundle/v1 stream to w. It
// runs as the user, never follows symlinks and writes nothing to w when it refuses the project. If a
// file changes while its content is copied, the error is returned and w holds a partial stream that
// the caller must discard.
func Build(root string, opts BuildOptions, w io.Writer) (BuildResult, error) {
	manifest, excluded, err := scanTree(root, scanOptions{Exclude: opts.Exclude, Limits: opts.Limits})
	if err != nil {
		return BuildResult{}, err
	}
	header, err := manifest.MarshalCanonical()
	if err != nil {
		return BuildResult{}, err
	}
	if err := writeFramed(w, BundleFormat, header); err != nil {
		return BuildResult{}, err
	}
	if err := writeContents(root, manifest.Entries, w); err != nil {
		return BuildResult{}, err
	}
	included := make([]string, len(manifest.Entries))
	for i, e := range manifest.Entries {
		included[i] = e.Path
	}
	return BuildResult{Manifest: manifest, Digest: digestOf(header), Included: included, Excluded: excluded}, nil
}

// WriteNewFile creates path with mode 0600 through a temporary sibling that is fsynced and then
// linked into place without replacing anything. A failure leaves nothing at path.
func WriteNewFile(path string, fill func(io.Writer) error) error {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(suffix[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		os.Remove(tmp)
	}()
	buffered := bufio.NewWriter(f)
	if err := fill(buffered); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, tmp, unix.AT_FDCWD, path, unix.RENAME_NOREPLACE); err != nil {
		return &os.PathError{Op: "create", Path: path, Err: err}
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
