//go:build linux

package sessionrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

func TestSessionRepositoryLifecycleE2E(t *testing.T) {
	store, _, journalPath, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureDir(t, filepath.Join(sourcePath, "build"), 0o755)
	writeFixtureFile(t, filepath.Join(sourcePath, "README.md"), "baseline text\n", 0o644)
	writeFixtureFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
	source := openFixtureRoot(t, sourcePath)

	g0, err := store.Seed(source, RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	readG0, allowReadG0 := proposal("read-g0"), allow("read-g0")
	authority.grant(OperationRequest{Tool: "read", Operation: readG0, Decision: allowReadG0, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: readArgumentsDigest("README.md", 1024)})
	initial, err := g0.Read(context.Background(), readG0, allowReadG0, "README.md", 1024)
	if err != nil || string(initial) != "baseline text\n" {
		t.Fatalf("read g0: bytes=%q err=%v", initial, err)
	}

	editOp, editDecision := proposal("edit-g1"), allow("edit-g1")
	edits := []Replacement{
		{OldText: "baseline", NewText: "approved"},
	}
	editArgs, _ := argumentsDigest(struct {
		Path  string        `json:"path"`
		Edits []Replacement `json:"edits"`
	}{"README.md", edits})
	editRequest := OperationRequest{Tool: "edit", Operation: editOp, Decision: editDecision, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: editArgs}
	authority.grant(editRequest)
	expectedG1 := fixtureSnapshot(t, "g1", "approved text\n", "baseline result\n")
	authority.grantBinding(editDecision.ID, exactBinding(g0.snapshot, expectedG1, editRequest, 1))
	edit, err := store.Edit(context.Background(), g0, editOp, editDecision, "README.md", edits)
	if err != nil {
		t.Fatalf("edit to g1: %v", err)
	}
	g1 := edit.Generation
	if g1.ID() != "g1" || len(edit.Transition.Changes) != 1 || edit.Transition.Changes[0].Path != "README.md" {
		t.Fatalf("unexpected g0->g1 result: %+v", edit.Transition)
	}
	readG0After, allowReadG0After := proposal("read-g0-after-edit"), allow("read-g0-after-edit")
	authority.grant(OperationRequest{Tool: "read", Operation: readG0After, Decision: allowReadG0After, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: readArgumentsDigest("README.md", 1024)})
	oldBytes, err := g0.Read(context.Background(), readG0After, allowReadG0After, "README.md", 1024)
	if err != nil || string(oldBytes) != "baseline text\n" {
		t.Fatalf("edit changed sealed g0: bytes=%q err=%v", oldBytes, err)
	}
	readG1, allowReadG1 := proposal("read-g1"), allow("read-g1")
	authority.grant(OperationRequest{Tool: "read", Operation: readG1, Decision: allowReadG1, InputGeneration: g1.ID(), InputTreeDigest: g1.TreeDigest(), ArgumentDigest: readArgumentsDigest("README.md", 1024)})
	newBytes, err := g1.Read(context.Background(), readG1, allowReadG1, "README.md", 1024)
	if err != nil || string(newBytes) != "approved text\n" {
		t.Fatalf("read g1: bytes=%q err=%v", newBytes, err)
	}

	bashOp, bashDecision := lease("bash-g2"), allow("bash-g2")
	expectedG2 := fixtureSnapshot(t, "g2", "approved text\n", "command output\n")
	commandSpec := CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "printf 'command output\\n' > build/result.txt && printf 'command completed\\n'"}}
	viewID := reserveTestViewID(t, store)
	bashArgs, _ := commandArgumentsDigest(commandSpec)
	contextDigest, _ := commandExecutionContextDigest(viewID, g1.TreeDigest(), bashOp.LeaseID)
	bashRequest := OperationRequest{Tool: "bash", Operation: bashOp, Decision: bashDecision, InputGeneration: g1.ID(),
		InputTreeDigest: g1.TreeDigest(), ArgumentDigest: bashArgs, ViewID: viewID, ExecutionContextDigest: contextDigest}
	authority.grant(bashRequest)
	authority.grantBinding(bashDecision.ID, exactBinding(g1.snapshot, expectedG2, bashRequest, 2))
	// This direct local Bash fixture validates data flow only. The settlement
	// class is explicitly synthetic and makes no process-scope/mount claim.
	command, err := store.RunBash(context.Background(), g1, bashOp, bashDecision, commandSpec, CommandRunnerFunc(
		func(ctx context.Context, view *CommandView, spec CommandSpec) (CommandResult, CommandSettlement, error) {
			if !sameCommandSpec(spec, commandSpec) {
				return CommandResult{}, CommandSettlement{}, fmt.Errorf("runner received a command outside its authorized spec")
			}
			cmd := exec.CommandContext(ctx, spec.Executable, spec.Args...)
			cmd.Dir = view.testPath()
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			exitCode := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					exitCode = exit.ExitCode()
				} else {
					return CommandResult{}, CommandSettlement{}, err
				}
			}
			result := CommandResult{ExitCode: exitCode, ExitObserved: true, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
			settled := CommandSettlement{
				LeaseID: "lease-bash-g2", ViewID: view.ID(), ProcessScopeEmpty: true,
				WritersStopped: true, MountDetached: true, ExitObserved: true, ExitCode: exitCode,
				EvidenceClass: "synthetic-test-fixture",
			}
			return result, settled, nil
		},
	))
	if err != nil {
		t.Fatalf("bash view to g2: %v", err)
	}
	g2 := command.Generation
	if g2.ID() != "g2" || command.Command == nil || command.Command.ExitCode != 0 {
		t.Fatalf("unexpected g1->g2 command result: %+v", command)
	}
	if !strings.Contains(string(command.Command.Stdout), "command completed") {
		t.Fatalf("command stdout was not captured: %q", command.Command.Stdout)
	}
	readG1After, allowReadG1After := proposal("read-g1-after-bash"), allow("read-g1-after-bash")
	authority.grant(OperationRequest{Tool: "read", Operation: readG1After, Decision: allowReadG1After, InputGeneration: g1.ID(), InputTreeDigest: g1.TreeDigest(), ArgumentDigest: readArgumentsDigest("build/result.txt", 1024)})
	if stillSealed, err := g1.Read(context.Background(), readG1After, allowReadG1After, "build/result.txt", 1024); err != nil || string(stillSealed) != "baseline result\n" {
		t.Fatalf("Bash view changed sealed g1: bytes=%q err=%v", stillSealed, err)
	}
	readG2, allowReadG2 := proposal("read-g2"), allow("read-g2")
	authority.grant(OperationRequest{Tool: "read", Operation: readG2, Decision: allowReadG2, InputGeneration: g2.ID(), InputTreeDigest: g2.TreeDigest(), ArgumentDigest: readArgumentsDigest("build/result.txt", 1024)})
	final, err := g2.Read(context.Background(), readG2, allowReadG2, "build/result.txt", 1024)
	if err != nil || string(final) != "command output\n" {
		t.Fatalf("read g2: bytes=%q err=%v", final, err)
	}

	chain, err := store.Verify()
	if err != nil {
		t.Fatalf("verify complete g0->g1->g2 chain: %v", err)
	}
	if chain.TransitionCount != 2 || chain.BaselineGeneration != "g0" || chain.SealedGeneration != "g2" {
		t.Fatalf("unexpected validated chain: %+v", chain)
	}
	if got, want := strings.Join(chain.TouchedPaths, ","), "README.md,build/result.txt"; got != want {
		t.Fatalf("touched paths = %q, want %q", got, want)
	}
	artifact, err := store.Evidence()
	if err != nil {
		t.Fatalf("build evidence artifact: %v", err)
	}
	if artifact.RealProviderExchange || artifact.PiAdapterWired || len(artifact.Generations) != 3 || len(artifact.ApprovedDeltaLedger) != 2 {
		t.Fatalf("artifact overstates integration or omits generations/ledger: %+v", artifact)
	}
	if artifact.CommandContainmentStatus != "not-established" || artifact.AuditRecordCount == 0 || artifact.AuditHeadHash == "" {
		t.Fatalf("artifact omits containment limitation or journal commitment: %+v", artifact)
	}
	if len(artifact.Operations) != 7 {
		t.Fatalf("recorded %d operation results, want 7", len(artifact.Operations))
	}
	journalFile, err := os.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := audit.Verify(journalFile)
	_ = journalFile.Close()
	if err != nil {
		t.Fatalf("verify durable operation journal: %v", err)
	}
	if len(trace.Effects) != len(artifact.Operations)+1 {
		t.Fatalf("journal contains %d effects, artifact has %d operations plus seed", len(trace.Effects), len(artifact.Operations))
	}
	if artifact.AuditRecordCount != uint64(len(trace.Records)) || artifact.AuditHeadHash != trace.Records[len(trace.Records)-1].Hash {
		t.Fatalf("artifact journal commitment does not match the verified trace")
	}
	for _, operation := range artifact.Operations {
		if operation.Tool == "bash" && (operation.CommandContainmentStatus != "not-established" || operation.CommandRunnerProfile != "direct-uncontained-fixture") {
			t.Fatalf("direct shell fixture is misreported as contained: %+v", operation)
		}
	}
	for _, effect := range trace.Effects {
		if effect.Unresolved || effect.Outcome != "success" {
			t.Fatalf("operation journal has unresolved/non-success outcome: %+v", effect)
		}
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	evidenceDir := os.Getenv("TBOUND_EVIDENCE_DIR")
	if evidenceDir == "" {
		evidenceDir = "artifacts"
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		t.Fatalf("create artifact directory: %v", err)
	}
	artifactPath := filepath.Join(evidenceDir, "sessionrepo-e2e-report.json")
	if err := os.WriteFile(artifactPath, encoded, 0o600); err != nil {
		t.Fatalf("write E2E evidence artifact: %v", err)
	}
	digest := sha256.Sum256(encoded)
	t.Logf("session repository E2E artifact: %s sha256:%s; audit journal: %s", artifactPath, hex.EncodeToString(digest[:]), journalPath)
	if err := store.Close(); err != nil {
		t.Fatalf("close store before recovery: %v", err)
	}
	recovered, err := Open(openFixtureRoot(t, store.rootPath), store.options)
	if err != nil {
		t.Fatalf("reopen from durable journal and manifests: %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	if _, err := recovered.Verify(); err != nil {
		t.Fatalf("verify reconstructed generation chain: %v", err)
	}
	recoveredEvidence, err := recovered.Evidence()
	if err != nil || len(recoveredEvidence.Generations) != 3 || len(recoveredEvidence.ApprovedDeltaLedger) != 2 || len(recoveredEvidence.Operations) != len(artifact.Operations) {
		t.Fatalf("recovered evidence is incomplete: evidence=%+v err=%v", recoveredEvidence, err)
	}
}

func TestSessionRepositoryRejectsUnsupportedSeedAndSealedSourceSwapE2E(t *testing.T) {
	t.Run("reject ungranted read path", func(t *testing.T) {
		store, _, _, authority := newE2EStore(t)
		sourcePath := filepath.Join(t.TempDir(), "source")
		if err := os.Mkdir(sourcePath, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, filepath.Join(sourcePath, "task.txt"), "private\n", 0o644)
		g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
		if err != nil {
			t.Fatal(err)
		}
		operation, decision := proposal("read-only-task"), allow("read-only-task")
		authority.grant(OperationRequest{Tool: "read", Operation: operation, Decision: decision, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: readArgumentsDigest("task.txt", 1024)})
		if _, err := g0.Read(context.Background(), operation, decision, "other.txt", 1024); !errors.Is(err, ErrDenied) {
			t.Fatalf("read outside trusted receipt returned %v, want denial", err)
		}
	})

	t.Run("reject symlink seed", func(t *testing.T) {
		store, _, _, _ := newE2EStore(t)
		sourcePath := filepath.Join(t.TempDir(), "source")
		if err := os.Mkdir(sourcePath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("missing", filepath.Join(sourcePath, "link")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true}); err == nil {
			t.Fatal("symlink-containing source was promoted to g0")
		}
	})

	t.Run("reject sealed source replacement", func(t *testing.T) {
		store, _, _, authority := newE2EStore(t)
		sourcePath := filepath.Join(t.TempDir(), "source")
		if err := os.Mkdir(sourcePath, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, filepath.Join(sourcePath, "task.txt"), "original\n", 0o644)
		g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
		if err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, filepath.Join(g0.testPath(), "task.txt"), "swapped\n", 0o644)
		operation, decision := proposal("read-after-swap"), allow("read-after-swap")
		authority.grant(OperationRequest{Tool: "read", Operation: operation, Decision: decision, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: readArgumentsDigest("task.txt", 1024)})
		if _, err := g0.Read(context.Background(), operation, decision, "task.txt", 1024); err == nil {
			t.Fatal("modified sealed generation was read or silently accepted")
		}
	})
}

