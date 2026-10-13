//go:build linux

package piruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// This integration fixture uses staticHostAttestor and a simulated
// per-command proof launcher. It exercises admission-to-StartChild wiring,
// rejection order, and failed-launch cleanup; it does not exercise genuine
// signed host admission, D09 entrypoint proof capture, or containment.
func TestD09LaunchHelperRejectsInvalidPerLaunchProofBeforeBootstrap(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(*D09TCBProof)
		rejectInStart bool
	}{
		{"profile template", func(p *D09TCBProof) { p.TemplateDigest = DigestBytes([]byte("other template")) }, true},
		{"image digest", func(p *D09TCBProof) {
			p.ImageDigest = DigestBytes([]byte("other image"))
			p.ImageRef = "cell@" + p.ImageDigest
		}, true},
		{"launcher PID", func(p *D09TCBProof) { p.LauncherProcessPID++ }, true},
		{"Store audit head", func(p *D09TCBProof) { p.SourceStoreAuditHead = DigestBytes([]byte("other audit head")) }, false},
		{"runtime device", func(p *D09TCBProof) { p.RuntimeDevice++ }, false},
		{"runtime inode", func(p *D09TCBProof) { p.RuntimeInode++ }, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, capability, channels, launcher, settler, bootstrapProbe :=
				newSimulatedD09LaunchHelperFixture(t, test.mutate)
			bootstrap := PiWorkerBootstrap{
				SessionID: "session-1", WorkflowID: "workflow-1", ConversationID: "conversation-1",
			}
			if err := bindD09BootstrapSourceEvidence(&bootstrap, capability.runtime.D09SourceEvidence); err != nil {
				t.Fatalf("bind admitted source before launch helper: %v", err)
			}
			worker, err := launchPiCLIWorker(context.Background(), profile, capability.runtime,
				capability, channels.channels, bootstrap, false, "", "")
			if err == nil || worker != nil {
				t.Fatalf("invalid launch proof returned worker=%v err=%v", worker, err)
			}
			if start, take, validate := launcher.callCounts(); start != 1 || take != 1 || validate != 1 {
				t.Fatalf("launcher calls start=%d take=%d validate=%d, want each once", start, take, validate)
			}

			if test.rejectInStart {
				if !errors.Is(err, ErrProductionLauncherUnavailable) {
					t.Fatalf("StartChild proof rejection = %v, want ErrProductionLauncherUnavailable", err)
				}
				cleanup := launcher.cleanupSnapshot()
				if !cleanup.called || !cleanup.exitObserved || !cleanup.descendantsSettled {
					t.Fatalf("StartChild rejection did not complete failed-launch cleanup: %+v", cleanup)
				}
			} else {
				if launcher.cleanupSnapshot().called {
					t.Fatal("consumer-binding rejection used launcher proof-rejection cleanup")
				}
				var launchErr *PiWorkerLaunchError
				if !errors.As(err, &launchErr) || !errors.Is(err, ErrHostBindingsMissing) {
					t.Fatalf("consumer-proof rejection = %v, want settled PiWorkerLaunchError wrapping ErrHostBindingsMissing", err)
				}
				if launchErr.Settlement.State != SettlementStopped || !launchErr.Settlement.ExitObserved ||
					!launchErr.Settlement.DescendantsSettled || launchErr.CleanupErr != nil {
					t.Fatalf("failed-launch settlement did not meet publication gate: %+v cleanup=%v",
						launchErr.Settlement, launchErr.CleanupErr)
				}
			}
			if got := settler.Calls(); got != 1 {
				t.Fatalf("per-command settlement calls=%d, want 1", got)
			}

			// launchPiCLIWorker closes its owned channels on rejection. A
			// duplicated read endpoint lets the test distinguish an empty
			// bootstrap pipe from a written frame after that close.
			// Rewrap a fresh nonblocking duplicate after launch has finished:
			// File.Fd during descriptor construction can restore blocking mode.
			probeFD, err := syscall.Dup(int(bootstrapProbe.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.SetNonblock(probeFD, true); err != nil {
				_ = syscall.Close(probeFD)
				t.Fatal(err)
			}
			boundedProbe := os.NewFile(uintptr(probeFD), "bounded-bootstrap-probe")
			t.Cleanup(func() { _ = boundedProbe.Close() })
			if err := boundedProbe.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatalf("set bootstrap probe deadline: %v", err)
			}
			var data [1]byte
			n, readErr := boundedProbe.Read(data[:])
			if n != 0 || !errors.Is(readErr, io.EOF) {
				t.Fatalf("bootstrap was written or pipe did not close: bytes=%d err=%v", n, readErr)
			}
		})
	}
}

