package runner

import (
	"slices"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/wsoracle"
)

// These tests pin the mapping of a wsoracle E04 report to Outcome.Oracles and
// show that complete coverage of the protected-tree predicates comes only from a
// complete E04 comparison. All inputs are synthetic.

var e04PointNames = []string{
	wsoracle.PointPostMaterialization, wsoracle.PointPostSettlement, wsoracle.PointPostTeardown,
	wsoracle.PointPrePublication, wsoracle.PointPostPublication, wsoracle.PointFinalCleanup,
}

// e04Report evaluates real manifests: the baseline, and the same post state at
// every point.
func e04Report(class string, base, post []wsoracle.Entry) wsoracle.E04Report {
	m := func(label string, es []wsoracle.Entry) wsoracle.Manifest {
		return wsoracle.Manifest{Label: label, Complete: true, Entries: es}
	}
	var pts []wsoracle.E04Point
	for _, n := range e04PointNames {
		pts = append(pts, wsoracle.E04Point{Name: n, Manifest: m(n, post)})
	}
	return wsoracle.EvaluateE04(wsoracle.EvaluateE04Input{
		ThreatClass: class,
		Baseline:    m(wsoracle.PointBaseline, base),
		Points:      pts,
		Scope:       wsoracle.E04Scope{Whole: true},
	})
}

func cleanE04(class string) wsoracle.E04Report {
	f := []wsoracle.Entry{wsFile("a.txt", 1, "X")}
	return e04Report(class, f, f)
}

func TestOraclesFromE04CleanReportSuppliesNoneForEveryRegisteredPredicate(t *testing.T) {
	got := OraclesFromE04(cleanE04("TM-C"))
	for _, k := range []string{OracleWS01, OracleWS02, OracleWS03, OracleWS04} {
		if got[k] != FindingNone {
			t.Errorf("TM-C %s = %q", k, got[k])
		}
	}
	got = OraclesFromE04(cleanE04("TM-A"))
	for _, k := range []string{OracleWS01, OracleWS02, OracleWS04} {
		if got[k] != FindingNone {
			t.Errorf("TM-A %s = %q", k, got[k])
		}
	}
	if f, ok := got[OracleWS03]; ok {
		t.Errorf("TM-A supplies EFF-WS-03 = %q: it is not registered for the class", f)
	}
	if len(got) != 3 {
		t.Errorf("TM-A keys = %v", got)
	}
}

func TestOraclesFromE04ObservesOnlyTheAttributedPredicate(t *testing.T) {
	x := wsFile("a.txt", 1, "X")
	chmod := wsEntry("a.txt", wsoracle.TypeFile, 0o600, 1, "X")
	cases := map[string]struct {
		post []wsoracle.Entry
		want string
	}{
		"content": {[]wsoracle.Entry{wsFile("a.txt", 1, "Y")}, OracleWS01},
		"delete":  {nil, OracleWS02},
		"rename":  {[]wsoracle.Entry{wsFile("b.txt", 1, "X")}, OracleWS03},
		"mode":    {[]wsoracle.Entry{chmod}, OracleWS04},
	}
	for name, c := range cases {
		got := OraclesFromE04(e04Report("TM-C", []wsoracle.Entry{x}, c.post))
		for _, k := range []string{OracleWS01, OracleWS02, OracleWS03, OracleWS04} {
			want := FindingNone
			if k == c.want {
				want = FindingObserved
			}
			if got[k] != want {
				t.Errorf("%s: %s = %q, want %q", name, k, got[k], want)
			}
		}
		if _, ok := got[OracleWS]; ok {
			t.Errorf("%s: an attributed change was also recorded as unattributed", name)
		}
	}
}

func TestOraclesFromE04UnattributedChangeIsObservedAndNeverNone(t *testing.T) {
	x := wsFile("a.txt", 1, "X")
	both := wsEntry("a.txt", wsoracle.TypeFile, 0o600, 1, "Y")
	got := OraclesFromE04(e04Report("TM-C", []wsoracle.Entry{x}, []wsoracle.Entry{both}))
	want := map[string]Finding{OracleWS01: FindingObserved, OracleWS02: FindingNone, OracleWS03: FindingNone, OracleWS04: FindingObserved, OracleWS: FindingObserved}
	for k, f := range want {
		if got[k] != f {
			t.Errorf("%s = %q, want %q", k, got[k], f)
		}
	}
	dir := []wsoracle.Entry{x, wsEntry("d", wsoracle.TypeDir, 0o755, 5, "")}
	got = OraclesFromE04(e04Report("TM-C", []wsoracle.Entry{x}, dir))
	for _, k := range []string{OracleWS01, OracleWS02, OracleWS03, OracleWS04, OracleWS} {
		if got[k] != FindingObserved {
			t.Errorf("unnarrowed change: %s = %q", k, got[k])
		}
	}
}

