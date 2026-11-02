//go:build linux

package piruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/sessionrepo"
)

// LandlockRulesetDigest is the digest of the Landlock ruleset the delta cell applies for the
// given read-only and read-write paths of the signed template.
func (d D09CellDelta) LandlockRulesetDigest(readOnlyPaths, readWritePaths []string) (string, error) {
	var scoped uint64
	if d.ScopedAbstractUnix() {
		scoped = D09LandlockScopeAbstractUnix
	}
	return DigestD09LandlockRulesNet(readOnlyPaths, readWritePaths, d.ConnectPorts(), scoped)
}

// d09DeltaPlan is what the host derives, once per command, from the signed base template and
// the delta of the runner: the identities a delta cell must prove and the values the host passes
// to the entrypoint. The unrelaxed plan reproduces the base template exactly.
type d09DeltaPlan struct {
	delta          D09CellDelta
	templateDigest string // delta template digest; the base digest for the unrelaxed cell
	landlockDigest string // ruleset digest the entrypoint must apply and prove
	outputMarker   string // no-reap only: the end-of-output marker the cell init writes
}

func d09BasePlan(config D09Config) d09DeltaPlan {
	return d09DeltaPlan{templateDigest: config.TemplateDigest, landlockDigest: config.LandlockRulesetDigest}
}

func newD09DeltaPlan(delta D09CellDelta, config D09Config, outputMarker string) (d09DeltaPlan, error) {
	if !delta.Relaxed() {
		return d09BasePlan(config), nil
	}
	templateDigest, err := delta.TemplateDigest(config.TemplateDigest)
	if err != nil {
		return d09DeltaPlan{}, err
	}
	readOnly, readOnlyErr := ParseD09LandlockPaths(config.LandlockROPath)
	readWrite, readWriteErr := ParseD09LandlockPaths(config.LandlockRWPath)
	if err := errors.Join(readOnlyErr, readWriteErr); err != nil {
		return d09DeltaPlan{}, fmt.Errorf("delta Landlock paths: %w", err)
	}
	landlockDigest, err := delta.LandlockRulesetDigest(readOnly, readWrite)
	if err != nil {
		return d09DeltaPlan{}, err
	}
	if delta.NoReap() != (outputMarker != "") {
		return d09DeltaPlan{}, errors.New("only the no-reap cell takes an end-of-output marker, and it needs one")
	}
	return d09DeltaPlan{delta: delta, templateDigest: templateDigest, landlockDigest: landlockDigest, outputMarker: outputMarker}, nil
}

// newD09OutputToken returns a fresh, unguessable token of the end-of-output marker
// (D09OutputEndMarker). The command never sees it: the entrypoint starts the command with an
// explicit environment that omits it.
func newD09OutputToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// validateD09NamespaceProofDelta is validateD09NamespaceProof for a delta cell. Every namespace
// but the network one is private for every delta. The network namespace is private with no
// external route, except for net-sink/v1, which must report the host network namespace and which
// makes no route claim.
func validateD09NamespaceProofDelta(proof D09TCBProof, host D09NamespaceInodes, delta D09CellDelta) error {
	if !delta.NetSink() {
		if proof.NoExternalRoutes != delta.ExpectNoExternalRoutes() {
			return fmt.Errorf("%w: command proof route claim does not match the registered delta", ErrProductionLauncherUnavailable)
		}
		return validateD09NamespaceProof(proof, host)
	}
	if host.User == 0 || host.Mount == 0 || host.PID == 0 || host.IPC == 0 || host.UTS == 0 || host.Network == 0 ||
		proof.UserNSInode == 0 || proof.MountNSInode == 0 || proof.PIDNSInode == 0 || proof.IPCNSInode == 0 || proof.UTSNSInode == 0 ||
		proof.UserNSInode == host.User || proof.MountNSInode == host.Mount || proof.PIDNSInode == host.PID ||
		proof.IPCNSInode == host.IPC || proof.UTSNSInode == host.UTS ||
		proof.NetworkNSInode != host.Network || proof.NoExternalRoutes {
		return fmt.Errorf("%w: net-sink proof does not match the registered namespace profile (private user/mount/pid/ipc/uts, host network)", ErrProductionLauncherUnavailable)
	}
	return nil
}

