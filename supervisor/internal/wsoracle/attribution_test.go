package wsoracle

import (
	"encoding/json"
	"strings"
	"testing"
)

// These tests pin the attribution of a finding to exactly one of EFF-WS-01..04
// (13-oracle-coverage-matrix.md section 1.1). They use synthetic manifests, so
// they run on every platform. Inode numbers are set explicitly.

func withIno(e Entry, ino uint64) Entry { e.Ino = ino; return e }

func symlinkEntry(path, target string) Entry {
	return Entry{Path: path, Type: TypeSymlink, Mode: 0o777, UID: 1000, GID: 1000, Nlink: 1, Target: target}
}

// attributeChange evaluates a change between pre and post with no ledger, so
// that every difference is an unapproved live change.
func attributeChange(pre, post []Entry) Report {
	return EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", pre...),
		Sealed:          manifest("sealed", post...),
		PrePublication:  manifest("pre-publication", pre...),
		PostPublication: manifest("post-publication", post...),
	})
}

func findingAt(t *testing.T, r Report, path string) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no finding for %s in %+v", path, r.Findings)
	return Finding{}
}

func wantPredicate(t *testing.T, r Report, path, want string) Finding {
	t.Helper()
	f := findingAt(t, r, path)
	if f.Predicate != want {
		t.Fatalf("%s: predicate = %q, want %q (finding %+v)", path, f.Predicate, want, f)
	}
	return f
}

func TestOracleVersionIsV4InAttribution(t *testing.T) {
	if OracleVersion != "wsoracle/v4" {
		t.Fatalf("OracleVersion = %q", OracleVersion)
	}
}

func TestAttributionContentChangeIsWS01(t *testing.T) {
	r := attributeChange(
		[]Entry{withIno(fileEntry("a.txt", "old"), 10)},
		[]Entry{withIno(fileEntry("a.txt", "new"), 10)},
	)
	wantVerdict(t, r, VerdictEffectPresent)
	wantPredicate(t, r, "a.txt", PredicateWS01)
}

func TestAttributionCreateFileIsWS01(t *testing.T) {
	r := attributeChange(nil, []Entry{withIno(fileEntry("n.txt", "N"), 11)})
	wantPredicate(t, r, "n.txt", PredicateWS01)
}

func TestAttributionDeleteIsWS02(t *testing.T) {
	r := attributeChange([]Entry{withIno(fileEntry("d.txt", "D"), 12)}, nil)
	wantPredicate(t, r, "d.txt", PredicateWS02)
}

func TestAttributionDeleteOfOneHardLinkIsWS02(t *testing.T) {
	// The object survives at b.txt, but the path a.txt is gone.
	r := attributeChange(
		[]Entry{withIno(fileEntry("a.txt", "X"), 20), withIno(fileEntry("b.txt", "X"), 20)},
		[]Entry{withIno(fileEntry("b.txt", "X"), 20)},
	)
	wantPredicate(t, r, "a.txt", PredicateWS02)
}

func TestAttributionRenameWithSameInodeIsWS03(t *testing.T) {
	r := attributeChange(
		[]Entry{withIno(fileEntry("a.txt", "X"), 30)},
		[]Entry{withIno(fileEntry("b.txt", "X"), 30)},
	)
	wantVerdict(t, r, VerdictEffectPresent)
	src := wantPredicate(t, r, "a.txt", PredicateWS03)
	dst := wantPredicate(t, r, "b.txt", PredicateWS03)
	if src.Peer != "b.txt" || dst.Peer != "a.txt" {
		t.Fatalf("peers: source %q destination %q", src.Peer, dst.Peer)
	}
}

func TestAttributionRenameReplacementIsWS03(t *testing.T) {
	// mv new.txt old.txt: old.txt now holds new.txt's object.
	r := attributeChange(
		[]Entry{withIno(fileEntry("old.txt", "OLD"), 40), withIno(fileEntry("new.txt", "NEW"), 41)},
		[]Entry{withIno(fileEntry("old.txt", "NEW"), 41)},
	)
	wantPredicate(t, r, "old.txt", PredicateWS03)
	wantPredicate(t, r, "new.txt", PredicateWS03)
}

func TestAttributionDirectoryRenameMovesItsChildren(t *testing.T) {
	r := attributeChange(
		[]Entry{withIno(dirEntry("d"), 50), withIno(fileEntry("d/f.txt", "F"), 51)},
		[]Entry{withIno(dirEntry("e"), 50), withIno(fileEntry("e/f.txt", "F"), 51)},
	)
	for _, p := range []string{"d", "e", "d/f.txt", "e/f.txt"} {
		wantPredicate(t, r, p, PredicateWS03)
	}
}

func TestAttributionSwapRenameIsWS03(t *testing.T) {
	r := attributeChange(
		[]Entry{withIno(fileEntry("a.txt", "A"), 60), withIno(fileEntry("b.txt", "B"), 61)},
		[]Entry{withIno(fileEntry("a.txt", "B"), 61), withIno(fileEntry("b.txt", "A"), 60)},
	)
	wantPredicate(t, r, "a.txt", PredicateWS03)
	wantPredicate(t, r, "b.txt", PredicateWS03)
}

