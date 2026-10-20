package runner

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/wsoracle"
)

// Service-side E04 points. The governed service takes the protected-tree manifest of the
// managed live root itself, at each registered E04 point as it passes it
// (cmd/tbound/production_e04_points_linux.go), and journals each one in a per-workflow
// store. The service receipt binds the head and count of that store, the way it binds the
// publication anchor. `tbound evidence e04-points` returns the store prefix the receipt
// binds, and E04PointsInput verifies it against the receipt and builds the EvaluateE04
// input. A collector outside the service cannot sample those moments (see
// SSHCollectors); this is the evidence the internal points need.

// EvidenceE04Points is the guest evidence kind: `e04-points --interval-ref REF`.
const EvidenceE04Points = "e04-points"

const (
	// E04PointSchema is the schema of one stored point record.
	E04PointSchema = "tbound-e04-point/v1"
	// E04PointRecordKind is the audit event kind of one stored point record.
	E04PointRecordKind = "e04_point"

	// E04StatusSnapshot is a point whose manifest was taken, complete, from the registered
	// live root. E04StatusGap is a point whose snapshot failed or was not trustworthy; the
	// record names the reason and the point is then absent from the EvaluateE04 input.
	E04StatusSnapshot = "snapshot"
	E04StatusGap      = "gap"

	// E04LedgerNone: no approved delta had been applied to the protected root at the point.
	// The service applies approved transitions to the workspace generations, and the
	// protected root receives them only at publication, so before publication and for a
	// run that published nothing the applied prefix is empty.
	E04LedgerNone = "none_applied_to_protected_root"
	// E04LedgerUnavailable: a delta may have been applied to the protected root, and the
	// service has no wsoracle.LedgerEntry for it (see the ledger note in
	// cmd/tbound/production_e04_points_linux.go). LedgerPrefixDigest is empty, so
	// EvaluateE04 answers evidence_lost, never no_effect.
	E04LedgerUnavailable = "unavailable_published_delta_has_no_concrete_ledger"
	// E04LedgerPublication: the record carries the approved-delta ledger of the live root as the
	// governed publication itself applied it (publication.Result.Ledger): one wsoracle.LedgerEntry
	// per applied object, with the before and after state read from the live root at apply time,
	// bound to the publication transaction id. LedgerApplied is the number of entries the point
	// had applied (all of them, at the points after publication), LedgerPrefixDigest is
	// wsoracle.LedgerPrefixDigest of that prefix, and Ledger holds the entries.
	E04LedgerPublication = "applied_by_governed_publication"
)

// E04PointRecord is one journaled point of the store. Seq is the position in the store
// (it equals the audit sequence of the record that holds it). ManifestIdentityDigest is
// Manifest.IdentityDigest. A gap record may keep the unusable manifest for diagnosis; it
// is never evaluated.
type E04PointRecord struct {
	Schema                 string             `json:"schema"`
	Seq                    uint64             `json:"seq"`
	WorkflowID             string             `json:"workflow_id"`
	RunID                  string             `json:"run_id"`
	Point                  string             `json:"point"`
	Status                 string             `json:"status"`
	GapReason              string             `json:"gap_reason,omitempty"`
	ManifestIdentityDigest string             `json:"manifest_identity_digest,omitempty"`
	Manifest               *wsoracle.Manifest `json:"manifest,omitempty"`
	LedgerApplied          int                `json:"ledger_applied"`
	LedgerPrefixDigest     string             `json:"ledger_prefix_digest"`
	LedgerBasis            string             `json:"ledger_basis"`
	// Ledger is the ledger the record's prefix is of. It is present only for E04LedgerPublication,
	// and then holds exactly the LedgerApplied entries the prefix digest names.
	Ledger []wsoracle.LedgerEntry `json:"ledger,omitempty"`
	// Publication is the publication-side evidence of EFF-WS-05 and EFF-WS-06. Only the post-publication record
	// of a run that reached Finalize carries it.
	Publication *E04PublicationEvidence `json:"publication,omitempty"`
}

