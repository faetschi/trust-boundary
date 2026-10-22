// Package trialfix holds the workspace fixture of a corpus case: the spec that names the
// exact tree a trial starts from (tbound-fixture/v1), the digest the corpus manifest pins
// for it, and, on Linux, the in-place populate and wipe that `tbound trial reset` applies
// to the live root. The package has no build tag: the spec, its digest and the digest of a
// live snapshot are computed the same way on the host and on the guest, so that a populated
// tree digests equal to its spec.
//
// The digest is the wsoracle manifest digest of the manifest the spec implies. That
// manifest has the canonical root (owner 995:986, mode 0700) and every entry owned by the
// same owner; link counts are normalized (a directory's count follows from its
// subdirectories and is recorded as zero, a file's is one); the canonical fence directory
// (an empty top-level lost+found owned by the fixture owner with mode 0755) is left out. The
// spec carries no symlink, hard link, extended attribute or special file, and no ownership:
// ownership is implicit.
package trialfix

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"tbound/supervisor/internal/wsoracle"
)

// Schema is the only accepted value of Spec.Schema.
const Schema = "tbound-fixture/v1"

// Entry types of a spec. A spec has no other type.
const (
	TypeFile = "file"
	TypeDir  = "dir"
)

// The canonical owner of a fixture and its root. They are the cell uid and gid of the
// guest; a test may use another Owner with DigestOwned and Populate.
const (
	OwnerUID uint32 = 995
	OwnerGID uint32 = 986
	// RootMode is the mode of the root of a fixture tree.
	RootMode uint32 = 0o700
)

// LostFound is the top-level directory a filesystem creates in its root, and the fence directory
// the effect oracle opens (effectoracle.FenceDir). It is not part of a fixture: a spec may not
// name it, and a tree digest leaves out only the canonical empty fence directory.
const LostFound = "lost+found"

// Label is the label of the implied manifest, and of a live manifest after TreeDigest.
const Label = "fixture"

// Bounds of a spec.
const (
	MaxSpecBytes  = 16 << 20
	MaxEntries    = 4096
	MaxFileBytes  = 1 << 20
	MaxTotalBytes = 8 << 20
	MaxPathBytes  = 1024
	MaxNameBytes  = 255
	MaxDepth      = 32
)

var modeRE = regexp.MustCompile(`^0[0-7]{3}$`)

// Owner is the uid and gid a fixture tree is owned by.
type Owner struct {
	UID, GID uint32
}

// Canonical is the owner a fixture digest pins.
var Canonical = Owner{UID: OwnerUID, GID: OwnerGID}

// Entry is one path of a spec. Path is relative to the root and canonical. Mode is four
// octal digits ("0644"), permission bits only. ContentB64 is the standard base64 of a
// file's content and is absent for a directory.
type Entry struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Mode       string `json:"mode"`
	ContentB64 string `json:"content_b64,omitempty"`
}

// Spec is a fixture: the entries of a tree, in any order. Every directory has an entry of its
// own, so that every mode is stated.
type Spec struct {
	Schema  string  `json:"schema"`
	Entries []Entry `json:"entries"`
}

// File is a validated, decoded entry.
type File struct {
	Path    string
	Dir     bool
	Mode    uint32
	Content []byte
}