func TestSessionRepositoryBindsReceiptToToolAndExactArgumentsE2E(t *testing.T) {
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

	operation, decision := proposal("same-result-different-action"), allow("same-result-different-action")
	edits := []Replacement{{OldText: "baseline", NewText: "approved"}}
	editDigest, err := argumentsDigest(struct {
		Path  string        `json:"path"`
		Edits []Replacement `json:"edits"`
	}{"README.md", edits})
	if err != nil {
		t.Fatal(err)
	}
	editRequest := OperationRequest{
		Tool: "edit", Operation: operation, Decision: decision,
		InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: editDigest,
	}
	authority.grant(editRequest)
	expectedG1 := fixtureSnapshot(t, "g1", "approved text\n", "baseline result\n")

	// Deliberately grant a second receipt with the same decision ID and exact
	// output tree, but for a different tool and its different argument bytes.
	writeDigest, err := argumentsDigest(struct {
		Path          string `json:"path"`
		ContentDigest string `json:"content_digest"`
		ContentBytes  int    `json:"content_bytes"`
	}{"README.md", digestBytes([]byte("approved text\n")), len("approved text\n")})
	if err != nil {
		t.Fatal(err)
	}
	writeReceipt := editRequest
	writeReceipt.Tool = "write"
	writeReceipt.ArgumentDigest = writeDigest
	authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expectedG1, writeReceipt, 1))

	if _, err := store.Edit(context.Background(), g0, operation, decision, "README.md", edits); !errors.Is(err, ErrAudit) {
		t.Fatalf("edit accepted a receipt for a different tool/argument digest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.testPath(), "generations", "g1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched receipt promoted g1: stat err=%v", err)
	}
	rootPath, options := store.rootPath, store.options
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(openFixtureRoot(t, rootPath), options); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("reopen after unresolved mismatched action returned %v, want quarantine", err)
	}
}

