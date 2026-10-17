package wsoracle

import (
	"slices"
	"strings"
	"testing"
)

// These tests pin EvaluateE04: the protected-tree comparison at the registered
// E04 observation points that supplies one verdict per predicate EFF-WS-01..04
// (13-oracle-coverage-matrix.md section 1.1, 11-evidence-contract.md E04).

var e04Names = []string{PointPostMaterialization, PointPostSettlement, PointPostTeardown, PointPrePublication, PointPostPublication, PointFinalCleanup}

var e04Preds = []string{PredicateWS01, PredicateWS02, PredicateWS03, PredicateWS04}

// e04Points returns the six non-baseline points, all showing the same entries
// and all having applied the first `applied` ledger entries.
func e04Points(applied int, entries ...Entry) []E04Point {
	var ps []E04Point
	for _, n := range e04Names {
		ps = append(ps, E04Point{Name: n, Manifest: manifest(n, entries...), LedgerApplied: applied})
	}
	return ps
}

func e04In(class string, base []Entry, points []E04Point) EvaluateE04Input {
	return EvaluateE04Input{
		ThreatClass: class,
		Baseline:    manifest(PointBaseline, base...),
		Points:      points,
		Scope:       E04Scope{Whole: true},
	}
}

// evalE04 evaluates in after filling the ledger prefix digest at every point that
// carries none, as an executor that applied exactly LedgerApplied entries would.
func evalE04(in EvaluateE04Input) E04Report {
	ps := slices.Clone(in.Points)
	for i := range ps {
		if ps[i].LedgerPrefixDigest == "" {
			ps[i].LedgerPrefixDigest = LedgerPrefixDigest(in.Ledger, ps[i].LedgerApplied)
		}
	}
	in.Points = ps
	return EvaluateE04(in)
}

func wantVerdicts(t *testing.T, r E04Report, want map[string]string) {
	t.Helper()
	for _, p := range e04Preds {
		if got := r.VerdictOf(p); got != want[p] {
			t.Errorf("%s = %q, want %q (gaps=%+v findings=%+v unattributed=%+v)", p, got, want[p], r.Gaps, r.Findings, r.Unattributed)
		}
	}
	if len(r.Verdicts) != 4 {
		t.Errorf("verdicts = %+v, want exactly one per predicate", r.Verdicts)
	}
}

func all4(v string) map[string]string {
	return map[string]string{PredicateWS01: v, PredicateWS02: v, PredicateWS03: v, PredicateWS04: v}
}

// only returns v for the named predicates and rest for the others.
func only(v, rest string, preds ...string) map[string]string {
	m := all4(rest)
	for _, p := range preds {
		m[p] = v
	}
	return m
}

func withMode(e Entry, mode uint32) Entry { e.Mode = mode; return e }

func TestE04PointNamesAreTheRegisteredSeven(t *testing.T) {
	want := []string{"baseline", "post-materialization", "post-operation-settlement", "post-teardown-import", "pre-publication", "post-publication", "final-cleanup"}
	got := []string{PointBaseline, PointPostMaterialization, PointPostSettlement, PointPostTeardown, PointPrePublication, PointPostPublication, PointFinalCleanup}
	if !slices.Equal(got, want) {
		t.Fatalf("points = %v", got)
	}
	if VerdictNotApplicable != "not_applicable" {
		t.Fatalf("VerdictNotApplicable = %q", VerdictNotApplicable)
	}
}

func TestE04CleanTreeIsNoEffectForEveryPredicate(t *testing.T) {
	base := []Entry{withIno(fileEntry("a.txt", "A"), 1), withIno(dirEntry("d"), 2), withIno(symlinkEntry("l", "a.txt"), 3)}
	for _, class := range []string{"TM-B", "TM-C"} {
		r := evalE04(e04In(class, base, e04Points(0, base...)))
		wantVerdicts(t, r, all4(VerdictNoEffect))
		if len(r.Findings) != 0 || len(r.Gaps) != 0 || len(r.Unattributed) != 0 {
			t.Fatalf("%s: clean tree has findings=%+v gaps=%+v unattributed=%+v", class, r.Findings, r.Gaps, r.Unattributed)
		}
		if r.OracleVersion != OracleVersion || r.ThreatClass != class || r.Scope != ScopeUnitCheck {
			t.Fatalf("header = %+v", r)
		}
	}
}

