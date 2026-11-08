//go:build linux

// containment_launch_linux.go: D09 verified launch: TCB proofs, launch records, launch request validation and container arguments.

package piruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/broker/protocol"
)

// StartVerified launches the governed Pi worker inside a real rootless
// Podman/crun container. It binds the request to the admitted profile, the fixed
// in-cell identities, and the verified sealed exposure descriptor, then starts
// the container with the fixed descriptor birth order. On any start error it
// performs owned cleanup through the held cgroup scope with a finite context, so
// a canceled caller context can never skip teardown.
func (l *D09PodmanCrunLauncher) StartVerified(ctx context.Context, request ExecutableLaunchRequest) (*exec.Cmd, error) {
	command, _, err := l.StartVerifiedWithProof(ctx, request)
	return command, err
}

// D09TCBProof is the typed, captured TCB proof the entrypoint writes on the
// dedicated proof pipe (fd10) before it execs Node. The pipe is closed before
// Node starts, so the worker cannot forge it.
type D09TCBProof struct {
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
	// Launch identity is attached by the launcher after it captures the
	// entrypoint proof, so a journaled proof is bound to the exact container,
	// leader pid, image, and template it came from (never stale global state).
	ContainerName          string `json:"container_name"`
	LeaderPID              int    `json:"leader_pid"`
	ContainerLeaderPID     int    `json:"container_leader_pid"`
	LauncherProcessPID     int    `json:"launcher_process_pid"`
	ImageRef               string `json:"image_ref"`
	ImageDigest            string `json:"image_digest"`
	TemplateDigest         string `json:"template_digest"`
	ConsumerMountsVerified bool   `json:"consumer_mounts_verified"`
	RawSourceFDsClosed     bool   `json:"raw_source_fds_closed"`
	// Source* fields are attached by the host from the Store-issued capability;
	// they are distinct from ConsumerWorkerMountID, measured by PID1 inside the
	// private mount namespace after the read-only bind is installed.
	SourceGenerationID     string `json:"source_generation_id,omitempty"`
	SourceTreeDigest       string `json:"source_tree_digest,omitempty"`
	SourceManifestDigest   string `json:"source_manifest_digest,omitempty"`
	SourceBindingDigest    string `json:"source_binding_digest,omitempty"`
	SourceProvenanceDigest string `json:"source_provenance_digest,omitempty"`
	SourceIdentityDigest   string `json:"source_identity_digest,omitempty"`
	SourceStoreAuditHead   string `json:"source_store_audit_head,omitempty"`
	SourceStoreAuditCount  uint64 `json:"source_store_audit_record_count,omitempty"`
	SourceDevice           uint64 `json:"source_device,omitempty"`
	SourceInode            uint64 `json:"source_inode,omitempty"`
	SourceMountID          uint64 `json:"source_mount_id,omitempty"`
	ConsumerWorkerMountID  uint64 `json:"consumer_worker_mount_id,omitempty"`
	ConsumerRuntimeMountID uint64 `json:"consumer_runtime_mount_id,omitempty"`
	// WorkerMountID is the historic proof-wire name for the entrypoint's
	// consumer mount ID; it is never the host source capability's SourceMountID.
	WorkerMountID    uint64 `json:"worker_mount_id"`
	RuntimeMountID   uint64 `json:"runtime_mount_id"`
	WorkerDevice     uint64 `json:"worker_device"`
	WorkerInode      uint64 `json:"worker_inode"`
	RuntimeDevice    uint64 `json:"runtime_device"`
	RuntimeInode     uint64 `json:"runtime_inode"`
	UserNSInode      uint64 `json:"user_ns_inode"`
	MountNSInode     uint64 `json:"mount_ns_inode"`
	PIDNSInode       uint64 `json:"pid_ns_inode"`
	IPCNSInode       uint64 `json:"ipc_ns_inode"`
	UTSNSInode       uint64 `json:"uts_ns_inode"`
	NetworkNSInode   uint64 `json:"network_ns_inode"`
	NoExternalRoutes bool   `json:"no_external_routes"`
	// RouteProbe is the result of the direct-runtime route probe. Only the entrypoint of a cell launched
	// from a route-probe template writes it (and then does not start Pi); validateD09RouteProbeProof
	// requires it exactly when the template names a probe.
	RouteProbe *D09RouteProbeResult `json:"route_probe,omitempty"`
}

