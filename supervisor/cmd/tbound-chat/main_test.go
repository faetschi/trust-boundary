package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/sessioncontrol"
)

func TestStartupBannerNamesFixtureChatAndURL(t *testing.T) {
	var buffer bytes.Buffer
	writeStartupBanner(&buffer, "127.0.0.1:8788", ".tbound-chat-token")
	out := buffer.String()
	for _, want := range []string{
		"tbound-chat", "FIXTURE MODE", "non-claim-bearing",
		"http://127.0.0.1:8788/", ".tbound-chat-token",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("startup banner missing %q in:\n%s", want, out)
		}
	}
}

func TestLauncherRefusesRealModeWithoutExplicitFixture(t *testing.T) {
	if err := run(nil); err != sessioncontrol.ErrRealLaunchRefused {
		t.Fatalf("launcher refusal = %v", err)
	}
}

func TestChatDisplayHasBoundsAndReplayDeduplication(t *testing.T) {
	for _, required := range []string{"maxRenderedEntries=512", "maxRenderedText=16384", "log.firstElementChild.remove()", "event.event_id<=cursor", "overflow-wrap:anywhere"} {
		if !strings.Contains(string(chatHTML), required) {
			t.Fatalf("missing bounded display behavior: %s", required)
		}
	}
}

func TestFixtureLauncherRequiresOwnerPrivatePairingMetadata(t *testing.T) {
	if err := run([]string{"--fixture", "--addr", "127.0.0.1:8799"}); err == nil || !strings.Contains(err.Error(), "--token-file") {
		t.Fatalf("fixture launch without secure pairing metadata = %v", err)
	}
}

func TestWorkerOutputSchemaRejectsDuplicateAndUnknownFields(t *testing.T) {
	if _, err := decodeWorkerOutput([]byte(`{"type":"turn_end","request_id":"r","request_id":"replay"}`)); err == nil {
		t.Fatal("duplicate child output field was accepted")
	}
	if _, err := decodeWorkerOutput([]byte(`{"type":"turn_end","request_id":"r","extra":true}`)); err == nil {
		t.Fatal("unknown child output field was accepted")
	}
	if _, err := decodeWorkerOutput([]byte(`{"type":"turn_end","request_id":"r"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerEnvironmentDoesNotInheritProviderCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy-provider-secret")
	t.Setenv("NODE_OPTIONS", "--require=untrusted.js")
	for _, value := range minimalWorkerEnv(`C:\\Program Files\\nodejs\\node.exe`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		if strings.HasPrefix(value, "OPENAI_API_KEY=") || strings.HasPrefix(value, "NODE_OPTIONS=") {
			t.Fatalf("secret or Node option inherited by worker: %q", value)
		}
	}
}

func TestFixtureIPCUsesExistingCorrelationAndGateForAllFourTools(t *testing.T) {
	token, err := ipc.NewBindingToken()
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := ipc.NewPipe(token)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow},
		{Tool: "edit", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var decisionMu sync.Mutex
	var decisions []sessioncontrol.ToolDecision
	broker, err := newFixtureBroker("fixture-generation-1")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		done <- (&supervisorAdapter{server: server, broker: broker, policy: policy, generation: "fixture-generation-1", toolDecision: func(decision sessioncontrol.ToolDecision) {
			decisionMu.Lock()
			decisions = append(decisions, decision)
			decisionMu.Unlock()
		}}).serve(ctx)
	}()
	defer cancel()
	for _, capture := range fixtureCapturePlan {
		proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: capture.ToolCallID, Tool: capture.Tool, Arguments: capture.Arguments}
		if err := client.SendProposal(proposal); err != nil {
			t.Fatal(err)
		}
		result, err := client.ReceiveResult()
		if err != nil {
			t.Fatal(err)
		}
		if result.Verdict != "ALLOW" || result.ReasonCode != "policy_rule_allow" || result.ResponseID == nil {
			t.Fatalf("%s result = %+v", capture.Tool, result)
		}
		if string(result.Output) != `{"mode":"fixture","effect":"not-executed"}` {
			t.Fatalf("%s effect output = %s", capture.Tool, result.Output)
		}
	}
	decisionMu.Lock()
	defer decisionMu.Unlock()
	if len(decisions) != len(fixtureCapturePlan)*2 {
		t.Fatalf("tool lifecycle decisions = %d, want %d", len(decisions), len(fixtureCapturePlan)*2)
	}
	for index, decision := range decisions {
		wantStatus := "completed"
		if index%2 == 0 {
			wantStatus = "pending"
		}
		if decision.Status != wantStatus || decision.Generation != "fixture-generation-1" {
			t.Fatalf("decision %d = %+v", index, decision)
		}
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fixture IPC supervisor did not close")
	}
}

func TestFixtureBrokerRejectsReplayThroughExistingStream(t *testing.T) {
	broker, err := newFixtureBroker("fixture-generation-1")
	if err != nil {
		t.Fatal(err)
	}
	capture := fixtureCapturePlan[0]
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: capture.ToolCallID, Tool: capture.Tool, Arguments: capture.Arguments}
	first, err := broker.Correlate(context.Background(), proposal)
	if err != nil || !first.Accepted {
		t.Fatalf("first fixture correlation = %+v, %v", first, err)
	}
	second, err := broker.Correlate(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	if second.Accepted || !second.StreamClosed {
		t.Fatalf("replay was accepted: %+v", second)
	}
}

func TestFixtureBrokerRejectsWrongArgumentsAndUnknownProviderIDs(t *testing.T) {
	broker, err := newFixtureBroker("fixture-generation-1")
	if err != nil {
		t.Fatal(err)
	}
	capture := fixtureCapturePlan[0]
	wrong := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: capture.ToolCallID, Tool: capture.Tool, Arguments: json.RawMessage(`{"path":"wrong.txt","offset":1,"limit":4}`)}
	decision, err := broker.Correlate(context.Background(), wrong)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Accepted || decision.ReasonCode != "argument_digest_mismatch" {
		t.Fatalf("wrong arguments decision = %+v", decision)
	}

	broker, err = newFixtureBroker("fixture-generation-1")
	if err != nil {
		t.Fatal(err)
	}
	unknown := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "unknown-provider-call", Tool: "read", Arguments: capture.Arguments}
	decision, err = broker.Correlate(context.Background(), unknown)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Accepted || decision.ReasonCode != "unknown_call" {
		t.Fatalf("unknown provider call decision = %+v", decision)
	}
}
