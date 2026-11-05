//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workflow"
)

// operatorPublication proves that the publication of the turn's workflow, when the project's publication journal
// names one, can no longer apply: the project still equals the admitted baseline, and the root ledger can resolve
// every unresolved lifecycle record. Publication recovery only observes, so at the baseline it ends a prepared
// transaction as failed and never applies anything. It returns the pending records that settle closes through the
// ledger's own recovery. The caller runs settle for a real resolution only, directly before the catalog append. A
// publication that applied or partly applied is refused by name, because the catalog cannot record a published
// outcome for an unknown turn.
func (e *productTurnEnv) operatorPublication(ctx context.Context, turn productsession.Turn, root *os.File, measured string) (closing []string, settle func() error, err error) {
	workflowID := turn.Link.WorkflowID
	named, _, err := e.workflowPublication(turn.ProjectID, workflowID)
	if err != nil {
		return nil, nil, errors.Join(errors.New("publication evidence is unreadable"), err)
	}
	if !named {
		return nil, nil, nil
	}
	switch measured {
	case turn.BaselineDigest:
	case turn.CandidateDigest:
		return nil, nil, errors.New("the publication was applied: the project equals the turn's candidate, and operator resolution cannot record a published outcome")
	default:
		return nil, nil, errors.New("the publication is not provably unapplied: the project equals neither the admitted baseline nor the turn's candidate")
	}
	projectDir := e.projectPublicationDir(turn.ProjectID)
	withLedger := func(use func(*audit.Journal, *workflow.RootLedger) error) (trace audit.Trace, err error) {
		journal, err := openExistingRecoveryJournal(filepath.Join(projectDir, "publication-audit.jsonl"))
		if err != nil {
			return trace, err
		}
		defer func() { err = errors.Join(err, journal.Close()) }()
		ledger, err := workflow.NewRootLedger(journal)
		if err == nil {
			err = use(journal, ledger)
		}
		if err == nil {
			trace, err = journal.Trace()
		}
		return trace, err
	}
	trace, err := withLedger(func(_ *audit.Journal, ledger *workflow.RootLedger) error {
		_, err := ledger.CheckRecoverBeforeAdmission(ctx, root)
		return err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("publication lifecycle: %w", err)
	}
	if closing = unresolvedPublicationEvents(trace, workflowID); len(closing) == 0 {
		return nil, nil, nil
	}
	return closing, func() error {
		stateRoot, err := os.Open(filepath.Join(projectDir, "publication-state"))
		if err != nil {
			return fmt.Errorf("close the publication lifecycle: %w", err)
		}
		defer stateRoot.Close()
		trace, err := withLedger(func(journal *audit.Journal, ledger *workflow.RootLedger) error {
			return ledger.RecoverBeforeAdmission(ctx, root, func(id string) (workflow.RecoveryRepository, error) {
				recovery, err := managedRecoveryWorkspace(journal, root, id)
				if err != nil {
					return nil, err
				}
				trace, err := journal.Trace()
				if err != nil {
					return nil, err
				}
				return publication.Acquire(publication.Options{WorkflowID: id, OwnerEpoch: nextPublicationOwnerEpoch(trace, id),
					StateRoot: stateRoot, Journal: journal, Workspace: recovery})
			})
		})
		if err == nil && len(unresolvedPublicationEvents(trace, workflowID)) != 0 {
			err = errors.New("a pending publication record of the workflow is registered against another live root")
		}
		if err != nil {
			return fmt.Errorf("close the publication lifecycle: %w", err)
		}
		return nil
	}, nil
}

// nextPublicationOwnerEpoch is the epoch after the highest the journal holds for the workflow. A recovery
// repository must outrank the owner that published, which holds epoch 1 in a product turn.
func nextPublicationOwnerEpoch(trace audit.Trace, workflowID string) uint64 {
	highest := uint64(0)
	for _, record := range trace.Records {
		var epoch struct {
			WorkflowID string `json:"workflow_id"`
			OwnerEpoch uint64 `json:"owner_epoch"`
		}
		if record.Event.Kind == "publication_owner_epoch" && json.Unmarshal(record.Event.Data, &epoch) == nil &&
			epoch.WorkflowID == workflowID {
			highest = max(highest, epoch.OwnerEpoch)
		}
	}
	return highest + 1
}

// unresolvedPublicationEvents lists the audit event IDs of the workflow's pending publication records that no
// resolved record closes, in journal order.
func unresolvedPublicationEvents(trace audit.Trace, workflowID string) []string {
	resolved := map[string]bool{}
	var pending []string
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "workflow_publication_resolved":
			resolved[strings.TrimSuffix(record.Event.ID, "/resolved")] = true
		case "workflow_publication_pending":
			var event struct {
				WorkflowID string `json:"workflow_id"`
			}
			if json.Unmarshal(record.Event.Data, &event) == nil && event.WorkflowID == workflowID {
				pending = append(pending, record.Event.ID)
			}
		}
	}
	return slices.DeleteFunc(pending, func(id string) bool { return resolved[id] })
}
