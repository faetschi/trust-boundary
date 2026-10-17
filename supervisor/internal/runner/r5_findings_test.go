package runner

import (
	"errors"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/wsoracle"
)

// These tests turn the round-3 Codex findings against the runner into
// counterexamples. Each failed against HEAD 97dd846.

// ---- Finding 1: publication-free evidence must match the governed service's own
// anchor (head and publication count). Without an anchor the interval is not exempt. ----

func withAnchor(p *PublicationFree, head string, count int) *PublicationFree {
	p.AnchorHead, p.AnchorCount = head, count
	return p
}

func TestR5AnchorMismatchBlocksTheExemption(t *testing.T) {
	cfg := testConfig()
	for _, class := range r3Classes {
		for name, mk := range map[string]func(*Outcome) *PublicationFree{
			"anchor head differs": func(o *Outcome) *PublicationFree { return withAnchor(evidenceFor(o.Interval), "forged", 0) },
			"anchor head of a longer journal": func(o *Outcome) *PublicationFree {
				_, longer := auditChain(audit.Event{Kind: IntervalMarkerKind, ID: o.Interval}, audit.Event{Kind: "note", ID: "n"})
				return withAnchor(evidenceFor(o.Interval), longer, 0)
			},
			"anchor counts a publication the journal lacks": func(o *Outcome) *PublicationFree {
				p := evidenceFor(o.Interval)
				return withAnchor(p, p.JournalDigest, 1)
			},
		} {
			wantIncomplete(t, cfg, class, name, exemptView(cfg, class, func(_ int, o *Outcome) *PublicationFree { return mk(o) }))
		}
	}
}

func TestR5MissingAnchorBlocksTheExemption(t *testing.T) {
	cfg := testConfig()
	for _, class := range r3Classes {
		wantIncomplete(t, cfg, class, "valid journal, no anchor", exemptView(cfg, class, func(_ int, o *Outcome) *PublicationFree {
			p := evidenceFor(o.Interval)
			p.AnchorHead, p.AnchorCount = "", 0
			return p
		}))
		// A marker-only journal that the executor minted for itself, without an anchor.
		wantIncomplete(t, cfg, class, "marker-only journal without anchor", exemptView(cfg, class, func(_ int, o *Outcome) *PublicationFree {
			b, head := auditChain(audit.Event{Kind: IntervalMarkerKind, ID: o.Interval})
			return &PublicationFree{Journal: b, JournalDigest: head}
		}))
	}
}

// A journal cut exactly before a publication record is a valid chain with a valid
// head. Only the service's anchor, which covers the whole interval, exposes it.
func TestR5RecordBoundaryTruncationIsCaughtByTheAnchor(t *testing.T) {
	cfg := testConfig()
	for _, class := range r3Classes {
		wantIncomplete(t, cfg, class, "journal truncated before a publication record", exemptView(cfg, class, func(_ int, o *Outcome) *PublicationFree {
			_, fullHead := auditChain(
				audit.Event{Kind: IntervalMarkerKind, ID: o.Interval},
				audit.Event{Kind: "note", ID: "n"},
				audit.Event{Kind: "publication_prepare", ID: "tx1"})
			prefix := evidenceFor(o.Interval, audit.Event{Kind: "note", ID: "n"}) // the prefix, with its own valid head
			return withAnchor(prefix, fullHead, 1)
		}))
	}
}

// ---- Finding 2: a "consistent" E04 report must not credit a non-change. ----

func TestR5AttributedFindingWithoutAChangeEarnsNoCredit(t *testing.T) {
	r := cleanE04("TM-C")
	setVerdict(&r, wsoracle.PredicateWS03, wsoracle.VerdictEffectPresent)
	r.Findings = []wsoracle.Finding{{Path: "a.txt", Change: wsoracle.ChangeNone, Predicate: wsoracle.PredicateWS03}}
	if r.OracleVersion != wsoracle.OracleVersion || len(r.Gaps) != 0 {
		t.Fatalf("fixture is not a gap-free current report: %q %v", r.OracleVersion, r.Gaps)
	}
	got := OraclesFromE04(r)
	if got[OracleWS03] != FindingLost {
		t.Errorf("EFF-WS-03 = %q, want lost: %v", got[OracleWS03], got)
	}
	if got[OracleWS] == FindingObserved {
		t.Errorf("a non-change earned generic observed credit: %v", got)
	}
	for k, f := range got {
		if f == FindingObserved || f == FindingNone {
			t.Errorf("an inconsistent report supplies %s = %q", k, f)
		}
	}
	// The calibration consequence: no P witness manifests from it.
	cfg := testConfig()
	v := goodAdv(cfg, evalcorpus.ClassTMC)
	v.Predicate = OracleWS03
	for _, p := range []*Outcome{v.StaticP, v.AdaptiveP} {
		p.Calibration.WitnessPredicateID = OracleWS03
		p.Oracles = merge(fakeOthers(cfg, evalcorpus.ClassTMC), got)
	}
	if a := cfg.AssembleAdversarial(v); a.Status == calibration.Manifested || a.Record.PStaticManifested || a.Record.PAdaptiveManifested {
		t.Errorf("a non-change manifested WS-03: %+v", a)
	}
	// Control: the same shape with a real change still earns credit.
	x := wsFile("a.txt", 1, "X")
	if got := OraclesFromE04(e04Report("TM-C", []wsoracle.Entry{x}, []wsoracle.Entry{wsFile("b.txt", 1, "X")})); got[OracleWS03] != FindingObserved {
		t.Errorf("control: %v", got)
	}
}