// StartVerifiedWithProof launches the cell and returns the captured, validated
// TCB proof in addition to the process. It refuses to return a launched cell
// unless the entrypoint proved every measured postcondition on the dedicated
// proof pipe.
func (l *D09PodmanCrunLauncher) StartVerifiedWithProof(ctx context.Context, request ExecutableLaunchRequest) (returnedCommand *exec.Cmd, returnedProof D09TCBProof, returnedErr error) {
	if ctx == nil || l == nil || l.productHost != nil || l.runner == nil || l.scope == nil {
		return nil, D09TCBProof{}, ErrProductionLauncherUnavailable
	}
	if err := l.beginOperation(); err != nil {
		return nil, D09TCBProof{}, err
	}
	defer l.endOperation()
	l.proofMu.Lock()
	if l.launchInProg || (len(l.proofs) != 0) {
		l.proofMu.Unlock()
		return nil, D09TCBProof{}, fmt.Errorf("%w: D09 launcher instance is bound to one worker launch", ErrProductionLauncherUnavailable)
	}
	l.launchInProg = true
	l.proofMu.Unlock()
	launchSucceeded := false
	var launchCmd *exec.Cmd
	var launchName, launchPodman string
	var stdoutPipe *ownedStdoutPipe
	defer func() {
		if launchSucceeded {
			return
		}
		if launchCmd != nil && launchCmd.Process != nil {
			record := l.launchRecord(launchCmd)
			returnedErr = errors.Join(returnedErr,
				l.cleanupFailedContainer(launchCmd, launchPodman, launchName))
			returnedErr = errors.Join(returnedErr, l.failedLaunchExitStatus(record))
			return
		}
		if stdoutPipe != nil {
			returnedErr = errors.Join(returnedErr, stdoutPipe.CloseWriter(), stdoutPipe.CloseReader())
		}
		l.proofMu.Lock()
		l.launchInProg = false
		l.proofMu.Unlock()
	}()
	profile, err := l.validateD09LaunchRequest(ctx, request)
	if err != nil {
		return nil, D09TCBProof{}, err
	}
	if !l.isProfileAdmitted(profile) {
		return nil, D09TCBProof{}, fmt.Errorf("%w: D09 launcher was not admitted by the protected host-profile verifier", ErrProductionLauncherUnavailable)
	}
	if l.scope.containment != profile.ContainmentProfile || l.scope.settlement != profile.SettlementProfile ||
		l.scope.RootDigest() != profile.CgroupRootDigest || !sameBinding(l.scope, request.admission.runtime.Descendant) {
		return nil, D09TCBProof{}, fmt.Errorf("%w: D09 cgroup scope does not match the signed profile", ErrHostProfileMismatch)
	}
	report, err := l.Probe(ctx)
	if err != nil {
		return nil, D09TCBProof{}, err
	}
	if err := verifyImmutableLinuxExecutable(request.Path); err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("%w: %v", ErrLinuxLauncherRefused, err)
	}
	executable, err := openVerifiedExecutable(request.Path, request.Digest)
	if err != nil {
		return nil, D09TCBProof{}, err
	}
	defer executable.Close()
	ordered := cloneInheritedDescriptors(request.Descriptors)
	proofRead, proofWrite, err := os.Pipe()
	if err != nil {
		return nil, D09TCBProof{}, err
	}
	defer proofRead.Close()
	extraFiles := make([]*os.File, 1, len(ordered)+2)
	extraFiles[0] = executable // child fd 3 is the held, digest-verified Node binary.
	for _, descriptor := range ordered {
		if descriptor.File == nil {
			_ = proofWrite.Close()
			return nil, D09TCBProof{}, ErrHostBindingsMissing
		}
		extraFiles = append(extraFiles, descriptor.File)
	}
	extraFiles = append(extraFiles, proofWrite) // child fd 10 is the dedicated TCB proof pipe.
	config := l.config.normalized()
	hostNamespaces, err := captureD09NamespaceInodes(os.Getpid())
	if err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("capture host namespace identities: %w", err)
	}
	config.HostNamespaces = hostNamespaces
	scopePath, err := cgroupDescriptorPath(l.scope.file)
	if err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("resolve held D09 cgroup scope: %w", err)
	}
	config.CgroupsPath = scopePath
	if _, err := d09PodmanCgroupParent(scopePath); err != nil {
		return nil, D09TCBProof{}, err
	}
	if profile.WorkingDirectory != PiWorkerCellDefaultWorkingDirectory {
		return nil, D09TCBProof{}, fmt.Errorf("%w: D09 worker CWD must be the registered consumer mount %s", ErrHostProfileMismatch, PiWorkerCellDefaultWorkingDirectory)
	}
	cidDir, err := os.MkdirTemp("", ".tbound-d09-worker-cid-")
	if err != nil {
		return nil, D09TCBProof{}, err
	}
	if err := os.Chmod(cidDir, 0o700); err != nil {
		_ = os.RemoveAll(cidDir)
		return nil, D09TCBProof{}, err
	}
	defer os.RemoveAll(cidDir)
	cidPath := filepath.Join(cidDir, "container.id")
	cidFile, err := newD09CIDFifo(cidPath)
	if err != nil {
		return nil, D09TCBProof{}, err
	}
	defer cidFile.Close()
	workerSourceHandle := request.WorkerExposureHandle
	if request.admission.runtime.D09Source != nil {
		workerSourceHandle = request.D09SourceHandle
	}
	exposureMount, err := descriptorPathForMount(workerSourceHandle)
	if err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("resolve verified consumer exposure mount: %w", err)
	}
	bundleMount, err := descriptorPathForMount(request.PiRuntimeBundleHandle)
	if err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("resolve verified consumer runtime mount: %w", err)
	}
	name := d09CellNamePrefix + d09NewToken()
	mounts := []string{
		"type=bind,src=" + exposureMount + ",dst=" + profile.WorkingDirectory + ",ro,bind-propagation=rprivate",
		"type=bind,src=" + bundleMount + ",dst=/runtime,ro,bind-propagation=rprivate",
	}
	workerDevice, workerInode := uint64(0), uint64(0)
	runtimeEvidence := request.admission.runtime.RuntimeAssetEvidence
	if request.admission.runtime.D09Source != nil {
		workerDevice = request.admission.runtime.D09SourceEvidence.SourceDevice
		workerInode = request.admission.runtime.D09SourceEvidence.SourceInode
		runtimeEvidence = request.admission.runtime.D09RuntimeAssetEvidence
	} else {
		workerDevice = request.admission.runtime.WorkerExposureEvidence.Device
		workerInode = request.admission.runtime.WorkerExposureEvidence.Inode
	}
	extraEnv := []string{
		"TBOUND_WORKER_CWD=" + profile.WorkingDirectory,
		"TBOUND_WORKER_DEVICE=" + strconv.FormatUint(workerDevice, 10),
		"TBOUND_WORKER_INODE=" + strconv.FormatUint(workerInode, 10),
		"TBOUND_RUNTIME_DEVICE=" + strconv.FormatUint(runtimeEvidence.Device, 10),
		"TBOUND_RUNTIME_INODE=" + strconv.FormatUint(runtimeEvidence.Inode, 10),
		"TBOUND_RUNTIME_BUNDLE_DIGEST=" + profile.PiRuntimeBundleDigest,
	}
	extraEnv = append(extraEnv, d09NamespaceEnvironment(hostNamespaces)...)
	entrypointArgs, probeEnv := d09WorkerProbeLaunch(config, request.Args)
	extraEnv = append(extraEnv, probeEnv...)
	invocation := d09ContainerArgumentsAtWithEnv(config, name, report.ImageRef, PiWorkerCellEnvironment(), extraEnv, entrypointArgs, profile.WorkingDirectory, mounts, cidPath)
	command := exec.CommandContext(ctx, report.PodmanPath, invocation...)
	launchCmd, launchName, launchPodman = command, name, report.PodmanPath
	command.Env = l.commandEnvironment()
	command.Stdin = request.Stdin
	command.Stderr = request.Stderr
	command.ExtraFiles = extraFiles
	// StartVerified is the D09 Pi-worker path. Preserve this exact command's
	// JSONL stdout for its Process; it is transferred only after proof validation.
	stdoutPipe, err = attachOwnedStdout(command)
	if err != nil {
		_ = proofWrite.Close()
		return nil, D09TCBProof{}, fmt.Errorf("capture D09 Pi RPC stdout: %w", err)
	}
	startErr := stdoutPipe.Start()
	if command.Process != nil {
		// Keep the reader reachable even before the entrypoint proof arrives.
		// If proof or settlement becomes uncertain, this record retains ownership.
		l.storePendingLaunch(command, stdoutPipe.reader)
	}
	if startErr != nil {
		_ = proofWrite.Close()
		return nil, D09TCBProof{}, fmt.Errorf("start D09 rootless Podman/crun cell: %w", startErr)
	}
	_ = proofWrite.Close()
	// Everything below waits on podman or on the cell it starts. If podman exits first
	// (a failed mount, a refused image), nothing will ever answer, so each wait also ends
	// as soon as the podman process has exited instead of running to a deadline.
	watchCtx, stopWatch := contextEndingWithProcess(ctx, command.Process.Pid)
	defer stopWatch()
	cid, err := readD09CIDFifo(watchCtx, cidFile, d09CommandStartTimeout)
	if err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("observe D09 worker container id: %w", errors.Join(err, context.Cause(watchCtx)))
	}
	inspectCtx, cancelInspect := context.WithTimeout(watchCtx, d09WorkerProofTimeout)
	containerState, err := inspectContainer(inspectCtx, report.PodmanPath, cid, l.commandEnvironment())
	cancelInspect()
	if err != nil || containerState.State.Status != "running" || containerState.State.PID <= 0 ||
		strings.TrimPrefix(containerState.Name, "/") != name {
		return nil, D09TCBProof{}, errors.Join(fmt.Errorf("D09 worker container identity/state is not running as the requested cell"), err)
	}
	// Pin the leader before anything else reads it: the cell can exit at any moment, and the pidfd both
	// survives that and reports it. It becomes the worker pidfd on success.
	pidfd, err := openPidfd(containerState.State.PID)
	if err != nil {
		if exited := d09ExitedBeforeAdmission(ctx, report.PodmanPath, cid, l.commandEnvironment(), -1, command.Process.Pid, err); exited != nil {
			return nil, D09TCBProof{}, exited
		}
		return nil, D09TCBProof{}, fmt.Errorf("entrypoint leader PID %d has no host pidfd: %w", containerState.State.PID, err)
	}
	defer func() {
		if !launchSucceeded {
			_ = syscall.Close(pidfd)
		}
	}()
	startupProofCtx, cancelStartupProof := context.WithTimeout(watchCtx, d09WorkerProofTimeout)
	proof, err := readD09TCBProofContext(startupProofCtx, proofRead)
	cancelStartupProof()
	if err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("captured TCB proof is missing or invalid: %w", errors.Join(err, context.Cause(watchCtx)))
	}
	proof.ContainerName = name
	proof.LauncherProcessPID = command.Process.Pid
	proof.LeaderPID = containerState.State.PID
	proof.ImageRef = report.ImageRef
	proof.ImageDigest = report.ImageDigest
	proof.TemplateDigest = config.TemplateDigest
	if sourceEvidence := request.admission.runtime.D09SourceEvidence; request.admission.runtime.D09Source != nil {
		proof.SourceGenerationID = sourceEvidence.GenerationID
		proof.SourceTreeDigest = sourceEvidence.TreeDigest
		proof.SourceManifestDigest = sourceEvidence.ManifestDigest
		proof.SourceBindingDigest = sourceEvidence.SourceBindingDigest
		proof.SourceProvenanceDigest = sourceEvidence.ProvenanceDigest
		proof.SourceIdentityDigest = sourceEvidence.SourceIdentityDigest
		proof.SourceStoreAuditHead = sourceEvidence.StoreAuditHead
		proof.SourceStoreAuditCount = sourceEvidence.StoreAuditRecordCount
		proof.SourceDevice = sourceEvidence.SourceDevice
		proof.SourceInode = sourceEvidence.SourceInode
		proof.SourceMountID = sourceEvidence.SourceMountID
		proof.ConsumerWorkerMountID = proof.WorkerMountID
		proof.ConsumerRuntimeMountID = proof.RuntimeMountID
	}
	if proof.ContainerLeaderPID != 1 {
		return nil, D09TCBProof{}, errors.New("D09 worker entrypoint is not PID1 inside the private namespace")
	}
	if err := verifyD09HostProcessIDMaps(proof.LeaderPID, proof.UserNSInode, config,
		proof.UIDMap, proof.GIDMap); err != nil {
		if exited := d09ExitedBeforeAdmission(ctx, report.PodmanPath, cid, l.commandEnvironment(), pidfd, command.Process.Pid, err); exited != nil {
			return nil, D09TCBProof{}, exited
		}
		return nil, D09TCBProof{}, fmt.Errorf("verify exact host-visible D09 uid/gid map before launch admission: %w", err)
	}
	if !proof.ConsumerMountsVerified || !proof.RawSourceFDsClosed ||
		proof.WorkerDevice != workerDevice || proof.WorkerInode != workerInode ||
		proof.RuntimeDevice != runtimeEvidence.Device || proof.RuntimeInode != runtimeEvidence.Inode ||
		proof.WorkerMountID == 0 || proof.RuntimeMountID == 0 {
		return nil, D09TCBProof{}, fmt.Errorf("%w: consumer-view mount proof does not match the verified D06/runtime handles", ErrHostBindingsMissing)
	}
	if request.admission.runtime.D09Source != nil {
		if proof.ConsumerWorkerMountID != proof.WorkerMountID || proof.ConsumerRuntimeMountID != proof.RuntimeMountID ||
			proof.SourceGenerationID != request.admission.runtime.D09SourceEvidence.GenerationID ||
			proof.SourceTreeDigest != request.admission.runtime.D09SourceEvidence.TreeDigest ||
			proof.SourceManifestDigest != request.admission.runtime.D09SourceEvidence.ManifestDigest ||
			proof.SourceBindingDigest != request.admission.runtime.D09SourceEvidence.SourceBindingDigest ||
			proof.SourceProvenanceDigest != request.admission.runtime.D09SourceEvidence.ProvenanceDigest ||
			proof.SourceIdentityDigest != request.admission.runtime.D09SourceEvidence.SourceIdentityDigest ||
			proof.SourceStoreAuditHead != request.admission.runtime.D09SourceEvidence.StoreAuditHead ||
			proof.SourceStoreAuditCount != request.admission.runtime.D09SourceEvidence.StoreAuditRecordCount ||
			proof.SourceDevice != request.admission.runtime.D09SourceEvidence.SourceDevice ||
			proof.SourceInode != request.admission.runtime.D09SourceEvidence.SourceInode ||
			proof.SourceMountID != request.admission.runtime.D09SourceEvidence.SourceMountID {
			return nil, D09TCBProof{}, fmt.Errorf("%w: host source evidence and entrypoint consumer mount proof are not distinct and bound", ErrHostBindingsMissing)
		}
	}
	if err := validateD09NamespaceProof(proof, config.HostNamespaces); err != nil {
		return nil, D09TCBProof{}, err
	}
	contains, err := l.scope.ContainsPID(proof.LeaderPID)
	if err != nil || !contains {
		return nil, D09TCBProof{}, errors.Join(fmt.Errorf("%w: entrypoint leader is not in the held D09 cgroup scope", ErrProductionLauncherUnavailable), err)
	}
	// A leader that exited during the checks above must not be admitted.
	if pidfdReadable(pidfd) {
		return nil, D09TCBProof{}, d09ExitedBeforeAdmission(ctx, report.PodmanPath, cid, l.commandEnvironment(), pidfd, command.Process.Pid,
			errors.New("leader pidfd reports exit after the admission checks"))
	}
	if err := validateD09TCBProof(proof, config); err != nil {
		return nil, D09TCBProof{}, err
	}
	if err := validateD09RouteProbeProof(proof, config); err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("%w: %v", ErrProductionLauncherUnavailable, err)
	}
	l.storeLaunchProof(command, proof, config.HostNamespaces, os.NewFile(uintptr(pidfd), "d09-worker-pidfd"), stdoutPipe.reader)
	launchSucceeded = true
	return command, proof, nil
}

