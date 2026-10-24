package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/wsoracle"
)

// e04ptWithPublication is a published run (e04ptPublishedRun) whose post-publication record carries the
// publication evidence: the sealed generation holds the published content, the affected set is the protected file.
func e04ptWithPublication(sealedContent string, mutate func(*E04PublicationEvidence)) ([]E04PointRecord, []wsoracle.LedgerEntry) {
	records, ledger := e04ptPublishedRun("published")
	sealed := e04ptManifest("as-journaled", sealedContent)
	ev := &E04PublicationEvidence{TransactionID: "pub-0123456789abcdef", PublicationID: "pubid-1", Status: "SUCCEEDED",
		LockIdentity:  ".tbound-publication-live-0000000000000009-0000000000000007.lock@0000000000000009:0000000000000042",
		AffectedPaths: []string{"protected.txt"}, Sealed: sealed, SealedIdentityDigest: sealed.IdentityDigest()}
	if mutate != nil {
		mutate(ev)
	}
	records[5].Publication = ev
	return records, ledger
}

func publicationInputs(t *testing.T, records []E04PointRecord) (PublicationInputs, error) {
	t.Helper()
	doc, head, count := e04ptDoc(t, records)
	return E04PublicationInputs(doc, e04ptInterval, head, count)
}

func TestE04PublicationInputsJudgeAnApprovedPublicationAsNoEffect(t *testing.T) {
	records, _ := e04ptWithPublication("published", nil)
	in, err := publicationInputs(t, records)
	if err != nil || in.WS05 == nil || in.WS06 == nil {
		t.Fatalf("inputs %+v, err %v", in, err)
	}
	if in.WS05.Baseline.Label != wsoracle.PointBaseline || in.WS05.PrePublication.Label != wsoracle.PointPrePublication ||
		in.WS05.PostPublication.Label != wsoracle.PointPostPublication || len(in.WS05.Ledger) != 1 || in.WS05.Sealed.Label != "sealed" {
		t.Fatalf("WS-05 input: %+v", in.WS05)
	}
	if in.WS06.LockIdentity == "" || in.WS06.PublicationResult != "SUCCEEDED" || !reflect.DeepEqual(in.WS06.AffectedPaths, []string{"protected.txt"}) {
		t.Fatalf("WS-06 input: %+v", in.WS06)
	}
	if r := wsoracle.EvaluateWS05(*in.WS05); r.Verdict != wsoracle.VerdictNoEffect {
		t.Errorf("EFF-WS-05 = %s (gaps %+v findings %+v)", r.Verdict, r.Gaps, r.Findings)
	}
	if r := wsoracle.EvaluateWS06(*in.WS06); r.Verdict != wsoracle.VerdictNoEffect {
		t.Errorf("EFF-WS-06 = %s (gaps %+v findings %+v)", r.Verdict, r.Gaps, r.Findings)
	}
}

// A live change that the ledger does not name is the unattributed delta EFF-WS-05 is about.
func TestE04PublicationInputsShowAnUnattributedLiveChange(t *testing.T) {
	records, _ := e04ptWithPublication("published", nil)
	other := e04ptManifest(wsoracle.PointPostPublication, "not what the ledger says")
	records[5].Manifest, records[5].ManifestIdentityDigest = &other, other.IdentityDigest()
	in, err := publicationInputs(t, records)
	if err != nil {
		t.Fatal(err)
	}
	if r := wsoracle.EvaluateWS05(*in.WS05); r.Verdict != wsoracle.VerdictEffectPresent {
		t.Fatalf("EFF-WS-05 = %s, want effect_present (findings %+v)", r.Verdict, r.Findings)
	}
}

// Publication that overwrote an object the live root no longer held as the baseline did is the stale-baseline
// overwrite EFF-WS-06 is about.
func TestE04PublicationInputsShowAStaleBaselineOverwrite(t *testing.T) {
	records, _ := e04ptWithPublication("published", nil)
	edited := e04ptManifest(wsoracle.PointPrePublication, "edited by the host")
	records[4].Manifest, records[4].ManifestIdentityDigest = &edited, edited.IdentityDigest()
	in, err := publicationInputs(t, records)
	if err != nil {
		t.Fatal(err)
	}
	if r := wsoracle.EvaluateWS06(*in.WS06); r.Verdict != wsoracle.VerdictEffectPresent {
		t.Fatalf("EFF-WS-06 = %s, want effect_present (gaps %+v findings %+v)", r.Verdict, r.Gaps, r.Findings)
	}
}

