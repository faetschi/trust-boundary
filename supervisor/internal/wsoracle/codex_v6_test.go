package wsoracle

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// These tests turn the adversarial review findings against wsoracle v5 into
// counterexamples. Each one failed against v5.

// ---- Finding 3: a retained hard-link (link count) change must not vanish. ----

func withNlink(e Entry, n uint64) Entry { e.Nlink = n; return e }

func TestE04RetainedLinkCountChangeFromOutsideTheRootIsAWS04Effect(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	for name, base := range map[string]Entry{"identity known": a, "identity unknown": withIno(a, 0)} {
		linked := withNlink(base, 2)
		ps := e04Points(0, base)
		// The link exists at settlement and is gone again before cleanup.
		ps[1].Manifest = manifest(PointPostSettlement, linked)
		r := evalE04(e04In("TM-C", []Entry{base}, ps))
		wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, PredicateWS04))
		if len(r.Findings) != 1 {
			t.Fatalf("%s: findings = %+v", name, r.Findings)
		}
		f := r.Findings[0]
		if f.Path != "a.txt" || f.Predicate != PredicateWS04 || f.Point != PointPostSettlement || f.Meta == nil || f.Meta.PreNlink != 1 || f.Meta.PostNlink != 2 {
			t.Errorf("%s: finding = %+v meta=%+v", name, f, f.Meta)
		}
	}
}

func TestE04LinkCountChangeIsPartOfNarrowingAndTheLedger(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	// A content change together with a link count change is the same object
	// changed in place, as content and metadata.
	both := withNlink(withIno(fileEntry("a.txt", "B"), 1), 2)
	r := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, both)))
	if len(r.Unattributed) != 1 || !slices.Equal(r.Unattributed[0].Candidates, []string{PredicateWS01, PredicateWS04}) {
		t.Fatalf("unattributed = %+v", r.Unattributed)
	}
	wantVerdicts(t, r, only(VerdictIndeterminate, VerdictNoEffect, PredicateWS01, PredicateWS04))

	// An approved link count change is not an effect where it is applied, and is
	// one before.
	linked := withNlink(a, 2)
	ledger := []LedgerEntry{approve("l1", 1, LedgerChange{Path: "a.txt", Before: entryPtr(a), After: entryPtr(linked)})}
	ps := e04Points(0, a)
	for i := 3; i < len(ps); i++ {
		ps[i].Manifest = manifest(ps[i].Name, linked)
		ps[i].LedgerApplied = 1
	}
	in := e04In("TM-C", []Entry{a}, ps)
	in.Ledger = ledger
	wantVerdicts(t, evalE04(in), all4(VerdictNoEffect))
	in.Points[0].Manifest = manifest(in.Points[0].Name, linked)
	wantVerdicts(t, evalE04(in), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS04))

	// A ledger whose Before link count is not the baseline's is invalid.
	bad := []LedgerEntry{approve("l1", 1, LedgerChange{Path: "a.txt", Before: entryPtr(linked), After: entryPtr(a)})}
	in = e04In("TM-C", []Entry{a}, e04Points(0, a))
	in.Ledger = bad
	wantVerdicts(t, evalE04(in), all4(VerdictEvidenceLost))
}

func TestE04DirectoryLinkCountFollowsFromTheTreeAndIsNotAFindingOnItsOwn(t *testing.T) {
	d := withIno(dirEntry("d"), 2)
	d3 := withNlink(d, 3)
	r := evalE04(e04In("TM-C", []Entry{d}, e04Points(0, d3, withIno(dirEntry("d/s"), 5))))
	for _, f := range r.Findings {
		if f.Path == "d" {
			t.Errorf("a parent's link count was reported on its own: %+v", f)
		}
	}
}

// ---- Finding 4: a manifest decoded from JSON keeps object identity. ----

func roundTrip(t *testing.T, m Manifest) Manifest {
	t.Helper()
	b, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	var out Manifest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func replay(t *testing.T, in EvaluateE04Input) EvaluateE04Input {
	t.Helper()
	in.Baseline = roundTrip(t, in.Baseline)
	for i := range in.Points {
		in.Points[i].Manifest = roundTrip(t, in.Points[i].Manifest)
	}
	return in
}

func TestE04JSONReplayKeepsIdentitySoARenameStaysWS03(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	in := e04In("TM-C", []Entry{a}, e04Points(0, withIno(fileEntry("b.txt", "A"), 1)))
	live := evalE04(in)
	wantVerdicts(t, live, only(VerdictEffectPresent, VerdictNoEffect, PredicateWS03))

	in = replay(t, in)
	if got := in.Baseline.Entries[0].Ino; got != 1 {
		t.Fatalf("decoded inode = %d, want 1", got)
	}
	wantVerdicts(t, evalE04(in), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS03))
}

