package wsoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
)

// digestOf is the SHA-256 of a file's content, matching fileEntry.
func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func fileEntry(path, content string) Entry {
	return Entry{Path: path, Type: TypeFile, Mode: 0o644, UID: 1000, GID: 1000, Nlink: 1, Size: int64(len(content)), SHA256: digestOf(content)}
}

func dirEntry(path string) Entry {
	return Entry{Path: path, Type: TypeDir, Mode: 0o755, UID: 1000, GID: 1000, Nlink: 2}
}

// rootEntry is the metadata of a well-formed protected root.
func rootEntry() *Entry {
	return &Entry{Type: TypeDir, Mode: 0o755, UID: 1000, GID: 1000, Nlink: 2, Ino: 1}
}

// manifest returns a complete manifest with a well-formed root.
func manifest(label string, entries ...Entry) Manifest {
	return Manifest{Label: label, Complete: true, Root: rootEntry(), Dev: 1, Entries: entries}
}

// withRootID returns a copy of m whose live-root identity is (dev, ino).
func withRootID(m Manifest, dev, ino uint64) Manifest {
	r := rootEntry()
	if m.Root != nil {
		c := *m.Root
		r = &c
	}
	r.Ino = ino
	m.Dev, m.Root = dev, r
	return m
}

func entryPtr(e Entry) *Entry { return &e }

func approve(id string, seq uint64, changes ...LedgerChange) LedgerEntry {
	return LedgerEntry{ID: id, Sequence: seq, Changes: changes}
}

func wantVerdict(t *testing.T, r Report, want string) {
	t.Helper()
	if r.Verdict != want {
		t.Fatalf("verdict = %q, want %q (gaps=%+v findings=%+v)", r.Verdict, want, r.Gaps, r.Findings)
	}
}

func wantSingleFinding(t *testing.T, r Report, path, change, reason string) {
	t.Helper()
	if len(r.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one for %s", r.Findings, path)
	}
	f := r.Findings[0]
	if f.Path != path || f.Change != change || f.Reason != reason {
		t.Fatalf("finding = %+v, want path=%s change=%s reason=%s", f, path, change, reason)
	}
}

func wantNoFindings(t *testing.T, r Report) {
	t.Helper()
	if len(r.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", r.Findings)
	}
}

func wantUnattributed(t *testing.T, r Report, paths ...string) {
	t.Helper()
	if !slices.Equal(r.Unattributed, paths) {
		t.Fatalf("unattributed = %v, want %v", r.Unattributed, paths)
	}
}

func wantGap(t *testing.T, r Report, kind, manifestLabel string) {
	t.Helper()
	for _, g := range r.Gaps {
		if g.Kind == kind && g.Manifest == manifestLabel {
			return
		}
	}
	t.Fatalf("no gap kind=%s manifest=%s in %+v", kind, manifestLabel, r.Gaps)
}

func TestWS05UnattributedCreateReachingLiveIsAnEffect(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	rogue := fileEntry("rogue.txt", "R")
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep),
		Sealed:          manifest("sealed", keep, rogue),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: manifest("post-publication", keep, rogue),
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "rogue.txt", ChangeCreate, ReasonUnattributedLiveChange)
	if !r.Findings[0].MatchesSealed {
		t.Fatalf("finding should match the sealed object: %+v", r.Findings[0])
	}
	wantUnattributed(t, r, "rogue.txt")
}

func TestWS05UnattributedDeleteReachingLiveIsAnEffect(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	old := fileEntry("old.txt", "O")
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep, old),
		Sealed:          manifest("sealed", keep),
		PrePublication:  manifest("pre-publication", keep, old),
		PostPublication: manifest("post-publication", keep),
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "old.txt", ChangeDelete, ReasonUnattributedLiveChange)
	wantUnattributed(t, r, "old.txt")
}

func TestWS05LedgeredCreateAppliedExactlyHasNoEffect(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	ok := fileEntry("ok.txt", "OK")
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep),
		Ledger:          []LedgerEntry{approve("e1", 1, LedgerChange{Path: "ok.txt", Before: nil, After: entryPtr(ok)})},
		Sealed:          manifest("sealed", keep, ok),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: manifest("post-publication", keep, ok),
	})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
	wantUnattributed(t, r)
}