// Parse reads one spec from r with the strict rules of Files: unknown fields, trailing data
// and anything over MaxSpecBytes are refused.
func Parse(r io.Reader) (Spec, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxSpecBytes+1))
	if err != nil {
		return Spec{}, fmt.Errorf("fixture spec: %w", err)
	}
	if len(raw) > MaxSpecBytes {
		return Spec{}, fmt.Errorf("fixture spec: more than %d bytes", MaxSpecBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var spec Spec
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("fixture spec: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Spec{}, errors.New("fixture spec: data after the document")
	}
	if _, err := spec.Files(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// Files validates the spec and returns its entries decoded and sorted by path, so that a
// directory always precedes what it holds. It refuses a wrong schema, more than MaxEntries
// entries, a path that is not canonical (relative, no empty, "." or ".." component, no NUL,
// control character or backslash, bounded length and depth, valid UTF-8), a top-level
// lost+found, a duplicate, an entry whose parent is not a directory entry of the spec, a
// type other than file or dir, a mode that is not four octal permission digits, a directory
// without owner rwx, content on a directory, content that is not canonical base64, a file
// over MaxFileBytes and more than MaxTotalBytes of content.
func (s Spec) Files() ([]File, error) {
	if s.Schema != Schema {
		return nil, fmt.Errorf("fixture spec: schema %q, want %q", s.Schema, Schema)
	}
	if len(s.Entries) > MaxEntries {
		return nil, fmt.Errorf("fixture spec: %d entries, at most %d", len(s.Entries), MaxEntries)
	}
	files := make([]File, 0, len(s.Entries))
	isDir := make(map[string]bool, len(s.Entries))
	var total int
	for _, e := range s.Entries {
		if err := validPath(e.Path); err != nil {
			return nil, err
		}
		if _, dup := isDir[e.Path]; dup {
			return nil, fmt.Errorf("fixture spec: %q appears twice", e.Path)
		}
		f := File{Path: e.Path}
		switch e.Type {
		case TypeFile:
		case TypeDir:
			f.Dir = true
		default:
			return nil, fmt.Errorf("fixture spec: %q has type %q, want file or dir", e.Path, e.Type)
		}
		if !modeRE.MatchString(e.Mode) {
			return nil, fmt.Errorf("fixture spec: %q has mode %q, want four octal digits such as 0644", e.Path, e.Mode)
		}
		mode, _ := strconv.ParseUint(e.Mode, 8, 32)
		f.Mode = uint32(mode)
		if f.Dir {
			if f.Mode&0o700 != 0o700 {
				return nil, fmt.Errorf("fixture spec: directory %q has mode %s without owner rwx", e.Path, e.Mode)
			}
			if e.ContentB64 != "" {
				return nil, fmt.Errorf("fixture spec: directory %q has content", e.Path)
			}
		} else {
			content, err := base64.StdEncoding.DecodeString(e.ContentB64)
			if err != nil || base64.StdEncoding.EncodeToString(content) != e.ContentB64 {
				return nil, fmt.Errorf("fixture spec: %q has content that is not canonical base64", e.Path)
			}
			if len(content) > MaxFileBytes {
				return nil, fmt.Errorf("fixture spec: %q has %d bytes, at most %d", e.Path, len(content), MaxFileBytes)
			}
			if total += len(content); total > MaxTotalBytes {
				return nil, fmt.Errorf("fixture spec: more than %d bytes of content", MaxTotalBytes)
			}
			f.Content = content
		}
		isDir[e.Path] = f.Dir
		files = append(files, f)
	}
	for _, f := range files {
		if i := strings.LastIndexByte(f.Path, '/'); i >= 0 && !isDir[f.Path[:i]] {
			return nil, fmt.Errorf("fixture spec: the parent of %q is not a directory entry", f.Path)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func validPath(p string) error {
	bad := func(why string) error { return fmt.Errorf("fixture spec: path %q %s", p, why) }
	if p == "" || len(p) > MaxPathBytes || !utf8.ValidString(p) {
		return bad("is empty, too long or not UTF-8")
	}
	parts := strings.Split(p, "/")
	if len(parts) > MaxDepth {
		return bad("is too deep")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > MaxNameBytes {
			return bad("is not a canonical relative path")
		}
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return bad("has a control character or backslash")
		}
	}
	if parts[0] == LostFound {
		return bad("is under lost+found, which is not part of a fixture")
	}
	return nil
}

// Manifest is the manifest the spec implies for a tree owned by owner. Its root is owned by
// owner with mode RootMode. Every directory (the root included) has link count zero and
// every file one.
func (s Spec) Manifest(owner Owner) (wsoracle.Manifest, error) {
	files, err := s.Files()
	if err != nil {
		return wsoracle.Manifest{}, err
	}
	m := wsoracle.Manifest{Label: Label, Complete: true,
		Root:    &wsoracle.Entry{Type: wsoracle.TypeDir, Mode: RootMode, UID: owner.UID, GID: owner.GID},
		Entries: make([]wsoracle.Entry, 0, len(files))}
	for _, f := range files {
		e := wsoracle.Entry{Path: f.Path, Type: wsoracle.TypeDir, Mode: f.Mode, UID: owner.UID, GID: owner.GID}
		if !f.Dir {
			sum := sha256Hex(f.Content)
			e.Type, e.Nlink, e.Size, e.SHA256 = wsoracle.TypeFile, 1, int64(len(f.Content)), sum
		}
		m.Entries = append(m.Entries, e)
	}
	return m, nil
}

// Digest is the digest a corpus manifest pins for the spec: "sha256:" and the wsoracle
// digest of the implied manifest of the canonical owner.
func Digest(s Spec) (string, error) { return DigestOwned(s, Canonical) }

// DigestOwned is Digest for a tree owned by owner. Production uses only Digest; a test that
// cannot chown to the canonical owner compares a live tree against this.
func DigestOwned(s Spec, owner Owner) (string, error) {
	m, err := s.Manifest(owner)
	if err != nil {
		return "", err
	}
	return "sha256:" + m.Digest(), nil
}

// TreeDigest applies the canonicalization of Digest to a live snapshot, so that a tree
// populated from a spec digests equal to it: the label is the fixture label, the canonical
// fence directory (an empty lost+found owned by the fixture owner with mode FenceMode) is left
// out, the link count of a directory (the root included) is zeroed, and inode numbers and the
// device do not take part. A fence that is not canonical, and everything below it, stays in the
// digest: a write into the fence changes the tree. The owner, the modes and the content stay as
// observed, so the tree digests equal only if the root and every entry really have the fixture's
// owner and mode and nothing else is present. It refuses a manifest that is not complete and
// without gaps, or has no root.
func TreeDigest(live wsoracle.Manifest) (string, error) {
	if !live.Complete || len(live.Gaps) != 0 || live.Root == nil {
		return "", errors.New("the live snapshot is incomplete")
	}
	skipFence := canonicalFenceIn(live)
	m := wsoracle.Manifest{Label: Label, Complete: true, Gaps: nil}
	root := *live.Root
	root.Ino, root.Nlink = 0, 0
	m.Root = &root
	m.Entries = make([]wsoracle.Entry, 0, len(live.Entries))
	for _, e := range live.Entries {
		if skipFence && (e.Path == LostFound || strings.HasPrefix(e.Path, LostFound+"/")) {
			continue
		}
		e.Ino = 0
		if e.Type == wsoracle.TypeDir {
			e.Nlink = 0
		}
		m.Entries = append(m.Entries, e)
	}
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	return "sha256:" + m.Digest(), nil
}

// canonicalFenceIn reports whether the snapshot holds the canonical fence: a top-level lost+found
// that is a real directory of the root's owner with mode FenceMode and nothing below it.
func canonicalFenceIn(live wsoracle.Manifest) bool {
	canonical := false
	for _, e := range live.Entries {
		switch {
		case e.Path == LostFound:
			canonical = e.Type == wsoracle.TypeDir && e.Mode == FenceMode && e.UID == live.Root.UID && e.GID == live.Root.GID
		case strings.HasPrefix(e.Path, LostFound+"/"):
			return false
		}
	}
	return canonical
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
