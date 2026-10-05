//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// The E05 fixture drives the normative two-generation workflow
//
//	read(g0) -> edit(g1) -> Bash(g2) -> read(g2)
//
// through the real ipc -> broker correlation -> gate -> DurableExecutor path
// with a synthetic trusted provider capture. It is deliberately synthetic and
// non-claim-bearing: no provider is contacted and the Bash runner stands in for
// a contained command by writing one file into the private command view.

const (
	e05RunnerLabel = "synthetic-e05-bash-runner"

	e05ReadCallID0   = "call-e05-read-0"
	e05EditCallID    = "call-e05-edit"
	e05BashCallID    = "call-e05-bash"
	e05ReadCallID2   = "call-e05-read-2"
	e05ReadRespID0   = "response-e05-read-0"
	e05EditRespID    = "response-e05-edit"
	e05BashRespID    = "response-e05-bash"
	e05ReadRespID2   = "response-e05-read-2"
	e05BashOutput    = "bash-generated.txt"
	e05BashContent   = "bash-generation\n"
	e05TaskBaseline  = "baseline\n"
	e05TaskEdited    = "edited\n"
	e05BuildResult   = "baseline result\n"
	e05TaskPath      = "task.txt"
	e05BuildDir      = "build"
	e05BuildFilePath = "build/result.txt"
)

// e05CallKey identifies one approved durable operation by tool and the sealed
// input generation it ran against.
type e05CallKey struct {
	tool            string
	inputGeneration string
}

// durableE05Authority is the fixture trusted policy authority and exact-binding
// verifier for the two-generation workflow. It approves read/edit/bash on the
// specific sealed generations the fixture precomputes, and it validates that
// every transition binding names exactly the request it authorized.
type durableE05Authority struct {
	mu                   sync.Mutex
	callIssuer           string
	policyDigest         string
	metadataPolicyDigest string
	trees                map[string]string // generation label -> sealed tree digest
	authorized           map[e05CallKey]sessionrepo.OperationRequest
	observed             map[uint64]string // transition sequence -> observed output digest
}

func newDurableE05Authority() *durableE05Authority {
	return &durableE05Authority{
		callIssuer: durableCallIssuer, policyDigest: durablePolicyDigest, metadataPolicyDigest: durableMetadataDigest,
		trees:      map[string]string{},
		authorized: map[e05CallKey]sessionrepo.OperationRequest{},
		observed:   map[uint64]string{},
	}
}

func (a *durableE05Authority) setTree(generation, treeDigest string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trees[generation] = treeDigest
}

func (a *durableE05Authority) observedOutput(sequence uint64) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.observed[sequence]
}

func (a *durableE05Authority) authorize(request sessionrepo.OperationRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case request.Tool != "read" && request.Tool != "edit" && request.Tool != "bash":
		return fmt.Errorf("fixture authority only grants the read/edit/bash surface, got %q", request.Tool)
	case request.Decision.Outcome != delta.PolicyAllow ||
		request.Decision.PolicyDigest != a.policyDigest ||
		request.Decision.MetadataPolicyDigest != a.metadataPolicyDigest ||
		!strings.HasPrefix(request.Decision.ID, "decision-"):
		return errors.New("decision commitment mismatch")
	case !strings.HasPrefix(request.ArgumentDigest, "sha256:"):
		return errors.New("argument digest is not a sha256 commitment")
	case !strings.HasPrefix(request.EffectID, "sessionrepo-effect-"):
		return errors.New("request lacks a supervisor effect ID")
	}
	expectedTree, known := a.trees[request.InputGeneration]
	if !known || request.InputTreeDigest != expectedTree {
		return fmt.Errorf("request input generation %q does not match the approved sealed tree", request.InputGeneration)
	}
	if request.Tool == "bash" {
		if request.Operation.Kind != delta.OperationLease || !strings.HasPrefix(request.Operation.LeaseID, "lease-") ||
			request.Operation.ProposalID != "" || request.Operation.CallIssuer != "" || request.Operation.CallID != "" {
			return errors.New("fixture bash lease identity is incomplete")
		}
		if request.ViewID == "" || request.ExecutionContextDigest == "" {
			return errors.New("fixture bash request lacks command-view context")
		}
	} else {
		if request.Operation.Kind != delta.OperationProposalCall || request.Operation.CallIssuer != a.callIssuer ||
			!strings.HasPrefix(request.Operation.ProposalID, "proposal-") || request.Operation.CallID == "" {
			return errors.New("fixture operation call identity is incomplete")
		}
		if request.ViewID != "" || request.ExecutionContextDigest != "" {
			return errors.New("fixture read/edit request carries unexpected command-view context")
		}
	}
	key := e05CallKey{tool: request.Tool, inputGeneration: request.InputGeneration}
	if _, exists := a.authorized[key]; exists {
		return fmt.Errorf("fixture authority already authorized %q against %s", request.Tool, request.InputGeneration)
	}
	a.authorized[key] = request
	return nil
}

