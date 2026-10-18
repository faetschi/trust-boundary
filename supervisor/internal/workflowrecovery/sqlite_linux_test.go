//go:build linux

package workflowrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type testRootHead struct {
	epoch       uint64
	digest      string
	incarnation string
}

type testRootDrain struct {
	after  uint64
	before uint64
}

type testRootAuthorization struct {
	epoch  uint64
	digest string
}

type testTrustedRoot struct {
	mu                 sync.Mutex
	identity           string
	fences             map[string]uint64
	incarnations       map[string]string
	heads              map[string]testRootHead
	drains             map[string]testRootDrain
	authorizations     map[string]testRootAuthorization
	authorizationCalls int
	boundaryCalls      int
	failNextCAS        error
	// deadlineNow is the clock VerifyWorkflowDeadline compares against; nil
	// means the real wall clock.
	deadlineNow func() time.Time
}

func newTestTrustedRoot() *testTrustedRoot {
	return &testTrustedRoot{
		identity:       testDigest("host-root-key-identity"),
		fences:         make(map[string]uint64),
		incarnations:   make(map[string]string),
		heads:          make(map[string]testRootHead),
		drains:         make(map[string]testRootDrain),
		authorizations: make(map[string]testRootAuthorization),
	}
}

func (r *testTrustedRoot) TrustedRootIdentityDigest() string { return r.identity }

func (r *testTrustedRoot) VerifyHostBoundary(_ context.Context, workflow string, store OwnerStoreIdentity, ownerIncarnation OwnerIncarnation) error {
	currentIncarnation, err := currentProcessIncarnation()
	if err != nil || !validID(workflow) || !validDigest(store.Digest) || store.Path == "" ||
		!reflect.DeepEqual(ownerIncarnation, currentIncarnation) || computeOwnerIncarnationDigest(ownerIncarnation) != ownerIncarnation.Digest {
		return errors.New("invalid protected owner-store identity")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.boundaryCalls++
	return nil
}

func (r *testTrustedRoot) VerifyFence(_ context.Context, workflow string, ownerEpoch uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fences[workflow] != ownerEpoch || ownerEpoch == 0 {
		return errors.New("owner epoch is not the exact active fence")
	}
	return nil
}

func (r *testTrustedRoot) AcquireOwnerEpoch(_ context.Context, workflow string, previousEpoch, nextEpoch uint64, ownerIncarnation OwnerIncarnation) error {
	currentIncarnation, err := currentProcessIncarnation()
	if err != nil || !reflect.DeepEqual(ownerIncarnation, currentIncarnation) || nextEpoch == 0 || nextEpoch <= previousEpoch {
		return errors.New("invalid owner incarnation or epoch acquisition")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if nextEpoch == previousEpoch+1 && r.fences[workflow] == nextEpoch && r.incarnations[workflow] == ownerIncarnation.Digest {
		return nil
	}
	if previousEpoch == 0 {
		if _, exists := r.heads[workflow]; exists || r.fences[workflow] > nextEpoch ||
			(r.fences[workflow] != 0 && r.fences[workflow] != nextEpoch) {
			return errors.New("fresh owner epoch cannot be acquired")
		}
	} else {
		drain, ok := r.drains[workflow]
		if !ok || drain.after > previousEpoch || drain.before < nextEpoch {
			return errors.New("replacement epoch acquisition requires all prior owners drained")
		}
		if r.fences[workflow] > nextEpoch {
			return errors.New("replacement owner epoch regressed behind the active fence")
		}
		if r.fences[workflow] == nextEpoch && r.incarnations[workflow] != "" && r.incarnations[workflow] != ownerIncarnation.Digest {
			return errors.New("candidate epoch is already bound to a different owner incarnation")
		}
	}
	r.fences[workflow] = nextEpoch
	r.incarnations[workflow] = ownerIncarnation.Digest
	return nil
}

func (r *testTrustedRoot) VerifyOwnersDrained(_ context.Context, workflow string, afterEpoch, beforeEpoch uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	drain, ok := r.drains[workflow]
	if !ok || drain.after > afterEpoch || drain.before < beforeEpoch || beforeEpoch <= afterEpoch {
		return errors.New("prior owner drain was not observed")
	}
	return nil
}

func (r *testTrustedRoot) VerifyCheckpoint(_ context.Context, workflow string, ownerEpoch uint64, digest, incarnation string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	head, exists := r.heads[workflow]
	if ownerEpoch == 0 && digest == "" && incarnation == "" {
		if exists {
			return errors.New("trusted root already has a workflow checkpoint")
		}
		return nil
	}
	if !exists || head.epoch != ownerEpoch || head.digest != digest || head.incarnation != incarnation {
		return errors.New("checkpoint does not match the external trusted root")
	}
	return nil
}

func (r *testTrustedRoot) VerifyWorkflowDeadline(_ context.Context, _ string, deadline time.Time) error {
	if deadline.IsZero() {
		return ErrClockUncertain
	}
	r.mu.Lock()
	now := time.Now
	if r.deadlineNow != nil {
		now = r.deadlineNow
	}
	r.mu.Unlock()
	if !now().Before(deadline) {
		return ErrDeadline
	}
	return nil
}

// setDeadlineClock pins the clock used by VerifyWorkflowDeadline so tests can
// cross a workflow deadline without sleeping against the real wall clock.
func (r *testTrustedRoot) setDeadlineClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadlineNow = now
}

func (r *testTrustedRoot) AuthorizeFreshWorkflow(_ context.Context, workflow string, ownerEpoch uint64, initialDigest string,
	incarnation OwnerIncarnation, deadline time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.heads[workflow]; exists || r.fences[workflow] != ownerEpoch ||
		r.incarnations[workflow] != incarnation.Digest || !validDigest(initialDigest) || deadline.IsZero() {
		return errors.New("fresh workflow is not authorized")
	}
	r.authorizationCalls++
	r.authorizations[workflow] = testRootAuthorization{epoch: ownerEpoch, digest: initialDigest}
	return nil
}

func (r *testTrustedRoot) CompareAndSwapCheckpoint(_ context.Context, workflow string, expectedOwnerEpoch uint64,
	expectedDigest string, nextOwnerEpoch uint64, nextDigest string, ownerIncarnation OwnerIncarnation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNextCAS != nil {
		err := r.failNextCAS
		r.failNextCAS = nil
		return err
	}
	if !validDigest(nextDigest) || r.fences[workflow] != nextOwnerEpoch || r.incarnations[workflow] != ownerIncarnation.Digest {
		return errors.New("next head does not match the active owner fence")
	}
	head, exists := r.heads[workflow]
	if expectedOwnerEpoch == 0 && expectedDigest == "" {
		authorization, authorized := r.authorizations[workflow]
		if exists || !authorized || authorization.epoch != nextOwnerEpoch || authorization.digest != nextDigest {
			return errors.New("fresh root authorization is absent or mismatched")
		}
		delete(r.authorizations, workflow)
	} else {
		if !exists || head.epoch != expectedOwnerEpoch || head.digest != expectedDigest {
			return errors.New("external checkpoint compare-and-swap mismatch")
		}
		if nextOwnerEpoch < expectedOwnerEpoch {
			return errors.New("owner epoch regressed")
		}
		if nextOwnerEpoch > expectedOwnerEpoch {
			drain, ok := r.drains[workflow]
			if !ok || drain.after > expectedOwnerEpoch || drain.before < nextOwnerEpoch {
				return errors.New("new epoch does not have a complete owner-drain assertion")
			}
		}
	}
	r.heads[workflow] = testRootHead{epoch: nextOwnerEpoch, digest: nextDigest, incarnation: ownerIncarnation.Digest}
	return nil
}

func (r *testTrustedRoot) setFence(workflow string, epoch uint64, drainedThrough uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fences[workflow] != epoch {
		r.incarnations[workflow] = ""
	}
	r.fences[workflow] = epoch
	if drainedThrough > 0 {
		r.drains[workflow] = testRootDrain{after: epoch - 1, before: drainedThrough}
	}
}

func (r *testTrustedRoot) seed(workflow string, fenceEpoch, checkpointEpoch uint64, digest, checkpointIncarnation string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fences[workflow] = fenceEpoch
	incarnation, _ := currentProcessIncarnation()
	r.incarnations[workflow] = incarnation.Digest
	r.heads[workflow] = testRootHead{epoch: checkpointEpoch, digest: digest, incarnation: checkpointIncarnation}
}

func (r *testTrustedRoot) authorizationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.authorizationCalls
}

type allowProgress struct{}

func (allowProgress) VerifyProgress(context.Context, Checkpoint, Pins) error { return nil }

type exactCapturedOperationVerifier struct {
	expected EffectBinding
}

