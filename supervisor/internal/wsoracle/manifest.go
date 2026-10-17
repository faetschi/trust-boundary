package wsoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Entry describes one path in a manifest. Size is set only for regular files.
// Target is set only for symlinks. Nlink is recorded but is not compared. Inode
// numbers are never recorded.
type Entry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Nlink  uint64 `json:"nlink"`
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
// when the walk read every path in full and recorded no gap. The root identity
// (device and inode) is kept for comparison across manifests of one live tree.
// It is never encoded.
type Manifest struct {
	Label    string  `json:"label"`
	Complete bool    `json:"complete"`
	Entries  []Entry `json:"entries"`
	Gaps     []Gap   `json:"gaps"`

	dev uint64
	ino uint64
}

// Digest returns the hex SHA-256 of the manifest's label, completeness, entries,
// and gaps. The root identity is not included.
func (m *Manifest) Digest() string {
	c := *m
	c.Entries = nonNil(c.Entries)
	c.Gaps = nonNil(c.Gaps)
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

// entryIndex maps each path to its entry. Equal paths cannot occur in one
// manifest, so the last entry wins if a caller supplies duplicates.
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
// type, mode, owner, group, size (regular files only), content digest, and
// link target. Link count and inode identity take no part in it.
func fingerprint(e *Entry) string {
	if e == nil {
		return "absent"
	}
	var size int64
	if e.Type == TypeFile {
		size = e.Size
	}
	return fmt.Sprintf("%s|%o|%d|%d|%d|%s|%s", e.Type, e.Mode, e.UID, e.GID, size, e.SHA256, e.Target)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
