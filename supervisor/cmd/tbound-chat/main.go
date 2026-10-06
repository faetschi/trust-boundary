// Command tbound-chat is the thin local launcher for the first governed-chat
// slice. It deliberately exposes only an explicit, non-claim-bearing fixture
// mode. Production/real Pi launch remains refused until the reviewed closure,
// containment, source, offline-profile, verifier, and cleanup gates exist.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/sessioncontrol"
)

//go:embed chat.html
var chatHTML []byte

type fixtureWorker struct {
	mu               sync.Mutex
	cmd              *exec.Cmd
	stdin            io.WriteCloser
	listener         net.Listener
	accepted         net.Conn
	ipcToken         string
	generation       string
	cancel           context.CancelFunc
	events           func(sessioncontrol.WorkerEvent)
	toolDecision     func(sessioncontrol.ToolDecision)
	ready            chan error
	exitErr          chan error
	done             chan struct{}
	outputDone       chan struct{}
	ipcDone          chan struct{}
	stopDone         chan struct{}
	stopStarted      bool
	settleStarted    bool
	stopSettled      bool
	exitedBeforeStop bool
	stopErr          error
	processErr       error
	prompt           chan promptResult
	stopped          bool
}

type promptResult struct {
	requestID string
	err       error
}

func newFixtureWorker(parent context.Context, identity sessioncontrol.SessionIdentity, adapterPath, cwd string, callbacks sessioncontrol.WorkerCallbacks) (sessioncontrol.Worker, error) {
	if parent == nil || callbacks.Event == nil || callbacks.ToolDecision == nil {
		return nil, errors.New("fixture worker requires context and event callback")
	}
	if identity.ID == "" || identity.Generation == "" {
		return nil, errors.New("fixture worker requires server-owned session identity")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen fixture IPC: %w", err)
	}
	token, err := ipc.NewBindingToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	workerContext, cancel := context.WithCancel(parent)
	workerArgs := []string{"--experimental-strip-types", adapterPath, "--mode", "fixture", "--ipc-address", listener.Addr().String()}
	if cwd != "" {
		workerArgs = append(workerArgs, "--cwd", cwd)
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		_ = listener.Close()
		cancel()
		return nil, fmt.Errorf("locate node runtime: %w", err)
	}
	cmd := exec.Command(nodePath, workerArgs...)
	configureWorkerProcess(cmd)
	cmd.Env = minimalWorkerEnv(nodePath, token)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		_ = listener.Close()
		return nil, fmt.Errorf("open fixture worker stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = listener.Close()
		_ = stdin.Close()
		return nil, fmt.Errorf("open fixture worker stdout: %w", err)
	}
	// Worker stderr is intentionally not copied into the control/event stream:
	// diagnostics must never accidentally expose IPC token material.
	if err := cmd.Start(); err != nil {
		cancel()
		_ = listener.Close()
		_ = stdin.Close()
		return nil, fmt.Errorf("start fixture SDK worker: %w", err)
	}
	worker := &fixtureWorker{
		cmd: cmd, stdin: stdin, listener: listener, ipcToken: token, generation: identity.Generation, cancel: cancel, events: callbacks.Event,
		toolDecision: callbacks.ToolDecision, ready: make(chan error, 1), exitErr: make(chan error, 1),
		done: make(chan struct{}), outputDone: make(chan struct{}), ipcDone: make(chan struct{}), stopDone: make(chan struct{}),
		prompt: make(chan promptResult, 1),
	}
	go worker.acceptIPC(workerContext)
	go worker.readOutput(stdout)
	go func() {
		err := cmd.Wait()
		worker.mu.Lock()
		worker.processErr = err
		if !worker.stopped && err != nil {
			worker.stopped = true
		}
		worker.mu.Unlock()
		worker.exitErr <- err
		close(worker.done)
	}()
	select {
	case err := <-worker.ready:
		if err != nil {
			_ = worker.Stop(context.Background())
			return nil, err
		}
	case <-time.After(5 * time.Second):
		_ = worker.Stop(context.Background())
		return nil, errors.New("fixture SDK worker did not become ready")
	case err := <-worker.exitErr:
		_ = worker.Stop(context.Background())
		if err == nil {
			return nil, errors.New("fixture SDK worker exited before becoming ready")
		}
		return nil, fmt.Errorf("fixture SDK worker exited before becoming ready: %w", err)
	case <-parent.Done():
		_ = worker.Stop(context.Background())
		return nil, parent.Err()
	}
	return worker, nil
}

