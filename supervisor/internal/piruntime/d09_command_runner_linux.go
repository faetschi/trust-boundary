//go:build linux

package piruntime

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/sessionrepo"
)

// D09CommandRunnerProfile is the production command-cell evidence label. It is
// emitted only after the real container's TCB proof, command exit, leader pidfd,
// per-command cgroup settlement, removal, and mount-reference scan all succeed.
const D09CommandRunnerProfile = "d09-rootless-podman-crun-frozen-cell/v1"

const (
	d09CommandOutputLimit  = 1 << 20
	d09CommandProofLimit   = 4096
	d09CommandStartTimeout = 15 * time.Second
	d09CommandCleanupLimit = 20 * time.Second
	d09CommandMaxRuntime   = 10 * time.Minute
)

// D09CommandRunner executes one authorized command in a fresh D09 Podman/crun
// cell. The command view is a typed sessionrepo.CommandView; only its held
// descriptor is used as the /work bind source, never a caller path. No executor
// injection is exposed on the production constructor.
type D09CommandRunner struct {
	leaseID  string
	launcher *D09PodmanCrunLauncher
	profile  HostProfile
	// delta is the registered command-cell delta of this lease; the zero value is the signed base
	// cell. It is fixed at construction by the service from the admitted role and the pinned manifest.
	delta D09CellDelta

	mu         sync.Mutex
	usedScopes map[string]struct{}
	usedViews  map[string]struct{}
	receipts   map[string]D09CommandReceipt
}

// NewD09CommandRunner constructs the production Bash runner for one
// authenticated command lease. The trusted CLI composition supplies the lease
// returned by its durable authority and a factory backed by delegated cgroup
// descriptors. It cannot substitute a command executor or settlement callback.
func NewD09CommandRunner(leaseID string, launcher *D09PodmanCrunLauncher) (*D09CommandRunner, error) {
	return NewD09CommandRunnerForDelta(leaseID, launcher, D09CellDelta{})
}

// NewD09CommandRunnerForDelta is NewD09CommandRunner for a command cell under a registered
// delta. The delta is a typed value that only ParseD09CellDelta can make relaxed.
func NewD09CommandRunnerForDelta(leaseID string, launcher *D09PodmanCrunLauncher, delta D09CellDelta) (*D09CommandRunner, error) {
	profile, admitted := launcher.admittedProfileSnapshot()
	if !validIdentity(leaseID) || launcher == nil || launcher.scope == nil || !admitted {
		return nil, ErrProductionLauncherUnavailable
	}
	return &D09CommandRunner{leaseID: leaseID, launcher: launcher, profile: profile, delta: delta,
		usedScopes: make(map[string]struct{}), usedViews: make(map[string]struct{}), receipts: make(map[string]D09CommandReceipt)}, nil
}

// Delta is the registered command-cell delta this runner launches its cells under.
func (r *D09CommandRunner) Delta() D09CellDelta { return r.delta }

func (*D09CommandRunner) Profile() string { return D09CommandRunnerProfile }

// D09CommandProof is the strict command-mode TCB proof captured from the
// entrypoint's dedicated pipe. It binds the authorized command, live view,
// container/image/template identity, and pre-/post-command measurements.
type D09CommandProof struct {
	Schema                 string `json:"schema"`
	UID                    int    `json:"uid"`
	GID                    int    `json:"gid"`
	CapEff                 string `json:"cap_eff"`
	CapPrm                 string `json:"cap_prm,omitempty"`
	CapBnd                 string `json:"cap_bnd,omitempty"`
	CapInh                 string `json:"cap_inh,omitempty"`
	CapAmb                 string `json:"cap_amb,omitempty"`
	NoNewPrivs             bool   `json:"no_new_privs"`
	Seccomp                int    `json:"seccomp"`
	LandlockABI            int    `json:"landlock_abi"`
	LandlockRulesetDigest  string `json:"landlock_ruleset_digest"`
	LandlockTSYNC          bool   `json:"landlock_tsync"`
	CgroupV2               bool   `json:"cgroup_v2"`
	UIDMap                 string `json:"uid_map"`
	GIDMap                 string `json:"gid_map"`
	UserNamespaceMapDigest string `json:"user_namespace_map_digest"`
	LeaderPID              int    `json:"leader_pid"`
	ContainerLeaderPID     int    `json:"container_leader_pid"`
	LeaseID                string `json:"lease_id"`
	ViewID                 string `json:"view_id"`
	CommandStarted         bool   `json:"command_started"`
	CommandReady           bool   `json:"command_ready"`
	CommandExitObserved    bool   `json:"command_exit_observed"`
	CommandExitCode        int    `json:"command_exit_code"`
	CommandPath            string `json:"command_path"`
	CommandDigest          string `json:"command_digest"`
	CommandWorkdir         string `json:"command_workdir"`
	ContainerName          string `json:"container_name"`
	ImageRef               string `json:"image_ref"`
	ImageDigest            string `json:"image_digest"`
	TemplateDigest         string `json:"template_digest"`
	ConsumerMountsVerified bool   `json:"consumer_mounts_verified"`
	RawSourceFDsClosed     bool   `json:"raw_source_fds_closed"`
	WorkerMountID          uint64 `json:"worker_mount_id"`
	RuntimeMountID         uint64 `json:"runtime_mount_id"`
	WorkerDevice           uint64 `json:"worker_device"`
	WorkerInode            uint64 `json:"worker_inode"`
	RuntimeDevice          uint64 `json:"runtime_device"`
	RuntimeInode           uint64 `json:"runtime_inode"`
	UserNSInode            uint64 `json:"user_ns_inode"`
	MountNSInode           uint64 `json:"mount_ns_inode"`
	PIDNSInode             uint64 `json:"pid_ns_inode"`
	IPCNSInode             uint64 `json:"ipc_ns_inode"`
	UTSNSInode             uint64 `json:"uts_ns_inode"`
	NetworkNSInode         uint64 `json:"network_ns_inode"`
	NoExternalRoutes       bool   `json:"no_external_routes"`
	// Delta and DeltaTemplateDigest name the registered command-cell delta the cell was launched
	// under and its derived template identity. Both are empty for the unrelaxed cell.
	Delta               string `json:"delta,omitempty"`
	DeltaTemplateDigest string `json:"delta_template_digest,omitempty"`
}

type d09CommandProof = D09CommandProof

// D09CommandSettlementEvidence describes the concrete exit/cleanup observations
// required before the runtime issues a result receipt. It is generated only on
// the successful path after each observation has been made.
type D09CommandSettlementEvidence struct {
	ContainerExitInspected   bool `json:"container_exit_inspected"`
	FinalCommandProofRead    bool `json:"final_command_proof_read"`
	PodmanWaitCompleted      bool `json:"podman_wait_completed"`
	OutputWritersDrained     bool `json:"output_writers_drained"`
	ContainerRemoved         bool `json:"container_removed"`
	LeaderPidfdExited        bool `json:"leader_pidfd_exited"`
	CgroupPopulatedZero      bool `json:"cgroup_populated_zero"`
	MountNamespaceReleased   bool `json:"mount_namespace_released"`
	CommandOutputWithinBound bool `json:"command_output_within_bound"`
	CommandCgroupRemoved     bool `json:"command_cgroup_removed"`
	// The no-reap/v1 command cell is left running, so the exit, wait, removal and detach
	// observations above do not exist for it. Instead the host observed the cell's own final proof,
	// the end-of-output marker of both streams, the container still running, and detached the
	// command cgroup from the lease without killing it. Both sets never hold in one receipt.
	SurvivorsTolerated   bool `json:"survivors_tolerated,omitempty"`
	OutputMarkerObserved bool `json:"output_marker_observed,omitempty"`
	ContainerLeftRunning bool `json:"container_left_running,omitempty"`
	CommandScopeDetached bool `json:"command_scope_detached,omitempty"`
}