func (v exactCapturedOperationVerifier) VerifyCapturedOperation(_ context.Context, cp Checkpoint, binding EffectBinding) error {
	if binding != v.expected || cp.Pins.ProviderDisposition != DispositionAwaitingGateDecision ||
		cp.Pins.PendingCallDisposition != PendingCallCaptured ||
		cp.Pins.HostCallReservation == nil || cp.Pins.HostCallReservation.CapturedCall == nil ||
		cp.Pins.HostCallReservation.CapturedCall.Disposition != CapturedCallAvailable ||
		!matchesCapturedEffectBinding(*cp.Pins.HostCallReservation.CapturedCall, binding) {
		return errors.New("exact captured operation and host ALLOW proof did not match")
	}
	return nil
}

func prepareCapturedCallForEffect(t *testing.T, ledger *Ledger, operationID, toolCallID string) (EffectBinding, Checkpoint) {
	t.Helper()
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-captured")); err != nil {
		t.Fatalf("register capture test conversation: %v", err)
	}
	if _, err := ledger.ReserveHostCall(context.Background(), testHostCallBinding("host-call-captured", 41)); err != nil {
		t.Fatalf("charge provider request before dispatch: %v", err)
	}
	checkpoint, _ := ledger.Snapshot()
	pins := clonePins(checkpoint.Pins)
	call := pins.HostCallReservation
	call.Disposition = HostCallTerminal
	call.OutcomeDigest = testDigest("verified provider response for captured call")
	call.CapturedCall = &CapturedCallPin{
		WorkflowID: checkpoint.Pins.WorkflowID, ConversationID: "conversation-captured",
		HostCallID: call.Binding.CallID, ToolCallID: toolCallID, OutcomeDigest: call.OutcomeDigest,
		RecoveryGeneration: checkpoint.Pins.RecoveryGeneration, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		Disposition: CapturedCallAvailable,
	}
	pins.ProviderTranscripts[0].JournalCount = 1
	pins.ProviderTranscripts[0].JournalHead = testDigest("captured provider journal head")
	pins.ProviderTranscripts[0].ProjectionCount = 1
	pins.ProviderTranscripts[0].ProjectionDigest = testDigest("captured provider projection")
	pins.ProviderTranscripts[0].ProjectionHead = testDigest("captured provider projection head")
	pins.ProviderTranscripts[0].Disposition = DispositionAwaitingGateDecision
	pins.ProviderProjectionCount = 1
	pins.ProviderProjectionDigest = TranscriptPinsDigest(pins.ProviderTranscripts)
	pins.ProviderProjectionHead = pins.ProviderTranscripts[0].ProjectionHead
	pins.ProviderDisposition = DispositionAwaitingGateDecision
	pins.PendingCallDisposition = PendingCallCaptured
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, pins); err != nil {
		t.Fatalf("persist verified terminal provider capture: %v", err)
	}
	checkpoint, _ = ledger.Snapshot()
	binding := testEffectBinding(operationID)
	binding.WorkflowID = checkpoint.Pins.WorkflowID
	binding.ConversationID = call.CapturedCall.ConversationID
	binding.HostCallID = call.CapturedCall.HostCallID
	binding.ToolCallID = call.CapturedCall.ToolCallID
	binding.HostCallOutcomeDigest = call.CapturedCall.OutcomeDigest
	binding.RecoveryGeneration = call.CapturedCall.RecoveryGeneration
	binding.OwnerEpoch = call.CapturedCall.OwnerEpoch
	return binding, checkpoint
}

type allowRecovery struct{}

func (allowRecovery) VerifyRecoveryObservation(context.Context, Checkpoint, Pins) error { return nil }

type explicitRetirementVerifier struct{}

func (explicitRetirementVerifier) VerifyRecoveryObservation(_ context.Context, previous Checkpoint, observed Pins) error {
	if previous.Pins.PendingCallDisposition != PendingCallInFlight ||
		previous.Pins.ProviderDisposition != DispositionAwaitingProviderExchange ||
		observed.PendingCallDisposition != PendingCallRetired || observed.ProviderDisposition != DispositionRetired ||
		observed.AdmissionJournalCount != previous.Pins.AdmissionJournalCount+1 ||
		observed.AdmissionJournalHead == previous.Pins.AdmissionJournalHead {
		return errors.New("host retirement record does not prove an unleased call with no effect authorization")
	}
	return nil
}

type denyRecovery struct{}

func (denyRecovery) VerifyRecoveryObservation(context.Context, Checkpoint, Pins) error {
	return errors.New("no independent retirement evidence")
}

func TestSQLiteOwnerUsesWALFullAndAuthorizesFreshExactlyOnce(t *testing.T) {
	owner, root, ledger, checkpoint, receipt := newWorkflowHarness(t, "workflow-fresh", testLimits())
	if receipt.WorkflowID() != checkpoint.Pins.WorkflowID || receipt.Counters() != (Counters{}) {
		t.Fatalf("fresh receipt does not bind the initial checkpoint: %+v", receipt)
	}
	if got := root.authorizationCount(); got != 1 {
		t.Fatalf("fresh external authorization calls = %d, want exactly one", got)
	}
	if _, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: checkpoint.Pins.WorkflowID, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
	}); err != nil {
		t.Fatalf("reopen validated fresh ledger: %v", err)
	}
	if got := root.authorizationCount(); got != 1 {
		t.Fatalf("reopen repeated fresh external authorization: got %d calls", got)
	}
	var journalMode string
	if err := owner.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil || journalMode != "wal" {
		t.Fatalf("journal_mode = %q, err=%v; want WAL", journalMode, err)
	}
	var synchronous int64
	if err := owner.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("synchronous = %d, err=%v; want FULL (2)", synchronous, err)
	}
	var engineVersion string
	if err := owner.db.QueryRow("SELECT sqlite_version()").Scan(&engineVersion); err != nil {
		t.Fatal(err)
	}
	if engineVersion != "3.53.4" {
		t.Fatalf("linked SQLite engine = %s, want pinned driver engine 3.53.4", engineVersion)
	}
	if err := requireWALResetFix(engineVersion); err != nil {
		t.Fatalf("pinned SQLite engine lacks required WAL fix: %v", err)
	}
	fileInfo, err := os.Stat(owner.path)
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("owner database mode = %v, err=%v; want 0600", fileInfo, err)
	}
	if err := validateSQLiteParent(owner.path); err != nil {
		t.Fatalf("owner database parent is not private: %v", err)
	}
	if current, digest := ledger.Snapshot(); current.ComputedCheckpointDigest != digest {
		t.Fatalf("ledger snapshot digest mismatch: checkpoint=%s snapshot=%s", current.ComputedCheckpointDigest, digest)
	}
}

func TestSQLiteOwnerRollsBackFailedMultiRecordReservation(t *testing.T) {
	owner, root, ledger, _, _ := newWorkflowHarness(t, "workflow-rollback", testLimits())
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	before, beforeDigest := ledger.Snapshot()
	if _, err := owner.db.Exec(`CREATE TRIGGER inject_recovery_insert_failure
		BEFORE INSERT ON workflow_recovery_events WHEN NEW.sequence>1
		BEGIN SELECT RAISE(ABORT,'injected reservation rollback'); END`); err != nil {
		t.Fatalf("install rollback trigger: %v", err)
	}
	if _, err := ledger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrCAS) {
		t.Fatalf("injected transaction failure = %v, want ErrCAS", err)
	}
	after, afterDigest := ledger.Snapshot()
	if afterDigest != beforeDigest || after.Counters != before.Counters || after.ComputedCheckpointDigest != before.ComputedCheckpointDigest {
		t.Fatalf("failed SQL transaction changed in-memory ledger: before=%+v after=%+v", before.Counters, after.Counters)
	}
	if _, err := owner.db.Exec("DROP TRIGGER inject_recovery_insert_failure"); err != nil {
		t.Fatalf("remove rollback trigger: %v", err)
	}
	stored, storedDigest, exists, err := owner.LoadCheckpoint(context.Background(), before.Pins.WorkflowID)
	if err != nil || !exists || storedDigest != beforeDigest || stored.Counters != before.Counters {
		t.Fatalf("rolled-back checkpoint = %+v digest=%s exists=%v err=%v", stored.Counters, storedDigest, exists, err)
	}
	reservation, err := ledger.ReserveHostCall(context.Background(), testHostCallBinding("call-rollback", 41))
	if err != nil {
		t.Fatalf("reserve provider call after rollback: %v", err)
	}
	if reservation.Counters().HostCalls != 1 || reservation.Counters().Tokens != 41 {
		t.Fatalf("host-call reservation counters = %+v", reservation.Counters())
	}
	root.mu.Lock()
	head := root.heads[before.Pins.WorkflowID]
	root.mu.Unlock()
	if head.digest != reservation.CheckpointDigest() {
		t.Fatalf("root checkpoint digest = %s, receipt digest = %s", head.digest, reservation.CheckpointDigest())
	}
}