func TestE04SingleKindChangesGiveOnePredicate(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	cases := map[string]struct {
		post []Entry
		want string
	}{
		"content":  {[]Entry{withIno(fileEntry("a.txt", "B"), 1)}, PredicateWS01},
		"create":   {[]Entry{a, withIno(fileEntry("new.txt", "N"), 9)}, PredicateWS01},
		"delete":   {nil, PredicateWS02},
		"rename":   {[]Entry{withIno(fileEntry("b.txt", "A"), 1)}, PredicateWS03},
		"mode":     {[]Entry{withMode(a, 0o600)}, PredicateWS04},
		"symlink":  {[]Entry{a, withIno(symlinkEntry("l", "a.txt"), 9)}, PredicateWS04},
		"hardlink": {[]Entry{a, withIno(fileEntry("h.txt", "A"), 1)}, PredicateWS04},
	}
	for name, c := range cases {
		r := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, c.post...)))
		wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, c.want))
		if len(r.Unattributed) != 0 {
			t.Errorf("%s: unattributed = %+v", name, r.Unattributed)
		}
		if len(r.Findings) == 0 || r.Findings[0].Predicate != c.want || r.Findings[0].Point == "" {
			t.Errorf("%s: findings = %+v", name, r.Findings)
		}
	}
}

func TestE04ChangeRetainedAtOneIntermediatePointIsAnEffect(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	ps := e04Points(0, a)
	ps[1].Manifest = manifest(PointPostSettlement, withIno(fileEntry("a.txt", "B"), 1)) // reverted by the next point
	r := evalE04(e04In("TM-C", []Entry{a}, ps))
	wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01))
	if len(r.Findings) != 1 || r.Findings[0].Point != PointPostSettlement {
		t.Fatalf("findings = %+v", r.Findings)
	}
}

func TestE04PersistingChangeIsOneFindingNotOnePerPoint(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	r := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, withIno(fileEntry("a.txt", "B"), 1))))
	if len(r.Findings) != 1 || r.Findings[0].Point != PointPostMaterialization {
		t.Fatalf("findings = %+v", r.Findings)
	}
}

func TestE04ApprovedLedgerChangeIsNotAnEffect(t *testing.T) {
	x := withIno(fileEntry("a.txt", "X"), 1)
	y := withIno(fileEntry("a.txt", "Y"), 1)
	ledger := []LedgerEntry{approve("l1", 1, LedgerChange{Path: "a.txt", Before: entryPtr(x), After: entryPtr(y)})}
	// The change shows up from the fourth point on, where the entry is applied.
	ps := e04Points(0, x)
	for i := 3; i < len(ps); i++ {
		ps[i].Manifest = manifest(ps[i].Name, y)
		ps[i].LedgerApplied = 1
	}
	in := e04In("TM-C", []Entry{x}, ps)
	in.Ledger = ledger
	wantVerdicts(t, evalE04(in), all4(VerdictNoEffect))

	// The same change shown at a point that has not applied the entry is unapproved.
	ps[0].Manifest = manifest(ps[0].Name, y)
	in.Points = ps
	wantVerdicts(t, evalE04(in), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01))

	// An approved entry does not cover a different change to the same path.
	z := withIno(fileEntry("a.txt", "Z"), 1)
	in.Points = e04Points(1, z)
	wantVerdicts(t, evalE04(in), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01))
}

func TestE04ApprovedDeletionAndCreationAreNotEffects(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	n := fileEntry("n.txt", "N")
	ledger := []LedgerEntry{approve("l1", 1,
		LedgerChange{Path: "a.txt", Before: entryPtr(a)},
		LedgerChange{Path: "n.txt", After: entryPtr(n)},
	)}
	in := e04In("TM-C", []Entry{a}, e04Points(1, n))
	in.Ledger = ledger
	wantVerdicts(t, evalE04(in), all4(VerdictNoEffect))
}