func TestD09StartChildCleanupFailsClosedWhenSettlementIsUnconfirmed(t *testing.T) {
	profile, capability, channels, launcher, settler, _ :=
		newSimulatedD09LaunchHelperFixture(t, func(proof *D09TCBProof) { proof.TemplateDigest = DigestBytes([]byte("unadmitted-template")) })
	settler.setOutcome(false, nil)
	bootstrap := PiWorkerBootstrap{
		SessionID: "session-1", WorkflowID: "workflow-1", ConversationID: "conversation-1",
	}
	if err := bindD09BootstrapSourceEvidence(&bootstrap, capability.runtime.D09SourceEvidence); err != nil {
		t.Fatalf("bind admitted source before launch helper: %v", err)
	}

	worker, err := launchPiCLIWorker(context.Background(), profile, capability.runtime,
		capability, channels.channels, bootstrap, false, "", "")
	if worker != nil || !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("unconfirmed cleanup returned worker=%v err=%v, want UNKNOWN settlement", worker, err)
	}
	cleanup := launcher.cleanupSnapshot()
	if !cleanup.called || !cleanup.exitObserved || cleanup.descendantsSettled {
		t.Fatalf("unconfirmed settlement cleanup=%+v, want observed exit and unsettled descendants", cleanup)
	}
	if got := settler.Calls(); got != 1 {
		t.Fatalf("per-command settlement calls=%d, want 1", got)
	}
	channels.channels.mu.Lock()
	closed := channels.channels.closed
	channels.channels.mu.Unlock()
	if closed {
		t.Fatal("UNKNOWN settlement prematurely closed the retained channel owner")
	}
}

type simulatedD09WaitOwner uint8

const (
	simulatedD09WaitUnclaimed simulatedD09WaitOwner = iota
	simulatedD09WaitCleanup
	simulatedD09WaitProcess
	simulatedD09WaitFallback
)

const simulatedD09WaitLimit = time.Second

type simulatedD09LaunchRecord struct {
	proof        D09TCBProof
	taken        bool
	waitOwner    simulatedD09WaitOwner
	waitStarted  bool
	waitDone     chan struct{}
	waitErr      error
	waitObserved bool
}

type simulatedD09Cleanup struct {
	called             bool
	exitObserved       bool
	descendantsSettled bool
}

// simulatedD09ProofLauncher starts only the existing blocking test child. Its
// per-command proof fields model launcher output for orchestration tests; they
// are not produced by the trusted D09 entrypoint or proof pipe.
type simulatedD09ProofLauncher struct {
	mu            sync.Mutex
	startCalls    int
	takeCalls     int
	validateCalls int
	records       map[*exec.Cmd]*simulatedD09LaunchRecord
	proofForPID   func(int) D09TCBProof
	expectedImage string
	settler       *simulatedD09Settler
	cleanup       simulatedD09Cleanup
}

func (*simulatedD09ProofLauncher) requirePerLaunchProof() {}

func (l *simulatedD09ProofLauncher) StartVerified(_ context.Context, request ExecutableLaunchRequest) (*exec.Cmd, error) {
	l.mu.Lock()
	l.startCalls++
	l.mu.Unlock()
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, "-test.run=^TestPiruntimeNoopChild$")
	cmd.Env = []string{"TBOUND_PIRUNTIME_BLOCK_CHILD=1"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = request.Stdin, request.Stdout, request.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.records == nil {
		l.records = make(map[*exec.Cmd]*simulatedD09LaunchRecord)
	}
	record := &simulatedD09LaunchRecord{waitDone: make(chan struct{})}
	l.records[cmd] = record
	l.mu.Unlock()
	record.proof = l.proofForPID(cmd.Process.Pid)
	return cmd, nil
}

func (l *simulatedD09ProofLauncher) TakeLaunchProof(cmd *exec.Cmd) (any, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.takeCalls++
	record, ok := l.records[cmd]
	if !ok || record.taken {
		return nil, false
	}
	record.taken = true
	return record.proof, true
}

func (l *simulatedD09ProofLauncher) DescendantSettlerFor(cmd *exec.Cmd) (DescendantSettler, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.records[cmd]
	return l.settler, ok && l.settler != nil
}

func (l *simulatedD09ProofLauncher) ValidateLaunchProof(value any, profile HostProfile, cmd *exec.Cmd) error {
	l.mu.Lock()
	l.validateCalls++
	proof, ok := value.(D09TCBProof)
	record := l.records[cmd]
	valid := ok && record != nil && cmd != nil && cmd.Process != nil && profile.Mode == ModeProduction &&
		proof.LauncherProcessPID == cmd.Process.Pid &&
		proof.TemplateDigest == profile.ContainmentProfileDigest &&
		proof.ImageDigest == l.expectedImage
	if !valid {
		l.mu.Unlock()
		return ErrProductionLauncherUnavailable
	}
	// StartChild constructs Process immediately after successful validation;
	// its waiter owns cmd.Wait from that point onward.
	record.waitOwner = simulatedD09WaitProcess
	l.mu.Unlock()
	return nil
}