// D09CommandReceipt is an opaque, one-use receipt for a single authorized
// command invocation. Exported fields are the durable snapshot; private nonce
// and seal are retained only in memory and prevent caller-constructed or
// post-return-mutated values from validating. The surrounding audit journal
// supplies persistence and binds this receipt to the operation/tree/run.
type D09CommandReceipt struct {
	SchemaVersion     string                        `json:"schema_version"`
	LeaseID           string                        `json:"lease_id"`
	ViewID            string                        `json:"view_id"`
	CellName          string                        `json:"cell_name"`
	ContainerID       string                        `json:"container_id"`
	ImageRef          string                        `json:"image_ref"`
	ImageDigest       string                        `json:"image_digest"`
	TemplateDigest    string                        `json:"template_digest"`
	CommandSpecDigest string                        `json:"command_spec_digest"`
	ResultDigest      string                        `json:"result_digest"`
	SettlementDigest  string                        `json:"settlement_digest"`
	ProofDigest       string                        `json:"proof_digest"`
	ReceiptDigest     string                        `json:"receipt_digest"`
	ExitCode          int                           `json:"exit_code"`
	CommandSpec       sessionrepo.CommandSpec       `json:"command_spec"`
	Proof             D09CommandProof               `json:"proof"`
	Settlement        sessionrepo.CommandSettlement `json:"settlement"`
	Evidence          D09CommandSettlementEvidence  `json:"evidence"`
	receiptNonce      [32]byte
	receiptBody       string
	receiptSeal       [32]byte
}

var ErrD09CommandReceiptInvalid = errors.New("D09 command receipt is missing, altered, stale, or not bound to this invocation")

// Validate checks the issuance seal, canonical receipt digest, captured proof,
// and every required observed settlement postcondition.
func (r D09CommandReceipt) Validate() error {
	return validateD09CommandReceipt(r, true)
}

// ValidateSnapshot validates the exported, journal-decoded receipt payload and
// its digest. It is structural only: production authorization must additionally
// compare the snapshot with the opaque receipt returned by TakeCommandReceipt.
func (r D09CommandReceipt) ValidateSnapshot() error {
	return validateD09CommandReceipt(r, false)
}

func validateD09CommandReceipt(r D09CommandReceipt, requireSeal bool) error {
	body, err := d09CommandReceiptBody(r)
	if err != nil {
		return errors.Join(ErrD09CommandReceiptInvalid, err)
	}
	proofBytes, err := json.Marshal(r.Proof)
	if err != nil {
		return errors.Join(ErrD09CommandReceiptInvalid, err)
	}
	settlementBytes, err := json.Marshal(r.Settlement)
	if err != nil {
		return errors.Join(ErrD09CommandReceiptInvalid, err)
	}
	commandBytes, err := json.Marshal(r.CommandSpec)
	if err != nil {
		return errors.Join(ErrD09CommandReceiptInvalid, err)
	}
	if r.SchemaVersion != "tbound-d09-command-receipt/v1" || !validIdentity(r.LeaseID) || !validIdentity(r.ViewID) ||
		!validIdentity(r.CellName) || !validD09ContainerID(r.ContainerID) || !validDigest(r.ImageDigest) || !validDigest(r.TemplateDigest) ||
		!validDigest(r.CommandSpecDigest) || !validDigest(r.ResultDigest) || !validDigest(r.SettlementDigest) || !validDigest(r.ProofDigest) ||
		r.ProofDigest != DigestBytes(proofBytes) || r.SettlementDigest != DigestBytes(settlementBytes) ||
		!validDigest(r.ReceiptDigest) || DigestBytes(body) != r.ReceiptDigest || DigestBytes(commandBytes) != r.CommandSpecDigest ||
		(r.receiptBody != "" && r.receiptBody != r.ReceiptDigest) ||
		r.Proof.LeaseID != r.LeaseID || r.Proof.ViewID != r.ViewID || r.Proof.ContainerName != r.CellName ||
		r.Proof.ImageRef != r.ImageRef || !strings.HasSuffix(r.ImageRef, "@"+r.ImageDigest) ||
		r.Proof.ImageDigest != r.ImageDigest || r.Proof.TemplateDigest != r.TemplateDigest ||
		r.Proof.CommandStarted != true || !r.Proof.CommandReady || !r.Proof.CommandExitObserved || r.Proof.CommandExitCode != r.ExitCode ||
		r.Proof.LeaderPID <= 0 || r.Proof.ContainerLeaderPID != 1 || !validDigest(r.Proof.CommandDigest) ||
		r.Settlement.LeaseID != r.LeaseID || r.Settlement.ViewID != r.ViewID || !r.Settlement.ExitObserved ||
		r.Settlement.ExitCode != r.ExitCode ||
		!strings.HasPrefix(r.Settlement.EvidenceClass, D09CommandRunnerProfile+":") ||
		!d09ReceiptSettlementComplete(r) {
		return ErrD09CommandReceiptInvalid
	}
	if requireSeal {
		sealInput := append(append([]byte("tbound-d09-command-receipt-seal/v1\x00"), r.receiptNonce[:]...), []byte(r.ReceiptDigest)...)
		if r.receiptNonce == ([32]byte{}) || sha256.Sum256(sealInput) != r.receiptSeal {
			return ErrD09CommandReceiptInvalid
		}
	}
	return nil
}

func validD09ContainerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// Digest returns the canonical digest of the validated receipt snapshot.
func (r D09CommandReceipt) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	return r.ReceiptDigest, nil
}

func d09CommandReceiptBody(receipt D09CommandReceipt) ([]byte, error) {
	type wire struct {
		SchemaVersion     string                        `json:"schema_version"`
		LeaseID           string                        `json:"lease_id"`
		ViewID            string                        `json:"view_id"`
		CellName          string                        `json:"cell_name"`
		ContainerID       string                        `json:"container_id"`
		ImageRef          string                        `json:"image_ref"`
		ImageDigest       string                        `json:"image_digest"`
		TemplateDigest    string                        `json:"template_digest"`
		CommandSpecDigest string                        `json:"command_spec_digest"`
		ResultDigest      string                        `json:"result_digest"`
		SettlementDigest  string                        `json:"settlement_digest"`
		ProofDigest       string                        `json:"proof_digest"`
		ExitCode          int                           `json:"exit_code"`
		CommandSpec       sessionrepo.CommandSpec       `json:"command_spec"`
		Proof             D09CommandProof               `json:"proof"`
		Settlement        sessionrepo.CommandSettlement `json:"settlement"`
		Evidence          D09CommandSettlementEvidence  `json:"evidence"`
	}
	return json.Marshal(wire{receipt.SchemaVersion, receipt.LeaseID, receipt.ViewID, receipt.CellName, receipt.ContainerID,
		receipt.ImageRef, receipt.ImageDigest, receipt.TemplateDigest, receipt.CommandSpecDigest, receipt.ResultDigest,
		receipt.SettlementDigest, receipt.ProofDigest, receipt.ExitCode,
		receipt.CommandSpec, receipt.Proof, receipt.Settlement, receipt.Evidence})
}

