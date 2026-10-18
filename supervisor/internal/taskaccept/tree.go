package taskaccept

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"tbound/supervisor/internal/trialfix"
)

// Bounds of a tree read from a file system.
const (
	// MaxTreeEntries bounds the entries of a post-run tree; a larger tree is an error.
	MaxTreeEntries = 1 << 16
	// MaxReadBytes bounds the bytes read of one file by a check.
	MaxReadBytes = 8 << 20
)

// node is one path of a tree. Content is held in memory unless fsys is set.
type node struct {
	mode    fs.FileMode // type bits and permission bits as the file system reports them
	size    int64
	content []byte
	fsys    fs.FS
	name    string
}

// tree maps every path below the root (not the root itself) to its node.
type tree map[string]node

func (n node) open() (io.ReadCloser, error) {
	if n.fsys == nil {
		return io.NopCloser(bytes.NewReader(n.content)), nil
	}
	return n.fsys.Open(n.name)
}

// read returns the content of a regular file of at most limit bytes.
func (n node) read(limit int64) ([]byte, error) {
	if !n.mode.IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if n.size > limit {
		return nil, fmt.Errorf("%d bytes, more than the %d a check reads", n.size, limit)
	}
	f, err := n.open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("more than the %d bytes a check reads", limit)
	}
	return data, nil
}

// treeOfFS snapshots fsys without following symbolic links. A top-level lost+found, which a
// filesystem creates, is left out as in a fixture digest.
func treeOfFS(fsys fs.FS) (tree, error) {
	t := tree{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if p == trialfix.LostFound {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if len(t) >= MaxTreeEntries {
			return fmt.Errorf("the tree has more than %d entries", MaxTreeEntries)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		t[p] = node{mode: info.Mode(), size: info.Size(), fsys: fsys, name: p}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the post-run tree: %w", err)
	}
	return t, nil
}

// treeOfFixture is the tree a fixture spec names.
func treeOfFixture(spec trialfix.Spec) (tree, error) {
	files, err := spec.Files()
	if err != nil {
		return nil, err
	}
	t := make(tree, len(files))
	for _, f := range files {
		n := node{mode: fs.FileMode(f.Mode), size: int64(len(f.Content)), content: f.Content}
		if f.Dir {
			n.mode |= fs.ModeDir
		}
		t[f.Path] = n
	}
	return t, nil
}
