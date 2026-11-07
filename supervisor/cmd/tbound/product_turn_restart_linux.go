//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workflowrecovery"
)

// projectPublicationDir holds a project's publication lifecycle journal and repository state.
func (e *productTurnEnv) projectPublicationDir(projectID string) string {
	key := sha256.Sum256([]byte(projectID))
	return filepath.Join(e.StateRoot, "projects", hex.EncodeToString(key[:]))
}

// workflowPublication reads the project's publication lifecycle journal for one workflow: whether
// any pending, transaction or resolved record names it, and the transaction ID when the journal
// shows its transaction resolved SUCCEEDED. A project without a journal has no record.
func (e *productTurnEnv) workflowPublication(projectID, workflowID string) (bool, string, error) {
	path := filepath.Join(e.projectPublicationDir(projectID), "publication-audit.jsonl")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	journal, err := openExistingRecoveryJournal(path)
	if err != nil {
		return false, "", err
	}
	trace, err := journal.Trace()
	if err = errors.Join(err, journal.Close()); err != nil {
		return false, "", err
	}
	named := false
	var transaction, succeeded string
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "workflow_publication_pending", "workflow_publication_transaction", "workflow_publication_resolved":
		default:
			continue
		}
		var event struct {
			WorkflowID    string             `json:"workflow_id"`
			TransactionID string             `json:"transaction_id"`
			Status        publication.Status `json:"status"`
		}
		if err := json.Unmarshal(record.Event.Data, &event); err != nil {
			return false, "", fmt.Errorf("decode publication record %d: %w", record.Sequence, err)
		}
		if event.WorkflowID != workflowID {
			continue
		}
		named = true
		switch {
		case record.Event.Kind == "workflow_publication_transaction":
			transaction = event.TransactionID
		case record.Event.Kind == "workflow_publication_resolved" && event.Status == publication.StatusSucceeded &&
			event.TransactionID != "" && event.TransactionID == transaction:
			succeeded = transaction
		}
	}
	return named, succeeded, nil
}

// loadRetainedProductCheckpoint reads a turn's latest owner checkpoint read-only through its
// retained protected root. The root is opened existing-only with a drain verifier and fresh policy
// that refuse everything, so nothing can be created or resumed.
func loadRetainedProductCheckpoint(ctx context.Context, turnDir, workflowID string) (workflowrecovery.Checkpoint, error) {
	ownerStoreDir := filepath.Join(turnDir, "owner-store")
	boundary, err := workflowrecovery.NewLinuxHostBoundary(ownerStoreDir, uint32(os.Geteuid()), productTurnNoPriorOwner{},
		&productTurnFreshPolicy{})
	if err != nil {
		return workflowrecovery.Checkpoint{}, err
	}
	root, err := workflowrecovery.OpenExistingProtectedRoot(filepath.Join(turnDir, "recovery-root", "workflow-root.sqlite"),
		ownerStoreDir, boundary)
	if err != nil {
		return workflowrecovery.Checkpoint{}, err
	}
	defer root.Close()
	view, err := workflowrecovery.LoadSettledCheckpoint(ctx, filepath.Join(ownerStoreDir, "workflow.sqlite"), root, workflowID)
	if err != nil {
		return workflowrecovery.Checkpoint{}, err
	}
	if view.Digest != view.Checkpoint.ComputedCheckpointDigest || view.Checkpoint.Pins.WorkflowID != workflowID {
		return workflowrecovery.Checkpoint{}, errors.New("retained owner checkpoint does not belong to the turn's workflow")
	}
	return view.Checkpoint, nil
}