func (w *fixtureWorker) acceptIPC(ctx context.Context) {
	defer close(w.ipcDone)
	conn, err := w.listener.Accept()
	if err != nil {
		select {
		case w.ready <- fmt.Errorf("accept fixture IPC: %w", err):
		default:
		}
		return
	}
	_ = w.listener.Close()
	w.mu.Lock()
	w.accepted = conn
	w.mu.Unlock()
	broker, brokerErr := newFixtureBroker(w.generation)
	if brokerErr != nil {
		_ = conn.Close()
		select {
		case w.ready <- brokerErr:
		default:
		}
		return
	}
	policy, policyErr := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow},
		{Tool: "edit", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow},
	}})
	if policyErr != nil {
		_ = conn.Close()
		select {
		case w.ready <- policyErr:
		default:
		}
		return
	}
	server, err := ipc.NewServer(conn, w.ipcToken)
	if err != nil {
		_ = conn.Close()
		select {
		case w.ready <- err:
		default:
		}
		return
	}
	supervisor := &fixtureSupervisor{server: server, broker: broker, policy: policy, generation: w.generation, toolDecision: w.toolDecision}
	if err := supervisor.serve(ctx); err != nil && ctx.Err() == nil {
		w.events(sessioncontrol.WorkerEvent{Kind: "error", Error: "fixture IPC supervisor stopped: " + err.Error()})
	}
}

func (w *fixtureWorker) readOutput(reader io.Reader) {
	defer close(w.outputDone)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		value, err := decodeWorkerOutput([]byte(scanner.Text()))
		if err != nil {
			w.abortProcess()
			select {
			case w.ready <- fmt.Errorf("fixture SDK worker emitted malformed output: %w", err):
			default:
			}
			return
		}
		switch value.Type {
		case "ready":
			select {
			case w.ready <- nil:
			default:
			}
		case "text_delta":
			w.events(sessioncontrol.WorkerEvent{Kind: "text_delta", Text: value.Text})
		case "error":
			w.events(sessioncontrol.WorkerEvent{Kind: "error", Error: value.Error})
			select {
			case w.prompt <- promptResult{requestID: value.RequestID, err: errors.New(value.Error)}:
			default:
			}
		case "turn_end":
			select {
			case w.prompt <- promptResult{requestID: value.RequestID}:
			default:
			}
		case "stopped":
			return
		default:
			w.events(sessioncontrol.WorkerEvent{Kind: "error", Error: "fixture SDK worker emitted unsupported event"})
		}
	}
	if err := scanner.Err(); err != nil {
		w.abortProcess()
		w.events(sessioncontrol.WorkerEvent{Kind: "error", Error: "fixture SDK worker output failed"})
	}
}

type workerOutput struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Text      string          `json:"text,omitempty"`
	Error     string          `json:"error,omitempty"`
	Model     json.RawMessage `json:"model,omitempty"`
	Tools     []string        `json:"tools,omitempty"`
}

func decodeWorkerOutput(raw []byte) (workerOutput, error) {
	var value workerOutput
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return value, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return value, errors.New("worker output must be an object")
	}
	var rawType string
	if err := json.Unmarshal(fields["type"], &rawType); err != nil {
		return value, errors.New("worker output type is required")
	}
	required := map[string]struct{}{"type": {}}
	allowed := map[string]struct{}{"type": {}}
	switch rawType {
	case "ready":
		allowed["model"], allowed["tools"] = struct{}{}, struct{}{}
	case "text_delta":
		required["request_id"], required["text"] = struct{}{}, struct{}{}
		allowed["request_id"], allowed["text"] = struct{}{}, struct{}{}
	case "error":
		required["request_id"], required["error"] = struct{}{}, struct{}{}
		allowed["request_id"], allowed["error"] = struct{}{}, struct{}{}
	case "turn_end":
		required["request_id"] = struct{}{}
		allowed["request_id"] = struct{}{}
	case "stopped":
	default:
		return value, errors.New("unsupported worker output type")
	}
	if len(fields) != len(allowed) {
		return value, errors.New("worker output has unknown or missing fields")
	}
	for key := range allowed {
		if _, ok := fields[key]; !ok {
			return value, fmt.Errorf("worker output is missing %q", key)
		}
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return value, fmt.Errorf("worker output has unknown field %q", key)
		}
	}
	for key := range required {
		if len(fields[key]) == 0 {
			return value, fmt.Errorf("worker output is missing %q", key)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return workerOutput{}, err
	}
	if value.Type != "ready" && value.RequestID == "" {
		return workerOutput{}, errors.New("worker output request id is empty")
	}
	if value.Type == "text_delta" && !utf8.ValidString(value.Text) {
		return workerOutput{}, errors.New("worker output text is not valid UTF-8")
	}
	return value, nil
}

