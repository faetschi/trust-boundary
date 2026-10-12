package providerbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

type memoryJournal struct {
	mu       sync.Mutex
	events   []audit.Event
	sequence uint64
	failKind string
}

func (j *memoryJournal) Append(event audit.Event) (audit.Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if event.Kind == j.failKind {
		return audit.Record{}, errors.New("fixture journal sync failure")
	}
	j.sequence++
	copy := event
	copy.Data = append([]byte(nil), event.Data...)
	j.events = append(j.events, copy)
	h := sha256.Sum256(append([]byte(fmt.Sprintf("%d\x00", j.sequence)), event.Data...))
	return audit.Record{Version: 1, Sequence: j.sequence, Event: copy, Hash: hex.EncodeToString(h[:])}, nil
}

func (j *memoryJournal) snapshot() []audit.Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]audit.Event, len(j.events))
	copy(out, j.events)
	return out
}

type fixtureResponse struct {
	responseID string
	callID     string
	tool       string
	arguments  json.RawMessage
	text       string
}

type fixtureDoer struct {
	mu        sync.Mutex
	responses []fixtureResponse
	requests  [][]byte
	call      int
	block     <-chan struct{}
	started   chan struct{}
}

func (d *fixtureDoer) Do(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || request.URL.String() != openrouterEndpointForTest {
		return nil, errors.New("unexpected fixture provider route")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.requests = append(d.requests, append([]byte(nil), body...))
	if d.block != nil {
		wait := d.block
		if d.started != nil {
			close(d.started)
		}
		d.mu.Unlock()
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-wait:
			return nil, errors.New("unexpected fixture release")
		}
	}
	if d.call >= len(d.responses) {
		d.mu.Unlock()
		return nil, errors.New("fixture provider response budget exhausted")
	}
	response := d.responses[d.call]
	d.call++
	d.mu.Unlock()
	var raw []byte
	if response.tool != "" {
		raw = toolResponse(response)
	} else {
		raw = textResponse(response)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), Request: request}, nil
}

const openrouterEndpointForTest = "https://openrouter.ai/api/v1/chat/completions"

type fixtureExecutor struct {
	outputs []json.RawMessage
	call    int
}

type gatedExecutor struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	output  json.RawMessage
}

func (e *gatedExecutor) Execute(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error) {
	e.calls.Add(1)
	close(e.started)
	<-e.release
	return append(json.RawMessage(nil), e.output...), nil
}

func (e *fixtureExecutor) Execute(_ context.Context, _ protocol.Proposal, _ gate.Decision) (json.RawMessage, error) {
	if e.call >= len(e.outputs) {
		return nil, errors.New("fixture executor output budget exhausted")
	}
	out := append(json.RawMessage(nil), e.outputs[e.call]...)
	e.call++
	return out, nil
}

