//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/wsoracle"
)

// Service-side E04 points. EFF-WS-01..04 sample the complete protected-tree manifest at each
// registered E04 point (13-oracle-coverage-matrix.md section 3; 11-evidence-contract.md E04).
// Five of the seven points are moments inside the service run, so only the service can
// sample them: a collector that looks from outside sees the root before the request and
// after the answer, and a protected file changed mid-run and restored before the answer reads
// as no change. The service therefore takes the manifest itself, with wsoracle.Snapshot of
// the managed live root, as it passes each point, and journals it.
//
// Where the points are (one governed workflow, in the order the service passes them):
//
//	baseline                  the binders (production_binder_linux.go,
//	                          production_recovery_binder_linux.go): the writer lease on the live
//	                          root is held and its lifecycle is recovered, before source capture
//	                          and so before any workspace or cell exists.
//	post-materialization      the binders: the workspace generation is seeded from the live root
//	                          and the private-Git snapshot is bound, before the worker is exposed
//	                          to anything and before the cell is launched.
//	post-operation-settlement runComposedProductionPiSession: the worker's turn has ended, so the
//	                          operations it ran are settled, and the worker is not yet torn down.
//	post-teardown-import      the publication attempt: the worker is stopped and its descendants
//	                          settled (the gate), the loops are joined, and the supervisor has
//	                          imported the workspace result (Store.EvidenceBundle).
//	pre-publication           the same attempt, immediately before Publication.Finalize (or the
//	                          no-diff release), the step that may write the live root.
//	post-publication          the same attempt, after Finalize, the terminal record and the
//	                          no-diff release.
//	final-cleanup             the first closer after the cell, scope and Store closers: the cell
//	                          is gone and the writer lease is still held.
//
// The service does not sample inside a single operation: EvaluateE04 takes each point once, and
// the points above are the registered ones. A change made and reverted between two points is
// out of reach of any sampling; the change made before a point and reverted after it is not.
//
// The store. One audit journal per workflow, <AuditRoot>/<workflow id>-e04-audit.jsonl, beside
// the workflow's other journals: the service's own private directory, the audit package's
// 0600 file with an exclusive writer lock and a sha256 hash chain, append only. Each record is
// one E04PointRecord: workflow id, run id, point, sequence, status, the manifest and its
// IdentityDigest, and the ledger prefix applied at the point. A resumed workflow appends the
// points of its new run to the same store, tagged with its run id.
//
// A snapshot that fails is a gap record (status gap, with a reason), never a skipped point: an
// error, a panic, an incomplete manifest, a root that is not the held live root, or a record
// too large for the journal. A point whose gap record also cannot be written is counted in
// gaps() and leaves a hole in the sequence of points, which EvaluateE04 reports as a missing
// point. Nothing here blocks the workflow: recording is best effort for the run and fail
// closed for the oracle.
//
// The ledger. EvaluateE04Input needs, for the protected root, a []wsoracle.LedgerEntry of
// approved transitions with full before/after Entry values (type, mode, owner, group, link
// count, size, sha256 or link target) for every changed path, and at each point the count of
// entries applied plus wsoracle.LedgerPrefixDigest of that prefix. The session's approved-delta
// ledger (sessionrepo EvidenceBundle.ApprovedDeltaLedger, delta.Transition) is not that: it
// records ObjectState{Exists, Type, ContentDigest, MetadataIdentity}, where MetadataIdentity is a
// hash of the policy-selected metadata, not the mode, owner and link count, and its transitions
// apply to the workspace generations, not to the live root. The live-root ledger comes from the
// governed publication itself, which is the only thing that writes the live root, and writes only
// transitions that were approved: for each object it applies, internal/publication reads the
// object's before state (stat and hash through the bound parent descriptor, under the live-root
// lock, at apply time) and its after state (the same, after the rename, which must be the staged
// inode), journals them with the object's "applied" checkpoint, and returns them as
// publication.Result.Ledger: one entry per applied object, in apply order, with an ID bound to the
// transaction. productionE04LedgerFromResult turns the outcome into the ledger the points after
// publication record (the entries themselves, in each record, so the store alone rebuilds
// EvaluateE04Input.Ledger, and their prefix digest). Every point before publication, and every
// point of a run that applied nothing, records the empty prefix, wsoracle.LedgerPrefixDigest(nil,
// 0). Whenever the outcome does not say exactly what was applied (an error, an UNKNOWN status or
// object, an applied object without an entry, an entry for an object that was not applied, a
// ledger too large for one record), the point records no prefix digest (LedgerBasis unavailable):
// EvaluateE04 then reports a ledger-provenance gap and evidence_lost. A ledger entry whose after
// state is not what the post-publication manifest shows is not special-cased: it is the ordinary
// comparison, an effect (or a lost interval if the manifest is unusable).

