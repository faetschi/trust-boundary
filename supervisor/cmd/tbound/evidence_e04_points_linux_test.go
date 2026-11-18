//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/wsoracle"
)

// e04KindEnv is the guest's view of one fake workflow: the workflow catalog, the request
// journal with the completed terminal carrying receipt, and the workflow's store (the real file
// the recorder wrote). editTrace, if set, changes a trace on its way to the reader.
func e04KindEnv(t *testing.T, w *e04FakeWorkflow, receipt productionPiReceipt, editTrace func(path string, trace audit.Trace) audit.Trace) evidenceEnv {
	t.Helper()
	return e04KindEnvWith(t, w, receipt, "completed", 1, editTrace)
}

func e04KindEnvWith(t *testing.T, w *e04FakeWorkflow, receipt productionPiReceipt, status string, admissions int,
	editTrace func(path string, trace audit.Trace) audit.Trace) evidenceEnv {
	t.Helper()
	entry := piServiceWorkflowEntry{CorpusManifestDigest: "sha256:" + strings.Repeat("a", 64), HostAdmissionID: "case-e04/trajectory/1",
		TaskID: "case-e04", WorkflowID: e04TestWorkflow, RunID: e04TestRun, PeerUID: 1000, RequestID: "req-e04",
		PayloadDigest: "sha256:" + strings.Repeat("b", 64)}
	var catalogEvents []audit.Event
	for i := 0; i < admissions; i++ {
		data, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: fmt.Sprintf("h%d", i), Entry: entry})
		if err != nil {
			t.Fatal(err)
		}
		catalogEvents = append(catalogEvents, audit.Event{Kind: "pi_workflow_admission_reserved", ID: fmt.Sprintf("admission-%d", i), Data: data})
	}
	receipt.SchemaVersion, receipt.RunID, receipt.PublicationIntervalRef = "tbound-production-pi-receipt/v1", e04TestRun, e04TestInterval
	rawReceipt, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if status == "unknown/no-receipt" {
		status, rawReceipt = "unknown", nil
	}
	terminal, err := json.Marshal(piServiceRequestTerminalEvent{SchemaVersion: "tbound-pi-service-request-terminal/v1", ClientUID: entry.PeerUID,
		RequestID: entry.RequestID, PayloadDigest: entry.PayloadDigest,
		Response: piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: entry.RequestID, Status: status, Receipt: rawReceipt}})
	if err != nil {
		t.Fatal(err)
	}
	catalog := reconcileTestTrace(t, catalogEvents...)
	requests := reconcileTestTrace(t, audit.Event{Kind: "pi_service_request_terminal",
		ID: piServiceRequestKey(entry.PeerUID, entry.RequestID) + "/terminal", Data: terminal})
	storePath, err := productionE04StorePath(productionAuditRootDefault, e04TestWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	return evidenceEnv{readTrace: func(path string, mustExist bool) (audit.Trace, error) {
		var trace audit.Trace
		switch path {
		case piruntime.D09ManagedPiWorkflowCatalogPath:
			trace = catalog
		case piServiceRequestJournalPath:
			trace = requests
		case storePath:
			read, err := readEvidenceTrace(w.store, mustExist)
			if err != nil {
				return audit.Trace{}, err
			}
			trace = read
		default:
			return audit.Trace{}, fmt.Errorf("unexpected journal %s", path)
		}
		if editTrace != nil {
			trace = editTrace(path, trace)
		}
		return trace, nil
	}}
}

// e04CompletedWorkflow is a fake workflow that passed all seven points, with its receipt.
func e04CompletedWorkflow(t *testing.T) (*e04FakeWorkflow, productionPiReceipt) {
	t.Helper()
	w := newE04FakeWorkflow(t)
	w.pass(nil)
	if err := w.rec.close(); err != nil {
		t.Fatal(err)
	}
	var receipt productionPiReceipt
	applyE04Binding(&receipt, w.rec)
	return w, receipt
}

