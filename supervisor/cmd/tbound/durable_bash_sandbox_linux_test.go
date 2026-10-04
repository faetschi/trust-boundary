//go:build linux

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/executor"
	"tbound/supervisor/internal/sandbox"
	"tbound/supervisor/internal/workspace"
)

// durableBashSandboxProfile is the descriptive, non-claim-bearing runner label
// the real sandbox-backed executor.Runner must record in the durable evidence.
const durableBashSandboxProfile = "wsl-dev-sandbox-non-claim-bearing"

// TestDurableBashSandboxEndToEnd composes the REAL sandbox runner into the
// cmd/tbound durable bash path. A trusted ALLOW bash proposal becomes one
// sessionrepo command lease whose runner is executor.New(leaseID), which adapts
// sandbox.Launch into the sessionrepo.CommandRunner seam. The command mutates
// the private command view, so the promoted g1 differs from the seeded g0; the
// fixture authority binds the exact expected settled tree and verifies every
// postcondition. This proves the production wiring without changing the sandbox.
func TestDurableBashSandboxEndToEnd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable sandbox integration runs only on Linux")
	}
	requireDurableSandboxMechanisms(t)

	store, journal, authority, g0 := durableBashFixture(t)

	// The command writes a new regular file at the cell root, so the settled
	// generation is observably different from the seeded g0. umask 022 pins the
	// created file to the canonical non-executable 0644 the workspace accepts.
	command := "umask 022; printf 'sandboxed\\n' > created-by-bash.txt"
	rawArguments, err := json.Marshal(struct {
		Command string `json:"command"`
	}{command})
	if err != nil {
		t.Fatalf("encode bash arguments: %v", err)
	}
	proposal, decision := durableBashProposal(string(rawArguments))
	leaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil {
		t.Fatalf("derive bash lease identity: %v", err)
	}

	// Build the expected settled tree independently and bind it as the
	// authorized output for g0. RunBash then has to reproduce it exactly.
	expected := durableBashSandboxExpectedSnapshot(t)
	if expected.TreeDigest == g0.TreeDigest() {
		t.Fatalf("expected sandbox output equals the seeded g0; the command would not change the view")
	}
	authority.setBinding(g0.TreeDigest(), expected.TreeDigest)

	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: executor.New(leaseID),
	})
	if err != nil {
		t.Fatalf("construct durable executor: %v", err)
	}

	output, err := durable.Execute(context.Background(), proposal, decision)
	if err != nil {
		t.Fatalf("durable sandbox bash: %v", err)
	}
	if err := protocol.ValidateStrictJSON(output); err != nil {
		t.Fatalf("durable sandbox output is not strict JSON: %v", err)
	}
	var summary durableMutationSummaryPayload
	if err := json.Unmarshal(output, &summary); err != nil {
		t.Fatalf("decode durable sandbox output %q: %v", output, err)
	}
	if summary.Tool != "bash" || summary.Generation.ID != "g1" ||
		summary.Generation.TreeDigest != expected.TreeDigest ||
		summary.Transition.ID != "transition-000001" || summary.Transition.Sequence != 1 ||
		!strings.HasPrefix(summary.EffectID, "sessionrepo-effect-") || summary.Outcome != "success" {
		t.Fatalf("unexpected durable sandbox settlement summary: %+v", summary)
	}
	// The real command exited 0 and the sandbox never claims containment.
	if summary.Command == nil || summary.Command.ExitCode != 0 || !summary.Command.ExitObserved ||
		summary.Command.CommandContainmentStatus != "not-established" ||
		summary.Command.CommandRunnerProfile != durableBashSandboxProfile {
		t.Fatalf("unexpected durable sandbox command summary: %+v", summary.Command)
	}

	// The tip advanced to the exact tree the real sandbox produced, and the
	// durable chain verifies.
	if tip := durable.Tip(); tip == nil || tip.ID() != "g1" || tip.TreeDigest() != expected.TreeDigest {
		t.Fatalf("durable sandbox tip did not advance to the settled g1: %+v", tip)
	}
	chain, err := store.Verify()
	if err != nil || chain.TransitionCount != 1 || chain.BaselineGeneration != "g0" || chain.SealedGeneration != "g1" {
		t.Fatalf("durable sandbox chain = %+v, err=%v", chain, err)
	}

	// Exactly one resolved bash effect reached the journal.
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace durable sandbox journal: %v", err)
	}
	if effects := countBashEffects(t, trace); effects != 1 {
		t.Fatalf("journal has %d durable bash effects; want 1", effects)
	}
}

// durableBashSandboxExpectedSnapshot builds the seeded g0 tree plus the file the
// sandboxed command creates, then scans it with the same trusted profile RunBash
// uses to import the settled view. Its digest is bound as the authorized output.
func durableBashSandboxExpectedSnapshot(t *testing.T) workspace.Snapshot {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "expected-sandbox")
	durableMkdir(t, rootPath, 0o700)
	durableWriteFile(t, filepath.Join(rootPath, "task.txt"), "baseline\n", 0o644)
	durableMkdir(t, filepath.Join(rootPath, "build"), 0o755)
	durableWriteFile(t, filepath.Join(rootPath, "build", "result.txt"), "baseline result\n", 0o644)
	durableWriteFile(t, filepath.Join(rootPath, "created-by-bash.txt"), "sandboxed\n", 0o644)
	root := durableOpenRoot(t, rootPath)
	snapshot, err := workspace.Scan(root, workspace.Options{
		Generation: "g1", MetadataPolicyDigest: durableMetadataDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
	})
	if err != nil {
		t.Fatalf("scan expected sandbox output fixture: %v", err)
	}
	return snapshot
}

// requireDurableSandboxMechanisms skips cleanly when sandbox.Probe() reports a
// mechanism sandbox.Launch requires is unavailable in this environment, instead
// of failing falsely. The required set mirrors the probe's own admission check;
// it never weakens the sandbox or masks a real result.
func requireDurableSandboxMechanisms(t *testing.T) {
	t.Helper()
	report, err := sandbox.Probe()
	if err != nil {
		t.Skipf("sandbox probe failed in this environment: %v", err)
	}
	var missing []string
	if report.LandlockABI < 1 {
		missing = append(missing, "landlock-abi1")
	}
	if !report.SeccompBPF {
		missing = append(missing, "seccomp-bpf")
	}
	if !report.UserNamespaces {
		missing = append(missing, "user-namespace")
	}
	if !report.MountNamespace {
		missing = append(missing, "mount-namespace")
	}
	if !report.PidNamespace {
		missing = append(missing, "pid-namespace")
	}
	if !report.NetworkNamespace {
		missing = append(missing, "network-namespace")
	}
	if !report.Loopback {
		missing = append(missing, "loopback-interface")
	}
	if !report.NoNewPrivs {
		missing = append(missing, "no-new-privs")
	}
	if !report.CapabilitiesEmpty {
		missing = append(missing, "empty-capabilities")
	}
	if len(missing) > 0 {
		t.Skipf("sandbox mechanisms unavailable in this environment: %s", strings.Join(missing, ","))
	}
}