func TestReserveEffectUsesTypedNoTokenReservation(t *testing.T) {
	owner, _, ledger, _, _ := newWorkflowHarness(t, "workflow-effect-reservation", testLimits())
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	previous, previousDigest := ledger.Snapshot()
	forged := previous
	forged.Counters.HostCalls++
	forged.PreviousCheckpointDigest = previousDigest
	forged.ComputedCheckpointDigest = CheckpointDigest(forged)
	if _, err := owner.CompareAndSwapCheckpoint(context.Background(), previous.Pins.WorkflowID,
		previous.Pins.OwnerEpoch, previousDigest, forged, TransitionProof{}); !errors.Is(err, ErrObservation) {
		t.Fatalf("untyped no-token counter increment = %v, want ErrObservation", err)
	}
	receipt, err := ledger.ReserveEffect(context.Background(), testEffectBinding("operation-1"), nil)
	if err != nil {
		t.Fatalf("durably reserve effect call: %v", err)
	}
	if receipt.Counters().HostCalls != 1 || receipt.Counters().Tokens != 0 {
		t.Fatalf("typed effect reservation counters = %+v", receipt.Counters())
	}
	stored, digest, exists, err := owner.LoadCheckpoint(context.Background(), previous.Pins.WorkflowID)
	if err != nil || !exists || digest != receipt.CheckpointDigest() || stored.Counters != receipt.Counters() {
		t.Fatalf("replayed typed effect reservation = %+v digest=%s exists=%v err=%v", stored.Counters, digest, exists, err)
	}
}

func TestReserveEffectConsumesExactCapturedCallOnce(t *testing.T) {
	_, _, ledger, _, _ := newWorkflowHarness(t, "workflow-captured-effect", testLimits())
	binding, before := prepareCapturedCallForEffect(t, ledger, "operation-captured", "provider-tool-call-17")
	if before.Pins.PendingCallDisposition != PendingCallCaptured {
		t.Fatalf("provider tool call was mislabeled terminal before Store outcome: %q", before.Pins.PendingCallDisposition)
	}
	verifier := exactCapturedOperationVerifier{expected: binding}
	if _, err := ledger.ReserveEffect(context.Background(), binding, verifier); err != nil {
		t.Fatalf("reserve effect from exact terminal captured call: %v", err)
	}
	after, digest := ledger.Snapshot()
	if after.Counters.HostCalls != before.Counters.HostCalls+1 || after.Counters.Tokens != before.Counters.Tokens ||
		after.Pins.PendingCallDisposition != PendingCallCaptured ||
		after.Pins.EffectReservation == nil || after.Pins.EffectReservation.Disposition != EffectReservationReserved ||
		after.Pins.EffectReservation.Binding != binding || after.Pins.HostCallReservation.CapturedCall == nil ||
		after.Pins.HostCallReservation.CapturedCall.Disposition != CapturedCallConsumed {
		t.Fatalf("effect did not atomically debit and consume the captured call: counters=%+v pins=%+v", after.Counters, after.Pins)
	}
	if digest == before.ComputedCheckpointDigest {
		t.Fatal("effect reservation did not advance the durable checkpoint head")
	}

	if _, err := ledger.ReserveEffect(context.Background(), binding, verifier); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("duplicate reservation for consumed provider call = %v, want ErrPendingUnknown", err)
	}
	duplicate, duplicateDigest := ledger.Snapshot()
	if duplicateDigest != digest || duplicate.Counters != after.Counters {
		t.Fatalf("duplicate operation changed checkpoint or allowance: before=%+v after=%+v", after.Counters, duplicate.Counters)
	}
	newOperation := binding
	newOperation.OperationID = "different-operation-same-captured-call"
	newOperationVerifier := exactCapturedOperationVerifier{expected: newOperation}
	if _, err := ledger.ReserveEffect(context.Background(), newOperation, newOperationVerifier); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("new operation identity reused consumed ToolCallID = %v, want ErrPendingUnknown", err)
	}
	stillConsumed, stillConsumedDigest := ledger.Snapshot()
	if stillConsumedDigest != digest || stillConsumed.Counters != after.Counters {
		t.Fatalf("new operation identity changed consumed-call allowance: before=%+v after=%+v", after.Counters, stillConsumed.Counters)
	}
}

func TestCapturedCallCannotBeRelabeledTerminalBeforeStoreOutcome(t *testing.T) {
	_, _, ledger, _, _ := newWorkflowHarness(t, "workflow-captured-not-terminal", testLimits())
	binding, before := prepareCapturedCallForEffect(t, ledger, "operation-still-pending", "provider-tool-call-pending")
	verifier := exactCapturedOperationVerifier{expected: binding}

	for _, disposition := range []string{PendingCallInFlight, PendingCallTerminal, PendingCallUnknown} {
		t.Run(disposition, func(t *testing.T) {
			pins := clonePins(before.Pins)
			pins.PendingCallDisposition = disposition
			if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, pins); err == nil {
				t.Fatalf("accepted %q for a tool call still awaiting gate/effect settlement", disposition)
			}
			current, digest := ledger.Snapshot()
			if digest != before.ComputedCheckpointDigest || current.Counters != before.Counters {
				t.Fatalf("rejected disposition changed checkpoint or allowance: counters=%+v", current.Counters)
			}
		})
	}
	if _, err := ledger.ReserveEffect(context.Background(), binding, verifier); err != nil {
		t.Fatalf("exact captured call was not admissible after rejecting premature dispositions: %v", err)
	}
}

func TestLaterCapturedCallCanFollowSettledRetiredCall(t *testing.T) {
	_, _, ledger, _, _ := newWorkflowHarness(t, "workflow-captured-multiple-calls", testLimits())
	_, first := prepareCapturedCallForEffect(t, ledger, "operation-first-denied", "provider-tool-call-first")

	retired := clonePins(first.Pins)
	retired.ProviderDisposition = DispositionAwaitingHostPrompt
	retired.ProviderTranscripts[0].Disposition = DispositionAwaitingHostPrompt
	retired.PendingCallDisposition = PendingCallRetired
	retiredCapture := retired.HostCallReservation.CapturedCall
	retiredCapture.Disposition = CapturedCallRetired
	retiredCapture.RetirementDigest = testDigest("verified denied-call retirement")
	retired.ProviderProjectionDigest = TranscriptPinsDigest(retired.ProviderTranscripts)
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, retired); err != nil {
		t.Fatalf("settle the first denied provider call: %v", err)
	}

	secondHostBinding := testHostCallBinding("host-call-second", 41)
	if _, err := ledger.ReserveHostCall(context.Background(), secondHostBinding); err != nil {
		t.Fatalf("charge the next provider request before dispatch: %v", err)
	}
	checkpoint, _ := ledger.Snapshot()
	second := clonePins(checkpoint.Pins)
	second.PendingCallDisposition = PendingCallCaptured
	second.ProviderDisposition = DispositionAwaitingGateDecision
	second.ProviderTranscripts[0].Disposition = DispositionAwaitingGateDecision
	second.ProviderTranscripts[0].JournalCount++
	second.ProviderTranscripts[0].JournalHead = testDigest("second provider response journal head")
	second.ProviderTranscripts[0].ProjectionCount++
	second.ProviderTranscripts[0].ProjectionDigest = testDigest("second provider response projection")
	second.ProviderTranscripts[0].ProjectionHead = testDigest("second provider response projection head")
	second.ProviderProjectionCount++
	second.ProviderProjectionDigest = TranscriptPinsDigest(second.ProviderTranscripts)
	second.ProviderProjectionHead = second.ProviderTranscripts[0].ProjectionHead
	second.HostCallReservation.Disposition = HostCallTerminal
	second.HostCallReservation.OutcomeDigest = testDigest("second terminal host-call outcome")
	second.HostCallReservation.CapturedCall = &CapturedCallPin{
		WorkflowID: checkpoint.Pins.WorkflowID, ConversationID: "conversation-captured",
		HostCallID: secondHostBinding.CallID, ToolCallID: "provider-tool-call-second",
		OutcomeDigest:      second.HostCallReservation.OutcomeDigest,
		RecoveryGeneration: checkpoint.Pins.RecoveryGeneration, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		Disposition: CapturedCallAvailable,
	}
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, second); err != nil {
		t.Fatalf("record a different captured call after the prior call was retired: %v", err)
	}

	secondBinding := testEffectBinding("operation-second")
	secondBinding.WorkflowID = checkpoint.Pins.WorkflowID
	secondBinding.ConversationID = "conversation-captured"
	secondBinding.HostCallID = secondHostBinding.CallID
	secondBinding.ToolCallID = "provider-tool-call-second"
	secondBinding.HostCallOutcomeDigest = second.HostCallReservation.OutcomeDigest
	secondBinding.RecoveryGeneration = checkpoint.Pins.RecoveryGeneration
	secondBinding.OwnerEpoch = checkpoint.Pins.OwnerEpoch
	if _, err := ledger.ReserveEffect(context.Background(), secondBinding,
		exactCapturedOperationVerifier{expected: secondBinding}); err != nil {
		t.Fatalf("authorize only the second exact captured call: %v", err)
	}
}