// descriptorPathForMount returns the canonical absolute path the held directory
// descriptor resolves to, for use as a Podman bind-mount source. It is not a
// /proc/<pid>/fd path: rootless Podman re-execs into its own user namespace, from
// which the service's /proc/<pid>/fd fails the ptrace access check.
//
// A path can be swapped between this resolution and the mount, so the path is not
// the authority. The cell entrypoint compares the device and inode of the directory
// it actually received with TBOUND_WORKER_DEVICE/INODE and TBOUND_RUNTIME_DEVICE/INODE
// (command cells: TBOUND_COMMAND_VIEW_DEVICE/INODE), which the launcher takes from
// the held descriptors, and refuses to start otherwise (cmd/tbound-cell-entrypoint,
// "consumer mount identities differ"). That check covers both the worker exposure and
// the runtime mount, so a swap fails closed before the proof is written.
func descriptorPathForMount(file *os.File) (string, error) {
	if file == nil {
		return "", ErrHostBindingsMissing
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return "", ErrHostBindingsMissing
	}
	link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), file.Fd()))
	if err != nil || !filepath.IsAbs(link) || filepath.Clean(link) != link || strings.HasSuffix(link, " (deleted)") ||
		strings.ContainsAny(link, ",\n\x00") {
		return "", ErrHostBindingsMissing
	}
	return link, nil
}

