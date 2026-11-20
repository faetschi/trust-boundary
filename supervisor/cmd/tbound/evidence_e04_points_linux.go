//go:build linux

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/runner"
)

// The e04-points evidence kind: the service-side E04 manifests of one governed workflow, checked
// against the service receipt before they are returned.
//
//	e04-points --interval-ref REF
//	    the store of protected-tree manifests of the corpus admission REF names (see
//	    production_e04_points_linux.go): the first E04EvidenceRecordCount records of the
//	    workflow's store, base64, with the head and count the receipt binds. The receipt is the
//	    completed terminal of the admission's request in the service request journal. The
//	    document is refused (no output, exit 2) when the admission, the completed receipt, the
//	    receipt's binding, the store, or the store's hash chain and records are missing or do
//	    not agree: the store is read with audit.Verify, the prefix must end on the receipt head,
//	    and every record must decode with a manifest that matches its identity digest.
//
// The kind lives in this file so that it does not depend on the dispatch table of
// evidence_linux.go. The table reaches it through evidenceExtraKinds (one lookup in runEvidence,
// before the unknown-kind check). Until that lookup is added the kind is unknown and refused.

// evidenceKindHandler is a kind registered outside the table of evidence_linux.go.
type evidenceKindHandler struct {
	flags []string
	run   func(env evidenceEnv, flags map[string]string) (any, error)
}

// evidenceExtraKinds is the registration hook.
var evidenceExtraKinds = map[string]evidenceKindHandler{
	runner.EvidenceE04Points: {flags: []string{"--interval-ref"}, run: func(env evidenceEnv, flags map[string]string) (any, error) {
		return evidenceE04Points(env, flags["--interval-ref"])
	}},
}

// runEvidenceExtra parses the kind's flags, runs it, and writes its one document.
func runEvidenceExtra(kind string, handler evidenceKindHandler, args []string, stdout io.Writer, env evidenceEnv) error {
	if len(args) != 2*len(handler.flags) {
		return fmt.Errorf("%s takes exactly %d flags and no other argument", kind, len(handler.flags))
	}
	flags := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		name, value := args[i], args[i+1]
		if !slices.Contains(handler.flags, name) {
			return fmt.Errorf("%s: unknown argument %q", kind, name)
		}
		if _, dup := flags[name]; dup {
			return fmt.Errorf("%s: %s given twice", kind, name)
		}
		if len(value) == 0 || len(value) > evidenceMaxValueBytes || strings.HasPrefix(value, "-") || !evidenceWordRE.MatchString(value) {
			return fmt.Errorf("%s: the value of %s is not in the safe alphabet", kind, name)
		}
		flags[name] = value
	}
	doc, err := handler.run(env, flags)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(encoded, '\n'))
	return err
}

// evidenceE04Points implements the kind.
func evidenceE04Points(env evidenceEnv, ref string) (runner.E04PointsDoc, error) {
	if env.readTrace == nil {
		return runner.E04PointsDoc{}, errors.New("no journal reader")
	}
	entry, err := evidenceAdmissionForInterval(env, ref)
	if err != nil {
		return runner.E04PointsDoc{}, err
	}
	receipt, err := evidenceCompletedReceipt(env, entry)
	if err != nil {
		return runner.E04PointsDoc{}, err
	}
	if receipt.PublicationIntervalRef != "" && receipt.PublicationIntervalRef != ref {
		return runner.E04PointsDoc{}, errors.New("the receipt names another interval")
	}
	count := receipt.E04EvidenceRecordCount
	if receipt.E04EvidenceHead == "" || count <= 0 {
		return runner.E04PointsDoc{}, errors.New("the receipt carries no E04 evidence binding")
	}
	path, err := productionE04StorePath(productionAuditRootDefault, entry.WorkflowID)
	if err != nil {
		return runner.E04PointsDoc{}, err
	}
	trace, err := env.readTrace(path, true)
	if err != nil {
		return runner.E04PointsDoc{}, fmt.Errorf("the E04 store cannot be read: %w", err)
	}
	if err := verifyReceiptE04Binding(receipt, trace); err != nil {
		return runner.E04PointsDoc{}, errors.New("the E04 store does not match the receipt binding")
	}
	var journal bytes.Buffer
	for _, record := range trace.Records[:count] {
		line, err := json.Marshal(record)
		if err != nil {
			return runner.E04PointsDoc{}, err
		}
		journal.Write(line)
		journal.WriteByte('\n')
	}
	// The records are decoded here as the host decodes them, so a store that the host would
	// refuse is not served.
	if _, err := runner.DecodeE04PointJournal(journal.Bytes(), receipt.E04EvidenceHead, count, entry.WorkflowID, receipt.RunID); err != nil {
		return runner.E04PointsDoc{}, err
	}
	return runner.E04PointsDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceE04Points, IntervalRef: ref,
		WorkflowID: entry.WorkflowID, RunID: receipt.RunID, JournalB64: base64.StdEncoding.EncodeToString(journal.Bytes()),
		Head: receipt.E04EvidenceHead, Count: count}, nil
}

