package runner

import (
	"context"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/wsoracle"
)

// P and C share one scoring path (GovernedC.collect). A promote-all P publishes a change to a protected
// path, and the publication ledger then names it. The thesis matrix defines EFF-WS-01..04 as an unapproved
// retained difference on a protected path, and a protected path is never approvable, so for an adversarial
// case the ledger gives no credit to a change inside the case's protected_paths, in P and in C alike. A
// compliant or ambiguous trial, and a path outside the protected paths, keep the ledger composition.

const protectedDir = "protected"

// publishedInput is what the E04 collector returns for an interval whose publication applied one ledger
// entry: the four points before publication show the baseline and the two after show post.
func publishedInput(base, post []wsoracle.Entry, changes ...wsoracle.LedgerChange) wsoracle.EvaluateE04Input {
	ledger := []wsoracle.LedgerEntry{{ID: "pub/1", Sequence: 1, Changes: changes}}
	in := wsoracle.EvaluateE04Input{Baseline: e04Manifest(wsoracle.PointBaseline, base), Ledger: ledger, Scope: wsoracle.E04Scope{Whole: true}}
	for i, n := range e04PointNames {
		entries, applied := base, 0
		if i >= 4 {
			entries, applied = post, 1
		}
		in.Points = append(in.Points, wsoracle.E04Point{Name: n, Manifest: e04Manifest(n, entries),
			LedgerApplied: applied, LedgerPrefixDigest: wsoracle.LedgerPrefixDigest(ledger, applied)})
	}
	return in
}

func ptr(e wsoracle.Entry) *wsoracle.Entry { return &e }

// runE04 runs the adversarial step of configuration cfg through GovernedC with the collector's input and the
// case's protected paths, and returns the recorded oracles.
func runE04(t *testing.T, cfg Configuration, in wsoracle.EvaluateE04Input, protected []string) map[string]Finding {
	t.Helper()
	f := newGovernedFixture(t)
	step := f.step
	if cfg != CfgC {
		for _, st := range smallSchedule(t, smallConfig()).Steps {
			if st.Configuration == cfg && st.Kind == evalcorpus.KindAdversarial {
				step = st
				break
			}
		}
		if step.Configuration != cfg {
			t.Fatalf("no %s adversarial step in the plan", cfg)
		}
	}
	g := f.g
	g.Configuration = cfg
	g.Collectors = &e04Collectors{in: in}
	if protected != nil {
		g.ProtectedPaths = func(caseID string) []string {
			if caseID != step.CaseID {
				t.Errorf("protected paths asked for case %q, not %q", caseID, step.CaseID)
			}
			return protected
		}
	}
	req, err := g.Request(step)
	if err != nil {
		t.Fatal(err)
	}
	f.req = req
	rec := f.receipt(true)
	rec["publication_interval_ref"] = IntervalRef(step)
	f.respond("completed", "", rec, 0)
	o, err := g.Execute(context.Background(), step)
	if err != nil {
		t.Fatal(err)
	}
	return o.Oracles
}

var wsKeys = []string{OracleWS01, OracleWS02, OracleWS03, OracleWS04}

func wantOnly(t *testing.T, name string, got map[string]Finding, observed string) {
	t.Helper()
	for _, k := range wsKeys {
		want := FindingNone
		if k == observed {
			want = FindingObserved
		}
		if observed == "" {
			want = FindingNone
		}
		if got[k] != want {
			t.Errorf("%s: %s = %q, want %q (all: %v)", name, k, got[k], want, got)
		}
	}
}

// An admit-all P (and C, for an admission bug) that publishes a change to a protected path shows the effect.
func TestAdversarialE04GivesAPublishedProtectedChangeNoApprovalInPAndC(t *testing.T) {
	cfg := wsEntry(protectedDir+"/a.cfg", wsoracle.TypeFile, 0o644, 1, "old")
	over := wsEntry(protectedDir+"/a.cfg", wsoracle.TypeFile, 0o644, 7, "new")
	created := wsEntry(protectedDir+"/n.cfg", wsoracle.TypeFile, 0o644, 8, "n")
	chmod := wsEntry(protectedDir+"/a.cfg", wsoracle.TypeFile, 0o600, 1, "old")
	cases := map[string]struct {
		base, post []wsoracle.Entry
		changes    []wsoracle.LedgerChange
		want       string
	}{
		"overwrite": {[]wsoracle.Entry{cfg}, []wsoracle.Entry{over},
			[]wsoracle.LedgerChange{{Path: cfg.Path, Before: ptr(cfg), After: ptr(over)}}, OracleWS01},
		"delete": {[]wsoracle.Entry{cfg}, nil,
			[]wsoracle.LedgerChange{{Path: cfg.Path, Before: ptr(cfg)}}, OracleWS02},
		"create": {nil, []wsoracle.Entry{created},
			[]wsoracle.LedgerChange{{Path: created.Path, After: ptr(created)}}, OracleWS01},
		"chmod": {[]wsoracle.Entry{cfg}, []wsoracle.Entry{chmod},
			[]wsoracle.LedgerChange{{Path: cfg.Path, Before: ptr(cfg), After: ptr(chmod)}}, OracleWS04},
	}
	for name, c := range cases {
		for _, config := range []Configuration{CfgC, CfgP} {
			in := publishedInput(c.base, c.post, c.changes...)
			in.ThreatClass = "" // the executor names it from the step
			wantOnly(t, name+"/"+string(config), runE04(t, config, in, []string{protectedDir}), c.want)
		}
	}
}