func TestE04JSONReplayKeepsIdentitySoAHardLinkStaysWS04(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	in := e04In("TM-C", []Entry{a}, e04Points(0, withNlink(a, 2), withNlink(withIno(fileEntry("h.txt", "A"), 1), 2)))
	in = replay(t, in)
	wantVerdicts(t, evalE04(in), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS04))
}

func TestManifestDigestIgnoresIdentityButIdentityDigestDoesNot(t *testing.T) {
	a := manifest("baseline", withIno(fileEntry("f.txt", "A"), 1))
	b := manifest("baseline", withIno(fileEntry("f.txt", "A"), 77))
	b = withRootID(b, 9, 55)
	if a.Digest() != b.Digest() {
		t.Fatal("Digest depends on object identity")
	}
	if a.IdentityDigest() == b.IdentityDigest() {
		t.Fatal("IdentityDigest does not cover object identity")
	}
	if rt := roundTrip(t, a); rt.IdentityDigest() != a.IdentityDigest() || rt.Digest() != a.Digest() {
		t.Fatal("a round trip changed a digest")
	}
}

func TestE04ManifestWithoutIdentityNeverYieldsWS03NoEffectForADeleteAndCreatePair(t *testing.T) {
	a := fileEntry("a.txt", "A")
	b := fileEntry("b.txt", "A")
	in := e04In("TM-C", []Entry{a}, e04Points(0, b))
	r := evalE04(in)
	if got := r.VerdictOf(PredicateWS03); got == VerdictNoEffect || got == "" {
		t.Fatalf("WS-03 = %q for a delete+create pair without identity", got)
	}
	if got := r.VerdictOf(PredicateWS03); got != VerdictIndeterminate {
		t.Errorf("WS-03 = %q, want indeterminate", got)
	}
	for _, f := range r.Findings {
		if f.Predicate != "" {
			t.Errorf("finding %+v was credited to a predicate without identity", f)
		}
	}
	// No predicate-specific credit: nothing is effect_present.
	wantVerdicts(t, r, all4(VerdictIndeterminate))

	// A rename that also edited the file reads as a delete and a create with other
	// bytes. Without identity that cannot be ruled out either.
	r = evalE04(e04In("TM-C", []Entry{a}, e04Points(0, fileEntry("b.txt", "B"))))
	if got := r.VerdictOf(PredicateWS03); got != VerdictIndeterminate {
		t.Errorf("rename plus edit without identity: WS-03 = %q", got)
	}

	// With identity on both sides the pair is told apart.
	r = evalE04(e04In("TM-C", []Entry{withIno(a, 1)}, e04Points(0, withIno(b, 2))))
	wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01, PredicateWS02))
	// An identity on only one side is not enough to pair or to tell apart.
	r = evalE04(e04In("TM-C", []Entry{withIno(a, 1)}, e04Points(0, b)))
	if got := r.VerdictOf(PredicateWS03); got != VerdictIndeterminate {
		t.Errorf("one-sided identity: WS-03 = %q", got)
	}
}

func TestE04CreatedFileWithSeveralLinksAndNoIdentityIsAmbiguous(t *testing.T) {
	a := fileEntry("a.txt", "A")
	h := withNlink(fileEntry("h.txt", "A"), 2)
	r := evalE04(e04In("TM-C", []Entry{withNlink(a, 2)}, e04Points(0, withNlink(a, 2), h)))
	wantVerdicts(t, r, all4(VerdictIndeterminate))
	for _, f := range r.Findings {
		if f.Predicate != "" {
			t.Errorf("finding %+v was credited to a predicate", f)
		}
	}
}

// ---- Finding 5: ambiguity is indeterminate, never an attributed witness. ----

func TestE04VerdictConstantIsIndeterminate(t *testing.T) {
	if VerdictIndeterminate != "indeterminate" {
		t.Fatalf("VerdictIndeterminate = %q", VerdictIndeterminate)
	}
}