// ---- Finding 3: every WS finding carries its own report reference. ----

func exportWith(t *testing.T, hook func(s Step, o *Outcome) error) error {
	t.Helper()
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	fake.Hook = hook
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	_, err := Export(s, j, ExportOptions{})
	return err
}

func TestR5ExportRejectsWSFindingsWithoutAReportReference(t *testing.T) {
	adv := func(f func(o *Outcome)) func(Step, *Outcome) error {
		return func(s Step, o *Outcome) error {
			if s.Kind == evalcorpus.KindAdversarial {
				f(o)
			}
			return nil
		}
	}
	// firstRef applies f to one finding's ref (all refs of the outcome, if it has no ref).
	firstRef := func(f func(r *OracleRef)) func(o *Outcome) {
		return func(o *Outcome) {
			for k, r := range o.OracleRefs {
				f(&r)
				o.OracleRefs[k] = r
				return
			}
		}
	}
	for name, hook := range map[string]func(Step, *Outcome) error{
		"current version, empty report":           adv(func(o *Outcome) { o.OracleReport, o.OracleRefs = "", nil }),
		"aggregate report but no per-finding ref": adv(func(o *Outcome) { o.OracleRefs = nil }),
		"one finding lacks its ref": adv(func(o *Outcome) {
			for k := range o.OracleRefs {
				delete(o.OracleRefs, k)
				return
			}
		}),
		"one finding is bound to an old version": adv(firstRef(func(r *OracleRef) { r.Version = "wsoracle/v6" })),
		"empty report in a ref":                  adv(firstRef(func(r *OracleRef) { r.Report = "" })),
	} {
		if err := exportWith(t, hook); !errors.Is(err, ErrOracleVersion) {
			t.Errorf("%s: Export err = %v, want ErrOracleVersion", name, err)
		}
	}
}

func TestR5FakeExecutorOutcomesCarryProperReferences(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	checked := 0
	for _, st := range s.Steps {
		if st.Kind != evalcorpus.KindAdversarial {
			continue
		}
		o := defaultOutcome(s.Config, st)
		for _, k := range oracleKeys {
			if o.Oracles[k] == "" {
				continue
			}
			checked++
			if ref := o.OracleRefs[k]; ref.Report == "" || ref.Version != wsoracle.OracleVersion {
				t.Errorf("%s: %s has ref %+v", st.ID(), k, ref)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no WS finding was checked")
	}
}

// Counterexample: an unversioned legacy WS-03 observed, then a current WS-05 report
// recorded on top, kept the WS-03 credit under v7 provenance.
func TestR5RecordRefusesToLaunderALegacyFinding(t *testing.T) {
	x := wsFile("a.txt", 1, "X")
	cur := wsReport([]wsoracle.Entry{x}, []wsoracle.Entry{x})
	if cur.OracleVersion != wsoracle.OracleVersion {
		t.Fatalf("fixture version %q", cur.OracleVersion)
	}
	o := Outcome{Oracles: map[string]Finding{OracleWS03: FindingObserved}}
	if err := o.RecordWS(cur); !errors.Is(err, ErrOracleProvenance) {
		t.Errorf("RecordWS err = %v, want ErrOracleProvenance", err)
	}
	if oracleVersionOK(&o) {
		t.Errorf("legacy WS-03 finding kept v7 provenance: %+v", o)
	}
	o = Outcome{Oracles: map[string]Finding{OracleWS03: FindingObserved}}
	if err := o.RecordE04(cleanE04("TM-C")); !errors.Is(err, ErrOracleProvenance) {
		t.Errorf("RecordE04 err = %v, want ErrOracleProvenance", err)
	}
	if oracleVersionOK(&o) {
		t.Errorf("legacy WS-03 finding kept v7 provenance after RecordE04: %+v", o)
	}
	// Even a caller that sets the aggregate label itself cannot launder it: the
	// finding has no ref of its own.
	o = Outcome{Oracles: map[string]Finding{OracleWS03: FindingObserved}, OracleVersion: wsoracle.OracleVersion, OracleReport: "r"}
	if oracleVersionOK(&o) {
		t.Errorf("finding without its own ref accepted: %+v", o)
	}
}

func TestR5RecordBindsEachFindingToItsReport(t *testing.T) {
	var o Outcome
	e := cleanE04("TM-C")
	x := wsFile("a.txt", 1, "X")
	w := wsReport([]wsoracle.Entry{x}, []wsoracle.Entry{x})
	if err := o.RecordE04(e); err != nil {
		t.Fatal(err)
	}
	if err := o.RecordWS(w); err != nil {
		t.Fatal(err)
	}
	de, _ := e.Digest()
	dw, _ := w.Digest()
	if got := o.OracleRefs[OracleWS01]; got.Report != de || got.Version != wsoracle.OracleVersion {
		t.Errorf("WS-01 ref = %+v, want report %s", got, de)
	}
	if got := o.OracleRefs[OracleWS05]; got.Report != dw || got.Version != wsoracle.OracleVersion {
		t.Errorf("WS-05 ref = %+v, want report %s", got, dw)
	}
	if !oracleVersionOK(&o) {
		t.Errorf("two current reports rejected: %+v", o)
	}
}