const productionE04StoreSuffix = "-e04-audit.jsonl"

// productionE04StorePath is the one store path of a workflow. The workflow id must be one
// plain path component.
func productionE04StorePath(auditRoot, workflowID string) (string, error) {
	if !validIdentityText(workflowID) || strings.ContainsAny(workflowID, "/\\\x00") || workflowID == "." || workflowID == ".." {
		return "", errors.New("e04 store: the workflow id is not a plain file name component")
	}
	return filepath.Join(auditRoot, workflowID+productionE04StoreSuffix), nil
}

// productionE04Ledger is the ledger prefix the service records at a point.
type productionE04Ledger struct {
	applied int
	digest  string
	basis   string
	// entries is the whole ledger when basis is runner.E04LedgerPublication; applied is its length.
	entries []wsoracle.LedgerEntry
}

// productionE04LedgerFromResult is the ledger the points after publication record for a publication
// outcome. The publication ledger names what the publication applied to the live root, with the
// state it read there (internal/publication, Result.Ledger). It is used only when the outcome says
// exactly what happened:
//
//   - the publish call returned no error and the status is SUCCEEDED or FAILED, both terminal and
//     known: an UNKNOWN status, an error, or no status at all (the call was refused before it could
//     publish, or was cut short) may have applied objects that nothing records;
//   - no object is UNKNOWN: an unknown object may or may not be in the live root;
//   - every SUCCEEDED object has exactly one ledger entry of its own path, bound to the
//     transaction, in sequence order, and no entry names an object that did not succeed. An
//     applied object without an entry makes the whole ledger unavailable, never a shorter one.
//
// With no SUCCEEDED object nothing was applied and the prefix is the empty one. A FAILED outcome
// with some SUCCEEDED objects keeps those objects in the live root (publication does not roll
// back), and the ledger names exactly them.
func productionE04LedgerFromResult(result publication.Result, publishErr error) productionE04Ledger {
	if publishErr != nil || (result.Status != publication.StatusSucceeded && result.Status != publication.StatusFailed) {
		return productionE04LedgerUnavailable()
	}
	applied := make(map[string]bool, len(result.Objects))
	for _, object := range result.Objects {
		switch object.Status {
		case publication.ObjectSucceeded:
			if applied[object.Path] {
				return productionE04LedgerUnavailable()
			}
			applied[object.Path] = true
		case publication.ObjectFailed:
		default:
			return productionE04LedgerUnavailable()
		}
	}
	if len(result.Ledger) != len(applied) {
		return productionE04LedgerUnavailable()
	}
	if len(applied) == 0 {
		return productionE04LedgerNone()
	}
	covered := make(map[string]bool, len(applied))
	for i, entry := range result.Ledger {
		if result.TransactionID == "" || entry.Sequence != uint64(i+1) || !strings.HasPrefix(entry.ID, result.TransactionID+"/") ||
			len(entry.Changes) != 1 || !applied[entry.Changes[0].Path] || covered[entry.Changes[0].Path] {
			return productionE04LedgerUnavailable()
		}
		covered[entry.Changes[0].Path] = true
	}
	entries := append([]wsoracle.LedgerEntry(nil), result.Ledger...)
	return productionE04Ledger{applied: len(entries), digest: wsoracle.LedgerPrefixDigest(entries, len(entries)),
		basis: runner.E04LedgerPublication, entries: entries}
}

func productionE04LedgerNone() productionE04Ledger {
	return productionE04Ledger{applied: 0, digest: wsoracle.LedgerPrefixDigest(nil, 0), basis: runner.E04LedgerNone}
}

func productionE04LedgerUnavailable() productionE04Ledger {
	return productionE04Ledger{basis: runner.E04LedgerUnavailable}
}