func newD09CommandReceipt(leaseID, viewID, cellName, containerID string, spec sessionrepo.CommandSpec,
	result sessionrepo.CommandResult, settlement sessionrepo.CommandSettlement, proof D09CommandProof,
	evidence D09CommandSettlementEvidence) (D09CommandReceipt, error) {
	commandBytes, err := json.Marshal(spec)
	if err != nil {
		return D09CommandReceipt{}, err
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return D09CommandReceipt{}, err
	}
	settlementBytes, err := json.Marshal(settlement)
	if err != nil {
		return D09CommandReceipt{}, err
	}
	proofBytes, err := json.Marshal(proof)
	if err != nil {
		return D09CommandReceipt{}, err
	}
	receipt := D09CommandReceipt{
		SchemaVersion: "tbound-d09-command-receipt/v1", LeaseID: leaseID, ViewID: viewID,
		CellName: cellName, ContainerID: containerID, ImageRef: proof.ImageRef,
		ImageDigest: proof.ImageDigest, TemplateDigest: proof.TemplateDigest,
		CommandSpecDigest: DigestBytes(commandBytes), ResultDigest: DigestBytes(resultBytes),
		SettlementDigest: DigestBytes(settlementBytes), ProofDigest: DigestBytes(proofBytes),
		ExitCode: result.ExitCode, CommandSpec: sessionrepo.CommandSpec{Executable: spec.Executable,
			Args: append([]string(nil), spec.Args...), WorkingDirectory: spec.WorkingDirectory},
		Proof: proof, Settlement: settlement, Evidence: evidence,
	}
	if _, err := rand.Read(receipt.receiptNonce[:]); err != nil {
		return D09CommandReceipt{}, err
	}
	body, err := d09CommandReceiptBody(receipt)
	if err != nil {
		return D09CommandReceipt{}, err
	}
	receipt.receiptBody = DigestBytes(body)
	receipt.ReceiptDigest = receipt.receiptBody
	sealInput := append(append([]byte("tbound-d09-command-receipt-seal/v1\x00"), receipt.receiptNonce[:]...), []byte(receipt.ReceiptDigest)...)
	receipt.receiptSeal = sha256.Sum256(sealInput)
	if err := receipt.Validate(); err != nil {
		return D09CommandReceipt{}, err
	}
	return receipt, nil
}

// TakeCommandReceipt consumes the captured proof/result receipt for the exact
// view, command spec, result, and settlement returned by the immediately
// preceding successful Run. It is one-use; a mismatched or stale attempt also
// consumes the entry and refuses rather than permitting substitution.
func (r *D09CommandRunner) TakeCommandReceipt(view *sessionrepo.CommandView, spec sessionrepo.CommandSpec,
	result sessionrepo.CommandResult, settlement sessionrepo.CommandSettlement) (D09CommandReceipt, error) {
	if r == nil || view == nil {
		return D09CommandReceipt{}, ErrD09CommandReceiptInvalid
	}
	return r.takeCommandReceipt(view.ID(), spec, result, settlement)
}

func (r *D09CommandRunner) takeCommandReceipt(viewID string, spec sessionrepo.CommandSpec,
	result sessionrepo.CommandResult, settlement sessionrepo.CommandSettlement) (D09CommandReceipt, error) {
	if r == nil || !validIdentity(viewID) {
		return D09CommandReceipt{}, ErrD09CommandReceiptInvalid
	}
	r.mu.Lock()
	receipt, exists := r.receipts[viewID]
	delete(r.receipts, viewID)
	r.mu.Unlock()
	if !exists {
		return D09CommandReceipt{}, ErrD09CommandReceiptInvalid
	}
	if r.launcher != nil {
		r.launcher.releaseCommandReceipt()
	}
	commandBytes, commandErr := json.Marshal(spec)
	resultBytes, resultErr := json.Marshal(result)
	settlementBytes, settlementErr := json.Marshal(settlement)
	if commandErr != nil || resultErr != nil || settlementErr != nil || receipt.LeaseID != r.leaseID || receipt.ViewID != viewID ||
		receipt.CommandSpecDigest != DigestBytes(commandBytes) || receipt.ResultDigest != DigestBytes(resultBytes) ||
		receipt.SettlementDigest != DigestBytes(settlementBytes) || receipt.Settlement != settlement || receipt.ExitCode != result.ExitCode ||
		!result.ExitObserved || !settlement.ExitObserved {
		return D09CommandReceipt{}, ErrD09CommandReceiptInvalid
	}
	if err := receipt.Validate(); err != nil {
		return D09CommandReceipt{}, err
	}
	return receipt, nil
}

// d09BoundedOutput retains at most the sessionrepo command-output bound while
// continuing to drain the pipe. Overflow is recorded and causes a refusal after
// cleanup; it cannot block a child writer indefinitely.
type d09BoundedOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *d09BoundedOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) <= remaining {
			_, _ = b.buffer.Write(data)
		} else {
			_, _ = b.buffer.Write(data[:remaining])
			b.overflow = true
		}
	} else if len(data) != 0 {
		b.overflow = true
	}
	return len(data), nil
}

func (b *d09BoundedOutput) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func (b *d09BoundedOutput) Overflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow
}