func TestConversationOwnsFourToolE05TranscriptAndDurableResults(t *testing.T) {
	doer := &fixtureDoer{responses: []fixtureResponse{
		{responseID: "response-read-g0", callID: "call-read-g0", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)},
		{responseID: "response-edit-g1", callID: "call-edit-g1", tool: "edit", arguments: json.RawMessage(`{"path":"README.md","edits":[{"oldText":"old","newText":"new"}]}`)},
		{responseID: "response-bash-g2", callID: "call-bash-g2", tool: "bash", arguments: json.RawMessage(`{"command":"go test ./..."}`)},
		{responseID: "response-read-g2", callID: "call-read-g2", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)},
		{responseID: "response-final", text: "The synthetic changes are complete."},
	}}
	journal := &memoryJournal{}
	c := newFixtureConversation(t, journal, doer, "g0", "tree-g0")
	if err := c.AdmitPrompt("task-1", "Review the synthetic fixture.\nUse bounded inputs."); err != nil {
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "edit", Effect: gate.EffectAllow},
		{Tool: "bash", Effect: gate.EffectAllow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	outputs := []json.RawMessage{
		readOutput("g0", "tree-g0", "old\n"),
		mutationOutput("edit", "g1", "tree-g1", "transition-edit", 1, "effect-edit", nil),
		mutationOutput("bash", "g2", "tree-g2", "transition-bash", 2, "effect-bash", &commandSummary{ExitCode: 0, ExitObserved: true, CommandContainmentStatus: "not-established", CommandRunnerProfile: "wsl-dev-sandbox-non-claim-bearing"}),
		readOutput("g2", "tree-g2", "new\n"),
	}
	executor, err := NewExecutor(c, &fixtureExecutor{outputs: outputs})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		tool            string
		inputGeneration string
		responseID      string
	}{
		{"read", "g0", "response-read-g0"},
		{"edit", "g0", "response-edit-g1"},
		{"bash", "g1", "response-bash-g2"},
		{"read", "g2", "response-read-g2"},
	}
	for i, step := range want {
		turn, err := c.NextTurn(context.Background())
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		if turn.ToolCall == nil || turn.ToolCall.Name != step.tool || turn.Generation != step.inputGeneration || turn.ResponseID != step.responseID {
			t.Fatalf("turn %d projection=%+v", i, turn)
		}
		proposal := proposalFor(turn)
		matched, err := c.Correlate(context.Background(), proposal)
		if err != nil || !matched.Accepted || matched.StreamClosed {
			t.Fatalf("correlate turn %d: decision=%+v err=%v", i, matched, err)
		}
		decision := gate.Evaluate(proposal, matched, policy)
		if err := c.RecordDecision(context.Background(), proposal, decision); err != nil {
			t.Fatalf("record decision turn %d: %v", i, err)
		}
		if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
			t.Fatalf("execute result turn %d: %v", i, err)
		}
	}
	if err := c.AdmitPrompt("task-follow-up", "Summarize the changes."); !errors.Is(err, ErrWrongState) {
		t.Fatalf("follow-up admission before terminal assistant response = %v", err)
	}
	final, err := c.NextTurn(context.Background())
	if err != nil || final.ToolCall != nil || final.AssistantText != "The synthetic changes are complete." {
		t.Fatalf("final assistant turn=%+v err=%v", final, err)
	}
	if err := c.AdmitPrompt("task-follow-up", "Summarize the changes."); err != nil {
		t.Fatalf("trusted follow-up after terminal assistant response: %v", err)
	}

	journalEvents := journal.snapshot()
	var toolResults []resultEvidence
	for _, event := range journalEvents {
		if event.Kind == "provider_tool_result" {
			var result resultEvidence
			if err := json.Unmarshal(event.Data, &result); err != nil {
				t.Fatal(err)
			}
			toolResults = append(toolResults, result)
		}
	}
	if len(toolResults) != 4 {
		t.Fatalf("durable tool-result count=%d want=4", len(toolResults))
	}
	from := []string{"g0", "g0", "g1", "g2"}
	to := []string{"g0", "g1", "g2", "g2"}
	for i := range toolResults {
		if toolResults[i].GenerationFrom != from[i] || toolResults[i].GenerationTo != to[i] ||
			toolResults[i].ResultID == "" || !strings.HasPrefix(toolResults[i].ResultDigest, "sha256:") {
			t.Fatalf("result %d generation=%s->%s", i, toolResults[i].GenerationFrom, toolResults[i].GenerationTo)
		}
		digest := sha256.Sum256(toolResults[i].Result)
		if toolResults[i].ResultDigest != "sha256:"+hex.EncodeToString(digest[:]) {
			t.Fatalf("result %d digest does not bind exact protocol result", i)
		}
		assertRequestResultExact(t, doer.requests[i+1], toolResults[i])
	}
	if len(doer.requests) != 5 {
		t.Fatalf("HTTP fixture request count=%d want=5", len(doer.requests))
	}
	assertRequestTranscript(t, doer.requests[0], 3, "user", "Review the synthetic fixture.")
	assertRequestTranscript(t, doer.requests[1], 5, "tool", "call-read-g0")
	assertRequestTranscript(t, doer.requests[2], 7, "tool", "call-edit-g1")
	assertRequestTranscript(t, doer.requests[3], 9, "tool", "call-bash-g2")
	assertRequestTranscript(t, doer.requests[4], 11, "tool", "call-read-g2")
}

