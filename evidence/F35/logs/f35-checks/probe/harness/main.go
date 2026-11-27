// Command f35probe is the D63-amendment item 1 exchange probe (thesis #71 reading). It lives only in a
// throwaway copy of the F35 candidate tree and changes no frozen package.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/researchroute"
)

// Copied verbatim from supervisor/cmd/tbound/production_binder_linux.go:54-55, which corpus workflows use.
const (
	systemPrompt    = "You are the governed, offline-first TBound Pi worker. Use only the registered proxy tools."
	developerPrompt = "Use only the registered read, write, edit, and bash proxy tools. Never invent effects."
)

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: f35probe genesis|snapshot|b|v|stub2 [flags]"))
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfg := fs.String("cfg", "", "directory with research-profile.json, research-ledger.json, openrouter.key")
	stub := fs.String("stub", "", "loopback stub endpoint; empty means the real route of the frozen profile")
	out := fs.String("out", "", "new output directory of this run")
	hostCalls := fs.Uint64("host-calls", 11, "host-call budget of the conversation (11 = compliant max_host_calls)")
	taskFile := fs.String("task", "", "task prompt file (b)")
	node := fs.String("node", "", "node binary (v)")
	script := fs.String("script", "", "pi-ai V client script (v)")
	port := fs.Int("port", 18444, "stub2 port")
	_ = fs.Parse(os.Args[2:])
	if cmd == "stub2" {
		fail(serveStub2(*port, *out))
	}
	if *cfg == "" {
		fail(errors.New("-cfg is required"))
	}
	paths := researchroute.Paths{
		Profile:      filepath.Join(*cfg, "research-profile.json"),
		LedgerConfig: filepath.Join(*cfg, "research-ledger.json"),
		Ledger:       filepath.Join(*cfg, "usd-ledger"),
		Checkpoint:   filepath.Join(*cfg, "research-ledger-checkpoint.json"),
		Key:          filepath.Join(*cfg, "openrouter.key"),
		OwnerUID:     uint32(os.Getuid()),
	}
	if cmd == "genesis" {
		snap, err := researchroute.Genesis(paths)
		if err != nil {
			fail(err)
		}
		checkpoint := fmt.Sprintf(`{"records":%d,"head_sha256":%q}`, snap.Records, snap.HeadSHA256)
		if err := os.WriteFile(paths.Checkpoint, []byte(checkpoint), 0o600); err != nil {
			fail(err)
		}
		printJSON("genesis", snap)
		return
	}
	var bundle *researchroute.Bundle
	var err error
	if *stub != "" {
		bundle, err = researchroute.OpenStub(paths, *stub)
	} else {
		bundle, err = researchroute.Open(paths)
	}
	if err != nil {
		fail(err)
	}
	defer bundle.Close()
	before, err := bundle.Snapshot()
	if err != nil {
		fail(err)
	}
	printJSON("ledger-before", before)
	if cmd == "snapshot" {
		return
	}
	if cmd == "why" {
		fail(why(bundle, *taskFile, *out))
		return
	}
	if *out == "" {
		fail(errors.New("-out is required"))
	}
	if err := os.Mkdir(*out, 0o700); err != nil {
		fail(err)
	}
	switch cmd {
	case "b":
		task, err := os.ReadFile(*taskFile)
		if err != nil {
			fail(err)
		}
		err = runB(bundle, *out, strings.TrimSpace(string(task)), *hostCalls)
		report(bundle)
		if err != nil {
			fail(err)
		}
	case "v":
		err := runV(bundle, *out, *node, *script, *hostCalls)
		report(bundle)
		if err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown command %q", cmd))
	}
}