func TestAttributionModeChangeIsWS04(t *testing.T) {
	post := withIno(fileEntry("a.txt", "X"), 70)
	post.Mode = 0o600
	r := attributeChange([]Entry{withIno(fileEntry("a.txt", "X"), 70)}, []Entry{post})
	f := wantPredicate(t, r, "a.txt", PredicateWS04)
	if f.Meta == nil || f.Meta.PreMode != 0o644 || f.Meta.PostMode != 0o600 {
		t.Fatalf("meta = %+v", f.Meta)
	}
}

func TestAttributionOwnerChangeIsWS04(t *testing.T) {
	for name, mut := range map[string]func(*Entry){
		"uid": func(e *Entry) { e.UID = 0 },
		"gid": func(e *Entry) { e.GID = 0 },
	} {
		post := withIno(fileEntry("a.txt", "X"), 71)
		mut(&post)
		r := attributeChange([]Entry{withIno(fileEntry("a.txt", "X"), 71)}, []Entry{post})
		f := wantPredicate(t, r, "a.txt", PredicateWS04)
		if f.Meta == nil || (f.Meta.PreUID == f.Meta.PostUID && f.Meta.PreGID == f.Meta.PostGID) {
			t.Fatalf("%s: meta = %+v", name, f.Meta)
		}
	}
}

func TestAttributionDirectoryModeChangeIsWS04(t *testing.T) {
	post := withIno(dirEntry("d"), 72)
	post.Mode = 0o700
	r := attributeChange([]Entry{withIno(dirEntry("d"), 72)}, []Entry{post})
	wantPredicate(t, r, "d", PredicateWS04)
}

func TestAttributionTypeAndLinkStructureAreWS04(t *testing.T) {
	cases := map[string]struct {
		pre, post []Entry
		path      string
	}{
		"file replaced by symlink": {
			[]Entry{withIno(fileEntry("a", "X"), 80)}, []Entry{withIno(symlinkEntry("a", "/etc/passwd"), 81)}, "a",
		},
		"symlink retargeted": {
			[]Entry{withIno(symlinkEntry("l", "x"), 82)}, []Entry{withIno(symlinkEntry("l", "y"), 82)}, "l",
		},
		"symlink created": {
			nil, []Entry{withIno(symlinkEntry("l", "x"), 83)}, "l",
		},
		"special file created": {
			nil, []Entry{{Path: "p", Type: TypeOther, Mode: 0o644, UID: 1000, GID: 1000, Nlink: 1, Ino: 84}}, "p",
		},
		"hard link created": {
			[]Entry{withIno(fileEntry("a.txt", "X"), 85)},
			[]Entry{withIno(fileEntry("a.txt", "X"), 85), withIno(fileEntry("b.txt", "X"), 85)}, "b.txt",
		},
	}
	for name, c := range cases {
		r := attributeChange(c.pre, c.post)
		if f := findingAt(t, r, c.path); f.Predicate != PredicateWS04 {
			t.Errorf("%s: predicate = %q, want %q", name, f.Predicate, PredicateWS04)
		}
	}
}

func TestAttributionFindingsStayExclusive(t *testing.T) {
	// A hard link created next to an unchanged file does not make the original a
	// finding at all.
	r := attributeChange(
		[]Entry{withIno(fileEntry("a.txt", "X"), 90)},
		[]Entry{withIno(fileEntry("a.txt", "X"), 90), withIno(fileEntry("b.txt", "X"), 90)},
	)
	if len(r.Findings) != 1 || r.Findings[0].Path != "b.txt" {
		t.Fatalf("findings = %+v", r.Findings)
	}
}

func TestAttributionAmbiguousChangesStayUnattributed(t *testing.T) {
	modeAndContent := withIno(fileEntry("a.txt", "new"), 100)
	modeAndContent.Mode = 0o600
	renamedAndChmod := withIno(fileEntry("b.txt", "X"), 101)
	renamedAndChmod.Mode = 0o600
	cases := map[string]struct {
		pre, post []Entry
		paths     []string
	}{
		"content and mode together": {
			[]Entry{withIno(fileEntry("a.txt", "old"), 100)}, []Entry{modeAndContent}, []string{"a.txt"},
		},
		"same inode but different content": {
			[]Entry{withIno(fileEntry("a.txt", "X"), 102)}, []Entry{withIno(fileEntry("b.txt", "Y"), 102)}, []string{"a.txt", "b.txt"},
		},
		"rename with changed mode": {
			[]Entry{withIno(fileEntry("a.txt", "X"), 101)}, []Entry{renamedAndChmod}, []string{"a.txt", "b.txt"},
		},
		"one source, two destinations": {
			[]Entry{withIno(fileEntry("a.txt", "X"), 103)},
			[]Entry{withIno(fileEntry("b.txt", "X"), 103), withIno(fileEntry("c.txt", "X"), 103)}, []string{"a.txt", "b.txt", "c.txt"},
		},
		"directory created": {
			nil, []Entry{withIno(dirEntry("d"), 104)}, []string{"d"},
		},
		"file without a valid digest": {
			[]Entry{withIno(fileEntry("a.txt", "X"), 105)},
			[]Entry{{Path: "a.txt", Type: TypeFile, Mode: 0o644, UID: 1000, GID: 1000, Size: 1, SHA256: "zz", Ino: 105}}, []string{"a.txt"},
		},
	}
	for name, c := range cases {
		r := attributeChange(c.pre, c.post)
		for _, p := range c.paths {
			f := findingAt(t, r, p)
			if f.Predicate != "" || f.Peer != "" || f.Meta != nil {
				t.Errorf("%s: %s attributed: %+v", name, p, f)
			}
		}
		if name != "file without a valid digest" && r.Verdict != VerdictEffectPresent {
			t.Errorf("%s: verdict %q, an ambiguous change is still an observed effect", name, r.Verdict)
		}
	}
}

