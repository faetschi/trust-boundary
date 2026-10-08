package piruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
)

func TestProductionAdmissionRequiresHostProfileAndAttestation(t *testing.T) {
	profile, attestation := testHostProfile()
	if err := ValidateHostAdmission(profile, HostAttestation{}); !errors.Is(err, ErrHostAttestationMissing) {
		t.Fatalf("missing host attestation error = %v", err)
	}
	if err := ValidateHostAdmission(profile, attestation); err != nil {
		t.Fatalf("valid structural attestation rejected: %v", err)
	}
	if _, err := AdmitProduction(context.Background(), profile, nil); !errors.Is(err, ErrHostAttestationMissing) {
		t.Fatalf("nil host verifier was accepted: %v", err)
	}
	if _, err := AdmitProduction(context.Background(), profile, staticHostAttestor{attestation: attestation}); !errors.Is(err, ErrHostBindingsMissing) {
		t.Fatalf("synthetic receipt without trusted runtime bindings was accepted: %v", err)
	}
	componentMismatches := []struct {
		name string
		set  func(*HostAttestation)
	}{
		{"executable", func(a *HostAttestation) { a.ExecutableDigest = DigestBytes([]byte("different-executable")) }},
		{"arguments", func(a *HostAttestation) { a.ChildArgumentsDigest = DigestBytes([]byte("different-arguments")) }},
		{"working-directory", func(a *HostAttestation) { a.WorkingDirectoryDigest = DigestBytes([]byte("different-cwd")) }},
		{"stdin", func(a *HostAttestation) { a.StdinDigest = DigestBytes([]byte("different-stdin")) }},
		{"environment", func(a *HostAttestation) { a.ChildEnvironmentDigest = DigestBytes([]byte("different-environment")) }},
		{"ipc", func(a *HostAttestation) { a.IPCProfileDigest = DigestBytes([]byte("different-ipc")) }},
		{"terminal", func(a *HostAttestation) { a.TerminalDigest = DigestBytes([]byte("different-terminal")) }},
		{"containment", func(a *HostAttestation) { a.ContainmentDigest = DigestBytes([]byte("different-containment")) }},
		{"settlement", func(a *HostAttestation) { a.SettlementDigest = DigestBytes([]byte("different-settlement")) }},
		{"observation", func(a *HostAttestation) { a.ObservationDigest = DigestBytes([]byte("different-observation")) }},
		{"source", func(a *HostAttestation) { a.SourceDigest = DigestBytes([]byte("different-source")) }},
		{"offline-verifier", func(a *HostAttestation) { a.OfflineVerifierDigest = DigestBytes([]byte("different-verifier")) }},
		{"provider", func(a *HostAttestation) { a.ProviderProfileDigest = DigestBytes([]byte("different-provider")) }},
	}
	for _, mismatch := range componentMismatches {
		t.Run(mismatch.name, func(t *testing.T) {
			candidate := attestation
			mismatch.set(&candidate)
			if err := ValidateHostAdmission(profile, candidate); !errors.Is(err, ErrHostProfileMismatch) {
				t.Fatalf("mismatched %s receipt was accepted: %v", mismatch.name, err)
			}
		})
	}
	profile.Mode = ModeFixture
	if err := ValidateHostAdmission(profile, attestation); !errors.Is(err, ErrHostAdmissionRefused) {
		t.Fatalf("fixture profile was admitted as production: %v", err)
	}
}