func TestWS05ApprovedContentReplacedByOtherLiveContentIsAnEffect(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	approved := fileEntry("f.txt", "APPROVED")
	tampered := fileEntry("f.txt", "TAMPERED")
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep),
		Ledger:          []LedgerEntry{approve("e1", 1, LedgerChange{Path: "f.txt", After: entryPtr(approved)})},
		Sealed:          manifest("sealed", keep, approved),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: manifest("post-publication", keep, tampered),
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "f.txt", ChangeCreate, ReasonUnattributedLiveChange)
	if r.Findings[0].MatchesSealed {
		t.Fatalf("live content differs from the sealed object, so MatchesSealed must be false")
	}
	if r.Findings[0].ReferenceSHA256 != digestOf("APPROVED") {
		t.Fatalf("reference digest = %s, want the approved content digest", r.Findings[0].ReferenceSHA256)
	}
	wantUnattributed(t, r)
}

func TestWS05StagedUnattributedDeltaThatIsBlockedIsNotAnEffect(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	rogue := fileEntry("rogue.txt", "R")
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep),
		Sealed:          manifest("sealed", keep, rogue),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: manifest("post-publication", keep),
	})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
	wantUnattributed(t, r, "rogue.txt")
}

func TestWS05NoChangeAnywhereIsNoEffect(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep),
		Sealed:          manifest("sealed", keep),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: manifest("post-publication", keep),
	})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
	wantUnattributed(t, r)
}

func TestWS05StaleLiveFileThatPublicationDoesNotTouchIsNotAWS05Effect(t *testing.T) {
	// The host edited keep.txt before publication. That is EFF-WS-06 territory.
	// Publication did not change it, so EFF-WS-05 must stay silent.
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", fileEntry("keep.txt", "K")),
		Sealed:          manifest("sealed", fileEntry("keep.txt", "K")),
		PrePublication:  manifest("pre-publication", fileEntry("keep.txt", "H")),
		PostPublication: manifest("post-publication", fileEntry("keep.txt", "H")),
	})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
}

func TestWS05MalformedLedgerIsEvidenceLostAndNeverNoEffect(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	fresh := fileEntry("new.txt", "N")
	cases := []struct {
		name   string
		ledger []LedgerEntry
		detail string
	}{
		{
			name:   "sequence_does_not_start_at_one",
			ledger: []LedgerEntry{approve("e1", 2, LedgerChange{Path: "new.txt", After: entryPtr(fresh)})},
			detail: "sequence",
		},
		{
			name: "sequence_gap",
			ledger: []LedgerEntry{
				approve("e1", 1, LedgerChange{Path: "new.txt", After: entryPtr(fresh)}),
				approve("e2", 3, LedgerChange{Path: "keep.txt", Before: entryPtr(keep), After: nil}),
			},
			detail: "sequence",
		},
		{
			name: "duplicate_id",
			ledger: []LedgerEntry{
				approve("e1", 1, LedgerChange{Path: "new.txt", After: entryPtr(fresh)}),
				approve("e1", 2, LedgerChange{Path: "keep.txt", Before: entryPtr(keep), After: nil}),
			},
			detail: "id",
		},
		{
			name:   "empty_id",
			ledger: []LedgerEntry{approve("", 1, LedgerChange{Path: "new.txt", After: entryPtr(fresh)})},
			detail: "id",
		},
		{
			name:   "escaping_path",
			ledger: []LedgerEntry{approve("e1", 1, LedgerChange{Path: "../escape.txt", After: entryPtr(fileEntry("../escape.txt", "X"))})},
			detail: "path",
		},
		{
			name:   "absolute_path",
			ledger: []LedgerEntry{approve("e1", 1, LedgerChange{Path: "/etc/x", After: entryPtr(fileEntry("/etc/x", "X"))})},
			detail: "path",
		},
		{
			name:   "duplicate_path_within_one_entry",
			ledger: []LedgerEntry{approve("e1", 1, LedgerChange{Path: "new.txt", After: entryPtr(fresh)}, LedgerChange{Path: "new.txt", After: entryPtr(fresh)})},
			detail: "duplicate_path",
		},
		{
			name:   "entry_path_differs_from_change_path",
			ledger: []LedgerEntry{approve("e1", 1, LedgerChange{Path: "new.txt", After: entryPtr(fileEntry("other.txt", "N"))})},
			detail: "entry_path",
		},
		{
			name:   "before_does_not_match_composed_state",
			ledger: []LedgerEntry{approve("e1", 1, LedgerChange{Path: "keep.txt", Before: entryPtr(fileEntry("keep.txt", "WRONG")), After: entryPtr(fileEntry("keep.txt", "K2"))})},
			detail: "before_mismatch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := EvaluateWS05(WS05Input{
				Baseline:        manifest("baseline", keep),
				Ledger:          tc.ledger,
				Sealed:          manifest("sealed", keep, fresh),
				PrePublication:  manifest("pre-publication", keep),
				PostPublication: manifest("post-publication", keep),
			})
			wantVerdict(t, r, VerdictEvidenceLost)
			wantGap(t, r, GapLedgerInvalid, "ledger")
			found := false
			for _, g := range r.Gaps {
				if g.Kind == GapLedgerInvalid && g.Detail == tc.detail {
					found = true
				}
			}
			if !found {
				t.Fatalf("no ledger_invalid gap with detail %q in %+v", tc.detail, r.Gaps)
			}
		})
	}
}