func TestReserveEffectRejectsMismatchedCapturedOperationBindings(t *testing.T) {
	_, _, ledger, _, _ := newWorkflowHarness(t, "workflow-captured-mismatch", testLimits())
	binding, before := prepareCapturedCallForEffect(t, ledger, "operation-mismatch-base", "provider-tool-call-18")
	verifier := exactCapturedOperationVerifier{expected: binding}
	cases := map[string]func(*EffectBinding){
		"workflow":           func(b *EffectBinding) { b.WorkflowID = "other-workflow" },
		"conversation":       func(b *EffectBinding) { b.ConversationID = "other-conversation" },
		"host call":          func(b *EffectBinding) { b.HostCallID = "other-host-call" },
		"tool call":          func(b *EffectBinding) { b.ToolCallID = "other-tool-call" },
		"host outcome":       func(b *EffectBinding) { b.HostCallOutcomeDigest = testDigest("other host outcome") },
		"generation":         func(b *EffectBinding) { b.RecoveryGeneration++ },
		"owner fence":        func(b *EffectBinding) { b.OwnerEpoch++ },
		"operation identity": func(b *EffectBinding) { b.OperationID = "different-operation" },
		"operation spec":     func(b *EffectBinding) { b.SpecDigest = testDigest("different typed operation") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			wrong := binding
			mutate(&wrong)
			if _, err := ledger.ReserveEffect(context.Background(), wrong, verifier); !errors.Is(err, ErrObservation) {
				t.Fatalf("mismatched captured binding = %v, want ErrObservation", err)
			}
			current, digest := ledger.Snapshot()
			if digest != before.ComputedCheckpointDigest || current.Counters != before.Counters {
				t.Fatalf("rejected binding changed durable allowance: counters=%+v", current.Counters)
			}
		})
	}
}

func TestReserveEffectRejectsUnsettledAndUnknownProviderCalls(t *testing.T) {
	_, _, ledger, _, _ := newWorkflowHarness(t, "workflow-captured-unsettled", testLimits())
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-unsettled")); err != nil {
		t.Fatalf("register unsettled test conversation: %v", err)
	}
	if _, err := ledger.ReserveHostCall(context.Background(), testHostCallBinding("host-call-unsettled", 41)); err != nil {
		t.Fatalf("reserve provider request before HTTP: %v", err)
	}
	reserved, reservedDigest := ledger.Snapshot()
	if _, err := ledger.ReserveEffect(context.Background(), testEffectBinding("operation-before-http-settlement"), nil); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("effect while HTTP reservation remains unsettled = %v, want ErrPendingUnknown", err)
	}
	unchanged, unchangedDigest := ledger.Snapshot()
	if unchangedDigest != reservedDigest || unchanged.Counters != reserved.Counters {
		t.Fatalf("unsettled HTTP attempt changed durable allowance: before=%+v after=%+v", reserved.Counters, unchanged.Counters)
	}

	unknown := clonePins(reserved.Pins)
	unknown.HostCallReservation.Disposition = HostCallUnknown
	unknown.ProviderTranscripts[0].JournalCount = 1
	unknown.ProviderTranscripts[0].JournalHead = testDigest("unknown HTTP journal head")
	unknown.ProviderTranscripts[0].Disposition = DispositionUnknown
	unknown.ProviderDisposition = DispositionUnknown
	unknown.PendingCallDisposition = PendingCallUnknown
	unknown.ProviderProjectionDigest = TranscriptPinsDigest(unknown.ProviderTranscripts)
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, unknown); err != nil {
		t.Fatalf("record uncertain provider outcome for recovery inspection: %v", err)
	}
	unknownCheckpoint, unknownDigest := ledger.Snapshot()
	if _, err := ledger.ReserveEffect(context.Background(), testEffectBinding("operation-after-unknown-http"), nil); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("effect while HTTP outcome is unknown = %v, want ErrPendingUnknown", err)
	}
	stillUnknown, stillUnknownDigest := ledger.Snapshot()
	if stillUnknownDigest != unknownDigest || stillUnknown.Counters != unknownCheckpoint.Counters {
		t.Fatalf("unknown HTTP attempt changed durable allowance: before=%+v after=%+v", unknownCheckpoint.Counters, stillUnknown.Counters)
	}
}

func TestFreshCheckpointCannotSeedCapturedCallAuthority(t *testing.T) {
	pins := testPins(testDigest("fresh capture root"), testDigest("fresh capture incarnation"),
		"workflow-forged-capture", "session-forged-capture", 1, 1)
	transcript := emptyTranscript("conversation-forged-capture")
	transcript.JournalCount = 1
	transcript.JournalHead = testDigest("forged capture provider journal")
	transcript.ProjectionCount = 1
	transcript.ProjectionDigest = testDigest("forged capture projection")
	transcript.ProjectionHead = testDigest("forged capture projection head")
	transcript.Disposition = DispositionAwaitingGateDecision
	pins.ProviderTranscripts = []TranscriptPin{transcript}
	pins.ProviderProjectionCount = 1
	pins.ProviderProjectionDigest = TranscriptPinsDigest(pins.ProviderTranscripts)
	pins.ProviderProjectionHead = transcript.ProjectionHead
	pins.ProviderDisposition = DispositionAwaitingGateDecision
	pins.PendingCallDisposition = PendingCallCaptured
	hostCall := testHostCallBinding("host-call-forged-capture", 10)
	callOutcome := testDigest("forged provider outcome")
	pins.HostCallReservation = &HostCallReservationPin{
		Binding: hostCall, Disposition: HostCallTerminal, OutcomeDigest: callOutcome,
		CapturedCall: &CapturedCallPin{
			WorkflowID: pins.WorkflowID, ConversationID: transcript.ConversationID,
			HostCallID: hostCall.CallID, ToolCallID: "provider-tool-call-forged", OutcomeDigest: callOutcome,
			RecoveryGeneration: pins.RecoveryGeneration, OwnerEpoch: pins.OwnerEpoch,
			Disposition: CapturedCallAvailable,
		},
	}
	checkpoint := Checkpoint{SchemaVersion: checkpointSchema, Pins: pins, Limits: normalizeLimits(testLimits())}
	checkpoint.ComputedCheckpointDigest = CheckpointDigest(checkpoint)
	if err := validateOwnerTransition(Checkpoint{}, "", false, checkpoint, "", TransitionProof{}); !errors.Is(err, ErrRollback) {
		t.Fatalf("fresh checkpoint seeded a captured call without a prior verified exchange: %v", err)
	}
}

