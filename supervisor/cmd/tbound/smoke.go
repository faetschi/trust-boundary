package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	providerbroker "tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
)

const (
	smokeSocketName = "tbound.sock"
	smokeTokenName  = "tbound.token"
	smokeModel      = "synthetic/no-provider"
	smokeGeneration = "synthetic-g0"
)

type smokeCall struct {
	callID     string
	responseID string
	tool       string
	arguments  json.RawMessage
}

var smokeCalls = [...]smokeCall{
	{callID: "smoke-read", responseID: "smoke-response-read", tool: "read", arguments: json.RawMessage(`{"path":"README.md"}`)},
	{callID: "smoke-write", responseID: "smoke-response-write", tool: "write", arguments: json.RawMessage(`{"path":"smoke-output.txt","content":"synthetic; no filesystem write"}`)},
	{callID: "smoke-edit", responseID: "smoke-response-edit", tool: "edit", arguments: json.RawMessage(`{"path":"README.md","edits":[{"oldText":"before","newText":"after"}]}`)},
	{callID: "smoke-bash", responseID: "smoke-response-bash", tool: "bash", arguments: json.RawMessage(`{"command":"printf synthetic smoke","timeout":1}`)},
}

type scriptedDoer struct {
	responses [][]byte
	next      int
}

func (d *scriptedDoer) Do(request *http.Request) (*http.Response, error) {
	if d == nil || d.next >= len(d.responses) {
		return nil, errors.New("synthetic HTTP Doer has no scripted response remaining")
	}
	if request == nil || request.Method != http.MethodPost || request.URL == nil || request.URL.String() != openrouter.Endpoint {
		return nil, errors.New("synthetic HTTP Doer received an unexpected request")
	}
	if _, err := io.Copy(io.Discard, request.Body); err != nil {
		return nil, fmt.Errorf("read synthetic request body: %w", err)
	}
	responseBody := append([]byte(nil), d.responses[d.next]...)
	d.next++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Request:    request,
	}, nil
}

type noEffectExecutor struct{}

func (noEffectExecutor) Execute(ctx context.Context, _ protocol.Proposal, decision gate.Decision) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if decision.Verdict != gate.Allow {
		return nil, errors.New("no-effect executor refuses a non-allow decision")
	}
	return json.RawMessage(`{"status":"stubbed-no-effect"}`), nil
}

func newSyntheticRuntime() (*providerbroker.Broker, gate.Policy, Executor, error) {
	responses := make([][]byte, 0, len(smokeCalls))
	for _, call := range smokeCalls {
		response, err := syntheticSSE(call)
		if err != nil {
			return nil, gate.Policy{}, nil, fmt.Errorf("encode synthetic SSE: %w", err)
		}
		responses = append(responses, response)
	}
	prompt := "Synthetic IPC integration fixture; never sent to a provider."
	broker, err := providerbroker.New(providerbroker.Profile{
		ID: "tbound-synthetic-ipc-smoke-v1", Model: smokeModel,
		Messages:       []openrouter.Message{{Role: openrouter.User, Content: &prompt}},
		ToolManifest:   providerbroker.DeclaredToolManifest(),
		ToolCallIssuer: "fixture/synthetic-tool-call/v1", ResponseIDIssuer: "fixture/synthetic-response/v1",
		Generation: smokeGeneration,
	}, &scriptedDoer{responses: responses})
	if err != nil {
		return nil, gate.Policy{}, nil, fmt.Errorf("create synthetic broker: %w", err)
	}
	for index, expected := range smokeCalls {
		captured, err := broker.Exchange(context.Background())
		if err != nil {
			return nil, gate.Policy{}, nil, fmt.Errorf("capture synthetic SSE %d: %w", index+1, err)
		}
		actual, ok := captured.ToolCall()
		if !ok || actual.ID != expected.callID || actual.Name != expected.tool || !bytes.Equal(actual.RawArguments, expected.arguments) {
			return nil, gate.Policy{}, nil, fmt.Errorf("synthetic SSE %d did not capture the scripted proposal", index+1)
		}
	}
	rules := make([]gate.Rule, 0, len(smokeCalls))
	for _, call := range smokeCalls {
		rules = append(rules, gate.Rule{Tool: call.tool, Effect: gate.EffectAllow})
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: rules})
	if err != nil {
		return nil, gate.Policy{}, nil, fmt.Errorf("compile synthetic smoke policy: %w", err)
	}
	return broker, policy, noEffectExecutor{}, nil
}

