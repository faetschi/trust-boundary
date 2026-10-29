//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

// timeoutObservingRunner stands in for a command that runs until its context ends. It records the deadline its
// context carried and whether the context stopped it. The fallback timer keeps a missing deadline from hanging the test.
type timeoutObservingRunner struct {
	profile     string
	hasDeadline bool
	deadline    time.Time
	stoppedBy   string
}

func (r *timeoutObservingRunner) Profile() string { return r.profile }

func (r *timeoutObservingRunner) Run(ctx context.Context, _ *sessionrepo.CommandView, _ sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	r.deadline, r.hasDeadline = ctx.Deadline()
	select {
	case <-ctx.Done():
		r.stoppedBy = "context"
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ctx.Err()
	case <-time.After(5 * time.Second):
		r.stoppedBy = "fallback"
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("command ran past the fallback bound")
	}
}

// TestDurableBashStopsACommandAtItsRequestedTimeout proves the requested timeout reaches the trusted runner as the
// command's deadline. The runner stops the command at that deadline, and the durable executor releases nothing and
// moves no tip for a command it could not settle.
func TestDurableBashStopsACommandAtItsRequestedTimeout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	store, _, _, g0 := durableBashFixture(t)
	proposal, decision := durableBashProposal(`{"command":"sleep 30","timeout":0.2}`)
	runner := &timeoutObservingRunner{profile: durableBashRunnerLabel}
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: runner, BashOutputPaths: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	output, err := executor.Execute(context.Background(), proposal, decision)
	if runner.stoppedBy != "context" {
		t.Fatalf("the requested 0.2 second timeout did not reach the trusted runner: stopped by %q after %v",
			runner.stoppedBy, time.Since(started))
	}
	if !runner.hasDeadline || runner.deadline.Sub(started) > 2*time.Second {
		t.Fatalf("the runner context deadline is %v after the start; want the requested 0.2 seconds", runner.deadline.Sub(started))
	}
	if err == nil || len(output) != 0 {
		t.Fatalf("a timed-out command released output %q with err=%v", output, err)
	}
	if tip := executor.Tip(); tip == nil || tip.ID() != "g0" || tip.TreeDigest() != g0.TreeDigest() {
		t.Fatalf("a timed-out command moved the tip: %+v", tip)
	}
}

// TestDurableBashCompletesACommandWithinItsRequestedTimeout proves a bounded command that finishes in time is settled
// and released like an unbounded one.
func TestDurableBashCompletesACommandWithinItsRequestedTimeout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	store, _, _, g0 := durableBashFixture(t)
	proposal, decision := durableBashProposal(`{"command":"printf ok","timeout":5}`)
	leaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	runner := durableConsistentBashRunner(leaseID)
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: runner, BashOutputPaths: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := executor.Execute(context.Background(), proposal, decision)
	if err != nil {
		t.Fatalf("a command within its requested timeout was refused: %v", err)
	}
	released := decodeDurableBashReleased(t, output)
	if !released.Command.ExitObserved || released.Command.Stdout != "bash output\n" {
		t.Fatalf("released command = %+v; want the observed exit and the settled stdout", released.Command)
	}
}

// TestBashArgumentDigestBindsTheRequestedTimeout proves the Store argument digest covers the requested timeout, because
// the timeout bounds the execution the Store authorizes. Replay recomputes the same digest from the same arguments.
func TestBashArgumentDigestBindsTheRequestedTimeout(t *testing.T) {
	digestOf := func(t *testing.T, arguments string) string {
		t.Helper()
		digest, err := sessionrepoArgumentDigest(protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion,
			ToolCallID: "call-timeout-digest", Tool: "bash", Arguments: json.RawMessage(arguments)}, []string{})
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	unbounded := digestOf(t, `{"command":"true"}`)
	bounded := digestOf(t, `{"command":"true","timeout":5}`)
	other := digestOf(t, `{"command":"true","timeout":6}`)
	if bounded == unbounded || bounded == other {
		t.Fatalf("the Store argument digest ignores the requested timeout: unbounded=%s bounded=%s other=%s", unbounded, bounded, other)
	}
	if digestOf(t, `{"command":"true","timeout":5}`) != bounded {
		t.Fatal("the bash argument digest is not stable across calls")
	}
}

// TestReplayGovernedTranscriptReplaysABashCallWithARequestedTimeout proves a bounded bash call releases and replays
// exactly like an unbounded one, with the timeout bound into the digest on both sides.
func TestReplayGovernedTranscriptReplaysABashCallWithARequestedTimeout(t *testing.T) {
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "replay-response-bash-timeout", CallID: "replay-call-bash-timeout", Tool: "bash",
			Arguments: json.RawMessage(`{"command":"printf 'timed\\n'","timeout":5}`)},
	}, true)
	executeGovernedReplayTurn(t, fixture, true)
	if released := releasedProviderBashOutput(t, fixture.providerJournal); released.Command.Stdout != "timed\n" {
		t.Fatalf("the model received stdout %q for a bounded command", released.Command.Stdout)
	}
	if _, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins); err != nil {
		t.Fatalf("governed replay refused the bounded bash call: %v", err)
	}
}
