//go:build linux

package piruntime

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/providerbridge"
)

// TestDevelopmentPiSDKWorkerInheritedFDProcessRoundTrip runs the real Pi
// AgentSession in a direct Linux Node child. The only synthetic components are
// the Go provider-turn fixture and the DENY response; no credential, user
// directory, network, or host-containment claim participates. The child and
// all temporary source/CWD/HOME/TMP artifacts live under the caller-supplied
// mode-0700 ext4 test root; node_modules is a link to the preexisting pinned
// repository asset tree and is never modified.
func TestDevelopmentPiSDKWorkerInheritedFDProcessRoundTrip(t *testing.T) {
	nodePath := os.Getenv("TBOUND_GOVERNED_PI_NODE")
	dependencyRoot := os.Getenv("TBOUND_GOVERNED_PI_NODE_MODULES")
	privateRoot := os.Getenv("TBOUND_GOVERNED_PI_TEST_ROOT")
	if nodePath == "" || dependencyRoot == "" || privateRoot == "" {
		t.Skip("set explicit Linux Node, pinned existing node_modules, and private ext4 test root to run actual Pi process integration")
	}
	for name, path := range map[string]string{"Linux Node": nodePath, "pinned dependency root": dependencyRoot, "private test root": privateRoot} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s path must be absolute", name)
		}
	}
	if err := validatePrivate0700Directory(privateRoot); err != nil {
		t.Fatalf("private ext4 test root preflight: %v", err)
	}
	runRoot, err := os.MkdirTemp(privateRoot, ".governed-pi-run-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runRoot) })
	workerEnvHome := filepath.Join(runRoot, "worker-home")
	workerEnvTemp := filepath.Join(runRoot, "worker-tmp")
	for _, directory := range []string{workerEnvHome, workerEnvTemp} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(workerEnvHome)
		_ = os.RemoveAll(workerEnvTemp)
	})
	if err := validateMinimalWorkerEnvironment(minimalWorkerEnvironment(workerEnvHome, workerEnvTemp)); err != nil {
		t.Fatal(err)
	}
	nodeVersionCommand := exec.Command(nodePath, "--version")
	nodeVersionCommand.Env = minimalWorkerEnvironment(workerEnvHome, workerEnvTemp)
	nodeVersion, err := nodeVersionCommand.Output()
	if err != nil || strings.TrimSpace(string(nodeVersion)) != "v24.15.0" {
		t.Fatalf("explicit Linux Node is not the pinned v24.15.0 binary: %q %v", strings.TrimSpace(string(nodeVersion)), err)
	}

	bundleRoot := filepath.Join(runRoot, "adapter")
	workerDir := filepath.Join(bundleRoot, "src")
	workingDirectory := filepath.Join(runRoot, "session-root")
	if err := os.MkdirAll(workerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bundleRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dependencyRoot, filepath.Join(bundleRoot, "node_modules")); err != nil {
		t.Fatalf("link existing pinned dependencies into private source bundle: %v", err)
	}
	adapterSource := sourceAdapterRoot(t)
	for _, relative := range []string{
		"package.json", "package-lock.json",
		"src/governed-pi-worker.ts", "src/broker-provider.ts", "src/proxy-tools.ts",
		"src/ipc-transport.ts", "src/locked-resource-loader.ts",
	} {
		copyRegularSource(t, filepath.Join(adapterSource, filepath.FromSlash(relative)), filepath.Join(bundleRoot, filepath.FromSlash(relative)))
	}
	workingHandle, err := os.Open(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	bundleHandle, err := os.Open(bundleRoot)
	if err != nil {
		_ = workingHandle.Close()
		t.Fatal(err)
	}
	defer workingHandle.Close()
	defer bundleHandle.Close()
	worker, err := StartDevelopmentPiSDKWorker(context.Background(), DeveloperPiWorkerPlan{
		NodePath: nodePath, NodeVersion: "v24.15.0",
		BundleRoot: bundleRoot, DependencyRoot: dependencyRoot, BundleHandle: bundleHandle,
		WorkerPath: filepath.Join(bundleRoot, "src", "governed-pi-worker.ts"),
		WorkingDir: workingDirectory, WorkingHandle: workingHandle,
		Home: workerEnvHome, TempDir: workerEnvTemp,
		SessionID: "developer-worker-session", WorkflowID: "developer-worker-workflow",
		ConversationID: "developer-worker-conversation",
	})
	if err != nil {
		t.Fatalf("launch actual Go-managed Linux Pi worker: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = worker.Close(cleanupCtx)
	})
	workerCtx, cancelWorker := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelWorker()
	providerReady := make(chan struct{})
	providerRequests := make(chan uint64, 4)
	resultReleased := make(chan struct{})
	providerDone := make(chan error, 1)
	go func() {
		close(providerReady)
		providerDone <- serveSyntheticGoProvider(worker.Channels.ProviderChannel(), resultReleased, providerRequests)
	}()
	<-providerReady
	if err := worker.SendDevelopmentPrompt(workerCtx, "developer-task", "developer-request-1", "Read the synthetic fixture file."); err != nil {
		stopCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = worker.Close(stopCtx)
		stop()
		t.Fatalf("send host-controlled prompt to actual Pi: %v", err)
	}
	proposal, err := receiveGoProposal(workerCtx, worker.Channels.IPCServer())
	if err != nil {
		stopCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = worker.Close(stopCtx)
		stop()
		t.Fatalf("receive actual Pi proxy proposal: %v", err)
	}
	if proposal.Tool != "read" || proposal.ToolCallID != "developer-read-call" {
		t.Fatalf("actual Pi proposal differs from synthetic Go provider turn: %+v", proposal)
	}
	select {
	case sequence := <-providerRequests:
		if sequence != 1 {
			t.Fatalf("first provider request sequence = %d, want 1", sequence)
		}
	case <-workerCtx.Done():
		t.Fatalf("first provider request was not observed: %v", workerCtx.Err())
	}
	digest, err := protocol.CanonicalArgumentsDigest(proposal.Tool, proposal.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	response := correlation.Identifier{Issuer: "developer-go-broker", Opaque: "developer-response-1"}
	result := protocol.Result{
		SchemaVersion: protocol.ResultSchemaVersion, ResponseID: &response,
		ToolCallID: proposal.ToolCallID, Tool: proposal.Tool, Sequence: 1,
		CanonicalArgumentsDigest: digest, Verdict: "DENY",
		ReasonCode: "developer_synthetic_deny", PolicyDigest: "sha256:" + strings.Repeat("a", 64),
	}
	if err := worker.Channels.IPCServer().SendResult(result); err != nil {
		t.Fatalf("send exact-result-bound Go DENY over inherited IPC: %v", err)
	}
	close(resultReleased)
	select {
	case sequence := <-providerRequests:
		if sequence != 2 {
			t.Fatalf("expected provider continuation request 2 after the result; observed request %d", sequence)
		}
	case err := <-providerDone:
		t.Fatalf("synthetic provider bridge ended before continuation request: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("Pi did not request the result-dependent provider continuation")
	case <-workerCtx.Done():
		t.Fatalf("waiting for provider continuation: %v", workerCtx.Err())
	}
	var turnEnded bool
	for !turnEnded {
		event, err := worker.NextEvent(workerCtx)
		if err != nil {
			t.Fatalf("receive structured Pi worker event: %v", err)
		}
		switch event.Type {
		case "worker_error":
			t.Fatalf("actual Pi worker failed with bounded error code %s", event.Code)
		case "turn_end":
			if event.RequestID != "developer-request-1" {
				t.Fatalf("Pi turn event request ID = %q", event.RequestID)
			}
			turnEnded = true
		}
	}
	if err := <-providerDone; err != nil {
		t.Fatalf("Go provider-bridge stream fixture: %v", err)
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	settlement, closeErr := worker.Close(stopCtx)
	stop()
	if settlement.State != SettlementUnknown || closeErr == nil {
		t.Fatalf("uncontained development child must report UNKNOWN settlement: state=%s err=%v", settlement.State, closeErr)
	}
}

const testGoProviderBridgeVersion = "tbound-provider-bridge/v1"

type testGoProviderRequest struct {
	Version   string `json:"version"`
	Sequence  uint64 `json:"sequence"`
	Kind      string `json:"kind"`
	RequestID string `json:"request_id"`
}

type testGoProviderResponse struct {
	Version   string               `json:"version"`
	Sequence  uint64               `json:"sequence"`
	Kind      string               `json:"kind"`
	RequestID string               `json:"request_id"`
	Turn      *providerbridge.Turn `json:"turn,omitempty"`
}

func serveSyntheticGoProvider(channel io.ReadWriteCloser, resultReleased <-chan struct{}, requests chan<- uint64) error {
	defer channel.Close()
	for sequence := uint64(1); sequence <= 2; sequence++ {
		request, err := readTestGoProviderRequest(channel)
		if err != nil {
			return err
		}
		if request.Sequence != sequence || request.Kind != "next" || request.RequestID == "" {
			return fmt.Errorf("worker provider request has invalid kind/sequence: %s/%d", request.Kind, request.Sequence)
		}
		requests <- sequence
		if sequence == 2 {
			select {
			case <-resultReleased:
			case <-time.After(30 * time.Second):
				return errors.New("Go bridge withheld continuation until the correlated tool result")
			}
		}
		turn := syntheticGoProviderTurn(sequence)
		response := testGoProviderResponse{Version: testGoProviderBridgeVersion, Sequence: sequence, Kind: "turn", RequestID: request.RequestID, Turn: &turn}
		if err := writeGoProviderResponse(channel, response); err != nil {
			return err
		}
	}
	return nil
}

func syntheticGoProviderTurn(sequence uint64) providerbridge.Turn {
	turn := providerbridge.Turn{
		SchemaVersion: testGoProviderBridgeVersion,
		ResponseID:    fmt.Sprintf("developer-provider-response-%d", sequence),
		Model:         providerbridge.ApprovedModel,
		Created:       time.Now().Unix(),
		Generation:    "developer-nonclaiming-generation",
		Sequence:      sequence,
		FinishReason:  "stop",
		Usage:         json.RawMessage(`{"prompt_tokens":1,"completion_tokens":1}`),
		JournalSeq:    sequence,
		JournalHash:   strings.Repeat("b", 64),
	}
	if sequence == 1 {
		turn.FinishReason = "tool_calls"
		turn.ToolCall = &providerbridge.ToolCallProjection{
			ID: "developer-read-call", Name: "read",
			Arguments: json.RawMessage(`{"path":"synthetic/read.txt","offset":1,"limit":8}`),
		}
	} else {
		turn.AssistantText = "Synthetic Go provider continuation after the Go IPC DENY result."
	}
	return turn
}

func readTestGoProviderRequest(reader io.Reader) (testGoProviderRequest, error) {
	var request testGoProviderRequest
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return request, err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > PiWorkerMaxFrameBytes {
		return request, errors.New("Go provider test request frame length is invalid")
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(reader, body); err != nil {
		return request, err
	}
	if err := protocol.ValidateStrictJSON(body); err != nil {
		return request, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return request, err
	}
	if len(fields) != 4 {
		return request, errors.New("Go provider request contains fields outside the fixed next/cancel protocol")
	}
	for _, key := range []string{"version", "sequence", "kind", "request_id"} {
		if _, ok := fields[key]; !ok {
			return request, fmt.Errorf("Go provider request omitted fixed field %q", key)
		}
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return request, err
	}
	if request.Version != testGoProviderBridgeVersion {
		return request, errors.New("Go provider test request bridge version differs")
	}
	return request, nil
}

func writeGoProviderResponse(writer io.Writer, response testGoProviderResponse) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(body) == 0 || len(body) > PiWorkerMaxFrameBytes {
		return errors.New("Go provider test response frame exceeds bound")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
	if err := writePiAll(writer, prefix[:]); err != nil {
		return err
	}
	return writePiAll(writer, body)
}

func receiveGoProposal(ctx context.Context, server *ipc.Server) (protocol.Proposal, error) {
	type outcome struct {
		proposal protocol.Proposal
		err      error
	}
	result := make(chan outcome, 1)
	go func() { proposal, err := server.ReceiveProposal(); result <- outcome{proposal: proposal, err: err} }()
	select {
	case got := <-result:
		return got.proposal, got.err
	case <-ctx.Done():
		return protocol.Proposal{}, ctx.Err()
	}
}

func sourceAdapterRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Linux Go Pi integration source root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	return filepath.Join(root, "adapter")
}

func copyRegularSource(t *testing.T, source, target string) {
	t.Helper()
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > 16<<20 {
		t.Fatalf("source input is not a bounded regular file: %s", source)
	}
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func validatePrivate0700Directory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("test root must be a non-symlink mode-0700 directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("test root must be owned by the current Linux UID")
	}
	return nil
}