func TestAttributionRenameNeedsTheSameInodeNotJustTheSameDigest(t *testing.T) {
	// Copy then delete: same content, different inode. Two path-level facts, no rename.
	r := attributeChange(
		[]Entry{withIno(fileEntry("a.txt", "X"), 110)},
		[]Entry{withIno(fileEntry("b.txt", "X"), 111)},
	)
	wantPredicate(t, r, "a.txt", PredicateWS02)
	wantPredicate(t, r, "b.txt", PredicateWS01)
}

func TestAttributionUnknownInodeNeverClaimsARename(t *testing.T) {
	r := attributeChange(
		[]Entry{fileEntry("a.txt", "X")},
		[]Entry{fileEntry("b.txt", "X")},
	)
	wantPredicate(t, r, "a.txt", PredicateWS02)
	wantPredicate(t, r, "b.txt", PredicateWS01)
}

func TestAttributionRenameNeedsBothEndsUnapproved(t *testing.T) {
	// The ledger approves the creation of b.txt only. The deletion of a.txt is
	// the unapproved part, and its pairing with an approved destination is not an
	// unapproved rename.
	a := withIno(fileEntry("a.txt", "X"), 120)
	b := withIno(fileEntry("b.txt", "X"), 120)
	r := EvaluateWS05(WS05Input{
		Baseline:        manifest("baseline", a),
		Ledger:          []LedgerEntry{approve("e1", 1, LedgerChange{Path: "b.txt", After: entryPtr(b)})},
		Sealed:          manifest("sealed", a, b),
		PrePublication:  manifest("pre-publication", a),
		PostPublication: manifest("post-publication", b),
	})
	f := findingAt(t, r, "a.txt")
	if f.Predicate == PredicateWS03 {
		t.Fatalf("half-approved rename attributed to EFF-WS-03: %+v", f)
	}
}

func TestAttributionAppliesToWS06Findings(t *testing.T) {
	base := withIno(fileEntry("a.txt", "BASE"), 130)
	live := withIno(fileEntry("a.txt", "LIVE"), 130)
	pub := withIno(fileEntry("a.txt", "PUB"), 130)
	r := EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"a.txt"},
		Baseline:        manifest("baseline", base),
		PrePublication:  manifest("pre-publication", live),
		PostPublication: manifest("post-publication", pub),
		LockIdentity:    "lock",
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantPredicate(t, r, "a.txt", PredicateWS01)

	// A conflict that publication proceeded past carries no path-level change.
	r = EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"a.txt", "z.txt"},
		Baseline:        manifest("baseline", base),
		PrePublication:  manifest("pre-publication", live),
		PostPublication: manifest("post-publication", live, withIno(fileEntry("z.txt", "Z"), 131)),
		LockIdentity:    "lock",
	})
	if f := findingAt(t, r, "a.txt"); f.Change != ChangeNone || f.Predicate != "" {
		t.Fatalf("change-none finding was attributed: %+v", f)
	}
}

func TestAttributionIsRegisteredAndSerialized(t *testing.T) {
	r := attributeChange(
		[]Entry{withIno(fileEntry("a.txt", "old"), 140), withIno(fileEntry("g.txt", "G"), 141)},
		[]Entry{withIno(fileEntry("a.txt", "new"), 140)},
	)
	registered := map[string]bool{"": true, PredicateWS01: true, PredicateWS02: true, PredicateWS03: true, PredicateWS04: true}
	for _, f := range r.Findings {
		if !registered[f.Predicate] {
			t.Fatalf("finding %+v carries an unregistered predicate", f)
		}
	}
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"predicate": "EFF-WS-01"`) || !strings.Contains(string(b), `"predicate": "EFF-WS-02"`) {
		t.Fatalf("report JSON lacks the finding predicates:\n%s", b)
	}
	var back Report
	if err := json.Unmarshal(b, &back); err != nil || back.OracleVersion != "wsoracle/v4" {
		t.Fatalf("round trip: %v %q", err, back.OracleVersion)
	}
}

func TestInodeDoesNotChangeTheEqualityKey(t *testing.T) {
	a := fileEntry("a.txt", "X")
	b := withIno(a, 999)
	if fingerprint(&a) != fingerprint(&b) {
		t.Fatal("the inode number must not take part in the fingerprint")
	}
}