// E04PublicationEvidence is what the service knows of one publication transaction besides the live-root
// manifests and the ledger (publication.OracleEvidence, taken by publication.Publish under the live-root lock): the
// sealed generation's manifest (SealedIdentityDigest is its IdentityDigest), the intended affected-object set and the
// lock identity. Status is the publication status the service saw (SUCCEEDED, FAILED, UNKNOWN). It rides in the
// post-publication record, so the receipt's binding of the store (head and count) binds it too.
type E04PublicationEvidence struct {
	TransactionID        string            `json:"transaction_id"`
	PublicationID        string            `json:"publication_id"`
	Status               string            `json:"status"`
	LockIdentity         string            `json:"lock_identity"`
	AffectedPaths        []string          `json:"affected_paths"`
	Sealed               wsoracle.Manifest `json:"sealed"`
	SealedIdentityDigest string            `json:"sealed_identity_digest"`
	// ApplyStartMonoNS and ApplyEndMonoNS are the writes window of the transaction to the live root, CLOCK_MONOTONIC
	// nanoseconds on the guest (publication.OracleEvidence): the transient-mutation judge places the observer's events
	// in it. Zero when the transaction never reached the apply loop.
	ApplyStartMonoNS int64 `json:"apply_start_mono_ns"`
	ApplyEndMonoNS   int64 `json:"apply_end_mono_ns"`
}

// E04PointsDoc is the output of `tbound evidence e04-points`. JournalB64 is the first Count
// records of the workflow's store in audit format; Head is the hash of the last one. Head and
// Count are the service's own binding (the receipt's e04_evidence_head and
// e04_evidence_record_count). RunID is the receipt's run: the store of a workflow can also
// hold the points of earlier runs of the same workflow.
type E04PointsDoc struct {
	Schema      string `json:"schema"`
	Kind        string `json:"kind"`
	IntervalRef string `json:"interval_ref"`
	WorkflowID  string `json:"workflow_id"`
	RunID       string `json:"run_id"`
	JournalB64  string `json:"journal_b64"`
	Head        string `json:"head"`
	Count       int    `json:"count"`
}

// E04PointOrder is the order the service passes the points.
var E04PointOrder = []string{
	wsoracle.PointBaseline, wsoracle.PointPostMaterialization, wsoracle.PointPostSettlement,
	wsoracle.PointPostTeardown, wsoracle.PointPrePublication, wsoracle.PointPostPublication, wsoracle.PointFinalCleanup,
}

func e04PointsAbsent(format string, args ...any) error {
	return fmt.Errorf("%w: e04 points: %s", ErrEvidenceAbsent, fmt.Sprintf(format, args...))
}

// DecodeE04PointJournal verifies journal as an audit chain that ends on head with count
// records, and decodes every record: its kind, schema, seq, workflow, point, and the
// identity digest of a kept manifest are all checked. It returns the records of runID, in
// store order. A record of another run is chain-verified and skipped.
func DecodeE04PointJournal(journal []byte, head string, count int, workflowID, runID string) ([]E04PointRecord, error) {
	trace, err := audit.Verify(bytes.NewReader(journal))
	if err != nil {
		return nil, e04PointsAbsent("the store journal does not verify: %v", err)
	}
	if count <= 0 || len(trace.Records) != count || head == "" || trace.Records[count-1].Hash != head {
		return nil, e04PointsAbsent("the store journal does not end on the bound head and count")
	}
	var out []E04PointRecord
	for _, record := range trace.Records {
		if record.Event.Kind != E04PointRecordKind {
			return nil, e04PointsAbsent("record %d is not a point record", record.Sequence)
		}
		dec := json.NewDecoder(bytes.NewReader(record.Event.Data))
		dec.DisallowUnknownFields()
		var p E04PointRecord
		if err := dec.Decode(&p); err != nil {
			return nil, e04PointsAbsent("record %d is unreadable", record.Sequence)
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, e04PointsAbsent("record %d has trailing data", record.Sequence)
		}
		switch {
		case p.Schema != E04PointSchema || p.Seq != record.Sequence || p.WorkflowID != workflowID || p.RunID == "",
			!slices.Contains(E04PointOrder, p.Point),
			p.Status != E04StatusSnapshot && p.Status != E04StatusGap,
			p.Status == E04StatusSnapshot && (p.Manifest == nil || p.GapReason != ""),
			p.Status == E04StatusGap && p.GapReason == "",
			p.Manifest != nil && p.Manifest.IdentityDigest() != p.ManifestIdentityDigest,
			p.LedgerBasis == E04LedgerPublication && (len(p.Ledger) == 0 || p.LedgerApplied != len(p.Ledger) ||
				p.LedgerPrefixDigest != wsoracle.LedgerPrefixDigest(p.Ledger, p.LedgerApplied)),
			p.LedgerBasis != E04LedgerPublication && len(p.Ledger) != 0,
			p.Publication != nil && (p.Point != wsoracle.PointPostPublication || p.Publication.TransactionID == "" ||
				p.Publication.Sealed.IdentityDigest() != p.Publication.SealedIdentityDigest):
			return nil, e04PointsAbsent("record %d is not a valid point record of workflow %s", record.Sequence, workflowID)
		}
		if p.RunID == runID {
			out = append(out, p)
		}
	}
	return out, nil
}

