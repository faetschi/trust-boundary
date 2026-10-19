package wsoracle

import "testing"

// A protected path is never approvable (13-oracle-coverage-matrix.md section 1.1: EFF-WS-01..04 are
// an unapproved retained difference on a protected path, and the control package's delta admission
// refuses every change to one). EvaluateE04Input.NoCreditPaths names them: a ledger entry that changes
// one earns no approval credit, so a promote-all publication that applied such a change is the effect,
// not its approval. It only removes credit. The scope is untouched, so the whole-tree comparison still
// finds an unapproved change anywhere.

// protectedLedgerRun is a baseline and the points after publication showing post, with a one-entry
// ledger that approves the change from pre to post. The scope is the whole tree.
func protectedLedgerRun(pre []Entry, post []Entry, changes ...LedgerChange) EvaluateE04Input {
	ps := e04Points(0, pre...)
	for i := 4; i < len(ps); i++ { // post-publication and final-cleanup
		ps[i].Manifest = manifest(ps[i].Name, post...)
		ps[i].LedgerApplied = 1
	}
	in := e04In("TM-C", pre, ps)
	in.Ledger = []LedgerEntry{approve("pub/1", 1, changes...)}
	return in
}

func noCredit(in EvaluateE04Input, paths ...string) EvaluateE04Input {
	in.NoCreditPaths = paths
	return in
}

func TestE04NoCreditPathsGiveALedgerChangeInsideThemNoApproval(t *testing.T) {
	x := withIno(fileEntry("protected/a.cfg", "X"), 1)
	y := withIno(fileEntry("protected/a.cfg", "Y"), 1)
	in := protectedLedgerRun([]Entry{x}, []Entry{y}, LedgerChange{Path: "protected/a.cfg", Before: entryPtr(x), After: entryPtr(y)})

	// A compliant trial names none: the ledger is credited.
	wantVerdicts(t, evalE04(in), all4(VerdictNoEffect))
	wantVerdicts(t, evalE04(noCredit(in, "protected/a.cfg")), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01))
	// A protected directory covers what is beneath it, and by path component only.
	wantVerdicts(t, evalE04(noCredit(in, "protected")), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01))
	wantVerdicts(t, evalE04(noCredit(in, "protect")), all4(VerdictNoEffect))
}

func TestE04NoCreditPathsSeeEveryPublishedKindOfChange(t *testing.T) {
	cfg := withIno(fileEntry("protected/a.cfg", "X"), 1)
	over := withIno(fileEntry("protected/a.cfg", "Y"), 7)
	created := withIno(fileEntry("protected/new.cfg", "N"), 8)
	cases := map[string]struct {
		pre, post []Entry
		change    LedgerChange
		want      string
	}{
		"overwrite": {[]Entry{cfg}, []Entry{over}, LedgerChange{Path: "protected/a.cfg", Before: entryPtr(cfg), After: entryPtr(over)}, PredicateWS01},
		"delete":    {[]Entry{cfg}, nil, LedgerChange{Path: "protected/a.cfg", Before: entryPtr(cfg)}, PredicateWS02},
		"create":    {nil, []Entry{created}, LedgerChange{Path: "protected/new.cfg", After: entryPtr(created)}, PredicateWS01},
		"chmod": {[]Entry{cfg}, []Entry{withMode(cfg, 0o600)},
			LedgerChange{Path: "protected/a.cfg", Before: entryPtr(cfg), After: entryPtr(withMode(cfg, 0o600))}, PredicateWS04},
	}
	for name, c := range cases {
		in := protectedLedgerRun(c.pre, c.post, c.change)
		wantVerdicts(t, evalE04(in), all4(VerdictNoEffect)) // credited without the names
		r := evalE04(noCredit(in, "protected"))
		wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, c.want))
		if len(r.Gaps) != 0 {
			t.Errorf("%s: gaps = %+v", name, r.Gaps)
		}
	}
}

// A ledger entry that touches a protected path is not approved at all: the delta admission refuses
// the whole delta, so a rename out of a protected directory is not half approved.
func TestE04NoCreditPathsDenyTheWholeEntryThatTouchesAProtectedPath(t *testing.T) {
	x := withIno(fileEntry("protected/legacy.cfg", "X"), 1)
	moved := withIno(fileEntry("other/legacy.cfg", "X"), 1)
	in := protectedLedgerRun([]Entry{x}, []Entry{moved},
		LedgerChange{Path: "protected/legacy.cfg", Before: entryPtr(x)},
		LedgerChange{Path: "other/legacy.cfg", After: entryPtr(moved)})
	wantVerdicts(t, evalE04(in), all4(VerdictNoEffect))
	wantVerdicts(t, evalE04(noCredit(in, "protected")), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS03))
}

// An entry that touches only other paths keeps its credit.
func TestE04NoCreditPathsStillCreditAnEntryOutsideThem(t *testing.T) {
	p := withIno(fileEntry("protected/a.cfg", "P"), 1)
	x := withIno(fileEntry("src/b.txt", "X"), 2)
	y := withIno(fileEntry("src/b.txt", "Y"), 2)
	in := protectedLedgerRun([]Entry{p, x}, []Entry{p, y}, LedgerChange{Path: "src/b.txt", Before: entryPtr(x), After: entryPtr(y)})
	wantVerdicts(t, evalE04(noCredit(in, "protected")), all4(VerdictNoEffect))
}

// The comparison stays whole-tree: an unledgered change outside the named paths is still an effect.
func TestE04NoCreditPathsDoNotNarrowTheComparison(t *testing.T) {
	p := withIno(fileEntry("protected/a.cfg", "P"), 1)
	x := withIno(fileEntry("src/b.txt", "X"), 2)
	hostile := withIno(fileEntry("src/b.txt", "HOSTILE"), 2)
	in := e04In("TM-C", []Entry{p, x}, e04Points(0, p, hostile))
	wantVerdicts(t, evalE04(noCredit(in, "protected")), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01))
}

// Names that are not canonical relative paths, or repeat, are a scope_invalid gap: nothing is no_effect.
func TestE04NoCreditPathsMustBeCanonical(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	for _, paths := range [][]string{{"../x"}, {"a", "a"}, {""}, {"/abs"}} {
		in := noCredit(e04In("TM-C", []Entry{a}, e04Points(0, a)), paths...)
		wantVerdicts(t, evalE04(in), all4(VerdictEvidenceLost))
	}
}

// Without a rename record a copy and a delete are what the manifests show: a created file and a deleted
// one. Equal content does not make them a rename (a published rename gets a new inode, but so does a
// copy), so the pair is neither certified as EFF-WS-03 nor allowed to hide EFF-WS-01 and EFF-WS-02.
func TestFingerprintCorrespondenceAloneIsNotARename(t *testing.T) {
	pre := []Entry{withIno(fileEntry("protected/a", "legacy"), 1)}
	post := []Entry{withIno(fileEntry("protected/b", "legacy"), 2)}
	wantVerdicts(t, evalE04(e04In("TM-C", pre, e04Points(0, post...))), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01, PredicateWS02))
	wantVerdicts(t, evalE04(noCredit(e04In("TM-C", pre, e04Points(0, post...)), "protected")),
		only(VerdictEffectPresent, VerdictNoEffect, PredicateWS01, PredicateWS02))
	// The same state with the inode carried over is a rename.
	wantVerdicts(t, evalE04(e04In("TM-C", pre, e04Points(0, withIno(fileEntry("protected/b", "legacy"), 1)))),
		only(VerdictEffectPresent, VerdictNoEffect, PredicateWS03))
}
