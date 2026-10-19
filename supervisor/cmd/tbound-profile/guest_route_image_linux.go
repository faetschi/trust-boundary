//go:build linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"tbound/supervisor/internal/guestevidence"
)

// The baseline of the direct-runtime artifact: what the image the probe cell ran shows of the registered
// target. It is read from the image itself and not from any directory the operator names. The image is the
// local OCI layout the cell template names (OCILayoutPath, the layout the service verifies with cosign):
// the producer finds the manifest whose digest is the image digest of the TCB proof, hashes the manifest and
// every layer it names, and applies the layers in order (whiteouts and opaque directories included) to a tree
// of tar headers. Nothing is extracted and no file of the image is run. A layout that does not hold the
// proof's image, or whose blobs do not hash to their names, gives no baseline. Every layer is also content
// addressed, so the custody of the layout does not matter: only the bytes the digest names count.
//
// From the tree the producer resolves the target the way the kernel would in the cell (symlinks followed
// within the image, at most 40), and records the target and every directory the resolution passed through,
// with owner and mode, for the cell identity to be judged under ordinary file permissions. The cell's
// Landlock confinement is the thing under test, so it plays no part here.

const (
	maxOCIManifestBytes = 1 << 20
	maxOCILayers        = 256
	maxLayerBytes       = 2 << 30
	maxImageTreeBytes   = 8 << 30
	maxImageEntries     = 2_000_000
	maxSymlinkHops      = 40
)

type imageNode struct {
	kind     byte // tar type flag: tar.TypeReg, tar.TypeDir, tar.TypeSymlink, or another type
	mode     uint32
	uid, gid uint32
	size     int64
	link     string
}

type imageTree map[string]imageNode

// remove deletes p and everything below it.
func (t imageTree) remove(p string) {
	delete(t, p)
	prefix := p + "/"
	if p == "/" {
		prefix = "/"
	}
	for k := range t {
		if k != "/" && strings.HasPrefix(k, prefix) {
			delete(t, k)
		}
	}
}

// imageBaseline reads the baseline of target from the image with manifest digest imageDigest in the OCI layout.
func imageBaseline(layout, imageDigest, target string, cellUID, cellGID int) (guestevidence.RouteTarget, error) {
	var fact guestevidence.RouteTarget
	if !guestevidence.IsDigest(imageDigest) || !path.IsAbs(target) || path.Clean(target) != target || cellUID <= 0 || cellGID < 0 {
		return fact, errors.New("image baseline: the image digest, the target or the cell identity is not valid")
	}
	if !filepath.IsAbs(layout) {
		return fact, fmt.Errorf("the OCI layout %q is not an absolute path", layout)
	}
	layers, err := imageLayers(layout, imageDigest)
	if err != nil {
		return fact, err
	}
	tree := imageTree{}
	entries := 0
	for _, digest := range layers {
		if err := applyLayer(tree, layout, digest, &entries); err != nil {
			return fact, fmt.Errorf("layer %s: %w", digest, err)
		}
	}
	resolved, dirs, node, err := tree.resolve(target)
	if err != nil {
		return fact, fmt.Errorf("the target %s is not in the image %s: %w", target, imageDigest, err)
	}
	if node.kind != tar.TypeReg {
		return fact, fmt.Errorf("the target %s resolves to %s, which is not a regular file of the image", target, resolved)
	}
	fact = guestevidence.RouteTarget{Regular: true, Mode: node.mode & 0o777, UID: node.uid, GID: node.gid, Size: node.size, ImageDigest: imageDigest,
		CellUID: uint32(cellUID), CellGID: uint32(cellGID), ResolvedPath: resolved}
	for _, dir := range dirs {
		n, ok := tree.dirNode(dir)
		if !ok {
			return fact, fmt.Errorf("the directory %s above the target has no entry in the image: its permissions are unknown", dir)
		}
		fact.Ancestors = append(fact.Ancestors, guestevidence.RouteAncestor{Path: dir, Mode: n.mode & 0o777, UID: n.uid, GID: n.gid})
		if dir == path.Dir(resolved) {
			fact.ParentMode = n.mode & 0o777
		}
	}
	return fact, nil
}

