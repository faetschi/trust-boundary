//go:build linux

package sessionrepo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// An allowed Bash command can change no files, so its delta is empty. Admission
// recording clones the delta onto a nil slice (JSON null), while the recovered
// transition carries an empty slice (JSON []). Recovery must accept the retained
// Store, and must compare the recorded bytes without rewriting them.
func TestRecoveryAcceptsAnAllowedBashCommandWithNoWorkspaceChanges(t *testing.T) {
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureDir(t, filepath.Join(sourcePath, "build"), 0o755)
	writeFixtureFile(t, filepath.Join(sourcePath, "README.md"), "baseline text\n", 0o644)
	writeFixtureFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}

	op, decision := lease("empty-bash"), allow("empty-bash")
	viewID := reserveTestViewID(t, store)
	spec := CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "printf 'tests passed\\n'"}, DeclaredOutputPaths: []string{}}
	argumentDigest, err := CommandArgumentsDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	contextDigest, err := commandExecutionContextDigest(viewID, g0.TreeDigest(), op.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	request := OperationRequest{Tool: "bash", Operation: op, Decision: decision,
		InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: argumentDigest,
		ViewID: viewID, ExecutionContextDigest: contextDigest, OutputScopePaths: []string{}}
	expectedG1 := fixtureSnapshot(t, "g1", "baseline text\n", "baseline result\n")
	authority.grant(request)
	authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expectedG1, request, 1))
	result, err := store.RunBash(context.Background(), g0, op, decision, spec, CommandRunnerFunc(
		func(_ context.Context, view *CommandView, _ CommandSpec) (CommandResult, CommandSettlement, error) {
			return CommandResult{ExitObserved: true, Stdout: []byte("tests passed\n")}, CommandSettlement{
				LeaseID: op.LeaseID, ViewID: view.ID(), ProcessScopeEmpty: true, WritersStopped: true,
				MountDetached: true, ExitObserved: true, EvidenceClass: "synthetic-test-fixture",
			}, nil
		}))
	if err != nil || result.Outcome != "success" || result.Admission == nil || result.Admission.Outcome != "allow" ||
		len(result.Transition.Changes) != 0 {
		t.Fatalf("allowed no-change Bash: result=%+v err=%v", result, err)
	}

	admissionTrace, err := store.admissionJournal.Trace()
	if err != nil || len(admissionTrace.Records) != 1 {
		t.Fatalf("admission trace records=%d err=%v", len(admissionTrace.Records), err)
	}
	recordedData := append([]byte(nil), admissionTrace.Records[0].Event.Data...)
	record, err := decodeAdmissionRecord(recordedData)
	if err != nil || record.Outcome != "allow" || record.Candidate.Changes != nil {
		t.Fatalf("admission recording changed: record=%+v err=%v", record, err)
	}
	if !bytes.Contains(recordedData, []byte(`"changes":null`)) {
		t.Fatalf("recorded empty candidate is not null: %s", recordedData)
	}

	rootPath, options := store.rootPath, store.options
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(openFixtureRoot(t, rootPath), options)
	if err != nil {
		t.Fatalf("recovery refused a Store whose allowed Bash command changed no files: %v", err)
	}
	if len(recovered.generations) != 2 || len(recovered.transitions) != 1 || len(recovered.transitions[0].Changes) != 0 {
		t.Fatalf("recovered no-change Bash: generations=%d transitions=%+v", len(recovered.generations), recovered.transitions)
	}
	recoveredTrace, err := recovered.admissionJournal.Trace()
	if err != nil || len(recoveredTrace.Records) != 1 || !bytes.Equal(recoveredTrace.Records[0].Event.Data, recordedData) {
		t.Fatalf("recovery rewrote the recorded admission: err=%v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
}
