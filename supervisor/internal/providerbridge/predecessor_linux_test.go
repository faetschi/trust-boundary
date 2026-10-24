//go:build linux

package providerbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

const predecessorDenyHint = "The policy does not allow this tool or action. Use another available tool on the task's files."

type predecessorRun struct {
	cfg          Config
	policy       gate.Policy
	journal      *audit.Journal
	readResult   string
	deniedResult string
}

func readOnlyPolicy(t *testing.T) gate.Policy {
	t.Helper()
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "read", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func sha256Prefixed(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func testBoundary(label, outcome string) TurnBoundary {
	return TurnBoundary{PredecessorReceiptDigest: sha256Prefixed("receipt " + label), Outcome: outcome,
		ProjectBaselineDigest: recoveryTestTree("tree-0")}
}

func driveToolTurn(t *testing.T, conversation *Conversation, executor *ResultingExecutor, policy gate.Policy) {
	t.Helper()
	turn, err := conversation.NextTurn(context.Background())
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("provider turn=%+v err=%v", turn, err)
	}
	proposal := proposalFor(turn)
	matched, err := conversation.Correlate(context.Background(), proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate=%+v err=%v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, policy)
	if err := conversation.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	if decision.Verdict == gate.Allow {
		if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
			t.Fatal(err)
		}
	}
}