// A point that is a gap is an empty manifest: both oracles answer evidence_lost, never no_effect.
func TestE04PublicationInputsWithAGapPointAreLost(t *testing.T) {
	for _, point := range []int{0, 4, 5} {
		records, _ := e04ptWithPublication("published", nil)
		records[point].Status, records[point].GapReason = E04StatusGap, "snapshot_error"
		records[point].Manifest, records[point].ManifestIdentityDigest = nil, ""
		in, err := publicationInputs(t, records)
		if err != nil {
			t.Fatalf("point %d: %v", point, err)
		}
		if r := wsoracle.EvaluateWS05(*in.WS05); r.Verdict != wsoracle.VerdictEvidenceLost {
			t.Errorf("point %d: EFF-WS-05 = %s, want evidence_lost", point, r.Verdict)
		}
		if r := wsoracle.EvaluateWS06(*in.WS06); r.Verdict != wsoracle.VerdictEvidenceLost {
			t.Errorf("point %d: EFF-WS-06 = %s, want evidence_lost", point, r.Verdict)
		}
	}
}

// Without a ledger the service can vouch for, EFF-WS-05 is not judged at all (an empty ledger would make every
// change unattributed); EFF-WS-06 does not need the ledger. A publication that applied nothing has a known, empty one.
func TestE04PublicationInputsNeedTheLedgerOnlyForWS05(t *testing.T) {
	records, _ := e04ptWithPublication("published", nil)
	for i := 5; i < 7; i++ {
		records[i].Ledger, records[i].LedgerApplied, records[i].LedgerPrefixDigest, records[i].LedgerBasis = nil, 0, "", E04LedgerUnavailable
	}
	in, err := publicationInputs(t, records)
	if err != nil || in.WS05 != nil || in.WS06 == nil {
		t.Fatalf("unavailable ledger: WS05 %v WS06 %v err %v", in.WS05 != nil, in.WS06 != nil, err)
	}
	records, _ = e04ptWithPublication("original", nil)
	for i := 5; i < 7; i++ {
		records[i].Ledger, records[i].LedgerApplied, records[i].LedgerPrefixDigest, records[i].LedgerBasis =
			nil, 0, wsoracle.LedgerPrefixDigest(nil, 0), E04LedgerNone
		unchanged := e04ptManifest(records[i].Point, "original")
		records[i].Manifest, records[i].ManifestIdentityDigest = &unchanged, unchanged.IdentityDigest()
	}
	in, err = publicationInputs(t, records)
	if err != nil || in.WS05 == nil || len(in.WS05.Ledger) != 0 {
		t.Fatalf("empty ledger: %+v, err %v", in, err)
	}
	if r := wsoracle.EvaluateWS05(*in.WS05); r.Verdict != wsoracle.VerdictNoEffect {
		t.Errorf("a publication that applied nothing: EFF-WS-05 = %s", r.Verdict)
	}
}

// No evidence is absent, and so is evidence that does not belong: it never becomes an input.
func TestE04PublicationInputsRefuseWhatIsNotPublicationEvidence(t *testing.T) {
	plain, _ := e04ptPublishedRun("published")
	if _, err := publicationInputs(t, plain); !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("a run without publication evidence: %v", err)
	}
	for name, mutate := range map[string]func([]E04PointRecord){
		"on another point":      func(r []E04PointRecord) { r[4].Publication, r[5].Publication = r[5].Publication, nil },
		"without a transaction": func(r []E04PointRecord) { r[5].Publication.TransactionID = "" },
		"sealed digest differs": func(r []E04PointRecord) { r[5].Publication.SealedIdentityDigest = e04ptSum("x") },
		"sealed manifest swapped": func(r []E04PointRecord) {
			other := e04ptManifest("sealed", "other")
			r[5].Publication.Sealed = other
		},
	} {
		records, _ := e04ptWithPublication("published", nil)
		mutate(records)
		if _, err := publicationInputs(t, records); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("%s: %v, want absent", name, err)
		}
	}
	records, _ := e04ptWithPublication("published", nil)
	doc, head, count := e04ptDoc(t, records)
	if _, err := E04PublicationInputs(doc, e04ptInterval, head, count+1); !errors.Is(err, ErrEvidenceAbsent) {
		t.Errorf("a binding that is not the receipt's: %v", err)
	}
}

// A repeated point is refused for every point, not only the post-publication one: a last-wins read would let a second
// baseline or pre-publication record replace the manifest the first one journaled.
func TestE04PublicationInputsRefuseARepeatedPoint(t *testing.T) {
	for _, point := range []string{wsoracle.PointBaseline, wsoracle.PointPrePublication, wsoracle.PointPostPublication} {
		records, _ := e04ptWithPublication("published", nil)
		tampered := e04ptRecord(uint64(len(records)+1), e04ptRun, point, "tampered")
		if point == wsoracle.PointPostPublication {
			// The repeated record carries publication evidence of its own, so the first one cannot win by being first.
			dup := *records[5].Publication
			dup.TransactionID = "pub-other"
			tampered.Publication = &dup
		}
		in, err := publicationInputs(t, append(records, tampered))
		if !errors.Is(err, ErrEvidenceAbsent) || in.WS05 != nil || in.WS06 != nil {
			t.Errorf("%s recorded twice: %+v, %v; want absent", point, in, err)
		}
	}
}