// Run starts a real, unique D09 command cell. It returns only after: the
// command-mode entrypoint TCB proof was captured and validated against the
// authenticated lease/view, the entrypoint leader was verified in this exact
// cgroup and opened with pidfd, container inspect reported the command exit,
// the entrypoint's final proof agreed, the container was removed, the pidfd
// became readable, cgroup.kill reached populated=0, and no surviving process
// mount table references the exact command-view source. Any unknown state
// returns no result or settlement.
func (r *D09CommandRunner) Run(ctx context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (_ sessionrepo.CommandResult, _ sessionrepo.CommandSettlement, err error) {
	if r == nil || ctx == nil || r.launcher == nil || !validIdentity(r.leaseID) {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrProductionLauncherUnavailable
	}
	if err := r.launcher.beginOperation(); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	defer r.launcher.endOperation()
	if view == nil || !r.launcher.isProfileAdmitted(r.profile) {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrProductionLauncherUnavailable
	}
	spec.Args = append([]string(nil), spec.Args...)
	if err := validateD09CommandSpec(spec, r.launcher.config); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if err := d09CommandSpecBounded(spec); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	viewID := view.ID()
	if !validIdentity(viewID) {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: command view identity is absent", ErrHostBindingsMissing)
	}
	if err := r.reserveCommandView(viewID); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	// This typed API can return only a registered, live sessionrepo view source;
	// no arbitrary caller path reaches podman.
	viewSource, err := view.MountSource()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("open authorized command view: %w", err)
	}
	defer viewSource.Close()
	viewPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", viewSource.Fd()))
	if err != nil || !filepath.IsAbs(viewPath) || strings.HasSuffix(viewPath, " (deleted)") {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: authorized view descriptor has no stable path", ErrHostBindingsMissing)
	}
	viewInfo, err := viewSource.Stat()
	if err != nil || !viewInfo.IsDir() {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: command view descriptor is not a directory", ErrHostBindingsMissing)
	}
	viewStat, ok := viewInfo.Sys().(*syscall.Stat_t)
	if !ok || viewStat.Ino == 0 {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrHostBindingsMissing
	}
	viewMountPath := viewPath // canonical path, not /proc/<pid>/fd: see descriptorPathForMount; the entrypoint checks TBOUND_COMMAND_VIEW_DEVICE/INODE
	readOnlyPaths, err := view.ReadOnlyMountPaths()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: %v", ErrHostBindingsMissing, err)
	}

	report, err := r.launcher.Probe(ctx)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	scope, err := r.allocateCommandScope(ctx, viewID)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if scope == nil || scope.scope == nil || scope.leaseID != r.leaseID || scope.viewID != viewID {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrCgroupScopeRefused
	}
	containerStarted := false
	ackSent := false
	abandoned := false // a no-reap/v1 scope handed over to the operator reset: never killed here
	scopeClosed := false
	name := ""
	scopePath := ""
	closeScope := func() error {
		if scopeClosed {
			return nil
		}
		if err := scope.Remove(); err != nil {
			return err
		}
		scopeClosed = true
		r.mu.Lock()
		delete(r.usedScopes, scopePath)
		r.mu.Unlock()
		return nil
	}
	defer func() {
		if !scope.IsSettled() && !abandoned {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), d09CommandCleanupLimit)
			if containerStarted {
				cleanup := exec.CommandContext(cleanupCtx, report.PodmanPath, "rm", "-f", name)
				cleanup.Env = r.launcher.commandEnvironment()
				cleanup.Stdout, cleanup.Stderr = io.Discard, io.Discard
				_ = cleanup.Run()
			}
			settled, _ := scope.Settle(cleanupCtx)
			if settled && !containerStarted {
				_ = scope.MarkReconciled()
			}
			cancel()
		}
		if scope.IsSettled() {
			_ = closeScope()
		}
		err = d09PreStartFailure(err, ackSent, scope.IsSettled())
	}()
	if scope.scope.containment != D09ContainmentProfile || scope.scope.settlement != D09SettlementProfile {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: command scope profile differs from frozen D09", ErrCgroupScopeRefused)
	}
	if err := validateCgroupDirectory(scope.scope.file); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(ErrCgroupScopeRefused, err)
	}
	scopePath, err = scope.Path()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	r.mu.Lock()
	if _, reused := r.usedScopes[scopePath]; reused {
		r.mu.Unlock()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: command cgroup scope was reused", ErrCgroupScopeRefused)
	}
	r.usedScopes[scopePath] = struct{}{}
	r.mu.Unlock()

	name = d09CellNamePrefix + "cmd-" + d09NewToken()
	commandCtx, cancelCommand := context.WithTimeout(ctx, d09CommandMaxRuntime)
	defer cancelCommand()
	cidDir, err := os.MkdirTemp("", ".tbound-d09-cid-")
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if err := os.Chmod(cidDir, 0o700); err != nil {
		_ = os.RemoveAll(cidDir)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	defer os.RemoveAll(cidDir)
	cidPath := filepath.Join(cidDir, "container.id")
	cidFile, err := newD09CIDFifo(cidPath)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	defer cidFile.Close()
	proofRead, proofWrite, err := os.Pipe()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	defer proofRead.Close()
	ackRead, ackWrite, err := os.Pipe()
	if err != nil {
		_ = proofWrite.Close()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	defer ackWrite.Close()
	config := r.launcher.config.normalized()
	hostNamespaces, err := captureD09NamespaceInodes(os.Getpid())
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("capture host namespace identities for command: %w", err)
	}
	config.HostNamespaces = hostNamespaces
	config.CgroupsPath = scopePath
	if _, err := d09PodmanCgroupParent(scopePath); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	outputToken := ""
	if r.delta.NoReap() {
		if outputToken, err = newD09OutputToken(); err != nil {
			return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
		}
	}
	plan, err := newD09DeltaPlan(r.delta, config, outputToken)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("derive the command-cell delta identity: %w", err)
	}
	args := d09CommandCellArgumentsDelta(plan, config, name, cidPath, report.ImageRef, viewMountPath, readOnlyPaths, viewID, r.leaseID, uint64(viewStat.Dev), viewStat.Ino, spec)
	var stdout, stderr d09CapturedOutput = &d09BoundedOutput{limit: d09CommandOutputLimit}, &d09BoundedOutput{limit: d09CommandOutputLimit}
	if r.delta.NoReap() {
		stdout = newD09MarkedOutput(d09CommandOutputLimit, []byte(D09OutputEndMarker(outputToken)))
		stderr = newD09MarkedOutput(d09CommandOutputLimit, []byte(D09OutputEndMarker(outputToken)))
	}
	command := exec.CommandContext(commandCtx, report.PodmanPath, args...)
	command.Env = r.launcher.commandEnvironment()
	command.ExtraFiles = []*os.File{viewSource, proofWrite, ackRead} // child fds3=readonly-view-source,4=proof,5=start-ack.
	command.Stdout, command.Stderr = stdout, stderr
	productOwner := D09ProductCommandOwnership{ContainerName: name, LeaseID: r.leaseID, ViewID: viewID, ViewDevice: uint64(viewStat.Dev), ViewInode: viewStat.Ino}
	if err := r.launcher.reportProductCommand(productOwner, nil, 0); err != nil {
		_ = proofWrite.Close()
		_ = ackRead.Close()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if err := command.Start(); err != nil {
		_ = proofWrite.Close()
		_ = ackRead.Close()
		r.cleanupCommand(scope, report.PodmanPath, name)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("start D09 command container: %w", err)
	}
	containerStarted = true
	_ = proofWrite.Close()
	_ = ackRead.Close()
	waitResult := make(chan error, 1)
	go func() { waitResult <- command.Wait() }()
	cid, err := readD09CIDFifo(commandCtx, cidFile, d09CommandStartTimeout)
	if err != nil {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("D09 command container identity is UNKNOWN: %w", err)
	}
	containerState, err := inspectContainer(commandCtx, report.PodmanPath, cid, r.launcher.commandEnvironment())
	if err != nil || containerState.State.Status != "running" || containerState.State.PID <= 0 ||
		strings.TrimPrefix(containerState.Name, "/") != name {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(errors.New("D09 command container did not start with the authorized identity"), err)
	}

	startupCtx, cancelStartup := context.WithTimeout(commandCtx, d09CommandStartTimeout)
	startProof, proofReader, proofErr := readD09CommandProofLine(startupCtx, proofRead)
	cancelStartup()
	if proofErr != nil {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("D09 command TCB startup proof is UNKNOWN: %w", proofErr)
	}
	if startProof.ContainerLeaderPID != 1 {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("D09 command entrypoint is not PID 1 in its private PID namespace")
	}
	startProof.LeaderPID = containerState.State.PID // trusted host PID from Podman inspect, not an NSpid guess.
	if err := verifyD09HostProcessIDMaps(startProof.LeaderPID, startProof.UserNSInode, config,
		startProof.UIDMap, startProof.GIDMap); err != nil {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("verify exact host-visible D09 command uid/gid map before start acknowledgement: %w", err)
	}
	if err := validateD09CommandProofDelta(plan, startProof, config, report.ImageRef, r.leaseID, viewID, name, uint64(viewStat.Dev), viewStat.Ino, spec); err != nil {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	contains, err := scope.ContainsPID(startProof.LeaderPID)
	if err != nil || !contains {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(fmt.Errorf("%w: command entrypoint leader is not in its unique cgroup", ErrCgroupScopeRefused), err)
	}
	pidfd, err := openPidfd(startProof.LeaderPID)
	if err != nil {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("open command entrypoint pidfd: %w", err)
	}
	defer syscall.Close(pidfd)
	mountNSInode, err := processMountNamespaceInode(startProof.LeaderPID)
	if err != nil || mountNSInode == 0 {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(fmt.Errorf("read command mount-namespace identity"), err)
	}
	productOwner.MountNSInode = mountNSInode
	if err := r.launcher.reportProductCommand(productOwner, scope.scope, startProof.LeaderPID); err != nil {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	var ack [1]byte
	ack[0] = 1
	ackSent = true // from here the cell may hold the acknowledgement and start the command
	if err := writeD09All(ackWrite, ack[:]); err != nil {
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("acknowledge D09 command TCB proof: %w", err)
	}
	_ = ackWrite.Close()

	if r.delta.NoReap() {
		// no-reap/v1: the command cell is left running. There is no container exit to wait for;
		// the cell's own final proof and end-of-output marker are the exit and output observation,
		// and the detached command cgroup is handed over to the operator reset instead of killed.
		markedOut, markedErr := stdout.(*d09MarkedOutput), stderr.(*d09MarkedOutput)
		result, settlement, finalProof, evidence, noReapErr := r.settleNoReap(commandCtx, d09NoReapSettle{
			scope: scope, proofReader: proofReader, startProof: startProof, containerPID: containerState.State.PID,
			podmanPath: report.PodmanPath, cid: cid, stdout: markedOut, stderr: markedErr, viewID: viewID,
		})
		if noReapErr == nil {
			noReapErr = scope.abandon()
		}
		if noReapErr != nil {
			r.cleanupCommand(scope, report.PodmanPath, name)
			_ = command.Process.Kill()
			_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
			return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, noReapErr
		}
		abandoned = true
		if err := r.issueReceipt(viewID, name, cid, spec, result, settlement, finalProof, evidence); err != nil {
			r.cleanupCommand(nil, report.PodmanPath, name)
			return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
		}
		return result, settlement, nil
	}

	var waitErr error
	select {
	case waitErr = <-waitResult:
	case <-commandCtx.Done():
		r.cleanupCommand(scope, report.PodmanPath, name)
		_ = command.Process.Kill()
		_ = waitD09PodmanShutdown(waitResult, d09CommandCleanupLimit)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, commandCtx.Err()
	}
	state, err := inspectContainer(commandCtx, report.PodmanPath, cid, r.launcher.commandEnvironment())
	if err != nil || state.State.Status != "exited" {
		r.cleanupCommand(scope, report.PodmanPath, name)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(fmt.Errorf("D09 command container did not report an observed exit"), err)
	}
	launcherExitCode, waitErrKind, waitErr := d09PodmanRunExit(waitErr)
	if waitErr != nil || launcherExitCode != state.State.ExitCode {
		r.cleanupCommand(scope, report.PodmanPath, name)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(
			fmt.Errorf("D09 Podman output/exit completion disagrees with inspected command exit (podman=%d state=%d kind=%s)", launcherExitCode, state.State.ExitCode, waitErrKind), waitErr)
	}
	finalProof, err := readD09CommandProofNext(proofReader)
	finalProof.LeaderPID = containerState.State.PID
	if err != nil || !finalProof.CommandStarted || !finalProof.CommandReady || !finalProof.CommandExitObserved || finalProof.CommandExitCode != state.State.ExitCode ||
		!sameD09CommandProofIdentity(startProof, finalProof) {
		r.cleanupCommand(scope, report.PodmanPath, name)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(fmt.Errorf("D09 command exit proof is missing or disagrees with container state"), err)
	}
	if stdout.Overflowed() || stderr.Overflowed() {
		r.cleanupCommand(scope, report.PodmanPath, name)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("D09 command output exceeded the bounded result size")
	}
	if err := removeContainer(commandCtx, report.PodmanPath, cid, r.launcher.commandEnvironment()); err != nil {
		r.cleanupCommand(scope, report.PodmanPath, name)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if exists, err := containerExists(commandCtx, report.PodmanPath, cid, r.launcher.commandEnvironment()); err != nil || exists {
		r.cleanupCommand(scope, report.PodmanPath, name)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(fmt.Errorf("D09 command container removal is UNKNOWN"), err)
	}
	settleCtx, cancelSettle := context.WithTimeout(context.Background(), d09CommandCleanupLimit)
	settled, settleErr := scope.Settle(settleCtx)
	cancelSettle()
	if settleErr != nil || !settled {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(fmt.Errorf("D09 command cgroup did not settle"), settleErr)
	}
	if !pidfdReadable(pidfd) {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("D09 command leader pidfd exit is not observed")
	}
	scanCtx, cancelScan := context.WithTimeout(context.Background(), d09CommandCleanupLimit)
	referenced, scanErr := processMountNamespaceReferences(scanCtx, mountNSInode)
	cancelScan()
	if scanErr != nil || referenced {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(fmt.Errorf("D09 command view mount detachment is UNKNOWN"), scanErr)
	}
	if err := scope.MarkReconciled(); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	if err := closeScope(); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("close settled D09 command cgroup scope: %w", err)
	}
	result := sessionrepo.CommandResult{ExitCode: state.State.ExitCode, ExitObserved: true, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	settlement := sessionrepo.CommandSettlement{
		LeaseID: r.leaseID, ViewID: viewID,
		ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true,
		ExitObserved: true, ExitCode: state.State.ExitCode,
		EvidenceClass: D09CommandRunnerProfile + ":inspect-exit+podman-wait+proof-pipe-eof+output-drained+container-removed+leader-pidfd+cgroup-populated0+mountns-released+cgroup-removed",
	}
	if err := r.issueReceipt(viewID, name, cid, spec, result, settlement, finalProof,
		D09CommandSettlementEvidence{
			ContainerExitInspected: true, FinalCommandProofRead: true, PodmanWaitCompleted: true,
			OutputWritersDrained: true, ContainerRemoved: true, LeaderPidfdExited: true,
			CgroupPopulatedZero: true, MountNamespaceReleased: true, CommandOutputWithinBound: true,
			CommandCgroupRemoved: true,
		}); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	return result, settlement, nil
}

// issueReceipt seals the one-use receipt of a finished command and retains it for the exact view.
func (r *D09CommandRunner) issueReceipt(viewID, name, cid string, spec sessionrepo.CommandSpec, result sessionrepo.CommandResult,
	settlement sessionrepo.CommandSettlement, finalProof d09CommandProof, evidence D09CommandSettlementEvidence) error {
	receipt, err := newD09CommandReceipt(r.leaseID, viewID, name, cid, spec, result, settlement, finalProof, evidence)
	if err != nil {
		return fmt.Errorf("issue D09 command proof receipt: %w", err)
	}
	if err := r.launcher.retainCommandReceipt(); err != nil {
		return err
	}
	r.mu.Lock()
	if _, exists := r.receipts[viewID]; exists {
		r.mu.Unlock()
		r.launcher.releaseCommandReceipt()
		return ErrD09CommandReceiptInvalid
	}
	r.receipts[viewID] = receipt
	r.mu.Unlock()
	return nil
}

// reserveCommandView atomically burns a unique live CommandView for one Run.
// It remains burned on every error/UNKNOWN path; an ambiguous command cannot be
// replayed under the same view identity.
func (r *D09CommandRunner) reserveCommandView(viewID string) error {
	if r == nil || !validIdentity(viewID) {
		return ErrD09CommandReceiptInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.usedViews == nil {
		r.usedViews = make(map[string]struct{})
	}
	if _, reused := r.usedViews[viewID]; reused {
		return fmt.Errorf("%w: command view already has a D09 invocation", ErrD09CommandReceiptInvalid)
	}
	r.usedViews[viewID] = struct{}{}
	return nil
}

func sameD09CommandProofIdentity(first, final d09CommandProof) bool {
	return first.Schema == final.Schema && first.UID == final.UID && first.GID == final.GID &&
		first.CapEff == final.CapEff && first.CapPrm == final.CapPrm && first.CapBnd == final.CapBnd &&
		first.CapInh == final.CapInh && first.CapAmb == final.CapAmb && first.NoNewPrivs == final.NoNewPrivs && first.Seccomp == final.Seccomp &&
		first.LandlockABI == final.LandlockABI && first.LandlockRulesetDigest == final.LandlockRulesetDigest &&
		first.LandlockTSYNC == final.LandlockTSYNC && first.CgroupV2 == final.CgroupV2 &&
		first.UIDMap == final.UIDMap && first.GIDMap == final.GIDMap && first.UserNamespaceMapDigest == final.UserNamespaceMapDigest &&
		first.LeaderPID == final.LeaderPID && first.LeaseID == final.LeaseID && first.ViewID == final.ViewID &&
		first.ContainerLeaderPID == final.ContainerLeaderPID &&
		first.ContainerName == final.ContainerName && first.ImageRef == final.ImageRef &&
		first.ImageDigest == final.ImageDigest && first.TemplateDigest == final.TemplateDigest &&
		first.CommandPath == final.CommandPath && first.CommandDigest == final.CommandDigest && first.CommandWorkdir == final.CommandWorkdir &&
		first.ConsumerMountsVerified == final.ConsumerMountsVerified && first.RawSourceFDsClosed == final.RawSourceFDsClosed &&
		first.CommandReady == final.CommandReady &&
		first.WorkerMountID == final.WorkerMountID && first.RuntimeMountID == final.RuntimeMountID &&
		first.WorkerDevice == final.WorkerDevice && first.WorkerInode == final.WorkerInode &&
		first.RuntimeDevice == final.RuntimeDevice && first.RuntimeInode == final.RuntimeInode &&
		first.UserNSInode == final.UserNSInode && first.MountNSInode == final.MountNSInode &&
		first.PIDNSInode == final.PIDNSInode && first.IPCNSInode == final.IPCNSInode &&
		first.UTSNSInode == final.UTSNSInode && first.NetworkNSInode == final.NetworkNSInode &&
		first.NoExternalRoutes == final.NoExternalRoutes &&
		first.Delta == final.Delta && first.DeltaTemplateDigest == final.DeltaTemplateDigest
}

// d09PreStartFailure types a failed command run as protocol.ErrCommandNotStarted when the host never
// sent the start acknowledgement, which the entrypoint waits for before it starts the command, and the
// cell's cgroup was then observed empty. Any other failure stays untyped.
func d09PreStartFailure(err error, ackSent, cellSettled bool) error {
	if err == nil || ackSent || !cellSettled {
		return err
	}
	return errors.Join(protocol.ErrCommandNotStarted, err)
}

// allocateCommandScope allocates the unique command cgroup of the lease's delta. A failure here is
// typed protocol.ErrCommandNotStarted: no container or process exists yet, and the scope allocation
// removes the empty directory it made (an empty one left behind holds no process), so the one-use lease is
// released instead of staying pending as an unknown effect.
func (r *D09CommandRunner) allocateCommandScope(ctx context.Context, viewID string) (*d09OwnedCommandScope, error) {
	var scope *d09OwnedCommandScope
	var err error
	switch {
	case r.delta.NoReap():
		scope, err = r.launcher.NewDetachedCommandScope(ctx, r.leaseID, viewID)
	case r.delta.NetSink():
		// Fail closed before any cell starts: the pinned rule must be installed and attested as
		// loaded for the current net-sink parent cgroup (NewNetSinkCommandScope).
		scope, err = r.launcher.NewNetSinkCommandScope(ctx, r.leaseID, viewID)
	default:
		scope, err = r.launcher.NewCommandScope(ctx, r.leaseID, viewID)
	}
	if err != nil {
		return nil, d09PreStartFailure(fmt.Errorf("allocate unique D09 command cgroup: %w", err), false, true)
	}
	return scope, nil
}

func (r *D09CommandRunner) cleanupCommand(scope *d09OwnedCommandScope, podmanPath, name string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), d09CommandCleanupLimit)
	defer cancel()
	cmd := exec.CommandContext(cleanupCtx, podmanPath, "rm", "-f", name)
	cmd.Env = r.launcher.commandEnvironment()
	_ = cmd.Run()
	if scope != nil {
		_, _ = scope.Settle(cleanupCtx)
	}
}

func writeD09All(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := writer.Write(data)
		if n < 0 || n > len(data) {
			return errors.New("writer returned invalid byte count")
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func newD09CIDFifo(path string) (*os.File, error) {
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
}

// readD09CIDFifo waits for the 64-hex container id podman writes to its cidfile
// FIFO. The FIFO is opened O_RDWR by this process, so an early podman exit never
// produces EOF, and a Go read on a pollable FIFO blocks in the poller without
// returning EAGAIN. The read therefore runs in a goroutine and the wait ends with
// the context or the timeout, whichever is first; the pending read is then
// released through the read deadline.
func readD09CIDFifo(ctx context.Context, fifo *os.File, timeout time.Duration) (string, error) {
	if ctx == nil || fifo == nil {
		return "", ErrProductionLauncherUnavailable
	}
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_ = fifo.SetReadDeadline(time.Time{}) // an earlier wait on this FIFO may have left an expired deadline
	type readResult struct {
		data []byte
		err  error
	}
	for {
		completed := make(chan readResult, 1)
		go func() {
			buffer := make([]byte, 256)
			n, err := fifo.Read(buffer)
			completed <- readResult{data: buffer[:n], err: err}
		}()
		select {
		case result := <-completed:
			if len(result.data) > 0 {
				id := strings.TrimSpace(string(result.data))
				if len(id) != 64 {
					return "", errors.New("cidfile did not contain a complete 64-hex container id")
				}
				if _, err := hex.DecodeString(id); err != nil {
					return "", errors.New("cidfile container id is not hexadecimal")
				}
				return id, nil
			}
			if result.err != nil && !errors.Is(result.err, syscall.EAGAIN) && !errors.Is(result.err, syscall.EWOULDBLOCK) {
				return "", result.err
			}
			select {
			case <-readCtx.Done():
				return "", readCtx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		case <-readCtx.Done():
			_ = fifo.SetReadDeadline(time.Now())
			// Let the released read finish so it cannot take bytes meant for a later read.
			select {
			case <-completed:
			case <-time.After(time.Second):
			}
			return "", readCtx.Err()
		}
	}
}

func validateD09CommandSpec(spec sessionrepo.CommandSpec, config D09Config) error {
	if !filepath.IsAbs(spec.Executable) || filepath.Clean(spec.Executable) != spec.Executable || spec.Executable != config.CommandExecutable {
		return fmt.Errorf("%w: command executable is not the frozen Bash identity", ErrProductionLauncherUnavailable)
	}
	if spec.WorkingDirectory != "" {
		cleaned := filepath.Clean(spec.WorkingDirectory)
		if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("%w: command working directory escapes the disposable view", ErrProductionLauncherUnavailable)
		}
	}
	for _, arg := range spec.Args {
		if strings.ContainsRune(arg, '\x00') || len(arg) > 64<<10 {
			return fmt.Errorf("%w: command argument is invalid or exceeds bound", ErrProductionLauncherUnavailable)
		}
	}
	return nil
}

// d09CommandCellArgumentsDelta builds the Podman command vector of one command cell under the plan
// of its delta. The unrelaxed plan yields the signed base cell. net-sink/v1 changes the network
// mode and the Landlock ruleset digest, and passes the delta and its derived template identity;
// no-reap/v1 passes the delta, its identity and the end-of-output marker. Nothing else differs.
// readOnly lists view entries mounted read-only over /work (product scope only; empty otherwise).
func d09CommandCellArgumentsDelta(plan d09DeltaPlan, config D09Config, name, cidPath, imageRef, viewPath string, readOnly []string, viewID, leaseID string, viewDevice, viewInode uint64, spec sessionrepo.CommandSpec) []string {
	uid, gid := strconv.Itoa(config.UID), strconv.Itoa(config.GID)
	workdir := "/work"
	if spec.WorkingDirectory != "" {
		workdir = filepath.Join(workdir, spec.WorkingDirectory)
	}
	entrypoint := config.EntrypointPath
	if entrypoint == "" {
		entrypoint = d09EntrypointPath
	}
	args := []string{
		"run", "--name", name, "--runtime", config.CrunPath, "--cidfile", cidPath, "--pull=never", "--network=" + plan.delta.NetworkMode(),
		"--pid=private", "--ipc=private", "--uts=private", "--read-only", "--cap-drop=ALL",
		"--security-opt", "no-new-privileges", "--security-opt", "seccomp=" + config.SeccompProfilePath,
		"--pids-limit", strconv.Itoa(config.PidsMax), "--memory", strconv.Itoa(config.MemoryMB) + "m", "--cpus", config.CPUs,
		"--uidmap", strconv.Itoa(config.CellUID) + ":0:1", "--gidmap", strconv.Itoa(config.CellGID) + ":0:1",
		"--user", uid + ":" + gid, "--workdir", "/work", "--preserve-fds", "3",
		// tbound-pi.service runs with UMask=0077, which a podman-started cell inherits. The settled view
		// import accepts only 0644 and 0755 files, so files a command creates must not be 0600.
		"--umask=0022",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777",
		"--cgroup-parent", d09PodmanCgroupParentOrSelf(config.CgroupsPath),
		"--mount", "type=bind,src=" + viewPath + ",dst=/work,rw,bind-propagation=rprivate",
		"-e", "HOME=" + PiWorkerCellHome, "-e", "TMPDIR=" + PiWorkerCellTemp, "-e", "LANG=C.UTF-8", "-e", "PI_OFFLINE=1",
		"-e", "TBOUND_CELL_UID=" + uid, "-e", "TBOUND_CELL_GID=" + gid,
		"-e", "TBOUND_USERNS_MAP_PROFILE=" + config.UserNamespaceMappingProfile,
		"-e", "TBOUND_USERNS_MAP_DIGEST=" + config.UserNamespaceMappingDigest,
		"-e", "TBOUND_USERNS_HOST_UID=" + strconv.Itoa(config.UserNamespaceHostUID),
		"-e", "TBOUND_USERNS_HOST_GID=" + strconv.Itoa(config.UserNamespaceHostGID),
		"-e", "TBOUND_LANDLOCK_ABI=" + strconv.Itoa(config.LandlockABI),
		"-e", "TBOUND_LANDLOCK_RULESET_DIGEST=" + plan.landlockDigest,
		"-e", "TBOUND_LANDLOCK_RO_PATH=" + config.LandlockROPath,
		"-e", "TBOUND_LANDLOCK_RW_PATH=" + config.LandlockRWPath,
		"-e", "TBOUND_CELL_MODE=command", "-e", "TBOUND_CELL_COMMAND=" + spec.Executable,
		"-e", "TBOUND_COMMAND_DIGEST=" + config.CommandExecutableDigest,
		"-e", "TBOUND_IMAGE_REF=" + imageRef,
		"-e", "TBOUND_IMAGE_DIGEST=" + config.ExpectedImageDigest,
		"-e", "TBOUND_TEMPLATE_DIGEST=" + config.TemplateDigest,
		"-e", "TBOUND_CONTAINER_NAME=" + name,
		"-e", "TBOUND_COMMAND_LEASE_ID=" + leaseID, "-e", "TBOUND_COMMAND_VIEW_ID=" + viewID,
		"-e", "TBOUND_COMMAND_WORKDIR=" + spec.WorkingDirectory,
		"-e", "TBOUND_COMMAND_VIEW_DEVICE=" + strconv.FormatUint(viewDevice, 10),
		"-e", "TBOUND_COMMAND_VIEW_INODE=" + strconv.FormatUint(viewInode, 10),
		"-e", "TBOUND_HOST_NS_USER=" + strconv.FormatUint(config.HostNamespaces.User, 10),
		"-e", "TBOUND_HOST_NS_MOUNT=" + strconv.FormatUint(config.HostNamespaces.Mount, 10),
		"-e", "TBOUND_HOST_NS_PID=" + strconv.FormatUint(config.HostNamespaces.PID, 10),
		"-e", "TBOUND_HOST_NS_IPC=" + strconv.FormatUint(config.HostNamespaces.IPC, 10),
		"-e", "TBOUND_HOST_NS_UTS=" + strconv.FormatUint(config.HostNamespaces.UTS, 10),
		"-e", "TBOUND_HOST_NS_NETWORK=" + strconv.FormatUint(config.HostNamespaces.Network, 10),
		"-e", "TBOUND_CELL_PROOF_FD=4", "-e", "TBOUND_CELL_ACK_FD=5",
	}
	for _, path := range readOnly {
		// Podman reads --mount as comma-separated CSV, so such a path could inject mount options. It
		// stays unmounted; admission still refuses changes there.
		if strings.ContainsAny(path, ",\"") {
			continue
		}
		args = append(args, "--mount", "type=bind,src="+viewPath+"/"+path+",dst=/work/"+path+",ro,bind-propagation=rprivate")
	}
	if plan.delta.Relaxed() {
		args = append(args, "-e", "TBOUND_CELL_DELTA="+plan.delta.Name(), "-e", "TBOUND_DELTA_TEMPLATE_DIGEST="+plan.templateDigest)
	}
	if ports := plan.delta.ConnectPortsText(); ports != "" {
		args = append(args, "-e", "TBOUND_LANDLOCK_CONNECT_PORTS="+ports)
	}
	if plan.outputMarker != "" {
		args = append(args, "-e", "TBOUND_OUTPUT_END_MARKER="+plan.outputMarker)
	}
	if plan.delta.NoReap() {
		// The cell is left running and the container is not removed (no --rm): the labels let the
		// operator reset find and remove exactly these containers (tbound trial reset).
		args = append(args, "--label", D09NoReapContainerLabel, "--label", D09LeaseContainerLabelKey+"="+leaseID)
	}
	// Pin the verified entrypoint explicitly; the image ENTRYPOINT would otherwise be followed by
	// the entrypoint path again and the entrypoint requires argv[1] to be the signed command.
	args = append(args, "--entrypoint", entrypoint, imageRef, spec.Executable)
	return append(args, spec.Args...)
}

func readD09CommandProofLine(ctx context.Context, reader io.Reader) (d09CommandProof, *bufio.Reader, error) {
	buffered := bufio.NewReaderSize(reader, d09CommandProofLimit)
	lineCh := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, err := readBoundedProofLine(buffered)
		lineCh <- struct {
			line string
			err  error
		}{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return d09CommandProof{}, nil, ctx.Err()
	case result := <-lineCh:
		if result.err != nil {
			return d09CommandProof{}, nil, result.err
		}
		if err := protocol.ValidateStrictJSON([]byte(strings.TrimSpace(result.line))); err != nil {
			return d09CommandProof{}, nil, err
		}
		var proof d09CommandProof
		decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(result.line)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&proof); err != nil {
			return proof, nil, err
		}
		return proof, buffered, nil
	}
}

func readD09CommandProofNext(reader *bufio.Reader) (d09CommandProof, error) {
	line, err := readBoundedProofLine(reader)
	if err != nil {
		return d09CommandProof{}, errors.Join(errors.New("read final command TCB proof"), err)
	}
	if err := protocol.ValidateStrictJSON([]byte(strings.TrimSpace(line))); err != nil {
		return d09CommandProof{}, err
	}
	var proof d09CommandProof
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(line)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil {
		return proof, err
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		return d09CommandProof{}, errors.New("final command proof pipe remained open or emitted extra data")
	}
	return proof, nil
}

func readBoundedProofLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return "", err
	}
	if len(line) > d09CommandProofLimit {
		return "", errors.New("TCB proof exceeds byte bound")
	}
	return string(line), nil
}

// validateD09CommandProofDelta validates the command proof of a cell launched under the plan of its
// delta: the ruleset digest, namespace profile and delta identity must be the ones the host derived
// for exactly this delta, so the proof of one cell cannot stand for another.
func validateD09CommandProofDelta(plan d09DeltaPlan, proof d09CommandProof, config D09Config, imageRef, leaseID, viewID, containerName string, viewDevice, viewInode uint64, spec sessionrepo.CommandSpec) error {
	if proof.Delta != plan.delta.Name() || (proof.DeltaTemplateDigest != "") != plan.delta.Relaxed() ||
		plan.delta.Relaxed() && proof.DeltaTemplateDigest != plan.templateDigest {
		return fmt.Errorf("%w: command proof delta identity does not match the launched delta", ErrProductionLauncherUnavailable)
	}
	config.LandlockRulesetDigest = plan.landlockDigest
	base := D09TCBProof{Schema: proof.Schema, UID: proof.UID, GID: proof.GID, CapEff: proof.CapEff,
		CapPrm: proof.CapPrm, CapBnd: proof.CapBnd, CapInh: proof.CapInh, CapAmb: proof.CapAmb, NoNewPrivs: proof.NoNewPrivs, Seccomp: proof.Seccomp, LandlockABI: proof.LandlockABI,
		LandlockRulesetDigest: proof.LandlockRulesetDigest, LandlockTSYNC: proof.LandlockTSYNC,
		CgroupV2: proof.CgroupV2, UIDMap: proof.UIDMap, GIDMap: proof.GIDMap, UserNamespaceMapDigest: proof.UserNamespaceMapDigest,
		LeaderPID: proof.LeaderPID, ContainerLeaderPID: proof.ContainerLeaderPID, ContainerName: proof.ContainerName,
		ImageDigest: proof.ImageDigest, TemplateDigest: proof.TemplateDigest,
		ImageRef:               proof.ImageRef,
		ConsumerMountsVerified: proof.ConsumerMountsVerified, RawSourceFDsClosed: proof.RawSourceFDsClosed,
		WorkerMountID: proof.WorkerMountID, RuntimeMountID: proof.RuntimeMountID,
		WorkerDevice: proof.WorkerDevice, WorkerInode: proof.WorkerInode,
		RuntimeDevice: proof.RuntimeDevice, RuntimeInode: proof.RuntimeInode,
		UserNSInode: proof.UserNSInode, MountNSInode: proof.MountNSInode,
		PIDNSInode: proof.PIDNSInode, IPCNSInode: proof.IPCNSInode,
		UTSNSInode: proof.UTSNSInode, NetworkNSInode: proof.NetworkNSInode,
		NoExternalRoutes: proof.NoExternalRoutes,
	}
	if err := validateD09TCBProof(base, config); err != nil {
		return err
	}
	if err := validateD09NamespaceProofDelta(base, config.HostNamespaces, plan.delta); err != nil {
		return err
	}
	if proof.UserNamespaceMapDigest != config.UserNamespaceMappingDigest ||
		VerifyD09InsideIDMaps(config.UserNamespaceMappingProfile, config.UserNamespaceMappingDigest,
			config.CellUID, config.CellGID, config.UserNamespaceHostUID, config.UserNamespaceHostGID,
			[]byte(proof.UIDMap), []byte(proof.GIDMap)) != nil {
		return fmt.Errorf("%w: command proof does not match the signed one-UID/GID mapping", ErrProductionLauncherUnavailable)
	}
	if !proof.ConsumerMountsVerified || !proof.RawSourceFDsClosed || proof.WorkerMountID == 0 ||
		proof.RuntimeMountID != proof.WorkerMountID ||
		proof.WorkerDevice != viewDevice || proof.WorkerInode != viewInode ||
		proof.RuntimeDevice != viewDevice || proof.RuntimeInode != viewInode ||
		proof.LeaseID != leaseID || proof.ViewID != viewID || proof.ContainerName != containerName || proof.ImageRef != imageRef ||
		proof.ImageDigest != config.ExpectedImageDigest || proof.TemplateDigest != config.TemplateDigest || !proof.CommandReady || proof.CommandStarted ||
		proof.CommandPath != spec.Executable || proof.CommandDigest != config.CommandExecutableDigest || proof.CommandWorkdir != filepath.Clean(spec.WorkingDirectory) {
		return fmt.Errorf("%w: command TCB proof does not bind the authorized lease/view", ErrProductionLauncherUnavailable)
	}
	return nil
}

type d09ContainerState struct {
	Name  string `json:"Name"`
	State struct {
		Status    string `json:"Status"`
		ExitCode  int    `json:"ExitCode"`
		PID       int    `json:"Pid"`
		OOMKilled bool   `json:"OOMKilled"`
		Error     string `json:"Error"`
	} `json:"State"`
}

func inspectContainer(ctx context.Context, podmanPath, id string, environment []string) (d09ContainerState, error) {
	cmd := exec.CommandContext(ctx, podmanPath, "inspect", id)
	cmd.Env = environment
	var output d09BoundedOutput
	output.limit = 1 << 20
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return d09ContainerState{}, err
	}
	var values []d09ContainerState
	if err := json.Unmarshal(output.Bytes(), &values); err != nil || len(values) != 1 || output.Overflowed() {
		return d09ContainerState{}, errors.Join(errors.New("decode bounded Podman container state"), err)
	}
	return values[0], nil
}