func (l *simulatedD09ProofLauncher) cleanupFailedLaunch(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	l.mu.Lock()
	record := l.records[cmd]
	if record == nil || record.waitOwner != simulatedD09WaitUnclaimed {
		l.mu.Unlock()
		return false
	}
	record.waitOwner = simulatedD09WaitCleanup
	l.cleanup = simulatedD09Cleanup{called: true}
	l.mu.Unlock()

	killErr := cmd.Process.Kill()
	waitErr, waitObserved := l.waitOwnedCommand(cmd, record)
	exitObserved := waitObserved && (waitErr == nil || func() bool {
		var exitErr *exec.ExitError
		return errors.As(waitErr, &exitErr)
	}())
	settled, settleErr := false, errors.New("child exit was not observed")
	if exitObserved {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), simulatedD09WaitLimit)
		settled, settleErr = l.settler.Settle(cleanupCtx)
		cancel()
	}
	cleanup := simulatedD09Cleanup{
		called: true, exitObserved: exitObserved,
		descendantsSettled: settled && settleErr == nil,
	}
	l.mu.Lock()
	l.cleanup = cleanup
	l.mu.Unlock()
	return exitObserved && cleanup.descendantsSettled &&
		(killErr == nil || errors.Is(killErr, os.ErrProcessDone))
}

// waitOwnedCommand starts at most one waiter. The ownership marker is set
// before calling this method; cleanup fallback may only join the same result.
func (l *simulatedD09ProofLauncher) waitOwnedCommand(cmd *exec.Cmd, record *simulatedD09LaunchRecord) (error, bool) {
	l.mu.Lock()
	if !record.waitStarted {
		record.waitStarted = true
		go func() {
			err := cmd.Wait()
			l.mu.Lock()
			record.waitErr, record.waitObserved = err, true
			close(record.waitDone)
			l.mu.Unlock()
		}()
	}
	done := record.waitDone
	l.mu.Unlock()

	timer := time.NewTimer(simulatedD09WaitLimit)
	defer timer.Stop()
	select {
	case <-done:
		l.mu.Lock()
		err, observed := record.waitErr, record.waitObserved
		l.mu.Unlock()
		return err, observed
	case <-timer.C:
		return nil, false
	}
}

// cleanupStartedCommands is the test-only last-resort path. It kills all
// fixture-owned children but never reads ProcessState or calls a second Wait.
func (l *simulatedD09ProofLauncher) cleanupStartedCommands() {
	type ownedCommand struct {
		cmd    *exec.Cmd
		record *simulatedD09LaunchRecord
	}
	l.mu.Lock()
	commands := make([]ownedCommand, 0, len(l.records))
	for cmd, record := range l.records {
		if record.waitOwner == simulatedD09WaitUnclaimed {
			record.waitOwner = simulatedD09WaitFallback
		}
		commands = append(commands, ownedCommand{cmd: cmd, record: record})
	}
	l.mu.Unlock()

	for _, owned := range commands {
		if owned.cmd.Process != nil {
			_ = owned.cmd.Process.Kill()
		}
	}
	for _, owned := range commands {
		l.mu.Lock()
		owner := owned.record.waitOwner
		l.mu.Unlock()
		if owner == simulatedD09WaitCleanup || owner == simulatedD09WaitFallback {
			_, _ = l.waitOwnedCommand(owned.cmd, owned.record)
		}
	}
}

func (l *simulatedD09ProofLauncher) cleanupSnapshot() simulatedD09Cleanup {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cleanup
}

func (l *simulatedD09ProofLauncher) callCounts() (int, int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.startCalls, l.takeCalls, l.validateCalls
}

type simulatedD09Settler struct {
	mu      sync.Mutex
	calls   int
	settled bool
	err     error
}

func (s *simulatedD09Settler) Settle(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.settled, s.err
}

func (s *simulatedD09Settler) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *simulatedD09Settler) setOutcome(settled bool, err error) {
	s.mu.Lock()
	s.settled, s.err = settled, err
	s.mu.Unlock()
}

