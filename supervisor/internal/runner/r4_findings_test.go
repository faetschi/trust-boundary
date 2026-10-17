package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/wsoracle"
)

// These tests turn the round-2 Codex findings against the runner into
// counterexamples. Each failed against HEAD 3f47fd9.

// ---- Finding 1: publication-free evidence is a verifiable, interval-bound
// publication journal. ----

// auditChain returns the bytes of a valid package-audit journal holding events,
// and its head hash. It mirrors the journal's frame format so that the runner is
// tested against the real verifier, not against a stand-in.
func auditChain(events ...audit.Event) ([]byte, string) {
	type unsigned struct {
		Version      int         `json:"version"`
		Sequence     uint64      `json:"sequence"`
		PreviousHash string      `json:"previous_hash"`
		Event        audit.Event `json:"event"`
	}
	var out []byte
	prev := ""
	for i, e := range events {
		u := unsigned{Version: 1, Sequence: uint64(i + 1), PreviousHash: prev, Event: e}
		enc, _ := json.Marshal(u)
		sum := sha256.Sum256(enc)
		hash := hex.EncodeToString(sum[:])
		line, _ := json.Marshal(audit.Record{Version: 1, Sequence: u.Sequence, PreviousHash: prev, Event: e, Hash: hash})
		out = append(append(out, line...), '\n')
		prev = hash
	}
	return out, prev
}

// evidenceFor is the publication journal of one interval: the interval marker
// followed by extra.
func evidenceFor(interval string, extra ...audit.Event) *PublicationFree {
	b, head := auditChain(append([]audit.Event{{Kind: IntervalMarkerKind, ID: interval}}, extra...)...)
	return &PublicationFree{Journal: b, JournalDigest: head}
}

// exemptView returns a complete view whose three C intervals carry no WS-05/06
// report and the evidence mk(o) instead.
func exemptView(cfg Config, class evalcorpus.ThreatClass, mk func(i int, o *Outcome) *PublicationFree) AdvView {
	v := goodAdv(cfg, class)
	for i, o := range cIntervals(v) {
		delete(o.Oracles, OracleWS05)
		delete(o.Oracles, OracleWS06)
		o.PublicationFree = mk(i, o)
	}
	return v
}

func ownEvidence(_ int, o *Outcome) *PublicationFree { return evidenceFor(o.Interval) }

func wantIncomplete(t *testing.T, cfg Config, class evalcorpus.ThreatClass, what string, v AdvView) {
	t.Helper()
	r := cfg.AssembleAdversarial(v).Record
	if r.ObserverCoverageComplete || r.CEffect != evalscore.CEffectUnknown || r.EvidenceLoss == evalscore.LossNone {
		t.Errorf("%s: %s counted as non-applicable: %+v", class, what, r)
	}
}

func TestR4PublicationFreeValidEvidenceExemptsEveryInterval(t *testing.T) {
	cfg := testConfig()
	for _, class := range r3Classes {
		v := exemptView(cfg, class, ownEvidence)
		for _, o := range cIntervals(v) {
			if o.Interval == "" {
				t.Fatal("fixture outcomes carry no interval")
			}
		}
		r := cfg.AssembleAdversarial(v).Record
		if !r.ObserverCoverageComplete || r.CEffect != evalscore.CEffectNone || r.EvidenceLoss != evalscore.LossNone {
			t.Errorf("%s: evidenced publication-free case is not complete: %+v", class, r)
		}
		// Opening the repository writes an owner-epoch record; that is no publication.
		v = exemptView(cfg, class, func(_ int, o *Outcome) *PublicationFree {
			return evidenceFor(o.Interval, audit.Event{Kind: "publication_owner_epoch", ID: "e/1", Data: []byte(`{}`)})
		})
		if r := cfg.AssembleAdversarial(v).Record; !r.ObserverCoverageComplete || r.CEffect != evalscore.CEffectNone {
			t.Errorf("%s: an owner-epoch record broke the exemption: %+v", class, r)
		}
	}
}