// dirNode is the directory entry for p. The root of an image that has no entry for it is root-owned 0755.
func (t imageTree) dirNode(p string) (imageNode, bool) {
	if n, ok := t[p]; ok && n.kind == tar.TypeDir {
		return n, true
	}
	if p == "/" {
		return imageNode{kind: tar.TypeDir, mode: 0o755}, true
	}
	return imageNode{}, false
}

// resolve follows target through the tree. It returns the resolved path, the directories searched on the way
// (every directory each component was looked up in, the directories symlinks lead through included, sorted by
// first visit) and the final node.
func (t imageTree) resolve(target string) (string, []string, imageNode, error) {
	var dirs []string
	seen := map[string]bool{}
	visit := func(d string) {
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	pending := strings.Split(strings.TrimPrefix(target, "/"), "/")
	cur := "/"
	hops := 0
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		if name == "" || name == "." {
			continue
		}
		if name == ".." {
			cur = path.Dir(cur)
			continue
		}
		if _, ok := t.dirNode(cur); !ok {
			return "", nil, imageNode{}, fmt.Errorf("%s is not a directory", cur)
		}
		visit(cur)
		next := path.Join(cur, name)
		n, ok := t[next]
		if !ok {
			return "", nil, imageNode{}, fmt.Errorf("%s does not exist", next)
		}
		if n.kind == tar.TypeSymlink {
			hops++
			if hops > maxSymlinkHops {
				return "", nil, imageNode{}, errors.New("too many levels of symbolic links")
			}
			if strings.HasPrefix(n.link, "/") {
				cur = "/"
			}
			pending = append(strings.Split(n.link, "/"), pending...)
			continue
		}
		if len(pending) == 0 {
			return next, dirs, n, nil
		}
		cur = next
	}
	return "", nil, imageNode{}, errors.New("the path names a directory")
}

// openBlob opens a blob of the layout as a regular file, without following a symbolic link.
func openBlob(layout, digest string) (*os.File, error) {
	if !guestevidence.IsDigest(digest) {
		return nil, fmt.Errorf("blob digest %q is not valid", digest)
	}
	p := filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("blob %s is not a regular file", digest)
	}
	return f, nil
}

// imageLayers finds the manifest of the image in the layout, checks that it hashes to imageDigest and returns
// the digests of its layers in order.
func imageLayers(layout, imageDigest string) ([]string, error) {
	index, err := readLayoutFile(filepath.Join(layout, "index.json"))
	if err != nil {
		return nil, fmt.Errorf("OCI index: %w", err)
	}
	var idx struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(index, &idx); err != nil {
		return nil, fmt.Errorf("OCI index: %w", err)
	}
	listed := false
	for _, m := range idx.Manifests {
		if m.Digest == imageDigest {
			listed = true
		}
	}
	if !listed {
		return nil, fmt.Errorf("the OCI layout %s does not hold the probed image %s", layout, imageDigest)
	}
	f, err := openBlob(layout, imageDigest)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", imageDigest, err)
	}
	defer f.Close()
	manifest, err := io.ReadAll(io.LimitReader(f, maxOCIManifestBytes+1))
	if err != nil || len(manifest) > maxOCIManifestBytes {
		return nil, errors.New("the image manifest is unreadable or too large")
	}
	sum := sha256.Sum256(manifest)
	if "sha256:"+hex.EncodeToString(sum[:]) != imageDigest {
		return nil, fmt.Errorf("the manifest blob does not hash to %s", imageDigest)
	}
	var doc struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(manifest, &doc); err != nil || len(doc.Layers) == 0 || len(doc.Layers) > maxOCILayers {
		return nil, fmt.Errorf("the image manifest has no layers or too many: %v", err)
	}
	if !guestevidence.IsDigest(doc.Config.Digest) {
		return nil, fmt.Errorf("config digest %q is not valid", doc.Config.Digest)
	}
	if err := verifyBlob(layout, doc.Config.Digest); err != nil {
		return nil, fmt.Errorf("image config %s: %w", doc.Config.Digest, err)
	}
	var out []string
	for _, l := range doc.Layers {
		if !guestevidence.IsDigest(l.Digest) {
			return nil, fmt.Errorf("layer digest %q is not valid", l.Digest)
		}
		out = append(out, l.Digest)
	}
	return out, nil
}