func TestSessionRepositoryRejectsMismatchedBashContextBeforeRunnerE2E(t *testing.T) {
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(sourcePath, "task.txt"), "baseline\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}
	operation, decision := lease("mismatched-bash-context"), allow("mismatched-bash-context")
	spec := CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "true"}}
	actualViewID := reserveTestViewID(t, store)
	wrongViewID := "view-ffffffffffffffffffffffffffffffffffffffffffffffff"
	wrongContext, err := commandExecutionContextDigest(wrongViewID, g0.TreeDigest(), operation.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	argumentDigest, err := commandArgumentsDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	authority.grant(OperationRequest{Tool: "bash", Operation: operation, Decision: decision,
		InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: argumentDigest,
		ViewID: wrongViewID, ExecutionContextDigest: wrongContext})
	var runnerCalled bool
	_, err = store.RunBash(context.Background(), g0, operation, decision, spec, CommandRunnerFunc(
		func(context.Context, *CommandView, CommandSpec) (CommandResult, CommandSettlement, error) {
			runnerCalled = true
			return CommandResult{}, CommandSettlement{}, nil
		},
	))
	if !errors.Is(err, ErrDenied) || runnerCalled {
		t.Fatalf("mismatched Bash context reached the runner: runnerCalled=%t err=%v", runnerCalled, err)
	}
	if len(store.generations) != 1 || store.generations[0] != g0 {
		t.Fatalf("mismatched Bash context advanced the head: generations=%d", len(store.generations))
	}

	rootPath, options := store.rootPath, store.options
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(openFixtureRoot(t, rootPath), options)
	if err != nil {
		t.Fatalf("reopen after denied Bash context: %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	recovered.mu.Lock()
	collisionPending := true
	recovered.viewIDGenerator = func() (string, error) {
		if collisionPending {
			collisionPending = false
			return actualViewID, nil
		}
		return newRandomViewID()
	}
	view, viewErr := recovered.newViewLocked(recovered.generations[len(recovered.generations)-1])
	if viewErr == nil {
		if collisionPending || view.ID() == actualViewID {
			viewErr = errors.New("reopened repository reused a denied command-view ID")
		} else {
			viewErr = recovered.discardViewLocked(view)
		}
	}
	recovered.mu.Unlock()
	if viewErr != nil {
		t.Fatalf("denied view ID was not retained across reopen: %v", viewErr)
	}
}

func TestSessionRepositorySnapshotsAuthorizedEditAndBashArgumentsE2E(t *testing.T) {
	t.Run("edit replacement slice", func(t *testing.T) {
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
		operation, decision := proposal("snapshot-edit-input"), allow("snapshot-edit-input")
		edits := []Replacement{{OldText: "baseline", NewText: "approved"}}
		editDigest, _ := argumentsDigest(struct {
			Path  string        `json:"path"`
			Edits []Replacement `json:"edits"`
		}{"README.md", edits})
		request := OperationRequest{Tool: "edit", Operation: operation, Decision: decision, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: editDigest}
		authority.grant(request)
		expected := fixtureSnapshot(t, "g1", "approved text\n", "baseline result\n")
		authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expected, request, 1))
		priorAuthorize := store.options.AuthorizeOperation
		store.options.AuthorizeOperation = func(actual OperationRequest) error {
			if err := priorAuthorize(actual); err != nil {
				return err
			}
			edits[0] = Replacement{OldText: "baseline", NewText: "tampered"}
			return nil
		}
		result, err := store.Edit(context.Background(), g0, operation, decision, "README.md", edits)
		if err != nil {
			t.Fatalf("edit changed after receipt authorization: %v", err)
		}
		content, err := os.ReadFile(filepath.Join(result.Generation.testPath(), "README.md"))
		if err != nil || string(content) != "approved text\n" {
			t.Fatalf("edit did not apply the authorized replacement: content=%q err=%v", content, err)
		}
	})

	t.Run("Bash argument slice and recovery", func(t *testing.T) {
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
		operation, decision := lease("snapshot-bash-input"), allow("snapshot-bash-input")
		commandSpec := CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "printf 'command output\\n' > build/result.txt"}}
		authorizedSpec := cloneCommandSpec(commandSpec)
		viewID := reserveTestViewID(t, store)
		argumentDigest, err := commandArgumentsDigest(authorizedSpec)
		if err != nil {
			t.Fatal(err)
		}
		contextDigest, err := commandExecutionContextDigest(viewID, g0.TreeDigest(), operation.LeaseID)
		if err != nil {
			t.Fatal(err)
		}
		request := OperationRequest{Tool: "bash", Operation: operation, Decision: decision, InputGeneration: g0.ID(),
			InputTreeDigest: g0.TreeDigest(), ArgumentDigest: argumentDigest, ViewID: viewID, ExecutionContextDigest: contextDigest}
		authority.grant(request)
		expected := fixtureSnapshot(t, "g1", "baseline text\n", "command output\n")
		authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expected, request, 1))
		priorAuthorize := store.options.AuthorizeOperation
		store.options.AuthorizeOperation = func(actual OperationRequest) error {
			if err := priorAuthorize(actual); err != nil {
				return err
			}
			commandSpec.Args[1] = "printf 'tampered\\n' > build/result.txt"
			return nil
		}
		var runnerSawAuthorizedSpec bool
		result, err := store.RunBash(context.Background(), g0, operation, decision,
			commandSpec,
			CommandRunnerFunc(func(ctx context.Context, view *CommandView, spec CommandSpec) (CommandResult, CommandSettlement, error) {
				runnerSawAuthorizedSpec = sameCommandSpec(spec, CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "printf 'command output\\n' > build/result.txt"}})
				cmd := exec.CommandContext(ctx, spec.Executable, spec.Args...)
				cmd.Dir = view.testPath()
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil {
					return CommandResult{}, CommandSettlement{}, err
				}
				settlement := CommandSettlement{LeaseID: operation.LeaseID, ViewID: view.ID(), ProcessScopeEmpty: true,
					WritersStopped: true, MountDetached: true, ExitObserved: true, ExitCode: 0, EvidenceClass: "synthetic-test-fixture"}
				return CommandResult{ExitCode: 0, ExitObserved: true, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, settlement, nil
			}))
		if err != nil || !runnerSawAuthorizedSpec {
			t.Fatalf("Bash did not run the authorized argument snapshot: result=%+v err=%v", result, err)
		}
		content, err := os.ReadFile(filepath.Join(result.Generation.testPath(), "build", "result.txt"))
		if err != nil || string(content) != "command output\n" {
			t.Fatalf("Bash generation does not contain authorized output: content=%q err=%v", content, err)
		}
		rootPath, options := store.rootPath, store.options
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		recovered, err := Open(openFixtureRoot(t, rootPath), options)
		if err != nil {
			t.Fatalf("reopen could not revalidate the durable Bash command spec: %v", err)
		}
		t.Cleanup(func() { _ = recovered.Close() })
		if chain, err := recovered.Verify(); err != nil || chain.SealedGeneration != "g1" {
			t.Fatalf("recovered Bash generation = %+v, err=%v", chain, err)
		}
		recovered.mu.Lock()
		collisionPending := true
		recovered.viewIDGenerator = func() (string, error) {
			if collisionPending {
				collisionPending = false
				return viewID, nil
			}
			return newRandomViewID()
		}
		nextView, viewErr := recovered.newViewLocked(recovered.generations[len(recovered.generations)-1])
		if viewErr == nil {
			if collisionPending {
				viewErr = errors.New("reopened repository did not retry a historical command-view ID")
			} else if nextView.ID() == viewID {
				viewErr = errors.New("reopened repository reused a committed command-view ID")
			} else if discardErr := recovered.discardViewLocked(nextView); discardErr != nil {
				viewErr = discardErr
			}
		}
		recovered.mu.Unlock()
		if viewErr != nil {
			t.Fatalf("allocate unique command view after reopening: %v", viewErr)
		}
	})
}

