//go:build linux

package providerbridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/sessionrepo"
)

// TestProviderChannelDisclosureOfOutsideReadIsNone is the E06
// ProviderChannelDisclosure evidence for the provider-broker route and the
// outside-workspace-path class. The model asks to read a real file outside the
// workspace whose content is a fresh canary; the canary is the file CONTENT and
// is not part of the path, so the path legitimately reaches the provider history
// in the model's own tool-call arguments while the canary must not.
//
// The test classifies what reaches the provider channel; it does not claim
// provider-channel confidentiality, which is outside H1.
//   - executor denial: the trusted executor fails the read with
//     "durable read: <sessionrepo.ErrInvalidPath>". The wrapper records
//     provider_tool_result_unknown, returns ErrResultWithheld and closes the
//     conversation, so no tool message and no second provider request exist.
//   - gate DENY: the executor is never called; the next provider request carries
//     a DENY tool message without output.
//
// The control uses an executor that really reads the outside file and returns
// its content; there the canary does reach the next provider request, so the
// request-body detector cannot pass vacuously.
func TestProviderChannelDisclosureOfOutsideReadIsNone(t *testing.T) {
	canary := e06ProviderCanary(t)
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte(canary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]string{"path": outsideFile})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(arguments, []byte(canary)) {
		t.Fatal("fixture error: the canary is part of the proposed path")
	}
	responses := []fixtureResponse{
		{responseID: "response-outside-read", callID: "call-outside-read", tool: "read", arguments: arguments},
		{responseID: "response-final", text: "done"},
	}

	t.Run("executor denial withholds the result and closes the conversation", func(t *testing.T) {
		executor := &e06OutsideReadExecutor{}
		_, policy, journal, conversation, wrapped, doer := newDenialFixture(t, true, responses, executor)
		_, decision, err := driveDenialTurn(t, conversation, wrapped, policy, true)
		if decision.Verdict != gate.Allow || executor.calls != 1 {
			t.Fatalf("gate verdict %q, executor calls %d; want ALLOW and one durable read attempt", decision.Verdict, executor.calls)
		}
		if !errors.Is(err, ErrResultWithheld) || !errors.Is(err, sessionrepo.ErrInvalidPath) {
			t.Fatalf("Execute = %v, want ErrResultWithheld wrapping %v", err, sessionrepo.ErrInvalidPath)
		}
		if _, err := conversation.NextTurn(context.Background()); !errors.Is(err, ErrClosed) {
			t.Fatalf("conversation after the withheld read = %v, want %v", err, ErrClosed)
		}
		requests := e06ProviderRequests(doer)
		if len(requests) != 1 {
			t.Fatalf("provider requests = %d, want exactly the one that proposed the read", len(requests))
		}
		e06AssertNoCanary(t, requests, canary)
		if tools := e06ToolMessages(t, requests[0]); len(tools) != 0 {
			t.Fatalf("the only provider request carries %d tool messages", len(tools))
		}
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		if last := trace.Records[len(trace.Records)-1].Event.Kind; last != "provider_tool_result_unknown" {
			t.Fatalf("last journal record %q, want provider_tool_result_unknown", last)
		}
		for _, record := range trace.Records {
			if bytes.Contains(record.Event.Data, []byte(canary)) {
				t.Fatalf("journal record %q carries the canary", record.Event.Kind)
			}
		}
		t.Logf("disclosure classification: provider requests=1, tool messages=0, result=withheld (provider_tool_result_unknown), canary bytes disclosed=none")
	})

	t.Run("gate DENY sends a tool message without output", func(t *testing.T) {
		executor := &e06LeakingReadExecutor{}
		_, policy, _, conversation, wrapped, doer := newDenialFixture(t, false, responses, executor)
		_, decision, err := driveDenialTurn(t, conversation, wrapped, policy, true)
		if err != nil || decision.Verdict != gate.Deny {
			t.Fatalf("gate decision %q, %v; want DENY", decision.Verdict, err)
		}
		if executor.calls != 0 {
			t.Fatalf("a gate-denied read reached the executor %d times", executor.calls)
		}
		turn, err := conversation.NextTurn(context.Background())
		if err != nil || turn.ToolCall != nil {
			t.Fatalf("turn after the DENY = %+v, %v", turn, err)
		}
		requests := e06ProviderRequests(doer)
		if len(requests) != 2 {
			t.Fatalf("provider requests = %d, want 2", len(requests))
		}
		e06AssertNoCanary(t, requests, canary)
		tools := e06ToolMessages(t, requests[1])
		if len(tools) != 1 {
			t.Fatalf("the request after the DENY carries %d tool messages, want 1", len(tools))
		}
		var object map[string]json.RawMessage
		raw, hint, _ := strings.Cut(tools[0], "\nhint: ")
		if hint != permittedAlternativeHints["policy_default_deny"] {
			t.Fatalf("unexpected denial hint: %q", hint)
		}
		if err := json.Unmarshal([]byte(raw), &object); err != nil {
			t.Fatalf("tool message is not a JSON Result: %v", err)
		}
		if output, present := object["output"]; present && string(output) != "null" {
			t.Fatalf("DENY tool message carries output %s", output)
		}
		result, err := protocol.DecodeResult([]byte(raw))
		if err != nil || result.Verdict != "DENY" || len(result.Output) != 0 {
			t.Fatalf("DENY tool message = %+v, %v", result, err)
		}
		_, present := object["output"]
		t.Logf("disclosure classification: provider requests=2, tool messages=1 (verdict=%s reason=%s output key present=%t), canary bytes disclosed=none",
			result.Verdict, result.ReasonCode, present)
	})

	t.Run("control: an undenied outside read reaches the provider", func(t *testing.T) {
		executor := &e06LeakingReadExecutor{}
		_, policy, _, conversation, wrapped, doer := newDenialFixture(t, true, responses, executor)
		if _, _, err := driveDenialTurn(t, conversation, wrapped, policy, true); err != nil || executor.calls != 1 {
			t.Fatalf("leaking control Execute = %v (calls %d)", err, executor.calls)
		}
		if _, err := conversation.NextTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
		requests := e06ProviderRequests(doer)
		if len(requests) != 2 || !bytes.Contains(requests[1], []byte(canary)) {
			t.Fatalf("control: the leaked canary did not reach the next provider request (%d requests)", len(requests))
		}
	})
}

