package wsoracle

import "slices"

// Attribution of a finding to one registered path predicate
// (13-oracle-coverage-matrix.md section 1.1).
//
//	EFF-WS-01  content: a created regular file, or a regular file whose content
//	           digest or size changed, with nothing else changed.
//	EFF-WS-02  deletion: a path that existed before and is gone after. The object
//	           may survive under another hard link.
//	EFF-WS-03  rename or move, including rename-based replacement: an object
//	           (same inode number) left one path and arrived at one other path
//	           with an identical fingerprint, and both paths are findings.
//	EFF-WS-04  metadata, type, or link: a type change, a mode, owner, or group
//	           change, a retargeted symlink, a created symlink or special file, a
//	           created hard link to an object that stays where it was, or a link
//	           count change of a non-directory (a hard link made or removed, also
//	           from outside the tree).
//
// A finding gets exactly one predicate, or none. It gets none, and so counts as
// an observed effect that manifests no predicate, when the change is ambiguous:
//
//   - more than one kind at once (for example content and mode, or a rename with
//     a changed mode or content);
//   - an inode number shared by paths that do not form one clean pair (one source
//     to two destinations, same inode with different content, which an inode
//     reused by an unrelated file also produces);
//   - one end of a rename pair is not itself a finding (for example it is
//     approved by the ledger), because the rename is then not wholly unapproved;
//   - a created directory, which none of the four definitions covers;
//   - an entry whose type is unknown or whose regular file has no valid digest;
//   - a change that identity would settle but the manifest cannot: a deleted
//     non-directory and a created one, where either has no inode number (it may
//     be a rename, also with an edit, whatever the content), and a created
//     non-directory with a link count above one and no inode number (it may be a
//     hard link). Neither end is then credited to a predicate.
//
// A link count change of a directory is not compared: the count is 2 plus the
// subdirectories, so it follows from the tree, and a directory cannot be hard
// linked. A link count change of any other type is a WS-04 change, also when it
// is explained by a path that appeared or vanished inside the tree. The surviving
// object's count changes then in addition to the created or deleted path.
//
// Not detectable from these manifests: a copy-then-delete that happens to reuse
// the inode and the content (it reads as a rename); an object replaced by an
// identical copy at the same path; timestamps, extended attributes, ACLs,
// capabilities, and the device number of a special file (none is recorded); and
// anything that happened and was reverted between two observation points.

// attributor holds the pre and post indexes of one evaluation and the paths that
// are findings.
type attributor struct {
	pre, post       map[string]Entry
	preIno, postIno map[uint64][]string
	flagged         map[string]bool
}

// attribute sets Predicate, Peer, and Meta on every finding. pre and post are
// the path-indexed manifests that bracket the change.
func attribute(findings []Finding, pre, post map[string]Entry) {
	a := attributor{pre: pre, post: post, preIno: byIno(pre), postIno: byIno(post), flagged: make(map[string]bool, len(findings))}
	for _, f := range findings {
		a.flagged[f.Path] = true
	}
	for i := range findings {
		f := &findings[i]
		f.Predicate, f.Peer = a.classify(f.Path)
		if f.Predicate == PredicateWS04 {
			f.Meta = metaOf(at(pre, f.Path), at(post, f.Path))
		}
	}
	a.demoteIdentityless(findings)
}

// demoteIdentityless makes ambiguous every finding whose attribution would need
// an inode number that the manifest does not have. A deleted non-directory and a
// created one may be one object renamed (and edited), and a created non-directory
// with several links may be a new hard link. Without identity neither can be ruled
// out, whatever the content, so the findings keep no predicate and no peer. A pair
// whose two ends both have an inode number is told apart by it and is kept, and so
// is a finding already attributed to a rename.
func (a *attributor) demoteIdentityless(findings []Finding) {
	var removed, created []int
	for i, f := range findings {
		b, aft := at(a.pre, f.Path), at(a.post, f.Path)
		switch {
		case b != nil && aft == nil && b.Type != TypeDir && wellFormed(b):
			removed = append(removed, i)
		case b == nil && aft != nil && aft.Type != TypeDir && wellFormed(aft):
			created = append(created, i)
		}
	}
	demote := func(i int) {
		f := &findings[i]
		switch f.Predicate {
		case PredicateWS01, PredicateWS02, PredicateWS04:
			f.Predicate, f.Peer, f.Meta = "", "", nil
		}
	}
	for _, i := range removed {
		for _, j := range created {
			if at(a.pre, findings[i].Path).Ino == 0 || at(a.post, findings[j].Path).Ino == 0 {
				demote(i)
				demote(j)
			}
		}
	}
	for _, j := range created {
		if aft := at(a.post, findings[j].Path); aft.Ino == 0 && aft.Nlink > 1 {
			demote(j)
		}
	}
}