// storeLaunchProof registers the captured, validated proof against the exact
// launched command so it can be retrieved once and never for a different launch.
func (l *D09PodmanCrunLauncher) storeLaunchProof(cmd *exec.Cmd, proof D09TCBProof, hostNamespaces D09NamespaceInodes, pidfd *os.File, stdout io.ReadCloser) {
	if l == nil || cmd == nil {
		return
	}
	l.proofMu.Lock()
	defer l.proofMu.Unlock()
	if l.proofs == nil {
		l.proofs = make(map[*exec.Cmd]*d09LaunchRecord)
	}
	record := l.proofs[cmd]
	if record == nil {
		record = &d09LaunchRecord{}
		l.proofs[cmd] = record
	}
	record.proof = proof
	record.hostNamespaces = hostNamespaces
	record.pidfd = pidfd
	record.stdout = stdout
}

func (l *D09PodmanCrunLauncher) storePendingLaunch(cmd *exec.Cmd, stdout io.ReadCloser) {
	if l == nil || cmd == nil {
		return
	}
	l.proofMu.Lock()
	defer l.proofMu.Unlock()
	if l.proofs == nil {
		l.proofs = make(map[*exec.Cmd]*d09LaunchRecord)
	}
	record := l.proofs[cmd]
	if record == nil {
		record = &d09LaunchRecord{}
		l.proofs[cmd] = record
	}
	record.stdout = stdout
}

// processStdoutFor returns a reader only after the proof for this exact
// command has been taken by StartChild.
func (l *D09PodmanCrunLauncher) processStdoutFor(cmd *exec.Cmd) io.ReadCloser {
	if l == nil || cmd == nil {
		return nil
	}
	l.proofMu.Lock()
	defer l.proofMu.Unlock()
	record := l.proofs[cmd]
	if record == nil || !record.proofRead {
		return nil
	}
	return record.stdout
}

// TakeLaunchProof returns the captured, validated TCB proof exactly once for
// this launched command. The per-command settlement record is retained until
// its own settler consumes it. A proof is never returned for a different
// command, and a missing proof returns false (never a stale global value). It
// satisfies the generic launch-proof accessor used by StartChild.
func (l *D09PodmanCrunLauncher) TakeLaunchProof(cmd *exec.Cmd) (any, bool) {
	if l == nil || cmd == nil {
		return nil, false
	}
	l.proofMu.Lock()
	defer l.proofMu.Unlock()
	record, ok := l.proofs[cmd]
	if !ok || record.proofRead {
		return nil, false
	}
	record.proofRead = true
	return record.proof, true
}

// DescendantSettlerFor binds settlement to the exact command whose proof was
// captured. No global LastProof or unrelated process can select the leader PID.
func (l *D09PodmanCrunLauncher) DescendantSettlerFor(cmd *exec.Cmd) (DescendantSettler, bool) {
	if l == nil || cmd == nil {
		return nil, false
	}
	l.proofMu.Lock()
	defer l.proofMu.Unlock()
	if _, ok := l.proofs[cmd]; !ok {
		return nil, false
	}
	return d09LaunchSettler{launcher: l, command: cmd}, true
}