func e06ProviderCanary(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return "tbound-e06-provider-canary-" + hex.EncodeToString(raw[:])
}

// e06OutsideReadExecutor fails every read the way the durable executor fails a
// read whose path leaves the workspace grammar.
type e06OutsideReadExecutor struct{ calls int }

func (e *e06OutsideReadExecutor) Execute(_ context.Context, proposal protocol.Proposal, _ gate.Decision) (json.RawMessage, error) {
	e.calls++
	if proposal.Tool != "read" {
		return nil, fmt.Errorf("unexpected tool %q", proposal.Tool)
	}
	return nil, fmt.Errorf("durable read: %w", sessionrepo.ErrInvalidPath)
}

// e06LeakingReadExecutor is the denial-removed mutation: it reads the proposed
// path from the host filesystem and returns the content as a successful read.
type e06LeakingReadExecutor struct{ calls int }

func (e *e06LeakingReadExecutor) Execute(_ context.Context, proposal protocol.Proposal, _ gate.Decision) (json.RawMessage, error) {
	e.calls++
	var arguments struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(proposal.Arguments, &arguments); err != nil {
		return nil, err
	}
	content, err := os.ReadFile(arguments.Path)
	if err != nil {
		return nil, err
	}
	return readOutputFor(arguments.Path, "g0", recoveryTestTree("tree-0"), string(content)), nil
}

func e06ProviderRequests(doer *fixtureDoer) [][]byte {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	out := make([][]byte, len(doer.requests))
	for index, request := range doer.requests {
		out[index] = append([]byte(nil), request...)
	}
	return out
}

func e06AssertNoCanary(t *testing.T, requests [][]byte, canary string) {
	t.Helper()
	for index, request := range requests {
		if bytes.Contains(request, []byte(canary)) {
			t.Fatalf("provider request %d carries the outside canary", index)
		}
	}
}

// e06ToolMessages returns the content of every tool-role message in one
// provider request body.
func e06ToolMessages(t *testing.T, raw []byte) []string {
	t.Helper()
	var request struct {
		Messages []struct {
			Role    string  `json:"role"`
			Content *string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	var tools []string
	for _, message := range request.Messages {
		if message.Role == "tool" {
			if message.Content == nil {
				t.Fatal("tool message without content")
			}
			tools = append(tools, *message.Content)
		}
	}
	return tools
}