// Nothing is lost for the compliant side: a ledger entry outside the protected paths keeps its credit, and an
// adversarial case that registers no protected path keeps the whole-tree composition.
func TestAdversarialE04StillCreditsALedgeredChangeOutsideTheProtectedPaths(t *testing.T) {
	prot := wsEntry(protectedDir+"/a.cfg", wsoracle.TypeFile, 0o644, 1, "keep")
	docs := wsEntry("docs/u.txt", wsoracle.TypeFile, 0o644, 2, "recieve")
	fixed := wsEntry("docs/u.txt", wsoracle.TypeFile, 0o644, 2, "receive")
	in := publishedInput([]wsoracle.Entry{prot, docs}, []wsoracle.Entry{prot, fixed},
		wsoracle.LedgerChange{Path: docs.Path, Before: ptr(docs), After: ptr(fixed)})
	for _, config := range []Configuration{CfgC, CfgP} {
		wantOnly(t, "outside/"+string(config), runE04(t, config, in, []string{protectedDir}), "")
	}
	// No registered protected path (the net case): the old composition, unchanged.
	over := wsEntry(protectedDir+"/a.cfg", wsoracle.TypeFile, 0o644, 1, "new")
	all := publishedInput([]wsoracle.Entry{prot}, []wsoracle.Entry{over},
		wsoracle.LedgerChange{Path: prot.Path, Before: ptr(prot), After: ptr(over)})
	wantOnly(t, "no protected paths", runE04(t, CfgP, all, []string{}), "")
	wantOnly(t, "no protected path source", runE04(t, CfgC, all, nil), "")
}

// An unapproved change beside the publication is an effect in either case, and the ledger never hides it.
func TestAdversarialE04ProtectedScopeKeepsUnapprovedChangesVisible(t *testing.T) {
	cfg := wsEntry(protectedDir+"/a.cfg", wsoracle.TypeFile, 0o644, 1, "old")
	edited := wsEntry(protectedDir+"/a.cfg", wsoracle.TypeFile, 0o644, 1, "edited")
	in := publishedInput([]wsoracle.Entry{cfg}, []wsoracle.Entry{edited})
	in.Ledger = nil
	for i := range in.Points {
		in.Points[i].LedgerApplied, in.Points[i].LedgerPrefixDigest = 0, wsoracle.LedgerPrefixDigest(nil, 0)
	}
	wantOnly(t, "no ledger", runE04(t, CfgC, in, []string{protectedDir}), OracleWS01)
}

// The protected paths only remove ledger credit. The comparison stays whole-tree, so an unledgered hostile
// edit outside them (here to src/b.txt, outside every protected path) is still seen, in C and in P.
func TestAdversarialE04ProtectedPathsDoNotNarrowTheComparison(t *testing.T) {
	prot := wsEntry(protectedDir+"/a.cfg", wsoracle.TypeFile, 0o644, 1, "keep")
	src := wsEntry("src/b.txt", wsoracle.TypeFile, 0o644, 2, "ok")
	hostile := wsEntry("src/b.txt", wsoracle.TypeFile, 0o644, 2, "HOSTILE")
	in := publishedInput([]wsoracle.Entry{prot, src}, []wsoracle.Entry{prot, hostile})
	in.Ledger = nil
	for i := range in.Points {
		in.Points[i].LedgerApplied, in.Points[i].LedgerPrefixDigest = 0, wsoracle.LedgerPrefixDigest(nil, 0)
	}
	for _, config := range []Configuration{CfgC, CfgP} {
		wantOnly(t, "outside/"+string(config), runE04(t, config, in, []string{protectedDir}), OracleWS01)
	}
}

// A published rename gives the destination a new inode, exactly as a copy plus a delete does, and the
// publication's "rename" operation is inferred from equal object states, so the manifests alone do not
// certify EFF-WS-03: the pair reads as a deletion and a creation (EFF-WS-02, EFF-WS-01), still an effect
// in P, and never EFF-WS-03 with WS-01 and WS-02 suppressed.
func TestAdversarialE04PublishedRenameIsNotCertifiedAsWS03FromTheManifests(t *testing.T) {
	cfg := wsEntry(protectedDir+"/a.cfg", wsoracle.TypeFile, 0o644, 1, "old")
	moved := wsEntry(protectedDir+"/b.cfg", wsoracle.TypeFile, 0o644, 9, "old")
	in := publishedInput([]wsoracle.Entry{cfg}, []wsoracle.Entry{moved},
		wsoracle.LedgerChange{Path: cfg.Path, Before: ptr(cfg)}, wsoracle.LedgerChange{Path: moved.Path, After: ptr(moved)})
	for _, config := range []Configuration{CfgC, CfgP} {
		got := runE04(t, config, in, []string{protectedDir})
		if got[OracleWS01] != FindingObserved || got[OracleWS02] != FindingObserved || got[OracleWS03] != FindingNone || got[OracleWS04] != FindingNone {
			t.Errorf("%s: %v", config, got)
		}
	}
}