func (a *durableE05Authority) verifySettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if settlement.LeaseID != leaseID || settlement.ViewID != viewID {
		return errors.New("settlement receipt names a different lease or view")
	}
	if !strings.HasPrefix(leaseID, "lease-") || !strings.HasPrefix(viewID, "view-") {
		return errors.New("settlement receipt has an unrecognized lease or view identity")
	}
	if strings.TrimSpace(settlement.EvidenceClass) == "" {
		return errors.New("settlement receipt lacks an evidence class")
	}
	return nil
}

func (a *durableE05Authority) verifyDecision(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	expected, ok := a.authorized[e05CallKey{tool: binding.Tool, inputGeneration: binding.InputGeneration}]
	if !ok {
		return fmt.Errorf("no authorized %q request for input generation %s", binding.Tool, binding.InputGeneration)
	}
	switch {
	case binding.ArgumentDigest != expected.ArgumentDigest:
		return errors.New("binding argument digest differs from the authorized request")
	case binding.Operation != expected.Operation:
		return errors.New("binding operation identity differs from the authorized request")
	case binding.ViewID != expected.ViewID || binding.ExecutionContextDigest != expected.ExecutionContextDigest:
		return errors.New("binding command-view context differs from the authorized request")
	case decision.Outcome != delta.PolicyAllow || decision.PolicyDigest != a.policyDigest ||
		decision.MetadataPolicyDigest != a.metadataPolicyDigest || !strings.HasPrefix(decision.ID, "decision-"):
		return errors.New("binding decision is not the configured allow decision")
	case binding.ID != fmt.Sprintf("transition-%06d", binding.Sequence):
		return fmt.Errorf("unexpected transition identity %q", binding.ID)
	}
	expectedOutput, known := a.trees[binding.OutputGeneration]
	if !known || binding.OutputTreeDigest != expectedOutput {
		return fmt.Errorf("binding output generation %q does not match the approved sealed tree", binding.OutputGeneration)
	}
	a.observed[binding.Sequence] = binding.OutputTreeDigest
	return nil
}

// durableE05BashRunner is a synthetic CommandRunner for the E05 fixture. It
// does not launch a process: it writes one new regular file into the private
// command view through the exported MountSource descriptor, returning a fully
// settled result so the durable Bash path imports an observably new generation.
type durableE05BashRunner struct {
	profile  string
	leaseID  string
	path     string
	content  string
	calls    int
	lastSpec sessionrepo.CommandSpec
	lastView string
}

func (r *durableE05BashRunner) Profile() string { return r.profile }

func (r *durableE05BashRunner) Run(_ context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	r.calls++
	if r.calls != 1 {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("e05 bash runner invoked more than once")
	}
	r.lastSpec = spec
	r.lastView = view.ID()
	mount, err := view.MountSource()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("open e05 command view: %w", err)
	}
	defer mount.Close()
	fd, err := syscall.Openat(int(mount.Fd()), r.path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("create e05 bash output file: %w", err)
	}
	file := os.NewFile(uintptr(fd), r.path)
	defer file.Close()
	if err := syscall.Fchown(fd, syscall.Geteuid(), syscall.Getegid()); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("normalize e05 output ownership: %w", err)
	}
	if err := syscall.Fchmod(fd, 0o644); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("normalize e05 output mode: %w", err)
	}
	if _, err := file.Write([]byte(r.content)); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("write e05 bash output: %w", err)
	}
	if err := file.Sync(); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("sync e05 bash output: %w", err)
	}
	if err := file.Close(); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("close e05 bash output: %w", err)
	}
	exitCode := 0
	result := sessionrepo.CommandResult{ExitCode: exitCode, ExitObserved: true, Stdout: []byte("e05 bash stdout\n")}
	settlement := sessionrepo.CommandSettlement{
		LeaseID: r.leaseID, ViewID: view.ID(),
		ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true,
		ExitObserved: true, ExitCode: exitCode, EvidenceClass: "synthetic-e05-bash-fixture",
	}
	return result, settlement, nil
}

