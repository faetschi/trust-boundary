//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/drainproof"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workflowrecovery"
)

func TestCatalogOwnerDrainRequiresCleanupReceiptOrLiveScope(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	cgroupRootDigest := "sha256:" + strings.Repeat("a", 64)
	bareDigest := strings.Repeat("b", 64)
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
		cgroupRootDigest: cgroupRootDigest, containment: piruntime.D09ContainmentProfile,
		settlement: piruntime.D09SettlementProfile}

	_, err := verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), workflow, []workflowrecovery.OwnerEpochIdentity{owner})
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
		// A bound cleanup receipt names the scope identity it cleaned up. An exact
		// receipt must carry that identity, not only the epoch and incarnation.
		// The never-exposed receipt has its own tests below, driven by the real writer.
		ScopeIdentityDigest: "sha256:" + strings.Repeat("d", 64),
		Drained:             true,
	}}
	if _, err := verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), workflow, []workflowrecovery.OwnerEpochIdentity{owner}); err != nil {
		t.Fatalf("exact prior cleanup receipt was not accepted: %v", err)
	}
}

// currentProcessIdentityForTest returns this process's real boot id and start
// ticks, so a test can present a genuinely live owner to the drain verifier.
func currentProcessIdentityForTest(t *testing.T) (string, uint64) {
	t.Helper()
	bootData, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	statData, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	closeParen := strings.LastIndexByte(string(statData), ')')
	fields := strings.Fields(string(statData[closeParen+1:]))
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		t.Fatalf("parse own start ticks: %v", err)
	}
	return strings.TrimSpace(string(bootData)), start
}

// drainedOwnerScopeFixture is a catalog whose owner epoch 1 is recorded as
// drained, with a cleanup receipt identity supplied by the caller.
func drainedOwnerScopeFixture(t *testing.T, workflow string, incarnation workflowrecovery.OwnerIncarnation,
	scopeIdentity string) (*piServiceWorkflowCatalog, catalogOwnerDrainVerifier, workflowrecovery.OwnerEpochIdentity) {
	t.Helper()
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	entry := piServiceWorkflowEntry{AdmissionRecordHash: digest, WorkflowID: workflow, CheckpointBound: true}
	owner := workflowrecovery.OwnerEpochIdentity{Epoch: 1, Incarnation: incarnation}
	key := workflowOwnerEpochKey(workflow, owner.Epoch)
	catalog := &piServiceWorkflowCatalog{entries: map[string]piServiceWorkflowEntry{digest: entry},
		ownerScopes: map[string]piServiceWorkflowOwnerScope{key: {
			AdmissionHash: digest, WorkflowID: workflow, OwnerEpoch: owner.Epoch,
			Incarnation: incarnation, ScopeName: workflowOwnerScopeName(workflow, owner.Epoch),
			ScopeIdentityDigest: scopeIdentity, Drained: true,
		}}}
	verifier := catalogOwnerDrainVerifier{catalog: catalog, cgroupRoot: filepath.Clean(root),
		cgroupRootDigest: "sha256:" + strings.Repeat("a", 64), containment: piruntime.D09ContainmentProfile,
		settlement: piruntime.D09SettlementProfile}
	return catalog, verifier, owner
}

// A drained cleanup receipt describes a scope. It says nothing about whether
// the recorded owner process is still running, so a live owner is refused.
func TestCatalogOwnerDrainRefusesLiveOwnerEvenWithDrainedReceipt(t *testing.T) {
	bootID, startTicks := currentProcessIdentityForTest(t)
	incarnation := workflowrecovery.OwnerIncarnation{
		Digest: strings.Repeat("b", 64), BootID: bootID, PID: os.Getpid(), StartTimeTicks: startTicks,
		PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64),
	}
	_, verifier, owner := drainedOwnerScopeFixture(t, "workflow-live-drained", incarnation, "sha256:"+strings.Repeat("d", 64))
	_, err := verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), "workflow-live-drained",
		[]workflowrecovery.OwnerEpochIdentity{owner})
	if err == nil {
		t.Fatal("a drained cleanup receipt was accepted for a live recorded owner process")
	}
}

// A receipt that does not name the scope identity it cleaned up is not a
// cleanup receipt for this scope, so the absent scope is refused.
func TestCatalogOwnerDrainRefusesDrainedReceiptWithoutScopeIdentity(t *testing.T) {
	incarnation := workflowrecovery.OwnerIncarnation{
		Digest: strings.Repeat("b", 64), BootID: "different-boot", PID: 424242, StartTimeTicks: 1,
		PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64),
	}
	_, verifier, owner := drainedOwnerScopeFixture(t, "workflow-identityless-receipt", incarnation, "")
	_, err := verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), "workflow-identityless-receipt",
		[]workflowrecovery.OwnerEpochIdentity{owner})
	if err == nil {
		t.Fatal("a cleanup receipt without a scope identity was accepted")
	}
}