func syntheticSSE(call smokeCall) ([]byte, error) {
	chunk := func(delta any, finish any) []byte {
		payload, err := json.Marshal(map[string]any{
			"id": call.responseID, "model": smokeModel, "created": 1,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		if err != nil {
			return nil
		}
		return append(append([]byte("data: "), payload...), []byte("\n\n")...)
	}
	first := chunk(map[string]any{
		"role": "assistant",
		"tool_calls": []any{map[string]any{
			"index": 0, "id": call.callID, "type": "function",
			"function": map[string]any{"name": call.tool, "arguments": string(call.arguments)},
		}},
	}, nil)
	if first == nil {
		return nil, errors.New("marshal initial synthetic SSE event")
	}
	finish := chunk(map[string]any{}, "tool_calls")
	if finish == nil {
		return nil, errors.New("marshal synthetic SSE finish event")
	}
	return append(append(first, finish...), []byte("data: [DONE]\n\n")...), nil
}

func runSyntheticListener(ctx context.Context, socketDir string, transcript io.Writer) error {
	if err := validatePrivateSocketDirectory(socketDir); err != nil {
		return err
	}
	broker, policy, executor, err := newSyntheticRuntime()
	if err != nil {
		return err
	}
	token, err := ipc.NewBindingToken()
	if err != nil {
		return err
	}
	socketPath := filepath.Join(socketDir, smokeSocketName)
	listener, err := ipc.ListenUnix(socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := verifySmokeSocket(socketPath); err != nil {
		return err
	}
	tokenPath := filepath.Join(socketDir, smokeTokenName)
	if err := createPrivateTokenFile(tokenPath, token); err != nil {
		return err
	}
	defer os.Remove(tokenPath)
	stopClose := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stopClose()

	server, err := listener.Accept(token)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	session := &Supervisor{
		IPC: server, Broker: broker, Policy: policy, Executor: executor,
		Transcript: transcript, ProposalLimit: uint64(len(smokeCalls)),
	}
	if err := session.Serve(ctx); err != nil {
		return err
	}
	if session.handledProposals != uint64(len(smokeCalls)) {
		return fmt.Errorf("synthetic IPC session ended after %d proposals; expected %d", session.handledProposals, len(smokeCalls))
	}
	return nil
}

func validatePrivateSocketDirectory(path string) error {
	if path == "" {
		return errors.New("socket directory is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect caller-provided socket directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("caller-provided socket directory must be a non-symlink mode-0700 directory")
	}
	return nil
}

func createPrivateTokenFile(path, token string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create private IPC token file: %w", err)
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("set IPC token file permissions: %w", err)
	}
	if _, err := io.WriteString(file, token); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write IPC token file: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = os.Remove(path)
		return errors.New("IPC token file is not a regular mode-0600 file")
	}
	return nil
}

func verifySmokeSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect created IPC socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return errors.New("created IPC endpoint is not a mode-0600 Unix socket")
	}
	return nil
}

func smokeProposals() []protocol.Proposal {
	proposals := make([]protocol.Proposal, 0, len(smokeCalls))
	for _, call := range smokeCalls {
		proposals = append(proposals, protocol.Proposal{
			SchemaVersion: protocol.ProposalSchemaVersion,
			ToolCallID:    call.callID, Tool: call.tool,
			Arguments: append(json.RawMessage(nil), call.arguments...),
		})
	}
	return proposals
}

func smokeTokenFilePath(socketDir string) string { return filepath.Join(socketDir, smokeTokenName) }
