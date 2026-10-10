//go:build linux

package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/piruntime"
)

// This opt-in test runs one actual Pi SDK AgentSession in a Node child over
// inherited provider fd 3 and the existing proposal IPC. Go owns prompt/history,
// provider HTTP fixture, correlation, gate, executor result, and audit journal.
// No real provider, credentials, external network, or host/VM operation is used.
// Set the three explicit path variables to enable it; otherwise it is skipped.
func TestInheritedFDRealPiSDKGoOwnedE05CrossProcess(t *testing.T) {
	node := os.Getenv("TBOUND_PROVIDERBRIDGE_NODE")
	adapter := os.Getenv("TBOUND_PROVIDERBRIDGE_ADAPTER")
	home := os.Getenv("TBOUND_PROVIDERBRIDGE_HOME")
	if node == "" || adapter == "" || home == "" {
		t.Skip("set explicit TBOUND_PROVIDERBRIDGE_NODE/ADAPTER/HOME for offline Pi/Go cross-process fixture")
	}
	for name, path := range map[string]string{"node": node, "adapter": adapter, "runtime home": home} {
		if strings.ContainsRune(path, '\x00') {
			t.Fatalf("%s path contains NUL", name)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("explicit %s path is unavailable: %v", name, err)
		}
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime HOME must be an existing private 0700 directory: info=%v err=%v", info, err)
	}
	tmp := os.Getenv("TMPDIR")
	if info, err := os.Stat(tmp); tmp == "" || err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("TMPDIR must be an existing private 0700 directory: info=%v err=%v", info, err)
	}

	const prompt = "Review the synthetic fixture using read, edit, bash, then read again."
	journalDir, err := os.MkdirTemp(tmp, ".providerbridge-e05-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(journalDir, 0o700); err != nil {
		_ = os.RemoveAll(journalDir)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(journalDir) })
	journal, err := audit.Open(filepath.Join(journalDir, "provider.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	goFDs, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	goFile := os.NewFile(uintptr(goFDs[0]), "providerbridge-go-peer")
	childFile := os.NewFile(uintptr(goFDs[1]), "providerbridge-node-fd3")
	if goFile == nil || childFile == nil {
		if goFile != nil {
			_ = goFile.Close()
		}
		if childFile != nil {
			_ = childFile.Close()
		}
		t.Fatal("provider socketpair did not create valid descriptors")
	}
	defer goFile.Close()
	providerConn, err := net.FileConn(goFile)
	if err != nil {
		_ = childFile.Close()
		t.Fatal(err)
	}
	defer providerConn.Close()

	ipcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = childFile.Close()
		t.Fatal(err)
	}
	defer ipcListener.Close()
	token, err := ipc.NewBindingToken()
	if err != nil {
		_ = childFile.Close()
		t.Fatal(err)
	}
	acceptResult := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		conn, err := ipcListener.Accept()
		acceptResult <- struct {
			conn net.Conn
			err  error
		}{conn, err}
	}()

	// Test-only budgets separate cold module import from the actual E05 exchange.
	// Neither changes production provider/session timeouts.
	childCtx, cancelChild := context.WithCancel(context.Background())
	defer cancelChild()
	child := exec.CommandContext(childCtx, node, "--experimental-strip-types", "--input-type=module", "-e", crossProcessE05NodeScript)
	child.Dir = adapter
	child.ExtraFiles = []*os.File{childFile} // only provider bridge inherited as child fd 3
	child.Env = []string{
		"HOME=" + home,
		"TMPDIR=" + tmp,
		"LANG=C.UTF-8",
		"PI_OFFLINE=1",
		"TBOUND_IPC_PORT=" + strconv.Itoa(ipcListener.Addr().(*net.TCPAddr).Port),
		"TBOUND_IPC_TOKEN=" + token,
		"TBOUND_TEST_PROMPT=" + prompt,
	}
	stdout, stderr := newBoundedTestLog(64<<10), newBoundedTestLog(64<<10)
	child.Stdout = stdout
	child.Stderr = stderr
	if err := child.Start(); err != nil {
		_ = childFile.Close()
		t.Fatal(err)
	}
	_ = childFile.Close()
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()

	var ipcConn net.Conn
	startupTimer := time.NewTimer(90 * time.Second)
	defer startupTimer.Stop()
	select {
	case accepted := <-acceptResult:
		if accepted.err != nil {
			cancelChild()
			stopCrossProcessChild(childDone)
			t.Fatal(accepted.err)
		}
		ipcConn = accepted.conn
	case childErr := <-childDone:
		logDir, logErr := retainStartupLogs(tmp, stdout.String(), stderr.String())
		t.Fatalf("Pi child exited before connecting to proposal IPC: child=%v logdir=%s persistErr=%v\nstdout=%s\nstderr=%s",
			childErr, logDir, logErr, stdout.String(), stderr.String())
	case <-startupTimer.C:
		cancelChild()
		var childErr error
		select {
		case childErr = <-childDone:
		case <-time.After(3 * time.Second):
			childErr = errors.New("child did not settle after startup timeout")
		}
		logDir, logErr := retainStartupLogs(tmp, stdout.String(), stderr.String())
		t.Fatalf("Pi child did not connect to bounded local IPC within %s: child=%v logdir=%s persistErr=%v\nstdout=%s\nstderr=%s",
			90*time.Second, childErr, logDir, logErr, stdout.String(), stderr.String())
	}
	if !startupTimer.Stop() {
		select {
		case <-startupTimer.C:
		default:
		}
	}
	runCtx, cancelRun := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRun()
	stopChildOnRunEnd := context.AfterFunc(runCtx, cancelChild)
	defer stopChildOnRunEnd()
	defer ipcConn.Close()
	ipcServer, err := ipc.NewServer(ipcConn, token)
	if err != nil {
		_ = child.Process.Kill()
		stopCrossProcessChild(childDone)
		t.Fatal(err)
	}

	doer := &fixtureDoer{responses: []fixtureResponse{
		{responseID: "e05-response-read-g0", callID: "e05-call-read-g0", tool: "read", arguments: json.RawMessage(`{"path":"fixture/read.txt"}`)},
		{responseID: "e05-response-edit-g1", callID: "e05-call-edit-g1", tool: "edit", arguments: json.RawMessage(`{"path":"fixture/read.txt","edits":[{"oldText":"old","newText":"new"}]}`)},
		{responseID: "e05-response-bash-g2", callID: "e05-call-bash-g2", tool: "bash", arguments: json.RawMessage(`{"command":"echo fixture"}`)},
		{responseID: "e05-response-read-g2", callID: "e05-call-read-g2", tool: "read", arguments: json.RawMessage(`{"path":"fixture/read.txt"}`)},
		{responseID: "e05-response-final", text: "The bounded synthetic fixture is complete."},
	}}
	conversation, err := newWithDoer(Config{
		ConversationID: "e05-crossprocess-conversation", WorkflowID: "e05-crossprocess-workflow", ProfileID: "e05-approved-profile",
		InitialGenerationID: "g0", InitialTreeDigest: "tree-g0",
		SystemPrompt: "Use only the registered proxy tools.", DeveloperPrompt: "This is an offline synthetic fixture.",
	}, ApprovedModel, doer, journal)
	if err != nil {
		_ = child.Process.Kill()
		stopCrossProcessChild(childDone)
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("e05-synthetic-task", prompt); err != nil {
		_ = child.Process.Kill()
		stopCrossProcessChild(childDone)
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "edit", Effect: gate.EffectAllow},
		{Tool: "bash", Effect: gate.EffectAllow},
	}})
	if err != nil {
		_ = child.Process.Kill()
		stopCrossProcessChild(childDone)
		t.Fatal(err)
	}
	inner := &fixtureExecutor{outputs: []json.RawMessage{
		readOutputFor("fixture/read.txt", "g0", "tree-g0", "old\n"),
		mutationOutput("edit", "g1", "tree-g1", "e05-transition-edit", 1, "e05-effect-edit", nil),
		mutationOutput("bash", "g2", "tree-g2", "e05-transition-bash", 2, "e05-effect-bash", &commandSummary{
			ExitCode: 0, ExitObserved: true, CommandContainmentStatus: "not-established", CommandRunnerProfile: "offline-fixture-non-claim-bearing",
		}),
		readOutputFor("fixture/read.txt", "g2", "tree-g2", "new\n"),
	}}
	wrapped, err := NewExecutor(conversation, inner)
	if err != nil {
		_ = child.Process.Kill()
		stopCrossProcessChild(childDone)
		t.Fatal(err)
	}
	supervisor := &piruntime.Supervisor{
		IPC: ipcServer, Broker: conversation, Policy: policy,
		Decisions: conversation, Executor: wrapped, ProposalLimit: 4,
	}
	providerDone := make(chan error, 1)
	supervisorDone := make(chan error, 1)
	go func() { providerDone <- Serve(runCtx, providerConn, conversation) }()
	go func() { supervisorDone <- supervisor.Serve(runCtx) }()

	childErr := <-childDone
	if childErr != nil {
		cancelRun()
		_ = providerConn.Close()
		_ = ipcConn.Close()
		t.Fatalf("Linux Node/Pi/Go child failed: %v\nstdout=%s\nstderr=%s", childErr, stdout.String(), stderr.String())
	}
	t.Logf("cross-process Pi startup/turn phases: %s", strings.TrimSpace(stdout.String()))
	for label, done := range map[string]<-chan error{"provider bridge": providerDone, "proposal supervisor": supervisorDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s failed: %v\nchild stdout=%s\nstderr=%s", label, err, stdout.String(), stderr.String())
			}
		case <-runCtx.Done():
			t.Fatalf("%s did not settle: %v", label, runCtx.Err())
		}
	}
	if supervisor.HandledProposals() != 4 || inner.call != 4 {
		t.Fatalf("actual Pi tool dispatch count supervisor/executor=%d/%d", supervisor.HandledProposals(), inner.call)
	}
	if len(doer.requests) != 5 {
		t.Fatalf("Go provider exchange count=%d want=5", len(doer.requests))
	}
	assertRequestTranscript(t, doer.requests[1], 5, "tool", "e05-call-read-g0")
	assertRequestTranscript(t, doer.requests[2], 7, "tool", "e05-call-edit-g1")
	assertRequestTranscript(t, doer.requests[3], 9, "tool", "e05-call-bash-g2")
	assertRequestTranscript(t, doer.requests[4], 11, "tool", "e05-call-read-g2")
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	var toolResults []resultEvidence
	for _, record := range trace.Records {
		if record.Event.Kind == "provider_tool_result" {
			var result resultEvidence
			if err := json.Unmarshal(record.Event.Data, &result); err != nil {
				t.Fatal(err)
			}
			toolResults = append(toolResults, result)
		}
	}
	if len(toolResults) != 4 {
		t.Fatalf("actual cross-process durable provider tool results=%d want=4", len(toolResults))
	}
	for i, result := range toolResults {
		assertRequestResultExact(t, doer.requests[i+1], result)
	}
	if strings.Contains(stdout.String()+stderr.String(), prompt) {
		// The test worker may print its exact prompt only if an SDK diagnostic
		// unexpectedly leaks it; provider channel frames themselves never carry it.
		t.Fatal("user prompt leaked into cross-process diagnostics")
	}
}