// fakeOwnerScopeOpener stands in for the delegated cgroup v2 root. A scope is a
// directory under root, and Remove unlinks it. It follows the open contract of
// piruntime.OpenLinuxCgroupScopeForDrain: a non-empty expected identity must
// equal the observed identity, and an absent directory reports ENOENT. The
// tests use it to exercise the verifier's ordering, binding and proof logic. It
// does not exercise cgroup mechanics.
type fakeOwnerScopeOpener struct {
	root       string
	rootDigest string
	identity   string
	opens      []string
	removed    []string
}

type fakeOwnerScope struct {
	opener *fakeOwnerScopeOpener
	path   string
}

func (f *fakeOwnerScopeOpener) open(_ *os.File, name, _, _, expectedIdentity string) (ownerScopeDrainer, error) {
	f.opens = append(f.opens, expectedIdentity)
	path := filepath.Join(f.root, name)
	if _, err := os.Lstat(path); err != nil {
		return nil, errors.Join(errors.New("fake owner scope refused"), err)
	}
	if expectedIdentity != "" && expectedIdentity != f.identity {
		return nil, errors.New("fake owner scope identity differs from the expected identity")
	}
	return &fakeOwnerScope{opener: f, path: path}, nil
}

func (s *fakeOwnerScope) RootDigest() string     { return s.opener.rootDigest }
func (s *fakeOwnerScope) IdentityDigest() string { return s.opener.identity }
func (s *fakeOwnerScope) Close() error           { return nil }
func (s *fakeOwnerScope) Remove(context.Context) error {
	if err := os.Remove(s.path); err != nil {
		return err
	}
	s.opener.removed = append(s.opener.removed, s.path)
	return nil
}

// unboundScopeFixture is a catalog with a real journal, an entry for the workflow,
// and no recorded owner scope for epoch 1. The scope directory exists when present.
type unboundScopeFixture struct {
	catalog  *piServiceWorkflowCatalog
	verifier catalogOwnerDrainVerifier
	journal  *audit.Journal
	opener   *fakeOwnerScopeOpener
	owner    workflowrecovery.OwnerEpochIdentity
	path     string
}

