//go:build linux

package main

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"tbound/supervisor/internal/providerbridge"
)

// TestDurableExecutorIgnoresExtraKeysInReadAndBash proves a key the published schema does not list changes neither the
// read nor the bash result the durable executor releases.
func TestDurableExecutorIgnoresExtraKeysInReadAndBash(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	store, _, _, g0 := e05Store(t, e05TaskBaseline)
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := executor.Execute(context.Background(),
		e05Proposal("call-e05-extra-read", "read", json.RawMessage(`{"path":"task.txt","extra":1}`)),
		e05ReadDecision("call-e05-extra-read", "response-e05-extra-read", 1))
	if err != nil {
		t.Fatalf("durable read refused a key the schema does not list: %v", err)
	}
	var read durableReadSummaryPayload
	if err := json.Unmarshal(output, &read); err != nil {
		t.Fatal(err)
	}
	if read.Path != e05TaskPath || read.Content != e05TaskBaseline {
		t.Fatalf("read with an extra key released path %q content %q; want %q and %q", read.Path, read.Content, e05TaskPath, e05TaskBaseline)
	}

	bashStore, _, _, bashG0 := durableBashFixture(t)
	proposal, decision := durableBashProposal(`{"command":"true","extra":{"nested":[1]}}`)
	leaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	bashExecutor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: bashStore, Tip: bashG0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: durableConsistentBashRunner(leaseID), BashOutputPaths: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	bashOutput, err := bashExecutor.Execute(context.Background(), proposal, decision)
	if err != nil {
		t.Fatalf("durable bash refused a key the schema does not list: %v", err)
	}
	if released := decodeDurableBashReleased(t, bashOutput); released.Command.Stdout != "bash output\n" {
		t.Fatalf("bash with an extra key released stdout %q; want the settled stdout", released.Command.Stdout)
	}
}

// TestReplayGovernedTranscriptIgnoresExtraKeysAcrossEveryTool runs read, write, edit and bash, each with keys the schemas
// do not list, including an extra key inside the edit. Each call executes as its plain form would, and replay accepts it.
func TestReplayGovernedTranscriptIgnoresExtraKeysAcrossEveryTool(t *testing.T) {
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "extra-response-read", CallID: "extra-call-read", Tool: "read",
			Arguments: json.RawMessage(`{"path":"task.txt","extra":1}`)},
		{ResponseID: "extra-response-write", CallID: "extra-call-write", Tool: "write",
			Arguments: json.RawMessage(`{"path":"out.txt","content":"extra write\n","extra":true}`)},
		{ResponseID: "extra-response-edit", CallID: "extra-call-edit", Tool: "edit",
			Arguments: json.RawMessage(`{"path":"out.txt","edits":[{"oldText":"extra","newText":"edited","extra":2}],"extra":"e"}`)},
		{ResponseID: "extra-response-bash", CallID: "extra-call-bash", Tool: "bash",
			Arguments: json.RawMessage(`{"command":"printf 'bash\\n'","timeout":5,"extra":null}`)},
	}, true)
	for turn := 0; turn < 4; turn++ {
		executeGovernedReplayTurn(t, fixture, true)
	}
	results := releasedProviderResults(t, fixture.providerJournal)
	if len(results) != 4 {
		t.Fatalf("provider journal released %d results; want 4", len(results))
	}
	var read durableReadSummaryPayload
	if err := decodeGovernedResult(results[0].Output, &read); err != nil {
		t.Fatalf("decode the read released with an extra key: %v", err)
	}
	if read.Content != "before\n" {
		t.Fatalf("read with an extra key released %q; want the fixture's before content", read.Content)
	}
	if released := releasedProviderBashOutput(t, fixture.providerJournal); released.Command.Stdout != "bash\n" {
		t.Fatalf("bash with an extra key released stdout %q; want %q", released.Command.Stdout, "bash\n")
	}
	if _, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins); err != nil {
		t.Fatalf("governed replay refused the transcript with extra keys: %v", err)
	}
}
