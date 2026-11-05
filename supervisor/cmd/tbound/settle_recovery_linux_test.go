//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/workflowrecovery"
)

// Replay in a fresh process: the child shares no memory with the writer, it only reads the file.
func TestCatalogTerminalReplaysInAFreshProcess(t *testing.T) {
	if path := os.Getenv("SETTLE_REPLAY_JOURNAL"); path != "" {
		journal, err := audit.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		catalog, err := loadPiServiceWorkflowCatalog(journal)
		if err != nil {
			t.Fatal(err)
		}
		entry, _ := catalog.admissionEntry(os.Getenv("SETTLE_REPLAY_HASH"))
		fmt.Printf("REPLAY terminal=%t abandoned=%t neverbound=%t bound=%t run=%q\n",
			entry.Terminal, entry.TerminalAbandoned, entry.TerminalNeverBound, entry.CheckpointBound, entry.TerminalRunID)
		_ = catalog.Close()
		return
	}
	for name, want := range map[string]struct {
		fixture func(*testing.T) boundCatalogFixture
		line    string
	}{
		"bound":       {boundCatalog, `REPLAY terminal=true abandoned=true neverbound=false bound=true run=""`},
		"never bound": {reservedCatalog, `REPLAY terminal=true abandoned=true neverbound=true bound=false run=""`},
	} {
		t.Run(name, func(t *testing.T) {
			f := want.fixture(t)
			if err := f.catalog.MarkAbandonedTerminal(f.admissionHash); err != nil {
				t.Fatal(err)
			}
			if err := f.catalog.Close(); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestCatalogTerminalReplaysInAFreshProcess$")
			command.Env = append(os.Environ(), "SETTLE_REPLAY_JOURNAL="+f.journalPath, "SETTLE_REPLAY_HASH="+f.admissionHash)
			output, err := command.CombinedOutput()
			if err != nil || !strings.Contains(string(output), want.line) {
				t.Fatalf("fresh process replay: %v\n%s", err, output)
			}
		})
	}
}

func TestNeverBoundAbandonedTerminalIsDistinctAndValidated(t *testing.T) {
	f := reservedCatalog(t)
	if err := f.catalog.MarkAbandonedTerminal(f.admissionHash); err != nil {
		t.Fatalf("a reserved workflow with no checkpoint was not closed: %v", err)
	}
	entry, _ := f.catalog.admissionEntry(f.admissionHash)
	if !entry.Terminal || !entry.TerminalNeverBound || entry.CheckpointBound || entry.LatestCheckpointDigest != "" {
		t.Fatalf("entry = %+v: a checkpoint was fabricated or the kind is not distinct", entry)
	}
	trace, err := f.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if err := trialPendingFrom(trace, trialTestTrace(), trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("the never-bound workflow still blocks the reset: %v", err)
	}
	// It stays closed: a later checkpoint bind or fresh authorization is refused.
	if err := f.catalog.MarkCheckpoint(f.admissionHash, entry.InitialCheckpointDigest, bareWorkflowDigest(digestBytes([]byte("x")))); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("a closed workflow advanced: %v", err)
	}
}

func TestNeverBoundAbandonedTerminalRefusesAnExposedOwner(t *testing.T) {
	f := reservedCatalog(t)
	f.catalog.ownerScopes = map[string]piServiceWorkflowOwnerScope{workflowOwnerEpochKey(catalogReplayWorkflowID, 1): {
		AdmissionHash: f.admissionHash, WorkflowID: catalogReplayWorkflowID, OwnerEpoch: 1}}
	if err := f.catalog.MarkAbandonedTerminal(f.admissionHash); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("a workflow with a bound owner scope was closed as never bound: %v", err)
	}
	entry, _ := f.catalog.admissionEntry(f.admissionHash)
	if entry.Terminal {
		t.Fatal("a refused close still marked the entry")
	}
}