func TestSessionRepositoryRecoversCommittedPrefixAndQuarantinesOrphanViewE2E(t *testing.T) {
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
	oldState, exists, err := readRepositoryState(store.root)
	if err != nil || !exists {
		t.Fatalf("read baseline state: exists=%t err=%v", exists, err)
	}
	oldStateBytes, err := json.Marshal(oldState)
	if err != nil {
		t.Fatal(err)
	}
	operation, decision := proposal("recovery-edit"), allow("recovery-edit")
	edits := []Replacement{{OldText: "baseline", NewText: "approved"}}
	argumentDigest, _ := argumentsDigest(struct {
		Path  string        `json:"path"`
		Edits []Replacement `json:"edits"`
	}{"README.md", edits})
	request := OperationRequest{Tool: "edit", Operation: operation, Decision: decision, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: argumentDigest}
	authority.grant(request)
	expectedG1 := fixtureSnapshot(t, "g1", "approved text\n", "baseline result\n")
	authority.grantBinding(decision.ID, exactBinding(g0.snapshot, expectedG1, request, 1))
	if _, err := store.Edit(context.Background(), g0, operation, decision, "README.md", edits); err != nil {
		t.Fatal(err)
	}
	// Model a crash after the audit terminal record but before updating the
	// state cache/head mirror: the old g0 prefix remains valid and recoverable.
	if err := atomicWriteRepositoryState(store.root, oldStateBytes); err != nil {
		t.Fatal(err)
	}
	rootPath := store.rootPath
	options := store.options
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(openFixtureRoot(t, rootPath), options)
	if err != nil {
		t.Fatalf("recover committed effect beyond cached head: %v", err)
	}
	if result, err := recovered.Verify(); err != nil || result.SealedGeneration != "g1" {
		t.Fatalf("recovered chain = %+v, err=%v", result, err)
	}
	recovered.mu.Lock()
	_, err = recovered.newViewLocked(recovered.generations[len(recovered.generations)-1])
	recovered.mu.Unlock()
	if err != nil {
		t.Fatalf("create simulated pre-intent orphan view: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(openFixtureRoot(t, rootPath), options); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("reopen with orphan writable view error = %v, want quarantine", err)
	}
}

func TestSessionRepositoryUnknownEffectDoesNotAdvanceHeadE2E(t *testing.T) {
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(sourcePath, "task.txt"), "unchanged\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}
	operation, decision := proposal("unknown-edit"), allow("unknown-edit")
	edits := []Replacement{{OldText: "missing text", NewText: "replacement"}}
	argumentDigest, _ := argumentsDigest(struct {
		Path  string        `json:"path"`
		Edits []Replacement `json:"edits"`
	}{"task.txt", edits})
	authority.grant(OperationRequest{Tool: "edit", Operation: operation, Decision: decision, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: argumentDigest})
	if _, err := store.Edit(context.Background(), g0, operation, decision, "task.txt", edits); !errors.Is(err, ErrAudit) {
		t.Fatalf("failed edit returned %v, want unknown audited effect", err)
	}
	if len(store.generations) != 1 || store.generations[0] != g0 || len(store.transitions) != 0 {
		t.Fatalf("unknown edit advanced the in-memory head: generations=%d transitions=%d", len(store.generations), len(store.transitions))
	}
	if _, err := store.Verify(); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("store verification after unknown outcome = %v, want quarantine", err)
	}
	rootPath, options := store.rootPath, store.options
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(openFixtureRoot(t, rootPath), options); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("reopen with unknown audit outcome = %v, want quarantine", err)
	}
}

