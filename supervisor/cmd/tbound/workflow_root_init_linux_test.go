//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/workflowrecovery"
)

type testDrainVerifier struct{}

func (testDrainVerifier) VerifyOwnerCellsDrainedWithProof(context.Context, string, []workflowrecovery.OwnerEpochIdentity) ([]string, error) {
	return nil, nil
}

type testFreshPolicy struct{}

func (testFreshPolicy) AuthorizeFreshWorkflow(context.Context, string, uint64, string,
	workflowrecovery.OwnerIncarnation, time.Time, string, workflowrecovery.OwnerStoreIdentity) error {
	return nil
}

func workflowRootInitPaths(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	for _, parent := range []string{"private-admin", "sessions"} {
		if err := os.Mkdir(filepath.Join(base, parent), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(base, "private-admin", "workflow-recovery", "root.db"), filepath.Join(base, "sessions", "workflow-owners")
}

func TestWorkflowRootInitCreatesTheRootOnceAndTheServiceCanOpenIt(t *testing.T) {
	rootDB, ownerStore := workflowRootInitPaths(t)
	var out bytes.Buffer
	if err := initializeWorkflowRoot(rootDB, ownerStore, &out); err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	var result workflowRootInitResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || strings.Count(out.String(), "\n") != 1 ||
		result.Path != rootDB || result.OwnerStore != ownerStore || len(result.RootIdentity) != 64 {
		t.Fatalf("output %q is not one JSON line naming the root: %+v, %v", out.String(), result, err)
	}
	info, err := os.Stat(rootDB)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("root database = %v, %v; want a 0600 file", info, err)
	}

	boundary, err := workflowrecovery.NewLinuxHostBoundary(ownerStore, uint32(os.Geteuid()), testDrainVerifier{}, testFreshPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := workflowrecovery.OpenExistingProtectedRoot(rootDB, ownerStore, boundary)
	if err != nil {
		t.Fatalf("the service's OpenExistingProtectedRoot could not open the bootstrapped root: %v", err)
	}
	if root.TrustedRootIdentityDigest() != result.RootIdentity {
		t.Fatalf("service root identity %s differs from the printed %s", root.TrustedRootIdentityDigest(), result.RootIdentity)
	}
	_ = root.Close()

	before, err := os.ReadFile(rootDB)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := initializeWorkflowRoot(rootDB, ownerStore, &out); err == nil {
		t.Fatal("a second bootstrap was accepted")
	}
	after, err := os.ReadFile(rootDB)
	if err != nil || !bytes.Equal(before, after) || out.Len() != 0 {
		t.Fatalf("the refused second bootstrap changed the root or printed output (%v)", err)
	}
}

func TestWorkflowRootInitBoundaryAuthorizesNothing(t *testing.T) {
	boundary := workflowRootBootstrapBoundary{}
	ctx := context.Background()
	if boundary.VerifyOwnerStore(ctx, "w", workflowrecovery.OwnerStoreIdentity{}) == nil ||
		boundary.VerifyOwnerIncarnation(ctx, "w", workflowrecovery.OwnerIncarnation{}) == nil ||
		boundary.AuthorizeFreshWorkflow(ctx, "w", 1, "", workflowrecovery.OwnerIncarnation{}, time.Time{}, "", workflowrecovery.OwnerStoreIdentity{}) == nil {
		t.Fatal("the bootstrap boundary authorized something")
	}
	if _, err := boundary.VerifyOwnersDrainedWithEvidence(ctx, "w", nil); err == nil {
		t.Fatal("the bootstrap boundary vouched for a drain")
	}
}

func TestWorkflowRootInitTakesNoArgumentsAndRefusesRoot(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runWorkflowRootInit([]string{"--path", "/tmp/x"}, &out, &errOut); err == nil {
		t.Fatal("a free path argument was accepted")
	}
	if os.Geteuid() != 0 {
		return
	}
	if err := runWorkflowRootInit(nil, &out, &errOut); err == nil {
		t.Fatal("root was allowed to bootstrap the service-owned root")
	}
}