func TestCatalogReplayRefusesAMalformedNeverBoundTerminal(t *testing.T) {
	for name, build := range map[string]struct {
		fixture func(*testing.T) boundCatalogFixture
		event   piServiceWorkflowEvent
	}{
		"never-bound outcome on a bound workflow": {boundCatalog, piServiceWorkflowEvent{Outcome: workflowTerminalAbandonedNeverBound}},
		"never-bound outcome naming a digest": {reservedCatalog, piServiceWorkflowEvent{Outcome: workflowTerminalAbandonedNeverBound,
			Digest: bareWorkflowDigest(digestBytes([]byte("fabricated")))}},
		"never-bound outcome with a link":              {reservedCatalog, piServiceWorkflowEvent{Outcome: workflowTerminalAbandonedNeverBound, RunID: "run-1"}},
		"abandoned-unknown on a never-bound workflow":  {reservedCatalog, piServiceWorkflowEvent{Outcome: workflowTerminalAbandonedUnknown}},
		"completed terminal on a never-bound workflow": {reservedCatalog, piServiceWorkflowEvent{}},
	} {
		t.Run(name, func(t *testing.T) {
			f := build.fixture(t)
			event := build.event
			event.SchemaVersion, event.AdmissionHash = workflowCatalogSchema, f.admissionHash
			appendCatalogTerminal(t, f, event)
			reopened, err := audit.Open(f.journalPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loadPiServiceWorkflowCatalog(reopened); !errors.Is(err, ErrPiWorkflowCatalog) {
				t.Fatalf("replayed: %v", err)
			}
		})
	}
}

func appendCatalogTerminal(t *testing.T, f boundCatalogFixture, event piServiceWorkflowEvent) {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.journal.Append(audit.Event{Kind: "pi_workflow_terminal",
		ID: workflowCatalogEventID("terminal", f.admissionHash), Data: data}); err != nil {
		t.Fatal(err)
	}
	_ = f.catalog.Close()
}

// The drain verifier's abandoned branch: an abandoned terminal needs no settlement link, any other
// terminal still does, and nothing is accepted for a workflow that never bound a checkpoint.
func TestOwnerDrainAbandonedTerminalBranch(t *testing.T) {
	const workflow = "workflow-abandoned-drain"
	incarnation := workflowrecovery.OwnerIncarnation{Digest: strings.Repeat("b", 64), BootID: "other-boot", PID: 424242,
		StartTimeTicks: 1, PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64)}
	hash := strings.Repeat("a", 64)
	for name, test := range map[string]struct {
		mutate  func(*piServiceWorkflowEntry)
		wantErr string
	}{
		"abandoned terminal is accepted":     {func(e *piServiceWorkflowEntry) { e.Terminal, e.TerminalAbandoned = true, true }, ""},
		"open entry is accepted":             {func(*piServiceWorkflowEntry) {}, ""},
		"terminal without link or abandoned": {func(e *piServiceWorkflowEntry) { e.Terminal = true }, "malformed terminal"},
		"abandoned flag without a terminal":  {func(e *piServiceWorkflowEntry) { e.TerminalAbandoned = true }, ""},
		"abandoned terminal, never bound": {func(e *piServiceWorkflowEntry) {
			e.Terminal, e.TerminalAbandoned, e.CheckpointBound = true, true, false
		}, "no checkpoint-bound catalog entry"},
		"completed terminal with a link": {func(e *piServiceWorkflowEntry) {
			e.Terminal, e.TerminalRunID, e.TerminalProofDigest = true, "run-1", "sha256:"+strings.Repeat("e", 64)
			e.TerminalProofSequence, e.TerminalTransactionID = 3, "tx-1"
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			catalog, verifier, owner := drainedOwnerScopeFixture(t, workflow, incarnation, "sha256:"+strings.Repeat("d", 64))
			entry := catalog.entries[hash]
			test.mutate(&entry)
			catalog.entries[hash] = entry
			_, err := verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), workflow, []workflowrecovery.OwnerEpochIdentity{owner})
			switch {
			case test.wantErr == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)):
				t.Fatalf("err = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// The service hosts its owners in-process, so the recorded owner process is the live service. A
// closed owner of this process is retired; nothing else counts as retired.
func TestOwnerDrainAcceptsAnOwnerThisProcessClosedAndNothingElse(t *testing.T) {
	const workflow = "workflow-live-retired"
	bootID, startTicks := currentProcessIdentityForTest(t)
	incarnation := workflowrecovery.OwnerIncarnation{Digest: strings.Repeat("b", 64), BootID: bootID, PID: os.Getpid(),
		StartTimeTicks: startTicks, PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64)}
	verify := func(catalog *piServiceWorkflowCatalog, verifier catalogOwnerDrainVerifier, owner workflowrecovery.OwnerEpochIdentity) error {
		_, err := verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), workflow, []workflowrecovery.OwnerEpochIdentity{owner})
		return err
	}
	fresh := func() (*piServiceWorkflowCatalog, catalogOwnerDrainVerifier, workflowrecovery.OwnerEpochIdentity) {
		return drainedOwnerScopeFixture(t, workflow, incarnation, "sha256:"+strings.Repeat("d", 64))
	}

	catalog, verifier, owner := fresh()
	if err := verify(catalog, verifier, owner); err == nil || !strings.Contains(err.Error(), "still live") {
		t.Fatalf("a live, unretired owner was accepted: %v", err)
	}
	// Retired marks for another epoch or another incarnation do not help.
	other := incarnation
	other.Digest = strings.Repeat("9", 64)
	catalog.markOwnerRetired(workflow, 2, incarnation)
	catalog.markOwnerRetired(workflow, 1, other)
	catalog.markOwnerRetired("workflow-other", 1, incarnation)
	if err := verify(catalog, verifier, owner); err == nil || !strings.Contains(err.Error(), "still live") {
		t.Fatalf("a mark for another epoch, incarnation or workflow retired the owner: %v", err)
	}
	catalog.markOwnerRetired(workflow, 1, incarnation)
	if err := verify(catalog, verifier, owner); err != nil {
		t.Fatalf("an owner this process closed was refused: %v", err)
	}
	// A mark never retires an owner of another process.
	foreign := incarnation
	foreign.PID = os.Getpid() + 1
	catalog.markOwnerRetired(workflow, 1, foreign)
	if catalog.ownerRetiredInThisProcess(workflow, 1, foreign) {
		t.Fatal("an owner of another process counted as retired")
	}
}