func readLayoutFile(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOCIManifestBytes+1))
	if err != nil || len(data) > maxOCIManifestBytes {
		return nil, fmt.Errorf("%s is unreadable or too large", p)
	}
	return data, nil
}

type hashingReader struct {
	r io.Reader
	h hash.Hash
}

func (h *hashingReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	return n, err
}

// applyLayer reads one layer blob (a tar, gzip-compressed or not) into the tree and requires the blob to
// hash to its digest.
func applyLayer(tree imageTree, layout, digest string, entries *int) error {
	f, err := openBlob(layout, digest)
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() > maxLayerBytes {
		return errors.New("the layer is larger than the bound")
	}
	raw := &hashingReader{r: f, h: sha256.New()}
	head := make([]byte, 4)
	n, _ := io.ReadFull(raw, head)
	body := io.MultiReader(bytes.NewReader(head[:n]), raw)
	var stream io.Reader = body
	switch {
	case n >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		gz, err := gzip.NewReader(body)
		if err != nil {
			return err
		}
		defer gz.Close()
		stream = gz
	case n == 4 && head[0] == 0x28 && head[1] == 0xb5 && head[2] == 0x2f && head[3] == 0xfd:
		return errors.New("zstd layers are not supported")
	}
	tr := tar.NewReader(io.LimitReader(stream, maxImageTreeBytes))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		*entries++
		if *entries > maxImageEntries {
			return errors.New("too many entries in the image")
		}
		if err := applyEntry(tree, hdr); err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, raw); err != nil {
		return err
	}
	if got := "sha256:" + hex.EncodeToString(raw.h.Sum(nil)); got != digest {
		return fmt.Errorf("the layer blob hashes to %s", got)
	}
	return nil
}

func applyEntry(tree imageTree, hdr *tar.Header) error {
	p := path.Clean("/" + hdr.Name)
	dir, base := path.Split(p)
	dir = path.Clean(dir)
	switch {
	case base == ".wh..wh..opq":
		for k := range tree {
			if k != dir && (dir == "/" || strings.HasPrefix(k, dir+"/")) && k != "/" {
				delete(tree, k)
			}
		}
		return nil
	case strings.HasPrefix(base, ".wh."):
		tree.remove(path.Join(dir, strings.TrimPrefix(base, ".wh.")))
		return nil
	}
	node := imageNode{kind: hdr.Typeflag, mode: uint32(hdr.Mode) & 0o7777, uid: uint32(hdr.Uid), gid: uint32(hdr.Gid), size: hdr.Size, link: hdr.Linkname}
	if hdr.Typeflag == tar.TypeLink {
		target, ok := tree[path.Clean("/"+hdr.Linkname)]
		if !ok {
			return fmt.Errorf("hard link %s names %s, which is not in the image", p, hdr.Linkname)
		}
		node = target
	}
	if node.kind != tar.TypeDir {
		tree.remove(p)
	}
	tree[p] = node
	return nil
}

// verifyBlob requires the blob to exist as a regular file and to hash to its digest.
func verifyBlob(layout, digest string) error {
	f, err := openBlob(layout, digest)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxLayerBytes+1)); err != nil {
		return err
	}
	if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != digest {
		return fmt.Errorf("the blob hashes to %s", got)
	}
	return nil
}