func TestWS05IncompleteLiveManifestIsEvidenceLostEvenWhenAnEffectIsSeen(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	rogue := fileEntry("rogue.txt", "R")
	pre := manifest("pre-publication", keep)
	pre.Complete = false
	pre.Gaps = []Gap{{Kind: GapUnreadable, Path: "locked"}}
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep),
		Sealed:          manifest("sealed", keep, rogue),
		PrePublication:  pre,
		PostPublication: manifest("post-publication", keep, rogue),
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapUnreadable, "pre-publication")
	wantSingleFinding(t, r, "rogue.txt", ChangeCreate, ReasonUnattributedLiveChange)
}

func TestWS05IncompleteFlagWithoutAGapRecordIsEvidenceLost(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	post := manifest("post-publication", keep)
	post.Complete = false
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep),
		Sealed:          manifest("sealed", keep),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: post,
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapIncompleteManifest, "post-publication")
}

func TestWS05IncompleteSealedGenerationIsEvidenceLost(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	sealed := manifest("sealed", keep)
	sealed.Complete = false
	sealed.Gaps = []Gap{{Kind: GapVanished, Path: "keep.txt"}}
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep),
		Sealed:          sealed,
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: manifest("post-publication", keep),
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapVanished, "sealed")
}

func TestWS05LiveRootIdentityChangeIsEvidenceLost(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	base := manifest("baseline", keep)
	base = withRootID(base, 1, 10)
	post := manifest("post-publication", keep)
	post = withRootID(post, 1, 99)
	r := EvaluateWS05(WS05Input{
		Baseline:        base,
		Sealed:          manifest("sealed", keep),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: post,
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapRootIdentity, "post-publication")
}

func TestWS05SealedGenerationIsASeparateTreeSoItsRootIdentityIsNotCompared(t *testing.T) {
	keep := fileEntry("keep.txt", "K")
	// The three live manifests share one root identity. The sealed generation
	// lives in another tree, and its identity is not compared with the live one.
	live := func(label string) Manifest {
		m := manifest(label, keep)
		m = withRootID(m, 1, 10)
		return m
	}
	sealed := manifest("sealed", keep)
	sealed = withRootID(sealed, 5, 50)
	r := EvaluateWS05(WS05Input{
		Baseline:        live("baseline"),
		Sealed:          sealed,
		PrePublication:  live("pre-publication"),
		PostPublication: live("post-publication"),
	})
	wantVerdict(t, r, VerdictNoEffect)
}

func TestWS06HostEditThenStaleOverwriteIsAnEffect(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		Baseline:          manifest("baseline", fileEntry("f.txt", "A"), fileEntry("g.txt", "G")),
		PrePublication:    manifest("pre-publication", fileEntry("f.txt", "H"), fileEntry("g.txt", "G")),
		PostPublication:   manifest("post-publication", fileEntry("f.txt", "P"), fileEntry("g.txt", "G")),
		AffectedPaths:     []string{"f.txt"},
		LockIdentity:      "lock-1",
		PublicationResult: "SUCCEEDED",
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "f.txt", ChangeModify, ReasonStaleBaselineOverwrite)
	f := r.Findings[0]
	if f.ReferenceSHA256 != digestOf("A") || f.PreSHA256 != digestOf("H") || f.PostSHA256 != digestOf("P") {
		t.Fatalf("finding digests wrong: %+v", f)
	}
}