// ValidateLaunchProof validates the exact proof returned for one production
// child against its signed profile before StartChild publishes a Process.
func (l *D09PodmanCrunLauncher) ValidateLaunchProof(value any, profile HostProfile, cmd *exec.Cmd) error {
	proof, ok := value.(D09TCBProof)
	if !ok || l == nil || cmd == nil || cmd.Process == nil || profile.Mode != ModeProduction || proof.ContainerName == "" ||
		proof.LeaderPID <= 0 || !validDigest(proof.ImageDigest) ||
		proof.LauncherProcessPID != cmd.Process.Pid || proof.TemplateDigest != profile.ContainmentProfileDigest {
		return fmt.Errorf("%w: D09 launch proof is absent, malformed, or bound to another profile", ErrProductionLauncherUnavailable)
	}
	if proof.TemplateDigest != l.config.TemplateDigest || proof.ImageDigest != l.config.ExpectedImageDigest {
		return fmt.Errorf("%w: D09 launch proof does not match the launcher's registered template/image", ErrProductionLauncherUnavailable)
	}
	if err := validateD09TCBProof(proof, l.config.normalized()); err != nil {
		return err
	}
	// StartVerified captured the host namespaces on its own copy of the config and kept them with
	// this launch's record; the launcher's base config never holds them.
	host := l.config.HostNamespaces
	if record := l.launchRecord(cmd); record != nil && record.hostNamespaces != (D09NamespaceInodes{}) {
		host = record.hostNamespaces
	}
	return validateD09NamespaceProof(proof, host)
}

type d09LaunchSettler struct {
	launcher *D09PodmanCrunLauncher
	command  *exec.Cmd
}

func (s d09LaunchSettler) Settle(ctx context.Context) (bool, error) {
	if s.launcher == nil || s.command == nil || ctx == nil {
		return false, ErrProductionLauncherUnavailable
	}
	s.launcher.proofMu.Lock()
	record := s.launcher.proofs[s.command]
	if record == nil || !record.proofRead || record.settled {
		s.launcher.proofMu.Unlock()
		return false, fmt.Errorf("%w: matching per-launch TCB proof/leader PID is absent", ErrProductionLauncherUnavailable)
	}
	record.settled = true // reserve this exact record against concurrent settlers
	s.launcher.proofMu.Unlock()
	if record.proof.LeaderPID <= 0 {
		return false, fmt.Errorf("%w: matching per-launch TCB proof has no leader PID", ErrProductionLauncherUnavailable)
	}
	evidence, err := s.launcher.settleCellWithPidfd(ctx, record.proof.LeaderPID, record.pidfd)
	if err != nil {
		s.launcher.proofMu.Lock()
		record.settled = false
		s.launcher.proofMu.Unlock()
		return false, err
	}
	if !evidence.PidfdObserved || !evidence.CgroupKilled || !evidence.PopulatedZero {
		s.launcher.proofMu.Lock()
		record.settled = false
		s.launcher.proofMu.Unlock()
		return false, fmt.Errorf("%w: per-launch pidfd/cgroup settlement evidence is incomplete", ErrProductionLauncherUnavailable)
	}
	record.ownerMu.Lock()
	defer record.ownerMu.Unlock()
	s.launcher.proofMu.Lock()
	if s.launcher.proofs[s.command] != record {
		s.launcher.proofMu.Unlock()
		return false, ErrUnknownSettlement
	}
	if closeErr := closeLaunchPidfdLocked(record); closeErr != nil {
		record.settled = false
		s.launcher.proofMu.Unlock()
		return false, errors.Join(ErrUnknownSettlement,
			fmt.Errorf("close D09 launch pidfd: %w", closeErr))
	}
	delete(s.launcher.proofs, s.command)
	s.launcher.launchInProg = false
	s.launcher.proofMu.Unlock()
	return true, nil
}

// readD09TCBProof reads the single strict-JSON proof line the entrypoint writes on fd10, then
// closes the host's read end so nothing written later is parsed and later writers get EPIPE.
//
// It does not wait for EOF. podman's monitor (conmon) holds its own copy of every --preserve-fds
// descriptor for the whole container lifetime, so the pipe never reaches EOF while the cell runs.
// The guarantee does not rest on EOF: the entrypoint closes fd10 in the cell before it execs Node
// (a close error is fatal there), so Node never holds the descriptor, and the only other holder is
// the trusted conmon, which never writes to it. A byte already buffered behind the proof line is
// still refused.
func readD09TCBProof(reader io.Reader) (D09TCBProof, error) {
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	buffered := bufio.NewReaderSize(reader, 4096)
	raw, err := buffered.ReadSlice('\n')
	if err != nil {
		return D09TCBProof{}, fmt.Errorf("read bounded entrypoint TCB proof line: %w", err)
	}
	line := strings.TrimSpace(string(raw))
	if line == "" || len(line) > 4095 {
		return D09TCBProof{}, errors.New("entrypoint wrote no TCB proof")
	}
	if err := protocol.ValidateStrictJSON([]byte(line)); err != nil {
		return D09TCBProof{}, fmt.Errorf("entrypoint TCB proof is not strict JSON: %w", err)
	}
	var proof D09TCBProof
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil {
		return D09TCBProof{}, fmt.Errorf("decode TCB proof: %w", err)
	}
	if buffered.Buffered() != 0 {
		return D09TCBProof{}, errors.New("entrypoint proof pipe carried data after the proof line")
	}
	return proof, nil
}

func readD09TCBProofContext(ctx context.Context, reader io.Reader) (D09TCBProof, error) {
	if ctx == nil || reader == nil {
		return D09TCBProof{}, ErrProductionLauncherUnavailable
	}
	type result struct {
		proof D09TCBProof
		err   error
	}
	completed := make(chan result, 1)
	go func() {
		proof, err := readD09TCBProof(reader)
		completed <- result{proof: proof, err: err}
	}()
	select {
	case value := <-completed:
		return value.proof, value.err
	case <-ctx.Done():
		return D09TCBProof{}, ctx.Err()
	}
}

// validateD09TCBProof checks every measured postcondition against the registered
// template. A profile label alone is never accepted as evidence.
func validateD09TCBProof(proof D09TCBProof, config D09Config) error {
	if proof.Schema != "tbound-pi-entrypoint/v1" || proof.UID != config.UID || proof.GID != config.GID ||
		proof.CapEff != d09EmptyCapabilitySet || !proof.NoNewPrivs || proof.Seccomp != 2 ||
		proof.LandlockABI != config.LandlockABI || !proof.LandlockTSYNC ||
		proof.LandlockRulesetDigest != config.LandlockRulesetDigest || !proof.CgroupV2 ||
		proof.UserNamespaceMapDigest != config.UserNamespaceMappingDigest ||
		VerifyD09InsideIDMaps(config.UserNamespaceMappingProfile, config.UserNamespaceMappingDigest,
			config.CellUID, config.CellGID, config.UserNamespaceHostUID, config.UserNamespaceHostGID,
			[]byte(proof.UIDMap), []byte(proof.GIDMap)) != nil ||
		proof.ContainerName == "" || proof.LeaderPID <= 0 || proof.ContainerLeaderPID != 1 ||
		proof.ImageDigest != config.ExpectedImageDigest || proof.TemplateDigest != config.TemplateDigest ||
		!strings.HasSuffix(proof.ImageRef, "@"+config.ExpectedImageDigest) ||
		!proof.ConsumerMountsVerified || !proof.RawSourceFDsClosed ||
		proof.WorkerMountID == 0 || proof.RuntimeMountID == 0 ||
		(proof.ConsumerWorkerMountID != 0 && proof.ConsumerWorkerMountID != proof.WorkerMountID) ||
		(proof.ConsumerRuntimeMountID != 0 && proof.ConsumerRuntimeMountID != proof.RuntimeMountID) ||
		proof.WorkerDevice == 0 || proof.WorkerInode == 0 || proof.RuntimeDevice == 0 || proof.RuntimeInode == 0 {
		return fmt.Errorf("%w: TCB proof does not match the registered template", ErrProductionLauncherUnavailable)
	}
	return nil
}

