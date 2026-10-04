//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	durableBashCallID      = "call-durable-bash"
	durableBashRunnerLabel = "synthetic-durable-bash-runner"
)

// durableBashStubRunner is a synthetic CommandRunner. It never launches a real
// process: it returns a fixed, internally consistent command result and
// settlement so the durable Bash path can be exercised without containment.
// Profile makes the store record a descriptive, non-established runner label.
type durableBashStubRunner struct {
	profile           string
	leaseID           string
	exitCode          int
	exitObserved      bool
	processScopeEmpty bool
	writersStopped    bool
	mountDetached     bool
	stdout            string
	stderr            string

	calls    int
	lastSpec sessionrepo.CommandSpec
	lastView string
}

func (r *durableBashStubRunner) Profile() string { return r.profile }

func (r *durableBashStubRunner) Run(_ context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	r.calls++
	r.lastSpec = spec
	r.lastView = view.ID()
	result := sessionrepo.CommandResult{
		ExitCode: r.exitCode, ExitObserved: r.exitObserved,
		Stdout: []byte(r.stdout), Stderr: []byte(r.stderr),
	}
	settlement := sessionrepo.CommandSettlement{
		LeaseID: r.leaseID, ViewID: view.ID(),
		ProcessScopeEmpty: r.processScopeEmpty, WritersStopped: r.writersStopped,
		MountDetached: r.mountDetached, ExitObserved: r.exitObserved, ExitCode: r.exitCode,
		EvidenceClass: "synthetic-durable-bash-fixture",
	}
	return result, settlement, nil
}

// durableConsistentBashRunner returns a stub whose settlement satisfies every
// RunBash postcondition, including the exact lease ID the executor derives.
func durableConsistentBashRunner(leaseID string) *durableBashStubRunner {
	return &durableBashStubRunner{
		profile: durableBashRunnerLabel, leaseID: leaseID, exitCode: 7,
		exitObserved: true, processScopeEmpty: true, writersStopped: true, mountDetached: true,
		stdout: "bash output\n",
	}
}

// durableBashFixture seeds a private session repository whose fixture authority
// authorizes the bash lease slice and whose VerifySettlement accepts the stub's
// settlement. Nothing modifies the command view, so the settled output tree
// equals the seeded input tree.
func durableBashFixture(t *testing.T) (*sessionrepo.Store, *audit.Journal, *durableReceiptAuthority, *sessionrepo.Generation) {
	t.Helper()
	base := durablePrivateBase(t)
	storePath := filepath.Join(base, "repository")
	auditDir := filepath.Join(base, "audit")
	durableMkdir(t, storePath, 0o700)
	durableMkdir(t, auditDir, 0o700)
	sourcePath := filepath.Join(base, "source")
	durableMkdir(t, sourcePath, 0o700)
	durableWriteFile(t, filepath.Join(sourcePath, "task.txt"), "baseline\n", 0o644)
	durableMkdir(t, filepath.Join(sourcePath, "build"), 0o755)
	durableWriteFile(t, filepath.Join(sourcePath, "build", "result.txt"), "baseline result\n", 0o644)
	source := durableOpenRoot(t, sourcePath)

	journal, err := audit.Open(filepath.Join(auditDir, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open audit journal: %v", err)
	}
	authority := &durableReceiptAuthority{
		callIssuer: durableCallIssuer, callID: durableBashCallID,
		policyDigest: durablePolicyDigest, metadataPolicyDigest: durableMetadataDigest,
	}
	options := sessionrepo.Options{
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		Limits:             workspace.DefaultLimits(),
		XattrVisibility:    workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
		Journal:            journal,
		AuthorizeOperation: authority.authorize,
		VerifyDecision:     authority.verifyDecision,
		VerifySettlement:   authority.verifySettlement,
	}
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), options)
	if err != nil {
		_ = journal.Close()
		t.Fatalf("create session repository: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = journal.Close()
	})
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	authority.setBinding(g0.TreeDigest(), g0.TreeDigest())
	return store, journal, authority, g0
}

