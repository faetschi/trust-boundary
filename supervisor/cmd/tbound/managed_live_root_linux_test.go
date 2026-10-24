//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

// fakeManagedRootRegistrationVerifier is a unit-only boundary for authority
// state-machine tests. It proves neither a real signed registration nor the
// host's service-account isolation and is never installed by production code.
type fakeManagedRootRegistrationVerifier struct {
	registration piruntime.D09ManagedLiveRootRegistration
	root         *os.File
	identity     managedLiveRootDescriptorIdentity
	cgroup       piruntime.D09ManagedLiveRootCgroupIdentity
}

func (v *fakeManagedRootRegistrationVerifier) Registration() piruntime.D09ManagedLiveRootRegistration {
	return v.registration
}

func (v *fakeManagedRootRegistrationVerifier) DuplicateRoot() (*os.File, error) {
	if v == nil || v.root == nil {
		return nil, ErrManagedLiveRootUnavailable
	}
	fd, err := syscall.Dup(int(v.root.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "fake-registered-live-root"), nil
}

func (v *fakeManagedRootRegistrationVerifier) Revalidate(ctx context.Context, candidate *os.File) error {
	if v == nil || ctx == nil || candidate == nil {
		return ErrManagedLiveRootUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, err := measureManagedLiveRootDescriptor(candidate)
	if err != nil || identity != v.identity {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	return nil
}

func (v *fakeManagedRootRegistrationVerifier) CgroupIdentity() (piruntime.D09ManagedLiveRootCgroupIdentity, bool) {
	if v == nil || v.cgroup.Path == "" || v.cgroup.Inode == 0 {
		return piruntime.D09ManagedLiveRootCgroupIdentity{}, false
	}
	return v.cgroup, true
}

func (v *fakeManagedRootRegistrationVerifier) Close() error {
	if v == nil || v.root == nil {
		return nil
	}
	err := v.root.Close()
	v.root = nil
	return err
}

type managedRootD09CommandFixture struct{ leaseID string }

func (fixture managedRootD09CommandFixture) Profile() string {
	return piruntime.D09CommandRunnerProfile
}

func (fixture managedRootD09CommandFixture) Run(_ context.Context, view *sessionrepo.CommandView,
	_ sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	source, err := view.MountSource()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	path, err := os.Readlink(filepath.Join("/proc/self/fd", fmt.Sprint(source.Fd())))
	_ = source.Close()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if err := os.WriteFile(filepath.Join(path, "task.txt"), []byte("bash-tip\n"), 0o600); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	result := sessionrepo.CommandResult{ExitCode: 0, ExitObserved: true, Stdout: []byte("ok\n")}
	settlement := sessionrepo.CommandSettlement{LeaseID: fixture.leaseID, ViewID: view.ID(), ExitObserved: true,
		ExitCode: 0, ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true,
		EvidenceClass: piruntime.D09CommandRunnerProfile + ":test-only"}
	return result, settlement, nil
}

func TestManagedLiveRootAuthorityPermitCoversDurableRootLedgerTerminal(t *testing.T) {
	root := privateGovernedTestRoot(t)
	livePath := filepath.Join(root, "live")
	if err := os.Mkdir(livePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(livePath, "task.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	journal, err := audit.Open(filepath.Join(root, "publication-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	store, baseline, tip, bundle, metadataDigest := managedRootTestSession(t)
	defer store.Close()
	workspaceOptions := workspace.Options{Generation: baseline.ID(), MetadataPolicyDigest: metadataDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: managedRootTestXattr, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}}
	rootLedger, err := workflow.NewRootLedger(journal)
	if err != nil {
		t.Fatal(err)
	}
	const runID, workflowID, publicationID = "managed-test-run", "managed-test-workflow", "managed-test-publication"
	registrationDigest := testManagedRootDigest
	authority := newFakeManagedLiveRootAuthority()
	lease, err := authority.acquire(context.Background(), live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: registrationDigest}, journal,
		runID, workflowID, publicationID, 1)
	if err != nil {
		t.Fatalf("acquire test-only registered-root boundary: %v", err)
	}
	if duplicate, err := authority.acquire(context.Background(), live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: registrationDigest}, journal,
		"duplicate-run", "duplicate-workflow", "duplicate-publication", 2); err == nil {
		if duplicate != nil {
			_ = duplicate.close()
		}
		t.Fatal("same process acquired a second active writer epoch")
	}
	if _, err := authority.acquire(context.Background(), live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: registrationDigest}, journal,
		"stale-run", "stale-workflow", "stale-publication", 1); err == nil {
		t.Fatal("stale managed-root owner epoch was accepted")
	}
	if err := lease.completeAdmissionRecovery(context.Background()); err != nil {
		t.Fatalf("complete empty RootLedger admission recovery: %v", err)
	}
	if err := lease.revalidate(context.Background(), live); err != nil {
		t.Fatalf("managed-root revalidation before permit: %v", err)
	}
	if !validManagedTreeDigest(baseline.TreeDigest()) || !validManagedTreeDigest(tip.TreeDigest()) {
		t.Fatalf("test generations have invalid tree digests baseline=%q tip=%q", baseline.TreeDigest(), tip.TreeDigest())
	}
	request := workflow.EvidenceRequest{Bundle: bundle, Verified: bundle.ValidatedChain, LiveRoot: live,
		Workflow: workflowID, OwnerEpoch: 1}
	if err := lease.issuePublicationPermit(context.Background(), live, baseline, tip); err != nil {
		t.Fatalf("issue managed-root one-use permit for exact baseline/tip: %v", err)
	}
	if err := lease.verifyPermitForTransition(context.Background(), live); err != nil {
		t.Fatalf("verify issued one-use permit before Finalizer transition checks: %v", err)
	}
	if err := lease.authorizePublication(context.Background(), live, request, baseline); err != nil {
		t.Fatalf("authorize the verified Store chain under the managed-root permit: %v", err)
	}
	if err := lease.authorizePublication(context.Background(), live, request, baseline); err == nil {
		t.Fatal("managed-root publication permit was reusable")
	}
	reservation, err := rootLedger.Begin(live, workflowID, 1, publicationID, workspaceOptions)
	if err != nil {
		t.Fatalf("RootLedger.Begin under held managed-root permit: %v", err)
	}
	if _, err := rootLedger.Begin(live, workflowID, 2, "managed-test-terminal", workspaceOptions); err == nil {
		t.Fatal("RootLedger accepted another publication while the current reservation was active")
	}
	const transactionID = "managed-test-transaction"
	if err := rootLedger.RecordTransaction(reservation, live, workflowID, publicationID, transactionID); err != nil {
		t.Fatal(err)
	}
	if err := rootLedger.Resolve(reservation, live, workflowID, publicationID, transactionID, publication.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	reservation.Release()
	if err := lease.recordDurableTerminal(context.Background(), journal, publication.StatusSucceeded, transactionID); err != nil {
		t.Fatalf("managed-root permit rejected matching durable RootLedger terminal: %v", err)
	}
	if err := lease.close(); err != nil {
		t.Fatalf("release authority after durable terminal cleanup: %v", err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if lenRuntimeEvents(trace, "managed_live_root_run_admitted") != 1 || lenRuntimeEvents(trace, "managed_live_root_run_released") != 1 {
		t.Fatalf("managed-root owner admission/release was not durably recorded: %+v", trace.Records)
	}
}

func TestManagedLiveRootAuthorityRejectsWrongDescriptorRegistrationAndStaleRuntimeIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*piruntime.D09ManagedLiveRootRegistration){
		"wrong root path":   func(registration *piruntime.D09ManagedLiveRootRegistration) { registration.RootPath += "/other" },
		"wrong root device": func(registration *piruntime.D09ManagedLiveRootRegistration) { registration.RootDevice++ },
		"wrong root uid":    func(registration *piruntime.D09ManagedLiveRootRegistration) { registration.RootUID++ },
		"wrong root gid":    func(registration *piruntime.D09ManagedLiveRootRegistration) { registration.RootGID++ },
		"wrong root mode":   func(registration *piruntime.D09ManagedLiveRootRegistration) { registration.RootMode = 0o755 },
		"wrong registration": func(registration *piruntime.D09ManagedLiveRootRegistration) {
			registration.Digest = "sha256:" + stringsOf('f', 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := privateGovernedTestRoot(t)
			livePath := filepath.Join(root, "live")
			if err := os.Mkdir(livePath, 0o700); err != nil {
				t.Fatal(err)
			}
			candidate, err := os.Open(livePath)
			if err != nil {
				t.Fatal(err)
			}
			defer candidate.Close()
			journal, err := audit.Open(filepath.Join(root, "publication.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			authority := newFakeManagedLiveRootAuthorityWithMutation(mutate)
			_, err = authority.acquire(context.Background(), candidate,
				piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest}, journal,
				"mismatch-run", "mismatch-workflow", "mismatch-publication", 1)
			if !errors.Is(err, ErrManagedLiveRootUnavailable) {
				t.Fatalf("mismatched registered descriptor passed admission: %v", err)
			}
			trace, traceErr := journal.Trace()
			if traceErr != nil || len(trace.Records) != 0 {
				t.Fatalf("rejected descriptor wrote run authority: records=%+v err=%v", trace.Records, traceErr)
			}
		})
	}

	t.Run("per-invocation cgroup identity changed", func(t *testing.T) {
		root := privateGovernedTestRoot(t)
		livePath := filepath.Join(root, "live")
		if err := os.Mkdir(livePath, 0o700); err != nil {
			t.Fatal(err)
		}
		candidate, err := os.Open(livePath)
		if err != nil {
			t.Fatal(err)
		}
		defer candidate.Close()
		journal, err := audit.Open(filepath.Join(root, "publication.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		authority := newFakeManagedLiveRootAuthority()
		lease, err := authority.acquire(context.Background(), candidate,
			piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest}, journal,
			"cgroup-run", "cgroup-workflow", "cgroup-publication", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.completeAdmissionRecovery(context.Background()); err != nil {
			t.Fatal(err)
		}
		fake := lease.verified.(*fakeManagedRootRegistrationVerifier)
		original := fake.cgroup
		fake.cgroup.Inode++
		if err := lease.revalidate(context.Background(), candidate); !errors.Is(err, ErrManagedLiveRootUnavailable) {
			t.Fatalf("changed per-invocation service cgroup passed revalidation: %v", err)
		}
		fake.cgroup = original
		if err := lease.abortBeforeTask(productionLifecycleSnapshot{}, nil); err != nil {
			t.Fatal(err)
		}
		if err := lease.close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestManagedLiveRootUnknownCleanupDoesNotReleasePermitOrOwner(t *testing.T) {
	root := privateGovernedTestRoot(t)
	livePath := filepath.Join(root, "live")
	if err := os.Mkdir(livePath, 0o700); err != nil {
		t.Fatal(err)
	}
	live, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	journal, err := audit.Open(filepath.Join(root, "publication.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	store, baseline, tip, bundle, _ := managedRootTestSession(t)
	defer store.Close()
	authority := newFakeManagedLiveRootAuthority()
	lease, err := authority.acquire(context.Background(), live,
		piruntime.D09CellTemplate{ManagedLiveRootRegistrationDigest: testManagedRootDigest}, journal,
		"unknown-run", "unknown-workflow", "unknown-publication", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.completeAdmissionRecovery(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.issuePublicationPermit(context.Background(), live, baseline, tip); err != nil {
		t.Fatal(err)
	}
	if err := lease.authorizePublication(context.Background(), live, workflow.EvidenceRequest{
		Bundle: bundle, Verified: bundle.ValidatedChain, Workflow: "unknown-workflow", OwnerEpoch: 1,
	}, baseline); err != nil {
		t.Fatal(err)
	}
	if err := lease.close(); !errors.Is(err, ErrManagedLiveRootRetained) {
		t.Fatalf("UNKNOWN applying permit cleanup was not retained: %v", err)
	}
	authority.mu.Lock()
	active := authority.active[lease.key]
	authority.mu.Unlock()
	if active != lease {
		t.Fatal("managed-root owner was released while publication status was UNKNOWN")
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if lenRuntimeEvents(trace, "managed_live_root_run_released") != 0 {
		t.Fatal("UNKNOWN managed-root run wrote a release record")
	}
	// This test deliberately models process death at the boundary; close the test
	// descriptors without resolving the owner so the next epoch must reconcile it.
	_ = lease.root.Close()
	_ = lease.verified.Close()
}

func TestProductionD09OperationBundleBindsActualBashAuditAndTreeDelta(t *testing.T) {
	store, _, _, bundle, _ := managedRootTestSession(t)
	defer store.Close()
	if len(bundle.Operations) == 0 || len(bundle.ApprovedDeltaLedger) == 0 {
		t.Fatal("test Store did not produce the Bash operation")
	}
	operation := bundle.Operations[len(bundle.Operations)-1]
	transition := bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1]
	if operation.Tool != "bash" || transition.Tool != "bash" || operation.Operation.Kind != delta.OperationLease {
		t.Fatalf("fixture terminal operation is not Bash lease evidence: op=%+v transition=%+v", operation, transition)
	}
	spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}, DeclaredOutputPaths: []string{"task.txt"}}
	result := sessionrepo.CommandResult{ExitCode: 0, ExitObserved: true, Stdout: []byte("ok\n")}
	settlement := sessionrepo.CommandSettlement{LeaseID: operation.Operation.LeaseID, ViewID: operation.ViewID,
		ExitObserved: true, ExitCode: 0, ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true,
		EvidenceClass: piruntime.D09CommandRunnerProfile + ":test-only"}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	settlementBytes, err := json.Marshal(settlement)
	if err != nil {
		t.Fatal(err)
	}
	receipt := piruntime.D09CommandReceipt{LeaseID: operation.Operation.LeaseID, ViewID: operation.ViewID,
		CommandSpecDigest: digestBytes(specBytes), ResultDigest: digestBytes(resultBytes),
		SettlementDigest: digestBytes(settlementBytes), ExitCode: result.ExitCode, Settlement: settlement}
	storeDigest, err := sessionrepo.CommandArgumentsDigest(spec)
	if err != nil || storeDigest != operation.ArgumentDigest || receipt.CommandSpecDigest == storeDigest {
		t.Fatalf("execution receipt and scope-inclusive Store digest are not distinct: receipt=%q Store=%q operation=%q err=%v", receipt.CommandSpecDigest, storeDigest, operation.ArgumentDigest, err)
	}
	if err := validateProductionD09OperationBundle(bundle, transition, operation, receipt); err != nil {
		t.Fatalf("actual durable command effect/OperationEvidence/transition bundle was refused: %v", err)
	}
	for name, adversary := range map[string]func(*sessionrepo.EvidenceBundle, *delta.Transition,
		*sessionrepo.OperationEvidence, *piruntime.D09CommandReceipt){
		"wrong lease": func(_ *sessionrepo.EvidenceBundle, _ *delta.Transition, operation *sessionrepo.OperationEvidence,
			receipt *piruntime.D09CommandReceipt) {
			operation.Operation.LeaseID = "other-lease"
			receipt.LeaseID = "other-lease"
		},
		"wrong view": func(_ *sessionrepo.EvidenceBundle, _ *delta.Transition, operation *sessionrepo.OperationEvidence,
			receipt *piruntime.D09CommandReceipt) {
			operation.ViewID = "other-view"
			receipt.ViewID = "other-view"
		},
		"wrong spec": func(_ *sessionrepo.EvidenceBundle, _ *delta.Transition, operation *sessionrepo.OperationEvidence,
			receipt *piruntime.D09CommandReceipt) {
			operation.ArgumentDigest = "sha256:" + stringsOf('e', 64)
			receipt.CommandSpecDigest = operation.ArgumentDigest
		},
		"wrong Store scope digest": func(_ *sessionrepo.EvidenceBundle, transition *delta.Transition, operation *sessionrepo.OperationEvidence, _ *piruntime.D09CommandReceipt) {
			otherSpec := cloneProductionD09CommandSpec(spec)
			otherSpec.DeclaredOutputPaths = []string{"other.txt"}
			digest, err := sessionrepo.CommandArgumentsDigest(otherSpec)
			if err != nil {
				t.Fatal(err)
			}
			operation.ArgumentDigest, transition.ArgumentDigest = digest, digest
		},
		"receipt uses Store digest": func(_ *sessionrepo.EvidenceBundle, _ *delta.Transition, operation *sessionrepo.OperationEvidence, receipt *piruntime.D09CommandReceipt) {
			receipt.CommandSpecDigest = operation.ArgumentDigest
		},
		"wrong admission reference": func(_ *sessionrepo.EvidenceBundle, _ *delta.Transition, operation *sessionrepo.OperationEvidence, _ *piruntime.D09CommandReceipt) {
			operation.Admission.Sequence++
		},
		"wrong result": func(_ *sessionrepo.EvidenceBundle, _ *delta.Transition, operation *sessionrepo.OperationEvidence,
			receipt *piruntime.D09CommandReceipt) {
			operation.ResultDigest = "sha256:" + stringsOf('e', 64)
			receipt.ResultDigest = operation.ResultDigest
		},
		"wrong settlement": func(_ *sessionrepo.EvidenceBundle, _ *delta.Transition, _ *sessionrepo.OperationEvidence,
			receipt *piruntime.D09CommandReceipt) {
			receipt.SettlementDigest = "sha256:" + stringsOf('e', 64)
		},
		"wrong input tree": func(_ *sessionrepo.EvidenceBundle, transition *delta.Transition,
			operation *sessionrepo.OperationEvidence, _ *piruntime.D09CommandReceipt) {
			operation.InputTreeDigest = "wrong-tree"
			transition.InputTreeDigest = "wrong-tree"
		},
		"wrong output delta": func(_ *sessionrepo.EvidenceBundle, transition *delta.Transition,
			_ *sessionrepo.OperationEvidence, _ *piruntime.D09CommandReceipt) {
			transition.Changes[0].Path = "other.txt"
		},
		"wrong output generation": func(_ *sessionrepo.EvidenceBundle, transition *delta.Transition,
			operation *sessionrepo.OperationEvidence, _ *piruntime.D09CommandReceipt) {
			operation.OutputGeneration = "g99"
			transition.OutputGeneration = "g99"
		},
		"wrong output tree": func(_ *sessionrepo.EvidenceBundle, transition *delta.Transition,
			operation *sessionrepo.OperationEvidence, _ *piruntime.D09CommandReceipt) {
			operation.OutputTreeDigest = "wrong-output-tree"
			transition.OutputTreeDigest = "wrong-output-tree"
		},
		"wrong audit sequence": func(_ *sessionrepo.EvidenceBundle, _ *delta.Transition,
			operation *sessionrepo.OperationEvidence, _ *piruntime.D09CommandReceipt) {
			operation.AuditSequence++
		},
		"wrong effect": func(_ *sessionrepo.EvidenceBundle, _ *delta.Transition,
			operation *sessionrepo.OperationEvidence, _ *piruntime.D09CommandReceipt) {
			operation.EffectID = "other-effect"
		},
		"wrong journal": func(bundle *sessionrepo.EvidenceBundle, _ *delta.Transition,
			_ *sessionrepo.OperationEvidence, _ *piruntime.D09CommandReceipt) {
			bundle.AuditJournal = append(append([]byte(nil), bundle.AuditJournal...), []byte("{}\n")...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidateBundle := bundle
			candidateBundle.AuditJournal = append([]byte(nil), bundle.AuditJournal...)
			candidateOperation := operation
			if operation.Admission != nil {
				reference := *operation.Admission
				candidateOperation.Admission = &reference
			}
			if operation.ExitCode != nil {
				candidateOperation.ExitCode = intPointer(*operation.ExitCode)
			}
			candidateTransition := transition
			candidateTransition.Changes = append([]delta.ObjectChange(nil), transition.Changes...)
			candidateReceipt := receipt
			adversary(&candidateBundle, &candidateTransition, &candidateOperation, &candidateReceipt)
			if err := validateProductionD09OperationBundle(candidateBundle, candidateTransition, candidateOperation, candidateReceipt); err == nil {
				t.Fatal("altered Store effect/OperationEvidence/tree delta was accepted")
			}
		})
	}
	if bytes.Equal(bundle.AuditJournal, nil) {
		t.Fatal("fixture Store audit journal unexpectedly empty")
	}
}

func newFakeManagedLiveRootAuthority() *managedLiveRootAuthority {
	return newFakeManagedLiveRootAuthorityWithMutation(nil)
}

func newFakeManagedLiveRootAuthorityWithMutation(mutate func(*piruntime.D09ManagedLiveRootRegistration)) *managedLiveRootAuthority {
	authority := &managedLiveRootAuthority{active: make(map[managedLiveRootKey]*managedLiveRootLease)}
	authority.verifyRegistration = func(_ context.Context, candidate *os.File) (managedRootRegistrationVerifier, error) {
		identity, err := measureManagedLiveRootDescriptor(candidate)
		if err != nil {
			return nil, err
		}
		fd, err := syscall.Dup(int(candidate.Fd()))
		if err != nil {
			return nil, err
		}
		registration := piruntime.D09ManagedLiveRootRegistration{Digest: testManagedRootDigest,
			RootPath: identity.Path, RootDevice: identity.Device, RootInode: identity.Inode,
			RootMountID: identity.MountID, RootFilesystemMagic: identity.Filesystem,
			RootUID: int(identity.UID), RootGID: int(identity.GID), RootMode: identity.Mode,
			ServiceUID: int(os.Geteuid()), ServiceGID: int(os.Getegid()), ServiceCgroupPath: "/test/service"}
		if mutate != nil {
			mutate(&registration)
		}
		return &fakeManagedRootRegistrationVerifier{registration: registration,
			root: os.NewFile(uintptr(fd), "fake-managed-root-verifier"), identity: identity,
			cgroup: piruntime.D09ManagedLiveRootCgroupIdentity{Path: "/test/service", Device: 1, Inode: 2, MountID: 3}}, nil
	}
	return authority
}

func managedRootTestSession(t *testing.T) (*sessionrepo.Store, *sessionrepo.Generation, *sessionrepo.Generation,
	sessionrepo.EvidenceBundle, string) {
	t.Helper()
	root := privateGovernedTestRoot(t)
	storePath, sourcePath := filepath.Join(root, "store"), filepath.Join(root, "source")
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storeJournal, err := audit.Open(filepath.Join(root, "store-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), sessionrepo.Options{
		PolicyDigest: managedRootTestPolicy, MetadataPolicyDigest: managedRootTestMetadata,
		Limits:          workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: managedRootTestXattr, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted},
		Journal:         storeJournal, AuthorizeOperation: func(sessionrepo.OperationRequest) error { return nil },
		VerifyDecision:   func(delta.PolicyDecision, delta.TransitionBinding) error { return nil },
		VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error { return nil },
	})
	if err != nil {
		_ = storeJournal.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeJournal.Close() })
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true}) // test fixture only
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	operation := delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: "managed-test-proposal",
		CallIssuer: "managed-test-call-issuer", CallID: "managed-test-call"}
	decision := delta.PolicyDecision{ID: "managed-test-decision", Outcome: delta.PolicyAllow,
		PolicyDigest: managedRootTestPolicy, MetadataPolicyDigest: managedRootTestMetadata}
	mutation, err := store.Write(context.Background(), baseline, operation, decision, "task.txt", []byte("tip\n"))
	if err != nil {
		t.Fatal(err)
	}
	leaseID := "managed-test-bash-lease"
	bashOperation := delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: leaseID}
	bashDecision := delta.PolicyDecision{ID: "managed-test-bash-decision", Outcome: delta.PolicyAllow,
		PolicyDigest: managedRootTestPolicy, MetadataPolicyDigest: managedRootTestMetadata}
	bashSpec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}, DeclaredOutputPaths: []string{"task.txt"}}
	bashMutation, err := store.RunBash(context.Background(), mutation.Generation, bashOperation, bashDecision,
		bashSpec, managedRootD09CommandFixture{leaseID: leaseID})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	return store, baseline, bashMutation.Generation, bundle, managedRootTestMetadata
}

const (
	testManagedRootDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	managedRootTestPolicy   = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	managedRootTestMetadata = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	managedRootTestXattr    = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func stringsOf(char byte, count int) string { return strings.Repeat(string(char), count) }