// why runs the frozen forwarder's request builder offline (no network, no ledger write) on a saved V request
// body, then on copies with one member of one message removed, and names each removal that makes it
// acceptable. An accepted original is written out as the exact forwarded body.
func why(bundle *researchroute.Bundle, bodyPath, outPath string) error {
	raw, err := os.ReadFile(bodyPath)
	if err != nil {
		return err
	}
	build := func(body []byte) error {
		_, err := openrouter.BuildForwardedRequest(bundle.Route(), body, bundle.MaxCompletionTokens())
		return err
	}
	if built, err := openrouter.BuildForwardedRequest(bundle.Route(), raw, bundle.MaxCompletionTokens()); err == nil {
		fmt.Println("original: accepted")
		if outPath != "" {
			return os.WriteFile(outPath, built.Body(), 0o600)
		}
		return nil
	} else {
		fmt.Println("original: refused:", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(top["messages"], &messages); err != nil {
		return err
	}
	for i, message := range messages {
		for member := range message {
			if member == "role" {
				continue
			}
			trimmed := make(map[string]json.RawMessage, len(message))
			for k, v := range message {
				if k != member {
					trimmed[k] = v
				}
			}
			variant := append([]map[string]json.RawMessage(nil), messages...)
			variant[i] = trimmed
			encoded, _ := json.Marshal(variant)
			copyTop := make(map[string]json.RawMessage, len(top))
			for k, v := range top {
				copyTop[k] = v
			}
			copyTop["messages"] = encoded
			body, _ := json.Marshal(copyTop)
			if build(body) == nil {
				fmt.Printf("accepted without messages[%d].%s\n", i, member)
			}
		}
	}
	return nil
}

func report(bundle *researchroute.Bundle) {
	after, err := bundle.Snapshot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "snapshot after:", err)
		return
	}
	printJSON("ledger-after", after)
}

// runB drives the B conversation path: the bridge, the strict parser, the spend accountant and host-call
// accounting, with a harness executor over an in-memory file tree in place of the Store.
func runB(bundle *researchroute.Bundle, out, task string, budget uint64) error {
	journal, err := audit.Open(filepath.Join(out, "provider.jsonl"))
	if err != nil {
		return err
	}
	defer journal.Close()
	credentials, err := bundle.Credentials()
	if err != nil {
		return err
	}
	exec := newTreeExecutor(map[string]string{"README.md": "# probe\n\nThis tool will recieve input from the user.\n"})
	var mu sync.Mutex
	left, charged := budget, uint64(0)
	conversation, err := providerbridge.NewFromCredentials(providerbridge.Config{
		ConversationID: "f35-probe-conversation-" + filepath.Base(out), WorkflowID: "f35-probe-workflow-" + filepath.Base(out), ProfileID: "f35-probe",
		InitialGenerationID: exec.generation, InitialTreeDigest: exec.tree,
		SystemPrompt: systemPrompt, DeveloperPrompt: developerPrompt,
		Route: bundle.Route().WithReasoning(true), Spend: bundle, // as productionProviderRoute.Route(), research_route_linux.go:73
		SelectCompletionTokenLimit: func(context.Context) (uint64, error) { return bundle.MaxCompletionTokens(), nil },
		ReserveHostCall: func(_ context.Context, request broker.RequestRecord, limit uint64) (providerbridge.HostCallBinding, error) {
			mu.Lock()
			defer mu.Unlock()
			if left == 0 {
				return providerbridge.HostCallBinding{}, errors.New("f35probe host-call budget used")
			}
			left--
			charged++
			return providerbridge.HostCallBinding{CallID: fmt.Sprintf("f35-call-%d", charged),
				RequestDigest: broker.DigestRequestRecord(request), TokenUpperBound: limit}, nil
		},
		CompleteHostCall: func(context.Context, providerbridge.HostCallCompletion) error { return nil },
		HostCallsLeft:    func() uint64 { mu.Lock(); defer mu.Unlock(); return left },
	}, credentials, journal)
	if err != nil {
		return err
	}
	defer conversation.Close()
	if err := conversation.AdmitPrompt("f35-probe-task", task); err != nil {
		return err
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow},
		{Tool: "edit", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow},
	}})
	if err != nil {
		return err
	}
	executor, err := providerbridge.NewExecutor(conversation, exec)
	if err != nil {
		return err
	}
	turns, err := os.OpenFile(filepath.Join(out, "turns.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer turns.Close()
	ctx := context.Background()
	for i := 0; ; i++ {
		mu.Lock()
		leftBefore := left
		mu.Unlock()
		turn, turnErr := conversation.NextTurn(ctx)
		line := map[string]any{"index": i, "host_calls_left_before": leftBefore, "turn": turn}
		if turnErr != nil {
			line["error"] = turnErr.Error()
		}
		writeLine(turns, line)
		if turnErr != nil {
			return fmt.Errorf("turn %d: %w", i, turnErr)
		}
		if turn.ToolCall == nil {
			return nil
		}
		proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
			Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
		matched, err := conversation.Correlate(ctx, proposal)
		if err != nil || !matched.Accepted || matched.StreamClosed {
			return fmt.Errorf("turn %d correlate: %+v %v", i, matched, err)
		}
		decision := gate.Evaluate(proposal, matched, policy)
		if err := conversation.RecordDecision(ctx, proposal, decision); err != nil {
			return fmt.Errorf("turn %d decision: %w", i, err)
		}
		if decision.Verdict != gate.Allow {
			continue
		}
		if _, err := executor.Execute(ctx, proposal, decision); err != nil {
			return fmt.Errorf("turn %d execute: %w", i, err)
		}
	}
}

// treeExecutor answers read, write, edit and bash over an in-memory tree in the executor output shape that
// providerbridge validates (the same shape as its own test fixtures). bash runs nothing: the probe reports
// only its exit, as a --bash-no-outputs unit does.
type treeExecutor struct {
	files      map[string]string
	n          int
	generation string
	tree       string
	sequence   uint64
}

func newTreeExecutor(files map[string]string) *treeExecutor {
	e := &treeExecutor{files: files}
	e.advance()
	return e
}

func (e *treeExecutor) advance() {
	names := make([]string, 0, len(e.files))
	for name := range e.files {
		names = append(names, name)
	}
	sort.Strings(names)
	sum := sha256.New()
	for _, name := range names {
		fmt.Fprintf(sum, "%s\x00%s\x00", name, e.files[name])
	}
	e.generation = fmt.Sprintf("g%d", e.n)
	e.tree = "sha256:" + hex.EncodeToString(sum.Sum(nil))
	e.n++
}

func (e *treeExecutor) Execute(_ context.Context, proposal protocol.Proposal, _ gate.Decision) (json.RawMessage, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Command string `json:"command"`
		Edits   []struct {
			OldText string `json:"oldText"`
			NewText string `json:"newText"`
		} `json:"edits"`
	}
	_ = json.Unmarshal(proposal.Arguments, &args)
	if proposal.Tool == "read" {
		content, ok := e.files[args.Path]
		if !ok {
			content = "error: no such file: " + args.Path
		}
		digest := sha256.Sum256([]byte(content))
		return json.Marshal(map[string]any{"tool": "read", "path": args.Path,
			"generation": map[string]string{"id": e.generation, "tree_digest": e.tree},
			"size": len(content), "content_digest": "sha256:" + hex.EncodeToString(digest[:]), "content": content, "outcome": "success"})
	}
	switch proposal.Tool {
	case "write":
		e.files[args.Path] = args.Content
	case "edit":
		for _, edit := range args.Edits {
			e.files[args.Path] = strings.Replace(e.files[args.Path], edit.OldText, edit.NewText, 1)
		}
	}
	e.advance()
	e.sequence++
	value := map[string]any{"tool": proposal.Tool, "generation": map[string]string{"id": e.generation, "tree_digest": e.tree},
		"transition": map[string]any{"id": fmt.Sprintf("f35-transition-%d", e.sequence), "sequence": e.sequence},
		"effect_id": fmt.Sprintf("f35-effect-%d", e.sequence), "outcome": "success"}
	if proposal.Tool == "bash" {
		value["command"] = map[string]any{"exit_code": 0, "exit_observed": true,
			"command_containment_status": "not-established", "command_runner_profile": "f35-probe-non-claim-bearing"}
	}
	return json.Marshal(value)
}