// runPredecessor is conversation 1: an ALLOWed read, a DENIED write whose result carries a static hint,
// and a final assistant answer with no tool call.
func runPredecessor(t *testing.T) predecessorRun {
	t.Helper()
	cfg := recoveryTestConfig()
	cfg.ConversationID, cfg.WorkflowID = "conversation-p1", "workflow-p1"
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "p1.audit"))
	conversation, err := newScripted(cfg, journal, &recordingSource{inner: NewStaticScript([]ScriptTurn{
		{ResponseID: "p1-response-read", CallID: "p1-call-read", Tool: "read", Arguments: json.RawMessage(`{"path":"README.md"}`)},
		{ResponseID: "p1-response-write", CallID: "p1-call-write", Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"x"}`)},
		{ResponseID: "p1-response-final", Text: "p1 final answer"},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-1", "turn one prompt"); err != nil {
		t.Fatal(err)
	}
	policy := readOnlyPolicy(t)
	executor, err := NewExecutor(conversation, &fixtureExecutor{outputs: []json.RawMessage{
		readOutputFor("README.md", "g0", recoveryTestTree("tree-0"), "readme content"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	driveToolTurn(t, conversation, executor, policy)
	driveToolTurn(t, conversation, executor, policy)
	if turn, err := conversation.NextTurn(context.Background()); err != nil || turn.ToolCall != nil || turn.AssistantText != "p1 final answer" {
		t.Fatalf("final turn=%+v err=%v", turn, err)
	}
	run := predecessorRun{cfg: cfg, policy: policy, journal: journal}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var results []string
	for _, record := range trace.Records {
		if record.Event.Kind == "provider_tool_result" {
			var evidence resultEvidence
			if err := json.Unmarshal(record.Event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			results = append(results, string(evidence.Result))
		}
	}
	if len(results) != 2 || !strings.Contains(results[1], `"DENY"`) {
		t.Fatalf("predecessor results=%q", results)
	}
	run.readResult, run.deniedResult = results[0], results[1]
	return run
}

func (r predecessorRun) pins() RecoveryPins {
	return RecoveryPins{Config: r.cfg, Model: ApprovedModel, Policy: r.policy}
}

func successorConfig(predecessor PredecessorContext) Config {
	cfg := recoveryTestConfig()
	cfg.ConversationID, cfg.WorkflowID = "conversation-p2", "workflow-p2"
	cfg.Predecessor = predecessor
	return cfg
}

type wireMessage struct {
	Role       string  `json:"role"`
	Content    *string `json:"content"`
	ToolCallID string  `json:"tool_call_id"`
	ToolCalls  []struct {
		ID string `json:"id"`
	} `json:"tool_calls"`
}

// firstRequestMessages decodes the exact first request body from the journaled provider exchange.
func firstRequestMessages(t *testing.T, journal *audit.Journal) []wireMessage {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_exchange" {
			continue
		}
		var evidence exchangeEvidence
		if err := json.Unmarshal(record.Event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		var body struct {
			Messages []wireMessage `json:"messages"`
		}
		if err := json.Unmarshal(evidence.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		return body.Messages
	}
	t.Fatal("journal has no provider exchange")
	return nil
}

func content(message wireMessage) string {
	if message.Content == nil {
		return "<nil>"
	}
	return *message.Content
}

func TestPredecessorHistoryRoundTripsIntoAFreshConversation(t *testing.T) {
	p1 := runPredecessor(t)
	boundary := testBoundary("p1", "no_diff")
	predecessor, err := ReplayPredecessorContext(p1.journal, p1.pins(), &replayVerifierFixture{}, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if predecessor.IsZero() || !validPrefixedDigest(predecessor.Digest()) || predecessor.PredecessorReceiptDigest() != boundary.PredecessorReceiptDigest {
		t.Fatalf("context digest=%q receipt=%q", predecessor.Digest(), predecessor.PredecessorReceiptDigest())
	}

	cfg := successorConfig(predecessor)
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "p2.audit"))
	source := &recordingSource{inner: NewStaticScript([]ScriptTurn{{ResponseID: "p2-response-final", Text: "p2 answer"}})}
	conversation, err := newScripted(cfg, journal, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-2", "turn two prompt"); err != nil {
		t.Fatal(err)
	}
	if turn, err := conversation.NextTurn(context.Background()); err != nil || turn.AssistantText != "p2 answer" {
		t.Fatalf("successor turn=%+v err=%v", turn, err)
	}

	wantBoundary := `[TBound host record] The previous user turn ended with outcome "no_diff": it left the project files unchanged. Current project baseline: ` +
		recoveryTestTree("tree-0") + `. Tool results above show the workspace as it was when each tool ran.`
	type expected struct{ role, content, toolCallID, callID string }
	want := []expected{
		{role: "system", content: "pinned system prompt"},
		{role: "system", content: "pinned developer prompt"},
		{role: "user", content: "turn one prompt"},
		{role: "assistant", content: "", callID: "p1-call-read"},
		{role: "tool", content: p1.readResult, toolCallID: "p1-call-read"},
		{role: "assistant", content: "", callID: "p1-call-write"},
		{role: "tool", content: p1.deniedResult + "\nhint: " + predecessorDenyHint, toolCallID: "p1-call-write"},
		{role: "assistant", content: "p1 final answer"},
		{role: "user", content: wantBoundary},
		{role: "user", content: "turn two prompt"},
	}
	got := firstRequestMessages(t, journal)
	if len(got) != len(want) {
		t.Fatalf("first request has %d messages, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		callID := ""
		if len(g.ToolCalls) == 1 {
			callID = g.ToolCalls[0].ID
		}
		if g.Role != w.role || content(g) != w.content || g.ToolCallID != w.toolCallID || callID != w.callID {
			t.Errorf("message %d = role %q content %q tool_call_id %q call %q, want %+v", i, g.Role, content(g), g.ToolCallID, callID, w)
		}
	}
	if len(source.histories) != 1 || len(source.histories[0]) != len(want) ||
		source.histories[0][6].Content != p1.deniedResult || source.histories[0][8].Content != wantBoundary {
		t.Fatalf("script source saw %+v", source.histories)
	}

	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, record := range trace.Records {
		kinds = append(kinds, record.Event.Kind)
	}
	if strings.Join(kinds, ",") != "provider_history_imported,provider_user_admitted,provider_exchange" {
		t.Fatalf("successor journal kinds=%v", kinds)
	}

	t.Run("replay requires the exact imported prefix pin", func(t *testing.T) {
		pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: p1.policy, ImportedPrefixDigest: predecessor.Digest()}
		projection, err := ReplayTranscript(journal, pins, nil)
		if err != nil {
			t.Fatal(err)
		}
		if projection.ImportedPrefixDigest != predecessor.Digest() || projection.Disposition != DispositionAwaitingHostPrompt ||
			projection.EventCount != 3 || !VerifyTranscriptProjectionCommitment(projection) {
			t.Fatalf("projection=%+v", projection)
		}
		for name, pin := range map[string]string{"empty": "", "wrong": sha256Prefixed("other prefix")} {
			pins.ImportedPrefixDigest = pin
			if _, err := ReplayTranscript(journal, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
				t.Errorf("%s pin: replay err=%v, want ErrTranscriptReplay", name, err)
			}
		}
		legacy := p1.pins()
		legacy.ImportedPrefixDigest = predecessor.Digest()
		if _, err := ReplayTranscript(p1.journal, legacy, &replayVerifierFixture{}); !errors.Is(err, ErrTranscriptReplay) {
			t.Errorf("legacy journal with a prefix pin: err=%v, want ErrTranscriptReplay", err)
		}
	})

	t.Run("the import is journaled once and chains into the next context", func(t *testing.T) {
		source.inner = NewStaticScript([]ScriptTurn{{ResponseID: "p2-response-second", Text: "p2 second answer"}})
		if err := conversation.AdmitPrompt("task-2b", "turn two follow-up"); err != nil {
			t.Fatal(err)
		}
		if _, err := conversation.NextTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		imports := 0
		for _, record := range trace.Records {
			if record.Event.Kind == historyImportedKind {
				imports++
			}
		}
		if imports != 1 {
			t.Fatalf("history imported %d times", imports)
		}
		pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: p1.policy, ImportedPrefixDigest: predecessor.Digest()}
		chained, err := ReplayPredecessorContext(journal, pins, nil, testBoundary("p2", "published"))
		if err != nil {
			t.Fatal(err)
		}
		cfg3 := successorConfig(chained)
		cfg3.ConversationID = "conversation-p3"
		journal3 := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "p3.audit"))
		conversation3, err := newScripted(cfg3, journal3, &recordingSource{inner: NewStaticScript([]ScriptTurn{{ResponseID: "p3-response", Text: "p3"}})})
		if err != nil {
			t.Fatal(err)
		}
		if err := conversation3.AdmitPrompt("task-3", "turn three prompt"); err != nil {
			t.Fatal(err)
		}
		if _, err := conversation3.NextTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		var users []string
		for _, message := range firstRequestMessages(t, journal3) {
			if message.Role == "user" {
				users = append(users, content(message))
			}
		}
		if len(users) != 6 || users[0] != "turn one prompt" || users[1] != wantBoundary || users[2] != "turn two prompt" ||
			users[3] != "turn two follow-up" || !strings.Contains(users[4], `outcome "published": its file changes were applied`) ||
			users[5] != "turn three prompt" {
			t.Fatalf("chained user messages=%q", users)
		}
	})
}

func TestPredecessorReplayRefusesTamperedOrIncompleteJournals(t *testing.T) {
	p1 := runPredecessor(t)
	boundary := testBoundary("p1", "no_diff")
	trace, err := p1.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var events []audit.Event
	for _, record := range trace.Records {
		event := record.Event
		if event.Kind == "provider_user_admitted" {
			event.Data = []byte(strings.Replace(string(event.Data), "turn one prompt", "turn one prompX", 1))
		}
		events = append(events, event)
	}
	if _, err := ReplayPredecessorContext(rechainRecoveryEvents(t, events), p1.pins(), &replayVerifierFixture{}, boundary); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("flipped prompt byte: err=%v, want ErrTranscriptReplay", err)
	}

	cfg := recoveryTestConfig()
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "pending.audit"))
	conversation, err := newScripted(cfg, journal, &recordingSource{inner: NewStaticScript([]ScriptTurn{
		{ResponseID: "pending-response", CallID: "pending-call", Tool: "read", Arguments: json.RawMessage(`{"path":"README.md"}`)},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-pending", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: p1.policy}
	if _, err := ReplayPredecessorContext(journal, pins, nil, boundary); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("pending tool call: err=%v, want ErrTranscriptReplay", err)
	}

	empty := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "empty.audit"))
	if _, err := ReplayPredecessorContext(empty, pins, nil, boundary); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("empty predecessor: err=%v, want ErrTranscriptReplay", err)
	}
	for name, bad := range map[string]TurnBoundary{
		"unknown outcome":   {PredecessorReceiptDigest: boundary.PredecessorReceiptDigest, Outcome: "awaiting_review", ProjectBaselineDigest: boundary.ProjectBaselineDigest},
		"bare receipt hex":  {PredecessorReceiptDigest: strings.TrimPrefix(boundary.PredecessorReceiptDigest, "sha256:"), Outcome: "no_diff", ProjectBaselineDigest: boundary.ProjectBaselineDigest},
		"uppercase receipt": {PredecessorReceiptDigest: strings.ToUpper(boundary.PredecessorReceiptDigest), Outcome: "no_diff", ProjectBaselineDigest: boundary.ProjectBaselineDigest},
		"bad baseline":      {PredecessorReceiptDigest: boundary.PredecessorReceiptDigest, Outcome: "no_diff", ProjectBaselineDigest: "tree-0"},
	} {
		if _, err := ReplayPredecessorContext(p1.journal, p1.pins(), &replayVerifierFixture{}, bad); err == nil {
			t.Errorf("%s: boundary accepted", name)
		}
		if _, err := ExtendPredecessorContext(PredecessorContext{}, bad); err == nil {
			t.Errorf("%s: extension accepted", name)
		}
	}
}

func TestSuccessorRefusesReusedPredecessorIdentities(t *testing.T) {
	p1 := runPredecessor(t)
	predecessor, err := ReplayPredecessorContext(p1.journal, p1.pins(), &replayVerifierFixture{}, testBoundary("p1", "no_diff"))
	if err != nil {
		t.Fatal(err)
	}
	start := func(t *testing.T, turn ScriptTurn) (*Conversation, *audit.Journal) {
		t.Helper()
		journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "p2.audit"))
		conversation, err := newScripted(successorConfig(predecessor), journal, &recordingSource{inner: NewStaticScript([]ScriptTurn{turn})})
		if err != nil {
			t.Fatal(err)
		}
		if err := conversation.AdmitPrompt("task-2", "turn two prompt"); err != nil {
			t.Fatal(err)
		}
		return conversation, journal
	}
	for name, turn := range map[string]ScriptTurn{
		"tool-call ID": {ResponseID: "p2-response", CallID: "p1-call-read", Tool: "read", Arguments: json.RawMessage(`{"path":"README.md"}`)},
		"response ID":  {ResponseID: "p1-response-final", Text: "reused"},
	} {
		t.Run(name, func(t *testing.T) {
			conversation, _ := start(t, turn)
			if _, err := conversation.NextTurn(context.Background()); !errors.Is(err, ErrClosed) {
				t.Fatalf("reused predecessor %s: err=%v, want refusal", name, err)
			}
			if err := conversation.AdmitPrompt("task-3", "after refusal"); !errors.Is(err, ErrClosed) {
				t.Fatalf("conversation stayed open after refusal: %v", err)
			}
		})
	}
	t.Run("a Pi proposal with a predecessor call ID does not correlate", func(t *testing.T) {
		conversation, _ := start(t, ScriptTurn{ResponseID: "p2-response", CallID: "p2-call-read", Tool: "read", Arguments: json.RawMessage(`{"path":"README.md"}`)})
		turn, err := conversation.NextTurn(context.Background())
		if err != nil || turn.ToolCall == nil || turn.ToolCall.ID != "p2-call-read" {
			t.Fatalf("fresh call turn=%+v err=%v", turn, err)
		}
		stale := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "p1-call-read", Tool: "read",
			Arguments: json.RawMessage(`{"path":"README.md"}`)}
		decision, _ := conversation.Correlate(context.Background(), stale)
		if decision.Accepted || decision.BrokerCapture != nil || !decision.StreamClosed {
			t.Fatalf("stale predecessor call correlated: %+v", decision)
		}
	})
}

func TestOversizedPredecessorIsRefusedBeforeAnyJournalWrite(t *testing.T) {
	huge := strings.Repeat("x", MaxTranscriptBytes)
	many := make([]importedMessage, MaxTranscriptTurns*3+1)
	for i := range many {
		text := fmt.Sprintf("message %d", i)
		many[i] = importedMessage{Role: "user", Content: &text}
	}
	for name, messages := range map[string][]importedMessage{
		"bytes":    {{Role: "user", Content: &huge}},
		"messages": many,
	} {
		t.Run(name, func(t *testing.T) {
			predecessor := newPredecessorContext(sha256Prefixed("receipt"), messages, nil, nil)
			journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "p2.audit"))
			_, err := newScripted(successorConfig(predecessor), journal, &recordingSource{inner: NewStaticScript(nil)})
			if !errors.Is(err, ErrContextExhausted) {
				t.Fatalf("oversized prefix: err=%v, want ErrContextExhausted", err)
			}
			trace, err := journal.Trace()
			if err != nil || len(trace.Records) != 0 {
				t.Fatalf("journal records=%d err=%v", len(trace.Records), err)
			}
		})
	}
}

func TestPrefixWithoutRoomForThePromptIsRefusedBeforeAnyJournalWrite(t *testing.T) {
	full := make([]importedMessage, MaxTranscriptTurns*3)
	for i := range full {
		text := fmt.Sprintf("message %d", i)
		full[i] = importedMessage{Role: "user", Content: &text}
	}
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "p2.audit"))
	conversation, err := newScripted(successorConfig(newPredecessorContext(sha256Prefixed("receipt"), full, nil, nil)), journal,
		&recordingSource{inner: NewStaticScript(nil)})
	if err != nil {
		t.Fatalf("a prefix at the message cap was refused at construction: %v", err)
	}
	if err := conversation.AdmitPrompt("task-full", "one more prompt"); !errors.Is(err, ErrContextExhausted) {
		t.Fatalf("prompt past the message cap: err=%v, want ErrContextExhausted", err)
	}
	if trace, err := journal.Trace(); err != nil || len(trace.Records) != 0 {
		t.Fatalf("exhausted admission journaled records=%d err=%v", len(trace.Records), err)
	}
}

func TestExtendPredecessorContextChainsBoundaries(t *testing.T) {
	zero := PredecessorContext{}
	if !zero.IsZero() || zero.Digest() != "" || zero.PredecessorReceiptDigest() != "" {
		t.Fatal("zero context is not empty")
	}
	first, err := ExtendPredecessorContext(zero, testBoundary("failed turn", "failed"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExtendPredecessorContext(first, testBoundary("cancelled turn", "cancelled"))
	if err != nil {
		t.Fatal(err)
	}
	if second.PredecessorReceiptDigest() != sha256Prefixed("receipt cancelled turn") || second.Digest() == first.Digest() {
		t.Fatalf("chained context digest=%s receipt=%s", second.Digest(), second.PredecessorReceiptDigest())
	}
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "p.audit"))
	conversation, err := newScripted(successorConfig(second), journal, &recordingSource{inner: NewStaticScript([]ScriptTurn{{ResponseID: "r", Text: "ok"}})})
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task", "next prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := firstRequestMessages(t, journal)
	if len(got) != 5 || !strings.Contains(content(got[2]), `outcome "failed": it failed; none of its file changes are in the project.`) ||
		!strings.Contains(content(got[3]), `outcome "cancelled": it was cancelled; none of its file changes are in the project.`) ||
		content(got[4]) != "next prompt" {
		t.Fatalf("first request=%+v", got)
	}
	pins := RecoveryPins{Config: successorConfig(second), Model: ApprovedModel, Policy: recoveryTestPolicy(t, false), ImportedPrefixDigest: second.Digest()}
	if _, err := ReplayTranscript(journal, pins, nil); err != nil {
		t.Fatalf("replay of an extended prefix: %v", err)
	}

	// A legacy conversation (zero Predecessor) writes no import record and replays with an empty pin.
	legacyJournal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "legacy.audit"))
	legacy, err := newScripted(successorConfig(zero), legacyJournal, &recordingSource{inner: NewStaticScript([]ScriptTurn{{ResponseID: "r", Text: "ok"}})})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.AdmitPrompt("task", "next prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	legacyTrace, err := legacyJournal.Trace()
	if err != nil || len(legacyTrace.Records) != 2 || legacyTrace.Records[0].Event.Kind != "provider_user_admitted" {
		t.Fatalf("legacy journal=%+v err=%v", legacyTrace.Records, err)
	}
	if got := firstRequestMessages(t, legacyJournal); len(got) != 3 {
		t.Fatalf("legacy first request=%+v", got)
	}
	legacyPins := RecoveryPins{Config: successorConfig(zero), Model: ApprovedModel, Policy: recoveryTestPolicy(t, false)}
	if _, err := ReplayTranscript(legacyJournal, legacyPins, nil); err != nil {
		t.Fatalf("legacy replay: %v", err)
	}
}

func TestPredecessorDigestCommitsToEveryInput(t *testing.T) {
	base := testBoundary("p", "published")
	variants := map[string]TurnBoundary{"base": base}
	for _, outcome := range []string{"no_diff", "discarded", "failed", "cancelled"} {
		variant := base
		variant.Outcome = outcome
		variants["outcome "+outcome] = variant
	}
	receipt := base
	receipt.PredecessorReceiptDigest = sha256Prefixed("other receipt")
	variants["receipt"] = receipt
	baseline := base
	baseline.ProjectBaselineDigest = recoveryTestTree("tree-other")
	variants["baseline"] = baseline
	seen := map[string]string{}
	for name, boundary := range variants {
		extended, err := ExtendPredecessorContext(PredecessorContext{}, boundary)
		if err != nil {
			t.Fatal(err)
		}
		if other, duplicate := seen[extended.Digest()]; duplicate {
			t.Errorf("%s and %s share digest %s", name, other, extended.Digest())
		}
		seen[extended.Digest()] = name
	}
	message, err := base.message()
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := base.PredecessorReceiptDigest
	for name, extended := range map[string]PredecessorContext{
		"reserved response": newPredecessorContext(receiptDigest, []importedMessage{message}, []string{"r1"}, nil),
		"reserved call":     newPredecessorContext(receiptDigest, []importedMessage{message}, nil, []string{"c1"}),
	} {
		if other, duplicate := seen[extended.Digest()]; duplicate {
			t.Errorf("%s and %s share digest", name, other)
		}
		seen[extended.Digest()] = name
	}
}