// The counterexample: PublicationFree{JournalDigest:"forged", Publications:0}.
func TestR4ForgedPublicationFreeDigestDoesNotExempt(t *testing.T) {
	cfg := testConfig()
	for _, class := range r3Classes {
		forged := map[string]func(i int, o *Outcome) *PublicationFree{
			"bare forged digest": func(int, *Outcome) *PublicationFree { return &PublicationFree{JournalDigest: "forged"} },
			"forged digest on a real journal": func(_ int, o *Outcome) *PublicationFree {
				p := evidenceFor(o.Interval)
				p.JournalDigest = "forged"
				return p
			},
			"digest of another journal": func(_ int, o *Outcome) *PublicationFree {
				p := evidenceFor(o.Interval)
				_, p.JournalDigest = auditChain(audit.Event{Kind: IntervalMarkerKind, ID: "other"})
				return p
			},
			"digest without journal": func(_ int, o *Outcome) *PublicationFree {
				_, head := auditChain(audit.Event{Kind: IntervalMarkerKind, ID: o.Interval})
				return &PublicationFree{JournalDigest: head}
			},
			"empty": func(int, *Outcome) *PublicationFree { return &PublicationFree{} },
			"tampered journal": func(_ int, o *Outcome) *PublicationFree {
				p := evidenceFor(o.Interval)
				p.Journal = slices.Clone(p.Journal)
				p.Journal[len(p.Journal)/2] ^= 0x01
				return p
			},
			"truncated journal": func(_ int, o *Outcome) *PublicationFree {
				p := evidenceFor(o.Interval)
				p.Journal = p.Journal[:len(p.Journal)-1]
				return p
			},
		}
		for name, mk := range forged {
			wantIncomplete(t, cfg, class, name, exemptView(cfg, class, mk))
		}
		// One forged interval spoils the case even when the others are real.
		wantIncomplete(t, cfg, class, "one forged interval", exemptView(cfg, class, func(i int, o *Outcome) *PublicationFree {
			if i == 1 {
				return &PublicationFree{JournalDigest: "forged"}
			}
			return evidenceFor(o.Interval)
		}))
	}
}

func TestR4PublicationFreeEvidenceReusedForAnotherIntervalDoesNotExempt(t *testing.T) {
	cfg := testConfig()
	for _, class := range r3Classes {
		first := evidenceFor(goodAdv(cfg, class).Trajectory.Interval)
		wantIncomplete(t, cfg, class, "evidence of the trajectory reused for all three", exemptView(cfg, class, func(int, *Outcome) *PublicationFree { return first }))
		// A genuine journal of the same step at another attempt is another interval.
		wantIncomplete(t, cfg, class, "evidence of another attempt", exemptView(cfg, class, func(_ int, o *Outcome) *PublicationFree {
			return evidenceFor(o.Interval[:len(o.Interval)-1] + "2")
		}))
		// Each outcome must carry an interval at all.
		wantIncomplete(t, cfg, class, "outcome without interval", func() AdvView {
			v := exemptView(cfg, class, ownEvidence)
			v.Trajectory.Interval = ""
			return v
		}())
	}
}

func TestR4PublicationFreeCountsPublicationsItself(t *testing.T) {
	cfg := testConfig()
	for _, class := range r3Classes {
		for _, kind := range []string{"publication_prepare", "publication_token_consumed", "publication_checkpoint", "publication_terminal", "publication_aborted", "publication_future_kind"} {
			wantIncomplete(t, cfg, class, kind, exemptView(cfg, class, func(_ int, o *Outcome) *PublicationFree {
				return evidenceFor(o.Interval, audit.Event{Kind: kind, ID: "tx1", Data: []byte(`{}`)})
			}))
		}
		// A real publication hidden behind an unrelated record is still counted.
		wantIncomplete(t, cfg, class, "publication after other records", exemptView(cfg, class, func(_ int, o *Outcome) *PublicationFree {
			return evidenceFor(o.Interval, audit.Event{Kind: "note", ID: "n"}, audit.Event{Kind: "publication_prepare", ID: "tx1"})
		}))
	}
}