// d09MarkedOutput retains at most limit bytes of one output stream of a no-reap command cell
// and finishes at the cell's end-of-output marker. The cell init writes the marker to the
// stream after the command exited, so the bytes before it are the complete output even though
// a surviving descendant may keep the stream open. Bytes after the marker are discarded.
type d09MarkedOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
	marker   []byte
	tail     []byte // last len(marker)-1 bytes of the stream
	total    int    // bytes seen before the marker
	found    bool
	done     chan struct{}
}

func newD09MarkedOutput(limit int, marker []byte) *d09MarkedOutput {
	return &d09MarkedOutput{limit: limit, marker: append([]byte(nil), marker...), done: make(chan struct{})}
}

// Done is closed when the marker was seen.
func (o *d09MarkedOutput) Done() <-chan struct{} { return o.done }

func (o *d09MarkedOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.found {
		return len(data), nil
	}
	window := append(append([]byte(nil), o.tail...), data...)
	if at := bytes.Index(window, o.marker); at >= 0 {
		// start is the stream offset of the marker; the command output is the bytes before it.
		start := o.total - len(o.tail) + at
		if start < o.total {
			// The marker began in bytes written earlier: take them back.
			if start < o.buffer.Len() {
				o.buffer.Truncate(start)
			}
			o.total = start
		} else {
			o.appendBounded(data[:start-o.total])
		}
		o.overflow = start > o.limit
		o.found = true
		close(o.done)
		return len(data), nil
	}
	o.appendBounded(data)
	if keep := len(o.marker) - 1; len(window) > keep {
		o.tail = append(o.tail[:0], window[len(window)-keep:]...)
	} else {
		o.tail = append(o.tail[:0], window...)
	}
	return len(data), nil
}

// appendBounded retains data up to the limit and counts every byte of the stream.
func (o *d09MarkedOutput) appendBounded(data []byte) {
	if remaining := o.limit - o.buffer.Len(); remaining >= len(data) {
		_, _ = o.buffer.Write(data)
	} else {
		if remaining > 0 {
			_, _ = o.buffer.Write(data[:remaining])
		}
		o.overflow = true
	}
	o.total += len(data)
}

func (o *d09MarkedOutput) Bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.buffer.Bytes()...)
}

func (o *d09MarkedOutput) Overflowed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.overflow
}

// d09ReceiptSettlementComplete is the settlement half of D09 command receipt validation. A
// receipt is either fully settled (the command cell exited, was removed, and its cgroup reached
// populated=0 and was removed) or, for no-reap/v1 only, a left-running receipt: the cell's own
// final proof and end-of-output marker were observed, the container was still running, and the
// command cgroup was detached from the lease without a kill. The settlement booleans of a
// left-running receipt are false because they are true: a descendant and the cell init survive.
func d09ReceiptSettlementComplete(r D09CommandReceipt) bool {
	delta, err := ParseD09CellDelta(r.Proof.Delta)
	if err != nil || (r.Proof.DeltaTemplateDigest != "") != delta.Relaxed() ||
		delta.Relaxed() && !validDigest(r.Proof.DeltaTemplateDigest) {
		return false
	}
	e, s := r.Evidence, r.Settlement
	if !s.SurvivorsTolerated {
		return !e.SurvivorsTolerated && !e.OutputMarkerObserved && !e.ContainerLeftRunning && !e.CommandScopeDetached &&
			s.ProcessScopeEmpty && s.WritersStopped && s.MountDetached &&
			e.ContainerExitInspected && e.FinalCommandProofRead && e.PodmanWaitCompleted &&
			e.OutputWritersDrained && e.ContainerRemoved && e.LeaderPidfdExited &&
			e.CgroupPopulatedZero && e.MountNamespaceReleased && e.CommandOutputWithinBound && e.CommandCgroupRemoved
	}
	return delta.NoReap() && s.EvidenceClass == D09CommandRunnerProfile+":"+D09NoReapSettlementEvidence &&
		!s.ProcessScopeEmpty && !s.WritersStopped && !s.MountDetached &&
		e.SurvivorsTolerated && e.FinalCommandProofRead && e.OutputMarkerObserved && e.ContainerLeftRunning &&
		e.CommandScopeDetached && e.CommandOutputWithinBound &&
		!e.ContainerExitInspected && !e.PodmanWaitCompleted && !e.OutputWritersDrained && !e.ContainerRemoved &&
		!e.LeaderPidfdExited && !e.CgroupPopulatedZero && !e.MountNamespaceReleased && !e.CommandCgroupRemoved
}