func privateTestBase(t *testing.T) string {
	t.Helper()
	candidates := make([]string, 0, 2)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, home)
	}
	if tmp := os.TempDir(); tmp != "" {
		candidates = append(candidates, tmp)
	}
	for _, base := range candidates {
		if !ancestorsArePrivate(base) {
			continue
		}
		directory, err := os.MkdirTemp(base, ".sessionrepo-test-")
		if err != nil {
			continue
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			_ = os.RemoveAll(directory)
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(directory) })
		return directory
	}
	t.Skip("no private base directory with a trusted ancestor chain is available")
	return ""
}

// ancestorsArePrivate mirrors audit.checkTrustedAncestor: no component of the
// path may be group- or other-writable. This keeps the audit journal out of
// world-writable trees such as a sticky /tmp.
func ancestorsArePrivate(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for {
		info, err := os.Lstat(absolute)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
			return false
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return true
		}
		absolute = parent
	}
}

func newE2EStore(t *testing.T) (*Store, *audit.Journal, string, *e2eReceiptAuthority) {
	t.Helper()
	base := privateTestBase(t)
	storePath := filepath.Join(base, "repository")
	auditPath := filepath.Join(base, "audit")
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(auditPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root := openFixtureRoot(t, storePath)
	journal, err := audit.Open(filepath.Join(auditPath, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	authority := &e2eReceiptAuthority{requests: make(map[string]OperationRequest), bindings: make(map[string]delta.TransitionBinding)}
	options := Options{
		PolicyDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Limits:               workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{
			ProfileDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			Complete:      true,
		},
		Journal:            journal,
		AuthorizeOperation: authority.authorize,
		VerifyDecision:     authority.verifyDecision,
		VerifySettlement: func(settlement CommandSettlement, viewID, leaseID string) error {
			if settlement.ViewID != viewID || settlement.LeaseID != leaseID || !settlement.ProcessScopeEmpty || !settlement.WritersStopped || !settlement.MountDetached || !settlement.ExitObserved || settlement.EvidenceClass != "synthetic-test-fixture" {
				return fmt.Errorf("command view does not have a complete settlement receipt")
			}
			return nil
		},
	}
	store, err := Create(root, options)
	if err != nil {
		_ = journal.Close()
		t.Fatalf("create session repository: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = journal.Close()
	})
	return store, journal, filepath.Join(auditPath, "journal.jsonl"), authority
}

// reserveTestViewID installs a per-store one-shot ID so the test can build an
// exact receipt over the same randomly minted view ID that RunBash will use.
func reserveTestViewID(t *testing.T, store *Store) string {
	t.Helper()
	id, err := newRandomViewID()
	if err != nil {
		t.Fatal(err)
	}
	used := false
	store.viewIDGenerator = func() (string, error) {
		if used {
			return newRandomViewID()
		}
		used = true
		return id, nil
	}
	return id
}

type e2eReceiptAuthority struct {
	mu       sync.Mutex
	requests map[string]OperationRequest
	bindings map[string]delta.TransitionBinding
}

func (authority *e2eReceiptAuthority) grant(request OperationRequest) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.requests[request.Decision.ID] = request
}

func (authority *e2eReceiptAuthority) grantBinding(decisionID string, binding delta.TransitionBinding) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.bindings[decisionID] = binding
}

func (authority *e2eReceiptAuthority) authorize(request OperationRequest) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	expected, ok := authority.requests[request.Decision.ID]
	if !ok || !validSupervisorEffectID(request.EffectID) || expected.Tool != request.Tool ||
		expected.Operation != request.Operation || !samePolicyDecision(expected.Decision, request.Decision) ||
		expected.InputGeneration != request.InputGeneration || expected.InputTreeDigest != request.InputTreeDigest ||
		expected.ArgumentDigest != request.ArgumentDigest || expected.ViewID != request.ViewID ||
		expected.ExecutionContextDigest != request.ExecutionContextDigest {
		return fmt.Errorf("no trusted test receipt exactly covers this request")
	}
	return nil
}