type recordedCloser struct{ err error }

func (c recordedCloser) Close() error { return c.err }

func TestClosingTheOwnerRetiresItOnlyWhenTheCloseSucceeded(t *testing.T) {
	incarnation := workflowrecovery.OwnerIncarnation{Digest: strings.Repeat("b", 64), BootID: "boot", PID: os.Getpid(), StartTimeTicks: 1}
	for name, closeErr := range map[string]error{"closed": nil, "close failed": errors.New("db busy")} {
		t.Run(name, func(t *testing.T) {
			catalog := &piServiceWorkflowCatalog{}
			admission := &productionWorkflowAdmission{Catalog: catalog, Entry: piServiceWorkflowEntry{WorkflowID: "workflow-close"},
				CandidateEpoch: 1, OwnerIncarnation: incarnation, OwnerCloser: recordedCloser{closeErr}}
			err := admission.CloseOwner()
			if (err == nil) != (closeErr == nil) {
				t.Fatalf("CloseOwner err = %v", err)
			}
			if got := catalog.ownerRetiredInThisProcess("workflow-close", 1, incarnation); got != (closeErr == nil) {
				t.Fatalf("retired = %v after close error %v", got, closeErr)
			}
		})
	}
}

// Crash gap: the abandon record is written and the catalog terminal is not. The next service start
// writes it, but only after the evidence bound in the abandon record still holds.
func TestAbandonedRequestIsClosedAtStartAfterACrashGap(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	marker := &markingReconciler{fakeAbandonReconciler: newFakeAbandonReconciler(), journal: h.audit, t: t, failure: ErrPiWorkflowCatalog}
	h.requests.setReconciler(marker)
	if err := h.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.requests.abandonUnknown(context.Background(), "request-0001", "outage"); err == nil {
		t.Fatal("the failed close was not reported")
	}
	marker.failure = nil
	// Evidence that no longer holds: no terminal.
	marker.fakeAbandonReconciler.failure["request-0001"] = errors.New("owner scope reappeared")
	before := marker.marks
	if err := h.requests.settleAbandonedPriors(context.Background()); err == nil || marker.marks != before {
		t.Fatalf("a terminal was written over evidence that no longer holds (marks %d, err %v)", marker.marks, err)
	}
	delete(marker.fakeAbandonReconciler.failure, "request-0001")
	if err := h.requests.settleAbandonedPriors(context.Background()); err != nil || marker.marks != before+1 {
		t.Fatalf("the missing terminal was not written at start (marks %d, err %v)", marker.marks, err)
	}
}