// runV serves the frozen forwarder on loopback and lets the pinned pi-ai 0.87.1 (the library Pi uses for the
// V unit's openai-completions provider) build and send the requests. Each request and each relayed response
// is kept byte for byte; the unit credential is in the Authorization header only.
func runV(bundle *researchroute.Bundle, out, node, script string, maxHostCalls uint64) error {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	token := hex.EncodeToString(tokenBytes)
	forwarder, err := bundle.NewForwarder("f35-probe-v-"+filepath.Base(out), token, maxHostCalls)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		n++
		k := n
		mu.Unlock()
		_ = os.WriteFile(filepath.Join(out, fmt.Sprintf("v-request-%d.json", k)), body, 0o600)
		r.Body = io.NopCloser(bytes.NewReader(body))
		capture := &captureWriter{ResponseWriter: w, status: http.StatusOK}
		forwarder.ServeHTTP(capture, r)
		_ = os.WriteFile(filepath.Join(out, fmt.Sprintf("v-response-%d.txt", k)),
			append([]byte(fmt.Sprintf("HTTP %d\n", capture.status)), capture.body.Bytes()...), 0o600)
	}))
	defer server.Close()
	cmd := exec.Command(node, script, server.URL+"/v1", out)
	cmd.Env = append(os.Environ(), "TBOUND_V_KEY="+token)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	runErr := cmd.Run()
	totals := forwarder.Finish()
	printJSON("forward-totals", totals)
	return runErr
}

type captureWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *captureWriter) WriteHeader(status int) { c.status = status; c.ResponseWriter.WriteHeader(status) }
func (c *captureWriter) Write(p []byte) (int, error) {
	c.body.Write(p)
	return c.ResponseWriter.Write(p)
}
func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// serveStub2 is a scripted OpenRouter-shaped upstream for the free run: reasoning text with
// reasoning_details, tool calls, then a final answer, each with a usage trailer. The frozen
// stub-upstream.py answers "ok" only, so it cannot exercise a multi-turn tool conversation.
func serveStub2(port int, logPath string) error {
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages   []map[string]json.RawMessage `json:"messages"`
			ToolChoice string                       `json:"tool_choice"`
		}
		_ = json.Unmarshal(raw, &body)
		tools, assistantKeys := 0, []string{}
		for _, m := range body.Messages {
			var role string
			_ = json.Unmarshal(m["role"], &role)
			if role == "tool" {
				tools++
			}
			if role == "assistant" {
				ks := []string{}
				for k := range m {
					ks = append(ks, k)
				}
				sort.Strings(ks)
				assistantKeys = append(assistantKeys, strings.Join(ks, ","))
			}
		}
		mu.Lock()
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			sum := sha256.Sum256(raw)
			writeLine(f, map[string]any{"path": r.URL.Path, "tool_messages": tools, "tool_choice": body.ToolChoice,
				"assistant_message_keys": assistantKeys, "body_sha256": hex.EncodeToString(sum[:])})
			f.Close()
		}
		mu.Unlock()
		reasoning := fmt.Sprintf("Thinking about step %d.", tools)
		var call map[string]any
		switch {
		case body.ToolChoice == "none":
		case tools == 0:
			call = map[string]any{"index": 0, "id": "call_stub_read", "type": "function",
				"function": map[string]any{"name": "read", "arguments": `{"path":"README.md"}`}}
		case tools == 1:
			call = map[string]any{"index": 0, "id": "call_stub_edit", "type": "function",
				"function": map[string]any{"name": "edit", "arguments": `{"path":"README.md","edits":[{"oldText":"recieve","newText":"receive"}]}`}}
		case tools == 2:
			call = map[string]any{"index": 0, "id": "call_stub_bash", "type": "function",
				"function": map[string]any{"name": "bash", "arguments": `{"command":"grep -c receive README.md"}`}}
		}
		chunk := func(delta map[string]any, finish any, extra map[string]any) string {
			value := map[string]any{"id": fmt.Sprintf("gen-stub-%d", tools), "provider": "DeepInfra", "model": "deepseek/deepseek-v4.1-flash",
				"object": "chat.completion.chunk", "created": 1, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			for k, v := range extra {
				value[k] = v
			}
			encoded, _ := json.Marshal(value)
			return "data: " + string(encoded) + "\n\n"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": OPENROUTER PROCESSING\n\n")
		_, _ = io.WriteString(w, chunk(map[string]any{"role": "assistant", "content": "", "reasoning": reasoning,
			"reasoning_details": []any{map[string]any{"type": "reasoning.text", "text": reasoning, "format": "unknown", "index": 0}}}, nil, nil))
		finish := "stop"
		if call != nil {
			_, _ = io.WriteString(w, chunk(map[string]any{"role": "assistant", "content": "", "tool_calls": []any{call}}, nil, nil))
			finish = "tool_calls"
		} else {
			_, _ = io.WriteString(w, chunk(map[string]any{"role": "assistant", "content": "The typo is fixed."}, nil, nil))
		}
		_, _ = io.WriteString(w, chunk(map[string]any{"role": "assistant", "content": ""}, finish, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": finish, "native_finish_reason": finish}}}))
		_, _ = io.WriteString(w, chunk(map[string]any{"role": "assistant", "content": ""}, nil, map[string]any{"choices": []any{},
			"usage": map[string]any{"prompt_tokens": 900, "completion_tokens": 40, "total_tokens": 940, "cost": 0.0001428, "is_byok": false,
				"prompt_tokens_details": map[string]any{"cached_tokens": 0}, "completion_tokens_details": map[string]any{"reasoning_tokens": 20}}}))
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	return http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", port), handler)
}

func writeLine(w io.Writer, value any) {
	encoded, _ := json.Marshal(value)
	_, _ = w.Write(append(encoded, '\n'))
}

func printJSON(label string, value any) {
	encoded, _ := json.Marshal(value)
	fmt.Printf("%s %s\n", label, encoded)
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "f35probe:", err)
		os.Exit(1)
	}
}
