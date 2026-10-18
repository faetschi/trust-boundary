//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"tbound/supervisor/internal/audit"
)

// The publication anchor is what the runner (internal/runner, PublicationFree.AnchorHead
// and AnchorCount) needs from the governed service to exempt one interval from the
// EFF-WS-05/06 publication predicates. The service fills it from its own publication
// journal, never from a client field.
//
// Why a workflow-scoped slice and not the head of the journal. The publication journal
// (publication-audit.jsonl) is one file shared by every workflow of the host: it also holds
// the managed-root and workflow-ledger records, and other workflows append to it. No head of
// that file belongs to one workflow's interval, and the runner cannot recompute it: the
// runner recomputes the head of a journal of its own (audit.Verify), whose first record is
// the IntervalMarkerKind record. The anchor is therefore the head of the interval journal in
// exactly that shape, derived by the service from its own records:
//
//	record 1:  kind runner_interval, id <interval ref>, no data
//	record 2..: the workflow's records whose kind has the prefix "publication" (the
//	            publication package's own: owner epoch, prepare, checkpoint, token,
//	            terminal or aborted, restore), in journal order, event copied verbatim
//
// chained from sequence 1 with the audit hash rule. A workflow is one admission, so the
// workflow's slice is its interval. The count is the number of those records other than
// publication_owner_epoch, the runner's counting rule (runner/provenance.go): one
// publication transaction is several records.
//
// Records are attributed to a workflow by the data the publication package writes: the
// workflow id of an owner-epoch, prepare or restore-intent record, the transaction id of
// every later record of a transaction, the restore id of every later restore record. A
// "publication" record that cannot be attributed (an unknown kind, an orphan, unreadable
// data) is an error: skipping it could hide a publication.

// publicationIntervalMarkerKind is runner.IntervalMarkerKind. The test pins them equal.
const publicationIntervalMarkerKind = "runner_interval"

// publicationAnchor is the service's evidence for one interval.
type publicationAnchor struct {
	IntervalRef string
	Head        string
	Count       int
}

// runnerStepRole maps a governed (service) role to the runner's Role and Repeat. The
// governed service only ever runs configuration C.
func runnerStepRole(role string) (string, int, bool) {
	switch role {
	case corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleAdaptiveReplay, corpusRoleCompliant, corpusRoleAmbiguous:
		return role, 0, true
	case corpusRoleVariance1:
		return "variance-repeat", 1, true
	case corpusRoleVariance2:
		return "variance-repeat", 2, true
	}
	return "", 0, false
}

// corpusIntervalRef maps a corpus admission id, <case_id>/<role>/<attempt>, to the runner's
// IntervalRef of that trial: <case_id>|<role>|C|<repeat>#<attempt> (Step.ID and IntervalRef).
// caseID is the case the catalog admitted; the id must name it.
func corpusIntervalRef(admissionID, caseID string) (string, bool) {
	attemptAt := strings.LastIndex(admissionID, "/")
	if attemptAt < 0 {
		return "", false
	}
	attempt, err := strconv.Atoi(admissionID[attemptAt+1:])
	if err != nil || (attempt != 1 && attempt != 2) || strconv.Itoa(attempt) != admissionID[attemptAt+1:] {
		return "", false
	}
	roleAt := strings.LastIndex(admissionID[:attemptAt], "/")
	if roleAt <= 0 || caseID == "" || admissionID[:roleAt] != caseID {
		return "", false
	}
	role, repeat, ok := runnerStepRole(admissionID[roleAt+1 : attemptAt])
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s|%s|C|%d#%d", caseID, role, repeat, attempt), true
}

// workflowPublicationSlice returns the workflow's records of the publication package, in
// journal order, and how many of them are publications for the runner (all but the
// owner-epoch records).
func workflowPublicationSlice(trace audit.Trace, workflowID string) ([]audit.Event, int, error) {
	if !validIdentityText(workflowID) {
		return nil, 0, errors.New("publication anchor: the workflow id is not valid")
	}
	transactionOwner := make(map[string]string)
	restoreOwner := make(map[string]string)
	var events []audit.Event
	count := 0
	for _, record := range trace.Records {
		kind := record.Event.Kind
		if !strings.HasPrefix(kind, "publication") {
			continue
		}
		var ids struct {
			WorkflowID    string `json:"workflow_id"`
			TransactionID string `json:"transaction_id"`
			RestoreID     string `json:"restore_id"`
		}
		if err := json.Unmarshal(record.Event.Data, &ids); err != nil {
			return nil, 0, fmt.Errorf("publication anchor: record %d (%s) is unreadable", record.Sequence, kind)
		}
		var owner string
		switch kind {
		case "publication_owner_epoch":
			owner = ids.WorkflowID
		case "publication_prepare":
			owner = ids.WorkflowID
			transactionOwner[ids.TransactionID] = owner
		case "publication_checkpoint", "publication_token_consumed", "publication_terminal", "publication_aborted":
			owner = transactionOwner[ids.TransactionID]
		case "publication_restore_intent":
			owner = ids.WorkflowID
			restoreOwner[ids.RestoreID] = owner
		case "publication_restore_checkpoint", "publication_restore_materialized", "publication_restore_outcome":
			owner = restoreOwner[ids.RestoreID]
		default:
			return nil, 0, fmt.Errorf("publication anchor: record %d has the unknown publication kind %q", record.Sequence, kind)
		}
		if owner == "" {
			return nil, 0, fmt.Errorf("publication anchor: record %d (%s) belongs to no workflow", record.Sequence, kind)
		}
		if owner != workflowID {
			continue
		}
		events = append(events, audit.Event{Kind: kind, ID: record.Event.ID, Data: append([]byte(nil), record.Event.Data...)})
		if kind != "publication_owner_epoch" {
			count++
		}
	}
	return events, count, nil
}

