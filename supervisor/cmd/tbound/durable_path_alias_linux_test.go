//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

// TestDurableArgumentsRefuseSpellingsThatLeaveTheWorkspace pins the spellings the path mapping must refuse: a sibling
// of a workspace root, a root itself, and dot-dot escapes. The argument decoders refuse them before the Store or
// admission sees the path.
func TestDurableArgumentsRefuseSpellingsThatLeaveTheWorkspace(t *testing.T) {
	for _, spelling := range []string{
		"/workx/a", "/work", "/work/", ".", "/work/../x",
		"/var/lib/tbound/pi-worker/../../etc/passwd", "a/../../b",
	} {
		read, err := json.Marshal(map[string]string{"path": spelling})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := durableReadArguments(read); err == nil {
			t.Errorf("read of %q: accepted", spelling)
		}
		write, err := json.Marshal(map[string]string{"path": spelling, "content": "x"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := durableWriteArguments(write); err == nil {
			t.Errorf("write of %q: accepted", spelling)
		}
		edit, err := json.Marshal(map[string]any{"path": spelling, "edits": []map[string]string{{"oldText": "a", "newText": "b"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := durableEditArguments(edit); err == nil {
			t.Errorf("edit of %q: accepted", spelling)
		}
	}
}

// TestProductTurnWritesCanonicalSpellingsAndCompletes runs a product turn whose model writes through a spelling that
// is not the canonical Store path. Preflight must see the canonical path, so the turn publishes the file instead of
// ending on ErrInvalidPath.
func TestProductTurnWritesCanonicalSpellingsAndCompletes(t *testing.T) {
	for _, testCase := range []struct{ spelling, file string }{
		{"./a.txt", "a.txt"}, {"src//a", "src/a"}, {"src/./a", "src/a"},
	} {
		t.Run(testCase.spelling, func(t *testing.T) {
			f := newScopedProductDriverFixture(t, productsession.PublicationAuto, nil, false, func(projectDir string) {
				src := filepath.Join(projectDir, "src")
				if err := os.Mkdir(src, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(src, 0o755); err != nil {
					t.Fatal(err)
				}
			})
			admission := f.submit(t, "turn-alias", productDriverPromptA)
			arguments, err := json.Marshal(map[string]string{"path": testCase.spelling, "content": productDriverNotes})
			if err != nil {
				t.Fatal(err)
			}
			script := &productDriverScript{turns: []providerbridge.ScriptTurn{
				{ResponseID: "t1-response-alias", CallID: "t1-call-alias", Tool: "write", Arguments: arguments},
				{ResponseID: "t1-response-final", Text: productDriverAnswerA},
			}}
			receipt, turn, err := f.run(t, admission, script)
			if err != nil {
				t.Fatalf("turn writing %q did not complete: %v", testCase.spelling, err)
			}
			published, err := os.ReadFile(filepath.Join(f.projectDir, testCase.file))
			if err != nil || string(published) != productDriverNotes {
				t.Fatalf("turn writing %q did not publish %s: %q %v", testCase.spelling, testCase.file, published, err)
			}
			if turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomePublished ||
				len(receipt.ToolSequence) != 1 || receipt.ToolSequence[0] != "write" || len(receipt.Denials) != 0 {
				t.Fatalf("turn writing %q settlement: turn=%+v receipt=%+v", testCase.spelling, turn, receipt)
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
	if first.Path != e05TaskPath || first.Content != "before\n" {
		t.Fatalf("first read released path %q content %q; want %q and the replay fixture's before content", first.Path, first.Content, e05TaskPath)
	}
	if last.Path != "out.txt" || last.Content != "edited write\n" {
		t.Fatalf("re-read released path %q content %q; want out.txt and the edited canonical file", last.Path, last.Content)
	}
	if _, err := ReplayGovernedTranscript(fixture.providerJournal, fixture.storeJournal, fixture.store, fixture.pins); err != nil {
		t.Fatalf("governed replay refused the aliased transcript: %v", err)
	}
}