func removeContainer(ctx context.Context, podmanPath, id string, environment []string) error {
	cmd := exec.CommandContext(ctx, podmanPath, "rm", "-f", id)
	cmd.Env = environment
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

func containerExists(ctx context.Context, podmanPath, id string, environment []string) (bool, error) {
	cmd := exec.CommandContext(ctx, podmanPath, "container", "exists", id)
	cmd.Env = environment
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func pidfdReadable(fd int) bool {
	epollFD, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return false
	}
	defer syscall.Close(epollFD)
	if err := syscall.EpollCtl(epollFD, syscall.EPOLL_CTL_ADD, fd, &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(fd)}); err != nil {
		return false
	}
	var events [1]syscall.EpollEvent
	count, err := syscall.EpollWait(epollFD, events[:], 0)
	return err == nil && count > 0
}

func processMountNamespaceInode(pid int) (uint64, error) {
	info, err := os.Stat(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return 0, errors.New("mount namespace has no stable inode")
	}
	return stat.Ino, nil
}

// processMountNamespaceReferences proves the exact container mount namespace
// is no longer held by process membership or an open namespace fd. Any
// unreadable live /proc entry is UNKNOWN rather than assumed detached.
// foreignProcessDenied reports an access denial on a process another user owns. Such a process cannot have
// joined the cell's mount namespace, which belongs to the service's own user namespace, without privilege
// (trusted root); a denial on one of the service's own processes still fails closed.
func foreignProcessDenied(pid string, err error) bool {
	if !errors.Is(err, os.ErrPermission) {
		return false
	}
	info, statErr := os.Stat(filepath.Join("/proc", pid))
	if statErr != nil {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(owner.Uid) != os.Geteuid()
}

func processMountNamespaceReferences(ctx context.Context, expectedInode uint64) (bool, error) {
	if ctx == nil || expectedInode == 0 {
		return false, errors.New("empty mount namespace identity")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	needle := fmt.Sprintf("mnt:[%d]", expectedInode)
	var firstErr error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		pid := entry.Name()
		if !isProcessID(pid) {
			continue
		}
		namespace := filepath.Join("/proc", pid, "ns", "mnt")
		info, statErr := os.Stat(namespace)
		if statErr == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Ino == expectedInode {
				return true, nil
			}
		} else if foreignProcessDenied(pid, statErr) {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) && !errors.Is(statErr, syscall.ESRCH) {
			firstErr = cmp.Or(firstErr, fmt.Errorf("inspect %s: %w", namespace, statErr))
			continue
		}
		fds := filepath.Join("/proc", pid, "fd")
		fdEntries, readErr := os.ReadDir(fds)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, syscall.ESRCH) || foreignProcessDenied(pid, readErr) {
				continue
			}
			firstErr = cmp.Or(firstErr, fmt.Errorf("scan %s: %w", fds, readErr))
			continue
		}
		for _, fd := range fdEntries {
			target, linkErr := os.Readlink(filepath.Join(fds, fd.Name()))
			if linkErr != nil {
				if errors.Is(linkErr, os.ErrNotExist) || errors.Is(linkErr, syscall.ESRCH) {
					continue
				}
				firstErr = cmp.Or(firstErr, fmt.Errorf("read %s fd target: %w", fds, linkErr))
				continue
			}
			if target == needle {
				return true, nil
			}
		}
	}
	return false, firstErr // an inaccessible process of the service's own is UNKNOWN unless a reference was found
}

