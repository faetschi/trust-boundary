//go:build linux

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workspace"
)

func TestRootLedgerQuarantinesAcrossWorkflowsUntilNoPrepareIsReconciled(t *testing.T) {
	base := t.TempDir()
	rootPath, journalPath := filepath.Join(base, "live"), filepath.Join(base, "state", "journal.jsonl")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(journalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	ledger, _ := NewRootLedger(journal)
	oldReservation, err := ledger.Begin(root, "workflow-old", 1, "publication-old", testRootWorkspace())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Begin(root, "workflow-new", 1, "publication-new", testRootWorkspace()); !errors.Is(err, ErrRootActive) {
		t.Fatalf("different workflow passed unresolved root barrier: %v", err)
	}
	oldReservation.Release() // model the old in-process owner having stopped
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	ledger, _ = NewRootLedger(journal)
	opened := false
	if err := ledger.RecoverBeforeAdmission(context.Background(), root, func(string) (RecoveryRepository, error) {
		opened = true
		return nil, errors.New("no repository should be needed without a publication prepare")
	}); err != nil || opened {
		t.Fatalf("no-prepare recovery should resolve safely without opening a publisher: opened=%t err=%v", opened, err)
	}
	newReservation, err := ledger.Begin(root, "workflow-new", 1, "publication-new", testRootWorkspace())
	if err != nil {
		t.Fatalf("new workflow remained quarantined after safe no-prepare reconciliation: %v", err)
	}
	if err := ledger.Resolve(newReservation, root, "workflow-new", "publication-new", "", publication.StatusFailed); err != nil {
		t.Fatal(err)
	}
	newReservation.Release()
	if _, err := ledger.Begin(root, "workflow-old", 2, "publication-old", testRootWorkspace()); !errors.Is(err, ErrLifecycleReplay) {
		t.Fatalf("resolved publication identity was reused: %v", err)
	}
}

func testRootWorkspace() workspace.Options {
	return workspace.Options{Generation: "g0", MetadataPolicyDigest: "sha256:test",
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: "sha256:test-profile", Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}}
}