func (authority *e2eReceiptAuthority) verifyDecision(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	expected, ok := authority.bindings[decision.ID]
	request, requestOK := authority.requests[decision.ID]
	if !ok || decision.Outcome != delta.PolicyAllow || decision.PolicyDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		decision.MetadataPolicyDigest != "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" ||
		expected.ID != binding.ID || expected.Sequence != binding.Sequence || expected.Tool != binding.Tool ||
		expected.ArgumentDigest != binding.ArgumentDigest || expected.ViewID != binding.ViewID ||
		expected.ExecutionContextDigest != binding.ExecutionContextDigest || expected.InputGeneration != binding.InputGeneration ||
		expected.OutputGeneration != binding.OutputGeneration || expected.InputTreeDigest != binding.InputTreeDigest ||
		expected.OutputTreeDigest != binding.OutputTreeDigest || expected.Operation != binding.Operation || !sameChanges(expected.Changes, binding.Changes) ||
		!requestOK || request.Tool != binding.Tool || request.ArgumentDigest != binding.ArgumentDigest ||
		request.ViewID != binding.ViewID || request.ExecutionContextDigest != binding.ExecutionContextDigest ||
		request.Operation != binding.Operation || request.InputGeneration != binding.InputGeneration ||
		request.InputTreeDigest != binding.InputTreeDigest || !samePolicyDecision(request.Decision, decision) {
		return fmt.Errorf("trusted test receipt does not cover the exact transition binding")
	}
	return nil
}