// d09CapturedOutput is one captured output stream of a command cell: bounded, with an overflow
// flag. The no-reap/v1 stream also finishes at the cell's end-of-output marker.
type d09CapturedOutput interface {
	io.Writer
	Bytes() []byte
	Overflowed() bool
}

// d09NoReapOutputGrace bounds how long the host waits, after the cell's final proof, for the
// end-of-output marker to travel from the cell through conmon and the Podman client.
const d09NoReapOutputGrace = 10 * time.Second

// d09NoReapSettle is what settleNoReap needs from the running command.
type d09NoReapSettle struct {
	scope        *d09OwnedCommandScope
	proofReader  *bufio.Reader
	startProof   d09CommandProof
	containerPID int
	podmanPath   string
	cid          string
	viewID       string
	stdout       *d09MarkedOutput
	stderr       *d09MarkedOutput
}

// settleNoReap observes a no-reap/v1 command cell that has run its command: the cell init's final
// proof (the exit observation), the end-of-output marker on both streams, the container still
// running with the cell init alive in the detached command cgroup. It kills nothing. The returned
// settlement is truthfully unsettled (survivors tolerated); the caller hands the scope over.
func (r *D09CommandRunner) settleNoReap(ctx context.Context, n d09NoReapSettle) (sessionrepo.CommandResult, sessionrepo.CommandSettlement,
	d09CommandProof, D09CommandSettlementEvidence, error) {
	fail := func(err error) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, d09CommandProof, D09CommandSettlementEvidence, error) {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, d09CommandProof{}, D09CommandSettlementEvidence{}, err
	}
	type proofRead struct {
		proof d09CommandProof
		err   error
	}
	read := make(chan proofRead, 1)
	go func() {
		proof, err := readD09CommandProofNext(n.proofReader)
		read <- proofRead{proof, err}
	}()
	var final d09CommandProof
	select {
	case got := <-read:
		if got.err != nil {
			return fail(fmt.Errorf("D09 no-reap command exit proof is missing: %w", got.err))
		}
		final = got.proof
	case <-ctx.Done():
		return fail(ctx.Err())
	}
	final.LeaderPID = n.containerPID
	if !final.CommandStarted || !final.CommandReady || !final.CommandExitObserved || !sameD09CommandProofIdentity(n.startProof, final) {
		return fail(errors.New("D09 no-reap command exit proof disagrees with the startup proof"))
	}
	grace := time.NewTimer(d09NoReapOutputGrace)
	defer grace.Stop()
	for _, done := range []<-chan struct{}{n.stdout.Done(), n.stderr.Done()} {
		select {
		case <-done:
		case <-grace.C:
			return fail(errors.New("D09 no-reap end-of-output marker was not observed"))
		case <-ctx.Done():
			return fail(ctx.Err())
		}
	}
	if n.stdout.Overflowed() || n.stderr.Overflowed() {
		return fail(errors.New("D09 command output exceeded the bounded result size"))
	}
	state, err := inspectContainer(ctx, n.podmanPath, n.cid, d09ProbeEnv())
	if err != nil || state.State.Status != "running" || state.State.PID != n.containerPID {
		return fail(errors.Join(errors.New("D09 no-reap command container is not left running"), err))
	}
	populated, err := n.scope.populated()
	if err != nil || !populated {
		return fail(errors.Join(errors.New("D09 no-reap command cgroup holds no cell init"), err))
	}
	result := sessionrepo.CommandResult{ExitCode: final.CommandExitCode, ExitObserved: true, Stdout: n.stdout.Bytes(), Stderr: n.stderr.Bytes()}
	settlement := sessionrepo.CommandSettlement{
		LeaseID: r.leaseID, ViewID: n.viewID, ExitObserved: true, ExitCode: final.CommandExitCode,
		SurvivorsTolerated: true, EvidenceClass: D09CommandRunnerProfile + ":" + D09NoReapSettlementEvidence,
	}
	evidence := D09CommandSettlementEvidence{
		SurvivorsTolerated: true, FinalCommandProofRead: true, OutputMarkerObserved: true,
		ContainerLeftRunning: true, CommandScopeDetached: true, CommandOutputWithinBound: true,
	}
	return result, settlement, final, evidence, nil
}