func TestSQLiteOwnerFreshProcessReopenKeepsUncertainReservationCharged(t *testing.T) {
	owner, root, ledger, _, _ := newWorkflowHarness(t, "workflow-fresh-process", testLimits())
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	reservation, err := ledger.ReserveHostCall(context.Background(), testHostCallBinding("call-fresh-process", 73))
	if err != nil {
		t.Fatalf("reserve host call: %v", err)
	}
	checkpoint, digest := ledger.Snapshot()
	if err := owner.Close(); err != nil {
		t.Fatalf("close before fresh-process recovery: %v", err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestSQLiteOwnerFreshProcessHelper$")
	command.Env = append(os.Environ(),
		"TBOUND_WORKFLOW_RECOVERY_HELPER=1",
		"TBOUND_WORKFLOW_RECOVERY_PATH="+owner.path,
		"TBOUND_WORKFLOW_RECOVERY_ID="+checkpoint.Pins.WorkflowID,
		"TBOUND_WORKFLOW_RECOVERY_ROOT="+root.identity,
		"TBOUND_WORKFLOW_RECOVERY_EPOCH="+strconv.FormatUint(checkpoint.Pins.OwnerEpoch, 10),
		"TBOUND_WORKFLOW_RECOVERY_DIGEST="+digest,
		"TBOUND_WORKFLOW_RECOVERY_INCARNATION="+checkpoint.Pins.OwnerIncarnationDigest,
		"TBOUND_WORKFLOW_RECOVERY_CALLS="+strconv.FormatUint(checkpoint.Counters.HostCalls, 10),
		"TBOUND_WORKFLOW_RECOVERY_TOKENS="+strconv.FormatUint(checkpoint.Counters.Tokens, 10),
		"TBOUND_WORKFLOW_RECOVERY_EXPECT_READONLY=1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh-process reopen failed: %v\n%s", err, output)
	}
	if reservation.Counters().HostCalls != 1 || reservation.Counters().Tokens != 73 {
		t.Fatalf("uncertain reservation was not charged: %+v", reservation.Counters())
	}
	owner, err = OpenExistingSQLiteOwner(owner.path, root)
	if err != nil {
		t.Fatalf("reopen owner after helper process: %v", err)
	}
	defer owner.Close()
	stored, storedDigest, exists, err := owner.LoadCheckpoint(context.Background(), checkpoint.Pins.WorkflowID)
	if err != nil || !exists || storedDigest != digest || stored.Counters != checkpoint.Counters {
		t.Fatalf("fresh-process state = %+v digest=%s exists=%v err=%v", stored.Counters, storedDigest, exists, err)
	}
}

func TestSQLiteOwnerFreshProcessHelper(t *testing.T) {
	if os.Getenv("TBOUND_WORKFLOW_RECOVERY_HELPER") != "1" {
		return
	}
	path := os.Getenv("TBOUND_WORKFLOW_RECOVERY_PATH")
	workflow := os.Getenv("TBOUND_WORKFLOW_RECOVERY_ID")
	rootID := os.Getenv("TBOUND_WORKFLOW_RECOVERY_ROOT")
	epoch, err := strconv.ParseUint(os.Getenv("TBOUND_WORKFLOW_RECOVERY_EPOCH"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	digest := os.Getenv("TBOUND_WORKFLOW_RECOVERY_DIGEST")
	checkpointIncarnation := os.Getenv("TBOUND_WORKFLOW_RECOVERY_INCARNATION")
	calls, err := strconv.ParseUint(os.Getenv("TBOUND_WORKFLOW_RECOVERY_CALLS"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := strconv.ParseUint(os.Getenv("TBOUND_WORKFLOW_RECOVERY_TOKENS"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	root := newTestTrustedRoot()
	root.identity = rootID
	root.seed(workflow, epoch, epoch, digest, checkpointIncarnation)
	owner, err := OpenExistingSQLiteOwner(path, root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	checkpoint, loadedDigest, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists || loadedDigest != digest || checkpoint.Counters.HostCalls != calls || checkpoint.Counters.Tokens != tokens {
		t.Fatalf("fresh-process checkpoint = %+v digest=%s exists=%v err=%v", checkpoint.Counters, loadedDigest, exists, err)
	}
	ledger, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: workflow, OwnerEpoch: epoch, RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
	})
	if err != nil {
		t.Fatalf("fresh-process ledger open: %v", err)
	}
	if os.Getenv("TBOUND_WORKFLOW_RECOVERY_EXPECT_READONLY") == "1" {
		attempts := []struct {
			name string
			call func() error
		}{
			{name: "retry", call: func() error { _, err := ledger.ReservePreEffectRetry(context.Background()); return err }},
			{name: "provider", call: func() error {
				_, err := ledger.ReserveHostCall(context.Background(), testHostCallBinding("call-readonly", 1))
				return err
			}},
			{name: "effect", call: func() error {
				_, err := ledger.ReserveEffect(context.Background(), testEffectBinding("operation-readonly"), nil)
				return err
			}},
		}
		for _, attempt := range attempts {
			if err := attempt.call(); !errors.Is(err, ErrPendingUnknown) {
				t.Fatalf("new process reserved same-epoch %s authority: %v", attempt.name, err)
			}
		}
	}
}

func TestSQLiteOwnerCrashAfterReservationBeforeProviderDispatch(t *testing.T) {
	owner, root, ledger, _, _ := newWorkflowHarness(t, "workflow-reservation-crash", testLimits())
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	checkpoint, beforeDigest := settleInitialTranscript(t, ledger)
	if err := owner.Close(); err != nil {
		t.Fatalf("close before reservation crash test: %v", err)
	}
	output, exitCode := runCrashCutpoint(t, owner.path, root.identity, checkpoint, beforeDigest, "after-reservation")
	if exitCode != 41 {
		t.Fatalf("reservation crash helper exit = %d, want 41; output=%s", exitCode, output)
	}
	var reservedDigest string
	var reservedIncarnation string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "RESERVED_CHECKPOINT=") {
			reservedDigest = strings.TrimPrefix(line, "RESERVED_CHECKPOINT=")
		}
		if strings.HasPrefix(line, "RESERVED_INCARNATION=") {
			reservedIncarnation = strings.TrimPrefix(line, "RESERVED_INCARNATION=")
		}
	}
	if !validDigest(reservedDigest) || !validDigest(reservedIncarnation) {
		t.Fatalf("crash helper did not report the externally committed checkpoint: %s", output)
	}
	rootAfterCrash := newTestTrustedRoot()
	rootAfterCrash.identity = root.identity
	rootAfterCrash.seed(checkpoint.Pins.WorkflowID, checkpoint.Pins.OwnerEpoch+1, checkpoint.Pins.OwnerEpoch+1,
		reservedDigest, reservedIncarnation)
	owner, err := OpenExistingSQLiteOwner(owner.path, rootAfterCrash)
	if err != nil {
		t.Fatalf("reopen SQLite after abrupt reservation process exit: %v", err)
	}
	defer owner.Close()
	stored, digest, exists, err := owner.LoadCheckpoint(context.Background(), checkpoint.Pins.WorkflowID)
	if err != nil || !exists || digest != reservedDigest || stored.Counters.CleanRestarts != 1 ||
		stored.Counters.HostCalls != 1 || stored.Counters.Tokens != 23 {
		t.Fatalf("post-crash reservation = %+v digest=%s exists=%v err=%v", stored.Counters, digest, exists, err)
	}
}

func TestSQLiteOwnerCrashBeforeExternalCASLeavesUnadmittedTail(t *testing.T) {
	owner, root, ledger, _, _ := newWorkflowHarness(t, "workflow-unanchored-crash", testLimits())
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	checkpoint, beforeDigest := settleInitialTranscript(t, ledger)
	if err := owner.Close(); err != nil {
		t.Fatalf("close before unanchored-tail crash test: %v", err)
	}
	output, exitCode := runCrashCutpoint(t, owner.path, root.identity, checkpoint, beforeDigest, "before-root-cas")
	if exitCode != 42 {
		t.Fatalf("pre-root-CAS crash helper exit = %d, want 42; output=%s", exitCode, output)
	}
	var restartDigest string
	var restartIncarnation string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "RESTART_CHECKPOINT=") {
			restartDigest = strings.TrimPrefix(line, "RESTART_CHECKPOINT=")
		}
		if strings.HasPrefix(line, "RESTART_INCARNATION=") {
			restartIncarnation = strings.TrimPrefix(line, "RESTART_INCARNATION=")
		}
	}
	if !validDigest(restartDigest) || !validDigest(restartIncarnation) {
		t.Fatalf("crash helper did not report committed clean-restart checkpoint: %s", output)
	}
	rootAtRestart := newTestTrustedRoot()
	rootAtRestart.identity = root.identity
	rootAtRestart.seed(checkpoint.Pins.WorkflowID, checkpoint.Pins.OwnerEpoch+1,
		checkpoint.Pins.OwnerEpoch+1, restartDigest, restartIncarnation)
	owner, err := OpenExistingSQLiteOwner(owner.path, rootAtRestart)
	if err != nil {
		t.Fatalf("reopen SQLite after crash: %v", err)
	}
	defer owner.Close()
	if _, _, _, err := owner.LoadCheckpoint(context.Background(), checkpoint.Pins.WorkflowID); !errors.Is(err, ErrRollback) {
		t.Fatalf("unanchored transaction tail admission = %v, want ErrRollback", err)
	}
}

func TestSQLiteOwnerCrashCutpointHelper(t *testing.T) {
	mode := os.Getenv("TBOUND_WORKFLOW_RECOVERY_CRASH_MODE")
	if mode == "" {
		return
	}
	path := os.Getenv("TBOUND_WORKFLOW_RECOVERY_PATH")
	workflow := os.Getenv("TBOUND_WORKFLOW_RECOVERY_ID")
	rootID := os.Getenv("TBOUND_WORKFLOW_RECOVERY_ROOT")
	previousEpoch, err := strconv.ParseUint(os.Getenv("TBOUND_WORKFLOW_RECOVERY_EPOCH"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	digest := os.Getenv("TBOUND_WORKFLOW_RECOVERY_DIGEST")
	checkpointIncarnation := os.Getenv("TBOUND_WORKFLOW_RECOVERY_INCARNATION")
	root := newTestTrustedRoot()
	root.identity = rootID
	root.seed(workflow, previousEpoch, previousEpoch, digest, checkpointIncarnation)
	ownerEpoch := previousEpoch + 1
	root.setFence(workflow, ownerEpoch, ownerEpoch)
	var trustedRoot TrustedRoot = root
	if mode == "before-root-cas" {
		trustedRoot = &crashAfterRootCAS{testTrustedRoot: root, allowedCalls: 1}
	}
	owner, err := OpenExistingSQLiteOwner(path, trustedRoot)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, loadedDigest, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists || loadedDigest != digest {
		t.Fatalf("load crash-cutpoint pre-state: exists=%v digest=%s err=%v", exists, loadedDigest, err)
	}
	ledger, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: workflow, OwnerEpoch: ownerEpoch, RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	observed := clonePins(checkpoint.Pins)
	observation, err := ledger.ObserveRecovery(context.Background(), allowRecovery{}, observed)
	if err != nil {
		t.Fatalf("verify settled checkpoint before crash cutpoint: %v", err)
	}
	nextPins := clonePins(observed)
	nextPins.SessionID = "session-crash-restart"
	nextPins.RecoveryGeneration++
	nextPins.OwnerEpoch = ownerEpoch
	nextPins.OwnerIncarnationDigest = owner.OwnerIncarnationDigest()
	nextPins.PreviousRecoveryLineageDigest = observed.RecoveryLineageDigest
	nextPins.PendingCallDisposition = PendingCallNone
	nextTranscript := emptyTranscript(fmt.Sprintf("conversation-%d", len(observed.ProviderTranscripts)+1))
	nextTranscript.Ordinal = uint64(len(observed.ProviderTranscripts) + 1)
	nextPins.ProviderTranscripts = append(append([]TranscriptPin(nil), observed.ProviderTranscripts...), nextTranscript)
	nextPins.ProviderProjectionCount = observed.ProviderProjectionCount
	nextPins.ProviderProjectionHead = ""
	nextPins.ProviderProjectionDigest = digestTranscriptPins(nextPins.ProviderTranscripts)
	nextPins.ProviderDisposition = DispositionAwaitingHostPrompt
	nextPins.RecoveryLineageDigest = ""
	nextPins.RecoveryLineageDigest = ComputeRecoveryLineageDigest(loadedDigest, observed, nextPins)
	restartReceipt, err := ledger.ReserveCleanRestart(context.Background(), observation, nextPins)
	if err != nil {
		t.Fatalf("reserve clean restart before crash cutpoint: %v", err)
	}
	if err := ledger.VerifyCleanRestartReceipt(context.Background(), restartReceipt, nextPins); err != nil {
		t.Fatalf("verify clean-restart receipt before crash cutpoint: %v", err)
	}
	if mode == "before-root-cas" {
		fmt.Printf("RESTART_CHECKPOINT=%s\n", restartReceipt.CheckpointDigest())
		fmt.Printf("RESTART_INCARNATION=%s\n", owner.OwnerIncarnationDigest())
	}
	switch mode {
	case "after-reservation":
		receipt, err := ledger.ReserveHostCall(context.Background(), testHostCallBinding("call-crash", 23))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("RESERVED_CHECKPOINT=%s\n", receipt.CheckpointDigest())
		fmt.Printf("RESERVED_INCARNATION=%s\n", owner.OwnerIncarnationDigest())
		os.Exit(41)
	case "before-root-cas":
		_, _ = ledger.ReservePreEffectRetry(context.Background())
		t.Fatal("root compare-and-swap returned without injected process termination")
	default:
		t.Fatalf("unknown crash mode %q", mode)
	}
}

type crashAfterRootCAS struct {
	*testTrustedRoot
	allowedCalls int
	calls        int
}

func (r *crashAfterRootCAS) CompareAndSwapCheckpoint(ctx context.Context, workflow string, expectedOwnerEpoch uint64,
	expectedDigest string, nextOwnerEpoch uint64, nextDigest string, ownerIncarnation OwnerIncarnation) error {
	r.calls++
	if r.calls > r.allowedCalls {
		os.Exit(42)
		return errors.New("unreachable after injected process termination")
	}
	return r.testTrustedRoot.CompareAndSwapCheckpoint(ctx, workflow, expectedOwnerEpoch, expectedDigest,
		nextOwnerEpoch, nextDigest, ownerIncarnation)
}

func runCrashCutpoint(t *testing.T, path, rootIdentity string, checkpoint Checkpoint, digest, mode string) ([]byte, int) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestSQLiteOwnerCrashCutpointHelper$")
	command.Env = append(os.Environ(),
		"TBOUND_WORKFLOW_RECOVERY_CRASH_MODE="+mode,
		"TBOUND_WORKFLOW_RECOVERY_PATH="+path,
		"TBOUND_WORKFLOW_RECOVERY_ID="+checkpoint.Pins.WorkflowID,
		"TBOUND_WORKFLOW_RECOVERY_ROOT="+rootIdentity,
		"TBOUND_WORKFLOW_RECOVERY_EPOCH="+strconv.FormatUint(checkpoint.Pins.OwnerEpoch, 10),
		"TBOUND_WORKFLOW_RECOVERY_DIGEST="+digest,
		"TBOUND_WORKFLOW_RECOVERY_INCARNATION="+checkpoint.Pins.OwnerIncarnationDigest,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		return output, 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run crash-cutpoint helper: %v\n%s", err, output)
	}
	return output, exitError.ExitCode()
}

func TestSQLiteOwnerAcceptsExternallyVerifiedAppendOnlyTailOnCleanRestart(t *testing.T) {
	owner, root, initialLedger, initial, _ := newWorkflowHarness(t, "workflow-restart-tail", testLimits())
	if _, err := initialLedger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	if _, err := initialLedger.ReservePreEffectRetry(context.Background()); err != nil {
		t.Fatalf("reserve first pre-effect retry: %v", err)
	}
	staleCheckpoint, staleDigest := initialLedger.Snapshot()
	root.setFence(initial.Pins.WorkflowID, 2, 2)
	newOwnerLedger, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: initial.Pins.WorkflowID, OwnerEpoch: 2,
		RetainedPins: staleCheckpoint.Pins, Limits: staleCheckpoint.Limits,
	})
	if err != nil {
		t.Fatalf("open drained replacement owner: %v", err)
	}
	observed := clonePins(staleCheckpoint.Pins)
	observed.ProviderTranscripts[0].JournalCount = 2
	observed.ProviderTranscripts[0].JournalHead = testDigest("provider-journal-tail")
	observed.ProviderTranscripts[0].Disposition = DispositionTerminal
	observed.ProviderDisposition = DispositionTerminal
	observed.LastDecisionDigest = testDigest("last-decision")
	observed.LastResultDigest = testDigest("last-result")
	observed.ProviderProjectionDigest = digestTranscriptPins(observed.ProviderTranscripts)
	observation, err := newOwnerLedger.ObserveRecovery(context.Background(), allowRecovery{}, observed)
	if err != nil {
		t.Fatalf("verify monotonic stale-checkpoint tail: %v", err)
	}
	nextPins := clonePins(observed)
	nextPins.SessionID = "session-recovered"
	nextPins.RecoveryGeneration = 2
	nextPins.OwnerEpoch = 2
	nextPins.PreviousRecoveryLineageDigest = observed.RecoveryLineageDigest
	nextTranscript := emptyTranscript("conversation-2")
	nextTranscript.Ordinal = 2
	nextPins.ProviderTranscripts = append(nextPins.ProviderTranscripts, nextTranscript)
	nextPins.ProviderProjectionDigest = digestTranscriptPins(nextPins.ProviderTranscripts)
	nextPins.ProviderDisposition = DispositionAwaitingHostPrompt
	previousLineageDigest := nextPins.RecoveryLineageDigest
	nextPins.RecoveryLineageDigest = ""
	nextPins.RecoveryLineageDigest = ComputeRecoveryLineageDigest(staleDigest, observed, nextPins)
	if nextPins.RecoveryLineageDigest == previousLineageDigest {
		t.Fatal("clean restart reused the previous recovery lineage")
	}
	receipt, err := newOwnerLedger.ReserveCleanRestart(context.Background(), observation, nextPins)
	if err != nil {
		t.Fatalf("reserve durable clean restart: %v", err)
	}
	if _, err := newOwnerLedger.ReserveHostCall(context.Background(), testHostCallBinding("call-new-owner", 1)); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("provider admission before receipt verification = %v, want ErrRestartRequired", err)
	}
	if err := newOwnerLedger.VerifyCleanRestartReceipt(context.Background(), receipt, nextPins); err != nil {
		t.Fatalf("verify externally rooted clean-restart receipt: %v", err)
	}
	checkpoint, digest := newOwnerLedger.Snapshot()
	if digest != receipt.CheckpointDigest() || checkpoint.Counters.CleanRestarts != 1 ||
		checkpoint.Counters.PreEffectRetries != 1 || checkpoint.Pins.ProviderTranscripts[0].JournalHead != observed.ProviderTranscripts[0].JournalHead {
		t.Fatalf("restart did not retain verified tail and cumulative counters: cp=%+v", checkpoint)
	}
	if _, err := newOwnerLedger.ReservePreEffectRetry(context.Background()); err != nil {
		t.Fatalf("reserve second workflow-wide retry after clean restart: %v", err)
	}
	if _, err := newOwnerLedger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrExhausted) {
		t.Fatalf("third total pre-effect retry = %v, want ErrExhausted", err)
	}
	if err := newOwnerLedger.VerifyCleanRestartReceipt(context.Background(), receipt, nextPins); !errors.Is(err, ErrObservation) {
		t.Fatalf("stale clean-restart receipt verification = %v, want ErrObservation", err)
	}
}