// durableBashProposal builds a trusted ALLOW bash decision over the raw adapter
// arguments. The executor derives the lease ID from this exact tuple.
func durableBashProposal(rawArguments string) (protocol.Proposal, gate.Decision) {
	proposal := protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    durableBashCallID, Tool: "bash",
		Arguments: json.RawMessage(rawArguments),
	}
	canonicalDigest, _ := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
	decision := gate.Decision{
		Verdict: gate.Allow, ReasonCode: "policy_rule_allow",
		PolicyDigest:             "tbound-policy/v1:sha256:" + strings.Repeat("4", 64),
		CanonicalArgumentsDigest: canonicalDigest,
		ToolCallID:               durableBashCallID, Tool: "bash", Sequence: 1,
		ResponseID: &correlation.Identifier{Issuer: durableResponseIssuer, Opaque: "response-durable-bash"},
	}
	return proposal, decision
}

// TestDurableBashDecisionPath proves a trusted ALLOW bash proposal becomes one
// durable command lease: the store journals the intent and outcome, imports the
// settled view as a new sealed generation, records honest containment evidence,
// and releases a strict-JSON summary carrying the exit code.
func TestDurableBashDecisionPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	store, journal, _, g0 := durableBashFixture(t)
	command := "printf bash-output"
	arguments := `{"command":"` + command + `","timeout":5}`
	proposal, decision := durableBashProposal(arguments)
	leaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	runner := durableConsistentBashRunner(leaseID)
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}

	output, err := executor.Execute(context.Background(), proposal, decision)
	if err != nil {
		t.Fatalf("durable bash: %v", err)
	}
	if err := protocol.ValidateStrictJSON(output); err != nil {
		t.Fatalf("durable bash output is not strict JSON: %v", err)
	}
	var summary durableMutationSummaryPayload
	if err := json.Unmarshal(output, &summary); err != nil {
		t.Fatalf("decode durable bash output %q: %v", output, err)
	}
	if summary.Tool != "bash" || summary.Generation.ID != "g1" ||
		summary.Generation.TreeDigest != g0.TreeDigest() ||
		summary.Transition.ID != "transition-000001" || summary.Transition.Sequence != 1 ||
		!strings.HasPrefix(summary.EffectID, "sessionrepo-effect-") || summary.Outcome != "success" {
		t.Fatalf("unexpected durable bash settlement summary: %+v", summary)
	}
	if summary.Command == nil || summary.Command.ExitCode != 7 || !summary.Command.ExitObserved ||
		summary.Command.CommandContainmentStatus != "not-established" ||
		summary.Command.CommandRunnerProfile != durableBashRunnerLabel {
		t.Fatalf("unexpected durable bash command summary: %+v", summary.Command)
	}

	// The trusted argv is exactly the adapter's command under a login shell.
	if runner.calls != 1 {
		t.Fatalf("durable bash ran the runner %d times; want 1", runner.calls)
	}
	wantSpec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", command}}
	if runner.lastSpec.Executable != wantSpec.Executable || len(runner.lastSpec.Args) != len(wantSpec.Args) ||
		runner.lastSpec.Args[0] != wantSpec.Args[0] || runner.lastSpec.Args[1] != wantSpec.Args[1] ||
		runner.lastSpec.WorkingDirectory != "" {
		t.Fatalf("durable bash argv = %+v, want %+v", runner.lastSpec, wantSpec)
	}
	if runner.lastView != "" && !strings.HasPrefix(runner.lastView, "view-") {
		t.Fatalf("durable bash runner saw an invalid command view %q", runner.lastView)
	}

	// The journal holds the durable intent and its resolved success outcome.
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace durable bash journal: %v", err)
	}
	if effects := countBashEffects(t, trace); effects != 1 {
		t.Fatalf("journal has %d durable bash effects; want 1", effects)
	}

	// The tip advanced from the committed mutation, and the chain verifies.
	if tip := executor.Tip(); tip == nil || tip.ID() != "g1" || tip.TreeDigest() != g0.TreeDigest() {
		t.Fatalf("durable bash tip did not advance to the settled g1: %+v", tip)
	}
	chain, err := store.Verify()
	if err != nil || chain.TransitionCount != 1 || chain.SealedGeneration != "g1" {
		t.Fatalf("durable bash chain = %+v, err=%v", chain, err)
	}

	// Evidence reports the exit code and never claims containment.
	artifact, err := store.Evidence()
	if err != nil {
		t.Fatalf("build store evidence: %v", err)
	}
	if artifact.CommandContainmentStatus != "not-established" {
		t.Fatalf("evidence overstates containment: %+v", artifact.CommandContainmentStatus)
	}
	if len(artifact.Operations) != 1 {
		t.Fatalf("evidence has %d operations; want 1", len(artifact.Operations))
	}
	bash := artifact.Operations[0]
	if bash.Tool != "bash" || bash.Outcome != "success" || bash.OutputGeneration != "g1" ||
		bash.ExitCode == nil || *bash.ExitCode != 7 ||
		bash.CommandContainmentStatus != "not-established" ||
		bash.CommandRunnerProfile != durableBashRunnerLabel {
		t.Fatalf("unexpected bash operation evidence: %+v", bash)
	}
}