// E04PointsInput verifies doc against the receipt binding (receiptHead and receiptCount are
// the receipt's e04_evidence_head and e04_evidence_record_count, as the service journaled
// them) and returns the EvaluateE04 input: the baseline record, and every other point whose
// record is a snapshot, each with the ledger prefix the service recorded, and Ledger from the
// records that carry one (E04LedgerPublication). A point that is a
// gap, or has no record, is left out, so EvaluateE04 reports it missing and every verdict
// is evidence_lost. ThreatClass is left to the caller; Scope is the whole tree. A doc that
// does not match the receipt, a store that does not verify, a repeated point, or a missing
// or gapped baseline is an ErrEvidenceAbsent error: nothing is returned, and the evidence
// stays absent.
func E04PointsInput(doc E04PointsDoc, interval, receiptHead string, receiptCount int) (wsoracle.EvaluateE04Input, error) {
	records, err := e04DocRecords(doc, interval, receiptHead, receiptCount)
	if err != nil {
		return wsoracle.EvaluateE04Input{}, err
	}
	seen := map[string]bool{}
	in := wsoracle.EvaluateE04Input{Scope: wsoracle.E04Scope{Whole: true}}
	baseline := false
	for _, p := range records {
		if seen[p.Point] {
			return wsoracle.EvaluateE04Input{}, e04PointsAbsent("point %s is recorded twice for the run", p.Point)
		}
		seen[p.Point] = true
		if p.LedgerBasis == E04LedgerPublication {
			// The ledger travels with every record that claims it, a gap record included: the point
			// may be unusable but the ledger it names was journaled. All of the run's copies must be
			// the same ledger, and EvaluateE04 then checks each point's claimed prefix against it.
			if in.Ledger != nil && wsoracle.LedgerPrefixDigest(in.Ledger, len(in.Ledger)) != wsoracle.LedgerPrefixDigest(p.Ledger, len(p.Ledger)) {
				return wsoracle.EvaluateE04Input{}, e04PointsAbsent("the run's records carry different ledgers")
			}
			in.Ledger = slices.Clone(p.Ledger)
		}
		if p.Status != E04StatusSnapshot {
			continue
		}
		m := *p.Manifest
		m.Label = p.Point
		if p.Point == wsoracle.PointBaseline {
			in.Baseline, baseline = m, true
			continue
		}
		in.Points = append(in.Points, wsoracle.E04Point{Name: p.Point, Manifest: m,
			LedgerApplied: p.LedgerApplied, LedgerPrefixDigest: p.LedgerPrefixDigest})
	}
	if !baseline {
		return wsoracle.EvaluateE04Input{}, e04PointsAbsent("the baseline point is missing or a gap")
	}
	return in, nil
}

// e04DocRecords checks doc against the interval and the receipt's binding of the store and returns the records
// of the doc's run (DecodeE04PointJournal).
func e04DocRecords(doc E04PointsDoc, interval, receiptHead string, receiptCount int) ([]E04PointRecord, error) {
	if doc.Schema != EvidenceSchema || doc.Kind != EvidenceE04Points || doc.IntervalRef != interval || interval == "" ||
		doc.WorkflowID == "" || doc.RunID == "" {
		return nil, e04PointsAbsent("the document is not for %s", interval)
	}
	if receiptHead == "" || receiptCount <= 0 {
		return nil, e04PointsAbsent("the receipt carries no E04 evidence binding")
	}
	if doc.Head != receiptHead || doc.Count != receiptCount {
		return nil, e04PointsAbsent("the document's head and count are not the receipt's")
	}
	journal, err := base64.StdEncoding.DecodeString(doc.JournalB64)
	if err != nil {
		return nil, e04PointsAbsent("the journal is not base64")
	}
	return DecodeE04PointJournal(journal, receiptHead, receiptCount, doc.WorkflowID, doc.RunID)
}

// PublicationInputs are the EFF-WS-05 and EFF-WS-06 inputs of one published interval, taken from the service's own
// store. A nil input is a predicate the store cannot judge (WS05 when the ledger basis of the publication is
// unavailable). The manifests are the store's baseline, pre-publication and post-publication points: a point that
// is a gap or has no record is an empty manifest, which the oracle reports as lost evidence, never as no effect.
type PublicationInputs struct {
	WS05 *wsoracle.WS05Input
	WS06 *wsoracle.WS06Input
	// TransactionID and PublicationID are the publication transaction the store recorded (E04PublicationEvidence). The
	// collector checks them against the anchored publication journal (bindPublicationTransaction).
	TransactionID, PublicationID string
}