// An abandon from before the terminal existed (the F15 dr request) is closed by the next admission
// that re-checks it, the same way.
func TestLegacyAbandonedRequestIsClosedByTheNextAdmission(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	if err := h.admitUnknown(t, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.requests.abandonUnknown(context.Background(), "request-0001", "legacy abandon"); err != nil {
		t.Fatal(err)
	}
	marker := &markingReconciler{fakeAbandonReconciler: h.reconciler, journal: h.audit, t: t}
	h.requests.setReconciler(marker)
	if err := h.admit(t, 2, false); err != nil {
		t.Fatal(err)
	}
	if marker.marks != 1 {
		t.Fatalf("the legacy abandoned request was closed %d times, want 1", marker.marks)
	}
}

// settleCompleted with the real owner-drain verifier as the settlement check: the in-process owner
// is refused while open and accepted once the run closed it, which is what the serve loop does.
func TestSettleCompletedPassesTheRealOwnerDrainOnceTheRunClosedItsOwner(t *testing.T) {
	const workflow = "workflow-settle-real-drain"
	bootID, startTicks := currentProcessIdentityForTest(t)
	incarnation := workflowrecovery.OwnerIncarnation{Digest: strings.Repeat("b", 64), BootID: bootID, PID: os.Getpid(),
		StartTimeTicks: startTicks, PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64)}
	catalog, verifier, owner := drainedOwnerScopeFixture(t, workflow, incarnation, "sha256:"+strings.Repeat("d", 64))
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	h.requests.settlementVerifier = func(ctx context.Context, _ piServiceRequestState) error {
		_, err := verifier.VerifyOwnerCellsDrainedWithProof(ctx, workflow, []workflowrecovery.OwnerEpochIdentity{owner})
		return err
	}
	if err := h.admit(t, 1, true); err != nil {
		t.Fatal(err)
	}
	request, _ := incrementalRequest(1)
	if err := h.requests.settleCompleted(context.Background(), incrementalPeer().UID, request); err == nil {
		t.Fatal("settlement passed while the owner was still open")
	}
	admission := &productionWorkflowAdmission{Catalog: catalog, Entry: piServiceWorkflowEntry{WorkflowID: workflow},
		CandidateEpoch: 1, OwnerIncarnation: incarnation, OwnerCloser: recordedCloser{}}
	if err := admission.CloseOwner(); err != nil {
		t.Fatal(err)
	}
	if err := h.requests.settleCompleted(context.Background(), incrementalPeer().UID, request); err != nil {
		t.Fatalf("settlement still refused after the owner was closed: %v", err)
	}
}

// Fresh process: the request journal is reopened from its file, so nothing is cached. A completed
// request that never got its terminal is verified (and so closed) at start; one whose proof fails
// stays open and still blocks admission.
func TestCompletedRequestWithoutTerminalIsSettledAtStart(t *testing.T) {
	path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
	first := openIncrementalJournal(t, path)
	if err := first.admit(t, 1, true); err != nil {
		t.Fatal(err)
	}
	first.close(t) // crash before settleCompleted ran

	restarted := openIncrementalJournal(t, path)
	defer restarted.close(t)
	if err := restarted.requests.settleCompletedPriors(context.Background()); err != nil || restarted.total != 1 {
		t.Fatalf("start recovery: verifier calls %d, err %v", restarted.total, err)
	}
	if err := restarted.admit(t, 2, false); err != nil || restarted.total != 1 {
		t.Fatalf("the next admission re-verified or was refused (calls %d, err %v)", restarted.total, err)
	}
}

func TestCompletedRequestWhoseProofFailsStaysOpenAtStart(t *testing.T) {
	path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
	first := openIncrementalJournal(t, path)
	if err := first.admit(t, 1, true); err != nil {
		t.Fatal(err)
	}
	first.close(t)

	restarted := openIncrementalJournal(t, path)
	defer restarted.close(t)
	restarted.failID = "request-0001"
	if err := restarted.requests.settleCompletedPriors(context.Background()); err == nil {
		t.Fatal("a failed settlement was swallowed")
	}
	if err := restarted.admit(t, 2, false); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
		t.Fatalf("the unsettled prior no longer blocks: %v", err)
	}
}