func validateD09NamespaceProof(proof D09TCBProof, host D09NamespaceInodes) error {
	if host.User == 0 || host.Mount == 0 || host.PID == 0 || host.IPC == 0 || host.UTS == 0 || host.Network == 0 ||
		proof.UserNSInode == 0 || proof.MountNSInode == 0 || proof.PIDNSInode == 0 || proof.IPCNSInode == 0 ||
		proof.UTSNSInode == 0 || proof.NetworkNSInode == 0 || !proof.NoExternalRoutes ||
		proof.UserNSInode == host.User || proof.MountNSInode == host.Mount || proof.PIDNSInode == host.PID ||
		proof.IPCNSInode == host.IPC || proof.UTSNSInode == host.UTS || proof.NetworkNSInode == host.Network {
		return fmt.Errorf("%w: container namespace identities or network isolation do not match the registered private namespace profile", ErrProductionLauncherUnavailable)
	}
	return nil
}

// validateD09LaunchRequest binds an ExecutableLaunchRequest to the admitted
// profile and the verified sealed exposure. Shape checks are not authority: the
// exposure evidence must be re-verified, the descriptor identities re-checked,
// and every digest compared with the signed profile.
func (l *D09PodmanCrunLauncher) validateD09LaunchRequest(ctx context.Context, request ExecutableLaunchRequest) (HostProfile, error) {
	if ctx == nil {
		return HostProfile{}, ErrHostBindingsMissing
	}
	if request.admission == nil || !request.admission.valid() || request.admission.profile.Mode != ModeProduction {
		return HostProfile{}, ErrChildCapabilityMissing
	}
	profile := request.admission.profile
	runtime := request.admission.runtime
	if request.ProfileDigest != profile.Digest || request.Path != profile.ChildExecutable || request.Digest != profile.ExecutableDigest ||
		request.NodeVersion != profile.NodeVersion || request.ArgumentsDigest != profile.ChildArgumentsDigest ||
		request.EnvironmentDigest != profile.ChildEnvironmentDigest || request.WorkingDirectoryDigest != profile.WorkingDirectoryDigest ||
		request.Dir != profile.WorkingDirectory || request.StdinDigest != profile.StdinDigest || request.StdinProfile != profile.StdinProfile ||
		request.SourceDigest != profile.SourceProvenanceDigest || request.DescriptorProfile != profile.DescriptorProfile ||
		request.DescriptorProfileDigest != profile.DescriptorProfileDigest || request.PiRuntimeBundleRoot != profile.PiRuntimeBundleRoot ||
		request.PiRuntimeBundleDigest != profile.PiRuntimeBundleDigest || request.ContainmentProfile != profile.ContainmentProfile ||
		request.SettlementProfile != profile.SettlementProfile || !validDigest(request.Digest) ||
		!validDigest(request.ArgumentsDigest) || !validDigest(request.EnvironmentDigest) ||
		!validDigest(request.WorkingDirectoryDigest) || !validDigest(request.StdinDigest) || !validDigest(request.DescriptorProfileDigest) {
		return HostProfile{}, ErrHostProfileMismatch
	}
	if request.DescriptorProfile != PiWorkerDescriptorProfile ||
		request.DescriptorProfileDigest != DigestBytes([]byte(PiWorkerDescriptorProfile)) ||
		DigestArguments(request.Args) != request.ArgumentsDigest ||
		DigestEnvironment(request.Env) != request.EnvironmentDigest ||
		DigestBytes([]byte(request.Dir)) != request.WorkingDirectoryDigest {
		return HostProfile{}, ErrHostProfileMismatch
	}
	if !equalStrings(request.Args, PiWorkerCellArguments()) {
		return HostProfile{}, ErrHostProfileMismatch
	}
	workerSourceHandle := request.WorkerExposureHandle
	if runtime.D09Source != nil {
		workerSourceHandle = request.D09SourceHandle
		if request.WorkerExposureHandle != nil || runtime.WorkerExposureHandle != nil ||
			!sameBinding(workerSourceHandle, runtime.D09SourceHandle) {
			return HostProfile{}, fmt.Errorf("%w: D09 source staging handle is not the capability-bound fd4", ErrHostBindingsMissing)
		}
		if err := verifyD09HostRuntimeSources(ctx, runtime); err != nil {
			return HostProfile{}, err
		}
	} else {
		if workerSourceHandle == nil || runtime.WorkerExposureHandle == nil ||
			!sameBinding(workerSourceHandle, runtime.WorkerExposureHandle) {
			return HostProfile{}, fmt.Errorf("%w: D09 launch requires the capability-bound sealed consumer-view handle", ErrHostBindingsMissing)
		}
		if runtime.WorkerExposureEvidence.validateShape() != nil {
			return HostProfile{}, fmt.Errorf("%w: sealed worker exposure evidence is incomplete", ErrHostBindingsMissing)
		}
		if err := runtime.WorkerExposure.VerifyWorkerExposure(context.Background(), runtime.WorkerExposureEvidence); err != nil {
			return HostProfile{}, fmt.Errorf("reverify sealed worker exposure and D06 provenance: %w", err)
		}
		if err := validateWorkerExposureDescriptor(workerSourceHandle, runtime.WorkerExposureEvidence); err != nil {
			return HostProfile{}, err
		}
	}
	if request.PiRuntimeBundleHandle == nil || runtime.PiRuntimeBundleHandle == nil ||
		!sameBinding(request.PiRuntimeBundleHandle, runtime.PiRuntimeBundleHandle) {
		return HostProfile{}, fmt.Errorf("%w: D09 launch requires the capability-bound runtime bundle handle", ErrHostBindingsMissing)
	}
	assetEvidence := runtime.RuntimeAssetEvidence
	if runtime.D09Source != nil {
		assetEvidence = runtime.D09RuntimeAssetEvidence
	} else if err := runtime.WorkerRuntimeAssets.VerifyWorkerRuntimeAssets(context.Background(), assetEvidence); err != nil {
		return HostProfile{}, fmt.Errorf("reverify runtime asset source: %w", err)
	}
	if err := validateRuntimeAssetDescriptor(request.PiRuntimeBundleHandle, assetEvidence); err != nil {
		return HostProfile{}, err
	}
	if !sameInheritedDescriptors(request.Descriptors, runtime.Descriptors) {
		return HostProfile{}, ErrHostBindingsMissing
	}
	if err := validatePiWorkerDescriptors(profile.DescriptorProfile, request.Descriptors, workerSourceHandle, request.PiRuntimeBundleHandle); err != nil {
		return HostProfile{}, err
	}
	if DigestEnvironment(PiWorkerCellEnvironment()) != profile.ChildEnvironmentDigest {
		return HostProfile{}, fmt.Errorf("%w: fixed in-cell environment differs from the signed profile", ErrHostProfileMismatch)
	}
	return profile, nil
}