// E04PublicationInputs verifies doc against the receipt binding (E04PointsInput) and builds the publication
// inputs from the run's post-publication record. It is an ErrEvidenceAbsent error when the run has no such record
// or the record carries no publication evidence: the service did not publish, or could not say what it published,
// and the predicates are then neither judged nor exempt (the interval's publication journal decides that).
func E04PublicationInputs(doc E04PointsDoc, interval, receiptHead string, receiptCount int) (PublicationInputs, error) {
	records, err := e04DocRecords(doc, interval, receiptHead, receiptCount)
	if err != nil {
		return PublicationInputs{}, err
	}
	return publicationInputsOf(records)
}

func publicationInputsOf(records []E04PointRecord) (PublicationInputs, error) {
	var post *E04PointRecord
	manifests := map[string]wsoracle.Manifest{}
	seen := map[string]bool{}
	for i := range records {
		p := &records[i]
		// A repeated point is refused for every point, as E04PointsInput does: a last-wins read would let a second
		// record replace the baseline or the pre-publication manifest.
		if seen[p.Point] {
			return PublicationInputs{}, e04PointsAbsent("point %s is recorded twice for the run", p.Point)
		}
		seen[p.Point] = true
		if p.Point == wsoracle.PointPostPublication {
			post = p
		}
		if p.Status == E04StatusSnapshot && p.Manifest != nil {
			m := *p.Manifest
			m.Label = p.Point
			manifests[p.Point] = m
		}
	}
	if post == nil || post.Publication == nil {
		return PublicationInputs{}, e04PointsAbsent("the run has no publication evidence")
	}
	pub := post.Publication
	manifest := func(point string) wsoracle.Manifest {
		if m, ok := manifests[point]; ok {
			return m
		}
		return wsoracle.Manifest{Label: point}
	}
	sealed := pub.Sealed
	sealed.Label = "sealed"
	baseline, pre, after := manifest(wsoracle.PointBaseline), manifest(wsoracle.PointPrePublication), manifest(wsoracle.PointPostPublication)
	in := PublicationInputs{WS06: &wsoracle.WS06Input{AffectedPaths: slices.Clone(pub.AffectedPaths), Baseline: baseline, PrePublication: pre,
		PostPublication: after, LockIdentity: pub.LockIdentity, PublicationResult: pub.Status},
		TransactionID: pub.TransactionID, PublicationID: pub.PublicationID}
	switch post.LedgerBasis {
	case E04LedgerPublication:
		in.WS05 = &wsoracle.WS05Input{Baseline: baseline, Ledger: slices.Clone(post.Ledger), Sealed: sealed, PrePublication: pre, PostPublication: after}
	case E04LedgerNone:
		in.WS05 = &wsoracle.WS05Input{Baseline: baseline, Sealed: sealed, PrePublication: pre, PostPublication: after}
	}
	return in, nil
}

// bindPublicationTransaction checks the publication transaction the E04 store recorded against the publication
// journal the receipt anchors. The journal must verify as an audit chain that ends on head (the receipt's
// publication_journal_head), and one of its publication_prepare records must be the transaction and publication the
// store names: a store of another transaction, or one that names a transaction the service never prepared, is
// absent evidence, never a publication that happened.
func bindPublicationTransaction(journal []byte, head string, in PublicationInputs) error {
	if in.TransactionID == "" {
		return e04PointsAbsent("the store names no publication transaction")
	}
	trace, err := audit.Verify(bytes.NewReader(journal))
	if err != nil || len(trace.Records) == 0 || trace.Records[len(trace.Records)-1].Hash != head {
		return e04PointsAbsent("the publication journal does not end on the receipt's anchor head")
	}
	for _, record := range trace.Records {
		if record.Event.Kind != "publication_prepare" {
			continue
		}
		var prepare struct {
			TransactionID string `json:"transaction_id"`
			PublicationID string `json:"publication_id"`
		}
		if json.Unmarshal(record.Event.Data, &prepare) == nil &&
			prepare.TransactionID == in.TransactionID && prepare.PublicationID == in.PublicationID {
			return nil
		}
	}
	return e04PointsAbsent("the anchored publication journal holds no transaction %s of publication %s", in.TransactionID, in.PublicationID)
}