func (w *fixtureWorker) abortProcess() {
	w.mu.Lock()
	w.stopped = true
	stdin, listener, accepted, cmd := w.stdin, w.listener, w.accepted, w.cmd
	w.cancel()
	w.mu.Unlock()
	_ = stdin.Close()
	_ = listener.Close()
	if accepted != nil {
		_ = accepted.Close()
	}
	killWorkerProcess(cmd)
}

func (w *fixtureWorker) Prompt(ctx context.Context, text string) error {
	if ctx == nil {
		return errors.New("fixture prompt context is required")
	}
	requestID := newOpaqueID("req")
	command, _ := json.Marshal(map[string]string{"type": "prompt", "request_id": requestID, "text": text})
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return errors.New("fixture worker is stopped")
	}
	_, err := w.stdin.Write(append(command, '\n'))
	w.mu.Unlock()
	if err != nil {
		return fmt.Errorf("send fixture prompt: %w", err)
	}
	select {
	case result := <-w.prompt:
		if result.requestID != requestID {
			return errors.New("fixture worker prompt correlation mismatch")
		}
		return result.err
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return errors.New("fixture worker exited before turn settled")
	}
}

func (w *fixtureWorker) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("fixture stop context is required")
	}
	w.mu.Lock()
	if w.stopStarted {
		done := w.stopDone
		w.mu.Unlock()
		select {
		case <-done:
			w.mu.Lock()
			err := w.stopErr
			w.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w.stopStarted = true
	w.stopped = true
	select {
	case <-w.done:
		w.exitedBeforeStop = true
	default:
	}
	stdin, listener, accepted, cmd := w.stdin, w.listener, w.accepted, w.cmd
	w.cancel()
	if !w.settleStarted {
		w.settleStarted = true
		go w.settle()
	}
	w.mu.Unlock()
	_ = stdin.Close()
	_ = listener.Close()
	if accepted != nil {
		_ = accepted.Close()
	}
	killWorkerProcess(cmd)
	select {
	case <-w.stopDone:
		w.mu.Lock()
		err := w.stopErr
		w.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *fixtureWorker) settle() {
	<-w.done
	<-w.outputDone
	<-w.ipcDone
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopSettled {
		w.stopSettled = true
		if w.exitedBeforeStop {
			w.stopErr = w.processErr
			if w.stopErr == nil {
				w.stopErr = errors.New("fixture SDK worker exited before explicit stop")
			}
		}
		close(w.stopDone)
	}
}

func minimalWorkerEnv(nodePath, bindingToken string) []string {
	// The SDK worker receives only the runtime search path, the platform
	// minimum required by Node, and its one explicit IPC binding. In particular
	// no inherited provider/API-key/Node-options/unknown environment is copied.
	env := []string{"TBOUND_IPC_TOKEN=" + bindingToken}
	if nodeDir := filepath.Dir(nodePath); nodeDir != "." {
		env = append(env, "PATH="+nodeDir)
	}
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type fixtureBroker struct {
	mu     sync.Mutex
	stream *protocol.Stream
}

type fixtureCaptureSpec struct {
	ToolCallID string
	ResponseID string
	Tool       string
	Arguments  json.RawMessage
}

var fixtureCapturePlan = []fixtureCaptureSpec{
	{ToolCallID: "fixture-call-read-1", ResponseID: "fixture-response-1", Tool: "read", Arguments: json.RawMessage(`{"path":"fixture/read.txt","offset":1,"limit":4}`)},
	{ToolCallID: "fixture-call-write-1", ResponseID: "fixture-response-2", Tool: "write", Arguments: json.RawMessage(`{"path":"fixture/write.txt","content":"fixture write"}`)},
	{ToolCallID: "fixture-call-edit-1", ResponseID: "fixture-response-3", Tool: "edit", Arguments: json.RawMessage(`{"path":"fixture/edit.txt","edits":[{"oldText":"before","newText":"after"}]}`)},
	{ToolCallID: "fixture-call-bash-1", ResponseID: "fixture-response-4", Tool: "bash", Arguments: json.RawMessage(`{"command":"echo fixture","timeout":1}`)},
}

func newFixtureBroker(generation string) (*fixtureBroker, error) {
	stream, err := protocol.NewStream("fixture-call", "fixture-response")
	if err != nil {
		return nil, err
	}
	broker := &fixtureBroker{stream: stream}
	for sequence, spec := range fixtureCapturePlan {
		digest, err := protocol.CanonicalArgumentsDigest(spec.Tool, spec.Arguments)
		if err != nil {
			return nil, err
		}
		decision := stream.Capture(protocol.TrustedCapture{
			ResponseID: correlation.Identifier{Issuer: "fixture-response", Opaque: spec.ResponseID},
			ToolCallID: correlation.Identifier{Issuer: "fixture-call", Opaque: spec.ToolCallID},
			ToolName:   spec.Tool, RawArguments: append([]byte(nil), spec.Arguments...),
			CanonicalizationProfile: protocol.CanonicalizationProfile, CanonicalArgumentsDigest: digest,
			Sequence: uint64(sequence + 1), Generation: generation,
		})
		if !decision.Accepted {
			return nil, fmt.Errorf("register fixture capture %s: %s", spec.ToolCallID, decision.ReasonCode)
		}
	}
	return broker, nil
}

func (b *fixtureBroker) Correlate(ctx context.Context, proposal protocol.Proposal) (correlation.Decision, error) {
	if err := ctx.Err(); err != nil {
		return correlation.Decision{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	encoded, err := protocol.MarshalProposal(proposal)
	if err != nil {
		return correlation.Decision{}, err
	}
	return b.stream.Propose(encoded), nil
}

type fixtureSupervisor struct {
	server       *ipc.Server
	broker       *fixtureBroker
	policy       gate.Policy
	generation   string
	toolDecision func(sessioncontrol.ToolDecision)
}

func (s *fixtureSupervisor) serve(ctx context.Context) error {
	if s == nil || s.server == nil || s.broker == nil {
		return errors.New("fixture supervisor is incomplete")
	}
	return (&supervisorAdapter{server: s.server, broker: s.broker, policy: s.policy, generation: s.generation, toolDecision: s.toolDecision}).serve(ctx)
}

// supervisorAdapter mirrors only the existing supervisor composition seam; it
// does not copy the production command. The executor returns a fixture record
// and never performs a filesystem or shell effect.
type supervisorAdapter struct {
	server       *ipc.Server
	broker       *fixtureBroker
	policy       gate.Policy
	generation   string
	toolDecision func(sessioncontrol.ToolDecision)
}

func (s *supervisorAdapter) serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = s.server.Close() })
	defer stop()
	defer s.server.Close()
	for {
		proposal, err := s.server.ReceiveProposal()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, ipc.ErrClosed) {
				return nil
			}
			return err
		}
		matched, err := s.broker.Correlate(ctx, proposal)
		if err != nil {
			return err
		}
		if s.toolDecision != nil && matched.Accepted && matched.BrokerCapture != nil {
			s.toolDecision(sessioncontrol.ToolDecision{
				Generation: matched.BrokerCapture.Generation, Sequence: matched.BrokerCapture.Sequence,
				ResponseID: matched.BrokerCapture.ResponseID.Opaque, ToolCallID: matched.BrokerCapture.ToolCallID.Opaque,
				Tool: matched.BrokerCapture.ToolName, Verdict: "PENDING", Status: "pending",
				Reason: "fixture provider capture correlated; awaiting policy decision",
			})
		}
		decision := gate.Evaluate(proposal, matched, s.policy)
		result := protocol.Result{SchemaVersion: protocol.ResultSchemaVersion, ResponseID: decision.ResponseID,
			ToolCallID: proposal.ToolCallID, Tool: proposal.Tool, Sequence: decision.Sequence,
			CanonicalArgumentsDigest: decision.CanonicalArgumentsDigest, Verdict: string(decision.Verdict),
			ReasonCode: decision.ReasonCode, PolicyDigest: decision.PolicyDigest}
		if decision.Verdict == gate.Allow {
			result.Output = json.RawMessage(`{"mode":"fixture","effect":"not-executed"}`)
		}
		if s.toolDecision != nil && matched.Accepted && matched.BrokerCapture != nil {
			s.toolDecision(sessioncontrol.ToolDecision{
				Generation: matched.BrokerCapture.Generation, Sequence: decision.Sequence,
				ResponseID: matched.BrokerCapture.ResponseID.Opaque, ToolCallID: matched.BrokerCapture.ToolCallID.Opaque,
				Tool: matched.BrokerCapture.ToolName, Verdict: string(decision.Verdict), Status: fixtureDecisionStatus(decision.Verdict), Reason: decision.ReasonCode,
			})
		} else if s.toolDecision != nil && matched.BrokerCapture != nil {
			s.toolDecision(sessioncontrol.ToolDecision{
				Generation: matched.BrokerCapture.Generation, Sequence: matched.BrokerCapture.Sequence,
				ResponseID: matched.BrokerCapture.ResponseID.Opaque, ToolCallID: matched.BrokerCapture.ToolCallID.Opaque,
				Tool: matched.BrokerCapture.ToolName, Verdict: "DENY", Status: "failed", Reason: string(matched.ReasonCode),
			})
		} else if s.toolDecision != nil {
			s.toolDecision(sessioncontrol.ToolDecision{
				Generation: s.generation, Status: "failed", Reason: string(matched.ReasonCode),
			})
		}
		if err := s.server.SendResult(result); err != nil {
			return err
		}
	}
}