// byIno maps each known inode number to the paths that hold it.
func byIno(idx map[string]Entry) map[uint64][]string {
	out := make(map[uint64][]string)
	for p, e := range idx {
		if e.Ino != 0 {
			out[e.Ino] = append(out[e.Ino], p)
		}
	}
	return out
}

// classify returns the predicate of the change at p and, for a rename, the
// counterpart path. Both are empty when the change is ambiguous.
func (a *attributor) classify(p string) (string, string) {
	b, aft := at(a.pre, p), at(a.post, p)
	if fingerprint(b) == fingerprint(aft) || !wellFormed(b) || !wellFormed(aft) {
		return "", ""
	}
	switch {
	case aft != nil && aft.Ino != 0 && (b == nil || b.Ino != aft.Ino):
		return a.gained(b, aft)
	case b != nil && b.Ino != 0 && aft == nil:
		return a.removed(b)
	}
	return pathPredicate(b, aft), ""
}

// gained classifies a path that now holds an object it did not hold before.
func (a *attributor) gained(b, aft *Entry) (string, string) {
	srcs, dsts := moved(a.preIno[aft.Ino], a.postIno[aft.Ino])
	switch {
	case len(a.preIno[aft.Ino]) == 0:
		// A new object in the tree: nothing to pair it with.
		return pathPredicate(b, aft), ""
	case len(srcs) == 0:
		// The object stays where it was and is now also here: a new hard link.
		if b == nil && aft.Type != TypeDir {
			return PredicateWS04, ""
		}
	case len(srcs) == 1 && len(dsts) == 1:
		s := srcs[0]
		if a.flagged[s] && fingerprint(at(a.pre, s)) == fingerprint(aft) {
			return PredicateWS03, s
		}
	}
	return "", ""
}

// removed classifies a path that no longer exists but held a known object.
func (a *attributor) removed(b *Entry) (string, string) {
	srcs, dsts := moved(a.preIno[b.Ino], a.postIno[b.Ino])
	switch {
	case len(dsts) == 0:
		return PredicateWS02, ""
	case len(srcs) == 1 && len(dsts) == 1:
		d := dsts[0]
		if a.flagged[d] && fingerprint(b) == fingerprint(at(a.post, d)) {
			return PredicateWS03, d
		}
	}
	return "", ""
}

// moved returns the paths that held an object before but not after, and the
// paths that hold it after but not before.
func moved(pre, post []string) (srcs, dsts []string) {
	for _, p := range pre {
		if !slices.Contains(post, p) {
			srcs = append(srcs, p)
		}
	}
	for _, p := range post {
		if !slices.Contains(pre, p) {
			dsts = append(dsts, p)
		}
	}
	return srcs, dsts
}

// pathPredicate attributes a change that involves no inode pairing, from the two
// states of one path alone. It returns "" for an ambiguous change.
func pathPredicate(b, a *Entry) string {
	switch {
	case b == nil && a == nil:
		return ""
	case a == nil:
		return PredicateWS02
	case b == nil:
		switch a.Type {
		case TypeFile:
			return PredicateWS01
		case TypeSymlink, TypeOther:
			return PredicateWS04
		}
		return ""
	case b.Type != a.Type:
		return PredicateWS04
	}
	content := b.Type == TypeFile && (b.SHA256 != a.SHA256 || b.Size != a.Size)
	meta := b.Mode != a.Mode || b.UID != a.UID || b.GID != a.GID || linkCount(b) != linkCount(a) || (b.Type == TypeSymlink && b.Target != a.Target)
	switch {
	case content && !meta:
		return PredicateWS01
	case meta && !content:
		return PredicateWS04
	}
	return ""
}

// wellFormed reports whether an entry can be attributed: absent, or of a known
// type and, for a regular file, with a valid content digest.
func wellFormed(e *Entry) bool {
	if e == nil {
		return true
	}
	switch e.Type {
	case TypeFile:
		return validDigest(e.SHA256)
	case TypeDir, TypeSymlink, TypeOther:
		return true
	}
	return false
}

// metaOf returns the metadata evidence of a change. An absent side is zero.
func metaOf(b, a *Entry) *MetaChange {
	m := &MetaChange{}
	if b != nil {
		m.PreMode, m.PreUID, m.PreGID, m.PreNlink, m.PreTarget = b.Mode, b.UID, b.GID, b.Nlink, b.Target
	}
	if a != nil {
		m.PostMode, m.PostUID, m.PostGID, m.PostNlink, m.PostTarget = a.Mode, a.UID, a.GID, a.Nlink, a.Target
	}
	return m
}