func newUnboundScopeFixture(t *testing.T, workflow string, incarnation workflowrecovery.OwnerIncarnation,
	present bool, identity string) unboundScopeFixture {
	t.Helper()
	root := t.TempDir()
	// audit.Open requires an exact 0700 parent; t.TempDir is 0755 under umask 022.
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(root, "workflow-catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	digest := strings.Repeat("a", 64)
	entry := piServiceWorkflowEntry{AdmissionRecordHash: digest, WorkflowID: workflow, CheckpointBound: true}
	catalog := &piServiceWorkflowCatalog{journal: journal,
		entries:           map[string]piServiceWorkflowEntry{digest: entry},
		byHostAdmissionID: make(map[string]string), ownerScopes: make(map[string]piServiceWorkflowOwnerScope)}
	name := workflowOwnerScopeName(workflow, 1)
	path := filepath.Join(root, name)
	if present {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cgroupRootDigest := "sha256:" + strings.Repeat("a", 64)
	opener := &fakeOwnerScopeOpener{root: root, rootDigest: cgroupRootDigest, identity: identity}
	return unboundScopeFixture{catalog: catalog, journal: journal, opener: opener, path: path,
		owner: workflowrecovery.OwnerEpochIdentity{Epoch: 1, Incarnation: incarnation},
		verifier: catalogOwnerDrainVerifier{catalog: catalog, cgroupRoot: root,
			cgroupRootDigest: cgroupRootDigest, containment: piruntime.D09ContainmentProfile,
			settlement: piruntime.D09SettlementProfile, openScope: opener.open}}
}

// A superseded owner with an unbound scope is recovered: the process is dead, so
// the scope is removed, bound to the recovered epoch, and marked drained. The
// proof commits to the identity captured before teardown. A live owner is refused
// first, and its scope is not touched. An absent scope with no record is refused.
func TestCatalogOwnerDrainUnboundScopeIsRecoveredOrRefused(t *testing.T) {
	const workflow = "workflow-unbound-scope"
	identity := "sha256:" + strings.Repeat("d", 64)
	bootID, liveStart := currentProcessIdentityForTest(t)
	deadIncarnation := workflowrecovery.OwnerIncarnation{
		Digest: strings.Repeat("b", 64), BootID: "different-boot", PID: 424242, StartTimeTicks: 1,
		PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64),
	}
	liveIncarnation := workflowrecovery.OwnerIncarnation{
		Digest: strings.Repeat("b", 64), BootID: bootID, PID: os.Getpid(), StartTimeTicks: liveStart,
		PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64),
	}

	t.Run("dead owner with unbound scope is removed, bound and drained", func(t *testing.T) {
		f := newUnboundScopeFixture(t, workflow, deadIncarnation, true, identity)
		digests, err := f.verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), workflow,
			[]workflowrecovery.OwnerEpochIdentity{f.owner})
		if err != nil || len(digests) != 1 {
			t.Fatalf("dead owner with unbound scope was refused: %v (%d digests)", err, len(digests))
		}
		if !reflect.DeepEqual(f.opener.opens, []string{""}) {
			t.Fatalf("unbound scope was opened with expected identities %q, want one empty expectation", f.opener.opens)
		}
		if _, err := os.Lstat(f.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unbound scope was not removed: %v", err)
		}
		state, bound := f.catalog.ownerScope(workflow, f.owner.Epoch)
		if !bound || !state.Drained || state.ScopeIdentityDigest != identity || state.ScopePath != f.path {
			t.Fatalf("recovered scope was not bound and drained at the captured identity: %+v", state)
		}
		trace, err := f.journal.Trace()
		if err != nil || len(trace.Records) != 2 ||
			trace.Records[0].Event.Kind != "pi_workflow_owner_scope_bound" ||
			trace.Records[1].Event.Kind != "pi_workflow_owner_scope_drained" {
			t.Fatalf("recovery did not journal bind then drain: records=%d err=%v", len(trace.Records), err)
		}
		// The proof is drainproof's proof of the observed facts, with the receipt
		// and the expectation both equal to the identity captured before teardown.
		want, err := drainproof.Prove(drainproof.OwnerObservation{
			Owner: f.owner, BootID: deadIncarnation.BootID, ObservedBootID: bootID,
			StartTicks:             deadIncarnation.StartTimeTicks,
			Scope:                  drainproof.ScopeObservation{Absent: true, IdentityDigest: identity},
			CleanupReceiptIdentity: identity, ExpectedScopeIdentity: identity,
		})
		if err != nil || digests[0] != want.Digest {
			t.Fatalf("drain digest %q does not bind the captured identity; want %q (%v)", digests[0], want.Digest, err)
		}
	})

	t.Run("live owner with unbound scope is refused and the scope is not removed", func(t *testing.T) {
		f := newUnboundScopeFixture(t, workflow, liveIncarnation, true, identity)
		if _, err := f.verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), workflow,
			[]workflowrecovery.OwnerEpochIdentity{f.owner}); err == nil {
			t.Fatal("live owner with an unbound scope was accepted")
		}
		if len(f.opener.opens) != 0 || len(f.opener.removed) != 0 {
			t.Fatalf("live owner's scope was opened or removed: opens=%q removed=%q", f.opener.opens, f.opener.removed)
		}
		if _, err := os.Lstat(f.path); err != nil {
			t.Fatalf("live owner's scope was removed: %v", err)
		}
		if _, bound := f.catalog.ownerScope(workflow, f.owner.Epoch); bound {
			t.Fatal("live owner's scope was bound")
		}
	})

	t.Run("absent scope with no record is refused", func(t *testing.T) {
		f := newUnboundScopeFixture(t, workflow, deadIncarnation, false, identity)
		if _, err := f.verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), workflow,
			[]workflowrecovery.OwnerEpochIdentity{f.owner}); err == nil {
			t.Fatal("absent scope with no record was accepted")
		}
		if _, bound := f.catalog.ownerScope(workflow, f.owner.Epoch); bound {
			t.Fatal("absent scope with no record was bound")
		}
		if trace, err := f.journal.Trace(); err != nil || len(trace.Records) != 0 {
			t.Fatalf("refused absent scope journaled %d records: %v", len(trace.Records), err)
		}
	})
}

// The drain digest that the protected root persists is drainproof's digest of
// the observed facts. Identical receipts give identical digests, and a
// different scope identity gives a different digest.
func TestCatalogOwnerDrainProofDigestBindsCleanupReceiptIdentity(t *testing.T) {
	const workflow = "workflow-drain-digest"
	incarnation := workflowrecovery.OwnerIncarnation{
		Digest: strings.Repeat("b", 64), BootID: "different-boot", PID: 424242, StartTimeTicks: 1,
		PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64),
	}
	digestFor := func(scopeIdentity string) string {
		t.Helper()
		_, verifier, owner := drainedOwnerScopeFixture(t, workflow, incarnation, scopeIdentity)
		digests, err := verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), workflow,
			[]workflowrecovery.OwnerEpochIdentity{owner})
		if err != nil || len(digests) != 1 {
			t.Fatalf("drained receipt with identity %q was refused or missing a digest: %v (%d digests)",
				scopeIdentity, err, len(digests))
		}
		return digests[0]
	}
	first := digestFor("sha256:" + strings.Repeat("d", 64))
	again := digestFor("sha256:" + strings.Repeat("d", 64))
	other := digestFor("sha256:" + strings.Repeat("e", 64))
	if len(first) != 64 {
		t.Fatalf("drain digest %q is not a SHA-256 hex digest", first)
	}
	if first != again {
		t.Fatal("identical observations produced different drain digests")
	}
	if first == other {
		t.Fatal("different scope identities produced the same drain digest")
	}
}

// neverExposedFixture is a durable catalog whose epoch-2 owner was cleaned up
// by the binder without ever creating a worker scope. The receipt is written by
// the real MarkOwnerUnexposedDrained, and the catalog is then closed and
// replayed cold from its journal, so nothing in memory survives from the writer.
type neverExposedFixture struct {
	verifier      catalogOwnerDrainVerifier
	owner         workflowrecovery.OwnerEpochIdentity
	cgroupRoot    string
	journalPath   string
	admissionHash string
}