func isProcessID(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func d09CommandSpecBounded(spec sessionrepo.CommandSpec) error {
	if len(spec.Args) > 256 {
		return errors.New("command argument count exceeds bound")
	}
	total := len(spec.Executable)
	for _, arg := range spec.Args {
		total += len(arg)
	}
	if total > 64<<10 {
		return errors.New("command argv exceeds byte bound")
	}
	return nil
}

// d09PodmanRunExit accepts only an observed Podman process exit (success or an
// ordinary non-zero ExitError). A pipe-copy, wait, or other launcher error is
// not writer-stop evidence. The caller also compares this code with inspect's
// independently observed container exit code.
func d09PodmanRunExit(waitErr error) (int, string, error) {
	if waitErr == nil {
		return 0, "success", nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode(), "exit-error", nil
	}
	return -1, "wait-error", waitErr
}

func waitD09PodmanShutdown(waitResult <-chan error, timeout time.Duration) error {
	if waitResult == nil || timeout <= 0 {
		return errors.New("invalid bounded Podman wait")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waitResult:
		return nil
	case <-timer.C:
		return errors.New("Podman wait/output-writer shutdown exceeded cleanup deadline")
	}
}

var _ sessionrepo.CommandRunner = (*D09CommandRunner)(nil)
var _ sessionrepo.CommandRunnerProfile = (*D09CommandRunner)(nil)