func TestServeRefusesEffectCapableSessionWithoutDecisionRecorder(t *testing.T) {
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := ipc.NewPipe("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	executed := false
	supervisor := &Supervisor{
		IPC: server, Broker: denyTestBroker{}, Policy: policy,
		Executor: executorTestFunc(func(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error) {
			executed = true
			return json.RawMessage(`{}`), nil
		}),
	}
	if err := supervisor.Serve(context.Background()); err == nil {
		t.Fatal("effect-capable supervisor without a decision recorder was admitted")
	}
	if executed {
		t.Fatal("executor ran despite missing decision recorder")
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "missing-recorder", Tool: "write", Arguments: json.RawMessage(`{"path":"x","content":"y"}`)}
	if err := client.SendProposal(proposal); err == nil {
		t.Fatal("supervisor released an IPC result after refusing the unrecorded session")
	}
}

func TestNoEffectSessionRequiresNoExecutorAndNeverCallsEffects(t *testing.T) {
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	client, server, err := ipc.NewPipe("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	called := false
	supervisor := &Supervisor{
		IPC: server, Broker: denyTestBroker{}, Policy: policy, NoEffect: &NoEffectSession{},
		Executor: executorTestFunc(func(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error) {
			called = true
			return nil, nil
		}),
	}
	if err := supervisor.Serve(context.Background()); err == nil {
		t.Fatal("no-effect fixture admitted a configured executor")
	}
	if called {
		t.Fatal("no-effect fixture called an executor")
	}
}

type denyTestBroker struct{}

func (denyTestBroker) Correlate(context.Context, protocol.Proposal) (correlation.Decision, error) {
	return correlation.Decision{}, nil
}

type executorTestFunc func(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error)

func (f executorTestFunc) Execute(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) (json.RawMessage, error) {
	return f(ctx, proposal, decision)
}

func TestUnboundAbsoluteChildIsRefusedBeforeLauncher(t *testing.T) {
	profile, attestation := testHostProfile()
	launcher := &countingLauncher{}
	bindings := testRuntimeBindings(profile, launcher)
	capability, err := AdmitProduction(context.Background(), profile, staticHostAttestor{attestation: attestation, bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	spec := testChildSpec(profile, bindings)
	if _, err := StartChild(context.Background(), spec); !errors.Is(err, ErrChildCapabilityMissing) {
		t.Fatalf("unbound absolute helper was not refused: %v", err)
	}
	if launcher.calls != 0 || capability == nil {
		t.Fatalf("unbound child caused launcher effects: calls=%d capability=%v", launcher.calls, capability != nil)
	}
}

func TestAdmissionCapabilityBindsExactChildEnvironment(t *testing.T) {
	profile, attestation := testHostProfile()
	launcher := &countingLauncher{}
	bindings := testRuntimeBindings(profile, launcher)
	capability, err := AdmitProduction(context.Background(), profile, staticHostAttestor{attestation: attestation, bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	spec := testChildSpec(profile, bindings)
	bound, err := capability.BindChild(spec, launcher)
	if err != nil {
		t.Fatalf("bind empty allow-list: %v", err)
	}
	if bound.capability == nil || bound.capability.bound == nil || bound.capability.bound.launcher != launcher {
		t.Fatal("bound child did not retain the admitted launcher capability")
	}
	if got := bound.Env; got == nil {
		t.Fatal("bound child converted the exact empty environment back to nil")
	}
	if _, err := capability.BindChild(spec, &countingLauncher{}); !errors.Is(err, ErrChildCapabilityMissing) {
		t.Fatalf("unadmitted launcher was accepted: %v", err)
	}
	spec.Env = []string{"INHERITED_SENTINEL=must-not-enter"}
	if _, err := capability.BindChild(spec, launcher); !errors.Is(err, ErrHostProfileMismatch) {
		t.Fatalf("unbound environment was accepted: %v", err)
	}
}

func TestAdmissionCapabilityCopiesExactEnvironmentVector(t *testing.T) {
	profile, attestation := testHostProfile()
	launcher := &countingLauncher{}
	bindings := testRuntimeBindings(profile, launcher)
	bindings.Environment = []string{"TBOUND_RUNTIME=offline"}
	profile.ChildEnvironmentDigest = DigestEnvironment(bindings.Environment)
	attestation.ChildEnvironmentDigest = profile.ChildEnvironmentDigest
	capability, err := AdmitProduction(context.Background(), profile, staticHostAttestor{attestation: attestation, bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	spec := testChildSpec(profile, bindings)
	bindings.Environment[0] = "INHERITED_SENTINEL=must-not-enter"
	bound, err := capability.BindChild(spec, launcher)
	if err != nil {
		t.Fatalf("mutating verifier-owned source slice changed admitted environment: %v", err)
	}
	bound.Env = []string{"post-bind-mutation=ignored"}
	if _, err := StartChild(context.Background(), bound); err == nil {
		t.Fatal("test launcher unexpectedly returned a process")
	}
	request := launcher.Request()
	if len(request.Env) != 1 || request.Env[0] != "TBOUND_RUNTIME=offline" {
		t.Fatalf("launcher did not receive the immutable exact environment: %#v", request.Env)
	}
}

func TestBoundChildIgnoresPostBindAuthorityFieldSwaps(t *testing.T) {
	profile, attestation := testHostProfile()
	launcher := &processLauncher{}
	bindings := testRuntimeBindings(profile, launcher)
	spec := testChildSpec(profile, bindings)
	expectedArguments := append([]string(nil), bindings.Arguments...)
	trustedSettler := bindings.Descendant
	capability, err := AdmitProduction(context.Background(), profile, staticHostAttestor{attestation: attestation, bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	bindings.Arguments[0] = "--mutated-verifier-slice"
	bound, err := capability.BindChild(spec, launcher)
	if err != nil {
		t.Fatal(err)
	}
	// These fields are intentionally exported for the source-compatible builder
	// shape. They must not be authority after BindChild has captured its private
	// immutable snapshot.
	bound.Executable = filepath.Join(os.TempDir(), "untrusted-after-bind")
	bound.Args = []string{"untrusted-argument"}
	bound.Dir = filepath.Join(os.TempDir(), "untrusted-cwd")
	bound.Stdin = bytes.NewReader([]byte("untrusted stdin"))
	bound.Env = []string{"INHERITED_SENTINEL=must-not-enter"}
	bound.Mode = IOModeObservation
	bound.TUIOutput = panicWriter{}
	bound.Observation = channelObservationSink{events: make(chan Observation, 1)}
	bound.ObservationMax = 1
	bound.Descendant = NoDescendants{}

	process, err := StartChild(context.Background(), bound)
	if err != nil {
		t.Fatalf("post-bind field mutation changed the bound launch: %v", err)
	}
	settlement, err := process.Settle(context.Background())
	if err != nil || settlement.State != SettlementStopped {
		t.Fatalf("trusted post-bind settlement was not retained: settlement=%+v err=%v", settlement, err)
	}
	request := launcher.Request()
	if request.Path != profile.ChildExecutable || request.Digest != profile.ExecutableDigest || request.Env == nil || len(request.Env) != 0 ||
		!equalStrings(request.Args, expectedArguments) || request.Dir != profile.WorkingDirectory ||
		!sameBinding(request.WorkingDirectoryHandle, testWorkingDirectoryHandle) || !sameBinding(request.Stdin, spec.Stdin) {
		t.Fatalf("post-bind mutation changed trusted launch request: %+v", request)
	}
	if got := trustedSettler.(*countingSettler).Calls(); got != 1 {
		t.Fatalf("post-bind NoDescendants swap bypassed trusted settlement: calls=%d", got)
	}
}

func TestBindChildRejectsUnadmittedInitialLaunchPlan(t *testing.T) {
	profile, attestation := testHostProfile()
	launcher := &countingLauncher{}
	bindings := testRuntimeBindings(profile, launcher)
	capability, err := AdmitProduction(context.Background(), profile, staticHostAttestor{attestation: attestation, bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name string
		edit func(*ChildSpec)
	}{
		{"arguments", func(spec *ChildSpec) { spec.Args = []string{"--unreviewed"} }},
		{"working-directory", func(spec *ChildSpec) { spec.Dir = filepath.Join(os.TempDir(), "unreviewed-cwd") }},
		{"stdin-endpoint", func(spec *ChildSpec) { spec.Stdin = bytes.NewReader(nil) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			spec := testChildSpec(profile, bindings)
			mutation.edit(&spec)
			if _, err := capability.BindChild(spec, launcher); !errors.Is(err, ErrHostProfileMismatch) {
				t.Fatalf("unadmitted initial %s was accepted: %v", mutation.name, err)
			}
			if launcher.calls != 0 {
				t.Fatalf("unadmitted initial %s started launcher %d times", mutation.name, launcher.calls)
			}
		})
	}
}

func TestAdmissionCloseCancelsAndDrainsLeases(t *testing.T) {
	admission := NewAdmission()
	if err := admission.Activate(); err != nil {
		t.Fatal(err)
	}
	lease, err := admission.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := admission.CloseAndWait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close unexpectedly drained a held lease: %v", err)
	}
	select {
	case <-lease.Context().Done():
	default:
		t.Fatal("closing admission did not cancel the lease context")
	}
	lease.Release()
	if err := admission.CloseAndWait(context.Background()); err != nil {
		t.Fatalf("drained admission did not close: %v", err)
	}
	if _, err := admission.Acquire(context.Background()); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatalf("closed admission accepted a new lease: %v", err)
	}
}

func TestSessionCloseDeadlineRetriesSharedCleanup(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestPiruntimeNoopChild$")
	cmd.Env = append(os.Environ(), "TBOUND_PIRUNTIME_BLOCK_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	settler := &blockingSettler{started: make(chan struct{}), release: make(chan struct{})}
	process := newProcess(cmd, settler, nil, false)
	session, err := NewSession(process)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := session.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	settlement, err := session.Close(short)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || settlement.State != SettlementUnknown {
		t.Fatalf("first session close was not honest UNKNOWN: settlement=%+v err=%v", settlement, err)
	}
	lease.Release()
	select {
	case <-settler.started:
	case <-time.After(time.Second):
		t.Fatalf("shared cleanup did not reach descendant settlement: session=%s child=%+v", session.State(), process.LastSettlement())
	}
	secondContext, secondCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	settlement, err = session.Close(secondContext)
	secondCancel()
	if !errors.Is(err, context.DeadlineExceeded) || settlement.State != SettlementUnknown {
		t.Fatalf("second session close did not preserve caller deadline: settlement=%+v err=%v", settlement, err)
	}
	close(settler.release)
	settlement, err = session.Close(context.Background())
	if err != nil || settlement.State != SettlementStopped || !settlement.DescendantsSettled {
		t.Fatalf("session retry did not return shared terminal settlement: settlement=%+v err=%v", settlement, err)
	}
}

func TestSessionLeaseDrainTimeoutStopsChildAndAllowsRetry(t *testing.T) {
	previousBudget := sessionCleanupBudget
	sessionCleanupBudget = 20 * time.Millisecond
	defer func() { sessionCleanupBudget = previousBudget }()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestPiruntimeNoopChild$")
	cmd.Env = append(os.Environ(), "TBOUND_PIRUNTIME_BLOCK_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := newProcess(cmd, NoDescendants{}, nil, false)
	session, err := NewSession(process)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := session.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := session.Close(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || settlement.State != SettlementUnknown {
		t.Fatalf("lease drain timeout did not report UNKNOWN: settlement=%+v err=%v", settlement, err)
	}
	select {
	case <-process.completion:
	case <-time.After(time.Second):
		t.Fatal("child remained running after lease drain timeout")
	}
	lease.Release()
	settlement, err = session.Close(context.Background())
	if err != nil || settlement.State != SettlementStopped || !settlement.ExitObserved || !settlement.DescendantsSettled {
		t.Fatalf("retry did not finish actual child settlement: settlement=%+v err=%v", settlement, err)
	}
}

func TestSessionCleanupWorkerSaturationClosesAdmissionAndRefuses(t *testing.T) {
	session, err := NewSession(completedProcess(nil))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxSessionCleanupWorkers; i++ {
		sessionCleanupWorkers <- struct{}{}
	}
	defer func() {
		for i := 0; i < maxSessionCleanupWorkers; i++ {
			<-sessionCleanupWorkers
		}
	}()
	settlement, err := session.Close(context.Background())
	if !errors.Is(err, ErrSessionCleanupCapacity) || settlement.State != SettlementUnknown || !session.Admission().Closed() || session.State() != SettlementUnknown {
		t.Fatalf("saturated session cleanup did not refuse with admission closed: settlement=%+v state=%s err=%v", settlement, session.State(), err)
	}
}

func TestObservationWriterIsBoundedAndStructured(t *testing.T) {
	events := make(chan Observation, 4)
	writer, err := newObservationWriter("stdout", channelObservationSink{events: events}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write([]byte("abcd")); err != nil || n != 4 {
		t.Fatalf("bounded observation write = %d, %v", n, err)
	}
	first := receiveObservation(t, events)
	if string(first.Data) != "abc" || !first.Truncated {
		t.Fatalf("unexpected bounded observation: %+v", first)
	}
	if _, err := writer.Write([]byte("ef")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(context.Background()); !errors.Is(err, ErrObservationCleanup) {
		t.Fatalf("bounded truncation did not surface an observation gap: %v", err)
	}
	marker := receiveObservation(t, events)
	if marker.DroppedBytes != 2 || !marker.Truncated {
		t.Fatalf("unexpected repeated truncation observation: %+v", marker)
	}
}

func TestBlockedObservationSinkDoesNotStallChildWriter(t *testing.T) {
	sink := &blockingObservationSink{started: make(chan struct{}), release: make(chan struct{})}
	writer, err := newObservationWriter("stdout", sink, MaxObservationBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("observation worker did not start")
	}
	start := time.Now()
	for i := 0; i < observationQueueSize+4; i++ {
		if _, err := writer.Write([]byte("bounded")); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("blocked observer stalled child writes for %s", elapsed)
	}
	if writer.DroppedBytes() == 0 {
		t.Fatal("backpressure did not expose an observation gap")
	}
	closeContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := writer.Close(closeContext); !errors.Is(err, ErrObservationCleanup) {
		t.Fatalf("stuck observer cleanup error = %v", err)
	}
	cancel()
	close(sink.release)
	if err := writer.Close(context.Background()); !errors.Is(err, ErrObservationCleanup) {
		t.Fatalf("backpressure gap was not retained after observer release: %v", err)
	}
}

func TestContextAwareObservationSinkReceivesCleanupCancellation(t *testing.T) {
	sink := &cancelAwareObservationSink{started: make(chan struct{}), canceled: make(chan struct{})}
	writer, err := newObservationWriter("stdout", sink, 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("cancel-me")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("context-aware observer did not start")
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := writer.Close(cleanupContext); !errors.Is(err, ErrObservationCleanup) {
		t.Fatalf("context-aware observer cleanup error = %v", err)
	}
	cancel()
	select {
	case <-sink.canceled:
	case <-time.After(time.Second):
		t.Fatal("observer did not receive cleanup cancellation")
	}
	if err := writer.Close(context.Background()); !errors.Is(err, ErrObservationCleanup) {
		t.Fatalf("cancelled observer failure was not retained: %v", err)
	}
}

func TestProcessAndSessionExposeObserverCleanupGap(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sink := &blockingObservationSink{started: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(sink.release)
		}
	}()
	writer, err := newObservationWriter("stdout", sink, 64)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestPiruntimeObserveOutputChild$")
	cmd.Env = append(os.Environ(), "TBOUND_PIRUNTIME_OBSERVE_CHILD=1")
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		closeObservationWriters([]*observationWriter{writer})
		t.Fatal(err)
	}
	process := newProcess(cmd, NoDescendants{}, []*observationWriter{writer}, false)
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("observer callback did not start from child output")
	}
	select {
	case <-process.completion:
	case <-time.After(2 * time.Second):
		t.Fatal("process wait did not finish after bounded observer cleanup")
	}
	session, err := NewSession(process)
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := session.Close(context.Background())
	if !errors.Is(err, ErrObservationCleanup) || settlement.State != SettlementStopped || !settlement.ExitObserved ||
		!settlement.DescendantsSettled || !settlement.ObservationGap || settlement.ObservationDetail == "" || session.State() != SettlementStopped {
		t.Fatalf("session hid observer cleanup gap or falsified process settlement: result=%+v state=%s err=%v", settlement, session.State(), err)
	}
	close(sink.release)
	released = true
	if err := writer.Close(context.Background()); err != nil {
		t.Fatalf("observer worker did not release after callback returned: %v", err)
	}
}

func TestProcessAndSessionRetainObservationSinkError(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sinkFailure := errors.New("observation sink rejected event")
	writer, err := newObservationWriter("stdout", failingObservationSink{err: sinkFailure}, 64)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestPiruntimeObserveOutputChild$")
	cmd.Env = append(os.Environ(), "TBOUND_PIRUNTIME_OBSERVE_CHILD=1")
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		closeObservationWriters([]*observationWriter{writer})
		t.Fatal(err)
	}
	process := newProcess(cmd, NoDescendants{}, []*observationWriter{writer}, false)
	select {
	case <-process.completion:
	case <-time.After(2 * time.Second):
		t.Fatal("process wait did not complete after observer rejection")
	}
	session, err := NewSession(process)
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := session.Close(context.Background())
	if !errors.Is(err, ErrObservationCleanup) || !errors.Is(err, sinkFailure) || settlement.State != SettlementStopped ||
		!settlement.ObservationGap || settlement.ObservationDroppedBytes == 0 || settlement.ObservationDetail == "" {
		t.Fatalf("sink failure was hidden from terminal settlement: result=%+v err=%v", settlement, err)
	}
}

func TestRepeatedObservationStartCloseReleasesBoundedWorkers(t *testing.T) {
	for i := 0; i < maxObservationWorkers*2; i++ {
		events := make(chan Observation, 1)
		writer, err := newObservationWriter("stdout", channelObservationSink{events: events}, 32)
		if err != nil {
			t.Fatalf("observation worker %d registration: %v", i, err)
		}
		if _, err := writer.Write([]byte("bounded")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(context.Background()); err != nil {
			t.Fatalf("observation worker %d cleanup: %v", i, err)
		}
	}
}

func TestWorkerPermitsRefuseSaturation(t *testing.T) {
	for i := 0; i < maxObservationWorkers; i++ {
		if !tryAcquireObservationWorker() {
			t.Fatalf("observation worker permit %d was unavailable before saturation", i)
		}
	}
	if tryAcquireObservationWorker() {
		t.Fatal("observation worker registration exceeded the global bound")
	}
	for i := 0; i < maxObservationWorkers; i++ {
		releaseObservationWorker()
	}

	for i := 0; i < maxSettlementWorkers; i++ {
		if !tryAcquireSettlementWorker() {
			t.Fatalf("settlement worker permit %d was unavailable before saturation", i)
		}
	}
	if tryAcquireSettlementWorker() {
		t.Fatal("settlement worker registration exceeded the global bound")
	}
	for i := 0; i < maxSettlementWorkers; i++ {
		releaseSettlementWorker()
	}
}

func TestSettlementWithoutDescendantProofStaysUnknown(t *testing.T) {
	process := completedProcess(nil)
	settlement, err := process.Settle(context.Background())
	if !errors.Is(err, ErrUnknownSettlement) || settlement.State != SettlementUnknown || !settlement.ExitObserved {
		t.Fatalf("missing descendant proof was not UNKNOWN: settlement=%+v err=%v", settlement, err)
	}

	settledProcess := completedProcess(NoDescendants{})
	settlement, err = settledProcess.Settle(context.Background())
	if err != nil || settlement.State != SettlementStopped || !settlement.DescendantsSettled {
		t.Fatalf("explicit fixture descendant proof did not settle: settlement=%+v err=%v", settlement, err)
	}
}

func TestSettlementTimeoutDoesNotPoisonRetry(t *testing.T) {
	settler := &blockingSettler{started: make(chan struct{}), release: make(chan struct{})}
	process := completedProcess(settler)
	settleCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	settlement, err := process.Settle(settleCtx)
	if !errors.Is(err, context.DeadlineExceeded) || settlement.State != SettlementUnknown {
		t.Fatalf("settlement timeout was not UNKNOWN: settlement=%+v err=%v", settlement, err)
	}
	close(settler.release)
	settlement, err = process.Settle(context.Background())
	if err != nil || settlement.State != SettlementStopped || !settlement.DescendantsSettled {
		t.Fatalf("retry did not observe eventual settlement: settlement=%+v err=%v", settlement, err)
	}
}

func TestSettlerReceivesOwnedFiniteCleanupContext(t *testing.T) {
	previousBudget := settlementCleanupBudget
	settlementCleanupBudget = 20 * time.Millisecond
	defer func() { settlementCleanupBudget = previousBudget }()
	settler := &cancelAwareSettler{started: make(chan struct{}), canceled: make(chan struct{})}
	process := completedProcess(settler)
	if !tryAcquireSettlementWorker() {
		t.Fatal("settlement worker permit unavailable")
	}
	process.settlementSlotHeld = true
	settlement, err := process.Settle(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || settlement.State != SettlementUnknown {
		t.Fatalf("owned settler deadline was not UNKNOWN: settlement=%+v err=%v", settlement, err)
	}
	select {
	case <-settler.canceled:
	case <-time.After(time.Second):
		t.Fatal("settler did not receive the owned cleanup deadline")
	}
	if !tryAcquireSettlementWorker() {
		t.Fatal("settlement worker permit was not released after cleanup")
	}
	releaseSettlementWorker()
}

func TestConcurrentSettlementSharesOneDescendantObservation(t *testing.T) {
	settler := &countingSettler{}
	process := completedProcess(settler)
	results := make(chan Settlement, 8)
	errs := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		go func() {
			result, err := process.Settle(context.Background())
			results <- result
			errs <- err
		}()
	}
	for i := 0; i < cap(results); i++ {
		if result := <-results; result.State != SettlementStopped {
			t.Fatalf("concurrent settlement state = %s", result.State)
		}
		if err := <-errs; err != nil {
			t.Fatalf("concurrent settlement error = %v", err)
		}
	}
	if settler.calls != 1 {
		t.Fatalf("descendant settler called %d times, want one", settler.calls)
	}
}

func TestChildSpecKeepsTUIAndObservationSeparate(t *testing.T) {
	if err := (ChildSpec{Executable: "pi", Mode: IOModeNativeTUI}).validate(); err == nil {
		t.Fatal("relative child executable was accepted")
	}
	if err := (ChildSpec{Executable: testExecutablePath(), Mode: IOModeNativeTUI}).validate(); err == nil {
		t.Fatal("native TUI without a terminal output was accepted")
	}
	if err := (ChildSpec{Executable: testExecutablePath(), Mode: IOModeObservation, Observation: channelObservationSink{events: make(chan Observation, 1)}, ObservationMax: 32, TUIOutput: io.Discard}).validate(); err == nil {
		t.Fatal("observation and TUI output were allowed to share a channel")
	}
}

func testHostProfile() (HostProfile, HostAttestation) {
	profile := HostProfile{
		ID: "profile-v1", Digest: DigestBytes([]byte("profile-v1")), Mode: ModeProduction,
		ChildExecutable: testExecutablePath(), ChildEnvironment: "env-v1", ChildEnvironmentDigest: DigestEnvironment(nil),
		ChildArgumentsProfile: "args-v1", ChildArgumentsDigest: DigestArguments([]string{"--native-tbound-host"}),
		WorkingDirectory: os.TempDir(), WorkingDirectoryDigest: DigestBytes([]byte(os.TempDir())),
		StdinProfile: "terminal-input-v1", StdinDigest: DigestBytes([]byte("terminal-input-v1")),
		IPCProfile: "ipc-v1", TerminalProfile: "terminal-v1", ContainmentProfile: "containment-v1",
		SettlementProfile: "settlement-v1", ObservationProfile: "observation-v1", SourceProvenance: "source-v1",
		OfflineVerifier: "verifier-v1", ProviderProfile: "provider-v1",
	}
	profile.ExecutableDigest = DigestBytes([]byte("pi"))
	profile.IPCProfileDigest = DigestBytes([]byte(profile.IPCProfile))
	profile.TerminalProfileDigest = DigestBytes([]byte(profile.TerminalProfile))
	profile.ContainmentProfileDigest = DigestBytes([]byte(profile.ContainmentProfile))
	profile.SettlementProfileDigest = DigestBytes([]byte(profile.SettlementProfile))
	profile.ObservationProfileDigest = DigestBytes([]byte(profile.ObservationProfile))
	profile.SourceProvenanceDigest = DigestBytes([]byte(profile.SourceProvenance))
	profile.OfflineVerifierDigest = DigestBytes([]byte(profile.OfflineVerifier))
	profile.ProviderProfileDigest = DigestBytes([]byte(profile.ProviderProfile))
	attestation := HostAttestation{
		HostID: "host-v1", ProfileID: profile.ID, ProfileDigest: profile.Digest,
		ExecutableDigest: profile.ExecutableDigest, ChildArgumentsDigest: profile.ChildArgumentsDigest,
		WorkingDirectoryDigest: profile.WorkingDirectoryDigest, StdinDigest: profile.StdinDigest,
		ChildEnvironmentDigest: profile.ChildEnvironmentDigest,
		IPCProfileDigest:       profile.IPCProfileDigest, TerminalDigest: profile.TerminalProfileDigest,
		ContainmentDigest: profile.ContainmentProfileDigest, SettlementDigest: profile.SettlementProfileDigest,
		ObservationDigest: profile.ObservationProfileDigest, SourceDigest: profile.SourceProvenanceDigest,
		OfflineVerifierDigest: profile.OfflineVerifierDigest, ProviderProfileDigest: profile.ProviderProfileDigest,
	}
	return profile, attestation
}

func testExecutablePath() string { return filepath.Join(os.TempDir(), "tbound-trusted-pi") }

type staticHostAttestor struct {
	attestation HostAttestation
	bindings    HostRuntimeBindings
}

func (a staticHostAttestor) Attest(context.Context, HostProfile) (HostAttestation, error) {
	return a.attestation, nil
}

func (a staticHostAttestor) BindRuntime(context.Context, HostProfile) (HostRuntimeBindings, error) {
	return a.bindings, nil
}

func testRuntimeBindings(profile HostProfile, launcher ExecutableLauncher) HostRuntimeBindings {
	arguments := []string{"--native-tbound-host"}
	return HostRuntimeBindings{
		ChildExecutable: profile.ChildExecutable, ChildArgumentsProfile: profile.ChildArgumentsProfile,
		WorkingDirectory: profile.WorkingDirectory, WorkingDirectoryHandle: testWorkingDirectoryHandle,
		StdinProfile: profile.StdinProfile,
		Arguments:    arguments, Stdin: bytes.NewReader(nil), ChildEnvironment: profile.ChildEnvironment,
		Environment: []string{},
		IPCProfile:  profile.IPCProfile, TerminalProfile: profile.TerminalProfile,
		ContainmentProfile: profile.ContainmentProfile, SettlementProfile: profile.SettlementProfile,
		ObservationProfile: profile.ObservationProfile, SourceProvenance: profile.SourceProvenance,
		OfflineVerifier: profile.OfflineVerifier, ProviderProfile: profile.ProviderProfile,
		Launcher: launcher, Descendant: &countingSettler{}, Terminal: io.Discard,
		Observation: testHostObservationSink{},
	}
}

var testWorkingDirectoryHandle = func() *os.File {
	file, err := os.Open(os.TempDir())
	if err != nil {
		panic(err)
	}
	return file
}()

type testHostObservationSink struct{}

func (testHostObservationSink) Observe(context.Context, Observation) error { return nil }

func testChildSpec(profile HostProfile, bindings HostRuntimeBindings) ChildSpec {
	return ChildSpec{
		Executable: profile.ChildExecutable, Args: append([]string(nil), bindings.Arguments...),
		Dir: bindings.WorkingDirectory, Env: append([]string{}, bindings.Environment...), Stdin: bindings.Stdin,
		Mode: IOModeNativeTUI, TUIOutput: bindings.Terminal, Descendant: bindings.Descendant,
	}
}

type countingLauncher struct {
	mu      sync.Mutex
	calls   int
	request ExecutableLaunchRequest
}

func (l *countingLauncher) StartVerified(_ context.Context, request ExecutableLaunchRequest) (*exec.Cmd, error) {
	l.mu.Lock()
	l.calls++
	l.request = request
	l.mu.Unlock()
	return nil, nil
}

type processLauncher struct {
	mu      sync.Mutex
	request ExecutableLaunchRequest
}

func (l *processLauncher) StartVerified(_ context.Context, request ExecutableLaunchRequest) (*exec.Cmd, error) {
	l.mu.Lock()
	l.request = request
	l.mu.Unlock()
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, "-test.run=^TestPiruntimeNoopChild$")
	cmd.Env = append([]string{}, request.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = request.Stdin, request.Stdout, request.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (l *processLauncher) Request() ExecutableLaunchRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.request
}

func (l *countingLauncher) Request() ExecutableLaunchRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.request
}

type channelObservationSink struct{ events chan Observation }

func (s channelObservationSink) Observe(_ context.Context, event Observation) error {
	s.events <- event
	return nil
}

func receiveObservation(t *testing.T, events <-chan Observation) Observation {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bounded observation")
		return Observation{}
	}
}

type blockingObservationSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingObservationSink) Observe(_ context.Context, _ Observation) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return nil
}

type cancelAwareObservationSink struct {
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

type failingObservationSink struct{ err error }

func (s failingObservationSink) Observe(context.Context, Observation) error { return s.err }

func (s *cancelAwareObservationSink) Observe(ctx context.Context, _ Observation) error {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	close(s.canceled)
	return ctx.Err()
}

type blockingSettler struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingSettler) Settle(context.Context) (bool, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return true, nil
}

type cancelAwareSettler struct {
	started  chan struct{}
	canceled chan struct{}
}

func (s *cancelAwareSettler) Settle(ctx context.Context) (bool, error) {
	close(s.started)
	<-ctx.Done()
	close(s.canceled)
	return false, ctx.Err()
}

type countingSettler struct {
	mu    sync.Mutex
	calls int
}

func (s *countingSettler) Settle(context.Context) (bool, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return true, nil
}

func (s *countingSettler) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("post-bind TUI output was used") }

func TestPiruntimeNoopChild(t *testing.T) {
	if os.Getenv("TBOUND_PIRUNTIME_BLOCK_CHILD") == "1" {
		select {}
	}
}

func TestPiruntimeObserveOutputChild(t *testing.T) {
	if os.Getenv("TBOUND_PIRUNTIME_OBSERVE_CHILD") == "1" {
		_, _ = os.Stdout.Write([]byte("observer-gap-test"))
	}
}

func completedProcess(settler DescendantSettler) *Process {
	process := &Process{completion: make(chan struct{}), settler: settler, settlement: Settlement{State: SettlementRunning}}
	close(process.completion)
	return process
}
