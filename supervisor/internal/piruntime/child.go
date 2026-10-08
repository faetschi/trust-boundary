package piruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type IOMode string

const (
	IOModeNativeTUI   IOMode = "native-tui"
	IOModeObservation IOMode = "observation"

	MaxObservationBytes      = 1 << 20
	observationQueueSize     = 8
	maxObservationWorkers    = 64
	maxSettlementWorkers     = 32
	observationCleanupBudget = 250 * time.Millisecond
)

var settlementCleanupBudget = 5 * time.Second

var (
	ErrObservationCapacity = errors.New("native observation worker capacity is exhausted")
	ErrObservationCleanup  = errors.New("native observation cleanup did not settle")
	ErrObservationSink     = errors.New("native observation sink is not context-aware")
	ErrSettlementCapacity  = errors.New("native settlement worker capacity is exhausted")
)

// Observation is bounded, structured process output. It is an observation
// only; it never carries an ALLOW, trust, generation, or publication decision.
// DroppedBytes is an explicit gap marker when a slow observer fills the bounded
// queue; child lifecycle never waits for the observer.
type Observation struct {
	Stream       string `json:"stream"`
	Data         []byte `json:"data,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	DroppedBytes uint64 `json:"dropped_bytes,omitempty"`
}

type ObservationSink interface {
	Observe(context.Context, Observation) error
}

// ChildSpec deliberately has no inherit-environment switch. Env is an exact
// allow-list and a nil value is converted to a non-nil empty environment.
// Native TUI output and structured observation are mutually exclusive.
type ChildSpec struct {
	Executable     string
	Args           []string
	Dir            string
	Env            []string
	Mode           IOMode
	Stdin          io.Reader
	TUIOutput      io.Writer
	Observation    ObservationSink
	ObservationMax int
	Descendant     DescendantSettler

	capability *childCapability
}

// ExecutableLaunchRequest is handed only to the trusted host launcher. The
// launcher must verify the actual executable bytes against Digest and provide a
// platform-specific immutable/handle-bound launch; path hashing followed by a
// normal path exec is not sufficient proof against TOCTOU.
type ExecutableLaunchRequest struct {
	Path                   string
	Digest                 string
	Args                   []string
	Dir                    string
	WorkingDirectoryHandle *os.File
	Env                    []string
	Stdin                  io.Reader
	Stdout                 io.Writer
	Stderr                 io.Writer
	ProfileDigest          string
}

type ExecutableLauncher interface {
	StartVerified(context.Context, ExecutableLaunchRequest) (*exec.Cmd, error)
}

var (
	observationWorkerSlots = make(chan struct{}, maxObservationWorkers)
	settlementWorkerSlots  = make(chan struct{}, maxSettlementWorkers)
)

func tryAcquireSettlementWorker() bool {
	select {
	case settlementWorkerSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func tryAcquireObservationWorker() bool {
	select {
	case observationWorkerSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseObservationWorker() {
	select {
	case <-observationWorkerSlots:
	default:
		panic("native observation worker permit released without ownership")
	}
}

func releaseSettlementWorker() {
	select {
	case <-settlementWorkerSlots:
	default:
		panic("native settlement worker permit released without ownership")
	}
}

func (p *Process) releaseSettlementWorker() {
	if p == nil || !p.settlementSlotHeld {
		return
	}
	p.settlementSlotOnce.Do(releaseSettlementWorker)
}

func (s ChildSpec) validateShape() error {
	if strings.TrimSpace(s.Executable) == "" || strings.ContainsAny(s.Executable, "\x00\r\n") || !filepath.IsAbs(s.Executable) {
		return errors.New("native child executable must be an absolute path")
	}
	if s.Mode != IOModeNativeTUI && s.Mode != IOModeObservation {
		return errors.New("native child IO mode is required")
	}
	if s.Mode == IOModeNativeTUI && s.Observation != nil {
		return errors.New("native TUI and structured observation cannot share child output")
	}
	if s.Mode == IOModeNativeTUI && s.TUIOutput == nil {
		return errors.New("native TUI mode requires a TUI output stream")
	}
	if s.Mode == IOModeObservation && s.TUIOutput != nil {
		return errors.New("observation mode cannot attach native TUI output")
	}
	seen := make(map[string]struct{}, len(s.Env))
	for _, item := range s.Env {
		name, _, ok := strings.Cut(item, "=")
		if !ok || name == "" || strings.ContainsAny(name, "=\x00\r\n") {
			return errors.New("native child environment must contain exact NAME=value entries")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("native child environment repeats %q", name)
		}
		seen[name] = struct{}{}
	}
	if s.Mode == IOModeObservation && s.Observation == nil {
		return errors.New("observation mode requires a structured observation sink")
	}
	if s.Mode == IOModeObservation && (s.ObservationMax <= 0 || s.ObservationMax > MaxObservationBytes) {
		return fmt.Errorf("observation bound must be between 1 and %d bytes", MaxObservationBytes)
	}
	return nil
}

// validate retains the package-local name used by the original cmd/tbound
// tests while keeping capability checks in StartChild separate from shape
// validation.
func (s ChildSpec) validate() error { return s.validateShape() }

func (c *AdmissionCapability) BindChild(spec ChildSpec, launcher ExecutableLauncher) (ChildSpec, error) {
	if c == nil || !c.valid() {
		return ChildSpec{}, ErrChildCapabilityMissing
	}
	if launcher == nil || spec.Descendant == nil || c.runtime.Launcher == nil || c.runtime.Descendant == nil ||
		!sameBinding(launcher, c.runtime.Launcher) || !sameBinding(spec.Descendant, c.runtime.Descendant) {
		return ChildSpec{}, ErrChildCapabilityMissing
	}
	if err := spec.validateShape(); err != nil {
		return ChildSpec{}, err
	}
	if spec.Mode == IOModeNativeTUI && !sameBinding(spec.TUIOutput, c.runtime.Terminal) {
		return ChildSpec{}, ErrHostBindingsMissing
	}
	if spec.Mode == IOModeObservation && !sameBinding(spec.Observation, c.runtime.Observation) {
		return ChildSpec{}, ErrHostBindingsMissing
	}
	if spec.Executable != c.profile.ChildExecutable || !equalStrings(spec.Args, c.runtime.Arguments) ||
		!equalStrings(spec.Env, c.runtime.Environment) || spec.Dir != c.runtime.WorkingDirectory ||
		!sameBinding(spec.Stdin, c.runtime.Stdin) ||
		DigestEnvironment(spec.Env) != c.profile.ChildEnvironmentDigest {
		return ChildSpec{}, ErrHostProfileMismatch
	}
	snapshot := &boundChild{
		admission: c, launcher: c.runtime.Launcher, settler: c.runtime.Descendant,
		executable: spec.Executable, executableDigest: c.profile.ExecutableDigest,
		args: append([]string(nil), c.runtime.Arguments...), dir: c.runtime.WorkingDirectory,
		workingDirectoryHandle: c.runtime.WorkingDirectoryHandle,
		env:                    append([]string{}, c.runtime.Environment...), stdin: spec.Stdin, mode: spec.Mode,
		tuiOutput: spec.TUIOutput, observation: spec.Observation,
		observationMax: spec.ObservationMax, profileDigest: c.profile.Digest,
	}
	bound := spec
	bound.Args = append([]string(nil), c.runtime.Arguments...)
	bound.Env = append([]string{}, c.runtime.Environment...)
	bound.capability = &childCapability{bound: snapshot}
	return bound, nil
}

// DescendantSettler is the host-profile-specific proof that the complete child
// scope is empty. Direct os/exec.Wait is not enough: if this is absent, Stop
// returns UNKNOWN even after the direct child exits.
type DescendantSettler interface {
	Settle(context.Context) (bool, error)
}

// NoDescendants is allowed only for an explicitly synthetic fixture whose
// child contract forbids spawning. It is never installed by production
// admission.
type NoDescendants struct{}

func (NoDescendants) Settle(context.Context) (bool, error) { return true, nil }

type SettlementState string

const (
	SettlementRunning SettlementState = "RUNNING"
	SettlementStopped SettlementState = "STOPPED"
	SettlementUnknown SettlementState = "UNKNOWN"
)

type Settlement struct {
	State                   SettlementState `json:"state"`
	ExitObserved            bool            `json:"exit_observed"`
	ExitCode                int             `json:"exit_code"`
	DescendantsSettled      bool            `json:"descendants_settled"`
	ObservationGap          bool            `json:"observation_gap"`
	ObservationDroppedBytes uint64          `json:"observation_dropped_bytes,omitempty"`
	ObservationDetail       string          `json:"observation_detail,omitempty"`
	Detail                  string          `json:"detail,omitempty"`
}

// boundChild is the immutable authority-bearing snapshot produced by
// AdmissionCapability.BindChild. ChildSpec remains an editable builder/handle
// for source compatibility, but StartChild never consults its exported fields
// after this snapshot exists.
type boundChild struct {
	admission              *AdmissionCapability
	launcher               ExecutableLauncher
	settler                DescendantSettler
	executable             string
	executableDigest       string
	args                   []string
	dir                    string
	workingDirectoryHandle *os.File
	env                    []string
	stdin                  io.Reader
	mode                   IOMode
	tuiOutput              io.Writer
	observation            ObservationSink
	observationMax         int
	profileDigest          string
}

type childCapability struct{ bound *boundChild }

type waitResult struct {
	err                error
	observationErr     error
	observationDropped uint64
}

// Process owns the child process and its wait/settlement lifecycle. Completion
// is independent of every caller context; a timeout cannot poison later waits.
type Process struct {
	cmd     *exec.Cmd
	settler DescendantSettler
	writers []*observationWriter

	completion chan struct{}
	waitMu     sync.Mutex
	waitResult waitResult

	settleMu      sync.Mutex
	settleStarted bool
	settleDone    chan struct{}
	settleResult  Settlement
	settleErr     error

	stopMu      sync.Mutex
	stopStarted bool
	stopDone    chan struct{}
	stopErr     error

	stateMu    sync.Mutex
	settlement Settlement

	settlementSlotHeld bool
	settlementSlotOnce sync.Once
}

func StartChild(ctx context.Context, spec ChildSpec) (*Process, error) {
	if ctx == nil {
		return nil, errors.New("native child context is required")
	}
	if spec.capability == nil || spec.capability.bound == nil {
		return nil, ErrChildCapabilityMissing
	}
	bound := spec.capability.bound
	if bound.admission == nil || !bound.admission.valid() || isNilBinding(bound.launcher) || isNilBinding(bound.settler) {
		return nil, ErrHostProfileMismatch
	}
	runtime := bound.admission.runtime
	profile := bound.admission.profile
	if err := runtime.validate(profile); err != nil || bound.executable != profile.ChildExecutable ||
		bound.executableDigest != profile.ExecutableDigest || !equalStrings(bound.args, runtime.Arguments) ||
		bound.dir != runtime.WorkingDirectory || !sameBinding(bound.workingDirectoryHandle, runtime.WorkingDirectoryHandle) ||
		!sameBinding(bound.stdin, runtime.Stdin) ||
		!equalStrings(bound.env, runtime.Environment) || DigestEnvironment(bound.env) != profile.ChildEnvironmentDigest ||
		(bound.mode == IOModeNativeTUI && !sameBinding(bound.tuiOutput, runtime.Terminal)) ||
		(bound.mode == IOModeObservation && !sameBinding(bound.observation, runtime.Observation)) {
		return nil, ErrHostBindingsMissing
	}
	if !tryAcquireSettlementWorker() {
		return nil, ErrSettlementCapacity
	}
	stdout, stderr, writers, err := childOutputs(bound.mode, bound.tuiOutput, bound.observation, bound.observationMax)
	if err != nil {
		releaseSettlementWorker()
		return nil, err
	}
	cmd, err := bound.launcher.StartVerified(ctx, ExecutableLaunchRequest{
		Path: bound.executable, Digest: bound.executableDigest,
		Args: append([]string(nil), bound.args...), Dir: bound.dir, WorkingDirectoryHandle: bound.workingDirectoryHandle,
		Env:   append([]string{}, bound.env...),
		Stdin: bound.stdin, Stdout: stdout, Stderr: stderr, ProfileDigest: bound.profileDigest,
	})
	if err != nil {
		releaseSettlementWorker()
		closeObservationWriters(writers)
		return nil, fmt.Errorf("start admitted native child: %w", err)
	}
	if cmd == nil {
		releaseSettlementWorker()
		closeObservationWriters(writers)
		return nil, errors.New("trusted native child launcher returned no process")
	}
	return newProcess(cmd, bound.settler, writers, true), nil
}

// StartFixtureChild is the only path that permits an unbound absolute helper.
// It is explicitly synthetic and never creates a production capability.
func StartFixtureChild(ctx context.Context, spec ChildSpec) (*Process, error) {
	if ctx == nil {
		return nil, errors.New("native fixture child context is required")
	}
	if err := spec.validateShape(); err != nil {
		return nil, err
	}
	if spec.Descendant == nil {
		return nil, errors.New("native fixture child requires an explicit fixture descendant settler")
	}
	if !tryAcquireSettlementWorker() {
		return nil, ErrSettlementCapacity
	}
	stdout, stderr, writers, err := childOutputs(spec.Mode, spec.TUIOutput, spec.Observation, spec.ObservationMax)
	if err != nil {
		releaseSettlementWorker()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append([]string{}, spec.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = spec.Stdin, stdout, stderr
	if err := cmd.Start(); err != nil {
		releaseSettlementWorker()
		closeObservationWriters(writers)
		return nil, fmt.Errorf("start native fixture child: %w", err)
	}
	return newProcess(cmd, spec.Descendant, writers, true), nil
}

func childOutputs(mode IOMode, tuiOutput io.Writer, observation ObservationSink, observationMax int) (io.Writer, io.Writer, []*observationWriter, error) {
	if mode == IOModeNativeTUI {
		return tuiOutput, tuiOutput, nil, nil
	}
	stdout, err := newObservationWriter("stdout", observation, observationMax)
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err := newObservationWriter("stderr", observation, observationMax)
	if err != nil {
		closeObservationWriters([]*observationWriter{stdout})
		return nil, nil, nil, err
	}
	return stdout, stderr, []*observationWriter{stdout, stderr}, nil
}

func newProcess(cmd *exec.Cmd, settler DescendantSettler, writers []*observationWriter, settlementSlotHeld bool) *Process {
	p := &Process{cmd: cmd, settler: settler, writers: writers, completion: make(chan struct{}), settlement: Settlement{State: SettlementRunning}, settlementSlotHeld: settlementSlotHeld}
	go func() {
		err := cmd.Wait()
		observationContext, cancel := context.WithTimeout(context.Background(), observationCleanupBudget)
		observationErr := closeObservationWritersWithContext(observationContext, writers)
		cancel()
		observationDropped := observationWritersDropped(writers)
		p.waitMu.Lock()
		p.waitResult = waitResult{err: err, observationErr: observationErr, observationDropped: observationDropped}
		close(p.completion)
		p.waitMu.Unlock()
	}()
	return p
}

func (p *Process) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *Process) wait(ctx context.Context) (waitResult, bool) {
	if p == nil || ctx == nil || p.completion == nil {
		return waitResult{err: errors.New("native child is not configured")}, false
	}
	select {
	case <-p.completion:
		p.waitMu.Lock()
		result := p.waitResult
		p.waitMu.Unlock()
		return result, true
	case <-ctx.Done():
		return waitResult{err: ctx.Err()}, false
	}
}

func exitSettlement(waitResult waitResult) Settlement {
	settlement := Settlement{State: SettlementUnknown, ExitObserved: true}
	if waitResult.err != nil {
		var exitErr *exec.ExitError
		if errors.As(waitResult.err, &exitErr) {
			settlement.ExitCode = exitErr.ExitCode()
		} else {
			settlement.Detail = waitResult.err.Error()
		}
	}
	if waitResult.observationErr != nil || waitResult.observationDropped > 0 {
		settlement.ObservationGap = true
		settlement.ObservationDroppedBytes = waitResult.observationDropped
		if waitResult.observationErr != nil {
			settlement.ObservationDetail = waitResult.observationErr.Error()
		} else {
			settlement.ObservationDetail = fmt.Sprintf("observer dropped %d bytes", waitResult.observationDropped)
		}
	}
	return settlement
}

func observationWritersDropped(writers []*observationWriter) uint64 {
	var total uint64
	for _, writer := range writers {
		if writer != nil {
			total = saturatingAdd(total, writer.DroppedBytes())
		}
	}
	return total
}

func (p *Process) Settle(ctx context.Context) (Settlement, error) {
	if p == nil || ctx == nil {
		return Settlement{State: SettlementUnknown, Detail: "child is not configured"}, ErrUnknownSettlement
	}
	waitResult, observed := p.wait(ctx)
	if !observed {
		settlement := p.unknownWithDetail(waitResult.err.Error())
		return settlement, waitResult.err
	}
	base := exitSettlement(waitResult)
	p.recordSettlement(base)
	if p.settler == nil {
		base.Detail = "direct child exit observed but descendant settlement is not attested"
		p.recordSettlement(base)
		p.releaseSettlementWorker()
		return base, errors.Join(ErrUnknownSettlement, waitResult.observationErr)
	}
	p.settleMu.Lock()
	if !p.settleStarted {
		if !p.settlementSlotHeld {
			if !tryAcquireSettlementWorker() {
				p.settleMu.Unlock()
				base.State = SettlementUnknown
				base.Detail = ErrSettlementCapacity.Error()
				p.recordSettlement(base)
				return base, errors.Join(ErrSettlementCapacity, waitResult.observationErr)
			}
			p.settlementSlotHeld = true
		}
		p.settleStarted = true
		p.settleDone = make(chan struct{})
		done := p.settleDone
		settler := p.settler
		go func() {
			settleContext, cancel := context.WithTimeout(context.Background(), settlementCleanupBudget)
			settled, err := settleSafely(settler, settleContext)
			cancel()
			result := base
			if err != nil {
				result.Detail = err.Error()
			} else if !settled {
				result.Detail = "descendant settlement was not confirmed"
			} else {
				result.State = SettlementStopped
				result.DescendantsSettled = true
			}
			if err == nil && !settled {
				err = ErrUnknownSettlement
			}
			p.settleMu.Lock()
			p.settleResult, p.settleErr = result, errors.Join(err, waitResult.observationErr)
			close(done)
			p.settleMu.Unlock()
			p.recordSettlement(result)
			p.releaseSettlementWorker()
		}()
	}
	done := p.settleDone
	p.settleMu.Unlock()
	select {
	case <-done:
		p.settleMu.Lock()
		result, err := p.settleResult, p.settleErr
		p.settleMu.Unlock()
		return result, err
	case <-ctx.Done():
		return p.unknownWithDetail(ctx.Err().Error()), ctx.Err()
	}
}

func settleSafely(settler DescendantSettler, ctx context.Context) (settled bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("native descendant settler panicked: %v", recovered)
		}
	}()
	return settler.Settle(ctx)
}

func (p *Process) unknownWithDetail(detail string) Settlement {
	p.stateMu.Lock()
	settlement := p.settlement
	p.stateMu.Unlock()
	settlement.State = SettlementUnknown
	if detail != "" {
		settlement.Detail = detail
	}
	p.recordSettlement(settlement)
	return settlement
}

func (p *Process) recordSettlement(settlement Settlement) {
	p.stateMu.Lock()
	p.settlement = settlement
	p.stateMu.Unlock()
}

func (p *Process) LastSettlement() Settlement {
	if p == nil {
		return Settlement{State: SettlementUnknown, Detail: "child is not configured"}
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.settlement
}

// Stop requests direct-child termination, then requires the same full-scope
// settlement proof as natural exit. Kill success is never treated as STOPPED.
func (p *Process) Stop(ctx context.Context) (Settlement, error) {
	if p == nil || p.cmd == nil || p.cmd.Process == nil || ctx == nil {
		return Settlement{State: SettlementUnknown, Detail: "child is not configured"}, ErrUnknownSettlement
	}
	select {
	case <-p.completion:
		return p.Settle(ctx)
	default:
	}
	p.stopMu.Lock()
	if !p.stopStarted {
		p.stopStarted = true
		p.stopDone = make(chan struct{})
		done := p.stopDone
		go func() {
			err := p.cmd.Process.Kill()
			if errors.Is(err, os.ErrProcessDone) {
				err = nil
			}
			p.stopMu.Lock()
			p.stopErr = err
			close(done)
			p.stopMu.Unlock()
		}()
	}
	done := p.stopDone
	p.stopMu.Unlock()
	select {
	case <-done:
		p.stopMu.Lock()
		err := p.stopErr
		p.stopMu.Unlock()
		if err != nil {
			select {
			case <-p.completion:
				return p.Settle(ctx)
			default:
			}
			return p.unknownWithDetail(err.Error()), err
		}
		return p.Settle(ctx)
	case <-ctx.Done():
		return p.unknownWithDetail(ctx.Err().Error()), ctx.Err()
	}
}

// DigestEnvironment canonicalizes the exact allow-list used by exec.Cmd.
func DigestEnvironment(env []string) string {
	copyEnv := append([]string(nil), env...)
	sort.Strings(copyEnv)
	return DigestBytes([]byte(strings.Join(copyEnv, "\x00")))
}

type observationWriter struct {
	stream string
	sink   ObservationSink
	queue  chan Observation
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu           sync.Mutex
	left         int
	pendingDrop  uint64
	droppedTotal uint64
	workerErr    error
	closed       bool
}

func newObservationWriter(stream string, sink ObservationSink, max int) (*observationWriter, error) {
	if isNilBinding(sink) {
		return nil, ErrObservationSink
	}
	if !tryAcquireObservationWorker() {
		return nil, ErrObservationCapacity
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &observationWriter{stream: stream, sink: sink, left: max, queue: make(chan Observation, observationQueueSize), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer releaseObservationWorker()
		defer close(w.done)
		for event := range w.queue {
			select {
			case <-w.ctx.Done():
				return
			default:
			}
			if err := observeSafely(sink, w.ctx, event); err != nil {
				w.mu.Lock()
				w.workerErr = err
				w.pendingDrop = saturatingAdd(w.pendingDrop, uint64(len(event.Data)))
				w.droppedTotal = saturatingAdd(w.droppedTotal, uint64(len(event.Data)))
				for {
					select {
					case dropped, ok := <-w.queue:
						if !ok {
							w.mu.Unlock()
							return
						}
						w.pendingDrop = saturatingAdd(w.pendingDrop, uint64(len(dropped.Data)))
						w.droppedTotal = saturatingAdd(w.droppedTotal, uint64(len(dropped.Data)))
					default:
						w.mu.Unlock()
						return
					}
				}
			}
		}
	}()
	return w, nil
}

func observeSafely(sink ObservationSink, ctx context.Context, event Observation) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("native observation sink panicked: %v", recovered)
		}
	}()
	return sink.Observe(ctx, event)
}

func (w *observationWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(data) == 0 {
		return 0, nil
	}
	if w.closed {
		return len(data), nil
	}
	if w.workerErr != nil {
		w.pendingDrop = saturatingAdd(w.pendingDrop, uint64(len(data)))
		w.droppedTotal = saturatingAdd(w.droppedTotal, uint64(len(data)))
		return len(data), nil
	}
	n := len(data)
	if n > w.left {
		n = w.left
	}
	if n == 0 {
		w.pendingDrop = saturatingAdd(w.pendingDrop, uint64(len(data)))
		w.droppedTotal = saturatingAdd(w.droppedTotal, uint64(len(data)))
		return len(data), nil
	}
	truncatedBytes := uint64(len(data) - n)
	event := Observation{Stream: w.stream, Data: append([]byte(nil), data[:n]...), Truncated: n < len(data), DroppedBytes: truncatedBytes}
	w.droppedTotal = saturatingAdd(w.droppedTotal, truncatedBytes)
	pendingDrop := w.pendingDrop
	if pendingDrop > 0 {
		event.DroppedBytes = pendingDrop
		event.Truncated = true
	}
	select {
	case w.queue <- event:
		w.pendingDrop = 0
	default:
		w.pendingDrop = saturatingAdd(pendingDrop, uint64(len(data)))
		w.droppedTotal = saturatingAdd(w.droppedTotal, uint64(n))
	}
	// The bound covers source bytes accepted or dropped, not only bytes that
	// happened to fit in the observer queue.
	w.left -= n
	if n < len(data) {
		w.left = 0
	}
	return len(data), nil
}

func (w *observationWriter) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		return ErrObservationCleanup
	}
	w.mu.Lock()
	if w.closed {
		done := w.done
		w.mu.Unlock()
		select {
		case <-done:
			w.mu.Lock()
			err := w.cleanupErrorLocked(nil)
			w.mu.Unlock()
			return err
		case <-ctx.Done():
			w.cancel()
			w.mu.Lock()
			err := w.cleanupErrorLocked(ctx.Err())
			w.mu.Unlock()
			return err
		}
	}
	w.closed = true
	if w.pendingDrop > 0 && w.workerErr == nil {
		marker := Observation{Stream: w.stream, Truncated: true, DroppedBytes: w.pendingDrop}
		for attempts := 0; attempts <= observationQueueSize+1 && w.pendingDrop > 0; attempts++ {
			select {
			case w.queue <- marker:
				w.pendingDrop = 0
			default:
				// Make room without waiting for a blocked sink. The removed
				// observation is part of the same explicit gap.
				select {
				case dropped := <-w.queue:
					marker.DroppedBytes = saturatingAdd(marker.DroppedBytes, uint64(len(dropped.Data)))
					marker.DroppedBytes = saturatingAdd(marker.DroppedBytes, dropped.DroppedBytes)
					w.droppedTotal = saturatingAdd(w.droppedTotal, uint64(len(dropped.Data)))
				default:
					// Do not spin while holding the writer lock. If the bounded
					// queue remains full, the retained gap is reported by the
					// cleanup error/boundary rather than blocking child teardown.
				}
			}
		}
	}
	close(w.queue)
	w.mu.Unlock()
	select {
	case <-w.done:
		w.mu.Lock()
		err := w.cleanupErrorLocked(nil)
		w.mu.Unlock()
		return err
	case <-ctx.Done():
		w.cancel()
		w.mu.Lock()
		err := w.cleanupErrorLocked(ctx.Err())
		w.mu.Unlock()
		return err
	}
}

func (w *observationWriter) DroppedBytes() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.droppedTotal
}

func (w *observationWriter) cleanupErrorLocked(contextErr error) error {
	var errs []error
	if w.workerErr != nil {
		errs = append(errs, w.workerErr)
	}
	if contextErr != nil {
		errs = append(errs, contextErr)
	}
	if w.droppedTotal > 0 || w.pendingDrop > 0 {
		errs = append(errs, fmt.Errorf("observation lost %d bytes", w.droppedTotal))
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(append([]error{ErrObservationCleanup}, errs...)...)
}

func saturatingAdd(left, right uint64) uint64 {
	if ^uint64(0)-left < right {
		return ^uint64(0)
	}
	return left + right
}

func closeObservationWriters(writers []*observationWriter) {
	ctx, cancel := context.WithTimeout(context.Background(), observationCleanupBudget)
	defer cancel()
	_ = closeObservationWritersWithContext(ctx, writers)
}

func closeObservationWritersWithContext(ctx context.Context, writers []*observationWriter) error {
	var firstErr error
	for _, writer := range writers {
		if writer != nil {
			if err := writer.Close(ctx); err != nil {
				firstErr = errors.Join(firstErr, err)
			}
		}
	}
	return firstErr
}
