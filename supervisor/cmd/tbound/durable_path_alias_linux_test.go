//go:build linux

package main

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/providerbridge"
)

// TestDurableReadResolvesWorkspaceAliasesToTheCanonicalFile proves every model-visible spelling of a workspace file
// reads that file, and that the released summary names the canonical Store-relative path.
func TestDurableReadResolvesWorkspaceAliasesToTheCanonicalFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable read path runs only on Linux")
	}
	aliases := []string{
		"task.txt", "./task.txt", ".//task.txt", "/work/task.txt", "/work//./task.txt",
		"/var/lib/tbound/pi-worker/task.txt", "build/../task.txt",
	}
	for _, alias := range aliases {
		t.Run(alias, func(t *testing.T) {
			store, _, _, g0 := e05Store(t, e05TaskBaseline)
			executor, err := NewDurableExecutor(DurableExecutorConfig{
				Store: store, Tip: g0, CallIssuer: durableCallIssuer,
				PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
			})
			if err != nil {
				t.Fatal(err)
			}
			arguments, err := json.Marshal(map[string]string{"path": alias})
			if err != nil {
				t.Fatal(err)
			}
			output, err := executor.Execute(context.Background(), e05Proposal("call-e05-alias", "read", arguments),
				e05ReadDecision("call-e05-alias", "response-e05-alias", 1))
			if err != nil {
				t.Fatalf("durable read refused the workspace alias %q: %v", alias, err)
			}
			var summary durableReadSummaryPayload
			if err := json.Unmarshal(output, &summary); err != nil {
				t.Fatalf("decode released read of %q: %v", alias, err)
			}
			if summary.Path != e05TaskPath || summary.Content != e05TaskBaseline {
				t.Fatalf("read of %q released path %q and content %q; want %q and %q",
					alias, summary.Path, summary.Content, e05TaskPath, e05TaskBaseline)
			}
		})
	}
}

// releasedProviderResults returns every provider result the journal released, in order, after strict decoding.
func releasedProviderResults(t *testing.T, journal *audit.Journal) []protocol.Result {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var results []protocol.Result
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_tool_result" {
			continue
		}
		var evidence providerResultEvidence
		if err := unmarshalGovernedJSON(record.Event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		result, err := protocol.DecodeResult(evidence.Result)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
	}
	return results
}

// TestGovernedReplayAcceptsWorkspaceAliasesForEveryPathTool runs read, write, edit and read again through alias
// spellings. Each call must mutate or read the canonical file, and the whole transcript must replay exactly.
func TestGovernedReplayAcceptsWorkspaceAliasesForEveryPathTool(t *testing.T) {
	fixture := newGovernedReplayFixture(t, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "alias-response-read", CallID: "alias-call-read", Tool: "read",
			Arguments: json.RawMessage(`{"path":"./task.txt"}`)},
		{ResponseID: "alias-response-write", CallID: "alias-call-write", Tool: "write",
			Arguments: json.RawMessage(`{"path":"/work/./out.txt","content":"alias write\n"}`)},
		{ResponseID: "alias-response-edit", CallID: "alias-call-edit", Tool: "edit",
			Arguments: json.RawMessage(`{"path":"/var/lib/tbound/pi-worker//out.txt","edits":[{"oldText":"alias","newText":"edited"}]}`)},
		{ResponseID: "alias-response-reread", CallID: "alias-call-reread", Tool: "read",
			Arguments: json.RawMessage(`{"path":"build/../out.txt"}`)},
	}, true)
	for turn := 0; turn < 4; turn++ {
		executeGovernedReplayTurn(t, fixture, true)
	}
	results := releasedProviderResults(t, fixture.providerJournal)
	if len(results) != 4 {
		t.Fatalf("provider journal released %d results; want 4", len(results))
	}
	var first, last durableReadSummaryPayload
	if err := decodeGovernedResult(results[0].Output, &first); err != nil {
		t.Fatalf("decode the first read: %v", err)
	}
	if err := decodeGovernedResult(results[3].Output, &last); err != nil {
		t.Fatalf("decode the re-read: %v", err)
	}
	if first.Path != e05TaskPath || first.Content != e05TaskBaseline {
		t.Fatalf("first read released path %q content %q; want %q and %q", first.Path, first.Content, e05TaskPath, e05TaskBaseline)
	}
	if last.Path != "out.txt" || last.Content != "edited write\n" {
		t.Fatalf("re-read released path %q content %q; want out.txt and the edited canonical file", last.Path, last.Content)
	}
	if _, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins); err != nil {
		t.Fatalf("governed replay refused the aliased transcript: %v", err)
	}
}