// productionE04PublicationFromResult is the publication evidence of a publication outcome: what publication.Publish
// held under the live-root lock (Result.Evidence) with the status the service saw. A result without that evidence
// (the request was refused before it was planned, or the result was recovered from the journal) gives nil: the
// evidence is absent, not an empty publication. The ledger is not part of it: the points carry it
// (productionE04LedgerFromResult), and EFF-WS-05 is judged only where they say what was applied.
func productionE04PublicationFromResult(result publication.Result) *runner.E04PublicationEvidence {
	if result.Evidence == nil || result.TransactionID == "" {
		return nil
	}
	status := string(result.Status)
	if status == "" {
		status = string(publication.StatusUnknown)
	}
	return &runner.E04PublicationEvidence{TransactionID: result.TransactionID, PublicationID: result.PublicationID, Status: status,
		LockIdentity: result.Evidence.LockIdentity, AffectedPaths: append([]string(nil), result.Evidence.AffectedPaths...),
		Sealed: result.Evidence.Sealed, SealedIdentityDigest: result.Evidence.Sealed.IdentityDigest(),
		FenceDir: result.Evidence.FenceDir, FenceBursts: result.Evidence.FenceBursts}
}

// productionE04Journal is the part of audit.Journal the recorder uses.
type productionE04Journal interface {
	Append(audit.Event) (audit.Record, error)
	TraceCommitted() (audit.Trace, error)
	Close() error
}

// productionE04Binding is what the receipt carries about the store.
type productionE04Binding struct {
	Head  string
	Count int
	Gaps  int
}

// productionE04Recorder takes and journals the manifests of one run of one workflow. A nil
// recorder records nothing (the dev and ordinary modes, which have no workflow); every method
// accepts it.
type productionE04Recorder struct {
	mu           sync.Mutex
	workflowID   string
	runID        string
	liveRootPath string
	rootDev      uint64
	rootIno      uint64
	rootKnown    bool
	// snapshot is wsoracle.Snapshot; a test replaces it.
	snapshot func(root, label string) (wsoracle.Manifest, error)

	journal productionE04Journal
	next    uint64
	ledger  productionE04Ledger
	// publication is the publication evidence the post-publication record carries (see setPublication).
	publication *runner.E04PublicationEvidence
	fatal       error
	gapped      int
	closed      bool
	sealed      productionE04Binding
	sealErr     error
}

// newProductionE04Recorder opens the workflow's store. It never fails: a store that cannot be
// opened leaves a recorder whose every point is counted as a gap. liveRoot is the held
// descriptor of the registered live root; a snapshot of another directory at the same path is
// a gap.
func newProductionE04Recorder(auditRoot, workflowID, runID, liveRootPath string, liveRoot *os.File) *productionE04Recorder {
	r := &productionE04Recorder{workflowID: workflowID, runID: runID, liveRootPath: liveRootPath,
		snapshot: func(root, label string) (wsoracle.Manifest, error) { return wsoracle.Snapshot(root, label), nil },
		ledger:   productionE04LedgerNone(), next: 1}
	if liveRoot != nil {
		if info, err := liveRoot.Stat(); err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && info.IsDir() {
				r.rootDev, r.rootIno, r.rootKnown = uint64(stat.Dev), uint64(stat.Ino), true
			}
		}
	}
	path, err := productionE04StorePath(auditRoot, workflowID)
	if err == nil {
		var journal *audit.Journal
		if journal, err = audit.Open(path); err == nil {
			var trace audit.Trace
			if trace, err = journal.Trace(); err == nil {
				r.journal, r.next = journal, uint64(len(trace.Records))+1
			} else {
				_ = journal.Close()
			}
		}
	}
	if err != nil {
		r.fatal = fmt.Errorf("e04 store unavailable: %w", err)
	}
	return r
}

// newProductionE04RecorderOn is a recorder over a journal the caller opened (a test).
func newProductionE04RecorderOn(journal productionE04Journal, next uint64, workflowID, runID, liveRootPath string, liveRoot *os.File) *productionE04Recorder {
	r := newProductionE04Recorder("", "", runID, liveRootPath, liveRoot)
	r.workflowID, r.journal, r.next, r.fatal = workflowID, journal, next, nil
	return r
}

// setLedger sets the ledger prefix recorded from now on.
func (r *productionE04Recorder) setLedger(ledger productionE04Ledger) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ledger = ledger
	r.mu.Unlock()
}

