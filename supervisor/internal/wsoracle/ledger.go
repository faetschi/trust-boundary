package wsoracle

import (
	"encoding/json"
	"strings"
)

// LedgerChange is one object change in an approved-delta ledger entry. Before
// and After are the object's state before and after the transition. A nil value
// means the path is absent. Entry.Path must equal Path when either is set.
type LedgerChange struct {
	Path   string `json:"path"`
	Before *Entry `json:"before"`
	After  *Entry `json:"after"`
}

// LedgerEntry is one approved transition, mirroring the fields that
// 02-process-model.md §5.4 requires of each ledger transition: an identity, an
// ordered sequence number, and the full changed-object list. Sequence must run
// 1, 2, 3, and so on. The caller supplies the ledger, and this package does not
// authenticate it.
type LedgerEntry struct {
	ID       string         `json:"id"`
	Sequence uint64         `json:"sequence"`
	Changes  []LedgerChange `json:"changes"`
}

// ledgerDigest returns the hex SHA-256 of the ledger's canonical JSON.
func ledgerDigest(ledger []LedgerEntry) string {
	b, err := json.Marshal(nonNil(ledger))
	if err != nil {
		// Only plain strings and integers are encoded, so this cannot happen.
		return ""
	}
	return sha256Hex(b)
}

// validRelPath reports whether p is a non-empty, slash-separated path with no
// empty, "." or ".." component, and no leading slash.
func validRelPath(p string) bool {
	if p == "" || strings.ContainsRune(p, 0) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// composeLedger returns the composed final state of every path that a ledger
// entry touches. A nil value in the map means the path is absent after the
// ledger. Each change's Before must equal the state composed so far, which is
// the baseline state until a ledger entry touches the path. On a malformed
// ledger it returns a short problem class and the offending path. The class is
// one of sequence, id, path, duplicate_path, entry_path, or before_mismatch.
func composeLedger(baseline *Manifest, ledger []LedgerEntry) (map[string]*Entry, string, string) {
	base := entryIndex(baseline)
	cur := make(map[string]*Entry)
	stateOf := func(p string) *Entry {
		if e, ok := cur[p]; ok {
			return e
		}
		return at(base, p)
	}
	seenIDs := make(map[string]bool, len(ledger))
	for i, entry := range ledger {
		if entry.Sequence != uint64(i+1) {
			return nil, "sequence", ""
		}
		if entry.ID == "" || seenIDs[entry.ID] {
			return nil, "id", ""
		}
		seenIDs[entry.ID] = true
		seenPaths := make(map[string]bool, len(entry.Changes))
		for _, c := range entry.Changes {
			if !validRelPath(c.Path) {
				return nil, "path", c.Path
			}
			if seenPaths[c.Path] {
				return nil, "duplicate_path", c.Path
			}
			seenPaths[c.Path] = true
			if (c.Before != nil && c.Before.Path != c.Path) || (c.After != nil && c.After.Path != c.Path) {
				return nil, "entry_path", c.Path
			}
			if fingerprint(c.Before) != fingerprint(stateOf(c.Path)) {
				return nil, "before_mismatch", c.Path
			}
		}
		// The entry is valid, so its changes apply together. Paths are distinct,
		// so the order of application does not matter.
		for _, c := range entry.Changes {
			if c.After == nil {
				cur[c.Path] = nil
				continue
			}
			after := *c.After
			cur[c.Path] = &after
		}
	}
	return cur, "", ""
}