func TestUnsettledProviderStateCanBeRecordedButCannotAdmitOrRetry(t *testing.T) {
	owner, root, ledger, checkpoint, _ := newWorkflowHarness(t, "workflow-pending", testLimits())
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	priorCheckpoint, _ := ledger.Snapshot()
	pending := clonePins(priorCheckpoint.Pins)
	pending.ProviderTranscripts[0].JournalCount = 1
	pending.ProviderTranscripts[0].JournalHead = testDigest("pending-provider-journal")
	pending.ProviderTranscripts[0].Disposition = DispositionAwaitingProviderExchange
	pending.ProviderDisposition = DispositionAwaitingProviderExchange
	pending.PendingCallDisposition = PendingCallInFlight
	pending.ProviderProjectionDigest = digestTranscriptPins(pending.ProviderTranscripts)
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, pending); err != nil {
		t.Fatalf("persist pending provider state for recovery inspection: %v", err)
	}
	if _, err := ledger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("retry admission with unresolved exchange = %v, want ErrPendingUnknown", err)
	}
	if _, err := ledger.ReserveHostCall(context.Background(), testHostCallBinding("call-pending", 10)); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("provider call admission with unresolved exchange = %v, want ErrPendingUnknown", err)
	}
	inspection, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: checkpoint.Pins.WorkflowID, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		RetainedPins: pending, Limits: checkpoint.Limits,
	})
	if err != nil {
		t.Fatalf("open unresolved provider state for inspection: %v", err)
	}
	if _, err := inspection.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("inspection ledger admitted unresolved provider state: %v", err)
	}
	root.setFence(checkpoint.Pins.WorkflowID, 2, 2)
	replacement, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: checkpoint.Pins.WorkflowID, OwnerEpoch: 2,
		RetainedPins: pending, Limits: checkpoint.Limits,
	})
	if err != nil {
		t.Fatalf("open drained owner for recovery inspection: %v", err)
	}
	pendingCheckpoint, pendingDigest := ledger.Snapshot()
	observed := clonePins(pending)
	observed.ProviderTranscripts[0].JournalCount = 2
	observed.ProviderTranscripts[0].JournalHead = testDigest("provider-call-retirement")
	observed.ProviderTranscripts[0].Disposition = DispositionRetired
	observed.ProviderDisposition = DispositionRetired
	observed.PendingCallDisposition = PendingCallRetired
	observed.AdmissionJournalCount++
	observed.AdmissionJournalHead = testDigest("explicit-host-retirement-record")
	observed.ProviderProjectionDigest = digestTranscriptPins(observed.ProviderTranscripts)
	unknown := clonePins(observed)
	unknown.ProviderTranscripts[0].Disposition = DispositionUnknown
	unknown.ProviderDisposition = DispositionUnknown
	unknown.PendingCallDisposition = PendingCallUnknown
	unknown.ProviderProjectionDigest = digestTranscriptPins(unknown.ProviderTranscripts)
	if _, err := replacement.ObserveRecovery(context.Background(), allowRecovery{}, unknown); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("UNKNOWN disposition was not paused: %v", err)
	}
	if _, err := replacement.ObserveRecovery(context.Background(), denyRecovery{}, observed); !errors.Is(err, ErrObservation) {
		t.Fatalf("retirement without independent evidence = %v, want ErrObservation", err)
	}
	observation, err := replacement.ObserveRecovery(context.Background(), explicitRetirementVerifier{}, observed)
	if err != nil {
		t.Fatalf("externally verified unleased-call retirement: %v", err)
	}
	nextPins := clonePins(observed)
	nextPins.SessionID = "session-after-retirement"
	nextPins.RecoveryGeneration = 2
	nextPins.OwnerEpoch = 2
	nextPins.OwnerIncarnationDigest = owner.OwnerIncarnationDigest()
	nextPins.PreviousRecoveryLineageDigest = observed.RecoveryLineageDigest
	nextPins.PendingCallDisposition = PendingCallNone
	nextTranscript := emptyTranscript("conversation-after-retirement")
	nextTranscript.Ordinal = 2
	nextPins.ProviderTranscripts = append(append([]TranscriptPin(nil), observed.ProviderTranscripts...), nextTranscript)
	nextPins.ProviderProjectionDigest = digestTranscriptPins(nextPins.ProviderTranscripts)
	nextPins.ProviderDisposition = DispositionAwaitingHostPrompt
	nextPins.RecoveryLineageDigest = ""
	nextPins.RecoveryLineageDigest = ComputeRecoveryLineageDigest(pendingDigest, observed, nextPins)
	receipt, err := replacement.ReserveCleanRestart(context.Background(), observation, nextPins)
	if err != nil {
		t.Fatalf("clean restart after externally verified retirement: %v", err)
	}
	if _, err := replacement.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("retry admission before clean-restart receipt verification = %v, want ErrRestartRequired", err)
	}
	if err := replacement.VerifyCleanRestartReceipt(context.Background(), receipt, nextPins); err != nil {
		t.Fatalf("verify receipt after explicit retirement: %v", err)
	}
	if receipt.Counters().HostCalls != pendingCheckpoint.Counters.HostCalls || receipt.Counters().Tokens != pendingCheckpoint.Counters.Tokens ||
		receipt.Counters().CleanRestarts != pendingCheckpoint.Counters.CleanRestarts+1 {
		t.Fatalf("retirement refunded/altered prior reservations: %+v", receipt.Counters())
	}
}