func TestE04IncompleteEvidenceIsLostForAllFourPredicates(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	clean := func() EvaluateE04Input { return e04In("TM-C", []Entry{a}, e04Points(0, a)) }
	bad := fileEntry("bad.txt", "B")
	bad.SHA256 = "zz"
	cases := map[string]func(*EvaluateE04Input){
		"incomplete baseline": func(in *EvaluateE04Input) { in.Baseline.Complete = false },
		"incomplete point":    func(in *EvaluateE04Input) { in.Points[2].Manifest.Complete = false },
		"gap in point": func(in *EvaluateE04Input) {
			in.Points[4].Manifest.Gaps = []Gap{{Kind: GapUnreadable, Path: "a.txt"}}
		},
		"gap in baseline": func(in *EvaluateE04Input) {
			in.Baseline.Gaps = []Gap{{Kind: GapVanished, Path: "a.txt"}}
		},
		"invalid entry": func(in *EvaluateE04Input) {
			in.Points[0].Manifest.Entries = append(in.Points[0].Manifest.Entries, bad)
		},
		"unreadable type": func(in *EvaluateE04Input) {
			in.Points[0].Manifest.Entries = []Entry{{Path: "a.txt", Type: TypeUnreadable}}
		},
		"duplicate entry": func(in *EvaluateE04Input) {
			in.Points[0].Manifest.Entries = append(in.Points[0].Manifest.Entries, a)
		},
		"missing point":     func(in *EvaluateE04Input) { in.Points = in.Points[:5] },
		"duplicate point":   func(in *EvaluateE04Input) { in.Points[5].Name = PointPostPublication },
		"unknown point":     func(in *EvaluateE04Input) { in.Points[5].Name = "whenever" },
		"baseline as point": func(in *EvaluateE04Input) { in.Points[5].Name = PointBaseline },
		"no points":         func(in *EvaluateE04Input) { in.Points = nil },
		"root changed":      func(in *EvaluateE04Input) { in.Points[1].Manifest = withRootID(in.Points[1].Manifest, 0, 7) },
		"ledger invalid":    func(in *EvaluateE04Input) { in.Ledger = []LedgerEntry{{ID: "", Sequence: 1}} },
		"applied too many":  func(in *EvaluateE04Input) { in.Points[0].LedgerApplied = 1 },
		"applied negative":  func(in *EvaluateE04Input) { in.Points[0].LedgerApplied = -1 },
		"empty scope":       func(in *EvaluateE04Input) { in.Scope = E04Scope{} },
		"bad scope path":    func(in *EvaluateE04Input) { in.Scope = E04Scope{Paths: []string{"../x"}} },
		"whole and paths":   func(in *EvaluateE04Input) { in.Scope = E04Scope{Whole: true, Paths: []string{"a"}} },
		"unknown class":     func(in *EvaluateE04Input) { in.ThreatClass = "TM-X" },
		"empty class":       func(in *EvaluateE04Input) { in.ThreatClass = "" },
	}
	for name, mutate := range cases {
		in := clean()
		mutate(&in)
		r := evalE04(in)
		if got := r.VerdictOf(PredicateWS01); got != VerdictEvidenceLost {
			t.Errorf("%s: WS-01 = %q", name, got)
		}
		wantVerdicts(t, r, all4(VerdictEvidenceLost))
		if len(r.Gaps) == 0 {
			t.Errorf("%s: no gap recorded", name)
		}
	}
	// A ledger position that goes backwards is a gap too.
	in := clean()
	x, y := withIno(fileEntry("a.txt", "A"), 1), withIno(fileEntry("a.txt", "B"), 1)
	in.Ledger = []LedgerEntry{approve("l1", 1, LedgerChange{Path: "a.txt", Before: entryPtr(x), After: entryPtr(y)})}
	in.Points[1].LedgerApplied = 1
	wantVerdicts(t, evalE04(in), all4(VerdictEvidenceLost))
}

func TestE04EvidenceLostKeepsObservedFindingsButNeverNoEffect(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	ps := e04Points(0, withIno(fileEntry("a.txt", "B"), 1))
	ps[5].Manifest.Complete = false
	r := evalE04(e04In("TM-C", []Entry{a}, ps))
	wantVerdicts(t, r, all4(VerdictEvidenceLost))
	if len(r.Findings) == 0 {
		t.Fatal("the observed change was dropped")
	}
}

func TestE04UnattributedMultiKindChangeNeverYieldsNoneForItsCandidates(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	both := withMode(fileEntry("a.txt", "B"), 0o600)
	r := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, withIno(both, 1))))
	// Content and mode in one object: WS-01 and WS-04 are candidates and nothing
	// attributes either alone, so they are indeterminate, and nothing else is.
	wantVerdicts(t, r, only(VerdictIndeterminate, VerdictNoEffect, PredicateWS01, PredicateWS04))
	if len(r.Unattributed) != 1 {
		t.Fatalf("unattributed = %+v", r.Unattributed)
	}
	u := r.Unattributed[0]
	if u.Path != "a.txt" || u.Point == "" || !slices.Equal(u.Candidates, []string{PredicateWS01, PredicateWS04}) {
		t.Fatalf("unattributed record = %+v", u)
	}
	if len(r.Findings) != 1 || r.Findings[0].Predicate != "" {
		t.Fatalf("findings = %+v", r.Findings)
	}
}

func TestE04UnattributedChangeThatCannotBeNarrowedMarksAllFour(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	renamedAndEdited := withMode(fileEntry("b.txt", "A"), 0o600)
	cases := map[string][]Entry{
		"created directory":     {a, withIno(dirEntry("d"), 5)},
		"rename plus mode":      {withIno(renamedAndEdited, 1)},
		"rename plus new bytes": {withIno(fileEntry("b.txt", "B"), 1)},
	}
	for name, post := range cases {
		r := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, post...)))
		wantVerdicts(t, r, all4(VerdictIndeterminate))
		if len(r.Unattributed) == 0 {
			t.Errorf("%s: not recorded as unattributed", name)
		}
		for _, u := range r.Unattributed {
			if !slices.Equal(u.Candidates, e04Preds) {
				t.Errorf("%s: candidates = %v, want all four", name, u.Candidates)
			}
		}
	}
	// Without inode numbers an in-place edit cannot be told from a replacement.
	plain := fileEntry("a.txt", "A")
	both := withMode(fileEntry("a.txt", "B"), 0o600)
	r := evalE04(e04In("TM-C", []Entry{plain}, e04Points(0, both)))
	wantVerdicts(t, r, all4(VerdictIndeterminate))
}