func TestWS06OverwriteOfAnUnchangedBaselineIsNoEffect(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		Baseline:        manifest("baseline", fileEntry("f.txt", "A")),
		PrePublication:  manifest("pre-publication", fileEntry("f.txt", "A")),
		PostPublication: manifest("post-publication", fileEntry("f.txt", "P")),
		AffectedPaths:   []string{"f.txt"},
		LockIdentity:    "lock-1",
	})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
}

func TestWS06StaleLiveFileThatPublicationAbortsIsNoEffect(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		Baseline:        manifest("baseline", fileEntry("f.txt", "A")),
		PrePublication:  manifest("pre-publication", fileEntry("f.txt", "H")),
		PostPublication: manifest("post-publication", fileEntry("f.txt", "H")),
		AffectedPaths:   []string{"f.txt"},
		LockIdentity:    "lock-1",
	})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
}

func TestWS06StaleLiveFileThatPublicationDoesNotTouchIsNoEffect(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		Baseline:        manifest("baseline", fileEntry("f.txt", "A"), fileEntry("g.txt", "G")),
		PrePublication:  manifest("pre-publication", fileEntry("f.txt", "H"), fileEntry("g.txt", "G")),
		PostPublication: manifest("post-publication", fileEntry("f.txt", "H"), fileEntry("g.txt", "G2")),
		AffectedPaths:   []string{"g.txt"},
		LockIdentity:    "lock-1",
	})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
}

func TestWS06StaleDeletionIsAnEffect(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		Baseline:        manifest("baseline", fileEntry("f.txt", "A")),
		PrePublication:  manifest("pre-publication", fileEntry("f.txt", "H")),
		PostPublication: manifest("post-publication"),
		AffectedPaths:   []string{"f.txt"},
		LockIdentity:    "lock-1",
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "f.txt", ChangeDelete, ReasonStaleBaselineOverwrite)
}

func TestWS06HostCreatedFileThatPublicationOverwritesIsAnEffect(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		Baseline:        manifest("baseline"),
		PrePublication:  manifest("pre-publication", fileEntry("h.txt", "HOST")),
		PostPublication: manifest("post-publication", fileEntry("h.txt", "P")),
		AffectedPaths:   []string{"h.txt"},
		LockIdentity:    "lock-1",
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "h.txt", ChangeModify, ReasonStaleBaselineOverwrite)
}

func TestWS06MissingLockIdentityIsEvidenceLostEvenWhenAnEffectIsSeen(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"f.txt"},
		Baseline:        manifest("baseline", fileEntry("f.txt", "A")),
		PrePublication:  manifest("pre-publication", fileEntry("f.txt", "H")),
		PostPublication: manifest("post-publication", fileEntry("f.txt", "P")),
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapLockIdentityMissing, "lock")
	wantSingleFinding(t, r, "f.txt", ChangeModify, ReasonStaleBaselineOverwrite)
}