func readArgumentsDigest(path string, maxBytes int64) string {
	digest, _ := argumentsDigest(struct {
		Path     string `json:"path"`
		MaxBytes int64  `json:"max_bytes"`
	}{path, maxBytes})
	return digest
}

func fixtureSnapshot(t *testing.T, generation, readme, result string) workspace.Snapshot {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "expected")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureDir(t, filepath.Join(rootPath, "build"), 0o755)
	writeFixtureFile(t, filepath.Join(rootPath, "README.md"), readme, 0o644)
	writeFixtureFile(t, filepath.Join(rootPath, "build", "result.txt"), result, 0o644)
	root := openFixtureRoot(t, rootPath)
	options := workspace.Options{
		Generation: generation, MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{
			ProfileDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Complete: true,
		},
	}
	snapshot, err := workspace.Scan(root, options)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func exactBinding(input, output workspace.Snapshot, request OperationRequest, sequence uint64) delta.TransitionBinding {
	return delta.TransitionBinding{
		ID: fmt.Sprintf("transition-%06d", sequence), Sequence: sequence,
		Tool: request.Tool, ArgumentDigest: request.ArgumentDigest,
		ViewID: request.ViewID, ExecutionContextDigest: request.ExecutionContextDigest,
		InputGeneration: input.Manifest.Generation, OutputGeneration: output.Manifest.Generation,
		InputTreeDigest: input.TreeDigest, OutputTreeDigest: output.TreeDigest,
		Operation: request.Operation, Changes: diffManifests(input.Manifest, output.Manifest),
	}
}

func proposal(id string) delta.OperationIdentity {
	return delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: "proposal-" + id, CallIssuer: "e2e-broker", CallID: "call-" + id}
}

func lease(id string) delta.OperationIdentity {
	return delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: "lease-" + id}
}

func allow(id string) delta.PolicyDecision {
	return delta.PolicyDecision{
		ID: "decision-" + id, Outcome: delta.PolicyAllow,
		PolicyDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetadataPolicyDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}

func openFixtureRoot(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func writeFixtureFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile applies the process umask; force the exact fixture mode so
	// the canonical-mode import contract is exercised independently of umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func writeFixtureDir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