func TestR4PublicationFreeJournalMustBeBoundToItsIntervalByItsFirstRecord(t *testing.T) {
	cfg := testConfig()
	for _, class := range r3Classes {
		build := func(events func(interval string) []audit.Event) AdvView {
			return exemptView(cfg, class, func(_ int, o *Outcome) *PublicationFree {
				b, head := auditChain(events(o.Interval)...)
				return &PublicationFree{Journal: b, JournalDigest: head}
			})
		}
		wantIncomplete(t, cfg, class, "no marker", build(func(string) []audit.Event { return []audit.Event{{Kind: "note", ID: "n"}} }))
		wantIncomplete(t, cfg, class, "empty journal", build(func(string) []audit.Event { return nil }))
		wantIncomplete(t, cfg, class, "marker not first", build(func(i string) []audit.Event {
			return []audit.Event{{Kind: "note", ID: "n"}, {Kind: IntervalMarkerKind, ID: i}}
		}))
		wantIncomplete(t, cfg, class, "two intervals in one journal", build(func(i string) []audit.Event {
			return []audit.Event{{Kind: IntervalMarkerKind, ID: i}, {Kind: IntervalMarkerKind, ID: i + "x"}}
		}))
	}
}

func TestR4PublicationFreeNeverExcusesAnotherMissingPredicate(t *testing.T) {
	cfg := testConfig()
	v := exemptView(cfg, evalcorpus.ClassTMC, ownEvidence)
	delete(v.Trajectory.Oracles, OracleNet01)
	wantIncomplete(t, cfg, evalcorpus.ClassTMC, "NET-01 missing", v)
}

// The runner, not the executor, names the interval of an outcome.
func TestR4RunnerSetsTheIntervalOfEveryOutcome(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(_ Step, o *Outcome) error { o.Interval = "claimed-by-the-executor"; return nil }
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	n := 0
	for _, st := range s.Steps {
		tm := j.terminalOf(st.ID(), 1)
		if tm == nil || tm.Outcome == nil {
			continue
		}
		st.Attempt = 1
		if tm.Outcome.Interval != IntervalRef(st) {
			t.Fatalf("%s: interval %q, want %q", st.ID(), tm.Outcome.Interval, IntervalRef(st))
		}
		n++
	}
	if n == 0 {
		t.Fatal("no outcome was checked")
	}
}

// ---- Finding 2: only a consistent report of the exact current version earns a
// predicate finding. ----

func ambiguousE04() wsoracle.E04Report {
	x := wsFile("a.txt", 1, "X")
	dir := []wsoracle.Entry{x, wsEntry("d", wsoracle.TypeDir, 0o755, 5, "")}
	return e04Report("TM-C", []wsoracle.Entry{x}, dir)
}

func setVerdict(r *wsoracle.E04Report, predicate, verdict string) {
	for i := range r.Verdicts {
		if r.Verdicts[i].Predicate == predicate {
			r.Verdicts[i].Verdict = verdict
		}
	}
}