func TestWS06IncompleteBaselineIsEvidenceLost(t *testing.T) {
	base := manifest("baseline", fileEntry("f.txt", "A"))
	base.Complete = false
	base.Gaps = []Gap{{Kind: GapUnreadable, Path: "f.txt"}}
	r := EvaluateWS06(WS06Input{
		Baseline:        base,
		PrePublication:  manifest("pre-publication", fileEntry("f.txt", "A")),
		PostPublication: manifest("post-publication", fileEntry("f.txt", "A")),
		AffectedPaths:   []string{"f.txt"},
		LockIdentity:    "lock-1",
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapUnreadable, "baseline")
}

func TestWS06LiveRootIdentityChangeIsEvidenceLost(t *testing.T) {
	pre := manifest("pre-publication", fileEntry("f.txt", "A"))
	pre = withRootID(pre, 2, 20)
	base := manifest("baseline", fileEntry("f.txt", "A"))
	base = withRootID(base, 1, 10)
	r := EvaluateWS06(WS06Input{
		Baseline:        base,
		PrePublication:  pre,
		PostPublication: manifest("post-publication", fileEntry("f.txt", "A")),
		AffectedPaths:   []string{"f.txt"},
		LockIdentity:    "lock-1",
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapRootIdentity, "pre-publication")
}

// Contradictory-manifest tests. A manifest marked Complete with no gap must
// still be rejected when its entries cannot be trusted.

func TestWS05DuplicatePathInPostManifestIsEvidenceLostNotNoEffect(t *testing.T) {
	ok := fileEntry("a.txt", "OK")
	// Rogue content first, approved content second. The last entry must not win.
	r := EvaluateWS05(WS05Input{
		Baseline: manifest("baseline"),
		Ledger: []LedgerEntry{approve("l1", 1,
			LedgerChange{Path: "a.txt", Before: nil, After: entryPtr(ok)})},
		Sealed:          manifest("sealed", ok),
		PrePublication:  manifest("pre-publication"),
		PostPublication: manifest("post-publication", fileEntry("a.txt", "ROGUE"), ok),
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapInvalidEntry, "post-publication")
}

func badEntryCases() map[string]Entry {
	noDigest := fileEntry("x.txt", "X")
	noDigest.SHA256 = ""
	badDigest := fileEntry("x.txt", "X")
	badDigest.SHA256 = "not-a-digest"
	return map[string]Entry{
		"absolute path":        fileEntry("/x.txt", "X"),
		"parent traversal":     fileEntry("../x.txt", "X"),
		"embedded parent":      fileEntry("a/../x.txt", "X"),
		"empty path":           fileEntry("", "X"),
		"dot component":        fileEntry("./x.txt", "X"),
		"double slash":         fileEntry("a//x.txt", "X"),
		"trailing slash":       fileEntry("x.txt/", "X"),
		"unreadable type":      {Path: "x.txt", Type: TypeUnreadable},
		"unknown type":         {Path: "x.txt", Type: "fifo"},
		"empty type":           {Path: "x.txt"},
		"file without digest":  noDigest,
		"file with bad digest": badDigest,
	}
}

func TestWS05InvalidEntryInAnyManifestIsEvidenceLost(t *testing.T) {
	for name, bad := range badEntryCases() {
		for _, slot := range []string{"baseline", "sealed", "pre-publication", "post-publication"} {
			in := noChangeCase()
			ms := map[string]*Manifest{"baseline": &in.Baseline, "sealed": &in.Sealed, "pre-publication": &in.PrePublication, "post-publication": &in.PostPublication}
			ms[slot].Entries = append(ms[slot].Entries, bad)
			r := EvaluateWS05(in)
			if r.Verdict != VerdictEvidenceLost {
				t.Errorf("%s in %s: verdict = %q, want evidence_lost", name, slot, r.Verdict)
			}
			wantGap(t, r, GapInvalidEntry, slot)
		}
	}
}

func TestWS05IdenticalUnreadableEntriesInAllCompleteManifestsAreEvidenceLost(t *testing.T) {
	u := Entry{Path: "locked.txt", Type: TypeUnreadable}
	keep := fileEntry("keep.txt", "K")
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", keep, u),
		Sealed:          manifest("sealed", keep, u),
		PrePublication:  manifest("pre-publication", keep, u),
		PostPublication: manifest("post-publication", keep, u),
	})
	wantVerdict(t, r, VerdictEvidenceLost)
}

func TestWS06DuplicatePathInPostManifestIsEvidenceLostNotNoEffect(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"f.txt"},
		Baseline:        manifest("baseline", fileEntry("f.txt", "A")),
		PrePublication:  manifest("pre-publication", fileEntry("f.txt", "A")),
		PostPublication: manifest("post-publication", fileEntry("f.txt", "ROGUE"), fileEntry("f.txt", "A")),
		LockIdentity:    "lock-1",
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapInvalidEntry, "post-publication")
}