func TestAdmissionClosesAndWaitsForAcceptedEffects(t *testing.T) {
	admission := NewAdmission()
	if err := admission.Activate(); err != nil {
		t.Fatal(err)
	}
	release, err := admission.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- admission.CloseAndWait(context.Background()) }()
	for {
		admission.mu.Lock()
		closed := admission.closed
		admission.mu.Unlock()
		if closed {
			break
		}
		runtime.Gosched()
	}
	if _, err := admission.Acquire(context.Background()); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatalf("closed admission accepted a new proposal: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("close returned while accepted operation remained in flight: %v", err)
	default:
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRootLedgerRejectsUnboundRecoveryRepositories(t *testing.T) {
	tests := []string{"nil repository", "typed nil repository", "wrong journal", "wrong workflow", "stale owner epoch", "wrong workspace"}
	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			base := t.TempDir()
			rootDir := filepath.Join(base, "live")
			stateDir := filepath.Join(base, "publication-state")
			journalDir := filepath.Join(base, "audit")
			for _, dir := range []string{rootDir, stateDir, journalDir} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.Open(rootDir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			journal, err := audit.Open(filepath.Join(journalDir, "journal.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			ledger, err := NewRootLedger(journal)
			if err != nil {
				t.Fatal(err)
			}
			workspaceOptions := testRootWorkspace()
			reservation, err := ledger.Begin(root, "workflow-recovery", 1, "publication-recovery", workspaceOptions)
			if err != nil {
				t.Fatal(err)
			}
			prepare, _ := json.Marshal(struct {
				TransactionID string `json:"transaction_id"`
				WorkflowID    string `json:"workflow_id"`
				OwnerEpoch    uint64 `json:"owner_epoch"`
				PublicationID string `json:"publication_id"`
			}{"pub-0123456789abcdef0123456789abcdef", "workflow-recovery", 1, "publication-recovery"})
			if _, err := journal.Append(audit.Event{Kind: "publication_prepare", ID: "pub-0123456789abcdef0123456789abcdef", Data: prepare}); err != nil {
				t.Fatal(err)
			}
			reservation.Release() // model a stopped owner; recovery must now bind its repository
			stateRoot, err := os.Open(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer stateRoot.Close()

			var wrongJournal *audit.Journal
			if test == "wrong journal" {
				otherDir := filepath.Join(base, "other-audit")
				if err := os.Mkdir(otherDir, 0o700); err != nil {
					t.Fatal(err)
				}
				wrongJournal, err = audit.Open(filepath.Join(otherDir, "journal.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				defer wrongJournal.Close()
			}
			opened := false
			err = ledger.RecoverBeforeAdmission(context.Background(), root, func(string) (RecoveryRepository, error) {
				opened = true
				if test == "nil repository" {
					return nil, nil
				}
				if test == "typed nil repository" {
					var repository *publication.Repository
					return repository, nil
				}
				repoWorkflow, epoch, repoWorkspace, repoJournal := "workflow-recovery", uint64(2), workspaceOptions, journal
				switch test {
				case "wrong journal":
					repoJournal = wrongJournal
				case "wrong workflow":
					repoWorkflow = "workflow-other"
				case "stale owner epoch":
					epoch = 1
				case "wrong workspace":
					repoWorkspace.Limits.MaxObjects--
				}
				return publication.Acquire(publication.Options{WorkflowID: repoWorkflow, OwnerEpoch: epoch,
					StateRoot: stateRoot, Journal: repoJournal, Workspace: repoWorkspace})
			})
			if !opened || !errors.Is(err, ErrRootQuarantined) {
				t.Fatalf("unbound recovery repository was not rejected: opened=%t err=%v", opened, err)
			}
			trace, traceErr := journal.Trace()
			if traceErr != nil {
				t.Fatal(traceErr)
			}
			for _, frame := range trace.Records {
				if frame.Event.Kind == "workflow_publication_resolved" {
					t.Fatalf("mismatched recovery binding cleared the root quarantine: %+v", frame.Event)
				}
			}
		})
	}
}

// The dry run decides exactly as RecoverBeforeAdmission does from the journal alone, and writes nothing.
func TestRootLedgerCheckRecoverBeforeAdmissionResolvesNothing(t *testing.T) {
	for _, state := range []string{"no prepare", "prepare without terminal", "prepare with terminal"} {
		t.Run(state, func(t *testing.T) {
			base := t.TempDir()
			rootDir, journalDir := filepath.Join(base, "live"), filepath.Join(base, "audit")
			for _, dir := range []string{rootDir, journalDir} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.Open(rootDir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			journal, err := audit.Open(filepath.Join(journalDir, "journal.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			ledger, _ := NewRootLedger(journal)
			reservation, err := ledger.Begin(root, "workflow-check", 1, "publication-check", testRootWorkspace())
			if err != nil {
				t.Fatal(err)
			}
			const transaction = "pub-0123456789abcdef0123456789abcdef"
			if state != "no prepare" {
				prepare, _ := json.Marshal(map[string]any{"transaction_id": transaction, "workflow_id": "workflow-check", "owner_epoch": 1, "publication_id": "publication-check"})
				if _, err := journal.Append(audit.Event{Kind: "publication_prepare", ID: transaction, Data: prepare}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "prepare with terminal" {
				terminal, _ := json.Marshal(map[string]any{"transaction_id": transaction, "status": publication.StatusFailed})
				if _, err := journal.Append(audit.Event{Kind: "publication_aborted", ID: transaction, Data: terminal}); err != nil {
					t.Fatal(err)
				}
			}
			reservation.Release()
			before, err := journal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			recoveries, checkErr := ledger.CheckRecoverBeforeAdmission(context.Background(), root)
			after, err := journal.Trace()
			if err != nil || len(after.Records) != len(before.Records) {
				t.Fatalf("the dry run wrote records: %d -> %d, %v", len(before.Records), len(after.Records), err)
			}
			needsRecovery := state == "prepare without terminal"
			if checkErr != nil || needsRecovery != (recoveries == 1) {
				t.Fatalf("dry run of %q: recoveries=%d err=%v", state, recoveries, checkErr)
			}
			recoverErr := ledger.RecoverBeforeAdmission(context.Background(), root, func(string) (RecoveryRepository, error) {
				return nil, errors.New("the journal alone decides this state")
			})
			if needsRecovery != errors.Is(recoverErr, ErrRootQuarantined) || !needsRecovery && recoverErr != nil {
				t.Fatalf("recovery disagrees with its dry run: %v", recoverErr)
			}
		})
	}
}