// TestDurableBashRejectsInconsistentSettlement proves fail-closed behavior: a
// runner that does not attest a settled process scope aborts RunBash, withholds
// the result, leaves the tip unchanged, and quarantines the store.
func TestDurableBashRejectsInconsistentSettlement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	store, _, _, g0 := durableBashFixture(t)
	proposal, decision := durableBashProposal(`{"command":"true"}`)
	leaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	runner := durableConsistentBashRunner(leaseID)
	runner.processScopeEmpty = false
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = executor.Execute(context.Background(), proposal, decision)
	if !errors.Is(err, sessionrepo.ErrInvalidSettlement) {
		t.Fatalf("unsettled durable bash error = %v, want ErrInvalidSettlement", err)
	}
	if tip := executor.Tip(); tip == nil || tip.ID() != "g0" {
		t.Fatalf("unsettled durable bash advanced the tip: %+v", tip)
	}
	if _, err := store.Verify(); !errors.Is(err, sessionrepo.ErrQuarantined) {
		t.Fatalf("store after unsettled durable bash = %v, want quarantine", err)
	}
}

// TestDurableBashWithoutRunnerFailsClosed proves a bash ALLOW with no configured
// runner is withheld before the store is touched.
func TestDurableBashWithoutRunnerFailsClosed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	store, _, _, g0 := durableBashFixture(t)
	proposal, decision := durableBashProposal(`{"command":"true"}`)
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := executor.Execute(context.Background(), proposal, decision); err == nil {
		t.Fatal("durable bash ran without a configured command runner")
	}
	if tip := executor.Tip(); tip == nil || tip.ID() != "g0" {
		t.Fatalf("nil-runner durable bash advanced the tip: %+v", tip)
	}
	if chain, err := store.Verify(); err != nil || chain.TransitionCount != 0 || chain.SealedGeneration != "g0" {
		t.Fatalf("nil-runner durable bash mutated the store: chain=%+v err=%v", chain, err)
	}
}

// TestDurableBashRejectsMalformedArguments proves the adapter schema is enforced
// strictly: unknown fields, trailing JSON, an empty or missing command, and a
// non-numeric timeout all fail closed before any command can run.
func TestDurableBashRejectsMalformedArguments(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	store, _, _, g0 := durableBashFixture(t)
	runner := durableConsistentBashRunner("lease-unused")
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		arguments string
	}{
		{"unknown field", `{"command":"true","extra":1}`},
		{"trailing json", `{"command":"true"}{"command":"false"}`},
		{"empty command", `{"command":""}`},
		{"missing command", `{"timeout":5}`},
		{"non-number timeout", `{"command":"true","timeout":"5"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			proposal, decision := durableBashProposal(testCase.arguments)
			if _, err := executor.Execute(context.Background(), proposal, decision); err == nil {
				t.Fatalf("durable bash accepted malformed arguments %s", testCase.arguments)
			}
		})
	}
	if runner.calls != 0 {
		t.Fatalf("malformed durable bash reached the runner %d times; want 0", runner.calls)
	}
	if tip := executor.Tip(); tip == nil || tip.ID() != "g0" {
		t.Fatalf("malformed durable bash advanced the tip: %+v", tip)
	}
}

// countBashEffects counts the durable bash effects in a journal trace and
// requires each to be a resolved success.
func countBashEffects(t *testing.T, trace audit.Trace) int {
	t.Helper()
	count := 0
	for _, effect := range trace.Effects {
		if !strings.Contains(string(effect.Intent), `"tool":"bash"`) {
			continue
		}
		count++
		if effect.Unresolved || effect.Outcome != "success" || effect.IntentSequence == 0 || effect.OutcomeSequence == 0 {
			t.Fatalf("durable bash effect is not a resolved success: %+v", effect)
		}
	}
	return count
}