func newSimulatedD09LaunchHelperFixture(t *testing.T, mutate func(*D09TCBProof)) (
	HostProfile, *AdmissionCapability, *D09PiWorkerChannels, *simulatedD09ProofLauncher, *simulatedD09Settler, *os.File,
) {
	t.Helper()
	profile, attestation := testHostProfile()
	profile.ContainmentProfile = D09ContainmentProfile
	profile.ContainmentProfileDigest = DigestBytes([]byte(D09ContainmentProfile))
	profile.SettlementProfile = D09SettlementProfile
	profile.SettlementProfileDigest = DigestBytes([]byte(D09SettlementProfile))
	profile.Digest = DigestBytes([]byte("simulated-d09-startchild-proof-integration/v1"))
	attestation.ProfileDigest = profile.Digest
	attestation.ContainmentDigest = profile.ContainmentProfileDigest
	attestation.SettlementDigest = profile.SettlementProfileDigest

	d09Channels, runtime := d09ChannelBindingFixture(t)
	assetEvidence := WorkerRuntimeAssetEvidence{
		BundleDigest:           profile.PiRuntimeBundleDigest,
		ViewIdentity:           DigestBytes([]byte("simulated-runtime-view")),
		NamespaceProfileDigest: profile.ContainmentProfileDigest,
		Device:                 4, Inode: 5, MountID: 6,
	}
	d09Channels.assetEvidence = assetEvidence

	settler := &simulatedD09Settler{settled: true}
	launcher := &simulatedD09ProofLauncher{
		expectedImage: DigestBytes([]byte("simulated-image")),
		settler:       settler,
	}
	t.Cleanup(launcher.cleanupStartedCommands)
	bindings := testRuntimeBindings(profile, launcher)
	bindings.Descendant = settler
	bindings.WorkerExposureHandle = nil
	bindings.WorkerExposure = nil
	bindings.WorkerExposureEvidence = WorkerExposureEvidence{}
	bindings.WorkerRuntimeAssets = nil
	bindings.RuntimeAssetEvidence = WorkerRuntimeAssetEvidence{}
	bindings.D09Source = d09Channels.source
	bindings.D09SourceCapability = d09Channels.sourceCapability
	bindings.D09SourceEvidence = d09SourceEvidenceForBindings(d09Channels.sourceEvidence)
	bindings.D09SourceHandle = d09Channels.channels.workingDirectory
	bindings.D09RuntimeAssetSource = d09Channels.assets
	bindings.D09RuntimeAssetEvidence = assetEvidence
	bindings.PiRuntimeBundleHandle = d09Channels.channels.runtimeBundle
	bindings.Stdin = d09Channels.channels.ChildStdin()
	bindings.Descriptors = d09Channels.channels.DescriptorBindings()
	bindings.Launcher = launcher
	runtime = bindings

	source, assets := runtime.D09SourceEvidence, runtime.D09RuntimeAssetEvidence
	launcher.proofForPID = func(pid int) D09TCBProof {
		proof := D09TCBProof{
			Schema: "tbound-pi-entrypoint/v1", ContainerName: "simulated-cell",
			LeaderPID: pid + 1, ContainerLeaderPID: 1, LauncherProcessPID: pid,
			ImageDigest: launcher.expectedImage, ImageRef: "cell@" + launcher.expectedImage,
			TemplateDigest:         profile.ContainmentProfileDigest,
			ConsumerMountsVerified: true, RawSourceFDsClosed: true,
			WorkerMountID: 31, RuntimeMountID: 32,
			ConsumerWorkerMountID: 31, ConsumerRuntimeMountID: 32,
			WorkerDevice: source.SourceDevice, WorkerInode: source.SourceInode,
			RuntimeDevice: assets.Device, RuntimeInode: assets.Inode,
			SourceGenerationID: source.GenerationID, SourceTreeDigest: source.TreeDigest,
			SourceManifestDigest: source.ManifestDigest, SourceBindingDigest: source.SourceBindingDigest,
			SourceProvenanceDigest: source.ProvenanceDigest, SourceIdentityDigest: source.SourceIdentityDigest,
			SourceStoreAuditHead: source.StoreAuditHead, SourceStoreAuditCount: source.StoreAuditRecordCount,
			SourceDevice: source.SourceDevice, SourceInode: source.SourceInode, SourceMountID: source.SourceMountID,
		}
		mutate(&proof)
		return proof
	}

	capability, err := AdmitProduction(context.Background(), profile, staticHostAttestor{
		attestation: attestation, bindings: runtime,
	})
	if err != nil {
		t.Fatalf("issue capability through AdmitProduction test path: %v", err)
	}

	fd, err := syscall.Dup(int(d09Channels.channels.childBootstrap.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapProbe := os.NewFile(uintptr(fd), "bootstrap-probe")
	t.Cleanup(func() { _ = bootstrapProbe.Close() })

	return profile, capability, d09Channels, launcher, settler, bootstrapProbe
}