// setPublication sets the publication evidence the post-publication record carries. Nil (the default) records none:
// the run did not reach Finalize, or Finalize left no evidence, and EFF-WS-05 and EFF-WS-06 are then not judged.
func (r *productionE04Recorder) setPublication(evidence *runner.E04PublicationEvidence) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.publication = evidence
	r.mu.Unlock()
}

// recordE04 records the point on the session's recorder, if the session has one.
func (s *productionPiSession) recordE04(point string) {
	if s != nil {
		s.E04.record(point)
	}
}

// finalCleanup is the closer that records the final-cleanup point. It never fails the cleanup.
func (r *productionE04Recorder) finalCleanup() error {
	r.record(wsoracle.PointFinalCleanup)
	return nil
}

// record takes and journals the manifest at point.
func (r *productionE04Recorder) record(point string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.fatal != nil || r.journal == nil || !validE04Point(point) {
		r.gapped++
		return
	}
	rec := runner.E04PointRecord{Schema: runner.E04PointSchema, Seq: r.next, WorkflowID: r.workflowID, RunID: r.runID,
		Point: point, LedgerApplied: r.ledger.applied, LedgerPrefixDigest: r.ledger.digest, LedgerBasis: r.ledger.basis}
	if r.ledger.basis == runner.E04LedgerPublication {
		rec.Ledger = r.ledger.entries
	}
	if point == wsoracle.PointPostPublication {
		rec.Publication = r.publication
	}
	manifest, reason := r.capture(point)
	rec.Status = runner.E04StatusSnapshot
	if reason != "" {
		rec.Status, rec.GapReason = runner.E04StatusGap, reason
	}
	if manifest.Root != nil || len(manifest.Entries) != 0 || len(manifest.Gaps) != 0 {
		rec.Manifest, rec.ManifestIdentityDigest = &manifest, manifest.IdentityDigest()
	}
	err := r.append(rec)
	if errors.Is(err, audit.ErrRecordTooLarge) && rec.Publication != nil {
		// The sealed manifest does not fit beside the point. The point keeps its own manifest and the publication
		// evidence is left out: EFF-WS-05/06 are then not judged (absent), and E04 loses nothing.
		rec.Publication = nil
		err = r.append(rec)
	}
	if errors.Is(err, audit.ErrRecordTooLarge) {
		// The manifest does not fit one journal frame. The point is still recorded, as a gap
		// that keeps only the digest of what was seen.
		rec.Status, rec.GapReason, rec.Manifest = runner.E04StatusGap, "record_too_large", nil
		err = r.append(rec)
		if errors.Is(err, audit.ErrRecordTooLarge) {
			// The ledger alone does not fit either. A ledger is never cut: the record names none,
			// and EvaluateE04 reports the missing prefix.
			rec.LedgerApplied, rec.LedgerPrefixDigest, rec.LedgerBasis, rec.Ledger = 0, "", runner.E04LedgerUnavailable, nil
			err = r.append(rec)
		}
	}
	if err != nil {
		r.fatal = fmt.Errorf("e04 store append: %w", err)
		r.gapped++
		return
	}
	if rec.Status == runner.E04StatusGap {
		r.gapped++
	}
	r.next++
}

func (r *productionE04Recorder) append(rec runner.E04PointRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	written, err := r.journal.Append(audit.Event{Kind: runner.E04PointRecordKind, ID: fmt.Sprintf("%s/%d", r.workflowID, rec.Seq), Data: data})
	if err != nil {
		return err
	}
	if written.Sequence != rec.Seq {
		return fmt.Errorf("the store sequence %d is not the expected %d", written.Sequence, rec.Seq)
	}
	return nil
}

// capture takes the snapshot and judges it. The reason is empty only for a complete manifest
// of the held live root.
func (r *productionE04Recorder) capture(point string) (manifest wsoracle.Manifest, reason string) {
	defer func() {
		if recover() != nil {
			manifest, reason = wsoracle.Manifest{}, "snapshot_panic"
		}
	}()
	manifest, err := r.snapshot(r.liveRootPath, point)
	switch {
	case err != nil:
		return wsoracle.Manifest{}, "snapshot_error"
	case manifest.Root == nil || manifest.Dev == 0 || manifest.Root.Ino == 0:
		return manifest, "root_identity_missing"
	case r.rootKnown && (manifest.Dev != r.rootDev || manifest.Root.Ino != r.rootIno):
		return manifest, "root_identity_mismatch"
	case !manifest.Complete || len(manifest.Gaps) != 0:
		return manifest, "manifest_incomplete"
	}
	manifest.Label = point
	return manifest, ""
}

