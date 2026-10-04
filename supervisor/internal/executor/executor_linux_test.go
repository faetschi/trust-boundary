//go:build linux

package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/sandbox"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	policyDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	metadataDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	xattrDigest    = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestProfileIsDevWSLNonClaimBearing(t *testing.T) {
	runner := New("lease-profile")
	if runner.Profile() == "direct-uncontained-fixture" {
		t.Fatalf("runner profile reuses the direct uncontained fixture label")
	}
	if !strings.Contains(runner.Profile(), "non-claim-bearing") || !strings.Contains(runner.Profile(), "wsl") {
		t.Fatalf("runner profile must be an explicit dev/WSL non-claim-bearing label: %q", runner.Profile())
	}
}

func TestMapOutcomeEchoesViewAndRealResult(t *testing.T) {
	result := sandbox.Result{ExitCode: 3, Stdout: []byte("out"), Stderr: []byte("err")}
	settlement := sandbox.Settlement{
		ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true,
		ExitObserved: true, ExitCode: 3,
		EvidenceClass: "dev-wsl-non-claim-bearing:landlock-abi1+seccomp-bpf+cgroup2=not-established",
	}
	commandResult, commandSettlement := mapOutcome(result, settlement, "view-0000", "lease-0000")
	if commandResult.ExitCode != 3 || !commandResult.ExitObserved || string(commandResult.Stdout) != "out" || string(commandResult.Stderr) != "err" {
		t.Fatalf("result mapping lost the real exit/output: %+v", commandResult)
	}
	if commandSettlement.ViewID != "view-0000" || commandSettlement.LeaseID != "lease-0000" {
		t.Fatalf("settlement did not echo view/lease: %+v", commandSettlement)
	}
	if commandSettlement.ExitCode != commandResult.ExitCode || !commandSettlement.ProcessScopeEmpty || !commandSettlement.WritersStopped || !commandSettlement.MountDetached {
		t.Fatalf("settlement mapping lost postconditions: %+v", commandSettlement)
	}
	if !IsNonClaimBearing(commandSettlement.EvidenceClass) {
		t.Fatalf("evidence class is not marked non-claim-bearing: %q", commandSettlement.EvidenceClass)
	}
}

// TestStoreRunBashIntegration exercises the real store.RunBash seam with the
// sandbox-backed runner. It proves the sealed source generation is unchanged,
// the command view is imported as a new generation, and the settlement is an
// honest non-claim-bearing receipt.
func TestStoreRunBashIntegration(t *testing.T) {
	base := privateBase(t)
	storePath := filepath.Join(base, "repository")
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(base, "audit")
	if err := os.Mkdir(auditPath, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(auditPath, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	root, err := os.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	options := sessionrepo.Options{
		PolicyDigest:         policyDigest,
		MetadataPolicyDigest: metadataDigest,
		Limits:               workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{
			ProfileDigest: xattrDigest,
			Complete:      true,
		},
		Journal: journal,
		AuthorizeOperation: func(sessionrepo.OperationRequest) error {
			return nil
		},
		VerifyDecision: func(delta.PolicyDecision, delta.TransitionBinding) error {
			return nil
		},
		VerifySettlement: func(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
			if settlement.ViewID != viewID || settlement.LeaseID != leaseID ||
				!settlement.ProcessScopeEmpty || !settlement.WritersStopped ||
				!settlement.MountDetached || !settlement.ExitObserved {
				return fmt.Errorf("incomplete settlement: %+v", settlement)
			}
			if !IsNonClaimBearing(settlement.EvidenceClass) {
				return fmt.Errorf("settlement is not an honest non-claim-bearing receipt: %q", settlement.EvidenceClass)
			}
			return nil
		},
	}
	store, err := sessionrepo.Create(root, options)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sourcePath := filepath.Join(base, "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(sourcePath, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(sourcePath, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
	writeFile(t, filepath.Join(sourcePath, "README.md"), "baseline text\n", 0o644)
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	if g0.ID() != "g0" {
		t.Fatalf("unexpected baseline generation %q", g0.ID())
	}

	lease := delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: "lease-executor-integration"}
	decision := delta.PolicyDecision{
		ID: "decision-executor-integration", Outcome: delta.PolicyAllow,
		PolicyDigest: policyDigest, MetadataPolicyDigest: metadataDigest,
	}
	spec := sessionrepo.CommandSpec{
		Executable: "/bin/bash",
		Args:       []string{"-c", "printf 'command output\\n' > build/result.txt && printf 'command completed\\n'"},
	}
	mutation, err := store.RunBash(context.Background(), g0, lease, decision, spec, New(lease.LeaseID))
	if err != nil {
		t.Fatalf("RunBash with sandbox runner: %v", err)
	}
	if mutation.Generation == nil || mutation.Generation.ID() != "g1" {
		t.Fatalf("command did not promote g1: %+v", mutation)
	}
	if mutation.Command == nil || mutation.Command.ExitCode != 0 || !mutation.Command.ExitObserved {
		t.Fatalf("command result not observed: %+v", mutation.Command)
	}
	if !strings.Contains(string(mutation.Command.Stdout), "command completed") {
		t.Fatalf("command stdout not captured: %q", mutation.Command.Stdout)
	}

	// The sealed baseline generation must be unchanged; only the imported view
	// generation carries the command output.
	if g0.ID() != "g0" || g0.TreeDigest() == "" {
		t.Fatalf("baseline generation lost identity: %+v", g0)
	}
	if chain, err := store.Verify(); err != nil || chain.SealedGeneration != "g1" || chain.TransitionCount != 1 {
		t.Fatalf("verify chain after sandbox command: result=%+v err=%v", chain, err)
	}
	evidence, err := store.Evidence()
	if err != nil {
		t.Fatalf("collect evidence: %v", err)
	}
	var bashOperation *sessionrepo.OperationEvidence
	for index := range evidence.Operations {
		if evidence.Operations[index].Tool == "bash" {
			bashOperation = &evidence.Operations[index]
		}
	}
	if bashOperation == nil {
		t.Fatalf("no bash operation recorded: %+v", evidence.Operations)
	}
	if bashOperation.CommandContainmentStatus != "not-established" {
		t.Fatalf("sandbox runner overstated containment: %+v", bashOperation)
	}
	if bashOperation.CommandRunnerProfile != profile {
		t.Fatalf("runner profile not recorded: %q", bashOperation.CommandRunnerProfile)
	}
	t.Logf("sandbox-backed RunBash promoted %s with runner %q and containment %q",
		mutation.Generation.ID(), bashOperation.CommandRunnerProfile, bashOperation.CommandContainmentStatus)
}

func privateBase(t *testing.T) string {
	t.Helper()
	candidates := []string{}
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
		directory, err := os.MkdirTemp(base, ".executor-test-")
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

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