// intervalChainHead is the head hash of the audit chain of events from sequence 1. It is the
// audit package's own record hash (version, sequence, previous hash and event, as JSON).
func intervalChainHead(events []audit.Event) string {
	type unsigned struct {
		Version      int         `json:"version"`
		Sequence     uint64      `json:"sequence"`
		PreviousHash string      `json:"previous_hash"`
		Event        audit.Event `json:"event"`
	}
	previous := ""
	for index, event := range events {
		encoded, err := json.Marshal(unsigned{Version: 1, Sequence: uint64(index + 1), PreviousHash: previous, Event: event})
		if err != nil {
			return ""
		}
		sum := sha256.Sum256(encoded)
		previous = hex.EncodeToString(sum[:])
	}
	return previous
}

// publicationAnchorFor derives the anchor of one workflow's interval from the publication
// journal trace.
func publicationAnchorFor(trace audit.Trace, workflowID, intervalRef string) (publicationAnchor, error) {
	if intervalRef == "" {
		return publicationAnchor{}, errors.New("publication anchor: no interval identity")
	}
	slice, count, err := workflowPublicationSlice(trace, workflowID)
	if err != nil {
		return publicationAnchor{}, err
	}
	head := intervalChainHead(append([]audit.Event{{Kind: publicationIntervalMarkerKind, ID: intervalRef}}, slice...))
	if head == "" {
		return publicationAnchor{}, errors.New("publication anchor: the interval chain cannot be encoded")
	}
	return publicationAnchor{IntervalRef: intervalRef, Head: head, Count: count}, nil
}

// admissionIntervalRef is the runner interval of a corpus-mode admission. It reads only the
// host catalog entry, which the service built from the signed manifest and the admitted
// request, and reports false for any other admission mode: without a corpus admission there
// is no interval identity and so no anchor.
func admissionIntervalRef(entry piServiceWorkflowEntry) (string, bool) {
	if entry.CorpusManifestDigest == "" {
		return "", false
	}
	return corpusIntervalRef(entry.HostAdmissionID, entry.TaskID)
}

// applyPublicationAnchor fills the receipt's anchor fields from the service's own evidence:
// the catalog entry of the admission and the publication journal. It reads no request field.
// A service that has no corpus admission leaves the receipt unanchored.
func applyPublicationAnchor(receipt *productionPiReceipt, admission *productionWorkflowAdmission, sessionWorkflowID string,
	trace audit.Trace) error {
	if receipt == nil || admission == nil {
		return nil
	}
	entry := admission.Entry
	ref, corpus := admissionIntervalRef(entry)
	if entry.CorpusManifestDigest == "" {
		return nil
	}
	if !corpus || sessionWorkflowID != entry.WorkflowID {
		return errors.New("publication anchor: the corpus admission does not name this interval or workflow")
	}
	anchor, err := publicationAnchorFor(trace, entry.WorkflowID, ref)
	if err != nil {
		return err
	}
	receipt.PublicationIntervalRef = anchor.IntervalRef
	receipt.PublicationJournalHead = anchor.Head
	receipt.PublicationJournalRecordCount = anchor.Count
	return nil
}

// verifyReceiptPublicationAnchor re-derives the anchor of a stored completed receipt from the
// publication journal. A receipt without an anchor (from before the fields existed, or a
// non-corpus admission) is accepted: with no anchor the runner exempts nothing.
func verifyReceiptPublicationAnchor(receipt productionPiReceipt, entry piServiceWorkflowEntry, trace audit.Trace) error {
	if receipt.PublicationIntervalRef == "" && receipt.PublicationJournalHead == "" && receipt.PublicationJournalRecordCount == 0 {
		return nil
	}
	ref, ok := admissionIntervalRef(entry)
	if !ok || ref != receipt.PublicationIntervalRef {
		return ErrPiWorkflowCatalog
	}
	anchor, err := publicationAnchorFor(trace, entry.WorkflowID, ref)
	if err != nil {
		return errors.Join(ErrPiWorkflowCatalog, err)
	}
	if anchor.Head != receipt.PublicationJournalHead || anchor.Count != receipt.PublicationJournalRecordCount {
		return ErrPiWorkflowCatalog
	}
	return nil
}