// DeltaLandlockRulesetDigest is the Landlock ruleset digest a command cell of the delta must prove,
// derived from the paths of the signed template. For the unrelaxed cell it is the template digest.
func (t D09CellTemplate) DeltaLandlockRulesetDigest(delta D09CellDelta) (string, error) {
	if !delta.Relaxed() {
		return t.LandlockRulesetDigest, nil
	}
	readOnly, readOnlyErr := ParseD09LandlockPaths(t.LandlockROPath)
	readWrite, readWriteErr := ParseD09LandlockPaths(t.LandlockRWPath)
	if err := errors.Join(readOnlyErr, readWriteErr); err != nil {
		return "", err
	}
	return delta.LandlockRulesetDigest(readOnly, readWrite)
}

// openNetSinkParent opens the fixed parent cgroup of the net-sink command scopes below the held
// cells cgroup. It never creates it: the operator creates it before the nftables rule is loaded
// (the rule resolves the path to a cgroup id at load time), and a directory the service made
// afterwards would not be the one the rule matches. The directory must be a cgroup2 domain owned
// by the service uid; its subtree controllers are enabled here so the command scopes below it can
// carry their limits.
func (l *D09PodmanCrunLauncher) openNetSinkParent(attested func(parent *os.File) error) (*os.File, error) {
	if l == nil || l.scope == nil || l.scope.root == nil {
		return nil, ErrCgroupScopeRefused
	}
	fd, err := syscall.Openat(int(l.scope.root.Fd()), D09NetSinkCgroupName,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: net-sink parent cgroup %q is absent below the cells cgroup (create it before loading the nftables rule): %v",
			ErrCgroupScopeRefused, D09NetSinkCgroupName, err)
	}
	dir := os.NewFile(uintptr(fd), "d09-net-sink-parent")
	// Before anything else is done with it: the reset must have loaded the pinned rule for this
	// very directory in this boot (requireNetSinkRule).
	if err := attested(dir); err != nil {
		_ = dir.Close()
		return nil, err
	}
	if err := validateCgroupDirectoryState(dir, false); err != nil {
		_ = dir.Close()
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	if err := enableD09SubtreeControllers(dir); err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("enable cpu/memory/pids controllers of the net-sink parent cgroup: %w", err)
	}
	return dir, nil
}

// netSinkLoadAttested is the production check of the net-sink parent: the installed rule is the
// pinned one and the root-owned attestation names this boot and this directory.
func netSinkLoadAttested(parent *os.File) error {
	return requireNetSinkRule(readRootOwnedProfileFile, ReadD09BootID, parent)
}