func validE04Point(point string) bool {
	for _, name := range runner.E04PointOrder {
		if name == point {
			return true
		}
	}
	return false
}

// gaps is how many points of this run are recorded as a gap or could not be recorded.
func (r *productionE04Recorder) gaps() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gapped
}

// binding is the head and count of the store as the recorder committed it. After close it is
// the head and count the store had when it was closed.
func (r *productionE04Recorder) binding() (productionE04Binding, error) {
	if r == nil {
		return productionE04Binding{}, errors.New("e04 store: the session has no recorder")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.sealed, r.sealErr
	}
	return r.bindingLocked()
}

func (r *productionE04Recorder) bindingLocked() (productionE04Binding, error) {
	if r.journal == nil {
		return productionE04Binding{Gaps: r.gapped}, errors.Join(errors.New("e04 store: no journal"), r.fatal)
	}
	trace, err := r.journal.TraceCommitted()
	if err != nil {
		return productionE04Binding{Gaps: r.gapped}, fmt.Errorf("e04 store: %w", err)
	}
	if len(trace.Records) == 0 {
		return productionE04Binding{Gaps: r.gapped}, errors.New("e04 store: nothing was recorded")
	}
	return productionE04Binding{Head: auditHeadHash(trace.Records), Count: len(trace.Records), Gaps: r.gapped}, nil
}

// close seals the binding and closes the store. It is idempotent.
func (r *productionE04Recorder) close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if r.journal != nil {
		r.sealed, r.sealErr = r.bindingLocked()
	} else {
		r.sealed, r.sealErr = productionE04Binding{Gaps: r.gapped}, errors.Join(errors.New("e04 store: no journal"), r.fatal)
	}
	r.closed = true
	if r.journal == nil {
		return nil
	}
	// A store that will not close is not a reason to fail the cleanup of the cell.
	_ = r.journal.Close()
	return nil
}

// applyE04Binding fills the receipt's E04 binding from the recorder. A recorder that cannot
// name its store leaves the receipt without a binding (and with the gap count): the host then
// has no E04 evidence for the run, which is never read as no change.
func applyE04Binding(receipt *productionPiReceipt, recorder *productionE04Recorder) {
	if receipt == nil || recorder == nil {
		return
	}
	binding, err := recorder.binding()
	receipt.E04GapCount = binding.Gaps
	if err == nil {
		receipt.E04EvidenceHead, receipt.E04EvidenceRecordCount = binding.Head, binding.Count
	}
}

// verifyReceiptE04Binding checks a stored receipt's binding against the store: the first
// E04EvidenceRecordCount records of the store must exist, verify, and end on E04EvidenceHead. A
// store that has grown since (a resumed workflow appends its next run) still matches, because
// the binding is of the prefix. A receipt without a binding is accepted, as a receipt without a
// publication anchor is: the host then has no E04 evidence and treats it as lost.
func verifyReceiptE04Binding(receipt productionPiReceipt, trace audit.Trace) error {
	if receipt.E04EvidenceHead == "" && receipt.E04EvidenceRecordCount == 0 {
		return nil
	}
	count := receipt.E04EvidenceRecordCount
	if receipt.E04EvidenceHead == "" || count <= 0 || count > len(trace.Records) ||
		auditHeadHash(trace.Records[:count]) != receipt.E04EvidenceHead {
		return ErrPiWorkflowCatalog
	}
	return nil
}

// verifyReceiptE04Store reads the workflow's store and verifies the receipt binding.
func verifyReceiptE04Store(receipt productionPiReceipt, workflowID string) error {
	if receipt.E04EvidenceHead == "" && receipt.E04EvidenceRecordCount == 0 {
		return nil
	}
	path, err := productionE04StorePath(productionAuditRootDefault, workflowID)
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	trace, err := readProductionE04Store(path)
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	return verifyReceiptE04Binding(receipt, trace)
}

// readProductionE04Store reads and verifies a store without taking its writer lock. The file
// must be a regular 0600 file of the service's uid, not reached through a link.
func readProductionE04Store(path string) (audit.Trace, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return audit.Trace{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return audit.Trace{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return audit.Trace{}, errors.New("e04 store: not a regular 0600 file of the service uid")
	}
	return audit.Verify(file)
}
