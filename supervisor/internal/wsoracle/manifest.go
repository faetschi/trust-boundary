package wsoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Entry describes one path in a manifest. Size is set only for regular files.
// Target is set only for symlinks. Nlink is recorded but is not compared. Ino is
// the inode number on the root's device (every recorded entry is on that device).
// It is zero when unknown. It is never encoded, so a manifest digest stays
// reproducible across roots, and a manifest decoded from JSON has no inodes. It
// never takes part in the equality key, and it is used only to pair a removed
// path with an added one as a rename or a hard link (see attribute.go).
// Timestamps, extended attributes, ACLs, capabilities, and device numbers of
// special files are not recorded.
type Entry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Nlink  uint64 `json:"nlink"`
	Ino    uint64 `json:"-"`
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