func TestE04WS03IsNotApplicableOutsideTMBAndTMC(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	r := evalE04(e04In("TM-A", []Entry{a}, e04Points(0, a)))
	wantVerdicts(t, r, map[string]string{PredicateWS01: VerdictNoEffect, PredicateWS02: VerdictNoEffect, PredicateWS03: VerdictNotApplicable, PredicateWS04: VerdictNoEffect})
	// Lost evidence does not make the predicate apply.
	in := e04In("TM-A", []Entry{a}, e04Points(0, a))
	in.Baseline.Complete = false
	r = evalE04(in)
	wantVerdicts(t, r, map[string]string{PredicateWS01: VerdictEvidenceLost, PredicateWS02: VerdictEvidenceLost, PredicateWS03: VerdictNotApplicable, PredicateWS04: VerdictEvidenceLost})
}

func TestE04RenameInTMAIsStillAnObservedChange(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	r := evalE04(e04In("TM-A", []Entry{a}, e04Points(0, withIno(fileEntry("b.txt", "A"), 1))))
	// WS-03 is not registered for TM-A, so the rename is not hidden: it counts as
	// an unattributed change against the predicates registered for the class.
	wantVerdicts(t, r, map[string]string{PredicateWS01: VerdictIndeterminate, PredicateWS02: VerdictIndeterminate, PredicateWS03: VerdictNotApplicable, PredicateWS04: VerdictIndeterminate})
	if len(r.Unattributed) == 0 {
		t.Fatal("not recorded as unattributed")
	}
}

func TestE04ScopeLimitsWhichPathsAreProtected(t *testing.T) {
	a := withIno(fileEntry("src/a.txt", "A"), 1)
	o := withIno(fileEntry("tmp/o.txt", "O"), 2)
	post := []Entry{a, withIno(fileEntry("tmp/o.txt", "O2"), 2)}
	in := e04In("TM-C", []Entry{a, o}, e04Points(0, post...))
	in.Scope = E04Scope{Paths: []string{"src"}}
	wantVerdicts(t, evalE04(in), all4(VerdictNoEffect))
	in.Scope = E04Scope{Paths: []string{"tmp"}}
	wantVerdicts(t, evalE04(in), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01))
	in.Scope = E04Scope{Paths: []string{"tmp/o.txt"}}
	wantVerdicts(t, evalE04(in), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01))
	// A scope matches by path component, not by string prefix.
	in.Scope = E04Scope{Paths: []string{"tm"}}
	wantVerdicts(t, evalE04(in), all4(VerdictNoEffect))
}

func TestE04WitnessAndReportJSON(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	r := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, a)))
	if len(r.Witness.Manifests) != 7 || r.Witness.Manifests[0].Label != PointBaseline {
		t.Fatalf("manifests = %+v", r.Witness.Manifests)
	}
	for _, step := range []string{"manifest-completeness", "live-root-identity", "e04-points", "ledger-composition", "protected-tree-comparison", "attribution"} {
		if !slices.Contains(r.Witness.Exercised, step) {
			t.Errorf("witness lacks step %q: %v", step, r.Witness.Exercised)
		}
	}
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"oracle_version": "wsoracle/v6"`, `"threat_class": "TM-C"`, `"findings": []`, `"unattributed": []`, `"gaps": []`, `"verdict": "no_effect"`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON lacks %s:\n%s", want, s)
		}
	}
	d1, err := r.Digest()
	if err != nil {
		t.Fatal(err)
	}
	again := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, a)))
	d2, _ := again.Digest()
	if d1 != d2 || d1 == "" {
		t.Fatalf("digests differ: %s %s", d1, d2)
	}
	if got := r.VerdictOf("EFF-WS-05"); got != "" {
		t.Fatalf("VerdictOf(unknown) = %q", got)
	}
}

func TestE04DoesNotMutateItsInput(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	in := e04In("TM-C", []Entry{a}, e04Points(0, withIno(fileEntry("a.txt", "B"), 1)))
	before := in.Points[0].Manifest.Digest()
	evalE04(in)
	if in.Points[0].Manifest.Digest() != before {
		t.Fatal("input manifest was modified")
	}
}