func TestWS06InvalidEntryInAnyManifestIsEvidenceLost(t *testing.T) {
	for name, bad := range badEntryCases() {
		for _, slot := range []string{"baseline", "pre-publication", "post-publication"} {
			in := WS06Input{
				AffectedPaths:   []string{"f.txt"},
				Baseline:        manifest("baseline", fileEntry("f.txt", "A")),
				PrePublication:  manifest("pre-publication", fileEntry("f.txt", "A")),
				PostPublication: manifest("post-publication", fileEntry("f.txt", "A")),
				LockIdentity:    "lock-1",
			}
			ms := map[string]*Manifest{"baseline": &in.Baseline, "pre-publication": &in.PrePublication, "post-publication": &in.PostPublication}
			ms[slot].Entries = append(ms[slot].Entries, bad)
			r := EvaluateWS06(in)
			if r.Verdict != VerdictEvidenceLost {
				t.Errorf("%s in %s: verdict = %q, want evidence_lost", name, slot, r.Verdict)
			}
			wantGap(t, r, GapInvalidEntry, slot)
		}
	}
}

func TestWS06IdenticalUnreadableEntriesInAllCompleteManifestsAreEvidenceLost(t *testing.T) {
	u := Entry{Path: "f.txt", Type: TypeUnreadable}
	r := EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"f.txt"},
		Baseline:        manifest("baseline", u),
		PrePublication:  manifest("pre-publication", u),
		PostPublication: manifest("post-publication", u),
		LockIdentity:    "lock-1",
	})
	wantVerdict(t, r, VerdictEvidenceLost)
}

// EFF-WS-06 is "publication proceeds although the live destination no longer
// matches the registered baseline" (13-oracle-coverage-matrix.md §1.1). A
// conflict anywhere in the intended affected set counts, even when publication
// has not touched the conflicting object yet.

func partialApplicationCase() WS06Input {
	return WS06Input{
		AffectedPaths:   []string{"a.txt", "b.txt"},
		Baseline:        manifest("baseline", fileEntry("a.txt", "A"), fileEntry("b.txt", "B")),
		PrePublication:  manifest("pre-publication", fileEntry("a.txt", "HOST"), fileEntry("b.txt", "B")),
		PostPublication: manifest("post-publication", fileEntry("a.txt", "HOST"), fileEntry("b.txt", "P")),
		LockIdentity:    "lock-1",
	}
}

func TestWS06PublicationAppliesNonConflictingObjectThenFailsBeforeConflictingOneIsAnEffect(t *testing.T) {
	in := partialApplicationCase()
	in.PublicationResult = "FAILED"
	r := EvaluateWS06(in)
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "a.txt", ChangeNone, ReasonBaselineConflictProceeded)
	if f := r.Findings[0]; f.PreSHA256 != digestOf("HOST") || f.ReferenceSHA256 != digestOf("A") {
		t.Fatalf("finding digests wrong: %+v", f)
	}
}

func TestWS06ConflictInAffectedSetWithNoPublicationChangeIsNoEffect(t *testing.T) {
	in := partialApplicationCase()
	in.PostPublication = manifest("post-publication", fileEntry("a.txt", "HOST"), fileEntry("b.txt", "B"))
	r := EvaluateWS06(in)
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
}

func TestWS06ConflictOutsideAffectedSetDoesNotMakeAnAffectedChangeAnEffect(t *testing.T) {
	in := partialApplicationCase()
	in.AffectedPaths = []string{"b.txt"}
	r := EvaluateWS06(in)
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
}

func TestWS06MissingOrInvalidAffectedSetIsEvidenceLost(t *testing.T) {
	cases := map[string][]string{
		"nil":            nil,
		"empty":          {},
		"empty path":     {"a.txt", ""},
		"absolute":       {"/a.txt"},
		"parent":         {"../a.txt"},
		"non-clean":      {"a.txt", "b//c"},
		"duplicate path": {"a.txt", "a.txt"},
	}
	for name, set := range cases {
		in := partialApplicationCase()
		in.AffectedPaths = set
		r := EvaluateWS06(in)
		if r.Verdict != VerdictEvidenceLost {
			t.Errorf("%s: verdict = %q, want evidence_lost", name, r.Verdict)
		}
		if len(set) == 0 {
			wantGap(t, r, GapAffectedSetMissing, "affected_paths")
		} else {
			wantGap(t, r, GapAffectedSetInvalid, "affected_paths")
		}
	}
}
