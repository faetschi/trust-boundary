package wsoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

// Entry describes one path in a manifest. Size is set only for regular files.
// Target is set only for symlinks. Nlink is the link count. It is compared for
// every type except a directory, whose count is 2 plus its subdirectories and so
// follows from the tree itself (a directory cannot be hard-linked).
//
// Ino is the inode number on the root's device (every recorded entry is on that
// device, so Manifest.Dev and Ino together are the object identity). It is zero
// when unknown. It is encoded, so a manifest decoded from JSON keeps the identity
// that rename and hard-link attribution need, but Manifest.Digest leaves it out
// so that a content digest stays reproducible across roots; Manifest.IdentityDigest
// covers it. Ino never takes part in the equality key (see fingerprint). Timestamps,
// extended attributes, ACLs, capabilities, and device numbers of special files are
// not recorded.
type Entry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Nlink  uint64 `json:"nlink"`
	Ino    uint64 `json:"ino,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"`
}

// Gap is an explicit incompleteness record. Path is root-relative and empty for
// the root itself. Manifest names the manifest or input the gap belongs to.
// Detail is a short error class, never a path.
type Gap struct {
	Manifest string `json:"manifest"`
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Detail   string `json:"detail,omitempty"`
}

// Manifest is one observation of a tree at a named point. Complete is true only
// when the walk read every path in full and recorded no gap.
//
// Root is the root directory's own metadata (type, mode, owner, group, link
// count, inode), with an empty Path. It is part of the protected state: the
// entries describe what is inside the root, and Root describes the root. Dev is
// the root's device. Dev and Root.Ino are the live-root identity, compared across
// manifests of one live tree.
type Manifest struct {
	Label    string  `json:"label"`
	Complete bool    `json:"complete"`
	Root     *Entry  `json:"root,omitempty"`
	Dev      uint64  `json:"dev,omitempty"`
	Entries  []Entry `json:"entries"`
	Gaps     []Gap   `json:"gaps"`
}

// rootIno returns the root's inode number, or zero when Root is not recorded.
func (m *Manifest) rootIno() uint64 {
	if m.Root == nil {
		return 0
	}
	return m.Root.Ino
}

// Digest returns the hex SHA-256 of the manifest's label, completeness, root
// metadata, entries, and gaps. It leaves out object identity (the device and
// every inode number), so the digest of the same content is reproducible across
// roots and across a rebuilt tree. IdentityDigest covers the identity.
func (m *Manifest) Digest() string { return m.digest(false) }

// IdentityDigest returns the hex SHA-256 of everything Digest covers plus the
// device and every inode number.
func (m *Manifest) IdentityDigest() string { return m.digest(true) }

func (m *Manifest) digest(identity bool) string {
	c := *m
	c.Entries = nonNil(slices.Clone(c.Entries))
	c.Gaps = nonNil(c.Gaps)
	if !identity {
		c.Dev = 0
		for i := range c.Entries {
			c.Entries[i].Ino = 0
		}
		if c.Root != nil {
			r := *c.Root
			r.Ino = 0
			c.Root = &r
		}
	}
	b, err := json.Marshal(&c)
	if err != nil {
		// Only plain strings and integers are encoded, so this cannot happen.
		return ""
	}
	return sha256Hex(b)
}

// clean reports whether the manifest is complete with no gap.
func (m *Manifest) clean() bool {
	return m.Complete && len(m.Gaps) == 0
}

// entryIndex maps each path to its entry. It is called only after checkEntries
// has recorded a gap for every duplicate, so a duplicate never decides a verdict.
func entryIndex(m *Manifest) map[string]Entry {
	idx := make(map[string]Entry, len(m.Entries))
	for _, e := range m.Entries {
		idx[e.Path] = e
	}
	return idx
}

// at returns a pointer to a copy of the entry at p, or nil when p is absent.
func at(idx map[string]Entry, p string) *Entry {
	e, ok := idx[p]
	if !ok {
		return nil
	}
	return &e
}

// fingerprint is the equality key for an entry, or "absent" for nil. It covers
// type, mode, owner, group, size (regular files only), content digest, link
// target, and link count (every type except a directory, see Entry). Inode
// identity takes no part in it.
func fingerprint(e *Entry) string {
	if e == nil {
		return "absent"
	}
	var size int64
	if e.Type == TypeFile {
		size = e.Size
	}
	return fmt.Sprintf("%s|%o|%d|%d|%d|%s|%s|%d", e.Type, e.Mode, e.UID, e.GID, size, e.SHA256, e.Target, linkCount(e))
}

// linkCount is the link count that takes part in comparison: zero for a
// directory, whose count is derived from its subdirectories.
func linkCount(e *Entry) uint64 {
	if e.Type == TypeDir {
		return 0
	}
	return e.Nlink
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkEntries returns an invalid_entry gap for each entry that cannot be
// trusted: a path that is not canonical (see validRelPath), a path already seen
// in this manifest, a type that is not file, dir, symlink, or other (this
// includes "unreadable"), or a regular file without a SHA-256 content digest.
// A caller can mark a manifest Complete with no gap and still supply such
// entries, so completeness alone is not trusted. Detail is a short class, never
// a path.
func checkEntries(m *Manifest) []Gap {
	var gaps []Gap
	seen := make(map[string]bool, len(m.Entries))
	bad := func(e Entry, class string) {
		gaps = append(gaps, Gap{Manifest: m.Label, Kind: GapInvalidEntry, Path: e.Path, Detail: class})
	}
	for _, e := range m.Entries {
		switch {
		case !validRelPath(e.Path):
			bad(e, "path")
		case seen[e.Path]:
			bad(e, "duplicate_path")
		case e.Type != TypeFile && e.Type != TypeDir && e.Type != TypeSymlink && e.Type != TypeOther:
			bad(e, "type")
		case e.Type == TypeFile && !validDigest(e.SHA256):
			bad(e, "digest")
		}
		seen[e.Path] = true
	}
	return gaps
}

// validDigest reports whether s is a 64-character lowercase hex SHA-256.
func validDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == hex.EncodeToString(b)
}