func fixtureDecisionStatus(verdict gate.Verdict) string {
	if verdict == gate.Allow {
		return "completed"
	}
	return "denied"
}

func newOpaqueID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return prefix + "-unavailable"
	}
	return prefix + "-" + hex.EncodeToString(bytes[:])
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("tbound-chat", flag.ContinueOnError)
	addr := flags.String("addr", "127.0.0.1:8788", "loopback address for the authenticated chat control API")
	fixture := flags.Bool("fixture", false, "explicitly enable the non-claim-bearing SDK fixture")
	adapterPath := flags.String("adapter-worker", filepath.FromSlash("adapter/src/sdk-worker.ts"), "path to the restricted SDK worker")
	tokenFile := flags.String("token-file", "", "required owner-only pairing metadata file for the generated API token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !*fixture {
		return sessioncontrol.ErrRealLaunchRefused
	}
	if *tokenFile == "" {
		return errors.New("fixture launch requires --token-file owner-private pairing metadata")
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || host != "127.0.0.1" {
		return errors.New("tbound-chat requires an exact 127.0.0.1 listener")
	}
	workerPath := *adapterPath
	if !filepath.IsAbs(workerPath) {
		if _, statErr := os.Stat(workerPath); statErr != nil {
			candidate := filepath.Join("..", workerPath)
			if _, candidateErr := os.Stat(candidate); candidateErr == nil {
				workerPath = candidate
			}
		}
	}
	manager, err := sessioncontrol.New(sessioncontrol.Config{
		FixtureFactory: func(ctx context.Context, identity sessioncontrol.SessionIdentity, callbacks sessioncontrol.WorkerCallbacks) (sessioncontrol.Worker, error) {
			return newFixtureWorker(ctx, identity, workerPath, "", callbacks)
		},
	})
	if err != nil {
		return err
	}
	if err := writeOwnerOnlyTokenFile(*tokenFile, manager.AuthToken()); err != nil {
		return fmt.Errorf("write owner-only API token metadata: %w", err)
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Close(shutdown)
	}()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	actualHost := listener.Addr().String()
	handler, err := sessioncontrol.NewHandler(sessioncontrol.HTTPConfig{Manager: manager, AllowedHost: actualHost, HTML: chatHTML})
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 20 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = manager.Close(shutdown)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