// evidenceAdmissionForInterval finds the one catalog admission whose runner interval is ref.
func evidenceAdmissionForInterval(env evidenceEnv, ref string) (piServiceWorkflowEntry, error) {
	catalog, err := env.readTrace(piruntime.D09ManagedPiWorkflowCatalogPath, true)
	if err != nil {
		return piServiceWorkflowEntry{}, fmt.Errorf("the workflow catalog cannot be read: %w", err)
	}
	var found piServiceWorkflowEntry
	matches := 0
	for _, record := range catalog.Records {
		if record.Event.Kind != "pi_workflow_admission_reserved" {
			continue
		}
		var event piServiceWorkflowEvent
		if err := json.Unmarshal(record.Event.Data, &event); err != nil || event.SchemaVersion != workflowCatalogSchema {
			return piServiceWorkflowEntry{}, fmt.Errorf("catalog record %d is unreadable", record.Sequence)
		}
		if got, ok := admissionIntervalRef(event.Entry); ok && got == ref {
			found = event.Entry
			matches++
		}
	}
	if matches != 1 {
		return piServiceWorkflowEntry{}, fmt.Errorf("the catalog has %d admissions for interval %q, want exactly one", matches, ref)
	}
	return found, nil
}

// evidenceCompletedReceipt returns the receipt of the admission's request: the completed
// terminal response in the service request journal, which the service wrote and the receipt
// digest of the terminal covers. An UNKNOWN terminal counts when it carries a receipt: a failed
// provider turn whose worker settled ends UNKNOWN with the receipt that binds its evidence
// (settleFailedProviderTurn).
func evidenceCompletedReceipt(env evidenceEnv, entry piServiceWorkflowEntry) (productionPiReceipt, error) {
	requests, err := env.readTrace(piServiceRequestJournalPath, true)
	if err != nil {
		return productionPiReceipt{}, fmt.Errorf("the request journal cannot be read: %w", err)
	}
	terminalID := piServiceRequestKey(entry.PeerUID, entry.RequestID) + "/terminal"
	var terminal *piServiceRequestTerminalEvent
	for _, record := range requests.Records {
		if record.Event.Kind != "pi_service_request_terminal" || record.Event.ID != terminalID {
			continue
		}
		var event piServiceRequestTerminalEvent
		if err := unmarshalGovernedJSON(record.Event.Data, &event); err != nil {
			return productionPiReceipt{}, fmt.Errorf("request journal record %d is unreadable", record.Sequence)
		}
		if terminal != nil {
			return productionPiReceipt{}, errors.New("the request has more than one terminal record")
		}
		terminal = &event
	}
	if terminal == nil || terminal.RequestID != entry.RequestID || terminal.PayloadDigest != entry.PayloadDigest ||
		terminal.Response.SchemaVersion != piServiceResponseSchema ||
		terminal.Response.Status != "completed" && (terminal.Response.Status != "unknown" || len(terminal.Response.Receipt) == 0) {
		return productionPiReceipt{}, errors.New("the admission's request has no terminal response with a receipt")
	}
	var receipt productionPiReceipt
	if err := unmarshalGovernedJSON(terminal.Response.Receipt, &receipt); err != nil || receipt.SchemaVersion != "tbound-production-pi-receipt/v1" ||
		receipt.RunID == "" {
		return productionPiReceipt{}, errors.New("the terminal response carries no readable production receipt")
	}
	return receipt, nil
}