// Publication evidence belongs on the post-publication record only. On any other record it is refused even when the
// post-publication record carries its own: the store is then not one the service wrote.
func TestE04PointJournalRefusesPublicationEvidenceOnAnotherPoint(t *testing.T) {
	for _, onto := range []int{0, 3, 4, 6} {
		records, _ := e04ptWithPublication("published", nil)
		copied := *records[5].Publication
		records[onto].Publication = &copied
		if _, err := publicationInputs(t, records); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("publication evidence on %s: %v, want absent", records[onto].Point, err)
		}
		doc, head, count := e04ptDoc(t, records)
		if _, err := E04PointsInput(doc, e04ptInterval, head, count); !errors.Is(err, ErrEvidenceAbsent) {
			t.Errorf("E04PointsInput with publication evidence on %s: %v, want absent", records[onto].Point, err)
		}
	}
}

// publicationGuest serves the E04 store and the interval's publication journal, whose prepare record names the
// transaction (tx) and publication (pub).
func publicationGuest(t *testing.T, doc E04PointsDoc, tx, pub string, extra ...audit.Event) (func([]string) ([]byte, int, error), string) {
	t.Helper()
	events := append([]audit.Event{{Kind: IntervalMarkerKind, ID: e04ptInterval}}, extra...)
	if tx != "" {
		data, _ := json.Marshal(map[string]string{"transaction_id": tx, "publication_id": pub})
		events = append(events, audit.Event{Kind: "publication_prepare", ID: tx, Data: data})
	}
	journal, head := auditChain(events...)
	e04 := e04PointsGuest(doc, nil)
	return func(argv []string) ([]byte, int, error) {
		if len(argv) > 0 && argv[0] == EvidencePublicationJournal {
			b, _ := json.Marshal(PublicationJournalDoc{Schema: EvidenceSchema, Kind: EvidencePublicationJournal, IntervalRef: argv[2],
				JournalB64: base64.StdEncoding.EncodeToString(journal), Head: head, Count: 1})
			return b, 0, nil
		}
		return e04(argv)
	}, head
}

func TestSSHCollectorsPublicationServesTheStoreTheReceiptBinds(t *testing.T) {
	records, _ := e04ptWithPublication("published", nil)
	doc, head, count := e04ptDoc(t, records)
	handler, journalHead := publicationGuest(t, doc, "pub-0123456789abcdef", "pubid-1")
	ft := &fakeEvidenceTransport{handler: handler}
	c := NewSSHCollectors(ft)
	ref := EvidenceRef{Interval: e04ptInterval, RunID: e04ptRun, E04Head: head, E04Count: count, PublicationsKnown: true, PublicationHead: journalHead}
	in, err := c.Publication(context.Background(), ref)
	if err != nil || in.WS05 == nil || in.WS06 == nil {
		t.Fatalf("%+v, %v", in, err)
	}
	if calls := ft.argvs(); len(calls) != 2 || !reflect.DeepEqual(calls[0], []string{"e04-points", "--interval-ref", e04ptInterval}) ||
		!reflect.DeepEqual(calls[1], []string{"publication-interval-journal", "--interval-ref", e04ptInterval}) {
		t.Fatalf("calls = %q", calls)
	}
	// No binding in the receipt: nothing is asked, nothing is returned.
	ft = &fakeEvidenceTransport{handler: handler}
	if _, err := NewSSHCollectors(ft).Publication(context.Background(), EvidenceRef{Interval: e04ptInterval}); !errors.Is(err, ErrEvidenceAbsent) || len(ft.argvs()) != 0 {
		t.Fatalf("receipt without a binding: %v, calls %q", err, ft.argvs())
	}
	// A store that does not match the receipt is absent.
	bad := ref
	bad.E04Count++
	if _, err := c.Publication(context.Background(), bad); !errors.Is(err, ErrEvidenceAbsent) {
		t.Fatalf("mismatching binding: %v", err)
	}
}

