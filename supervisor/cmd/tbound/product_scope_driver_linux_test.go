//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

const (
	scopeDriverPrompt = "Write the notes, and touch nothing else."
	scopeDriverAnswer = "I wrote notes/a.txt."
)

func productScopeNotes() *productsession.ProductScope {
	return &productsession.ProductScope{Writable: []string{"notes"}, Protected: []string{"notes/secret.txt"}}
}

// seedScopedProject adds notes/secret.txt and other.txt to the project the fixture measures.
func seedScopedProject(t *testing.T) func(string) {
	return func(projectDir string) {
		notes := filepath.Join(projectDir, "notes")
		if err := os.Mkdir(notes, 0o755); err != nil || os.Chmod(notes, 0o755) != nil {
			t.Fatal(err)
		}
		writeProductDriverFile(t, notes, "secret.txt", "private\n")
		writeProductDriverFile(t, projectDir, "other.txt", "outside\n")
	}
}

func scopeDriverWrite(id, path, content string) providerbridge.ScriptTurn {
	arguments, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		panic(err)
	}
	return providerbridge.ScriptTurn{ResponseID: "response-" + id, CallID: "call-" + id, Tool: "write", Arguments: arguments}
}

func scopeDriverFinal(id, text string) providerbridge.ScriptTurn {
	// Response IDs are unique across the turns of a session, because imported ones are reserved.
	return providerbridge.ScriptTurn{ResponseID: "response-final-" + id, Text: text}
}

// lastProviderRequestMessages decodes the exact last request body journaled for a conversation.
func lastProviderRequestMessages(t *testing.T, path string) []productWireMessage {
	t.Helper()
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var last []productWireMessage
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_exchange" {
			continue
		}
		var exchange struct {
			Request struct {
				Body []byte `json:"body"`
			} `json:"request"`
		}
		var body struct {
			Messages []productWireMessage `json:"messages"`
		}
		if err := json.Unmarshal(record.Event.Data, &exchange); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(exchange.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		last = body.Messages
	}
	if last == nil {
		t.Fatal("provider journal has no exchange")
	}
	return last
}

func toolMessage(t *testing.T, messages []productWireMessage, callID string) string {
	t.Helper()
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == callID && message.Content != nil {
			return *message.Content
		}
	}
	t.Fatalf("the provider request has no tool result for %s", callID)
	return ""
}

func fileContent(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(data)
}

func scopeDriverFirstTurn() *productDriverScript {
	return &productDriverScript{turns: []providerbridge.ScriptTurn{
		scopeDriverWrite("allowed", "notes/a.txt", "inside\n"),
		scopeDriverWrite("outside", "other.txt", "changed\n"),
		scopeDriverWrite("protected", "notes/secret.txt", "leaked\n"),
		scopeDriverFinal("t1", scopeDriverAnswer),
	}}
}

// A write inside a writable subtree is published. A write outside every writable entry, and a write to
// a protected path inside one, are denied by delta admission with their stable reason codes and hints.
// The turn still completes and the next turn rebuilds its context from the retained, scoped evidence.
func TestProductTurnDriverEnforcesTheProjectScope(t *testing.T) {
	f := newScopedProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, seedScopedProject(t))
	baseline := f.measure(t)
	receipt, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), scopeDriverFirstTurn())
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if got := fileContent(t, filepath.Join(f.projectDir, "notes"), "a.txt"); got != "inside\n" {
		t.Fatalf("the write inside the writable subtree was not published: %q", got)
	}
	if got := fileContent(t, f.projectDir, "other.txt"); got != "outside\n" {
		t.Fatalf("a write outside every writable entry reached the project: %q", got)
	}
	if got := fileContent(t, filepath.Join(f.projectDir, "notes"), "secret.txt"); got != "private\n" {
		t.Fatalf("a write to a protected path reached the project: %q", got)
	}
	wantDenials := []productTurnDenial{
		{ToolCallID: "call-outside", Tool: "write", ReasonCode: "scope_path_not_covered"},
		{ToolCallID: "call-protected", Tool: "write", ReasonCode: "scope_protected_path"},
	}
	if turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomePublished ||
		turn.ResultBaselineDigest == baseline || turn.ResultBaselineDigest != f.measure(t) || receipt.PublicationTransactionID == "" ||
		fmt.Sprint(receipt.ToolSequence) != "[write write write]" || fmt.Sprint(receipt.Denials) != fmt.Sprint(wantDenials) {
		t.Fatalf("turn 1 settlement: turn=%+v receipt=%+v", turn, receipt)
	}
	if turn.Scope == nil || fmt.Sprint(turn.Scope.Writable, turn.Scope.Protected) != fmt.Sprint([]string{"notes"}, []string{"notes/secret.txt"}) {
		t.Fatalf("the turn is not pinned to the project's scope: %+v", turn.Scope)
	}
	if session := f.currentSession(t); session.Blocked || session.Reserved != (productsession.Budget{}) {
		t.Fatalf("a denied write must not block the session: %+v", session)
	}

	messages := lastProviderRequestMessages(t, turn.Link.ProviderJournalPath)
	for callID, reason := range map[string]string{"call-outside": "scope_path_not_covered", "call-protected": "scope_protected_path"} {
		hint := providerbridge.HintCandidates(reason)["0"]
		denied := toolMessage(t, messages, callID)
		if hint == "" || !strings.Contains(denied, `"DENY"`) || !strings.Contains(denied, reason) || !strings.HasSuffix(denied, "\nhint: "+hint) {
			t.Errorf("%s: the provider did not get the denial with its hint: %q", callID, denied)
		}
	}
	if allowed := toolMessage(t, messages, "call-allowed"); strings.Contains(allowed, "DENY") || strings.Contains(allowed, "hint:") {
		t.Errorf("the admitted write was reported as denied: %q", allowed)
	}

	second := f.submit(t, "turn-2", "Read notes/a.txt back to me.")
	script2 := &productDriverScript{turns: []providerbridge.ScriptTurn{
		{ResponseID: "t2-read", CallID: "t2-call-read", Tool: "read", Arguments: json.RawMessage(`{"path":"notes/a.txt"}`)},
		scopeDriverFinal("t2", "It says inside."),
	}}
	_, turn2, err := f.run(t, second, script2)
	if err != nil || turn2.Outcome != productsession.OutcomeNoDiff || turn2.Scope == nil {
		t.Fatalf("turn 2 over the scoped predecessor: %+v %v", turn2, err)
	}
}