// TestDurableE05TwoGenerationWorkflow is the normative E05 fixture. It asserts
// exact proposal<->capture binding by call ID and canonical argument digest, the
// gate verdicts, the sealed chain g0 ->(edit) g1 ->(bash) g2 with reads not
// advancing, correlation/result identity and sequence, and that the final read
// observes the content produced by the Bash generation transition.
func TestDurableE05TwoGenerationWorkflow(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable E05 workflow runs only on Linux")
	}
	store, journal, authority, g0 := e05Store(t, e05TaskBaseline)
	g1Expected := e05ExpectedSnapshot(t, e05TaskEdited, false)
	g2Expected := e05ExpectedSnapshot(t, e05TaskEdited, true)
	authority.setTree("g1", g1Expected.TreeDigest)
	authority.setTree("g2", g2Expected.TreeDigest)

	policy := e05Policy(t, "read", "edit", "bash")
	stream, err := protocol.NewStream(durableCallIssuer, durableResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}

	read1Args := json.RawMessage(`{"path":"` + e05TaskPath + `"}`)
	editArgs := json.RawMessage(`{"path":"` + e05TaskPath + `","edits":[{"oldText":"baseline","newText":"edited"}]}`)
	bashArgs := json.RawMessage(`{"command":"create bash output"}`)
	read2Args := json.RawMessage(`{"path":"` + e05BashOutput + `"}`)
	read1Digest := e05Capture(t, stream, e05ReadCallID0, e05ReadRespID0, "read", read1Args, "g0", 1)
	editDigest := e05Capture(t, stream, e05EditCallID, e05EditRespID, "edit", editArgs, "g1", 2)
	bashDigest := e05Capture(t, stream, e05BashCallID, e05BashRespID, "bash", bashArgs, "g2", 3)
	read2Digest := e05Capture(t, stream, e05ReadCallID2, e05ReadRespID2, "read", read2Args, "g2", 4)

	leaseID := e05BashLeaseID(t, e05BashCallID, e05BashRespID, 3, bashArgs)
	runner := &durableE05BashRunner{profile: e05RunnerLabel, leaseID: leaseID, path: e05BashOutput, content: e05BashContent}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		CommandRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewAuditDecisionRecorder(journal)
	if err != nil {
		t.Fatal(err)
	}

	client, server, err := ipc.NewPipe(integrationToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	var mu sync.Mutex
	events := make([]string, 0, 4)
	supervisor := &Supervisor{
		IPC: server, Broker: &streamBroker{stream: stream, events: &events, mu: &mu}, Policy: policy,
		Executor: durable, Decisions: recorder,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(context.Background()) }()

	// Step 1: read(g0). A read must return the sealed g0 bytes without advancing.
	result := e05Exchange(t, client, e05Proposal(e05ReadCallID0, "read", read1Args))
	e05AssertAllow(t, result, "read", e05ReadCallID0, e05ReadRespID0, read1Digest, 1)
	read1 := e05DecodeRead(t, result)
	if read1.Tool != "read" || read1.Path != e05TaskPath || read1.Generation.ID != "g0" ||
		read1.Generation.TreeDigest != g0.TreeDigest() || read1.Content != e05TaskBaseline ||
		read1.Size != len(e05TaskBaseline) || read1.ContentDigest != durableContentDigest([]byte(e05TaskBaseline)) ||
		read1.Outcome != "success" {
		t.Fatalf("unexpected read(g0) summary: %+v", read1)
	}
	if tip := durable.Tip(); tip == nil || tip.ID() != "g0" {
		t.Fatalf("read(g0) advanced the generation: %+v", tip)
	}

	// Step 2: edit(g1). The edit binds the g0 write to a sealed g1 transition.
	result = e05Exchange(t, client, e05Proposal(e05EditCallID, "edit", editArgs))
	e05AssertAllow(t, result, "edit", e05EditCallID, e05EditRespID, editDigest, 2)
	edit := e05DecodeMutation(t, result)
	e05AssertMutation(t, edit, "edit", "g1", g1Expected.TreeDigest, "transition-000001", 1)
	if tip := durable.Tip(); tip == nil || tip.ID() != "g1" || tip.TreeDigest() != g1Expected.TreeDigest {
		t.Fatalf("edit did not advance to sealed g1: %+v", tip)
	}
	if observed := authority.observedOutput(1); observed != g1Expected.TreeDigest {
		t.Fatalf("edit transition bound output %s; want %s", observed, g1Expected.TreeDigest)
	}

	// Step 3: Bash(g2). The settled command view imports as sealed g2.
	result = e05Exchange(t, client, e05Proposal(e05BashCallID, "bash", bashArgs))
	e05AssertAllow(t, result, "bash", e05BashCallID, e05BashRespID, bashDigest, 3)
	bash := e05DecodeMutation(t, result)
	e05AssertMutation(t, bash, "bash", "g2", g2Expected.TreeDigest, "transition-000002", 2)
	if bash.Command == nil || bash.Command.ExitCode != 0 || !bash.Command.ExitObserved ||
		bash.Command.CommandContainmentStatus != "not-established" || bash.Command.CommandRunnerProfile != e05RunnerLabel {
		t.Fatalf("unexpected bash command summary: %+v", bash.Command)
	}
	if tip := durable.Tip(); tip == nil || tip.ID() != "g2" || tip.TreeDigest() != g2Expected.TreeDigest {
		t.Fatalf("bash did not advance to sealed g2: %+v", tip)
	}
	if observed := authority.observedOutput(2); observed != g2Expected.TreeDigest {
		t.Fatalf("bash transition bound output %s; want %s", observed, g2Expected.TreeDigest)
	}
	if runner.calls != 1 {
		t.Fatalf("e05 bash runner ran %d times; want 1", runner.calls)
	}
	wantSpec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "create bash output"}}
	if runner.lastSpec.Executable != wantSpec.Executable || len(runner.lastSpec.Args) != len(wantSpec.Args) ||
		runner.lastSpec.Args[0] != wantSpec.Args[0] || runner.lastSpec.Args[1] != wantSpec.Args[1] {
		t.Fatalf("e05 bash argv = %+v; want %+v", runner.lastSpec, wantSpec)
	}

	// Step 4: read(g2). The final read must observe the object Bash produced,
	// and must not advance the generation.
	result = e05Exchange(t, client, e05Proposal(e05ReadCallID2, "read", read2Args))
	e05AssertAllow(t, result, "read", e05ReadCallID2, e05ReadRespID2, read2Digest, 4)
	read2 := e05DecodeRead(t, result)
	if read2.Tool != "read" || read2.Path != e05BashOutput || read2.Generation.ID != "g2" ||
		read2.Generation.TreeDigest != g2Expected.TreeDigest || read2.Content != e05BashContent ||
		read2.Size != len(e05BashContent) || read2.ContentDigest != durableContentDigest([]byte(e05BashContent)) {
		t.Fatalf("final read(g2) did not observe the Bash generation content: %+v", read2)
	}
	if tip := durable.Tip(); tip == nil || tip.ID() != "g2" {
		t.Fatalf("final read(g2) advanced the generation: %+v", tip)
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("supervisor did not shut down cleanly: %v", err)
	}

	// The sealed chain composes g0 -> g1 -> g2 and the two reads leave no delta.
	chain, err := store.Verify()
	if err != nil {
		t.Fatalf("verify e05 chain: %v", err)
	}
	if chain.TransitionCount != 2 || chain.BaselineGeneration != "g0" || chain.SealedGeneration != "g2" ||
		chain.SealedTreeDigest != g2Expected.TreeDigest {
		t.Fatalf("unexpected e05 chain: %+v", chain)
	}
	artifact, err := store.Evidence()
	if err != nil {
		t.Fatalf("build e05 evidence: %v", err)
	}
	if artifact.RealProviderExchange || artifact.PiAdapterWired || artifact.CommandContainmentStatus != "not-established" {
		t.Fatalf("e05 evidence overstates integration or containment: %+v", artifact)
	}
	if len(artifact.Generations) != 3 || len(artifact.ApprovedDeltaLedger) != 2 {
		t.Fatalf("e05 evidence generations/ledger = %d/%d; want 3/2",
			len(artifact.Generations), len(artifact.ApprovedDeltaLedger))
	}
	if artifact.ApprovedDeltaLedger[0].InputGeneration != "g0" || artifact.ApprovedDeltaLedger[0].OutputGeneration != "g1" ||
		artifact.ApprovedDeltaLedger[1].InputGeneration != "g1" || artifact.ApprovedDeltaLedger[1].OutputGeneration != "g2" {
		t.Fatalf("e05 ledger does not chain g0 -> g1 -> g2: %+v", artifact.ApprovedDeltaLedger)
	}
	wantOperations := []struct {
		tool, input, output string
	}{
		{"read", "g0", "g0"},
		{"edit", "g0", "g1"},
		{"bash", "g1", "g2"},
		{"read", "g2", "g2"},
	}
	if len(artifact.Operations) != len(wantOperations) {
		t.Fatalf("e05 evidence has %d operations; want %d", len(artifact.Operations), len(wantOperations))
	}
	for index, want := range wantOperations {
		got := artifact.Operations[index]
		if got.Tool != want.tool || got.InputGeneration != want.input || got.OutputGeneration != want.output ||
			got.Outcome != "success" || got.ResultDigest == "" {
			t.Fatalf("e05 operation %d = %+v; want tool=%s input=%s output=%s", index, got, want.tool, want.input, want.output)
		}
	}

	// The journal durably records four ALLOW gate decisions and exactly one
	// resolved effect per tool call; reads are effects too but never transitions.
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace e05 journal: %v", err)
	}
	e05AssertGateDecisions(t, trace, 4)
	for tool, want := range map[string]int{"read": 2, "edit": 1, "bash": 1} {
		if got := e05CountEffects(t, trace, tool); got != want {
			t.Fatalf("e05 journal has %d %q effects; want %d", got, tool, want)
		}
	}

	// Exit criterion #5 (reconstructable evidence): capture the self-contained
	// bundle, discard the live Store, and re-derive the identical disposition
	// from retained bytes alone.
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatalf("build e05 evidence bundle: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close e05 store before replay: %v", err)
	}
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil {
		t.Fatalf("reconstruct e05 evidence bundle without the store: %v", err)
	}
	if disposition.Quarantined || disposition.Baseline != "g0" || disposition.Sealed != "g2" ||
		disposition.Attempts != 5 || disposition.Decisions != 4 || disposition.Effects != 5 ||
		disposition.Successes != 5 || disposition.Unknowns != 0 || disposition.Denials != 0 {
		t.Fatalf("unexpected e05 reconstructed disposition: %+v", disposition)
	}
	if !reflect.DeepEqual(disposition.Generations, artifact.Generations) {
		t.Fatalf("e05 reconstructed generations differ from the evidence artifact")
	}
	if !reflect.DeepEqual(disposition.Ledger, artifact.ApprovedDeltaLedger) {
		t.Fatalf("e05 reconstructed ledger differs from the evidence artifact")
	}
	if !reflect.DeepEqual(disposition.Operations, artifact.Operations) {
		t.Fatalf("e05 reconstructed operations differ:\n got %+v\nwant %+v", disposition.Operations, artifact.Operations)
	}
}