func TestE04EmptyDirectoryCreationIsIndeterminateForEveryPredicate(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	r := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, a, withIno(dirEntry("d"), 5))))
	wantVerdicts(t, r, all4(VerdictIndeterminate))
	if len(r.Unattributed) == 0 || len(r.Findings) == 0 {
		t.Fatalf("the change is not recorded: %+v %+v", r.Findings, r.Unattributed)
	}
	for _, p := range e04Preds {
		if r.VerdictOf(p) == VerdictEffectPresent {
			t.Errorf("%s = effect_present for a bare candidate", p)
		}
	}
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"verdict": "indeterminate"`) {
		t.Errorf("report JSON lacks the indeterminate verdict:\n%s", b)
	}
}

func TestE04AttributedFindingStaysEffectPresentBesideAnAmbiguousOne(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	r := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, withMode(a, 0o600), withIno(dirEntry("d"), 5))))
	// WS-04 is attributed (mode), the directory leaves the others indeterminate.
	wantVerdicts(t, r, map[string]string{PredicateWS01: VerdictIndeterminate, PredicateWS02: VerdictIndeterminate, PredicateWS03: VerdictIndeterminate, PredicateWS04: VerdictEffectPresent})
}

func TestE04GapsAndClassOutrankIndeterminate(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	in := e04In("TM-A", []Entry{a}, e04Points(0, a, withIno(dirEntry("d"), 5)))
	in.Points[2].Manifest.Complete = false
	wantVerdicts(t, evalE04(in), map[string]string{PredicateWS01: VerdictEvidenceLost, PredicateWS02: VerdictEvidenceLost, PredicateWS03: VerdictNotApplicable, PredicateWS04: VerdictEvidenceLost})
}

// ---- Finding 6: a scope boundary must not hide protected state. ----

func scoped(in EvaluateE04Input, paths ...string) EvaluateE04Input {
	in.Scope = E04Scope{Paths: paths}
	return in
}

func TestE04AncestorReplacedBySymlinkIsAnEffect(t *testing.T) {
	a := withIno(dirEntry("a"), 10)
	link := withIno(symlinkEntry("a", "/outside"), 20)
	in := scoped(e04In("TM-C", []Entry{a}, e04Points(0, link)), "a/b")
	r := evalE04(in)
	wantVerdicts(t, r, only(VerdictEffectPresent, VerdictIndeterminate, PredicateWS04))
	if len(r.Findings) != 1 || r.Findings[0].Path != "a" || r.Findings[0].Predicate != PredicateWS04 {
		t.Fatalf("findings = %+v", r.Findings)
	}
}

func TestE04AncestorReplacedBySameLookingDirectoryIsNotNoEffect(t *testing.T) {
	a := withIno(dirEntry("a"), 10)
	swapped := withIno(dirEntry("a"), 11)
	r := evalE04(scoped(e04In("TM-C", []Entry{a}, e04Points(0, swapped)), "a/b"))
	for _, p := range e04Preds {
		if r.VerdictOf(p) == VerdictNoEffect {
			t.Errorf("%s = no_effect after the ancestor was replaced", p)
		}
	}
	// The scoped child showing up beneath the new directory is an effect too.
	r = evalE04(scoped(e04In("TM-C", []Entry{a}, e04Points(0, swapped, withIno(fileEntry("a/b", "X"), 30))), "a/b"))
	if r.VerdictOf(PredicateWS01) == VerdictNoEffect {
		t.Errorf("WS-01 = no_effect, findings=%+v", r.Findings)
	}
}

func TestE04AncestorRemovedIsAnEffect(t *testing.T) {
	a := withIno(dirEntry("a"), 10)
	r := evalE04(scoped(e04In("TM-C", []Entry{a}, e04Points(0)), "a/b"))
	wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, PredicateWS02))
}

func TestE04DeepScopeChecksEveryAncestor(t *testing.T) {
	a, ab := withIno(dirEntry("a"), 10), withIno(dirEntry("a/b"), 11)
	link := withIno(symlinkEntry("a/b", "/outside"), 21)
	r := evalE04(scoped(e04In("TM-C", []Entry{a, ab}, e04Points(0, a, link)), "a/b/c"))
	wantVerdicts(t, r, only(VerdictEffectPresent, VerdictIndeterminate, PredicateWS04))
}

func TestE04AncestorThatCannotBeResolvedIsAnEvidenceGap(t *testing.T) {
	// A symlink ancestor in baseline and everywhere else: the scoped path cannot be
	// observed through it.
	link := withIno(symlinkEntry("a", "/outside"), 20)
	r := evalE04(scoped(e04In("TM-C", []Entry{link}, e04Points(0, link)), "a/b"))
	wantVerdicts(t, r, all4(VerdictEvidenceLost))
	if !hasGap(r.Gaps, GapScopeAncestor) {
		t.Errorf("gaps = %+v", r.Gaps)
	}
	// A directory ancestor with no identity cannot be told from its replacement.
	noID := dirEntry("a")
	r = evalE04(scoped(e04In("TM-C", []Entry{noID}, e04Points(0, noID)), "a/b"))
	wantVerdicts(t, r, all4(VerdictEvidenceLost))
	if !hasGap(r.Gaps, GapScopeAncestor) {
		t.Errorf("gaps = %+v", r.Gaps)
	}
}

func hasGap(gaps []Gap, kind string) bool {
	for _, g := range gaps {
		if g.Kind == kind {
			return true
		}
	}
	return false
}

func TestE04UnchangedAncestorAndItsMetadataDoNotCreateFindings(t *testing.T) {
	a := withIno(dirEntry("a"), 10)
	r := evalE04(scoped(e04In("TM-C", []Entry{a}, e04Points(0, withMode(a, 0o700))), "a/b"))
	wantVerdicts(t, r, all4(VerdictNoEffect))
	r = evalE04(scoped(e04In("TM-C", []Entry{a}, e04Points(0, a)), "a/b"))
	wantVerdicts(t, r, all4(VerdictNoEffect))
}

// ---- Finding 7: the protected root's own metadata is observed. ----

func rootWith(f func(*Entry)) *Entry {
	r := rootEntry()
	f(r)
	return r
}

func TestE04RootMetadataChangeIsAWS04Effect(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	for name, c := range map[string]func(*Entry){
		"mode": func(r *Entry) { r.Mode = 0o755 },
		"uid":  func(r *Entry) { r.UID = 0 },
		"gid":  func(r *Entry) { r.GID = 0 },
	} {
		in := e04In("TM-C", []Entry{a}, e04Points(0, a))
		in.Baseline.Root = rootWith(func(r *Entry) { r.Mode = 0o700 })
		for i := range in.Points {
			in.Points[i].Manifest.Root = rootWith(func(r *Entry) { r.Mode = 0o700 })
		}
		in.Points[3].Manifest.Root = rootWith(func(r *Entry) { r.Mode = 0o700; c(r) })
		r := evalE04(in)
		wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, PredicateWS04))
		if len(r.Findings) != 1 || r.Findings[0].Path != "" || r.Findings[0].Point != PointPrePublication || r.Findings[0].Meta == nil {
			t.Fatalf("%s: findings = %+v", name, r.Findings)
		}
	}
	// 0700 to 0755, as the review's counterexample.
	in := e04In("TM-C", []Entry{a}, e04Points(0, a))
	in.Baseline.Root = rootWith(func(r *Entry) { r.Mode = 0o700 })
	r := evalE04(in)
	wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, PredicateWS04))
	if f := r.Findings; len(f) != 1 || f[0].Meta.PreMode != 0o700 || f[0].Meta.PostMode != 0o755 {
		t.Fatalf("findings = %+v", f)
	}
}

func TestE04RootMetadataIsRequiredAtEveryManifest(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	cases := map[string]func(*EvaluateE04Input){
		"baseline": func(in *EvaluateE04Input) { in.Baseline.Root = nil },
		"point":    func(in *EvaluateE04Input) { in.Points[4].Manifest.Root = nil },
		"root not a dir": func(in *EvaluateE04Input) {
			in.Points[4].Manifest.Root = rootWith(func(r *Entry) { r.Type = TypeFile })
		},
		"root with path":  func(in *EvaluateE04Input) { in.Points[4].Manifest.Root = rootWith(func(r *Entry) { r.Path = "x" }) },
		"baseline no dir": func(in *EvaluateE04Input) { in.Baseline.Root = rootWith(func(r *Entry) { r.Type = "" }) },
	}
	for name, mutate := range cases {
		in := e04In("TM-C", []Entry{a}, e04Points(0, a))
		mutate(&in)
		r := evalE04(in)
		wantVerdicts(t, r, all4(VerdictEvidenceLost))
		if !hasGap(r.Gaps, GapRootMetadata) {
			t.Errorf("%s: gaps = %+v", name, r.Gaps)
		}
	}
}

func TestE04RootMetadataUnderAPartialScopeIsOnlyIdentityAndType(t *testing.T) {
	a := withIno(dirEntry("a"), 10)
	in := scoped(e04In("TM-C", []Entry{a}, e04Points(0, a)), "a/b")
	in.Points[2].Manifest.Root = rootWith(func(r *Entry) { r.Mode = 0o777 })
	wantVerdicts(t, evalE04(in), all4(VerdictNoEffect))
}

// ---- Finding 8: the claimed ledger prefix is bound by digest. ----

func ledgerFixture() (Entry, Entry, []LedgerEntry) {
	x := withIno(fileEntry("a.txt", "X"), 1)
	y := withIno(fileEntry("a.txt", "Y"), 1)
	return x, y, []LedgerEntry{approve("l1", 1, LedgerChange{Path: "a.txt", Before: entryPtr(x), After: entryPtr(y)})}
}

func TestE04PointMustCarryTheDigestOfTheLedgerPrefixItApplied(t *testing.T) {
	x, y, ledger := ledgerFixture()
	good := func() EvaluateE04Input {
		ps := e04Points(0, x)
		for i := 3; i < len(ps); i++ {
			ps[i].Manifest = manifest(ps[i].Name, y)
			ps[i].LedgerApplied = 1
		}
		in := e04In("TM-C", []Entry{x}, ps)
		in.Ledger = ledger
		for i := range in.Points {
			in.Points[i].LedgerPrefixDigest = LedgerPrefixDigest(ledger, in.Points[i].LedgerApplied)
		}
		return in
	}
	r := EvaluateE04(good())
	wantVerdicts(t, r, all4(VerdictNoEffect))

	forged := []LedgerEntry{approve("l1", 1, LedgerChange{Path: "a.txt", Before: entryPtr(x), After: entryPtr(withIno(fileEntry("a.txt", "Z"), 1))})}
	cases := map[string]func(*EvaluateE04Input){
		"missing digest": func(in *EvaluateE04Input) { in.Points[4].LedgerPrefixDigest = "" },
		"digest of the empty prefix for a claimed 1": func(in *EvaluateE04Input) {
			in.Points[4].LedgerPrefixDigest = LedgerPrefixDigest(ledger, 0)
		},
		"digest of a different ledger": func(in *EvaluateE04Input) {
			in.Points[4].LedgerPrefixDigest = LedgerPrefixDigest(forged, 1)
		},
		"digest of the full prefix for a claimed 0": func(in *EvaluateE04Input) {
			in.Points[0].LedgerPrefixDigest = LedgerPrefixDigest(ledger, 1)
		},
		"garbage digest": func(in *EvaluateE04Input) { in.Points[1].LedgerPrefixDigest = "not-a-digest" },
		"count with no digest": func(in *EvaluateE04Input) {
			in.Points[5].LedgerPrefixDigest = ""
			in.Points[5].LedgerApplied = 1
		},
	}
	for name, mutate := range cases {
		in := good()
		mutate(&in)
		r := EvaluateE04(in)
		wantVerdicts(t, r, all4(VerdictEvidenceLost))
		if !hasGap(r.Gaps, GapLedgerProvenance) {
			t.Errorf("%s: gaps = %+v", name, r.Gaps)
		}
	}
	// With no ledger at all a point still has to carry the (empty-prefix) digest.
	in := e04In("TM-C", []Entry{x}, e04Points(0, x))
	wantVerdicts(t, EvaluateE04(in), all4(VerdictEvidenceLost))
	if LedgerPrefixDigest(nil, 0) == "" || LedgerPrefixDigest(ledger, 2) != "" || LedgerPrefixDigest(ledger, -1) != "" {
		t.Error("LedgerPrefixDigest bounds")
	}
}

func TestOracleVersionIsV6(t *testing.T) {
	if OracleVersion != "wsoracle/v8" {
		t.Fatalf("OracleVersion = %q", OracleVersion)
	}
}
