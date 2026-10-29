//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/workflowrecovery"
)

// combinedRecoveryBinder is a fresh process that calls bindProductionPiRecoverySession over the history the original
// process crashed over. The catalog layout and the admission checks compare strings and the retained checkpoint, so
// they pass with production-shaped inputs. The binder then stops at its managed-root trust check, the first step that
// reads a root-owned host file. Nothing before that check may reserve a generation or create a journal.
func combinedRecoveryBinder(t *testing.T, root, step string) {
	t.Helper()
	ctx := context.Background()
	p := combinedRecoveryPathsFor(root)
	boundary, err := workflowrecovery.NewLinuxHostBoundary(p.ownerStore, uint32(os.Geteuid()), combinedRecoveryDrain{root: root}, &verticalFreshWorkflowPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	protectedRoot, err := workflowrecovery.OpenExistingProtectedRoot(p.rootDB, p.ownerStore, boundary)
	if err != nil {
		t.Fatal(err)
	}
	defer protectedRoot.Close()
	owner, err := workflowrecovery.OpenExistingSQLiteOwner(p.ownerDB, protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	before, _, exists, err := owner.LoadCheckpoint(ctx, combinedRecoveryWorkflow)
	if err != nil || !exists {
		t.Fatalf("load the retained checkpoint: exists=%v err=%v", exists, err)
	}
	profile, template := combinedProductionProfile(t)
	auditRoot := filepath.Join(root, "binder-audit")
	if err := os.Mkdir(auditRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := piServiceWorkflowEntry{
		WorkflowID: combinedRecoveryWorkflow, ConversationID: combinedOriginalConversation,
		OwnerDBPath:         filepath.Join(piServiceOwnerStoreRoot, combinedRecoveryWorkflow+".sqlite"),
		StoreRootPath:       filepath.Join(productionStoreRootDefault, "store-"+combinedRecoveryWorkflow),
		AdminRootPath:       filepath.Join(productionAdminRootDefault, "admin-"+combinedRecoveryWorkflow),
		StoreJournalPath:    filepath.Join(productionAuditRootDefault, "store-"+combinedRecoveryWorkflow+".jsonl"),
		ProviderJournalPath: filepath.Join(productionAuditRootDefault, "provider-"+combinedRecoveryWorkflow+".jsonl"),
		CommandJournalPath:  filepath.Join(productionAuditRootDefault, "command-"+combinedRecoveryWorkflow+".jsonl"),
		TaskDigest:          before.Pins.TaskDigest, PromptDigest: before.Pins.PromptDigest,
		BashOutputPaths: []string{"task.txt"}, Limits: before.Limits,
		CheckpointBound: true, FreshAuthorizationUsed: true,
	}
	admission := &productionWorkflowAdmission{Mode: "resume", Entry: entry, Catalog: &piServiceWorkflowCatalog{},
		Owner: owner, Root: protectedRoot, Ledger: &workflowrecovery.Ledger{}, Checkpoint: before,
		CheckpointHead: before.ComputedCheckpointDigest, CandidateEpoch: before.Pins.OwnerEpoch + 1}
	config := productionServeConfig{BashOutputPaths: []string{"task.txt"}, workflowAdmission: admission}
	artifacts := productionOperatorArtifacts{AuditRoot: auditRoot,
		PublicationStateRoot: filepath.Join(root, "publication-state"), LiveRoot: filepath.Join(root, "live")}

	session, bindErr := bindProductionPiRecoverySession(ctx, config, profile, template, artifacts, admission)
	combinedRecoveryWriteJSON(t, filepath.Join(root, "result-"+step+".json"), combinedRecoveryResult{Step: step,
		PrepareError: fmt.Sprint(bindErr), OwnerEpoch: before.Pins.OwnerEpoch, RecoveryGeneration: before.Pins.RecoveryGeneration})
	if session != nil || bindErr == nil {
		t.Fatalf("binder returned a session without the host trust roots: session=%v err=%v", session != nil, bindErr)
	}
	if !errors.Is(bindErr, ErrProductionXattrVisibilityUnavailable) || !errors.Is(bindErr, piruntime.ErrD09ManagedLiveRootRefused) {
		t.Fatalf("binder refused before or after its managed-root check: %v", bindErr)
	}
	after, _, _, err := owner.LoadCheckpoint(ctx, combinedRecoveryWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pins.RecoveryGeneration != before.Pins.RecoveryGeneration || after.Pins.OwnerEpoch != before.Pins.OwnerEpoch {
		t.Fatalf("refused binder changed the checkpoint: generation %d to %d, owner epoch %d to %d",
			before.Pins.RecoveryGeneration, after.Pins.RecoveryGeneration, before.Pins.OwnerEpoch, after.Pins.OwnerEpoch)
	}
	entries, err := os.ReadDir(auditRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused binder created %d journal file(s) under the audit root", len(entries))
	}
}

// TestProductionRecoveryBinderFailsClosedBeforeAnyReservation runs the production binder in a fresh process over a
// crashed history. On a host without the protected managed-root trust files, the binder must refuse at that check and
// leave the checkpoint and the audit root untouched. A host that has the files would pass that check, so the test
// skips there instead of asserting the refusal.
func TestProductionRecoveryBinderFailsClosedBeforeAnyReservation(t *testing.T) {
	if _, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(context.Background()); err == nil {
		t.Skip("protected managed-root trust files are present, so the binder would pass its trust check on this host")
	}
	root := privateGovernedTestRoot(t)
	combinedRecoveryRunChild(t, root, "binder-pins", "original", "o", combinedRecoveryCrashExit)
	result := combinedRecoveryRunChild(t, root, "binder-pins", "binder", "b1", 0)
	if result.PrepareError == "" {
		t.Fatal("the binder child recorded no refusal")
	}
	t.Logf("binder stopped at its managed-root trust check: %s", result.PrepareError)
}

// TestGovernedRecoveryCombinedBindsOnceAfterPromotedWriteAndBash recovers, in a fresh process, a history with one
// promoted write and one production Bash call. The activated authority holds its prepared session from Activate and
// refuses a second bind. The resumed session promotes a new write, and publication passes every check before the
// managed live-root permit.
func TestGovernedRecoveryCombinedBindsOnceAfterPromotedWriteAndBash(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "binder-production-write-bash"
	combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	resumed := combinedRecoveryRunChild(t, root, scenario, "recover-write", "r1", 0)
	if resumed.PrepareError != "" || resumed.ActivateError != "" || !resumed.Activated || resumed.Publication != combinedProductionStop {
		t.Fatalf("recovery after a promoted write and a Bash call: prepare=%q activate=%q publication=%q",
			resumed.PrepareError, resumed.ActivateError, resumed.Publication)
	}
}