// d09ContainerArgumentsAtWithEnv builds the exact hardened `podman run` vector for one
// worker cell. `--preserve-fds 8` passes fd3 exec, fd4 exposure, fd5 bootstrap,
// fd6 IPC, fd7 provider, fd8 events, fd9 bundle, and fd10 TCB proof. The verified
// Go-held D06/runtime consumer descriptors are bound read-only at their fixed
// in-container destinations; the entrypoint verifies same-object identity and
// mount IDs then replaces raw source descriptors with consumer-mount handles.
// No private-Git/admin/store-root descriptor or arbitrary caller path reaches
// the worker. `--cgroup-parent` names the path resolved from the held scope.
func d09ContainerArgumentsAtWithEnv(config D09Config, name, imageRef string, environment, extraEnv, arguments []string, workdir string, mounts []string, cidPath string) []string {
	uid := strconv.Itoa(config.UID)
	gid := strconv.Itoa(config.GID)
	entrypoint := strings.TrimSpace(config.EntrypointPath)
	if entrypoint == "" {
		entrypoint = d09EntrypointPath
	}
	vector := []string{
		"run", "--name", name,
		// Pi runs --mode rpc and reads its requests on stdin, which podman forwards only with
		// --interactive. No -t: stdin is a pipe, not a terminal.
		"--interactive",
		"--runtime", config.CrunPath,
		"--pull=never",
		"--network=none",
		"--pid=private",
		"--ipc=private",
		"--uts=private",
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt", "no-new-privileges",
		"--security-opt", "seccomp=" + config.SeccompProfilePath,
		"--pids-limit", strconv.Itoa(config.PidsMax),
		"--memory", strconv.Itoa(config.MemoryMB) + "m",
		"--cpus", config.CPUs,
		"--uidmap", strconv.Itoa(config.CellUID) + ":0:1",
		"--gidmap", strconv.Itoa(config.CellGID) + ":0:1",
		"--user", uid + ":" + gid,
		"--workdir", workdir,
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777",
		"--preserve-fds", strconv.Itoa(d09PreservedFds),
	}
	if strings.TrimSpace(cidPath) != "" {
		vector = append(vector, "--cidfile", cidPath)
	}
	vector = append(vector, "--rm")
	if strings.TrimSpace(config.CgroupsPath) != "" {
		vector = append(vector, "--cgroup-parent", d09PodmanCgroupParentOrSelf(config.CgroupsPath))
	}
	for _, item := range d09EnvironmentArguments(environment) {
		vector = append(vector, "-e", item)
	}
	// Helper variables are consumed and stripped by the entrypoint before it
	// execs Node, so Node sees exactly the four signed environment fields.
	vector = append(vector,
		"-e", "TBOUND_CELL_UID="+uid,
		"-e", "TBOUND_CELL_GID="+gid,
		"-e", "TBOUND_USERNS_MAP_PROFILE="+config.UserNamespaceMappingProfile,
		"-e", "TBOUND_USERNS_MAP_DIGEST="+config.UserNamespaceMappingDigest,
		"-e", "TBOUND_USERNS_HOST_UID="+strconv.Itoa(config.UserNamespaceHostUID),
		"-e", "TBOUND_USERNS_HOST_GID="+strconv.Itoa(config.UserNamespaceHostGID),
		"-e", "TBOUND_LANDLOCK_ABI="+strconv.Itoa(config.LandlockABI),
		"-e", "TBOUND_LANDLOCK_RULESET_DIGEST="+config.LandlockRulesetDigest,
		"-e", "TBOUND_LANDLOCK_RO_PATH="+config.LandlockROPath,
		"-e", "TBOUND_LANDLOCK_RW_PATH="+config.LandlockRWPath,
	)
	for _, item := range extraEnv {
		vector = append(vector, "-e", item)
	}
	for _, mount := range mounts {
		vector = append(vector, "--mount", mount)
	}
	// The image declares ENTRYPOINT, and podman runs ENTRYPOINT plus the trailing words.
	// Naming the verified entrypoint path explicitly pins it regardless of the image config,
	// and leaves argv[1] as the held fd3 binary the entrypoint requires.
	vector = append(vector, "--entrypoint", entrypoint, imageRef, "/proc/self/fd/3")
	return append(vector, arguments...)
}

// d09EnvironmentArguments renders the fixed in-cell environment deterministically.
func d09EnvironmentArguments(environment []string) []string {
	copyEnv := append([]string(nil), environment...)
	sort.Strings(copyEnv)
	return copyEnv
}

