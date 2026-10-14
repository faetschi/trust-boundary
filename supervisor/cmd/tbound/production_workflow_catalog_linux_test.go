//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/workflowrecovery"
)

func TestCatalogOwnerDrainRequiresCleanupReceiptOrLiveScope(t *testing.T) {
	root := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	bareDigest := strings.Repeat("a", 64)
	workflow := "workflow-test"
	entry := piServiceWorkflowEntry{
		AdmissionRecordHash: digest, WorkflowID: workflow, CheckpointBound: true,
	}
	incarnation := workflowrecovery.OwnerIncarnation{
		Digest: bareDigest, BootID: "different-boot", PID: 424242, StartTimeTicks: 1,
		PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: bareDigest,
	}
	owner := workflowrecovery.OwnerEpochIdentity{Epoch: 1, Incarnation: incarnation}
	catalog := &piServiceWorkflowCatalog{entries: map[string]piServiceWorkflowEntry{digest: entry}}
	verifier := catalogOwnerDrainVerifier{catalog: catalog, cgroupRoot: filepath.Clean(root),
		cgroupRootDigest: digest, containment: piruntime.D09ContainmentProfile,
		settlement: piruntime.D09SettlementProfile}

	err := verifier.VerifyOwnerCellsDrained(context.Background(), workflow, []workflowrecovery.OwnerEpochIdentity{owner})
	if err == nil {
		t.Fatalf("absent owner scope without a durable cleanup receipt was accepted: %v", err)
	}
	if _, exists := catalog.ownerScope(workflow, owner.Epoch); exists {
		t.Fatal("failed drain verification appended a synthetic cleanup receipt")
	}

	key := workflowOwnerEpochKey(workflow, owner.Epoch)
	catalog.ownerScopes = map[string]piServiceWorkflowOwnerScope{key: {
		AdmissionHash: digest, WorkflowID: workflow, OwnerEpoch: owner.Epoch,
		Incarnation: incarnation, ScopeName: workflowOwnerScopeName(workflow, owner.Epoch),
		Drained: true, Unexposed: true,
	}}
	if err := verifier.VerifyOwnerCellsDrained(context.Background(), workflow, []workflowrecovery.OwnerEpochIdentity{owner}); err != nil {
		t.Fatalf("exact prior cleanup receipt was not accepted: %v", err)
	}
}