func TestConversationWithholdsExchangeOnJournalFailure(t *testing.T) {
	journal := &memoryJournal{failKind: "provider_exchange"}
	doer := &fixtureDoer{responses: []fixtureResponse{{responseID: "response-1", callID: "call-1", tool: "read", arguments: json.RawMessage(`{"path":"x"}`)}}}
	c := newFixtureConversation(t, journal, doer, "g0", "tree-g0")
	if err := c.AdmitPrompt("task-1", "synthetic"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextTurn(context.Background()); err == nil {
		t.Fatal("provider response was forwarded without durable exchange record")
	}
	if _, err := c.NextTurn(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("conversation did not close after result withholding: %v", err)
	}
}

func TestConversationRejectsUncorrelatedOrReplayedProposal(t *testing.T) {
	doer := &fixtureDoer{responses: []fixtureResponse{{responseID: "response-1", callID: "call-1", tool: "read", arguments: json.RawMessage(`{"path":"x"}`)}}}
	c := newFixtureConversation(t, &memoryJournal{}, doer, "g0", "tree-g0")
	if err := c.AdmitPrompt("task-1", "synthetic"); err != nil {
		t.Fatal(err)
	}
	turn, err := c.NextTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	proposal.ToolCallID = "forged-call"
	decision, err := c.Correlate(context.Background(), proposal)
	if err != nil || !decision.StreamClosed || decision.Accepted {
		t.Fatalf("forged proposal decision=%+v err=%v", decision, err)
	}
	if _, err := c.NextTurn(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("conversation did not close after mismatch: %v", err)
	}
}

func TestDeniedResultIsJournaledBeforeContinuationWithoutExecutor(t *testing.T) {
	doer := &fixtureDoer{responses: []fixtureResponse{
		{responseID: "response-deny", callID: "call-deny", tool: "write", arguments: json.RawMessage(`{"path":"x","content":"y"}`)},
		{responseID: "response-after-deny", text: "The operation was denied."},
	}}
	journal := &memoryJournal{}
	c := newFixtureConversation(t, journal, doer, "g0", "tree-g0")
	if err := c.AdmitPrompt("task-deny", "synthetic deny test"); err != nil {
		t.Fatal(err)
	}
	turn, err := c.NextTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	matched, err := c.Correlate(context.Background(), proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate denied call: %+v %v", matched, err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion})
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Evaluate(proposal, matched, policy)
	if decision.Verdict != gate.Deny {
		t.Fatalf("default gate verdict=%s", decision.Verdict)
	}
	if err := c.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	turn, err = c.NextTurn(context.Background())
	if err != nil || turn.AssistantText != "The operation was denied." {
		t.Fatalf("continuation after durable denial=%+v err=%v", turn, err)
	}
	if len(doer.requests) != 2 {
		t.Fatalf("request count=%d want=2", len(doer.requests))
	}
	assertRequestTranscript(t, doer.requests[1], 5, "tool", "call-deny")
	var found bool
	for _, event := range journal.snapshot() {
		if event.Kind == "provider_tool_result" {
			var result resultEvidence
			if err := json.Unmarshal(event.Data, &result); err != nil {
				t.Fatal(err)
			}
			var protocolResult protocol.Result
			if err := json.Unmarshal(result.Result, &protocolResult); err != nil {
				t.Fatal(err)
			}
			if protocolResult.Verdict != string(gate.Deny) || protocolResult.Output != nil {
				t.Fatalf("denial result=%+v", protocolResult)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("denial continuation did not persist its exact protocol result")
	}
}

func TestExecutorResultJournalFailureWithholdsIPCOutputAndRecordsUnknown(t *testing.T) {
	doer := &fixtureDoer{responses: []fixtureResponse{{responseID: "response-write", callID: "call-write", tool: "write", arguments: json.RawMessage(`{"path":"x","content":"y"}`)}}}
	journal := &memoryJournal{failKind: "provider_tool_result"}
	c := newFixtureConversation(t, journal, doer, "g0", "tree-g0")
	if err := c.AdmitPrompt("task-failure", "synthetic durability failure"); err != nil {
		t.Fatal(err)
	}
	turn, err := c.NextTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	matched, err := c.Correlate(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}}})
	decision := gate.Evaluate(proposal, matched, policy)
	if err := c.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	output := mutationOutput("write", "g1", "tree-g1", "transition-1", 1, "effect-1", nil)
	executor, err := NewExecutor(c, &fixtureExecutor{outputs: []json.RawMessage{output}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), proposal, decision); err == nil {
		t.Fatal("executor result was released after durable result append failure")
	}
	if _, err := c.NextTurn(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("conversation remained open after result durability failure: %v", err)
	}
	unknown := false
	for _, event := range journal.snapshot() {
		if event.Kind == "provider_tool_result_unknown" {
			unknown = true
		}
	}
	if !unknown {
		t.Fatal("result append failure did not retain a conservative UNKNOWN record")
	}
}

func TestConcurrentDuplicateExecutorCallCannotReleaseTwice(t *testing.T) {
	doer := &fixtureDoer{responses: []fixtureResponse{{responseID: "response-write-race", callID: "call-write-race", tool: "write", arguments: json.RawMessage(`{"path":"x","content":"y"}`)}}}
	journal := &memoryJournal{}
	c := newFixtureConversation(t, journal, doer, "g0", "tree-g0")
	if err := c.AdmitPrompt("task-race", "synthetic executor race"); err != nil {
		t.Fatal(err)
	}
	turn, err := c.NextTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	matched, err := c.Correlate(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Evaluate(proposal, matched, policy)
	if err := c.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	inner := &gatedExecutor{started: make(chan struct{}), release: make(chan struct{}), output: mutationOutput("write", "g1", "tree-g1", "transition-race", 1, "effect-race", nil)}
	executor, err := NewExecutor(c, inner)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := executor.Execute(context.Background(), proposal, decision)
		firstDone <- err
	}()
	<-inner.started
	if _, err := executor.Execute(context.Background(), proposal, decision); err == nil {
		t.Fatal("concurrent duplicate executor call was accepted")
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("underlying executor called %d times", got)
	}
	close(inner.release)
	if err := <-firstDone; err == nil {
		t.Fatal("result was released after conversation closed during execution")
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("underlying executor called %d times after completion", got)
	}
	unknown := false
	for _, event := range journal.snapshot() {
		if event.Kind == "provider_tool_result_unknown" {
			unknown = true
		}
	}
	if !unknown {
		t.Fatal("ambiguous execution did not leave a durable UNKNOWN record")
	}
}

func TestConversationCancellationRecordsUnknownAndCloses(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	doer := &fixtureDoer{responses: []fixtureResponse{{responseID: "response-1", callID: "call-1", tool: "read", arguments: json.RawMessage(`{"path":"x"}`)}}, block: release, started: started}
	journal := &memoryJournal{}
	c := newFixtureConversation(t, journal, doer, "g0", "tree-g0")
	if err := c.AdmitPrompt("task-1", "synthetic"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	turnDone := make(chan error, 1)
	go func() {
		_, err := c.NextTurn(ctx)
		turnDone <- err
	}()
	<-started
	cancel()
	if err := <-turnDone; err == nil {
		t.Fatal("canceled provider turn returned a response")
	}
	if _, err := c.NextTurn(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("conversation did not remain closed after cancellation: %v", err)
	}
	foundUnknown := false
	for _, event := range journal.snapshot() {
		if event.Kind == "provider_exchange_unknown" {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Fatal("canceled in-flight provider request lacks a durable UNKNOWN exchange record")
	}
}

func newFixtureConversation(t *testing.T, journal eventJournal, doer broker.HTTPDoer, generation, digest string) *Conversation {
	t.Helper()
	c, err := newWithDoer(Config{
		ConversationID: "conversation-fixture", WorkflowID: "workflow-fixture", ProfileID: "profile-fixture",
		InitialGenerationID: generation, InitialTreeDigest: digest,
		SystemPrompt: "Fixture system prompt.", DeveloperPrompt: "Fixture developer profile.",
	}, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func proposalFor(turn Turn) protocol.Proposal {
	if turn.ToolCall == nil {
		panic("fixture turn lacks tool call")
	}
	return protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
		Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
}

func toolResponse(response fixtureResponse) []byte {
	call := map[string]any{"index": 0, "id": response.callID, "type": "function", "function": map[string]any{"name": response.tool, "arguments": string(response.arguments)}}
	return appendSSE(response.responseID, map[string]any{"role": "assistant", "tool_calls": []any{call}}, "tool_calls")
}

func textResponse(response fixtureResponse) []byte {
	return appendSSE(response.responseID, map[string]any{"role": "assistant", "content": response.text}, "stop")
}

func appendSSE(responseID string, delta map[string]any, finish string) []byte {
	first, _ := json.Marshal(map[string]any{
		"id": responseID, "model": ApprovedModel, "created": 1, "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	})
	last, _ := json.Marshal(map[string]any{
		"id": responseID, "model": ApprovedModel, "created": 1, "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
	})
	out := append([]byte("data: "), first...)
	out = append(out, []byte("\r\n\r\ndata: ")...)
	out = append(out, last...)
	out = append(out, []byte("\r\n\r\ndata: [DONE]\r\n\r\n")...)
	return out
}

func assertRequestTranscript(t *testing.T, raw []byte, wantMessages int, lastRole, contains string) {
	t.Helper()
	var request struct {
		Model    string `json:"model"`
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.Model != ApprovedModel || len(request.Messages) != wantMessages {
		t.Fatalf("request model/messages=%s/%d want %s/%d", request.Model, len(request.Messages), ApprovedModel, wantMessages)
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != lastRole {
		t.Fatalf("last role=%q want %q", last.Role, lastRole)
	}
	encoded, _ := json.Marshal(last.Content)
	if lastRole == "tool" && last.ToolCallID != contains {
		t.Fatalf("last tool call ID=%q want %q", last.ToolCallID, contains)
	}
	if lastRole == "user" && !strings.Contains(string(encoded), contains) {
		t.Fatalf("last user content=%s does not contain admitted prompt", encoded)
	}
}

func assertRequestResultExact(t *testing.T, raw []byte, expected resultEvidence) {
	t.Helper()
	var request struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			Content    any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) == 0 {
		t.Fatal("provider request has no tool result history")
	}
	last := request.Messages[len(request.Messages)-1]
	content, ok := last.Content.(string)
	if !ok || last.Role != "tool" || last.ToolCallID != expected.ToolCallID || content != string(expected.Result) {
		t.Fatalf("provider tool result differs from Go-owned durable result: role=%q call=%q content=%q want-call=%q want=%q", last.Role, last.ToolCallID, content, expected.ToolCallID, expected.Result)
	}
}

func readOutput(generation, treeDigest, content string) json.RawMessage {
	return readOutputFor("README.md", generation, treeDigest, content)
}

func readOutputFor(path, generation, treeDigest, content string) json.RawMessage {
	digest := sha256.Sum256([]byte(content))
	encoded, _ := json.Marshal(map[string]any{
		"tool": "read", "path": path, "generation": map[string]string{"id": generation, "tree_digest": treeDigest},
		"size": len(content), "content_digest": "sha256:" + hex.EncodeToString(digest[:]), "content": content, "outcome": "success",
	})
	return encoded
}

type commandSummary struct {
	ExitCode                 int    `json:"exit_code"`
	ExitObserved             bool   `json:"exit_observed"`
	CommandContainmentStatus string `json:"command_containment_status"`
	CommandRunnerProfile     string `json:"command_runner_profile"`
}

func mutationOutput(tool, generation, treeDigest, transition string, sequence uint64, effect string, command *commandSummary) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{
		"tool": tool, "generation": map[string]string{"id": generation, "tree_digest": treeDigest},
		"transition": map[string]any{"id": transition, "sequence": sequence}, "effect_id": effect,
		"outcome": "success", "command": command,
	})
	// The durable executor omits command for non-bash tools.
	if command == nil {
		var object map[string]json.RawMessage
		_ = json.Unmarshal(encoded, &object)
		delete(object, "command")
		encoded, _ = json.Marshal(object)
	}
	return encoded
}