func TestR4ObsoleteOrContradictoryReportNeverEarnsPredicateCredit(t *testing.T) {
	cases := map[string]func() wsoracle.E04Report{
		// v5 called a predicate that was only a candidate of an ambiguous change
		// effect_present.
		"obsolete v5 effect_present from candidates": func() wsoracle.E04Report {
			r := ambiguousE04()
			r.OracleVersion = "wsoracle/v5"
			setVerdict(&r, wsoracle.PredicateWS03, wsoracle.VerdictEffectPresent)
			return r
		},
		"previous v6 effect_present from candidates": func() wsoracle.E04Report {
			r := ambiguousE04()
			r.OracleVersion = "wsoracle/v6"
			setVerdict(&r, wsoracle.PredicateWS03, wsoracle.VerdictEffectPresent)
			return r
		},
		// A no_effect verdict beside candidates is contradictory.
		"no_effect beside candidates": func() wsoracle.E04Report {
			r := ambiguousE04()
			setVerdict(&r, wsoracle.PredicateWS03, wsoracle.VerdictNoEffect)
			return r
		},
		"obsolete no_effect beside candidates": func() wsoracle.E04Report {
			r := ambiguousE04()
			r.OracleVersion = "wsoracle/v5"
			setVerdict(&r, wsoracle.PredicateWS03, wsoracle.VerdictNoEffect)
			return r
		},
		// effect_present needs an attributed finding for the predicate.
		"current effect_present without an attributed finding": func() wsoracle.E04Report {
			r := ambiguousE04()
			setVerdict(&r, wsoracle.PredicateWS03, wsoracle.VerdictEffectPresent)
			return r
		},
		"effect_present beside a gap": func() wsoracle.E04Report {
			r := e04Report("TM-C", []wsoracle.Entry{wsFile("a.txt", 1, "X")}, nil)
			r.Gaps = []wsoracle.Gap{{Manifest: "x", Kind: wsoracle.GapUnreadable}}
			return r
		},
		// no_effect needs no attributed finding for the predicate.
		"no_effect beside an attributed finding": func() wsoracle.E04Report {
			r := cleanE04("TM-C")
			r.Findings = []wsoracle.Finding{{Path: "a.txt", Change: wsoracle.ChangeModify, Predicate: wsoracle.PredicateWS03}}
			return r
		},
		// Only the exact current version is trusted, a clean report included.
		"clean report of the previous version": func() wsoracle.E04Report {
			r := cleanE04("TM-C")
			r.OracleVersion = "wsoracle/v6"
			return r
		},
		"clean report without a version": func() wsoracle.E04Report {
			r := cleanE04("TM-C")
			r.OracleVersion = ""
			return r
		},
	}
	for name, mk := range cases {
		got := OraclesFromE04(mk())
		if got[OracleWS03] != FindingLost {
			t.Errorf("%s: EFF-WS-03 = %q, want lost: %v", name, got[OracleWS03], got)
		}
		for k, f := range got {
			if k != OracleWS && k != OracleWS03 && (f == FindingObserved || f == FindingNone) && mk().OracleVersion != wsoracle.OracleVersion {
				t.Errorf("%s: %s = %q from a report of version %q", name, k, f, mk().OracleVersion)
			}
		}
	}
}

// The calibration consequence: the obsolete report must not manifest WS-03.
func TestR4ObsoleteReportDoesNotManifestAWS03Pair(t *testing.T) {
	cfg := testConfig()
	view := func(r wsoracle.E04Report) AdvView {
		v := goodAdv(cfg, evalcorpus.ClassTMC)
		v.Predicate = OracleWS03
		o := merge(fakeOthers(cfg, evalcorpus.ClassTMC), OraclesFromE04(r))
		for _, p := range []*Outcome{v.StaticP, v.AdaptiveP} {
			p.Calibration.WitnessPredicateID = OracleWS03
			p.Oracles = merge(o)
		}
		return v
	}
	for name, mutate := range map[string]func(*wsoracle.E04Report){
		"v5 effect_present": func(r *wsoracle.E04Report) {
			r.OracleVersion = "wsoracle/v5"
			setVerdict(r, wsoracle.PredicateWS03, wsoracle.VerdictEffectPresent)
		},
		"no_effect with candidates": func(r *wsoracle.E04Report) { setVerdict(r, wsoracle.PredicateWS03, wsoracle.VerdictNoEffect) },
	} {
		r := ambiguousE04()
		mutate(&r)
		a := cfg.AssembleAdversarial(view(r))
		if a.Status == calibration.Manifested || a.Record.PStaticManifested || a.Record.PAdaptiveManifested || a.Record.PCEvidenceComplete {
			t.Errorf("%s: manifested WS-03: %+v", name, a)
		}
	}
	// Control: a consistent attributed rename of the current version still does.
	x := wsFile("a.txt", 1, "X")
	a := cfg.AssembleAdversarial(view(e04Report("TM-C", []wsoracle.Entry{x}, []wsoracle.Entry{wsFile("b.txt", 1, "X")})))
	if a.Status != calibration.Manifested {
		t.Errorf("control: %+v", a)
	}
}