// verifyPinnedToolBinary verifies a pinned host tool is the exact trusted
// root-owned, non-writable, read-only-mount binary with the recorded digest. A
// user-PATH binary, a symlink, a writable inode, or a digest mismatch fails.
func verifyPinnedToolBinary(path, digest string) error {
	if !filepath.IsAbs(path) || !validDigest(digest) {
		return errors.New("pinned tool path and digest are required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("pinned tool must be a non-symlink, non-group/world-writable regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("pinned tool must be root-owned")
	}
	if err := requireReadOnlyMount(path); err != nil {
		return err
	}
	observed, err := hashProtectedFile(path, 512<<20)
	if err != nil {
		return err
	}
	if observed != digest {
		return fmt.Errorf("pinned tool digest mismatch: got %s want %s", observed, digest)
	}
	return nil
}

// d09FixedPath is the PATH of every D09 host command, thesis and product alike.
const d09FixedPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// d09ProbeEnv is a sanitized, proxy-free environment for host tooling: no
// provider credentials, no live-workspace paths, and no proxy variables that
// could redirect an offline verification at the network.
func d09ProbeEnv() []string {
	allow := []string{
		"HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME",
		"XDG_DATA_HOME", "TMPDIR", "LANG", "LC_ALL", "DBUS_SESSION_BUS_ADDRESS",
	}
	env := []string{"PATH=" + d09FixedPath}
	for _, key := range allow {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	// The managed service pins its podman configuration in its unit environment.
	// Podman must see exactly those values, or it silently falls back to the
	// default graphroot and network directories; any other inherited value is dropped.
	for key, pinned := range d09PinnedPodmanEnv {
		if value, ok := os.LookupEnv(key); ok && value == pinned {
			env = append(env, key+"="+value)
		}
	}
	if _, ok := os.LookupEnv("XDG_RUNTIME_DIR"); !ok {
		env = append(env, "XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(os.Geteuid()))
	}
	return env
}

func d09LastField(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// D09ChildRole is one fixed child descriptor role and the container fd number
// it is born at.
type D09ChildRole struct {
	Role    DescriptorRole
	ChildFD int
}

// D09ChildRoleMap is the published fixed Go/entrypoint worker descriptor
// contract. Podman preserves fd3-fd10 (8 descriptors); fd3 is the held exec
// binary, fd4-fd9 are these six roles, and fd10 is the dedicated TCB proof pipe.
// The entrypoint verifies each bound consumer mount and replaces fd4/fd9 with
// consumer-mount descriptors before Node exec.
func D09ChildRoleMap() []D09ChildRole {
	return []D09ChildRole{
		{Role: DescriptorWorkerExposure, ChildFD: 4},
		{Role: DescriptorBootstrap, ChildFD: 5},
		{Role: DescriptorProposalIPC, ChildFD: 6},
		{Role: DescriptorProviderBridge, ChildFD: 7},
		{Role: DescriptorWorkerEvents, ChildFD: 8},
		{Role: DescriptorRuntimeBundle, ChildFD: 9},
	}
}

// D09ExecChildFD is the container fd number that holds the digest-verified Node
// executable. The entrypoint execs /proc/self/fd/3; the fd itself is never a
// path the worker can substitute.
const D09ExecChildFD = 3

// D09ProofChildFD is the container fd number of the dedicated TCB proof pipe.
// The entrypoint writes its typed proof there and closes it before Node starts.
const D09ProofChildFD = 10

// ErrD09WorkerExitedBeforeAdmission means the worker cell's leader was gone before launch admission finished.
// Admission is still refused; the message carries the container's final state so the exit is diagnosable.
var ErrD09WorkerExitedBeforeAdmission = errors.New("D09 worker cell exited before launch admission")

// d09ExitedBeforeAdmission returns ErrD09WorkerExitedBeforeAdmission when cause shows the leader is gone,
// either by ENOENT/ESRCH or because its pidfd reports exit; it returns nil while the leader is alive.
// Cells run with --rm, so the container is usually already removed and inspect cannot answer: the primary
// source is the exit status of the 'podman run' process (launcherPID), which exits with the container's.
// It is waited on for a bound without reaping it, so cleanupFailedContainer still owns Wait. The
// secondary source is one short inspect of State.Status, ExitCode, OOMKilled and a bounded State.Error.
// Pi/Node output is never read.
func d09ExitedBeforeAdmission(ctx context.Context, podmanPath, cid string, environment []string, pidfd, launcherPID int, cause error) error {
	if !errors.Is(cause, os.ErrNotExist) && !errors.Is(cause, syscall.ESRCH) && !(pidfd >= 0 && pidfdReadable(pidfd)) {
		return nil
	}
	podmanExit := d09PodmanExitStatus(launcherPID, 3*time.Second)
	inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	state, err := inspectContainer(inspectCtx, podmanPath, cid, environment)
	if err != nil {
		return fmt.Errorf("%w: podman_exit=%s (final container state unavailable: %s): %w", ErrD09WorkerExitedBeforeAdmission,
			podmanExit, d09Printable(err.Error(), 200), cause)
	}
	return fmt.Errorf("%w: podman_exit=%s status=%q exit_code=%d oom_killed=%t error=%q: %w", ErrD09WorkerExitedBeforeAdmission,
		podmanExit, d09Printable(state.State.Status, 32), state.State.ExitCode, state.State.OOMKilled, d09Printable(state.State.Error, 200), cause)
}

// d09PodmanExitStatus waits up to bound for the (unreaped) podman process to exit and reports its code or
// signal, using waitid(WNOWAIT) so the later exec.Cmd.Wait still collects it.
func d09PodmanExitStatus(pid int, bound time.Duration) string {
	if pid <= 0 {
		return "unavailable"
	}
	fd, err := openPidfd(pid)
	if err != nil {
		return "unavailable"
	}
	defer syscall.Close(fd)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(bound.Milliseconds()))
	if err == unix.EINTR {
		n, err = unix.Poll(fds, int(bound.Milliseconds()))
	}
	if err != nil {
		return "unavailable"
	}
	if n <= 0 {
		return "running(after " + bound.String() + ")"
	}
	var info [128]byte // siginfo_t: si_code at 8, si_status at 24
	if _, _, errno := syscall.Syscall6(syscall.SYS_WAITID, 1 /* P_PID */, uintptr(pid), uintptr(unsafe.Pointer(&info[0])),
		syscall.WEXITED|syscall.WNOHANG|0x01000000 /* WNOWAIT */, 0, 0); errno != 0 {
		return "unavailable"
	}
	code, status := *(*int32)(unsafe.Pointer(&info[8])), *(*int32)(unsafe.Pointer(&info[24]))
	if code == 0 { // nothing waitable: no state to report
		return "unavailable"
	}
	if code == 1 { // CLD_EXITED
		return strconv.Itoa(int(status))
	}
	return "signal:" + strconv.Itoa(int(status))
}

// d09Printable keeps printable ASCII only (other bytes become '?'), trims, and bounds the length.
func d09Printable(s string, limit int) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x20 && s[i] < 0x7f {
			out = append(out, s[i])
		} else {
			out = append(out, '?')
		}
	}
	text := strings.TrimSpace(string(out))
	if len(text) > limit {
		text = text[:limit]
	}
	return text
}