// Read-only sessions empty the writable list: nothing can change the project, on a project with a
// scope and on one without.
func TestProductTurnDriverReadOnlySessionChangesNothing(t *testing.T) {
	for name, scope := range map[string]*productsession.ProductScope{
		"project with a scope":    productScopeNotes(),
		"project without a scope": nil,
	} {
		t.Run(name, func(t *testing.T) {
			f := newScopedProductDriverFixture(t, productsession.PublicationAuto, scope, true, seedScopedProject(t))
			if !f.session.ReadOnly {
				t.Fatal("the session is not read-only")
			}
			baseline := f.measure(t)
			script := &productDriverScript{turns: []providerbridge.ScriptTurn{
				scopeDriverWrite("notes", "notes/a.txt", "inside\n"),
				scopeDriverWrite("task", "task.txt", "changed\n"),
				scopeDriverFinal("ro", "I could not change anything."),
			}}
			receipt, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
			if err != nil {
				t.Fatalf("turn: %v", err)
			}
			wantDenials := []productTurnDenial{
				{ToolCallID: "call-notes", Tool: "write", ReasonCode: "scope_path_not_covered"},
				{ToolCallID: "call-task", Tool: "write", ReasonCode: "scope_path_not_covered"},
			}
			if turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomeNoDiff ||
				turn.ResultBaselineDigest != baseline || f.measure(t) != baseline || receipt.PublicationTransactionID != "" ||
				fmt.Sprint(receipt.Denials) != fmt.Sprint(wantDenials) {
				t.Fatalf("a read-only turn changed something or settled wrongly: turn=%+v receipt=%+v", turn, receipt)
			}
			if turn.Scope == nil || len(turn.Scope.Writable) != 0 {
				t.Fatalf("a read-only turn keeps a writable path: %+v", turn.Scope)
			}
			if _, err := os.Stat(filepath.Join(f.projectDir, "notes", "a.txt")); !os.IsNotExist(err) {
				t.Fatalf("notes/a.txt exists after a read-only turn: %v", err)
			}
		})
	}
}

// A new registration can change a project's scope between turns. The earlier turn's retained Store
// was written under the scope it was admitted with, so the next turn must reopen it under that one.
func TestProductTurnDriverReopensAPredecessorUnderItsOwnScope(t *testing.T) {
	created := newScopedProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, seedScopedProject(t))
	_, turn1, err := created.run(t, created.submit(t, "turn-1", scopeDriverPrompt), scopeDriverFirstTurn())
	if err != nil || turn1.Outcome != productsession.OutcomePublished {
		t.Fatalf("turn 1: %+v %v", turn1, err)
	}
	if err := created.env.Catalog.Close(); err != nil {
		t.Fatal(err)
	}
	f := openProductDriverFixtureWithScope(t, created.root, &productsession.ProductScope{Writable: []string{"notes", "other.txt"}})
	f.session = created.session
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{
		scopeDriverWrite("now-allowed", "other.txt", "changed\n"),
		scopeDriverFinal("t2", "Done."),
	}}
	_, turn2, err := f.run(t, f.submit(t, "turn-2", "Update other.txt."), script)
	if err != nil || turn2.Outcome != productsession.OutcomePublished {
		t.Fatalf("turn 2 under the new registration: %+v %v", turn2, err)
	}
	if got := fileContent(t, f.projectDir, "other.txt"); got != "changed\n" {
		t.Fatalf("the new registration's scope did not apply: %q", got)
	}
	if turn1.Scope == nil || len(turn1.Scope.Writable) != 1 {
		t.Fatalf("turn 1 lost its scope: %+v", turn1.Scope)
	}
}