func TestZeroRecoveryAllowancesAreValidAndBudgetsStayWorkflowWide(t *testing.T) {
	limits := testLimits()
	limits.MaxPreEffectRetries = 0
	limits.MaxCleanRestarts = 0
	_, _, ledger, _, _ := newWorkflowHarness(t, "workflow-zero-allowance", limits)
	if _, err := ledger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrExhausted) {
		t.Fatalf("disabled retry allowance = %v, want ErrExhausted", err)
	}
	if err := validateLimits(limits, limits.Deadline.Add(-time.Minute)); err != nil {
		t.Fatalf("zero recovery allowances should be valid disabled budgets: %v", err)
	}
	usedLimits := testLimits()
	usedLimits.MaxHostCalls = 1
	usedLimits.MaxTokens = 10
	owner2, _, ledger2, checkpoint, _ := newWorkflowHarness(t, "workflow-call-caps", usedLimits)
	defer owner2.Close()
	if _, err := ledger2.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	if _, err := ledger2.ReserveHostCall(context.Background(), testHostCallBinding("call-restart", 10)); err != nil {
		t.Fatalf("reserve call at original token cap: %v", err)
	}
	settledCall, _ := ledger2.Snapshot()
	settledCall.Pins.HostCallReservation.Disposition = HostCallTerminal
	settledCall.Pins.HostCallReservation.OutcomeDigest = testDigest("verified provider response at token cap")
	if _, err := ledger2.AdvanceCheckpoint(context.Background(), allowProgress{}, settledCall.Pins); err != nil {
		t.Fatalf("settle the already-consumed provider call: %v", err)
	}
	if _, err := ledger2.ReserveEffect(context.Background(), testEffectBinding("operation-exhausted"), nil); !errors.Is(err, ErrExhausted) {
		t.Fatalf("effect after shared call cap = %v, want ErrExhausted", err)
	}
	changedLimits := checkpoint.Limits
	changedLimits.MaxHostCalls++
	if _, err := Open(context.Background(), owner2, OpenOptions{
		WorkflowID: checkpoint.Pins.WorkflowID, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		RetainedPins: checkpoint.Pins, Limits: changedLimits,
	}); !errors.Is(err, ErrRollback) {
		t.Fatalf("modified original call cap = %v, want ErrRollback", err)
	}

	// The workflow keeps a comfortable real-time deadline so setup under a
	// loaded or race-instrumented run can never expire it. Expiry is then
	// reached by moving the clocks, not by sleeping.
	owner3, root3, ledger3, deadlineCheckpoint, _ := newWorkflowHarness(t, "workflow-deadline", testLimits())
	defer owner3.Close()
	deadline := deadlineCheckpoint.Limits.Deadline
	if _, err := ledger3.ReservePreEffectRetry(context.Background()); err != nil {
		t.Fatalf("reservation before original deadline: %v", err)
	}
	// Root-verified expiry: the trusted root observes the deadline exactly.
	root3.setDeadlineClock(func() time.Time { return deadline })
	if _, err := ledger3.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrDeadline) {
		t.Fatalf("reservation after root-observed original deadline = %v, want ErrDeadline", err)
	}
	// Ledger-local wall-clock expiry: the root still reports time before the
	// deadline, while the ledger's own deadline is long past the real clock.
	pastDeadline := time.Now().UTC().Add(-time.Hour)
	root3.setDeadlineClock(func() time.Time { return pastDeadline.Add(-time.Second) })
	ledger3.mu.Lock()
	ledger3.checkpoint.Limits.Deadline = pastDeadline
	ledger3.mu.Unlock()
	if _, err := ledger3.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrDeadline) {
		t.Fatalf("reservation after ledger-observed original deadline = %v, want ErrDeadline", err)
	}
}