func newNeverExposedFixture(t *testing.T, incarnation workflowrecovery.OwnerIncarnation) neverExposedFixture {
	t.Helper()
	root := privateGovernedTestRoot(t)
	ownerRoot := filepath.Join(root, "owner-store")
	cgroupRoot := filepath.Join(root, "cgroup")
	for _, dir := range []string{ownerRoot, cgroupRoot} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	journalPath := filepath.Join(root, "workflow-catalog.jsonl")
	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	admissionHash := bareWorkflowDigest(digestBytes([]byte("never-exposed-admission")))
	entry := catalogReplayEntry(root, ownerRoot, admissionHash)
	reserved, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash, Entry: entry})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(audit.Event{Kind: "pi_workflow_admission_reserved",
		ID: workflowCatalogEventID("reserved", admissionHash), Data: reserved}); err != nil {
		t.Fatal(err)
	}
	catalog := reopenCatalogReplayJournal(t, journal, ownerRoot)
	initialDigest := bareWorkflowDigest(digestBytes([]byte("never-exposed-initial-checkpoint")))
	store := workflowrecovery.OwnerStoreIdentity{
		Digest: bareWorkflowDigest(digestBytes([]byte("never-exposed-store"))), Path: entry.OwnerDBPath,
	}
	freshIncarnation := workflowrecovery.OwnerIncarnation{
		Digest: bareWorkflowDigest(digestBytes([]byte("never-exposed-fresh-incarnation"))), BootID: "never-exposed-boot",
		PID: os.Getpid(), StartTimeTicks: 1, PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()),
		CgroupIdentityDigest: bareWorkflowDigest(digestBytes([]byte("never-exposed-fresh-cgroup"))),
	}
	if err := catalog.AuthorizeFreshWorkflow(context.Background(), catalogReplayWorkflowID, 1, initialDigest,
		freshIncarnation, entry.Limits.Deadline, bareWorkflowDigest(digestBytes([]byte("never-exposed-root"))), store); err != nil {
		t.Fatalf("fresh authorization refused: %v", err)
	}
	checkpoint, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema,
		AdmissionHash: admissionHash, Digest: initialDigest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(audit.Event{Kind: "pi_workflow_checkpoint_bound",
		ID: workflowCatalogEventID("checkpoint", admissionHash), Data: checkpoint}); err != nil {
		t.Fatal(err)
	}
	// The production writer, exactly as the binder failure paths call it.
	if err := catalog.MarkOwnerUnexposedDrained(admissionHash, catalogReplayWorkflowID, 2, incarnation, cgroupRoot); err != nil {
		t.Fatalf("MarkOwnerUnexposedDrained refused: %v", err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	cold := reopenCatalogReplayJournal(t, journal, ownerRoot)
	t.Cleanup(func() { _ = cold.Close() })
	return neverExposedFixture{journalPath: journalPath, cgroupRoot: cgroupRoot, admissionHash: admissionHash,
		owner: workflowrecovery.OwnerEpochIdentity{Epoch: 2, Incarnation: incarnation},
		verifier: catalogOwnerDrainVerifier{catalog: cold, cgroupRoot: cgroupRoot,
			cgroupRootDigest: "sha256:" + strings.Repeat("a", 64), containment: piruntime.D09ContainmentProfile,
			settlement: piruntime.D09SettlementProfile}}
}

func (f neverExposedFixture) verify(owner workflowrecovery.OwnerEpochIdentity) ([]string, error) {
	return f.verifier.VerifyOwnerCellsDrainedWithProof(context.Background(), catalogReplayWorkflowID,
		[]workflowrecovery.OwnerEpochIdentity{owner})
}

func deadNeverExposedIncarnation() workflowrecovery.OwnerIncarnation {
	return workflowrecovery.OwnerIncarnation{
		Digest: strings.Repeat("b", 64), BootID: "different-boot", PID: 424242, StartTimeTicks: 1,
		PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: strings.Repeat("c", 64),
	}
}

// A binder failure that never created a worker scope leaves an Unexposed,
// Drained receipt with no scope identity. After a cold replay that receipt must
// be a valid drain basis for a dead owner whose scope name is absent.
func TestCatalogOwnerDrainAcceptsNeverExposedReceiptFromProductionWriter(t *testing.T) {
	f := newNeverExposedFixture(t, deadNeverExposedIncarnation())
	state, ok := f.verifier.catalog.ownerScope(catalogReplayWorkflowID, f.owner.Epoch)
	if !ok || !state.Drained || !state.Unexposed || state.ScopeIdentityDigest != "" {
		t.Fatalf("cold replay did not restore the writer's never-exposed receipt: %+v", state)
	}
	digests, err := f.verify(f.owner)
	if err != nil || len(digests) != 1 {
		t.Fatalf("never-exposed receipt from the production writer was refused: %v (%d digests)", err, len(digests))
	}
	// The evidence digest binds drainproof's never-bound proof, which is not the
	// proof of any bound-scope receipt for the same owner.
	bootID, _ := currentProcessIdentityForTest(t)
	inc := f.owner.Incarnation
	want, err := drainproof.Prove(drainproof.OwnerObservation{
		Owner: f.owner, BootID: inc.BootID, ObservedBootID: bootID, StartTicks: inc.StartTimeTicks,
		Scope: drainproof.ScopeObservation{Absent: true, NeverBound: true,
			WorkflowID: catalogReplayWorkflowID, ScopeName: workflowOwnerScopeName(catalogReplayWorkflowID, f.owner.Epoch)},
	})
	if err != nil || digests[0] != want.Digest {
		t.Fatalf("drain digest %q does not bind the never-bound proof %q (%v)", digests[0], want.Digest, err)
	}
	// The digest is specific to the workflow: another workflow at the same epoch
	// under the same owner incarnation has a different never-bound proof.
	otherWorkflow := catalogReplayWorkflowID + "-other"
	other, err := drainproof.Prove(drainproof.OwnerObservation{
		Owner: f.owner, BootID: inc.BootID, ObservedBootID: bootID, StartTicks: inc.StartTimeTicks,
		Scope: drainproof.ScopeObservation{Absent: true, NeverBound: true,
			WorkflowID: otherWorkflow, ScopeName: workflowOwnerScopeName(otherWorkflow, f.owner.Epoch)},
	})
	if err != nil || other.Digest == digests[0] {
		t.Fatalf("never-bound digest does not bind the workflow identity: %v", err)
	}
	bound, err := drainproof.Prove(drainproof.OwnerObservation{
		Owner: f.owner, BootID: inc.BootID, ObservedBootID: bootID, StartTicks: inc.StartTimeTicks,
		Scope:                  drainproof.ScopeObservation{Absent: true, IdentityDigest: "sha256:" + strings.Repeat("d", 64)},
		CleanupReceiptIdentity: "sha256:" + strings.Repeat("d", 64), ExpectedScopeIdentity: "sha256:" + strings.Repeat("d", 64),
	})
	if err != nil || bound.Digest == digests[0] {
		t.Fatalf("never-bound digest is confusable with a bound-scope digest: %v", err)
	}
}

func TestCatalogOwnerDrainRefusesNeverExposedReceiptWhenNotProven(t *testing.T) {
	bootID, liveStart := currentProcessIdentityForTest(t)
	t.Run("scope present", func(t *testing.T) {
		f := newNeverExposedFixture(t, deadNeverExposedIncarnation())
		scope := filepath.Join(f.cgroupRoot, workflowOwnerScopeName(catalogReplayWorkflowID, f.owner.Epoch))
		if err := os.Mkdir(scope, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := f.verify(f.owner); err == nil {
			t.Fatal("never-exposed receipt was accepted while the scope name is present")
		}
		if _, err := os.Lstat(scope); err != nil {
			t.Fatalf("refused drain touched the present scope: %v", err)
		}
	})
	t.Run("live owner", func(t *testing.T) {
		live := deadNeverExposedIncarnation()
		live.BootID, live.PID, live.StartTimeTicks = bootID, os.Getpid(), liveStart
		f := newNeverExposedFixture(t, live)
		if _, err := f.verify(f.owner); err == nil {
			t.Fatal("never-exposed receipt was accepted for a live owner process")
		}
	})
	t.Run("wrong incarnation", func(t *testing.T) {
		f := newNeverExposedFixture(t, deadNeverExposedIncarnation())
		other := f.owner
		other.Incarnation.Digest = strings.Repeat("e", 64)
		if _, err := f.verify(other); err == nil {
			t.Fatal("never-exposed receipt was accepted for a different owner incarnation")
		}
	})
	t.Run("wrong epoch", func(t *testing.T) {
		f := newNeverExposedFixture(t, deadNeverExposedIncarnation())
		other := f.owner
		other.Epoch = 3
		if _, err := f.verify(other); err == nil {
			t.Fatal("never-exposed receipt for epoch 2 was accepted for epoch 3")
		}
	})
	t.Run("receipt also marked bound", func(t *testing.T) {
		f := newNeverExposedFixture(t, deadNeverExposedIncarnation())
		key := workflowOwnerEpochKey(catalogReplayWorkflowID, f.owner.Epoch)
		state := f.verifier.catalog.ownerScopes[key]
		state.ScopeIdentityDigest = "sha256:" + strings.Repeat("d", 64)
		f.verifier.catalog.ownerScopes[key] = state
		if _, err := f.verify(f.owner); err == nil {
			t.Fatal("receipt that is both unexposed and bound was accepted")
		}
	})
	t.Run("replay rejects an unexposed receipt over a bound scope", func(t *testing.T) {
		f := newNeverExposedFixture(t, deadNeverExposedIncarnation())
		_ = f.verifier.catalog.Close()
		journal, err := audit.Open(f.journalPath)
		if err != nil {
			t.Fatal(err)
		}
		const epoch = 3
		name := workflowOwnerScopeName(catalogReplayWorkflowID, epoch)
		bound := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: f.admissionHash,
			WorkflowID: catalogReplayWorkflowID, OwnerEpoch: epoch, Incarnation: f.owner.Incarnation,
			ScopeName: name, ScopePath: filepath.Join(f.cgroupRoot, name), ScopeIdentityDigest: "sha256:" + strings.Repeat("d", 64)}
		unexposed := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: f.admissionHash,
			WorkflowID: catalogReplayWorkflowID, OwnerEpoch: epoch, Incarnation: f.owner.Incarnation,
			ScopeName: name, DrainProofDigest: bareWorkflowDigest(digestBytes([]byte("forged"))), Unexposed: true}
		for _, record := range []struct {
			kind, id string
			event    piServiceWorkflowEvent
		}{
			{"pi_workflow_owner_scope_bound", workflowOwnerScopeEventID("bound", catalogReplayWorkflowID, epoch), bound},
			{"pi_workflow_owner_scope_drained", workflowOwnerScopeEventID("drained", catalogReplayWorkflowID, epoch), unexposed},
		} {
			encoded, err := json.Marshal(record.event)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := journal.Append(audit.Event{Kind: record.kind, ID: record.id, Data: encoded}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := loadPiServiceWorkflowCatalog(journal); !errors.Is(err, ErrPiWorkflowCatalog) {
			t.Fatalf("replay accepted an unexposed receipt over a bound scope: %v", err)
		}
	})
}

func TestPiServiceWorkflowCatalogTerminalTupleIsExactAndIdempotent(t *testing.T) {
	root := privateGovernedTestRoot(t)
	journal, err := audit.Open(filepath.Join(root, "workflow-catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	admissionHash := strings.Repeat("a", 64)
	checkpointDigest := strings.Repeat("b", 64)
	proofDigest := "sha256:" + strings.Repeat("c", 64)
	entry := piServiceWorkflowEntry{AdmissionRecordHash: admissionHash, WorkflowID: "workflow-terminal-tuple",
		CheckpointBound: true, LatestCheckpointDigest: checkpointDigest}
	catalog := &piServiceWorkflowCatalog{journal: journal,
		entries:           map[string]piServiceWorkflowEntry{admissionHash: entry},
		byHostAdmissionID: make(map[string]string), ownerScopes: make(map[string]piServiceWorkflowOwnerScope)}
	mark := func(checkpoint, runID, proof string, sequence uint64, transaction string) error {
		return catalog.MarkTerminal(admissionHash, checkpoint, runID, proof, sequence, transaction)
	}
	if err := mark(strings.Repeat("d", 64), "run-terminal-tuple", proofDigest, 3, "transaction-terminal-tuple"); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("stale checkpoint was accepted as terminal: %v", err)
	}
	if err := mark(checkpointDigest, "run-terminal-tuple", proofDigest, 3, "transaction-terminal-tuple"); err != nil {
		t.Fatalf("matching terminal tuple was refused: %v", err)
	}
	if err := mark(checkpointDigest, "run-terminal-tuple", proofDigest, 3, "transaction-terminal-tuple"); err != nil {
		t.Fatalf("exact terminal tuple was not idempotent: %v", err)
	}
	for name, mutate := range map[string]func() error{
		"run ID": func() error { return mark(checkpointDigest, "other-run", proofDigest, 3, "transaction-terminal-tuple") },
		"proof digest": func() error {
			return mark(checkpointDigest, "run-terminal-tuple", "sha256:"+strings.Repeat("e", 64), 3, "transaction-terminal-tuple")
		},
		"proof sequence": func() error {
			return mark(checkpointDigest, "run-terminal-tuple", proofDigest, 4, "transaction-terminal-tuple")
		},
		"publication transaction": func() error {
			return mark(checkpointDigest, "run-terminal-tuple", proofDigest, 3, "other-transaction")
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); !errors.Is(err, ErrPiWorkflowCatalog) {
				t.Fatalf("mismatched terminal tuple was accepted: %v", err)
			}
		})
	}
	terminal, ok := catalog.entries[admissionHash]
	trace, err := journal.Trace()
	if err != nil || !ok || !terminal.Terminal || terminal.LatestCheckpointDigest != checkpointDigest ||
		terminal.TerminalRunID != "run-terminal-tuple" || terminal.TerminalProofDigest != proofDigest ||
		terminal.TerminalProofSequence != 3 || terminal.TerminalTransactionID != "transaction-terminal-tuple" ||
		len(trace.Records) != 1 {
		t.Fatalf("terminal tuple was not durably recorded exactly once: entry=%+v records=%d err=%v", terminal, len(trace.Records), err)
	}
}

func TestSettledServiceProfileRequiresExactLandlockABI(t *testing.T) {
	profile, template, _ := productionProofFixture()
	profile.CgroupRootDigest = "sha256:" + strings.Repeat("9", 64)
	for _, test := range []struct {
		name string
		abi  int
	}{{"below", 7}, {"exact", 8}, {"future", 9}} {
		t.Run(test.name, func(t *testing.T) {
			abi := test.abi
			template.LandlockABI = abi
			err := validateSettledServiceProfile(profile, template)
			if abi == 8 && err != nil {
				t.Fatalf("the pinned Landlock ABI was refused: %v", err)
			}
			if abi != 8 && !errors.Is(err, ErrPiWorkflowCatalog) {
				t.Fatalf("unsupported Landlock ABI %d was accepted: %v", abi, err)
			}
		})
	}
}

func TestSettledServiceRequestPinsMatchBareRootIdentity(t *testing.T) {
	profile, template, _ := productionProofFixture()
	bareRoot := strings.Repeat("c", 64)
	entry := piServiceWorkflowEntry{WorkflowID: "workflow-settled", RunID: "run-settled", SessionID: "session-settled",
		ConversationID: "conversation-settled", RootIdentityDigest: bareRoot,
		TaskDigest: "sha256:" + strings.Repeat("d", 64), PromptDigest: "sha256:" + strings.Repeat("e", 64)}
	pins := workflowrecovery.Pins{SessionID: entry.SessionID, RootIdentityDigest: bareRoot,
		TaskDigest: bareWorkflowDigest(entry.TaskDigest), PromptDigest: bareWorkflowDigest(entry.PromptDigest),
		ProfileDigest: bareWorkflowDigest(profile.Digest), ImageDigest: bareWorkflowDigest(template.ExpectedImageDigest),
		OwnerEpoch: 1, RecoveryGeneration: 1, StoreJournalCount: 1, StoreJournalHead: strings.Repeat("f", 64),
		ProviderDisposition: workflowrecovery.DispositionAwaitingProviderExchange}
	view := workflowrecovery.SettledCheckpointView{Checkpoint: workflowrecovery.Checkpoint{Pins: pins}}
	// An admitted bare root identity must pass the pin comparison and reach the disposition gate, which refuses the unresolved exchange.
	err := (*piServiceWorkflowCatalog)(nil).verifyCompletedServiceRequestPins(context.Background(), piServiceRequestState{}, entry,
		productionPiReceipt{ProfileHash: profile.Digest}, view, piruntime.D09ManagedLiveRootRegistration{}, managedLiveRootDescriptorIdentity{},
		workflow.RootIdentity{}, catalogOwnerDrainVerifier{}, template)
	if !errors.Is(err, workflowrecovery.ErrPendingUnknown) {
		t.Fatalf("admitted bare root identity did not reach the settled disposition gate: %v", err)
	}
}

func TestSettledServiceDispositionRejectsUnresolvedPins(t *testing.T) {
	const currentConversation = "conversation-settled-g1"
	settled := func() workflowrecovery.Pins {
		return workflowrecovery.Pins{
			ProviderDisposition: workflowrecovery.DispositionAwaitingHostPrompt,
			ProviderTranscripts: []workflowrecovery.TranscriptPin{{ConversationID: currentConversation,
				Disposition: workflowrecovery.DispositionAwaitingHostPrompt}},
			PendingCallDisposition: workflowrecovery.PendingCallNone,
			EffectReservation:      &workflowrecovery.EffectReservationPin{Disposition: workflowrecovery.EffectReservationTerminal},
			HostCallReservation:    &workflowrecovery.HostCallReservationPin{Disposition: workflowrecovery.HostCallTerminal},
		}
	}
	if err := validateSettledServiceDisposition(settled(), currentConversation); err != nil {
		t.Fatalf("fully settled current conversation was refused: %v", err)
	}
	retired := settled()
	retired.ProviderDisposition = workflowrecovery.DispositionRetired
	retired.ProviderTranscripts[0].Disposition = workflowrecovery.DispositionRetired
	retired.PendingCallDisposition = workflowrecovery.PendingCallRetired
	retired.EffectReservation.Disposition = workflowrecovery.EffectReservationRetired
	retired.HostCallReservation.Disposition = workflowrecovery.HostCallRetired
	if err := validateSettledServiceDisposition(retired, currentConversation); err != nil {
		t.Fatalf("fully retired pins were refused: %v", err)
	}
	closedHistory := settled()
	closedHistory.ProviderTranscripts = []workflowrecovery.TranscriptPin{
		{ConversationID: "conversation-previous-g1", Disposition: workflowrecovery.DispositionRetired},
		{ConversationID: currentConversation, Disposition: workflowrecovery.DispositionAwaitingHostPrompt},
	}
	if err := validateSettledServiceDisposition(closedHistory, currentConversation); err != nil {
		t.Fatalf("a quiescent latest conversation after closed history was refused: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*workflowrecovery.Pins)
	}{
		{"provider awaiting exchange", func(p *workflowrecovery.Pins) {
			p.ProviderDisposition = workflowrecovery.DispositionAwaitingProviderExchange
		}},
		{"provider awaiting gate", func(p *workflowrecovery.Pins) {
			p.ProviderDisposition = workflowrecovery.DispositionAwaitingGateDecision
		}},
		{"provider pending result", func(p *workflowrecovery.Pins) {
			p.ProviderDisposition = workflowrecovery.DispositionPendingProviderResult
		}},
		{"provider unknown", func(p *workflowrecovery.Pins) { p.ProviderDisposition = workflowrecovery.DispositionUnknown }},
		{"pending call in flight", func(p *workflowrecovery.Pins) { p.PendingCallDisposition = workflowrecovery.PendingCallInFlight }},
		{"pending call captured", func(p *workflowrecovery.Pins) { p.PendingCallDisposition = workflowrecovery.PendingCallCaptured }},
		{"pending call unknown", func(p *workflowrecovery.Pins) { p.PendingCallDisposition = workflowrecovery.PendingCallUnknown }},
		{"effect reserved", func(p *workflowrecovery.Pins) {
			p.EffectReservation.Disposition = workflowrecovery.EffectReservationReserved
		}},
		{"effect unknown", func(p *workflowrecovery.Pins) {
			p.EffectReservation.Disposition = workflowrecovery.EffectReservationUnknown
		}},
		{"host call reserved", func(p *workflowrecovery.Pins) { p.HostCallReservation.Disposition = workflowrecovery.HostCallReserved }},
		{"host call unknown", func(p *workflowrecovery.Pins) { p.HostCallReservation.Disposition = workflowrecovery.HostCallUnknown }},
		{"transcript pending", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts[0].Disposition = workflowrecovery.DispositionPendingProviderResult
		}},
		{"transcript unknown", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts[0].Disposition = workflowrecovery.DispositionUnknown
		}},
		{"transcript missing terminal lineage", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts[0].Disposition = workflowrecovery.DispositionNone
			p.ProviderDisposition = workflowrecovery.DispositionNone
		}},
		{"non-final transcript awaiting host prompt", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts = []workflowrecovery.TranscriptPin{
				{ConversationID: "conversation-previous", Disposition: workflowrecovery.DispositionAwaitingHostPrompt},
				{ConversationID: currentConversation, Disposition: workflowrecovery.DispositionAwaitingHostPrompt},
			}
		}},
		{"provider disposition does not match latest transcript", func(p *workflowrecovery.Pins) {
			p.ProviderDisposition = workflowrecovery.DispositionRetired
		}},
		{"latest transcript does not match current generation", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts[0].ConversationID = "conversation-old-generation"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pins := settled()
			test.mutate(&pins)
			if err := validateSettledServiceDisposition(pins, currentConversation); !errors.Is(err, workflowrecovery.ErrPendingUnknown) {
				t.Fatalf("unresolved pins were admitted: %v", err)
			}
		})
	}
}