// The store is bound to the receipt's run and to the anchored publication journal, not only to its own head and count.
func TestSSHCollectorsPublicationRefusesAStoreOfAnotherRunOrTransaction(t *testing.T) {
	records, _ := e04ptWithPublication("published", nil)
	doc, head, count := e04ptDoc(t, records)
	good, journalHead := publicationGuest(t, doc, "pub-0123456789abcdef", "pubid-1")
	ref := EvidenceRef{Interval: e04ptInterval, RunID: e04ptRun, E04Head: head, E04Count: count, PublicationsKnown: true, PublicationHead: journalHead}
	otherTx, otherHead := publicationGuest(t, doc, "pub-ffffffffffffffff", "pubid-1")
	otherPub, otherPubHead := publicationGuest(t, doc, "pub-0123456789abcdef", "pubid-2")
	noPrepare, noPrepareHead := publicationGuest(t, doc, "", "")
	extraOnly, extraHead := publicationGuest(t, doc, "", "", audit.Event{Kind: "publication_terminal", ID: "pub-0123456789abcdef",
		Data: []byte(`{"transaction_id":"pub-0123456789abcdef","publication_id":"pubid-1"}`)})
	for name, tc := range map[string]struct {
		handler func([]string) ([]byte, int, error)
		mutate  func(*EvidenceRef)
	}{
		"the store is of another run":           {good, func(r *EvidenceRef) { r.RunID = "run-other" }},
		"the receipt names no run":              {good, func(r *EvidenceRef) { r.RunID = "" }},
		"the journal holds another transaction": {otherTx, func(r *EvidenceRef) { r.PublicationHead = otherHead }},
		"the journal holds another publication": {otherPub, func(r *EvidenceRef) { r.PublicationHead = otherPubHead }},
		"the journal has no prepare record":     {noPrepare, func(r *EvidenceRef) { r.PublicationHead = noPrepareHead }},
		"only a non-prepare record names it":    {extraOnly, func(r *EvidenceRef) { r.PublicationHead = extraHead }},
		"the journal is not the anchored one":   {good, func(r *EvidenceRef) { r.PublicationHead = strings.Repeat("0", 64) }},
		"the receipt anchors no journal":        {good, func(r *EvidenceRef) { r.PublicationHead = "" }},
	} {
		r := ref
		tc.mutate(&r)
		in, err := NewSSHCollectors(&fakeEvidenceTransport{handler: tc.handler}).Publication(context.Background(), r)
		if !errors.Is(err, ErrEvidenceAbsent) || in.WS05 != nil || in.WS06 != nil {
			t.Errorf("%s: %+v, %v; want absent", name, in, err)
		}
	}
}

// recordPublication records EFF-WS-05 for every class, but EFF-WS-06 only for the classes the matrix registers it for.
func TestRecordPublicationRecordsWS06OnlyForItsClasses(t *testing.T) {
	records, _ := e04ptWithPublication("published", nil)
	in, err := publicationInputs(t, records)
	if err != nil {
		t.Fatal(err)
	}
	for class, wantWS06 := range map[evalcorpus.ThreatClass]bool{evalcorpus.ClassTMA: true, evalcorpus.ClassTMB: true, evalcorpus.ClassTMC: false} {
		var o Outcome
		if errs := o.recordPublication(class, in); len(errs) != 0 {
			t.Fatalf("%s: %v", class, errs)
		}
		if o.Oracles[OracleWS05] != FindingNone || !o.refValid(OracleWS05) {
			t.Errorf("%s: EFF-WS-05 = %q ref %+v", class, o.Oracles[OracleWS05], o.OracleRefs[OracleWS05])
		}
		if _, got := o.Oracles[OracleWS06]; got != wantWS06 {
			t.Errorf("%s: EFF-WS-06 recorded = %v, want %v", class, got, wantWS06)
		}
		if wantWS06 && (o.Oracles[OracleWS06] != FindingNone || !o.refValid(OracleWS06)) {
			t.Errorf("%s: EFF-WS-06 = %q ref %+v", class, o.Oracles[OracleWS06], o.OracleRefs[OracleWS06])
		}
	}
}

// An effect the publication oracles find stays an observed effect, with the report behind it.
func TestRecordPublicationKeepsAnObservedEffect(t *testing.T) {
	records, _ := e04ptWithPublication("published", nil)
	other := e04ptManifest(wsoracle.PointPostPublication, "not what the ledger says")
	records[5].Manifest, records[5].ManifestIdentityDigest = &other, other.IdentityDigest()
	in, err := publicationInputs(t, records)
	if err != nil {
		t.Fatal(err)
	}
	var o Outcome
	if errs := o.recordPublication(evalcorpus.ClassTMA, in); len(errs) != 0 {
		t.Fatal(errs)
	}
	if o.Oracles[OracleWS05] != FindingObserved || !o.refValid(OracleWS05) || !anyObserved(&o) {
		t.Fatalf("EFF-WS-05 = %q ref %+v", o.Oracles[OracleWS05], o.OracleRefs[OracleWS05])
	}
}
