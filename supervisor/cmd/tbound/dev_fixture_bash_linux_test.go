//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// devFixtureRunnerStore seeds a private session repository whose authority
// registers the Bash lease with the bounded development runner and verifies the
// runner's measured settlement. It is the same composition the explicit
// `serve --pi --dev-fixture` route uses, reduced to one Bash step.
func devFixtureRunnerStore(t *testing.T, commands devFixtureRunner) (*sessionrepo.Store, *sessionrepo.Generation) {
	t.Helper()
	base := durablePrivateBase(t)
	storePath := filepath.Join(base, "repository")
	sourcePath := filepath.Join(base, "source")
	durableMkdir(t, storePath, 0o700)
	durableMkdir(t, sourcePath, 0o700)
	durableWriteFile(t, filepath.Join(sourcePath, "task.txt"), "old\n", 0o644)
	durableMkdir(t, filepath.Join(sourcePath, "build"), 0o755)

	journal, err := audit.Open(filepath.Join(base, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	authority := developmentFixtureAuthority{commands: commands}
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), sessionrepo.Options{
		PolicyDigest: devFixtureStorePolicy, MetadataPolicyDigest: devFixtureMetadataPolicy,
		Limits:          workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: devFixtureXattrPolicy, Complete: true},
		Journal:         journal, AuthorizeOperation: authority.authorize,
		VerifyDecision: authority.verifyDecision, VerifySettlement: authority.verifySettlement,
	})
	if err != nil {
		_ = journal.Close()
		t.Fatalf("create session repository: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = journal.Close()
	})
	g0, err := store.Seed(durableOpenRoot(t, sourcePath), sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	return store, g0
}

// TestDevFixtureCommandRunnerSettlesDurableBash composes the bounded fallback
// development runner into the real durable Bash lease path. The command mutates
// the private command view, the store promotes a distinct g1, and the runner
// reports a measured, non-establishing settlement whose profile is the bounded
// development label.
func TestDevFixtureCommandRunnerSettlesDurableBash(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("development command runner runs only on Linux")
	}
	commands := newBoundedDevelopmentFixtureCommandRunner()
	store, g0 := devFixtureRunnerStore(t, commands)

	operation := delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: "lease-dev-fixture-test"}
	decision := delta.PolicyDecision{
		ID: "decision-dev-fixture-test", Outcome: delta.PolicyAllow,
		PolicyDigest: devFixtureStorePolicy, MetadataPolicyDigest: devFixtureMetadataPolicy,
	}
	spec := sessionrepo.CommandSpec{
		Executable: "/bin/bash",
		Args:       []string{"-lc", "umask 022; printf bash > task.txt"},
	}
	mutation, err := store.RunBash(context.Background(), g0, operation, decision, spec, commands)
	if err != nil {
		t.Fatalf("durable development bash: %v", err)
	}
	if mutation.Generation == nil || mutation.Generation.ID() != "g1" || mutation.Transition.ID == "" ||
		mutation.EffectID == "" || mutation.Outcome != "success" {
		t.Fatalf("unexpected durable development bash result: %+v", mutation)
	}
	if mutation.Generation.TreeDigest() == g0.TreeDigest() {
		t.Fatalf("development bash did not change the command view (tree digest unchanged)")
	}
	if mutation.Command == nil || !mutation.Command.ExitObserved || mutation.Command.ExitCode != 0 {
		t.Fatalf("development bash command summary = %+v, want observed exit 0", mutation.Command)
	}
	if tip := commands.Profile(); tip != devFixtureCommandProfile {
		t.Fatalf("development runner profile = %q, want %q", tip, devFixtureCommandProfile)
	}

	// The settled generation really holds the command's output.
	source, err := mutation.Generation.ReadOnlyMountSource()
	if err != nil {
		t.Fatalf("mount settled generation: %v", err)
	}
	defer source.Close()
	content, err := os.ReadFile(fmt.Sprintf("/proc/self/fd/%d/task.txt", source.Fd()))
	if err != nil {
		t.Fatalf("read settled task.txt: %v", err)
	}
	if string(content) != "bash" {
		t.Fatalf("settled task.txt = %q, want %q", content, "bash")
	}
	chain, err := store.Verify()
	if err != nil || chain.TransitionCount != 1 || chain.SealedGeneration != "g1" {
		t.Fatalf("development bash chain = %+v, err=%v", chain, err)
	}
}

// TestDevFixtureCommandRunnerRejectsUnregisteredShape proves the runner fails
// closed on a command shape outside the registered `/bin/bash -lc` form and on a
// view it was never authorized for.
func TestDevFixtureCommandRunnerRejectsUnregisteredShape(t *testing.T) {
	commands := newBoundedDevelopmentFixtureCommandRunner()
	cases := []struct {
		name string
		spec sessionrepo.CommandSpec
	}{
		{"other executable", sessionrepo.CommandSpec{Executable: "/bin/sh", Args: []string{"-lc", "true"}}},
		{"other flag", sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "true"}}},
		{"empty command", sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "   "}}},
		{"wrong arity", sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// No command runs and the runner never fabricates a settlement.
			if _, settlement, err := commands.Run(context.Background(), nil, testCase.spec); err == nil {
				t.Fatalf("runner accepted an unregistered spec %+v (settlement=%+v)", testCase.spec, settlement)
			}
		})
	}
	if _, _, err := commands.Run(nil, nil, sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}}); err == nil {
		t.Fatal("runner accepted a nil context")
	}
}

func TestDevFixtureCommandWorkingDirRejectsEscape(t *testing.T) {
	for _, relative := range []string{"..", "../escape", "/etc", "a/../../b"} {
		if _, err := devFixtureCommandWorkingDir("/private/view", relative); err == nil {
			t.Fatalf("working directory %q escaped the private view", relative)
		}
	}
	if dir, err := devFixtureCommandWorkingDir("/private/view", ""); err != nil || dir != "/private/view" {
		t.Fatalf("empty working directory = %q, %v", dir, err)
	}
	if dir, err := devFixtureCommandWorkingDir("/private/view", "build"); err != nil || dir != "/private/view/build" {
		t.Fatalf("relative working directory = %q, %v", dir, err)
	}
}

func TestDevFixtureBoundedBufferCapsOutput(t *testing.T) {
	buffer := &devFixtureBoundedBuffer{max: 4}
	if written, err := buffer.Write([]byte("abcdef")); err != nil || written != 6 {
		t.Fatalf("bounded write = %d, %v", written, err)
	}
	if !buffer.truncated {
		t.Fatal("bounded buffer did not flag truncation")
	}
	if got := string(buffer.Bytes()); got != "abcd" {
		t.Fatalf("bounded buffer = %q, want %q", got, "abcd")
	}
	plain := &devFixtureBoundedBuffer{max: 4}
	if _, err := plain.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if plain.truncated || !strings.HasPrefix(string(plain.Bytes()), "ab") {
		t.Fatalf("under-cap buffer = %q truncated=%v", plain.Bytes(), plain.truncated)
	}
}