// A pure empty provider result creates no effect or host-call reservation, so
// its settled disposition must be accepted without either reservation pin.
func TestSettledServiceDispositionAcceptsPureEmptyProviderResult(t *testing.T) {
	const currentConversation = "conversation-settled-g1"
	pins := workflowrecovery.Pins{
		ProviderDisposition: workflowrecovery.DispositionTerminal,
		ProviderTranscripts: []workflowrecovery.TranscriptPin{{ConversationID: currentConversation,
			Disposition: workflowrecovery.DispositionTerminal}},
		PendingCallDisposition: workflowrecovery.PendingCallNone,
	}
	if err := validateSettledServiceDisposition(pins, currentConversation); err != nil {
		t.Fatalf("pure empty provider result without reservations was refused: %v", err)
	}
}

func TestProductionReceiptSettlementClaimRequiresExactDisposition(t *testing.T) {
	for _, test := range []struct {
		name    string
		receipt productionPiReceipt
		want    bool
	}{
		{"published with transaction", productionPiReceipt{Disposition: "PUBLISHED",
			PublicationStatus: string(publication.StatusSucceeded), PublicationTransactionID: "transaction-published"}, true},
		{"no change without transaction", productionPiReceipt{Disposition: "NO_DIFF", PublicationStatus: "NO_DIFF"}, true},
		{"no change naming a transaction", productionPiReceipt{Disposition: "NO_DIFF", PublicationStatus: "NO_DIFF",
			PublicationTransactionID: "transaction-forged"}, false},
		{"no change claimed as succeeded publication", productionPiReceipt{Disposition: "NO_DIFF",
			PublicationStatus: string(publication.StatusSucceeded)}, false},
		{"published without transaction", productionPiReceipt{Disposition: "PUBLISHED",
			PublicationStatus: string(publication.StatusSucceeded)}, false},
		{"unknown disposition", productionPiReceipt{Disposition: "UNKNOWN", PublicationStatus: "NO_DIFF"}, false},
		{"publication failed", productionPiReceipt{Disposition: "PUBLICATION_FAILED",
			PublicationStatus: string(publication.StatusFailed), PublicationTransactionID: "transaction-failed"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := productionReceiptSettlementClaimed(test.receipt); got != test.want {
				t.Fatalf("settlement claim = %t, want %t", got, test.want)
			}
		})
	}
}
