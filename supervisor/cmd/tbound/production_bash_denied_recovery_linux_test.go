//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/sessionrepo"
)

func deniedBashRun(t *testing.T) *productionBashRun {
	t.Helper()
	run := runProductionBash(t, []string{})
	var denied *protocol.AdmissionDeniedError
	if !errors.As(run.execErr, &denied) || denied.ReasonCode != sessionrepo.DenyReasonUncoveredPath || denied.Tool != "bash" {
		t.Fatalf("the refused Bash command was not released as an admission denial: %v", run.execErr)
	}
	if !deniedAfterEffect(run.operation) {
		t.Fatalf("Store operation = %+v, want denied_after_effect", run.operation)
	}
	return run
}

func promotedBashRun(t *testing.T) *productionBashRun {
	t.Helper()
	run := runProductionBash(t, []string{"task.txt"})
	if run.execErr != nil || run.operation.Outcome != "success" {
		t.Fatalf("the covered Bash command did not promote: %v %+v", run.execErr, run.operation)
	}
	return run
}

func settledRecords(t *testing.T, journal *audit.Journal) []audit.Record {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var records []audit.Record
	for _, record := range trace.Records {
		if record.Event.Kind == productionD09SettledKind {
			records = append(records, record)
		}
	}
	return records
}

// A refused command still ran and settled on its D09 cell. It leaves one settlement event of the denied
// shape that binds the receipt to the Store's denied operation and the refused candidate (no approved
// transition), and the lease is released, so cleanup is safe and the run settles.
func TestProductionD09DeniedBashRetainsASettlementAndReleasesItsLease(t *testing.T) {
	run := deniedBashRun(t)
	if run.runner.Pending() != 0 || run.runner.Settlements() != 1 || !productionCleanupSafe(productionLifecycleSnapshot{}, run.runner) {
		t.Fatalf("pending=%d settlements=%d", run.runner.Pending(), run.runner.Settlements())
	}
	records := settledRecords(t, run.journals[2])
	if len(records) != 1 {
		t.Fatalf("settlement events = %d, want 1", len(records))
	}
	var event productionD09DeniedCommandSettledEvent
	if err := unmarshalGovernedJSON(records[0].Event.Data, &event); err != nil {
		t.Fatalf("denied settlement event: %v", err)
	}
	if event.SchemaVersion != productionD09SettledSchemaDenied || event.Outcome != "denied_after_effect" ||
		event.Operation.Outcome != "denied_after_effect" || event.Operation.Admission == nil ||
		event.Operation.Admission.Outcome != "deny" || event.Operation.OutputGeneration != "" ||
		!bytes.Equal(mustJSON(event.Operation), mustJSON(run.operation)) ||
		event.DeniedCandidateDigest != productionD09DeniedCandidateDigest(run.retained.transition) ||
		bytes.Contains(records[0].Event.Data, []byte("approved_transition")) {
		t.Fatalf("denied settlement event = %s", records[0].Event.Data)
	}
	t.Logf("denied settlement event: %s", records[0].Event.Data)
	ctx := context.Background()
	if err := verifyProductionBashEvidence(ctx, run.runner, run.operation); err != nil {
		t.Fatalf("publication-time evidence check: %v", err)
	}
	if err := run.runner.VerifyProductionBashEvidenceWithOrigin(ctx, run.bundle, run.retained.providerCall,
		run.retained.transition, run.operation); err != nil {
		t.Fatalf("origin re-verification of the refused command: %v", err)
	}
}