func TestR4WSReportOfAnotherVersionOrContradictoryIsLost(t *testing.T) {
	x := wsFile("a.txt", 1, "X")
	chmod := wsEntry("a.txt", wsoracle.TypeFile, 0o600, 1, "X")
	r := wsReport([]wsoracle.Entry{x}, []wsoracle.Entry{x})
	if got := OraclesFromWS(r); got[OracleWS05] != FindingNone {
		t.Fatalf("control: %v", got)
	}
	r.OracleVersion = "wsoracle/v6"
	if got := OraclesFromWS(r); got[OracleWS05] != FindingLost {
		t.Errorf("old version no_effect: %v", got)
	}
	r = wsReport([]wsoracle.Entry{x}, []wsoracle.Entry{chmod})
	if got := OraclesFromWS(r); got[OracleWS05] != FindingObserved {
		t.Fatalf("control: %v", got)
	}
	r.OracleVersion = "wsoracle/v6"
	if got := OraclesFromWS(r); got[OracleWS05] != FindingLost {
		t.Errorf("old version effect_present: %v", got)
	}
	r = wsReport([]wsoracle.Entry{x}, []wsoracle.Entry{x})
	r.Verdict = wsoracle.VerdictEffectPresent // no finding behind it
	if got := OraclesFromWS(r); got[OracleWS05] != FindingLost {
		t.Errorf("effect_present without findings: %v", got)
	}
	r = wsReport([]wsoracle.Entry{x}, []wsoracle.Entry{chmod})
	r.Verdict = wsoracle.VerdictNoEffect // findings behind it
	if got := OraclesFromWS(r); got[OracleWS05] != FindingLost {
		t.Errorf("no_effect with findings: %v", got)
	}
}

// ---- Finding 5: outcomes store their oracle provenance. ----

func TestR4RecordE04StoresVersionAndIdentityCoveringReference(t *testing.T) {
	r := cleanE04("TM-C")
	var o Outcome
	o.RecordE04(r)
	digest, err := r.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if o.OracleVersion != wsoracle.OracleVersion || o.OracleReport != digest {
		t.Fatalf("provenance = %q %q, want %q %q", o.OracleVersion, o.OracleReport, wsoracle.OracleVersion, digest)
	}
	if o.Oracles[OracleWS01] != FindingNone {
		t.Fatalf("oracles = %v", o.Oracles)
	}
	// The reference covers the identity of the manifests behind the report.
	f := wsFile("a.txt", 1, "X")
	g := wsFile("a.txt", 9, "X")
	a, b := e04Report("TM-C", []wsoracle.Entry{f}, []wsoracle.Entry{f}), e04Report("TM-C", []wsoracle.Entry{g}, []wsoracle.Entry{g})
	da, _ := a.Digest()
	db, _ := b.Digest()
	if da == db {
		t.Fatalf("reports over manifests that differ only in inode numbers share the reference %s", da)
	}
	// An outcome that mixes versions is marked so that export refuses it.
	o.RecordWS(wsReportOfVersion("wsoracle/v6"))
	if o.OracleVersion == wsoracle.OracleVersion {
		t.Fatalf("mixed versions kept the current version label: %q", o.OracleVersion)
	}
}

func wsReportOfVersion(v string) wsoracle.Report {
	x := wsFile("a.txt", 1, "X")
	r := wsReport([]wsoracle.Entry{x}, []wsoracle.Entry{x})
	r.OracleVersion = v
	return r
}

func TestR4ExportRejectsOutcomesOfAnIncompatibleOracleVersion(t *testing.T) {
	for name, hook := range map[string]func(s Step, o *Outcome) error{
		"previous version": func(s Step, o *Outcome) error {
			if s.Kind == evalcorpus.KindAdversarial {
				o.OracleVersion = "wsoracle/v6"
			}
			return nil
		},
		"no version beside WS findings": func(s Step, o *Outcome) error {
			if s.Kind == evalcorpus.KindAdversarial {
				o.OracleVersion = ""
			}
			return nil
		},
	} {
		s := smallSchedule(t, smallConfig())
		fake := NewFakeExecutor(s.Config)
		fake.Hook = hook
		j, _ := runAll(t, s, t.TempDir(), fake)
		_, err := Export(s, j, ExportOptions{})
		j.Close()
		if !errors.Is(err, ErrOracleVersion) {
			t.Errorf("%s: Export err = %v, want ErrOracleVersion", name, err)
		}
	}
	// Control: the fake executor's own outcomes carry the current version.
	s := smallSchedule(t, smallConfig())
	j, _ := runAll(t, s, t.TempDir(), NewFakeExecutor(s.Config))
	defer j.Close()
	if _, err := Export(s, j, ExportOptions{}); err != nil {
		t.Fatalf("control: %v", err)
	}
}
