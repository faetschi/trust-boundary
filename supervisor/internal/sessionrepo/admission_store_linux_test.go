//go:build linux

package sessionrepo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/workspace"
)

func TestStoreBashDeniedCandidatePersistsWithoutPromoting(t *testing.T) {
	store, journal, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(sourcePath, "task.txt"), "before\n", 0o644)
	writeFixtureFile(t, filepath.Join(sourcePath, "other.txt"), "before\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}

	expectedPath := filepath.Join(t.TempDir(), "expected")
	if err := os.Mkdir(expectedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(expectedPath, "task.txt"), "before\n", 0o644)
	writeFixtureFile(t, filepath.Join(expectedPath, "other.txt"), "after\n", 0o644)
	expectedRoot := openFixtureRoot(t, expectedPath)
	defer expectedRoot.Close()
	expected, err := workspace.Scan(expectedRoot, store.options.workspaceOptions("g1", true))
	if err != nil {
		t.Fatal(err)
	}

	op, decision := lease("out-of-scope"), allow("out-of-scope")
	viewID := reserveTestViewID(t, store)
	spec := CommandSpec{Executable: "/bin/true", DeclaredOutputPaths: []string{"task.txt"}}
	argumentDigest, err := CommandArgumentsDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	contextDigest, err := commandExecutionContextDigest(viewID, g0.TreeDigest(), op.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	request := OperationRequest{
		Tool: "bash", Operation: op, Decision: decision, InputGeneration: g0.ID(),
		InputTreeDigest: g0.TreeDigest(), ArgumentDigest: argumentDigest,
		ViewID: viewID, ExecutionContextDigest: contextDigest,
		OutputScopePaths: []string{"task.txt"},
	}
	authority.grant(request)
	authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expected, request, 1))
	result, err := store.RunBash(context.Background(), g0, op, decision, spec,
		CommandRunnerFunc(func(_ context.Context, view *CommandView, _ CommandSpec) (CommandResult, CommandSettlement, error) {
			if err := os.WriteFile(filepath.Join(view.testPath(), "other.txt"), []byte("after\n"), 0o644); err != nil {
				return CommandResult{}, CommandSettlement{}, err
			}
			return CommandResult{ExitObserved: true}, CommandSettlement{
				LeaseID: op.LeaseID, ViewID: view.ID(), ProcessScopeEmpty: true,
				WritersStopped: true, MountDetached: true, ExitObserved: true,
				EvidenceClass: "synthetic-test-fixture",
			}, nil
		}))
	if !errors.Is(err, ErrDeltaDenied) || result.Outcome != "denied_after_effect" ||
		result.Command != nil ||
		result.Generation != nil || result.Admission == nil || result.Admission.Outcome != "deny" {
		t.Fatalf("denial result = %+v, err = %v", result, err)
	}
	if len(store.generations) != 1 || len(store.transitions) != 0 {
		t.Fatalf("denied candidate advanced store: generations=%d transitions=%d", len(store.generations), len(store.transitions))
	}
	if _, ok := store.views[viewID]; ok {
		t.Fatal("denied command view was retained")
	}
	if got, err := os.ReadFile(filepath.Join(g0.testPath(), "other.txt")); err != nil || string(got) != "before\n" {
		t.Fatalf("sealed input changed: bytes=%q err=%v", got, err)
	}

	admissionTrace, err := store.admissionJournal.Trace()
	if err != nil || len(admissionTrace.Records) != 1 {
		t.Fatalf("admission trace records=%d err=%v", len(admissionTrace.Records), err)
	}
	record, err := decodeAdmissionRecord(admissionTrace.Records[0].Event.Data)
	if err != nil || record.Outcome != "deny" || len(record.Output.Objects) != 2 ||
		len(record.Candidate.Changes) != 1 || record.Candidate.Changes[0].Path != "other.txt" {
		t.Fatalf("admission did not retain full denied candidate: %+v err=%v", record, err)
	}

	primaryTrace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var deniedEffect audit.EffectTrace
	for _, effect := range primaryTrace.Effects {
		if effect.ID == result.EffectID {
			var durable commandEffectResult
			if effect.Unresolved || effect.Outcome != "success" ||
				json.Unmarshal(effect.Result, &durable) != nil || durable.Admission != *result.Admission {
				t.Fatalf("primary effect did not durably contain admission reference: %+v", effect)
			}
			deniedEffect = effect
			break
		}
	}
	if deniedEffect.ID == "" {
		t.Fatal("primary denied effect was not found")
	}
	effectRequest, _, err := decodeOperationIntent(deniedEffect.Intent)
	if err != nil {
		t.Fatal(err)
	}
	recovery := &admissionRecovery{records: admissionTrace.Records, consumed: make(map[uint64]struct{})}
	wrongReference := *result.Admission
	wrongReference.RecordHash = digestBytes([]byte("wrong admission hash"))
	if _, err := store.recoverAdmissionLocked(recovery, deniedEffect, effectRequest, g0, result.Transition, wrongReference); err == nil {
		t.Fatal("recovery accepted a wrong admission record hash")
	}
	if len(recovery.consumed) != 0 {
		t.Fatal("failed reference verification consumed an admission record")
	}
	if _, err := store.recoverAdmissionLocked(recovery, deniedEffect, effectRequest, g0, result.Transition, *result.Admission); err != nil {
		t.Fatalf("recovery rejected the exact admission reference: %v", err)
	}
	if _, err := store.recoverAdmissionLocked(recovery, deniedEffect, effectRequest, g0, result.Transition, *result.Admission); err == nil {
		t.Fatal("recovery accepted duplicate consumption of one admission record")
	}
	if len(store.operations) != 1 || store.operations[0].Outcome != "denied_after_effect" ||
		store.operations[0].Admission == nil || *store.operations[0].Admission != *result.Admission {
		t.Fatalf("denied operation evidence lost admission reference: %+v", store.operations)
	}
	rootPath, options := store.rootPath, store.options
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(openFixtureRoot(t, rootPath), options)
	if err != nil {
		t.Fatalf("reopen denied candidate: %v", err)
	}
	if len(recovered.generations) != 1 || len(recovered.transitions) != 0 ||
		len(recovered.operations) != 1 || recovered.operations[0].Outcome != "denied_after_effect" ||
		recovered.operations[0].Admission == nil || *recovered.operations[0].Admission != *result.Admission {
		t.Fatalf("recovery promoted or lost denied candidate evidence: %+v", recovered.operations)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	admissions, err := audit.Open(filepath.Join(rootPath, admissionJournalName))
	if err != nil {
		t.Fatal(err)
	}
	_, appendErr := admissions.Append(audit.Event{Kind: admissionEventKind, ID: "orphan-copy", Data: admissionTrace.Records[0].Event.Data})
	_ = admissions.Close()
	if appendErr != nil {
		t.Fatal(appendErr)
	}
	if _, err := Open(openFixtureRoot(t, rootPath), options); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("Open accepted an unreferenced admission record: %v", err)
	}
}

func TestOpenDoesNotCreateMissingAdmissionJournal(t *testing.T) {
	store, _, _, _ := newE2EStore(t)
	rootPath, options := store.rootPath, store.options
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rootPath, admissionJournalName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(openFixtureRoot(t, rootPath), options); err == nil {
		t.Fatal("opened repository without its admission journal")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open recreated the missing admission journal: %v", err)
	}
}