const crossProcessE05NodeScript = `
const started = Date.now();
const stage = (name) => process.stdout.write(JSON.stringify({stage:name,elapsed_ms:Date.now()-started}) + "\n");
stage("NODE_EVAL_STARTED");
const { createConnection } = await import("node:net");
const { mkdtemp, rm } = await import("node:fs/promises");
const { tmpdir } = await import("node:os");
const { join, resolve } = await import("node:path");
stage("IMPORTING_BROKER_PROVIDER");
const { createBrokerProviderFromFD } = await import("./src/broker-provider.ts");
stage("BROKER_PROVIDER_IMPORTED");
const { createAgentSession, ModelRuntime, SessionManager, SettingsManager } = await import("@earendil-works/pi-coding-agent");
const { createProxyTools, IpcProposalSender } = await import("./src/proxy-tools.ts");
const { IpcClient } = await import("./src/ipc-transport.ts");
const { LockedResourceLoader } = await import("./src/locked-resource-loader.ts");
const bridge = createBrokerProviderFromFD(3);
const ipcSocket = createConnection({ host: "127.0.0.1", port: Number(process.env.TBOUND_IPC_PORT) });
await new Promise((resolvePromise, reject) => { ipcSocket.once("connect", resolvePromise); ipcSocket.once("error", reject); });
stage("LOCAL_IPC_CONNECTED");
const ipc = new IpcClient(ipcSocket, process.env.TBOUND_IPC_TOKEN);
const credentials = {
  async read() { return undefined; },
  async list() { return []; },
  async modify(_providerID, update) { return update(undefined); },
  async delete() {},
};
const modelRuntime = await ModelRuntime.create({ credentials, modelsPath: null, allowModelNetwork: false, refreshOnCreate: false });
modelRuntime.registerNativeProvider(bridge.provider);
const model = modelRuntime.getModel("tbound-go-broker", "nvidia/nemotron-3.5-lightning:free");
if (!model) throw new Error("registered Go broker model unavailable");
stage("PI_MODEL_RUNTIME_READY");
const cwd = await mkdtemp(join(tmpdir(), "tbound-providerbridge-agent-"));
const sessionManager = SessionManager.inMemory(cwd);
const settingsManager = SettingsManager.inMemory({
  defaultTools: [], extensions: [], packages: [], skills: [], prompts: [], themes: [],
  enableSkillCommands: false, enableAnalytics: false, enableInstallTelemetry: false,
  cacheWarming: "off", compaction: { enabled: false }, retry: { enabled: false },
});
const created = await createAgentSession({
  cwd, agentDir: resolve(cwd, ".agent"), modelRuntime, model, thinkingLevel: "off",
  settingsManager, sessionManager, resourceLoader: new LockedResourceLoader(),
  noTools: "builtin", tools: ["read", "write", "edit", "bash"],
  customTools: createProxyTools(new IpcProposalSender(ipc)),
});
stage("PI_AGENT_SESSION_READY");
try {
  stage("PI_PROMPT_STARTED");
  await created.session.prompt(process.env.TBOUND_TEST_PROMPT, { expandPromptTemplates: false, source: "rpc" });
  stage("PI_PROMPT_COMPLETED");
  process.stdout.write(JSON.stringify({ activeTools: created.session.getActiveToolNames(), turns: "completed" }));
} finally {
  created.session.dispose();
  ipc.close();
  bridge.client.close();
  await rm(cwd, { recursive: true, force: true });
}
`

type boundedTestLog struct {
	buffer    bytes.Buffer
	limit     int
	discarded int
}

func newBoundedTestLog(limit int) *boundedTestLog { return &boundedTestLog{limit: limit} }

func (l *boundedTestLog) Write(value []byte) (int, error) {
	original := len(value)
	remaining := l.limit - l.buffer.Len()
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		_, _ = l.buffer.Write(value[:remaining])
	}
	if remaining < len(value) {
		l.discarded += len(value) - remaining
	}
	return original, nil
}

func (l *boundedTestLog) String() string {
	if l.discarded == 0 {
		return l.buffer.String()
	}
	return l.buffer.String() + fmt.Sprintf("\n[discarded %d additional bytes]", l.discarded)
}

func stopCrossProcessChild(done <-chan error) {
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

func retainStartupLogs(tmp, stdout, stderr string) (string, error) {
	directory, err := os.MkdirTemp(tmp, ".providerbridge-startup-failure-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return directory, err
	}
	for name, contents := range map[string]string{"stdout.log": stdout, "stderr.log": stderr} {
		path := filepath.Join(directory, name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return directory, err
		}
		_, writeErr := file.WriteString(contents)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return directory, err
		}
	}
	return directory, nil
}