func TestSQLiteOwnerRequiresFenceDrainAndRejectsStaleOwner(t *testing.T) {
	owner, root, oldLedger, checkpoint, _ := newWorkflowHarness(t, "workflow-fencing", testLimits())
	root.setFence(checkpoint.Pins.WorkflowID, 2, 0)
	if _, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: checkpoint.Pins.WorkflowID, OwnerEpoch: 2,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
	}); !errors.Is(err, ErrOwnerFence) {
		t.Fatalf("new owner without drain = %v, want ErrOwnerFence", err)
	}
	root.setFence(checkpoint.Pins.WorkflowID, 2, 2)
	newLedger, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: checkpoint.Pins.WorkflowID, OwnerEpoch: 2,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
	})
	if err != nil {
		t.Fatalf("open drained newer owner for inspection: %v", err)
	}
	if _, err := newLedger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("new epoch retry before clean restart = %v, want ErrRestartRequired", err)
	}
	if _, err := oldLedger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrOwnerFence) {
		t.Fatalf("stale owner reservation = %v, want ErrOwnerFence", err)
	}
}

func TestSQLiteOwnerPoisonsUnanchoredTailAfterExternalCASFailure(t *testing.T) {
	owner, root, ledger, checkpoint, _ := newWorkflowHarness(t, "workflow-root-cas-failure", testLimits())
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	root.mu.Lock()
	root.failNextCAS = errors.New("injected external root outage")
	root.mu.Unlock()
	if _, err := ledger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrCAS) {
		t.Fatalf("external root CAS failure = %v, want ErrCAS", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close poisoned owner: %v", err)
	}
	owner, err := OpenExistingSQLiteOwner(owner.path, root)
	if err != nil {
		t.Fatalf("open local SQLite file to inspect tail: %v", err)
	}
	defer owner.Close()
	if _, _, _, err := owner.LoadCheckpoint(context.Background(), checkpoint.Pins.WorkflowID); !errors.Is(err, ErrRollback) {
		t.Fatalf("unanchored local tail admission = %v, want ErrRollback", err)
	}
}

func TestSQLiteOwnerConcurrentOwnersOnlyOneStaleCASWins(t *testing.T) {
	owner1, root, ledger1, checkpoint, _ := newWorkflowHarness(t, "workflow-concurrent", testLimits())
	defer owner1.Close()
	if _, err := ledger1.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	owner2, err := OpenExistingSQLiteOwner(owner1.path, root)
	if err != nil {
		t.Fatalf("open second owner connection: %v", err)
	}
	defer owner2.Close()
	stored, _, exists, err := owner2.LoadCheckpoint(context.Background(), checkpoint.Pins.WorkflowID)
	if err != nil || !exists {
		t.Fatalf("load before second owner open: exists=%v err=%v", exists, err)
	}
	ledger2, err := Open(context.Background(), owner2, OpenOptions{
		WorkflowID: checkpoint.Pins.WorkflowID, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		RetainedPins: stored.Pins, Limits: stored.Limits,
	})
	if err != nil {
		t.Fatalf("open competing ledger: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, ledger := range []*Ledger{ledger1, ledger2} {
		wait.Add(1)
		go func(ledger *Ledger) {
			defer wait.Done()
			<-start
			_, err := ledger.ReservePreEffectRetry(context.Background())
			errs <- err
		}(ledger)
	}
	close(start)
	wait.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrCAS) {
			conflicts++
		} else {
			t.Fatalf("unexpected competing-owner result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("competing owner CAS results: success=%d conflict=%d", successes, conflicts)
	}
	latest, _, exists, err := owner1.LoadCheckpoint(context.Background(), checkpoint.Pins.WorkflowID)
	if err != nil || !exists || latest.Counters.PreEffectRetries != 1 {
		t.Fatalf("latest concurrent checkpoint = %+v exists=%v err=%v", latest.Counters, exists, err)
	}
}

func newWorkflowHarness(t *testing.T, workflow string, limits Limits) (*SQLiteOwner, *testTrustedRoot, *Ledger, Checkpoint, Receipt) {
	t.Helper()
	root := newTestTrustedRoot()
	root.setFence(workflow, 1, 0)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	owner, err := CreateFreshSQLiteOwner(filepath.Join(directory, "workflow-owner.sqlite"), root)
	if err != nil {
		t.Fatalf("create fresh SQLite owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	pins := testPins(root.identity, owner.OwnerIncarnationDigest(), workflow, "session-initial", 1, 1)
	receipt, err := InitializeFresh(context.Background(), owner, pins, limits)
	if err != nil {
		t.Fatalf("initialize fresh workflow: %v", err)
	}
	checkpoint, digest, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists || digest != receipt.CheckpointDigest() {
		t.Fatalf("load fresh checkpoint: digest=%s exists=%v err=%v", digest, exists, err)
	}
	ledger, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: workflow, OwnerEpoch: 1, RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
	})
	if err != nil {
		t.Fatalf("open fresh ledger: %v", err)
	}
	return owner, root, ledger, checkpoint, receipt
}

func testLimits() Limits {
	return Limits{
		MaxPreEffectRetries: 2,
		MaxCleanRestarts:    2,
		MaxHostCalls:        8,
		MaxTokens:           512,
		Deadline:            time.Now().UTC().Add(time.Hour),
	}
}

func testPins(rootIdentity, incarnationIdentity, workflow, session string, generation, epoch uint64) Pins {
	return Pins{
		WorkflowID: workflow, SessionID: session, RecoveryGeneration: generation, OwnerEpoch: epoch,
		OwnerIncarnationDigest: incarnationIdentity,
		RootIdentityDigest:     rootIdentity, StoreIdentityDigest: testDigest("store identity"),
		AdmissionIdentityDigest: testDigest("admission identity"), CommandIdentityDigest: testDigest("command identity"),
		AdminIdentityDigest: testDigest("admin identity"), TaskDigest: testDigest("task"),
		PromptDigest: testDigest("original prompt"), PolicyDigest: testDigest("policy"),
		ProfileDigest: testDigest("profile"), ImageDigest: testDigest("image"), RuntimeDigest: testDigest("runtime"),
		D06ProvenanceDigest: testDigest("D06 g0 provenance"), PrivateGitIdentityDigest: testDigest("private Git v2 identity"),
		StoreBaselineGenerationID: "g0-baseline", StoreBaselineTreeDigest: testDigest("g0 baseline tree"),
		StoreTipGenerationID: "g0-baseline", StoreTipTreeDigest: testDigest("current Store tip"),
		ProviderProjectionDigest: TranscriptPinsDigest(nil), ProviderDisposition: DispositionNone,
		RecoveryLineageDigest: testDigest("initial recovery lineage"), PendingCallDisposition: PendingCallNone,
	}
}

func emptyTranscript(conversationID string) TranscriptPin {
	return TranscriptPin{
		Ordinal: 1, ConversationID: conversationID, JournalIdentityDigest: testDigest("journal:" + conversationID),
		Disposition: DispositionAwaitingHostPrompt,
	}
}

func settleInitialTranscript(t *testing.T, ledger *Ledger) (Checkpoint, string) {
	t.Helper()
	checkpoint, _ := ledger.Snapshot()
	pins := clonePins(checkpoint.Pins)
	if len(pins.ProviderTranscripts) != 1 {
		t.Fatalf("test expected exactly one registered transcript, got %d", len(pins.ProviderTranscripts))
	}
	pins.ProviderTranscripts[0].JournalCount = 1
	pins.ProviderTranscripts[0].JournalHead = testDigest("settled provider transcript")
	pins.ProviderTranscripts[0].Disposition = DispositionTerminal
	pins.ProviderDisposition = DispositionTerminal
	pins.ProviderProjectionDigest = digestTranscriptPins(pins.ProviderTranscripts)
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, pins); err != nil {
		t.Fatalf("settle initial transcript: %v", err)
	}
	checkpoint, digest := ledger.Snapshot()
	return checkpoint, digest
}

func testDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func testHostCallBinding(callID string, tokenUpperBound uint64) HostCallBinding {
	return HostCallBinding{CallID: callID, RequestDigest: testDigest("request:" + callID), TokenUpperBound: tokenUpperBound}
}

func testEffectBinding(operationID string) EffectBinding {
	return EffectBinding{OperationID: operationID, SpecDigest: testDigest("effect-spec:" + operationID)}
}