func TestE04PointsKindReturnsTheRecordedManifestsBoundToTheReceipt(t *testing.T) {
	w, receipt := e04CompletedWorkflow(t)
	doc, err := evidenceE04Points(e04KindEnv(t, w, receipt, nil), e04TestInterval)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != runner.EvidenceSchema || doc.Kind != runner.EvidenceE04Points || doc.IntervalRef != e04TestInterval ||
		doc.WorkflowID != e04TestWorkflow || doc.RunID != e04TestRun || doc.Head != receipt.E04EvidenceHead || doc.Count != 7 {
		t.Fatalf("doc = %+v", doc)
	}
	in, err := runner.E04PointsInput(doc, e04TestInterval, receipt.E04EvidenceHead, receipt.E04EvidenceRecordCount)
	if err != nil || len(in.Points) != 6 || in.Baseline.Label != wsoracle.PointBaseline {
		t.Fatalf("host input: %d points, err %v", len(in.Points), err)
	}
}

func TestE04PointsKindDetectsTamperingWithTheStore(t *testing.T) {
	read := func(t *testing.T, w *e04FakeWorkflow) []byte {
		raw, err := os.ReadFile(w.store)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	write := func(t *testing.T, w *e04FakeWorkflow, raw []byte) {
		if err := os.WriteFile(w.store, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]func(t *testing.T, w *e04FakeWorkflow){
		"a flipped byte in a manifest": func(t *testing.T, w *e04FakeWorkflow) {
			raw := read(t, w)
			raw[len(raw)/3] ^= 1
			write(t, w, raw)
		},
		"the last record cut": func(t *testing.T, w *e04FakeWorkflow) {
			raw := bytes.TrimSuffix(read(t, w), []byte("\n"))
			write(t, w, raw[:bytes.LastIndexByte(raw, '\n')+1])
		},
		"the store emptied": func(t *testing.T, w *e04FakeWorkflow) { write(t, w, nil) },
		"a whole other valid chain in its place": func(t *testing.T, w *e04FakeWorkflow) {
			other := newE04FakeWorkflow(t)
			other.write("a different tree")
			other.pass(nil)
			_ = other.rec.close()
			write(t, w, read(t, other))
		},
		"the store missing": func(t *testing.T, w *e04FakeWorkflow) {
			if err := os.Remove(w.store); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			w, receipt := e04CompletedWorkflow(t)
			tamper(t, w)
			if doc, err := evidenceE04Points(e04KindEnv(t, w, receipt, nil), e04TestInterval); err == nil {
				t.Fatalf("a tampered store was served: %+v", doc)
			}
		})
	}
}

func TestE04PointsKindRefusesWhatTheReceiptDoesNotBind(t *testing.T) {
	w, receipt := e04CompletedWorkflow(t)
	cases := map[string]func(*productionPiReceipt){
		"no binding":           func(r *productionPiReceipt) { r.E04EvidenceHead, r.E04EvidenceRecordCount = "", 0 },
		"another head":         func(r *productionPiReceipt) { r.E04EvidenceHead = strings.Repeat("c", 64) },
		"a count past the end": func(r *productionPiReceipt) { r.E04EvidenceRecordCount = 8 },
		"a count short of it":  func(r *productionPiReceipt) { r.E04EvidenceRecordCount = 3 },
	}
	for name, edit := range cases {
		r := receipt
		edit(&r)
		if doc, err := evidenceE04Points(e04KindEnv(t, w, r, nil), e04TestInterval); err == nil {
			t.Errorf("%s: served %+v", name, doc)
		}
	}
	if _, err := evidenceE04Points(e04KindEnvWith(t, w, receipt, "unknown/no-receipt", 1, nil), e04TestInterval); err == nil {
		t.Error("an UNKNOWN terminal without a receipt was served")
	}
	// A failed provider turn whose worker settled ends UNKNOWN with the receipt that binds its evidence
	// (register D30 as amended 2026-11-18).
	if _, err := evidenceE04Points(e04KindEnvWith(t, w, receipt, "unknown", 1, nil), e04TestInterval); err != nil {
		t.Errorf("an UNKNOWN terminal with its receipt was refused: %v", err)
	}
	for _, admissions := range []int{0, 2} {
		if _, err := evidenceE04Points(e04KindEnvWith(t, w, receipt, "completed", admissions, nil), e04TestInterval); err == nil {
			t.Errorf("%d admissions for the interval were served", admissions)
		}
	}
	if _, err := evidenceE04Points(e04KindEnv(t, w, receipt, nil), "other-case|trajectory|C|0#1"); err == nil {
		t.Error("an interval with no admission was served")
	}
}

// The store of a workflow that is resumed grows after the receipt of the earlier run was
// written. The earlier receipt still names its own prefix, and only the prefix is returned.
func TestE04PointsKindServesTheBoundPrefixOfAGrownStore(t *testing.T) {
	w, receipt := e04CompletedWorkflow(t)
	held, err := os.Open(w.root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	next := newProductionE04Recorder(w.auditDir, e04TestWorkflow, "run-e04-resumed", w.root, held)
	next.record(wsoracle.PointBaseline)
	if err := next.close(); err != nil {
		t.Fatal(err)
	}
	doc, err := evidenceE04Points(e04KindEnv(t, w, receipt, nil), e04TestInterval)
	if err != nil || doc.Count != 7 || doc.Head != receipt.E04EvidenceHead {
		t.Fatalf("doc = %+v, err = %v", doc, err)
	}
}

func TestE04PointsKindIsRegisteredWithItsOneFlagAndWritesOneDocument(t *testing.T) {
	handler, ok := evidenceExtraKinds[runner.EvidenceE04Points]
	if !ok || len(evidenceExtraKinds) != 1 || len(handler.flags) != 1 || handler.flags[0] != "--interval-ref" {
		t.Fatalf("registration = %+v", evidenceExtraKinds)
	}
	w, receipt := e04CompletedWorkflow(t)
	env := e04KindEnv(t, w, receipt, nil)
	var out bytes.Buffer
	if err := runEvidenceExtra(runner.EvidenceE04Points, handler, []string{"--interval-ref", e04TestInterval}, &out, env); err != nil {
		t.Fatal(err)
	}
	var doc runner.E04PointsDoc
	if err := runner.DecodeEvidenceDoc(bytes.TrimSuffix(out.Bytes(), []byte("\n")), &doc); err != nil || doc.Count != 7 || bytes.Count(out.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("output = %d bytes, err %v", out.Len(), err)
	}
	for name, args := range map[string][]string{
		"no flags":          nil,
		"an unknown flag":   {"--interval", e04TestInterval},
		"a repeated flag":   {"--interval-ref", e04TestInterval, "--interval-ref", e04TestInterval},
		"a leading dash":    {"--interval-ref", "-x"},
		"outside alphabet":  {"--interval-ref", "a b"},
		"an extra argument": {"--interval-ref", e04TestInterval, "more", "args"},
	} {
		out.Reset()
		if err := runEvidenceExtra(runner.EvidenceE04Points, handler, args, &out, env); err == nil || out.Len() != 0 {
			t.Errorf("%s: err = %v, output %d bytes", name, err, out.Len())
		}
	}
	failing := evidenceEnv{readTrace: func(string, bool) (audit.Trace, error) { return audit.Trace{}, errors.New("unreadable") }}
	out.Reset()
	if err := runEvidenceExtra(runner.EvidenceE04Points, handler, []string{"--interval-ref", e04TestInterval}, &out, failing); err == nil || out.Len() != 0 {
		t.Fatalf("an unreadable catalog must refuse without output: %v", err)
	}
}