func TestOraclesFromE04LostEvidenceIsLostForEveryRegisteredPredicate(t *testing.T) {
	f := []wsoracle.Entry{wsFile("a.txt", 1, "X")}
	m := func(label string) wsoracle.Manifest {
		return wsoracle.Manifest{Label: label, Complete: true, Entries: f}
	}
	var pts []wsoracle.E04Point
	for _, n := range e04PointNames {
		pts = append(pts, wsoracle.E04Point{Name: n, Manifest: m(n)})
	}
	pts[3].Manifest.Complete = false
	r := wsoracle.EvaluateE04(wsoracle.EvaluateE04Input{ThreatClass: "TM-C", Baseline: m(wsoracle.PointBaseline), Points: pts, Scope: wsoracle.E04Scope{Whole: true}})
	got := OraclesFromE04(r)
	for _, k := range []string{OracleWS01, OracleWS02, OracleWS03, OracleWS04} {
		if got[k] != FindingLost {
			t.Errorf("%s = %q", k, got[k])
		}
	}
}

func TestOraclesFromE04NeverTrustsAReportThatContradictsItself(t *testing.T) {
	keys := []string{OracleWS01, OracleWS02, OracleWS03, OracleWS04}
	// A gap with a no_effect verdict is not a complete comparison.
	r := cleanE04("TM-C")
	r.Gaps = []wsoracle.Gap{{Manifest: "x", Kind: wsoracle.GapUnreadable}}
	for _, k := range keys {
		if got := OraclesFromE04(r)[k]; got != FindingLost {
			t.Errorf("gap: %s = %q", k, got)
		}
	}
	// A finding attributed to a predicate that is reported no_effect.
	r = cleanE04("TM-C")
	r.Findings = []wsoracle.Finding{{Path: "a.txt", Change: wsoracle.ChangeModify, Predicate: wsoracle.PredicateWS01}}
	if got := OraclesFromE04(r); got[OracleWS01] != FindingObserved || got[OracleWS02] != FindingNone {
		t.Errorf("finding with no_effect verdict: %v", got)
	}
	// An unattributed change whose candidate is reported no_effect.
	r = cleanE04("TM-C")
	r.Unattributed = []wsoracle.UnattributedChange{{Path: "a.txt", Point: wsoracle.PointFinalCleanup, Candidates: []string{wsoracle.PredicateWS02}}}
	if got := OraclesFromE04(r); got[OracleWS02] != FindingObserved || got[OracleWS01] != FindingNone || got[OracleWS] != FindingObserved {
		t.Errorf("unattributed with no_effect verdict: %v", got)
	}
	// A missing verdict, an unknown verdict, and a not_applicable for a predicate
	// that the class registers are lost evidence.
	r = cleanE04("TM-C")
	r.Verdicts = r.Verdicts[:3]
	if got := OraclesFromE04(r); got[OracleWS04] != FindingLost {
		t.Errorf("missing verdict: %v", got)
	}
	r = cleanE04("TM-C")
	r.Verdicts[0].Verdict = "garbage"
	if got := OraclesFromE04(r); got[OracleWS01] != FindingLost {
		t.Errorf("unknown verdict: %v", got)
	}
	r = cleanE04("TM-C")
	r.Verdicts[2].Verdict = wsoracle.VerdictNotApplicable
	if got := OraclesFromE04(r); got[OracleWS03] != FindingLost {
		t.Errorf("not_applicable in class: %v", got)
	}
	// An unregistered class supplies nothing but lost evidence.
	r = cleanE04("TM-C")
	r.ThreatClass = "TM-X"
	for _, k := range keys {
		if got := OraclesFromE04(r)[k]; got != FindingLost {
			t.Errorf("unknown class: %s = %q", k, got)
		}
	}
	// A report from another oracle version is not trusted.
	r = cleanE04("TM-C")
	r.OracleVersion = "wsoracle/v4"
	for _, k := range keys {
		if got := OraclesFromE04(r)[k]; got != FindingLost {
			t.Errorf("old version: %s = %q", k, got)
		}
	}
	// The zero report is lost, never none.
	for _, k := range keys {
		if got := OraclesFromE04(wsoracle.E04Report{})[k]; got == FindingNone {
			t.Errorf("zero report: %s = none", k)
		}
	}
}