// The promoted command keeps its shape: the success event has an approved transition and no outcome member.
func TestProductionD09PromotedBashSettlementKeepsItsShape(t *testing.T) {
	run := promotedBashRun(t)
	records := settledRecords(t, run.journals[2])
	if len(records) != 1 {
		t.Fatalf("settlement events = %d, want 1", len(records))
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(records[0].Event.Data, &members); err != nil {
		t.Fatal(err)
	}
	if _, ok := members["approved_transition"]; !ok || members["outcome"] != nil ||
		string(members["schema_version"]) != `"tbound-production-d09-command-settled/v2"` {
		t.Fatalf("promoted settlement event members changed: %s", records[0].Event.Data)
	}
}

func TestProductionD09OperationValidatorAcceptsExactlyTwoShapes(t *testing.T) {
	denied, promoted := deniedBashRun(t), promotedBashRun(t)
	for name, run := range map[string]*productionBashRun{"denied": denied, "promoted": promoted} {
		isDenied := name == "denied"
		if err := validateProductionD09Operation(run.bundle, run.retained.transition, run.operation, run.retained.receipt, isDenied); err != nil {
			t.Fatalf("%s shape refused: %v", name, err)
		}
		if err := validateProductionD09Operation(run.bundle, run.retained.transition, run.operation, run.retained.receipt, !isDenied); err == nil {
			t.Fatalf("%s operation accepted under the other shape", name)
		}
	}

	// The Store side of each shape, one mismatch at a time.
	approved, refused := promoted.operation, denied.operation
	candidate := denied.retained.transition
	clone := func(operation sessionrepo.OperationEvidence, mutate func(*sessionrepo.OperationEvidence)) sessionrepo.OperationEvidence {
		reference := *operation.Admission
		operation.Admission = &reference
		mutate(&operation)
		return operation
	}
	for name, test := range map[string]struct {
		operation sessionrepo.OperationEvidence
		denied    bool
		want      bool
	}{
		"success, allow, promoted":           {approved, false, true},
		"denied_after_effect, deny, no tree": {refused, true, true},
		"success claimed for a denial": {clone(refused, func(o *sessionrepo.OperationEvidence) { o.Outcome = "success" }),
			false, false},
		"denied_after_effect under an allow admission": {clone(refused, func(o *sessionrepo.OperationEvidence) { o.Admission.Outcome = "allow" }),
			true, false},
		"denied_after_effect that promotes a generation": {clone(refused, func(o *sessionrepo.OperationEvidence) {
			o.OutputGeneration, o.OutputTreeDigest = "g1", approved.OutputTreeDigest
		}), true, false},
		"denied_after_effect read as success": {refused, false, false},
		"success read as denied_after_effect": {approved, true, false},
		"success without a promoted generation": {clone(approved, func(o *sessionrepo.OperationEvidence) {
			o.OutputGeneration, o.OutputTreeDigest = "", ""
		}), false, false},
		"success under a deny admission": {clone(approved, func(o *sessionrepo.OperationEvidence) { o.Admission.Outcome = "deny" }),
			true, false},
		"denied_after_effect relabelled success under allow": {clone(refused, func(o *sessionrepo.OperationEvidence) {
			o.Outcome, o.Admission.Outcome = "success", "allow"
		}), false, false},
	} {
		transition := promoted.retained.transition
		if test.denied {
			transition = candidate
		}
		if got := productionD09OperationShape(test.operation, transition, test.denied); got != test.want {
			t.Errorf("%s: shape accepted = %t, want %t", name, got, test.want)
		}
	}

	// The evidence side of the denied shape: the refused candidate is what the retained admission
	// record denied, and nothing of the lease reached the approved ledger.
	validate := func(bundle sessionrepo.EvidenceBundle, operation sessionrepo.OperationEvidence, candidate delta.Transition) error {
		return validateProductionD09Operation(bundle, candidate, operation, denied.retained.receipt, true)
	}
	other := candidate
	other.Changes = append([]delta.ObjectChange(nil), candidate.Changes[:len(candidate.Changes)-1]...)
	otherDigest := denied.operation
	reference := *otherDigest.Admission
	reference.RecordHash = "sha256:" + strings.Repeat("0", 64)
	otherDigest.Admission = &reference
	promotedLease := denied.bundle
	promotedLease.ApprovedDeltaLedger = append(append([]delta.Transition(nil), denied.bundle.ApprovedDeltaLedger...), candidate)
	for name, err := range map[string]error{
		"another candidate":               validate(denied.bundle, denied.operation, other),
		"another admission record digest": validate(denied.bundle, otherDigest, candidate),
		"a promoted lease":                validate(promotedLease, denied.operation, candidate),
		"the candidate of a promotion":    validate(denied.bundle, denied.operation, promoted.retained.transition),
	} {
		if err == nil {
			t.Errorf("denied shape accepted %s", name)
		}
	}
}

// A fresh instance (new journal handles, the persisted Store reopened as recovery's preview) verifies the
// settled denied command: the command-settlement verifier, the combined history and the historical Store
// verifier all accept it.
func TestProductionD09DeniedSettlementRecoversInAFreshInstance(t *testing.T) {
	run := deniedBashRun(t)
	settlement := run.retained.receipt.Settlement
	view, lease := run.operation.ViewID, run.operation.Operation.LeaseID
	bundle, storeJournal, commandJournal := run.reopen()

	verifier, err := run.historicalVerifier(commandJournal)
	if err != nil {
		t.Fatalf("fresh command-journal verifier: %v", err)
	}
	if err := verifier.VerifySettlement(settlement, view, lease); err != nil {
		t.Fatalf("settlement of the refused command: %v", err)
	}
	if err := verifier.VerifyStoreBundle(bundle); err != nil {
		t.Fatalf("Store bundle of the refused command: %v", err)
	}
	trace, err := commandJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	history, err := newProductionHistoricalBashHistory([]productionRecoveryCommandJournal{{Journal: commandJournal,
		Path: run.commandJournalPath, Pin: productionHistoricalCommandPin{IdentityDigest: "fresh-command-journal",
			Head: auditHeadHash(trace.Records), RecordCount: uint64(len(trace.Records))},
		Sessions: []productionRecoveryCommandSession{{RunID: bashRunRunID, SessionID: bashRunSessionID, ConversationID: bashRunConversationID}}}},
		run.profile, run.template, bashRunWorkflowID, map[string]struct{}{bashRunConversationID: {}})
	if err != nil {
		t.Fatal(err)
	}
	if err := history.VerifyStoreBundle(bundle); err != nil {
		t.Fatalf("combined history over the Store bundle: %v", err)
	}
	historical, err := newGovernedHistoricalStoreVerifier(storeJournal, bashRunStorePolicy, bashRunMetadataPolicy, history.VerifySettlement)
	if err != nil {
		t.Fatal(err)
	}
	if err := historical.VerifyBundle(bundle); err != nil {
		t.Fatalf("historical Store verifier over the refused Bash effect: %v", err)
	}
	if err := history.VerifyUnchanged(); err != nil {
		t.Fatal(err)
	}
}

// What a fresh process does with a Bash command that ran but has no matching settlement is the same for a
// promoted and a refused command: it refuses. A forged or mismatched settlement is refused too.
func TestProductionD09RecoveryRefusesUnsettledAndTamperedBashSettlements(t *testing.T) {
	denied, promoted := deniedBashRun(t), promotedBashRun(t)
	deniedBundle, _, deniedJournal := denied.reopen()
	promotedBundle, _, promotedJournal := promoted.reopen()
	deniedEvent := settledRecords(t, deniedJournal)[0].Event
	promotedEvent := settledRecords(t, promotedJournal)[0].Event

	var deniedPayload productionD09DeniedCommandSettledEvent
	var promotedPayload productionD09CommandSettledEvent
	if err := unmarshalGovernedJSON(deniedEvent.Data, &deniedPayload); err != nil {
		t.Fatal(err)
	}
	if err := unmarshalGovernedJSON(promotedEvent.Data, &promotedPayload); err != nil {
		t.Fatal(err)
	}
	encode := func(value any) []byte {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	// withEvent replaces the only settlement event of run's journal by data (nil drops it).
	withEvent := func(run *productionBashRun, journal *audit.Journal, data []byte) (*productionHistoricalBashVerifier, error) {
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		derived := derivedJournal(t, trace, func(_ int, event audit.Event) (audit.Event, bool) {
			if data == nil {
				return event, false
			}
			event.Data = data
			return event, true
		})
		return run.historicalVerifier(derived)
	}
	refused := func(name string, run *productionBashRun, journal *audit.Journal, bundle sessionrepo.EvidenceBundle, data []byte) {
		t.Helper()
		verifier, err := withEvent(run, journal, data)
		if err == nil {
			err = verifier.VerifyStoreBundle(bundle)
		}
		if err == nil {
			t.Errorf("%s: recovery accepted the Bash settlement", name)
		}
	}

	// Executed but unsettled, as after a crash between the Store effect and the settlement write.
	refused("promoted command without a settlement", promoted, promotedJournal, promotedBundle, nil)
	refused("refused command without a settlement", denied, deniedJournal, deniedBundle, nil)

	// A wrong admission record digest.
	wrongRecord := deniedPayload
	reference := *wrongRecord.Operation.Admission
	reference.RecordHash = "sha256:" + strings.Repeat("0", 64)
	wrongRecord.Operation.Admission = &reference
	refused("denied settlement with a wrong admission record digest", denied, deniedJournal, deniedBundle, encode(wrongRecord))

	// A wrong refused candidate, even with a matching candidate digest.
	wrongCandidate := deniedPayload
	wrongCandidate.DeniedCandidate.Changes = wrongCandidate.DeniedCandidate.Changes[:len(wrongCandidate.DeniedCandidate.Changes)-1]
	wrongCandidate.DeniedCandidateDigest = productionD09DeniedCandidateDigest(wrongCandidate.DeniedCandidate)
	refused("denied settlement of another candidate", denied, deniedJournal, deniedBundle, encode(wrongCandidate))

	// A candidate digest that does not match the candidate.
	wrongDigest := deniedPayload
	wrongDigest.DeniedCandidateDigest = "sha256:" + strings.Repeat("1", 64)
	refused("denied settlement with a wrong candidate digest", denied, deniedJournal, deniedBundle, encode(wrongDigest))

	// A success outcome on a denied effect: the promoted shape over the refused operation, in both
	// spellings of the claim.
	asPromoted := productionD09CommandSettledEvent{SchemaVersion: productionD09SettledSchemaSuccess,
		RunID: deniedPayload.RunID, SessionID: deniedPayload.SessionID, WorkflowID: deniedPayload.WorkflowID,
		ConversationID: deniedPayload.ConversationID, ProfileID: deniedPayload.ProfileID, ProfileDigest: deniedPayload.ProfileDigest,
		ProviderCall: deniedPayload.ProviderCall, Operation: deniedPayload.Operation, Transition: deniedPayload.DeniedCandidate,
		ReceiptDigest: deniedPayload.ReceiptDigest, Receipt: deniedPayload.Receipt}
	refused("promoted shape over a denied operation", denied, deniedJournal, deniedBundle, encode(asPromoted))
	asPromoted.Operation.Outcome = "success"
	refused("success outcome on a denied effect", denied, deniedJournal, deniedBundle, encode(asPromoted))
	relabelled := deniedPayload
	relabelled.Operation.Outcome = "success"
	refused("denied shape claiming a success operation", denied, deniedJournal, deniedBundle, encode(relabelled))

	// A denied outcome on a success effect.
	asDenied := productionD09DeniedCommandSettledEvent{SchemaVersion: productionD09SettledSchemaDenied, Outcome: productionD09OutcomeDenied,
		RunID: promotedPayload.RunID, SessionID: promotedPayload.SessionID, WorkflowID: promotedPayload.WorkflowID,
		ConversationID: promotedPayload.ConversationID, ProfileID: promotedPayload.ProfileID, ProfileDigest: promotedPayload.ProfileDigest,
		ProviderCall: promotedPayload.ProviderCall, Operation: promotedPayload.Operation, DeniedCandidate: promotedPayload.Transition,
		DeniedCandidateDigest: productionD09DeniedCandidateDigest(promotedPayload.Transition),
		ReceiptDigest:         promotedPayload.ReceiptDigest, Receipt: promotedPayload.Receipt}
	refused("denied shape over a success effect", promoted, promotedJournal, promotedBundle, encode(asDenied))
	asDenied.Operation.Outcome = "denied_after_effect"
	refused("denied outcome on a success effect", promoted, promotedJournal, promotedBundle, encode(asDenied))
	wrongOutcome := deniedPayload
	wrongOutcome.Outcome = "success"
	refused("denied event naming a success outcome", denied, deniedJournal, deniedBundle, encode(wrongOutcome))

	// The untouched events still verify, so each refusal above is the tamper and not the fixture.
	for name, check := range map[string]func() error{
		"denied": func() error {
			verifier, err := withEvent(denied, deniedJournal, deniedEvent.Data)
			if err == nil {
				err = verifier.VerifyStoreBundle(deniedBundle)
			}
			return err
		},
		"promoted": func() error {
			verifier, err := withEvent(promoted, promotedJournal, promotedEvent.Data)
			if err == nil {
				err = verifier.VerifyStoreBundle(promotedBundle)
			}
			return err
		},
	} {
		if err := check(); err != nil {
			t.Errorf("untouched %s settlement refused: %v", name, err)
		}
	}
}