// TestDurableE05AmbientBypassIsDenied proves a proposal with no matching trusted
// provider capture is DENIED, never reaches the executor, and has its denial
// durably recorded before any evidence of an effect.
func TestDurableE05AmbientBypassIsDenied(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable E05 workflow runs only on Linux")
	}
	store, journal, _, g0 := e05Store(t, e05TaskBaseline)
	policy := e05Policy(t, "read")
	stream, err := protocol.NewStream(durableCallIssuer, durableResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewAuditDecisionRecorder(journal)
	if err != nil {
		t.Fatal(err)
	}
	executor := &e05RecordingExecutor{inner: durable}
	client, server, err := ipc.NewPipe(integrationToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	var mu sync.Mutex
	events := make([]string, 0, 1)
	supervisor := &Supervisor{
		IPC: server, Broker: &streamBroker{stream: stream, events: &events, mu: &mu}, Policy: policy,
		Executor: executor, Decisions: recorder,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(context.Background()) }()

	proposal := e05Proposal("call-e05-ambient", "read", json.RawMessage(`{"path":"`+e05TaskPath+`"}`))
	result := e05Exchange(t, client, proposal)
	if result.Verdict != string(gate.Deny) || result.ReasonCode != "correlation_required" || len(result.Output) != 0 {
		t.Fatalf("ambient bypass was not denied before execution: %+v", result)
	}
	if calls := executor.calls.Load(); calls != 0 {
		t.Fatalf("executor ran %d times for an uncorrelated proposal; want 0", calls)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("supervisor did not shut down cleanly: %v", err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace ambient-bypass journal: %v", err)
	}
	e05AssertGateDecisions(t, trace, 1)
	if effects := e05CountEffects(t, trace, "read"); effects != 0 {
		t.Fatalf("ambient bypass produced %d durable read effects; want 0", effects)
	}
}

// TestDurableE05CancellationStopsSession proves a canceled supervisor context
// stops the durable session and releases no further result.
func TestDurableE05CancellationStopsSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable E05 workflow runs only on Linux")
	}
	store, journal, _, g0 := e05Store(t, e05TaskBaseline)
	policy := e05Policy(t, "read")
	stream, err := protocol.NewStream(durableCallIssuer, durableResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	readArgs := json.RawMessage(`{"path":"` + e05TaskPath + `"}`)
	e05Capture(t, stream, "call-e05-cancel-1", "response-e05-cancel-1", "read", readArgs, "g0", 1)
	e05Capture(t, stream, "call-e05-cancel-2", "response-e05-cancel-2", "read", readArgs, "g0", 2)
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewAuditDecisionRecorder(journal)
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := ipc.NewPipe(integrationToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	var mu sync.Mutex
	events := make([]string, 0, 2)
	supervisor := &Supervisor{
		IPC: server, Broker: &streamBroker{stream: stream, events: &events, mu: &mu}, Policy: policy,
		Executor: durable, Decisions: recorder,
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- supervisor.Serve(ctx) }()

	first := e05Exchange(t, client, e05Proposal("call-e05-cancel-1", "read", readArgs))
	if first.Verdict != string(gate.Allow) {
		t.Fatalf("first read was not allowed before cancellation: %+v", first)
	}

	cancel()
	if err := <-serveErr; err != nil {
		t.Fatalf("canceled supervisor did not stop cleanly: %v", err)
	}
	// The session is stopped: a later proposal must not release a result.
	if err := client.SendProposal(e05Proposal("call-e05-cancel-2", "read", readArgs)); err == nil {
		if _, err := client.ReceiveResult(); err == nil {
			t.Fatal("session released a result after cancellation")
		}
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatalf("trace cancellation journal: %v", err)
	}
	if effects := e05CountEffects(t, trace, "read"); effects != 1 {
		t.Fatalf("canceled session recorded %d read effects; want 1", effects)
	}
}

// TestDurableReadRejectsInvalidRequests proves the durable read path fails
// closed on a non-ALLOW verdict, empty/absolute/escaping/unknown paths,
// unsupported pagination, malformed arguments, and content that cannot fit the
// protocol bound.
func TestDurableReadRejectsInvalidRequests(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable read path runs only on Linux")
	}

	newExecutor := func(t *testing.T, taskContent string) (*sessionrepo.Store, *sessionrepo.Generation, *DurableExecutor) {
		t.Helper()
		store, _, _, g0 := e05Store(t, taskContent)
		executor, err := NewDurableExecutor(DurableExecutorConfig{
			Store: store, Tip: g0, CallIssuer: durableCallIssuer,
			PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		})
		if err != nil {
			t.Fatal(err)
		}
		return store, g0, executor
	}

	cases := []struct {
		name string
		args string
	}{
		{"empty path", `{"path":""}`},
		{"absolute path", `{"path":"/etc/passwd"}`},
		{"escaping path", `{"path":"../secret"}`},
		{"nested traversal", `{"path":"build/../task.txt"}`},
		{"offset unsupported", `{"path":"task.txt","offset":1}`},
		{"limit unsupported", `{"path":"task.txt","limit":2}`},
		{"unknown field", `{"path":"task.txt","extra":1}`},
		{"trailing json", `{"path":"task.txt"}{"path":"x"}`},
		{"missing path", `{}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store, g0, executor := newExecutor(t, e05TaskBaseline)
			decision := e05ReadDecision("call-e05-reject", "response-e05-reject", 1)
			output, err := executor.Execute(context.Background(), e05Proposal("call-e05-reject", "read", json.RawMessage(testCase.args)), decision)
			if err == nil || len(output) != 0 {
				t.Fatalf("durable read accepted a malformed request %s: output=%q err=%v", testCase.args, output, err)
			}
			if tip := executor.Tip(); tip == nil || tip.ID() != "g0" || tip.TreeDigest() != g0.TreeDigest() {
				t.Fatalf("rejected read advanced the generation: %+v", tip)
			}
			if _, err := store.Verify(); err != nil {
				t.Fatalf("rejected read disturbed the store: %v", err)
			}
		})
	}

	t.Run("non-allow verdict", func(t *testing.T) {
		_, _, executor := newExecutor(t, e05TaskBaseline)
		decision := e05ReadDecision("call-e05-reject", "response-e05-reject", 1)
		decision.Verdict = gate.Deny
		decision.ReasonCode = "policy_rule_deny"
		output, err := executor.Execute(context.Background(), e05Proposal("call-e05-reject", "read", json.RawMessage(`{"path":"task.txt"}`)), decision)
		if err == nil || len(output) != 0 {
			t.Fatalf("durable read ran under a non-ALLOW verdict: output=%q err=%v", output, err)
		}
	})

	t.Run("unknown path quarantines", func(t *testing.T) {
		store, _, executor := newExecutor(t, e05TaskBaseline)
		decision := e05ReadDecision("call-e05-reject", "response-e05-reject", 1)
		output, err := executor.Execute(context.Background(), e05Proposal("call-e05-reject", "read", json.RawMessage(`{"path":"missing.txt"}`)), decision)
		if err == nil || len(output) != 0 {
			t.Fatalf("durable read released a result for an unknown path: output=%q err=%v", output, err)
		}
		if _, err := store.Verify(); !errors.Is(err, sessionrepo.ErrQuarantined) {
			t.Fatalf("unknown-path read store state = %v; want quarantine", err)
		}
	})

	t.Run("file over read bound rejected", func(t *testing.T) {
		store, g0, executor := newExecutor(t, strings.Repeat("a", durableReadMaxBytes+1))
		decision := e05ReadDecision("call-e05-reject", "response-e05-reject", 1)
		output, err := executor.Execute(context.Background(), e05Proposal("call-e05-reject", "read", json.RawMessage(`{"path":"task.txt"}`)), decision)
		if err == nil || len(output) != 0 {
			t.Fatalf("durable read released an over-bound result: len=%d err=%v", len(output), err)
		}
		if tip := executor.Tip(); tip == nil || tip.ID() != "g0" || tip.TreeDigest() != g0.TreeDigest() {
			t.Fatalf("over-bound read advanced the generation: %+v", tip)
		}
		if _, err := store.Verify(); !errors.Is(err, sessionrepo.ErrQuarantined) {
			t.Fatalf("over-bound read store state = %v; want quarantine", err)
		}
	})

	t.Run("encoded result over protocol bound rejected", func(t *testing.T) {
		store, g0, executor := newExecutor(t, strings.Repeat("a", durableReadMaxBytes))
		decision := e05ReadDecision("call-e05-reject", "response-e05-reject", 1)
		output, err := executor.Execute(context.Background(), e05Proposal("call-e05-reject", "read", json.RawMessage(`{"path":"task.txt"}`)), decision)
		if err == nil || len(output) != 0 {
			t.Fatalf("durable read released a result over the protocol bound: len=%d err=%v", len(output), err)
		}
		if tip := executor.Tip(); tip == nil || tip.ID() != "g0" || tip.TreeDigest() != g0.TreeDigest() {
			t.Fatalf("over-protocol-bound read advanced the generation: %+v", tip)
		}
		// The read effect itself succeeded, so the store is not quarantined.
		if chain, err := store.Verify(); err != nil || chain.TransitionCount != 0 || chain.SealedGeneration != "g0" {
			t.Fatalf("over-protocol-bound read disturbed the store: chain=%+v err=%v", chain, err)
		}
	})
}

// e05RecordingExecutor wraps a real executor and counts invocations so a test
// can prove a denied proposal never reached the durable effect path.
type e05RecordingExecutor struct {
	inner Executor
	calls atomic.Int64
}

func (e *e05RecordingExecutor) Execute(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) (json.RawMessage, error) {
	e.calls.Add(1)
	return e.inner.Execute(ctx, proposal, decision)
}

// e05Store seeds a private session repository and returns it with its journal,
// fixture authority, and sealed baseline g0. The authority is pre-bound to g0.
func e05Store(t *testing.T, taskContent string) (*sessionrepo.Store, *audit.Journal, *durableE05Authority, *sessionrepo.Generation) {
	t.Helper()
	base := durablePrivateBase(t)
	storePath := filepath.Join(base, "repository")
	auditDir := filepath.Join(base, "audit")
	durableMkdir(t, storePath, 0o700)
	durableMkdir(t, auditDir, 0o700)
	sourcePath := filepath.Join(base, "source")
	durableMkdir(t, sourcePath, 0o700)
	durableWriteFile(t, filepath.Join(sourcePath, e05TaskPath), taskContent, 0o644)
	durableMkdir(t, filepath.Join(sourcePath, e05BuildDir), 0o755)
	durableWriteFile(t, filepath.Join(sourcePath, filepath.FromSlash(e05BuildFilePath)), e05BuildResult, 0o644)
	source := durableOpenRoot(t, sourcePath)

	journal, err := audit.Open(filepath.Join(auditDir, "journal.jsonl"))
	if err != nil {
		t.Fatalf("open e05 audit journal: %v", err)
	}
	authority := newDurableE05Authority()
	options := sessionrepo.Options{
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
		Limits:             workspace.DefaultLimits(),
		XattrVisibility:    workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
		Journal:            journal,
		AuthorizeOperation: authority.authorize,
		VerifyDecision:     authority.verifyDecision,
		VerifySettlement:   authority.verifySettlement,
	}
	store, err := sessionrepo.Create(durableOpenRoot(t, storePath), options)
	if err != nil {
		_ = journal.Close()
		t.Fatalf("create e05 session repository: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = journal.Close()
	})
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed e05 g0: %v", err)
	}
	authority.setTree("g0", g0.TreeDigest())
	return store, journal, authority, g0
}

// e05ExpectedSnapshot builds the expected sealed tree for the edited and
// Bash-produced generations with the same metadata profile the store uses.
func e05ExpectedSnapshot(t *testing.T, taskContent string, withBashOutput bool) workspace.Snapshot {
	t.Helper()
	rootPath := filepath.Join(t.TempDir(), "expected")
	durableMkdir(t, rootPath, 0o700)
	durableWriteFile(t, filepath.Join(rootPath, e05TaskPath), taskContent, 0o644)
	durableMkdir(t, filepath.Join(rootPath, e05BuildDir), 0o755)
	durableWriteFile(t, filepath.Join(rootPath, filepath.FromSlash(e05BuildFilePath)), e05BuildResult, 0o644)
	if withBashOutput {
		durableWriteFile(t, filepath.Join(rootPath, e05BashOutput), e05BashContent, 0o644)
	}
	root := durableOpenRoot(t, rootPath)
	snapshot, err := workspace.Scan(root, workspace.Options{
		Generation: "expected", MetadataPolicyDigest: durableMetadataDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: durableXattrDigest, Complete: true},
	})
	if err != nil {
		t.Fatalf("scan expected e05 tree: %v", err)
	}
	return snapshot
}

func e05Policy(t *testing.T, tools ...string) gate.Policy {
	t.Helper()
	rules := make([]gate.Rule, 0, len(tools))
	for _, tool := range tools {
		rules = append(rules, gate.Rule{Tool: tool, Effect: gate.EffectAllow})
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: rules})
	if err != nil {
		t.Fatalf("build e05 policy: %v", err)
	}
	return policy
}

func e05Capture(t *testing.T, stream *protocol.Stream, callID, responseID, tool string, arguments json.RawMessage, generation string, sequence uint64) string {
	t.Helper()
	digest, err := protocol.CanonicalArgumentsDigest(tool, arguments)
	if err != nil {
		t.Fatalf("canonical %s arguments: %v", tool, err)
	}
	capture := protocol.TrustedCapture{
		ResponseID: correlation.Identifier{Issuer: durableResponseIssuer, Opaque: responseID},
		ToolCallID: correlation.Identifier{Issuer: durableCallIssuer, Opaque: callID},
		ToolName:   tool, RawArguments: append(json.RawMessage(nil), arguments...),
		CanonicalizationProfile:  protocol.CanonicalizationProfile,
		CanonicalArgumentsDigest: digest, Sequence: sequence, Generation: generation,
	}
	if decision := stream.Capture(capture); !decision.Accepted || decision.StreamClosed {
		t.Fatalf("e05 capture %s: %+v", callID, decision)
	}
	return digest
}

func e05Proposal(callID, tool string, arguments json.RawMessage) protocol.Proposal {
	return protocol.Proposal{
		SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID:    callID, Tool: tool, Arguments: arguments,
	}
}

// e05ReadDecision builds a synthetic trusted ALLOW decision for one read call.
func e05ReadDecision(callID, responseID string, sequence uint64) gate.Decision {
	return gate.Decision{
		Verdict: gate.Allow, ReasonCode: "policy_rule_allow",
		PolicyDigest: "tbound-policy/v1:sha256:" + strings.Repeat("4", 64),
		ToolCallID:   callID, Tool: "read", Sequence: sequence,
		ResponseID: &correlation.Identifier{Issuer: durableResponseIssuer, Opaque: responseID},
	}
}

// e05BashLeaseID reproduces the deterministic lease identity the executor
// derives for a matched bash decision so the synthetic runner can settle it.
func e05BashLeaseID(t *testing.T, callID, responseID string, sequence uint64, arguments json.RawMessage) string {
	t.Helper()
	proposal := e05Proposal(callID, "bash", arguments)
	decision := gate.Decision{
		Verdict: gate.Allow, ToolCallID: callID, Tool: "bash", Sequence: sequence,
		ResponseID: &correlation.Identifier{Issuer: durableResponseIssuer, Opaque: responseID},
	}
	leaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil {
		t.Fatalf("derive e05 bash lease identity: %v", err)
	}
	return leaseID
}

func e05Exchange(t *testing.T, client *ipc.Client, proposal protocol.Proposal) protocol.Result {
	t.Helper()
	writeErr := make(chan error, 1)
	go func() { writeErr <- client.SendProposal(proposal) }()
	result, err := client.ReceiveResult()
	if err != nil {
		t.Fatalf("receive %s result: %v", proposal.Tool, err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("send %s proposal: %v", proposal.Tool, err)
	}
	return result
}

func e05AssertAllow(t *testing.T, result protocol.Result, tool, callID, responseID, digest string, sequence uint64) {
	t.Helper()
	if result.Verdict != string(gate.Allow) || result.ReasonCode != "policy_rule_allow" ||
		result.Tool != tool || result.ToolCallID != callID || result.Sequence != sequence ||
		result.CanonicalArgumentsDigest != digest || result.ResponseID == nil ||
		result.ResponseID.Issuer != durableResponseIssuer || result.ResponseID.Opaque != responseID {
		t.Fatalf("unexpected %s result: %+v", tool, result)
	}
}

func e05DecodeRead(t *testing.T, result protocol.Result) durableReadSummaryPayload {
	t.Helper()
	var summary durableReadSummaryPayload
	if err := json.Unmarshal(result.Output, &summary); err != nil {
		t.Fatalf("decode e05 read summary %q: %v", result.Output, err)
	}
	return summary
}

func e05DecodeMutation(t *testing.T, result protocol.Result) durableMutationSummaryPayload {
	t.Helper()
	if err := protocol.ValidateStrictJSON(result.Output); err != nil {
		t.Fatalf("e05 mutation output is not strict JSON: %v", err)
	}
	var summary durableMutationSummaryPayload
	if err := json.Unmarshal(result.Output, &summary); err != nil {
		t.Fatalf("decode e05 mutation summary %q: %v", result.Output, err)
	}
	return summary
}

func e05AssertMutation(t *testing.T, summary durableMutationSummaryPayload, tool, generation, treeDigest, transitionID string, sequence uint64) {
	t.Helper()
	if summary.Tool != tool || summary.Generation.ID != generation || summary.Generation.TreeDigest != treeDigest ||
		summary.Transition.ID != transitionID || summary.Transition.Sequence != sequence ||
		!strings.HasPrefix(summary.EffectID, "sessionrepo-effect-") || summary.Outcome != "success" {
		t.Fatalf("unexpected e05 %s summary: %+v", tool, summary)
	}
}

func e05AssertGateDecisions(t *testing.T, trace audit.Trace, want int) {
	t.Helper()
	found := 0
	for _, record := range trace.Records {
		if record.Event.Kind != durableDecisionEventKind {
			continue
		}
		found++
		var event durableDecisionEvent
		if err := json.Unmarshal(record.Event.Data, &event); err != nil {
			t.Fatalf("decode e05 gate_decision %d: %v", record.Sequence, err)
		}
		if !strings.HasPrefix(event.Proposal.ToolCallID, "call-e05-") ||
			event.Decision.ToolCallID != event.Proposal.ToolCallID ||
			event.Decision.PolicyDigest == "" {
			t.Fatalf("unexpected e05 gate_decision payload: %+v", event)
		}
		if event.Decision.Verdict == gate.Allow && event.Decision.CanonicalArgumentsDigest == "" {
			t.Fatalf("allowed e05 gate_decision lacks a canonical argument digest: %+v", event)
		}
	}
	if found != want {
		t.Fatalf("e05 journal has %d gate_decision records; want %d", found, want)
	}
}

func e05CountEffects(t *testing.T, trace audit.Trace, tool string) int {
	t.Helper()
	marker := `"tool":"` + tool + `"`
	count := 0
	for _, effect := range trace.Effects {
		if !strings.Contains(string(effect.Intent), marker) {
			continue
		}
		count++
		if effect.Unresolved || effect.Outcome != "success" || effect.IntentSequence == 0 || effect.OutcomeSequence == 0 {
			t.Fatalf("e05 %q effect is not a resolved success: %+v", tool, effect)
		}
	}
	return count
}