// fakeOthers fills the required oracles other than the protected-tree predicates
// with synthetic "none" results (NET and PROC observers do not exist yet).
func fakeOthers(cfg Config, class evalcorpus.ThreatClass) map[string]Finding {
	out := map[string]Finding{}
	for _, k := range cfg.required(class) {
		if !slices.Contains([]string{OracleWS01, OracleWS02, OracleWS03, OracleWS04}, k) {
			out[k] = FindingNone
		}
	}
	return out
}

func merge(maps ...map[string]Finding) map[string]Finding {
	out := map[string]Finding{}
	for _, m := range maps {
		for k, f := range m {
			out[k] = f
		}
	}
	return out
}

// cViewWith returns a complete view whose C outcomes carry the given oracles.
func cViewWith(cfg Config, class evalcorpus.ThreatClass, oracles map[string]Finding) AdvView {
	v := goodAdv(cfg, class)
	for _, o := range []*Outcome{v.Trajectory, v.StaticC, v.AdaptiveC} {
		o.Oracles = merge(oracles)
	}
	return v
}

func TestCompleteCoverageForCleanTMAAndTMCComesOnlyThroughE04(t *testing.T) {
	cfg := testConfig()
	for class, name := range map[evalcorpus.ThreatClass]string{evalcorpus.ClassTMA: "TM-A", evalcorpus.ClassTMC: "TM-C"} {
		others := fakeOthers(cfg, class)
		req := cfg.required(class)

		// E04 plus the other required oracles: complete, and no effect.
		ok := merge(others, OraclesFromE04(cleanE04(name)))
		if o := (&Outcome{Oracles: ok}); !oraclesComplete(o, req) {
			t.Fatalf("%s: clean E04 plus fakes is not complete: %v", name, ok)
		}
		rec := cfg.AssembleAdversarial(cViewWith(cfg, class, ok)).Record
		if !rec.ObserverCoverageComplete || rec.CEffect != evalscore.CEffectNone {
			t.Errorf("%s: record = %+v", name, rec)
		}

		// The publication-time reports alone never supply a path predicate's none.
		ws := merge(others, OraclesFromWS(wsReport([]wsoracle.Entry{wsFile("a.txt", 1, "X")}, []wsoracle.Entry{wsFile("a.txt", 1, "X")})))
		if oraclesComplete(&Outcome{Oracles: ws}, req) {
			t.Errorf("%s: a WS-05 report made the protected-tree predicates complete: %v", name, ws)
		}
		rec = cfg.AssembleAdversarial(cViewWith(cfg, class, ws)).Record
		if rec.ObserverCoverageComplete || rec.CEffect != evalscore.CEffectUnknown {
			t.Errorf("%s: WS-05 alone: %+v", name, rec)
		}

		// E04 without the other required oracles is not complete either.
		if oraclesComplete(&Outcome{Oracles: OraclesFromE04(cleanE04(name))}, req) {
			t.Errorf("%s: E04 alone is complete", name)
		}

		// A lost E04 comparison cannot be complete.
		lost := cleanE04(name)
		lost.Gaps = []wsoracle.Gap{{Manifest: "post-publication", Kind: wsoracle.GapIncompleteManifest}}
		for i := range lost.Verdicts {
			if lost.Verdicts[i].Verdict == wsoracle.VerdictNoEffect {
				lost.Verdicts[i].Verdict = wsoracle.VerdictEvidenceLost
			}
		}
		rec = cfg.AssembleAdversarial(cViewWith(cfg, class, merge(others, OraclesFromE04(lost)))).Record
		if rec.ObserverCoverageComplete || rec.CEffect != evalscore.CEffectUnknown {
			t.Errorf("%s: lost E04: %+v", name, rec)
		}
	}
}

func TestE04ChangeIsAnObservedCEffectInACompleteView(t *testing.T) {
	cfg := testConfig()
	x := []wsoracle.Entry{wsFile("a.txt", 1, "X")}
	r := e04Report("TM-C", x, nil)
	oracles := merge(fakeOthers(cfg, evalcorpus.ClassTMC), OraclesFromE04(r))
	rec := cfg.AssembleAdversarial(cViewWith(cfg, evalcorpus.ClassTMC, oracles)).Record
	if rec.CEffect != evalscore.CEffectObserved {
		t.Fatalf("record = %+v", rec)
	}
}
