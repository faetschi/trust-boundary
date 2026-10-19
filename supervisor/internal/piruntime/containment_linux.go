//go:build linux

package piruntime

import (
	"bufio"
	"bytes"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"tbound/supervisor/internal/broker/protocol"
)

// This file is the concrete D09 containment backend: a rootless Podman/crun
// cell for the governed Pi worker. It implements the sealed
// ExecutableLauncher.StartVerified seam that StartChild/LaunchPiSDKWorker drive,
// rather than a probe-only report.
//
// HONESTY BOUNDARY. This backend:
//
//   - probes the host and refuses (fail-closed, exact list) when podman/crun/
//     cosign/the pinned local image/the registered template are absent;
//   - binds every launch to the trusted signed profile, the fixed in-cell
//     identity digests, and the verified sealed WorkerExposure descriptor;
//   - launches a real podman/crun container that re-births the worker at the
//     fixed descriptor numbers (fd3 exec, fd4 exposure, fd5 bootstrap, fd6 IPC,
//     fd7 provider, fd8 events, fd9 bundle) and settles through a held,
//     verified LinuxCgroupScope (cgroup.kill + recursive populated=0 + deadline),
//     cleaning up on every start/run error or cancellation with an owned finite
//     context.
//
// It never claims G1. The in-image entrypoint measures the real TCB
// (uid/caps/no_new_privs/seccomp/Landlock/cgroup/namespaces) and a real
// provisioned launch remains externally UNVERIFIED until an operator runs the
// opt-in integration test on a prepared host.

const (
	// D09ContainmentProfile names the frozen rootless Podman/crun containment
	// profile a signed production host profile must select.
	D09ContainmentProfile = "d09-rootless-podman-crun-frozen-cell/v1"
	// D09SettlementProfile names the pidfd/cgroup-kill settlement profile.
	D09SettlementProfile = "d09-pidfd-cgroup-kill-settlement/v1"
	// d09CellImageReference is the local image reference the launcher resolves
	// to a repo digest. It is never pulled.
	d09CellImageReference = "localhost/tbound/pi-cell:latest"
	// d09CellNamePrefix prefixes the unique per-cell container name.
	d09CellNamePrefix = "tbound-pi-cell-"
	// d09PreservedFds is the number of descriptors passed into the container:
	// fd3 exec, fd4 exposure, fd5 bootstrap, fd6 IPC, fd7 provider, fd8 events,
	// fd9 bundle, fd10 dedicated TCB proof pipe.
	d09PreservedFds = 8
	// d09EntrypointPath is the signed, frozen in-image entrypoint that measures
	// the TCB, applies the process-wide Landlock ruleset, verifies the fixed
	// descriptor birth order, and execs fd3.
	d09EntrypointPath = "/usr/local/bin/tbound-pi-entrypoint"
	// d09CleanupTimeout bounds the owned cleanup context used on error paths so
	// a canceled caller context can never skip teardown.
	d09CleanupTimeout     = 20 * time.Second
	d09WorkerProofTimeout = 15 * time.Second

	defaultD09PidsMax  = 256
	defaultD09MemoryMB = 1024
	// d09EmptyCapabilitySet is the measured CapEff of a fully dropped cell.
	d09EmptyCapabilitySet = "0000000000000000"
)

// ErrD09SessionCompositionUnavailable is reserved for the remaining composition
// blocker (wiring the sealed session into the CLI). The backend below no longer
// returns it after a passing probe: it either launches or refuses with a typed
// prerequisite/mismatch error.
var ErrD09SessionCompositionUnavailable = errors.New("governed Pi D09 session composition (sealed D06 exposure, runtime bundle, descriptor role mapping) is not wired")

// D09CommandResult is the outcome of one injected probe command.
type D09CommandResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Err      error
}

// D09ProbeRunner is the injectable host command surface used only for
// probing (podman/crun/cosign/image). The exported production constructor never
// accepts a runner, so no in-process caller can substitute a fake host; tests in
// this package set the unexported field directly.
type D09ProbeRunner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args ...string) D09CommandResult
}

type execD09CommandRunner struct{}

func newExecD09CommandRunner() D09ProbeRunner { return execD09CommandRunner{} }

func (execD09CommandRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (execD09CommandRunner) Run(ctx context.Context, name string, args ...string) D09CommandResult {
	if ctx == nil {
		return D09CommandResult{ExitCode: -1, Err: errors.New("nil context")}
	}
	command := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.Env = d09ProbeEnv()
	if filepath.Base(name) == "cosign" && len(args) >= 1 && args[0] == "verify" {
		// Cosign local-image signature verification receives a private network
		// namespace, independently preventing egress/transparency-log/registry
		// access. If an unprivileged user/network namespace is unavailable, the
		// probe fails closed instead of silently running cosign on the host net.
		command.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
			UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
			GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
			GidMappingsEnableSetgroups: false,
		}
	}
	err := command.Run()
	result := D09CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result
	}
	result.ExitCode = -1
	result.Err = err
	return result
}

// D09Prerequisite is one unmet production containment prerequisite.
type D09Prerequisite struct {
	Name   string
	Detail string
}

func (p D09Prerequisite) String() string {
	if p.Detail == "" {
		return p.Name
	}
	return p.Name + ": " + p.Detail
}

// D09PrerequisiteError lists every missing prerequisite and wraps
// ErrProductionLauncherUnavailable.
type D09PrerequisiteError struct {
	Missing []D09Prerequisite
}

func (e *D09PrerequisiteError) Error() string {
	if e == nil || len(e.Missing) == 0 {
		return ErrProductionLauncherUnavailable.Error()
	}
	items := make([]string, 0, len(e.Missing))
	for _, item := range e.Missing {
		items = append(items, item.String())
	}
	return ErrProductionLauncherUnavailable.Error() + ": missing " + strings.Join(items, "; ")
}

func (e *D09PrerequisiteError) Unwrap() error { return ErrProductionLauncherUnavailable }

// D09Report is the measured probe evidence. A false field means the mechanism
// was not observed.
type D09Report struct {
	PodmanPresent bool
	PodmanPath    string
	PodmanVersion string
	Rootless      bool
	OCIRuntime    string
	CgroupVersion string
	CrunPresent   bool
	CosignPresent bool
	ImageRef      string
	ImageDigest   string
	ImagePinned   bool
	ImageVerified bool
	LandlockABI   int
	Unsupported   []string
}

// D09Config is the frozen launcher configuration, produced only from the
// registered, root-owned D09 cell template. It is data, never a callback.
type D09Config struct {
	Image                 string
	ExpectedImageDigest   string
	CosignPublicKeyPath   string
	CosignPublicKeyDigest string
	OCILayoutPath         string
	SeccompProfilePath    string
	SeccompProfileDigest  string
	LandlockABI           int
	LandlockRulesetDigest string
	LandlockROPath        string
	LandlockRWPath        string
	FrozenCapabilities    string
	EntrypointPath        string
	EntrypointDigest      string
	Runtime               string
	CgroupsPath           string
	CPUs                  string
	PidsMax               int
	MemoryMB              int
	UID                   int
	GID                   int
	// Pinned host tooling. When set, the launcher verifies the exact root-owned,
	// non-writable, read-only-mount binary bytes before any execution and never
	// resolves a user-PATH binary. A real template sets all three.
	PodmanPath   string
	PodmanDigest string
	CrunPath     string
	CrunDigest   string
	CosignPath   string
	CosignDigest string
	// CommandExecutable and CommandExecutableDigest bind the one immutable Bash
	// binary the production command runner may execute in the cell.
	CommandExecutable                 string
	CommandExecutableDigest           string
	ManagedLiveRootRegistrationDigest string
	UserNamespaceMappingProfile       string
	UserNamespaceMappingDigest        string
	// TemplateDigest is the canonical digest of the registered template this
	// config came from; it is attached to every captured TCB proof.
	TemplateDigest               string
	CellUID                      int
	CellGID                      int
	UserNamespaceHostUID         int
	UserNamespaceHostGID         int
	NamespaceProfile             string
	CgroupVersion                string
	CgroupControllers            string
	XattrVisibilityProfileDigest string
	XattrVisibilityAuthority     string
	XattrFilesystemMagic         uint64
	XattrMountOptionsDigest      string
	XattrScanUID                 int
	XattrScanGID                 int
	XattrScanPrivilegeProfile    string
	registeredTemplate           bool
	HostNamespaces               D09NamespaceInodes
	// RouteProbe is the route probe of a route-probe template; the zero value is a production template.
	RouteProbe D09RouteProbe
}

// D09NamespaceInodes are the host namespace identities captured immediately
// before a launch and compared with the entrypoint's consumer-side proof.
type D09NamespaceInodes struct {
	User    uint64
	Mount   uint64
	PID     uint64
	IPC     uint64
	UTS     uint64
	Network uint64
}

func captureD09NamespaceInodes(pid int) (D09NamespaceInodes, error) {
	if pid <= 0 {
		return D09NamespaceInodes{}, errors.New("invalid namespace identity pid")
	}
	values := []*uint64{}
	var ids D09NamespaceInodes
	values = append(values, &ids.User, &ids.Mount, &ids.PID, &ids.IPC, &ids.UTS, &ids.Network)
	for i, name := range []string{"user", "mnt", "pid", "ipc", "uts", "net"} {
		info, err := os.Stat(fmt.Sprintf("/proc/%d/ns/%s", pid, name))
		if err != nil {
			return D09NamespaceInodes{}, fmt.Errorf("stat host %s namespace: %w", name, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Ino == 0 {
			return D09NamespaceInodes{}, fmt.Errorf("host %s namespace inode is unavailable", name)
		}
		*values[i] = stat.Ino
	}
	return ids, nil
}

func d09NamespaceEnvironment(ids D09NamespaceInodes) []string {
	return []string{
		"TBOUND_HOST_NS_USER=" + strconv.FormatUint(ids.User, 10),
		"TBOUND_HOST_NS_MOUNT=" + strconv.FormatUint(ids.Mount, 10),
		"TBOUND_HOST_NS_PID=" + strconv.FormatUint(ids.PID, 10),
		"TBOUND_HOST_NS_IPC=" + strconv.FormatUint(ids.IPC, 10),
		"TBOUND_HOST_NS_UTS=" + strconv.FormatUint(ids.UTS, 10),
		"TBOUND_HOST_NS_NETWORK=" + strconv.FormatUint(ids.Network, 10),
	}
}

func (c D09Config) normalized() D09Config {
	if strings.TrimSpace(c.Image) == "" {
		c.Image = d09CellImageReference
	}
	if strings.TrimSpace(c.Runtime) == "" {
		c.Runtime = "crun"
	}
	if strings.TrimSpace(c.EntrypointPath) == "" {
		c.EntrypointPath = d09EntrypointPath
	}
	if c.CellUID > 0 {
		c.UID = c.CellUID
	} else if c.UID <= 0 {
		c.UID = os.Geteuid()
	}
	if c.CellGID > 0 {
		c.GID = c.CellGID
	} else if c.GID <= 0 {
		c.GID = os.Getegid()
	}
	if c.PidsMax <= 0 {
		c.PidsMax = defaultD09PidsMax
	}
	if c.MemoryMB <= 0 {
		c.MemoryMB = defaultD09MemoryMB
	}
	return c
}

func validateD09CellIdentity(config D09Config) error {
	if !config.registeredTemplate || !validDigest(config.TemplateDigest) || !validDigest(config.ManagedLiveRootRegistrationDigest) {
		return errors.New("D09 launcher config is not bound to the registered root-owned template")
	}
	if config.UserNamespaceMappingProfile != D09UserNamespaceMappingProfile || config.UserNamespaceHostUID <= 0 ||
		config.UserNamespaceHostGID <= 0 || config.UserNamespaceHostUID == config.CellUID ||
		config.UserNamespaceHostGID == config.CellGID || config.UserNamespaceMappingDigest != DigestD09UserNamespaceMapping(
		config.CellUID, config.CellGID, config.UserNamespaceHostUID, config.UserNamespaceHostGID) {
		return errors.New("D09 user namespace mapping does not match the exact signed cell/service UID/GID profile")
	}
	if config.CellUID <= 0 || config.CellGID <= 0 || config.UID != config.CellUID || config.GID != config.CellGID {
		return errors.New("rootless container UID/GID differs from the registered template")
	}
	if config.NamespaceProfile != "user,mount,pid,ipc,uts,network-none/v1" {
		return errors.New("namespace profile is not the frozen private user/mount/pid/ipc/uts/network-none profile")
	}
	if config.CgroupVersion != "v2" || config.CgroupControllers != "cpu,memory,pids" {
		return errors.New("cgroup-v2 cpu/memory/pids controller profile is not registered")
	}
	if !validDigest(config.XattrVisibilityProfileDigest) || config.XattrVisibilityAuthority != D09XattrVisibilityAuthority ||
		config.XattrScanPrivilegeProfile != D09XattrScanPrivilegeProfile ||
		config.XattrScanUID != config.UserNamespaceHostUID || config.XattrScanGID != config.UserNamespaceHostGID {
		return errors.New("registered xattr visibility profile is absent or bound to another scanner uid/gid")
	}
	if os.Geteuid() != config.UserNamespaceHostUID || os.Getegid() != config.UserNamespaceHostGID {
		return errors.New("invoking runner UID/GID differs from the registered managed service account")
	}
	return nil
}

// DefaultD09Config returns the conservative default used only to surface the
// host prerequisites when no registered template exists. Its measured identity
// fields are empty, so Probe reports the missing image/template prerequisite.
func DefaultD09Config(cosignPublicKeyPath string) D09Config {
	return D09Config{
		Image:               d09CellImageReference,
		CosignPublicKeyPath: cosignPublicKeyPath,
		Runtime:             "crun",
	}
}

// D09PodmanCrunLauncher is the concrete rootless Podman/crun cell launcher. The
// runner field is unexported so no external caller can substitute a fake host.
type D09PodmanCrunLauncher struct {
	runner D09ProbeRunner
	config D09Config
	scope  *LinuxCgroupScope

	stateMu                sync.Mutex
	activeOps              int
	pendingCommandReceipts int
	closed                 bool

	proofMu         sync.Mutex
	proofs          map[*exec.Cmd]*d09LaunchRecord
	launchInProg    bool
	commandMu       sync.Mutex
	commandScopes   map[string]*d09OwnedCommandScope
	admissionMu     sync.RWMutex
	admittedProfile HostProfile
	profileAdmitted bool
	managedRootMu   sync.Mutex
	managedRoot     *VerifiedD09ManagedLiveRoot
}

func (l *D09PodmanCrunLauncher) productionRunner() bool {
	if l == nil || l.runner == nil {
		return false
	}
	_, ok := l.runner.(execD09CommandRunner)
	return ok
}

func (l *D09PodmanCrunLauncher) isClosed() bool {
	if l == nil {
		return true
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.closed
}

func (*D09PodmanCrunLauncher) requirePerLaunchProof() {}

type d09LaunchRecord struct {
	proof          D09TCBProof
	pidfd          *os.File
	stdout         io.ReadCloser
	proofRead      bool
	settled        bool
	waitDone       chan struct{}
	waitErr        error
	ownerMu        sync.Mutex
	stdoutClosed   bool
	stdoutCloseErr error
	pidfdCloseErr  error
}

// NewD09PodmanCrunLauncher builds a launcher bound to the real host exec runner.
// A launched cell additionally requires BindCgroupScope; without a verified
// scope the launcher refuses admission and cannot launch.
func NewD09PodmanCrunLauncher(config D09Config) *D09PodmanCrunLauncher {
	return &D09PodmanCrunLauncher{runner: newExecD09CommandRunner(), config: config.normalized()}
}

// BindCgroupScope binds the supervisor-owned, verified cgroup-v2 scope that
// settlement and cleanup act on. It is supplied by the trusted composition root
// from a held delegated-root descriptor; a nil scope is refused.
func (l *D09PodmanCrunLauncher) BindCgroupScope(scope *LinuxCgroupScope) error {
	if l == nil || scope == nil || scope.file == nil || scope.root == nil {
		return ErrCgroupScopeRefused
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed || l.scope != nil {
		return ErrCgroupScopeRefused
	}
	if err := validateCgroupDirectory(scope.file); err != nil {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	if err := enableD09SubtreeControllers(scope.file); err != nil {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	l.scope = scope
	return nil
}

// BindManagedLiveRoot binds the exact registration-verification object C's
// ManagedLiveRootAuthority also consumes. The binding establishes root/service
// policy identity only; it does not grant exclusivity or a quiescence permit.
func (l *D09PodmanCrunLauncher) BindManagedLiveRoot(verified *VerifiedD09ManagedLiveRoot) error {
	if l == nil || verified == nil {
		return ErrD09ManagedLiveRootRefused
	}
	l.stateMu.Lock()
	if l.closed || l.activeOps != 0 {
		l.stateMu.Unlock()
		return ErrD09ManagedLiveRootRefused
	}
	l.managedRootMu.Lock()
	alreadyBound := l.managedRoot != nil
	l.managedRootMu.Unlock()
	if alreadyBound {
		l.stateMu.Unlock()
		return ErrD09ManagedLiveRootRefused
	}
	l.stateMu.Unlock()
	registration := verified.Registration()
	if registration.Digest != l.config.ManagedLiveRootRegistrationDigest {
		return ErrD09ManagedLiveRootRefused
	}
	owned, err := verified.DuplicateCapability(context.Background())
	if err != nil {
		return err
	}
	if err := owned.Revalidate(context.Background(), owned.root); err != nil {
		_ = owned.Close()
		return err
	}
	l.stateMu.Lock()
	if l.closed || l.activeOps != 0 {
		l.stateMu.Unlock()
		_ = owned.Close()
		return ErrD09ManagedLiveRootRefused
	}
	l.managedRootMu.Lock()
	if l.managedRoot != nil {
		l.managedRootMu.Unlock()
		l.stateMu.Unlock()
		_ = owned.Close()
		return ErrD09ManagedLiveRootRefused
	}
	l.managedRoot = owned
	l.config.UserNamespaceHostUID = registration.ServiceUID
	l.config.UserNamespaceHostGID = registration.ServiceGID
	l.managedRootMu.Unlock()
	l.stateMu.Unlock()
	return nil
}

// ManagedLiveRootRegistration returns the currently bound trusted registration
// snapshot. It is policy metadata only, not the CLI's exclusive writer permit.
func (l *D09PodmanCrunLauncher) ManagedLiveRootRegistration() (D09ManagedLiveRootRegistration, bool) {
	if l == nil {
		return D09ManagedLiveRootRegistration{}, false
	}
	l.managedRootMu.Lock()
	defer l.managedRootMu.Unlock()
	if l.managedRoot == nil {
		return D09ManagedLiveRootRegistration{}, false
	}
	return l.managedRoot.Registration(), true
}

func (l *D09PodmanCrunLauncher) ensureManagedLiveRoot(ctx context.Context) (D09ManagedLiveRootRegistration, error) {
	if l == nil || ctx == nil || !l.productionRunner() {
		return D09ManagedLiveRootRegistration{}, ErrD09ManagedLiveRootRefused
	}
	l.managedRootMu.Lock()
	defer l.managedRootMu.Unlock()
	if l.managedRoot != nil {
		if l.managedRoot.registration.Digest != l.config.ManagedLiveRootRegistrationDigest {
			return D09ManagedLiveRootRegistration{}, ErrD09ManagedLiveRootRefused
		}
		if err := l.managedRoot.Revalidate(ctx, l.managedRoot.root); err != nil {
			return D09ManagedLiveRootRegistration{}, err
		}
		return l.managedRoot.Registration(), nil
	}
	registration, err := LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	if registration.Digest != l.config.ManagedLiveRootRegistrationDigest {
		return D09ManagedLiveRootRegistration{}, ErrD09ManagedLiveRootRefused
	}
	root, err := openAbsoluteDirectoryNoSymlinks(registration.RootPath)
	if err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	verified, err := VerifyD09ManagedLiveRoot(ctx, root)
	_ = root.Close()
	if err != nil {
		return D09ManagedLiveRootRegistration{}, err
	}
	defer verified.Close()
	return registration, nil
}

func (l *D09PodmanCrunLauncher) beginOperation() error {
	if l == nil {
		return ErrProductionLauncherUnavailable
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed {
		return ErrProductionLauncherUnavailable
	}
	l.activeOps++
	return nil
}

func (l *D09PodmanCrunLauncher) endOperation() {
	if l == nil {
		return
	}
	l.stateMu.Lock()
	if l.activeOps > 0 {
		l.activeOps--
	}
	l.stateMu.Unlock()
}

func (l *D09PodmanCrunLauncher) retainCommandReceipt() error {
	if l == nil {
		return ErrProductionLauncherUnavailable
	}
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed {
		return ErrProductionLauncherUnavailable
	}
	l.pendingCommandReceipts++
	return nil
}

func (l *D09PodmanCrunLauncher) releaseCommandReceipt() {
	if l == nil {
		return
	}
	l.stateMu.Lock()
	if l.pendingCommandReceipts > 0 {
		l.pendingCommandReceipts--
	}
	l.stateMu.Unlock()
}

// Close deterministically unbinds and closes only the launcher's own duplicate
// of the managed-root registration. It never closes the caller's verified
// authority object or cgroup scope. Close refuses while a launch, proof, or
// command cgroup is active, so the caller must settle/remove every exact cell
// first. The C publication authority can retain its independent permit handle.
func (l *D09PodmanCrunLauncher) Close(ctx context.Context) error {
	if l == nil || ctx == nil {
		return ErrProductionLauncherUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.stateMu.Lock()
	if l.closed {
		l.stateMu.Unlock()
		return nil
	}
	if l.activeOps != 0 || l.pendingCommandReceipts != 0 {
		l.stateMu.Unlock()
		return ErrUnknownSettlement
	}
	l.proofMu.Lock()
	activeProof := l.launchInProg || len(l.proofs) != 0
	l.proofMu.Unlock()
	l.commandMu.Lock()
	activeCommands := len(l.commandScopes) != 0
	l.commandMu.Unlock()
	workerScopeOpen := false
	if l.scope != nil {
		l.scope.mu.Lock()
		workerScopeOpen = !l.scope.closed
		l.scope.mu.Unlock()
	}
	if activeProof || activeCommands || workerScopeOpen {
		l.stateMu.Unlock()
		return ErrUnknownSettlement
	}
	l.closed = true
	l.stateMu.Unlock()
	l.managedRootMu.Lock()
	managedRoot := l.managedRoot
	if managedRoot != nil {
		if err := managedRoot.Close(); err != nil {
			l.managedRootMu.Unlock()
			l.stateMu.Lock()
			l.closed = false
			l.stateMu.Unlock()
			return err
		}
		l.managedRoot = nil
	}
	l.managedRootMu.Unlock()
	l.admissionMu.Lock()
	l.admittedProfile = HostProfile{}
	l.profileAdmitted = false
	l.admissionMu.Unlock()
	return nil
}

// NewCommandScope creates a unique direct-child cgroup of the held, verified
// D09 worker scope. It consumes the authenticated lease/view identity only as
// input to freshness/identity validation; no caller-supplied path is accepted.
func (l *D09PodmanCrunLauncher) NewCommandScope(ctx context.Context, leaseID, viewID string) (*d09OwnedCommandScope, error) {
	return l.newCommandScope(ctx, leaseID, viewID, false, false)
}

// NewDetachedCommandScope is NewCommandScope for the no-reap/v1 command cell: the unique cgroup is a
// direct child of the delegated cells root, a sibling of the worker scope and not below it, so the
// worker scope's recursive cgroup.kill at settlement cannot reach a surviving descendant. The scope
// is never removed by the lease; abandon() hands it over to the operator reset.
func (l *D09PodmanCrunLauncher) NewDetachedCommandScope(ctx context.Context, leaseID, viewID string) (*d09OwnedCommandScope, error) {
	return l.newCommandScope(ctx, leaseID, viewID, true, false)
}

// NewNetSinkCommandScope is NewCommandScope for the net-sink/v1 command cell: the unique cgroup is a
// child of the fixed parent D09NetSinkCgroupName of the cells cgroup, which the guest nftables rule
// matches. The parent must exist and is only opened here, never created or removed. The scope is
// settled and removed by the lease like any other command scope.
func (l *D09PodmanCrunLauncher) NewNetSinkCommandScope(ctx context.Context, leaseID, viewID string) (*d09OwnedCommandScope, error) {
	return l.newCommandScope(ctx, leaseID, viewID, false, true)
}

func (l *D09PodmanCrunLauncher) newCommandScope(ctx context.Context, leaseID, viewID string, detached, netSink bool) (*d09OwnedCommandScope, error) {
	if l == nil || l.scope == nil || ctx == nil || !validIdentity(leaseID) || !validIdentity(viewID) {
		return nil, ErrCgroupScopeRefused
	}
	if err := l.beginOperation(); err != nil {
		return nil, err
	}
	defer l.endOperation()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parentPath, err := l.scope.Path()
	if err != nil {
		return nil, err
	}
	if detached {
		if parentPath, err = cgroupDescriptorPath(l.scope.root); err != nil {
			return nil, err
		}
	}
	name := "tbound-cmd-" + d09NewToken()
	parentDir := l.scope.file
	if detached {
		name = d09DetachedScopePrefix + d09NewToken()
		parentDir = l.scope.root
		if parentDir == nil {
			return nil, ErrCgroupScopeRefused
		}
	}
	if netSink {
		dir, err := l.openNetSinkParent(netSinkLoadAttested)
		if err != nil {
			return nil, err
		}
		defer dir.Close()
		parentDir = dir
		if parentPath, err = cgroupDescriptorPath(dir); err != nil {
			return nil, err
		}
	}
	parentFD, err := syscall.Openat(int(parentDir.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate D09 delegated cgroup root: %w", err)
	}
	parent := os.NewFile(uintptr(parentFD), "d09-command-parent")
	if err := syscall.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("create unique D09 command cgroup: %w", err)
	}
	childFD, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = unlinkatDir(int(parent.Fd()), name)
		_ = parent.Close()
		return nil, err
	}
	child := os.NewFile(uintptr(childFD), "d09-command-scope")
	scope, err := NewLinuxCgroupScope(parent, child, D09ContainmentProfile, D09SettlementProfile)
	if err != nil {
		_ = child.Close()
		_ = parent.Close()
		return nil, errors.Join(err, unlinkatDir(int(parentDir.Fd()), name))
	}
	if err := validateCgroupDirectory(scope.file); err != nil {
		_ = scope.Close()
		_ = unlinkatDir(int(parentDir.Fd()), name)
		return nil, errors.Join(ErrCgroupScopeRefused, err)
	}
	if err := enableD09SubtreeControllers(scope.file); err != nil {
		_ = scope.Close()
		_ = unlinkatDir(int(parentDir.Fd()), name)
		return nil, fmt.Errorf("enable D09 command-scope cpu/memory/pids controllers: %w", err)
	}
	owned := &d09OwnedCommandScope{launcher: l, name: name, scope: scope, leaseID: leaseID, viewID: viewID, parentPath: parentPath, detached: detached}
	l.commandMu.Lock()
	if l.commandScopes == nil {
		l.commandScopes = make(map[string]*d09OwnedCommandScope)
	}
	if _, collision := l.commandScopes[name]; collision {
		l.commandMu.Unlock()
		_ = scope.Close()
		_ = unlinkatDir(int(parentDir.Fd()), name)
		return nil, ErrCgroupScopeRefused
	}
	l.commandScopes[name] = owned
	l.commandMu.Unlock()
	return owned, nil
}

func unlinkatDir(parentFD int, name string) error {
	namePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(parentFD), uintptr(unsafe.Pointer(namePtr)), uintptr(0x200)) // AT_REMOVEDIR
	if errno != 0 {
		return errno
	}
	return nil
}

type d09OwnedCommandScope struct {
	launcher   *D09PodmanCrunLauncher
	name       string
	scope      *LinuxCgroupScope
	leaseID    string
	viewID     string
	parentPath string
	detached   bool

	mu         sync.Mutex
	settled    bool
	reconciled bool
	removed    bool
	removing   bool
	closed     bool
}

func (s *d09OwnedCommandScope) Path() (string, error) {
	if s == nil || s.scope == nil {
		return "", ErrCgroupScopeRefused
	}
	return s.scope.Path()
}

func (s *d09OwnedCommandScope) ContainsPID(pid int) (bool, error) {
	if s == nil || s.scope == nil {
		return false, ErrCgroupScopeRefused
	}
	return s.scope.ContainsPID(pid)
}

func (s *d09OwnedCommandScope) Settle(ctx context.Context) (bool, error) {
	if s == nil || s.scope == nil || ctx == nil {
		return false, ErrCgroupScopeRefused
	}
	settled, err := s.scope.Settle(ctx)
	if err == nil && settled {
		s.mu.Lock()
		s.settled = true
		s.mu.Unlock()
	}
	return settled, err
}

func (s *d09OwnedCommandScope) IsSettled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settled
}

// MarkReconciled records that the caller independently observed container
// removal, leader pidfd exit, and detached command-view mount references in
// addition to this scope's populated=0. Without this exact cleanup proof Close
// refuses and the scope remains retained for reconciliation.
func (s *d09OwnedCommandScope) MarkReconciled() error {
	if s == nil {
		return ErrCgroupScopeRefused
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.settled || s.closed {
		return ErrUnknownSettlement
	}
	s.reconciled = true
	return nil
}

// Close removes the scope only after its held cgroup.kill/populated observer
// established empty. If cleanup is UNKNOWN, descriptors and directory ownership
// remain retained by the launcher for reconciliation.
func (s *d09OwnedCommandScope) Close() error {
	if s == nil {
		return ErrCgroupScopeRefused
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if !s.settled || !s.reconciled || !s.removed || s.removing {
		s.mu.Unlock()
		return ErrUnknownSettlement
	}
	s.mu.Unlock()
	closeErr := s.scope.Close()
	if closeErr != nil {
		return closeErr
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if s.launcher == nil || s.launcher.scope == nil || s.launcher.scope.file == nil {
		return ErrCgroupScopeRefused
	}
	if s.launcher != nil {
		s.launcher.commandMu.Lock()
		delete(s.launcher.commandScopes, s.name)
		s.launcher.commandMu.Unlock()
	}
	return nil
}

// Remove unlinks the already-settled command cgroup by name relative to the
// held delegated scope before closing descriptors. A failure leaves the handles
// retained and the name registered for reconciliation.
func (s *d09OwnedCommandScope) Remove() error {
	if s == nil || s.launcher == nil || s.launcher.scope == nil || s.launcher.scope.file == nil || s.scope == nil || s.scope.root == nil {
		return ErrCgroupScopeRefused
	}
	s.mu.Lock()
	if s.closed || !s.settled || !s.reconciled || s.removing {
		s.mu.Unlock()
		return ErrUnknownSettlement
	}
	if s.removed {
		s.mu.Unlock()
		return s.Close()
	}
	s.removing = true
	s.mu.Unlock()
	err := unlinkatDir(int(s.scope.root.Fd()), s.name)
	s.mu.Lock()
	s.removing = false
	if err == nil {
		s.removed = true
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("remove reconciled command cgroup: %w", err)
	}
	return s.Close()
}

// admitsFrozenLinuxProfile is the admission gate consulted by the trusted host
// verifier. It refuses unless the signed profile names the frozen D09
// containment/settlement profile (and is not an unedited template), a verified
// cgroup scope is bound, and every host prerequisite is measured.
func (l *D09PodmanCrunLauncher) admitsFrozenLinuxProfile(profile HostProfile) error {
	if l == nil || profile.Mode != ModeProduction {
		return ErrProductionLauncherUnavailable
	}
	if profile.ID == LinuxHostProfileTemplateID {
		return fmt.Errorf("%w: unsigned template profile is not admissible", ErrProductionLauncherUnavailable)
	}
	if placeholders := profile.PlaceholderFields(); len(placeholders) != 0 {
		return fmt.Errorf("%w: profile still holds template placeholders (%s)", ErrProductionLauncherUnavailable, strings.Join(placeholders, ", "))
	}
	if profile.ContainmentProfile != D09ContainmentProfile || profile.SettlementProfile != D09SettlementProfile {
		return fmt.Errorf("%w: signed profile does not select the frozen D09 containment/settlement profile", ErrProductionLauncherUnavailable)
	}
	if !validDigest(l.config.TemplateDigest) || profile.ContainmentProfileDigest != l.config.TemplateDigest {
		return fmt.Errorf("%w: registered D09 template digest does not match the signed profile", ErrProductionLauncherUnavailable)
	}
	if l.scope == nil {
		return fmt.Errorf("%w: no verified cgroup scope is bound to the D09 launcher", ErrCgroupScopeRefused)
	}
	l.managedRootMu.Lock()
	managedRootBound := l.managedRoot != nil
	l.managedRootMu.Unlock()
	if !managedRootBound {
		return fmt.Errorf("%w: trusted composition has not bound the verified managed live-root registration", ErrD09ManagedLiveRootRefused)
	}
	report, err := l.Probe(context.Background())
	if err != nil {
		return err
	}
	if l.productionRunner() {
		if profile.ChildExecutable != PiWorkerCellExecutable {
			return fmt.Errorf("%w: production D09 host fd3 executable path differs from the frozen in-image Node path", ErrHostProfileMismatch)
		}
		if err := l.verifyImageWorkerExecutable(context.Background(), report.PodmanPath, report.ImageRef,
			profile.ChildExecutable, profile.ExecutableDigest); err != nil {
			return err
		}
	}
	l.admissionMu.Lock()
	l.admittedProfile = profile
	l.profileAdmitted = true
	l.admissionMu.Unlock()
	return nil
}

func (l *D09PodmanCrunLauncher) isProfileAdmitted(profile HostProfile) bool {
	if l == nil {
		return false
	}
	l.admissionMu.RLock()
	defer l.admissionMu.RUnlock()
	return l.profileAdmitted && l.admittedProfile == profile && l.config.TemplateDigest == profile.ContainmentProfileDigest
}

func (l *D09PodmanCrunLauncher) admittedProfileSnapshot() (HostProfile, bool) {
	if l == nil {
		return HostProfile{}, false
	}
	l.admissionMu.RLock()
	defer l.admissionMu.RUnlock()
	if l.isClosed() || !l.profileAdmitted || l.admittedProfile.Mode != ModeProduction || l.admittedProfile.ContainmentProfileDigest != l.config.TemplateDigest {
		return HostProfile{}, false
	}
	return l.admittedProfile, true
}

// BoundSettlement returns the held, verified cgroup scope so the process
// lifecycle settles through cgroup.kill and populated=0, not a container-name
// query.
func (l *D09PodmanCrunLauncher) BoundSettlement() DescendantSettler {
	if l == nil {
		return nil
	}
	return l.scope
}

// Probe measures every D09 prerequisite and returns the complete exact list of
// what is missing. Any podman info failure or unparseable rootless/crun/cgroup
// field is fatal: the probe never fails open.
func (l *D09PodmanCrunLauncher) Probe(ctx context.Context) (D09Report, error) {
	if l == nil || l.runner == nil || ctx == nil {
		return D09Report{}, &D09PrerequisiteError{Missing: []D09Prerequisite{{Name: "runner", Detail: "containment launcher is not configured"}}}
	}
	if err := l.beginOperation(); err != nil {
		return D09Report{}, err
	}
	defer l.endOperation()
	config := l.config.normalized()
	report := D09Report{}
	var missing []D09Prerequisite
	if err := validateD09CellIdentity(config); err != nil {
		missing = append(missing, D09Prerequisite{Name: "cell-identity", Detail: err.Error()})
	}
	if l.productionRunner() {
		if _, err := l.ensureManagedLiveRoot(ctx); err != nil {
			missing = append(missing, D09Prerequisite{Name: "managed-live-root", Detail: err.Error()})
		}
		abi, abiErr := D09LandlockKernelABI()
		if abiErr != nil {
			missing = append(missing, D09Prerequisite{Name: "landlock-abi", Detail: abiErr.Error()})
		} else {
			report.LandlockABI = abi
			if abi != config.LandlockABI {
				missing = append(missing, D09Prerequisite{Name: "landlock-abi", Detail: fmt.Sprintf("registered exact ABI %d required; kernel reports %d", config.LandlockABI, abi)})
			}
		}
	}

	podmanPath := ""
	if config.PodmanPath != "" {
		if err := verifyPinnedToolBinary(config.PodmanPath, config.PodmanDigest); err != nil {
			missing = append(missing, D09Prerequisite{Name: "podman", Detail: "pinned podman is not the trusted root-owned immutable binary: " + err.Error()})
		} else {
			podmanPath = config.PodmanPath
		}
	} else if l.productionRunner() {
		missing = append(missing, D09Prerequisite{Name: "podman", Detail: "registered root-owned podman path+digest is not configured"})
	} else if resolved, err := l.runner.LookPath("podman"); err == nil && strings.TrimSpace(resolved) != "" {
		podmanPath = resolved
	} else {
		missing = append(missing, D09Prerequisite{Name: "podman", Detail: "podman not found in PATH and no pinned podman is configured"})
	}
	if config.CrunPath != "" {
		if err := verifyPinnedToolBinary(config.CrunPath, config.CrunDigest); err != nil {
			missing = append(missing, D09Prerequisite{Name: config.Runtime, Detail: "pinned " + config.Runtime + " is not the trusted root-owned immutable binary: " + err.Error()})
		} else {
			report.CrunPresent = true
		}
	} else if l.productionRunner() {
		missing = append(missing, D09Prerequisite{Name: config.Runtime, Detail: "registered root-owned crun path+digest is not configured"})
	} else if _, err := l.runner.LookPath(config.Runtime); err != nil {
		missing = append(missing, D09Prerequisite{Name: config.Runtime, Detail: config.Runtime + " OCI runtime not found in PATH and no pinned runtime is configured"})
	} else {
		report.CrunPresent = true
	}
	cosignBinary := ""
	if config.CosignPath != "" {
		if err := verifyPinnedToolBinary(config.CosignPath, config.CosignDigest); err != nil {
			missing = append(missing, D09Prerequisite{Name: "cosign", Detail: "pinned cosign is not the trusted root-owned immutable binary: " + err.Error()})
		} else {
			cosignBinary = config.CosignPath
			report.CosignPresent = true
		}
	} else if l.productionRunner() {
		missing = append(missing, D09Prerequisite{Name: "cosign", Detail: "registered root-owned cosign path+digest is not configured"})
	} else if _, err := l.runner.LookPath("cosign"); err != nil {
		missing = append(missing, D09Prerequisite{Name: "cosign", Detail: "cosign not found in PATH and no pinned cosign is configured"})
	} else {
		cosignBinary = "cosign"
		report.CosignPresent = true
	}

	if podmanPath != "" {
		report.PodmanPresent = true
		report.PodmanPath = podmanPath
		if result := l.runner.Run(ctx, podmanPath, "--version"); result.Err != nil {
			missing = append(missing, D09Prerequisite{Name: "podman-info", Detail: "podman --version: " + result.Err.Error()})
		} else if result.ExitCode != 0 {
			missing = append(missing, D09Prerequisite{Name: "podman-info", Detail: "podman --version exited " + strconv.Itoa(result.ExitCode)})
		} else {
			report.PodmanVersion = d09LastField(string(result.Stdout))
			if report.PodmanVersion == "" {
				missing = append(missing, D09Prerequisite{Name: "podman-info", Detail: "podman --version produced no version"})
			}
		}
		info, infoErr := l.readInfo(ctx, podmanPath)
		if infoErr != nil {
			// Unknown/unparseable identity is fatal: never continue with an
			// unmeasured runtime.
			missing = append(missing, D09Prerequisite{Name: "podman-info", Detail: infoErr.Error()})
		} else {
			report.Rootless = info.Host.Security.Rootless
			report.OCIRuntime = strings.TrimSpace(info.Host.OCIRuntime.Name)
			report.CgroupVersion = strings.TrimSpace(info.Host.CgroupVersion)
			if !report.Rootless {
				missing = append(missing, D09Prerequisite{Name: "rootless", Detail: "podman is not in rootless mode"})
			}
			if report.OCIRuntime == "" || !strings.EqualFold(report.OCIRuntime, config.Runtime) {
				missing = append(missing, D09Prerequisite{Name: "oci-runtime", Detail: "expected " + config.Runtime + ", observed " + report.OCIRuntime})
			}
			if report.CgroupVersion != "v2" {
				missing = append(missing, D09Prerequisite{Name: "cgroup-v2", Detail: "observed " + report.CgroupVersion})
			}
		}
	}

	switch {
	case strings.TrimSpace(config.ExpectedImageDigest) == "":
		missing = append(missing, D09Prerequisite{Name: "image", Detail: "frozen Pi cell image digest pin is not configured"})
	case !report.PodmanPresent:
		missing = append(missing, D09Prerequisite{Name: "image", Detail: "local image cannot be inspected without podman"})
	default:
		image, imageErr := l.selectImage(ctx, report.PodmanPath, config.Image, config.ExpectedImageDigest)
		if imageErr != nil {
			missing = append(missing, D09Prerequisite{Name: "image", Detail: imageErr.Error()})
		} else {
			report.ImageRef = image
			report.ImageDigest = config.ExpectedImageDigest
			report.ImagePinned = true
			if !report.CosignPresent {
				missing = append(missing, D09Prerequisite{Name: "image-signature", Detail: "cosign is unavailable for local offline verification"})
			} else if verifyErr := l.verifyOfflineSignature(ctx, config, cosignBinary, l.productionRunner()); verifyErr != nil {
				missing = append(missing, D09Prerequisite{Name: "image-signature", Detail: verifyErr.Error()})
			} else {
				report.ImageVerified = true
			}
			if err := verifyD09SeccompProfile(config, l.productionRunner()); err != nil {
				missing = append(missing, D09Prerequisite{Name: "seccomp-profile", Detail: err.Error()})
			}
			if l.productionRunner() {
				if err := l.verifyEntrypointInImage(ctx, report.PodmanPath, report.ImageRef, config); err != nil {
					missing = append(missing, D09Prerequisite{Name: "entrypoint", Detail: err.Error()})
				}
			}
			if config.LandlockABI != D09LandlockABI || !validDigest(config.LandlockRulesetDigest) {
				missing = append(missing, D09Prerequisite{Name: "landlock", Detail: "the exact registered Landlock ABI-8 TSYNC/ruleset profile is not configured"})
			} else if roPaths, roErr := ParseD09LandlockPaths(config.LandlockROPath); roErr != nil {
				missing = append(missing, D09Prerequisite{Name: "landlock", Detail: roErr.Error()})
			} else if rwPaths, rwErr := ParseD09LandlockPaths(config.LandlockRWPath); rwErr != nil {
				missing = append(missing, D09Prerequisite{Name: "landlock", Detail: rwErr.Error()})
			} else if observed, digestErr := DigestD09LandlockRules(roPaths, rwPaths); digestErr != nil || observed != config.LandlockRulesetDigest {
				missing = append(missing, D09Prerequisite{Name: "landlock", Detail: "measured Landlock ruleset digest differs from the registered ABI-8 policy"})
			}
			if strings.TrimSpace(config.FrozenCapabilities) != d09EmptyCapabilitySet {
				missing = append(missing, D09Prerequisite{Name: "capabilities", Detail: "frozen empty capability set is not configured"})
			}
			if config.PidsMax <= 0 || config.MemoryMB <= 0 || strings.TrimSpace(config.CPUs) == "" {
				missing = append(missing, D09Prerequisite{Name: "resource-bounds", Detail: "cpu/memory/pids bounds are not configured"})
			}
		}
	}

	if len(missing) != 0 {
		return report, &D09PrerequisiteError{Missing: missing}
	}
	return report, nil
}

// verifyEntrypointInImage temporarily mounts the exact pinned, already-cosign-
// verified image through the verified Podman binary, hashes the entrypoint
// bytes actually stored in that image against the protected template digest,
// then unmounts the image. Any mount/hash/unmount ambiguity fails admission.
func (l *D09PodmanCrunLauncher) verifyEntrypointInImage(ctx context.Context, podmanPath, imageRef string, config D09Config) error {
	mounted := l.runner.Run(ctx, podmanPath, "image", "mount", imageRef)
	if mounted.Err != nil || mounted.ExitCode != 0 {
		return errors.Join(errors.New("mount pinned image to verify signed in-image entrypoint"), mounted.Err)
	}
	root := strings.TrimSpace(string(mounted.Stdout))
	unmount := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), d09CleanupTimeout)
		defer cancel()
		result := l.runner.Run(cleanupCtx, podmanPath, "image", "unmount", imageRef)
		if result.Err != nil || result.ExitCode != 0 {
			return errors.Join(errors.New("unmount pinned image after entrypoint verification"), result.Err)
		}
		return nil
	}
	if !filepath.IsAbs(root) {
		return errors.Join(errors.New("podman image mount returned no absolute image root"), unmount())
	}
	entry := filepath.Join(root, strings.TrimPrefix(filepath.Clean(config.EntrypointPath), string(os.PathSeparator)))
	digest, err := hashImageExecutable(entry)
	if err != nil {
		return errors.Join(err, unmount())
	}
	if digest != config.EntrypointDigest {
		return errors.Join(fmt.Errorf("in-image entrypoint digest mismatch: got %s want %s", digest, config.EntrypointDigest), unmount())
	}
	return unmount()
}

// verifyImageWorkerExecutable binds the held host fd3 Node bytes to the exact
// executable at the signed in-image path. Both identities must be the same
// digest: image signature verification alone cannot authenticate a different
// host binary which the entrypoint actually executes through fd3.
func (l *D09PodmanCrunLauncher) verifyImageWorkerExecutable(ctx context.Context, podmanPath, imageRef, executablePath, expectedDigest string) error {
	if ctx == nil || !filepath.IsAbs(executablePath) || filepath.Clean(executablePath) != executablePath || !validDigest(expectedDigest) {
		return ErrHostProfileMismatch
	}
	mounted := l.runner.Run(ctx, podmanPath, "image", "mount", imageRef)
	if mounted.Err != nil || mounted.ExitCode != 0 {
		return errors.Join(errors.New("mount pinned image to verify the fd3 worker executable identity"), mounted.Err)
	}
	root := strings.TrimSpace(string(mounted.Stdout))
	unmount := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), d09CleanupTimeout)
		defer cancel()
		result := l.runner.Run(cleanupCtx, podmanPath, "image", "unmount", imageRef)
		if result.Err != nil || result.ExitCode != 0 {
			return errors.Join(errors.New("unmount pinned image after fd3 worker identity verification"), result.Err)
		}
		return nil
	}
	if !filepath.IsAbs(root) {
		return errors.Join(errors.New("podman image mount returned no absolute image root for fd3 verification"), unmount())
	}
	imageExecutable := filepath.Join(root, strings.TrimPrefix(executablePath, string(os.PathSeparator)))
	digest, err := hashImageExecutable(imageExecutable)
	if err != nil || digest != expectedDigest {
		return errors.Join(fmt.Errorf("in-image fd3 executable digest mismatch: got %s want %s", digest, expectedDigest), err, unmount())
	}
	return unmount()
}

func hashImageExecutable(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("in-image entrypoint is not a non-writable executable regular file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("in-image entrypoint changed while opening")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, (128<<20)+1))
	if err != nil || n != info.Size() {
		return "", errors.Join(errors.New("in-image entrypoint read was incomplete or oversized"), err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type d09PodmanInfo struct {
	Host struct {
		CgroupVersion string `json:"cgroupVersion"`
		OCIRuntime    struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"ociRuntime"`
		Security struct {
			Rootless bool `json:"rootless"`
		} `json:"security"`
	} `json:"host"`
}

func (l *D09PodmanCrunLauncher) readInfo(ctx context.Context, podmanPath string) (d09PodmanInfo, error) {
	result := l.runner.Run(ctx, podmanPath, "info", "--format", "json")
	if result.Err != nil {
		return d09PodmanInfo{}, result.Err
	}
	if result.ExitCode != 0 {
		return d09PodmanInfo{}, fmt.Errorf("podman info exited %d: %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	if len(bytes.TrimSpace(result.Stdout)) == 0 {
		return d09PodmanInfo{}, errors.New("podman info produced no output")
	}
	var info d09PodmanInfo
	if err := json.Unmarshal(result.Stdout, &info); err != nil {
		return d09PodmanInfo{}, fmt.Errorf("decode podman info: %w", err)
	}
	return info, nil
}

// selectImage resolves a local reference to a repo@digest whose digest equals
// the frozen pin. It never pulls.
func (l *D09PodmanCrunLauncher) selectImage(ctx context.Context, podmanPath, reference, expected string) (string, error) {
	exists := l.runner.Run(ctx, podmanPath, "image", "exists", reference)
	if exists.Err != nil {
		return "", exists.Err
	}
	if exists.ExitCode != 0 {
		return "", fmt.Errorf("no local image %q is present in the podman store", reference)
	}
	inspect := l.runner.Run(ctx, podmanPath, "image", "inspect", reference)
	if inspect.Err != nil {
		return "", inspect.Err
	}
	if inspect.ExitCode != 0 {
		return "", fmt.Errorf("podman image inspect %q exited %d", reference, inspect.ExitCode)
	}
	var images []struct {
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(inspect.Stdout, &images); err != nil {
		return "", fmt.Errorf("decode image inspect: %w", err)
	}
	if len(images) == 0 {
		return "", fmt.Errorf("image inspect returned no object for %q", reference)
	}
	for _, repoDigest := range images[0].RepoDigests {
		at := strings.LastIndex(repoDigest, "@")
		if at < 0 {
			continue
		}
		digest := strings.TrimSpace(repoDigest[at+1:])
		if validDigest(strings.ToLower(digest)) && strings.EqualFold(digest, expected) {
			return repoDigest, nil
		}
	}
	return "", fmt.Errorf("local image %q does not match the frozen digest %s", reference, expected)
}

// verifyOfflineSignature verifies the locally mirrored OCI image layout with
// the official `cosign verify --offline --insecure-ignore-tlog=true --key <pub> --local-image <layout>` form, then
// cryptographically binds the verified signature payload to the frozen pin and
// the local OCI bytes. It never names a registry reference and runs with a
// sanitized, proxy-free environment. --insecure-ignore-tlog is required: a key
// signature that was never uploaded to Rekor fails cosign v3 log-inclusion
// otherwise (measured, cosign v3.1.3); trust rests on the pinned public key.
func (l *D09PodmanCrunLauncher) verifyOfflineSignature(ctx context.Context, config D09Config, cosignBinary string, requireImmutable bool) error {
	if strings.TrimSpace(config.CosignPublicKeyPath) == "" {
		return errors.New("protected offline cosign public key path is not configured")
	}
	if !validDigest(config.CosignPublicKeyDigest) {
		return errors.New("offline cosign public key digest is not registered")
	}
	var keyDigest string
	var keyErr error
	if requireImmutable {
		keyDigest, keyErr = hashProtectedFile(config.CosignPublicKeyPath, 1<<20)
	} else {
		keyDigest, keyErr = hashBoundedUntrustedFile(config.CosignPublicKeyPath, 1<<20)
	}
	if keyErr != nil || keyDigest != config.CosignPublicKeyDigest {
		return errors.Join(errors.New("offline cosign public key does not match registered digest"), keyErr)
	}
	if strings.TrimSpace(config.OCILayoutPath) == "" {
		return errors.New("local OCI image layout path is not configured for local verification")
	}
	if strings.TrimSpace(cosignBinary) == "" {
		cosignBinary = "cosign"
	}
	if err := verifyLocalOCIIdentity(config.OCILayoutPath, config.ExpectedImageDigest, requireImmutable); err != nil {
		return err
	}
	result := l.runner.Run(ctx, cosignBinary, "verify", "--offline", "--insecure-ignore-tlog=true", "--key", config.CosignPublicKeyPath, "--local-image", config.OCILayoutPath)
	if result.Err != nil {
		return result.Err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("cosign local-image verification exited %d: %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	claimed, err := parseCosignImageDigest(result.Stdout)
	if err != nil {
		return err
	}
	if !strings.EqualFold(claimed, config.ExpectedImageDigest) {
		return fmt.Errorf("cosign signature binds image %s, not the frozen %s", claimed, config.ExpectedImageDigest)
	}
	return nil
}

// parseCosignImageDigest extracts the mandatory image digest from the bounded,
// strict cosign JSON payload. A missing or unknown identity is fatal: a merely
// well-shaped payload is never accepted.
func parseCosignImageDigest(stdout []byte) (string, error) {
	if len(stdout) == 0 || len(stdout) > 1<<20 {
		return "", errors.New("cosign verification produced no bounded JSON payload")
	}
	if err := protocol.ValidateStrictJSON(stdout); err != nil {
		return "", fmt.Errorf("cosign verification output is not strict JSON: %w", err)
	}
	var entries []struct {
		Critical struct {
			Image struct {
				DockerManifestDigest string `json:"docker-manifest-digest"`
			} `json:"image"`
		} `json:"critical"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	if err := decoder.Decode(&entries); err != nil {
		return "", fmt.Errorf("decode cosign verification payload: %w", err)
	}
	for _, entry := range entries {
		digest := strings.TrimSpace(entry.Critical.Image.DockerManifestDigest)
		if validDigest(strings.ToLower(digest)) {
			return digest, nil
		}
	}
	return "", errors.New("cosign verification payload has no trusted image digest claim")
}

// verifyLocalOCIIdentity binds the local OCI image layout to the frozen pin: the
// index must name a manifest whose digest equals the pin, and the manifest blob
// must actually hash to that digest. A layout whose bytes do not match the pin
// fails closed even if a signature over a different image is valid.
func verifyLocalOCIIdentity(layoutPath, expectedDigest string, requireImmutable bool) error {
	info, err := os.Lstat(layoutPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("local OCI image layout %q is not a readable directory", layoutPath)
	}
	if requireImmutable {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return errors.New("local OCI image layout must be root-owned and not group/world writable")
		}
		if err := requireReadOnlyMount(layoutPath); err != nil {
			return fmt.Errorf("local OCI image layout is not on a read-only mount: %w", err)
		}
	}
	indexRaw, err := readBoundedFile(filepath.Join(layoutPath, "index.json"), 1<<20, requireImmutable)
	if err != nil {
		return fmt.Errorf("read OCI index: %w", err)
	}
	if err := protocol.ValidateStrictJSON(indexRaw); err != nil {
		return fmt.Errorf("OCI index is not strict JSON: %w", err)
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		return fmt.Errorf("decode OCI index: %w", err)
	}
	for _, manifest := range index.Manifests {
		digest := strings.TrimSpace(manifest.Digest)
		if !strings.EqualFold(digest, expectedDigest) {
			continue
		}
		hexDigest := strings.TrimPrefix(strings.ToLower(digest), "sha256:")
		blobPath := filepath.Join(layoutPath, "blobs", "sha256", hexDigest)
		blob, err := readBoundedFile(blobPath, 64<<20, requireImmutable)
		if err != nil {
			return fmt.Errorf("read OCI manifest blob: %w", err)
		}
		if DigestBytes(blob) != strings.ToLower(digest) {
			return fmt.Errorf("local OCI manifest blob does not hash to %s", digest)
		}
		if err := protocol.ValidateStrictJSON(blob); err != nil {
			return fmt.Errorf("OCI image manifest is not strict JSON: %w", err)
		}
		var imageManifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers []struct {
				Digest string `json:"digest"`
			} `json:"layers"`
		}
		if err := json.Unmarshal(blob, &imageManifest); err != nil || len(imageManifest.Layers) == 0 || len(imageManifest.Layers) > 4096 {
			return errors.Join(errors.New("OCI image manifest is invalid or outside layer bound"), err)
		}
		digests := make([]string, 0, len(imageManifest.Layers)+1)
		digests = append(digests, imageManifest.Config.Digest)
		for _, layer := range imageManifest.Layers {
			digests = append(digests, layer.Digest)
		}
		var total int64
		for _, childDigest := range digests {
			if !validDigest(childDigest) {
				return errors.New("OCI config/layer descriptor has invalid digest")
			}
			path := filepath.Join(layoutPath, "blobs", "sha256", strings.TrimPrefix(childDigest, "sha256:"))
			size, verifyErr := verifyOCIBlob(path, childDigest, PiRuntimeBundleMaxBytes-total, requireImmutable)
			if verifyErr != nil {
				return fmt.Errorf("verify OCI config/layer %s: %w", childDigest, verifyErr)
			}
			total += size
		}
		return nil
	}
	return fmt.Errorf("local OCI layout does not contain the frozen manifest %s", expectedDigest)
}

func readBoundedFile(path string, maximum int64, requireImmutable ...bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maximum {
		return nil, ErrD09TemplateInvalid
	}
	if len(requireImmutable) != 0 && requireImmutable[0] {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return nil, errors.New("OCI artifact must be root-owned and non-writable")
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("OCI artifact changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errors.Join(errors.New("OCI artifact read was incomplete or exceeded bound"), err)
	}
	return data, nil
}

func verifyOCIBlob(path, expected string, maximum int64, requireImmutable bool) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > maximum {
		return 0, ErrD09TemplateInvalid
	}
	if requireImmutable {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return 0, errors.New("OCI blob must be root-owned and non-writable")
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return 0, errors.New("OCI blob changed while opening")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil || n != info.Size() {
		return 0, errors.Join(errors.New("OCI blob read was incomplete or exceeded bound"), err)
	}
	observed := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(observed, expected) {
		return 0, errors.New("OCI blob content digest mismatch")
	}
	return n, nil
}

// verifyD09SeccompProfile binds the static seccomp-BPF profile bytes to the
// digest recorded in the registered template. It is skipped only when the
// template left seccomp entirely unconfigured.
func verifyD09SeccompProfile(config D09Config, requireProtected bool) error {
	path, digest := strings.TrimSpace(config.SeccompProfilePath), strings.TrimSpace(config.SeccompProfileDigest)
	if path == "" && digest == "" {
		return nil
	}
	if path == "" || digest == "" {
		return errors.New("static seccomp profile path and digest must both be configured")
	}
	var observed string
	var err error
	if requireProtected {
		observed, err = hashProtectedFile(path, maxD09TemplateBytes)
	} else {
		observed, err = hashBoundedUntrustedFile(path, maxD09TemplateBytes)
	}
	if err != nil {
		return fmt.Errorf("read static seccomp profile: %w", err)
	}
	if observed != digest {
		return fmt.Errorf("static seccomp profile digest mismatch: got %s want %s", observed, digest)
	}
	return nil
}

func hashBoundedUntrustedFile(path string, maximum int64) (string, error) {
	data, err := readBoundedFile(path, maximum)
	if err != nil {
		return "", err
	}
	return DigestBytes(data), nil
}

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
	if ctx == nil || l == nil || l.runner == nil || l.scope == nil {
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
			returnedErr = errors.Join(returnedErr,
				l.cleanupFailedContainer(launchCmd, launchPodman, launchName))
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
	command.Env = d09ProbeEnv()
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
	cid, err := readD09CIDFifo(ctx, cidFile, d09CommandStartTimeout)
	if err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("observe D09 worker container id: %w", err)
	}
	inspectCtx, cancelInspect := context.WithTimeout(ctx, d09WorkerProofTimeout)
	containerState, err := inspectContainer(inspectCtx, report.PodmanPath, cid)
	cancelInspect()
	if err != nil || containerState.State.Status != "running" || containerState.State.PID <= 0 ||
		strings.TrimPrefix(containerState.Name, "/") != name {
		return nil, D09TCBProof{}, errors.Join(fmt.Errorf("D09 worker container identity/state is not running as the requested cell"), err)
	}
	startupProofCtx, cancelStartupProof := context.WithTimeout(ctx, d09WorkerProofTimeout)
	proof, err := readD09TCBProofContext(startupProofCtx, proofRead)
	cancelStartupProof()
	if err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("captured TCB proof is missing or invalid: %w", err)
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
	pidfd, err := openPidfd(proof.LeaderPID)
	if err != nil {
		return nil, D09TCBProof{}, fmt.Errorf("entrypoint leader PID %d has no host pidfd: %w", proof.LeaderPID, err)
	}
	if err := validateD09TCBProof(proof, config); err != nil {
		_ = syscall.Close(pidfd)
		return nil, D09TCBProof{}, err
	}
	if err := validateD09RouteProbeProof(proof, config); err != nil {
		_ = syscall.Close(pidfd)
		return nil, D09TCBProof{}, fmt.Errorf("%w: %v", ErrProductionLauncherUnavailable, err)
	}
	l.storeLaunchProof(command, proof, os.NewFile(uintptr(pidfd), "d09-worker-pidfd"), stdoutPipe.reader)
	launchSucceeded = true
	return command, proof, nil
}

func descriptorPathForMount(file *os.File) (string, error) {
	if file == nil {
		return "", ErrHostBindingsMissing
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return "", ErrHostBindingsMissing
	}
	path := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), file.Fd())
	link, err := os.Readlink(path)
	if err != nil || !filepath.IsAbs(link) || strings.HasSuffix(link, " (deleted)") || strings.Contains(link, ",") {
		return "", ErrHostBindingsMissing
	}
	return path, nil
}

// storeLaunchProof registers the captured, validated proof against the exact
// launched command so it can be retrieved once and never for a different launch.
func (l *D09PodmanCrunLauncher) storeLaunchProof(cmd *exec.Cmd, proof D09TCBProof, pidfd *os.File, stdout io.ReadCloser) {
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
	return validateD09NamespaceProof(proof, l.config.HostNamespaces)
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

// readD09TCBProof reads the single strict-JSON proof line the entrypoint writes
// on fd10 and closes before exec.
func readD09TCBProof(reader io.Reader) (D09TCBProof, error) {
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
	if _, err := buffered.ReadByte(); !errors.Is(err, io.EOF) {
		return D09TCBProof{}, fmt.Errorf("entrypoint proof pipe was not closed before worker exec")
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

// closeLaunchPidfdLocked records close failures and clears the descriptor only
// after a successful Close. The caller must hold record.ownerMu.
func closeLaunchPidfdLocked(record *d09LaunchRecord) error {
	if record == nil {
		return nil
	}
	if record.pidfd != nil {
		err := record.pidfd.Close()
		record.pidfdCloseErr = errors.Join(record.pidfdCloseErr, err)
		if err == nil {
			record.pidfd = nil
		}
	}
	return record.pidfdCloseErr
}

// finishFailedLaunchRecord releases a failed launch's owners only after each
// close succeeds and the exact record is still registered.
func (l *D09PodmanCrunLauncher) finishFailedLaunchRecord(command *exec.Cmd, record *d09LaunchRecord) error {
	if l == nil || command == nil || record == nil {
		return errors.Join(ErrUnknownSettlement, errors.New("failed launch record is unavailable"))
	}
	record.ownerMu.Lock()
	defer record.ownerMu.Unlock()

	var closeErr error
	if record.stdout != nil && !record.stdoutClosed {
		err := record.stdout.Close()
		record.stdoutCloseErr = errors.Join(record.stdoutCloseErr, err)
		if err == nil {
			record.stdoutClosed = true
		}
	}
	if record.stdoutCloseErr != nil {
		closeErr = errors.Join(closeErr,
			fmt.Errorf("close failed-launch stdout owner: %w", record.stdoutCloseErr))
	}
	if err := closeLaunchPidfdLocked(record); err != nil {
		closeErr = errors.Join(closeErr, fmt.Errorf("close D09 launch pidfd: %w", err))
	}
	if closeErr != nil {
		return errors.Join(ErrUnknownSettlement, closeErr)
	}

	l.proofMu.Lock()
	defer l.proofMu.Unlock()
	if l.proofs[command] != record {
		return errors.Join(ErrUnknownSettlement, errors.New("failed launch record ownership changed"))
	}
	delete(l.proofs, command)
	l.launchInProg = false
	return nil
}

func (l *D09PodmanCrunLauncher) cleanupFailedContainer(command *exec.Cmd, podmanPath, name string) error {
	if command == nil || command.Process == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), d09CleanupTimeout)
	defer cancel()
	_ = command.Process.Kill()
	if podmanPath != "" && name != "" {
		l.cleanupContainer(podmanPath, name)
	}
	l.proofMu.Lock()
	record := l.proofs[command]
	l.proofMu.Unlock()
	if record == nil {
		l.storePendingLaunch(command, nil)
		l.proofMu.Lock()
		record = l.proofs[command]
		l.proofMu.Unlock()
	}
	waitErr := l.waitFailedCommand(cleanupCtx, command, record)
	if l.scope == nil {
		return errors.Join(ErrUnknownSettlement, waitErr)
	}
	settled, settleErr := l.scope.Settle(cleanupCtx)
	if !settled || settleErr != nil || waitErr != nil {
		return errors.Join(ErrUnknownSettlement, settleErr, waitErr)
	}
	return l.finishFailedLaunchRecord(command, record)
}

// waitFailedCommand owns the single Wait call for a failed, exact launch.
// Further cleanup attempts observe the same completion instead of racing Wait.
func failedCommandWaitResult(waitErr error) error {
	if waitErr == nil {
		return nil
	}
	if _, ok := waitErr.(*exec.ExitError); ok {
		return nil
	}
	return errors.Join(ErrUnknownSettlement, waitErr)
}

func (l *D09PodmanCrunLauncher) waitFailedCommand(ctx context.Context, command *exec.Cmd, record *d09LaunchRecord) error {
	l.proofMu.Lock()
	if record.waitDone == nil {
		record.waitDone = make(chan struct{})
		done := record.waitDone
		l.proofMu.Unlock()
		go func() {
			waitErr := command.Wait()
			l.proofMu.Lock()
			record.waitErr = waitErr
			close(done)
			l.proofMu.Unlock()
		}()
	} else {
		done := record.waitDone
		l.proofMu.Unlock()
		select {
		case <-done:
			return failedCommandWaitResult(record.waitErr)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-record.waitDone:
		return failedCommandWaitResult(record.waitErr)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *D09PodmanCrunLauncher) cleanupContainer(podmanPath, name string) {
	if strings.TrimSpace(podmanPath) == "" || strings.TrimSpace(name) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d09CleanupTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, podmanPath, "rm", "-f", name)
	command.Env = d09ProbeEnv()
	command.Stdout, command.Stderr = io.Discard, io.Discard
	_ = command.Run()
}

// cleanupCell kills and settles the exact launched worker command after a proof
// validation failure. It does not treat the supervisor's Podman CLI process as
// the cell leader. It succeeds only if the per-command proof record is present
// and the recorded host worker PID's pidfd plus held cgroup reach settlement.
func (l *D09PodmanCrunLauncher) cleanupFailedLaunch(cmd *exec.Cmd) bool {
	if l == nil || cmd == nil {
		return false
	}
	l.proofMu.Lock()
	record := l.proofs[cmd]
	l.proofMu.Unlock()
	if record == nil || record.proof.LeaderPID <= 0 || record.pidfd == nil {
		return false
	}
	if cmd.Process == nil {
		return false
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), d09CleanupTimeout)
	defer cancel()
	if record.proof.ContainerName != "" {
		l.cleanupContainer(l.config.PodmanPath, record.proof.ContainerName)
	}
	_ = cmd.Process.Kill()
	evidence, err := l.settleCellWithPidfd(cleanupCtx, record.proof.LeaderPID, record.pidfd)
	if err != nil || !evidence.PidfdObserved || !evidence.CgroupKilled || !evidence.PopulatedZero {
		return false
	}
	if l.waitFailedCommand(cleanupCtx, cmd, record) != nil {
		return false
	}
	return l.finishFailedLaunchRecord(cmd, record) == nil
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
		vector = append(vector, "--cgroup-parent", config.CgroupsPath)
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
	vector = append(vector,
		imageRef,
		entrypoint,
		"/proc/self/fd/3",
	)
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

// d09ProbeEnv is a sanitized, proxy-free environment for host tooling: no
// provider credentials, no live-workspace paths, and no proxy variables that
// could redirect an offline verification at the network.
func d09ProbeEnv() []string {
	allow := []string{
		"HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME",
		"XDG_DATA_HOME", "TMPDIR", "LANG", "LC_ALL", "DBUS_SESSION_BUS_ADDRESS",
	}
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	for _, key := range allow {
		if value, ok := os.LookupEnv(key); ok {
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

// D09SettlementEvidence records the measured postconditions of a settled cell.
type D09SettlementEvidence struct {
	LeaderPID     int
	PidfdObserved bool
	CgroupKilled  bool
	PopulatedZero bool
	Detail        string
}

// SettleCell records pidfd evidence for the cell leader and then settles the
// held cgroup scope (cgroup.kill + recursive populated=0 with a deadline). It is
// the D11 evidence API; a profile label alone is never evidence.
func (l *D09PodmanCrunLauncher) SettleCell(ctx context.Context, leaderPID int) (D09SettlementEvidence, error) {
	if l == nil || l.scope == nil || ctx == nil {
		return D09SettlementEvidence{}, ErrProductionLauncherUnavailable
	}
	if leaderPID <= 0 {
		return D09SettlementEvidence{}, fmt.Errorf("%w: entrypoint proof omitted a host leader PID", ErrProductionLauncherUnavailable)
	}
	pidfd, err := openPidfd(leaderPID)
	if err != nil {
		return D09SettlementEvidence{LeaderPID: leaderPID, Detail: "pidfd_open failed: " + err.Error()}, fmt.Errorf("%w: pidfd for D09 cell leader is unavailable: %v", ErrProductionLauncherUnavailable, err)
	}
	pidfdFile := os.NewFile(uintptr(pidfd), "d09-cell-pidfd")
	if pidfdFile == nil {
		_ = syscall.Close(pidfd)
		return D09SettlementEvidence{}, ErrProductionLauncherUnavailable
	}
	defer pidfdFile.Close()
	return l.settleCellWithPidfd(ctx, leaderPID, pidfdFile)
}

func (l *D09PodmanCrunLauncher) settleCellWithPidfd(ctx context.Context, leaderPID int, pidfdFile *os.File) (D09SettlementEvidence, error) {
	if l == nil || l.scope == nil || ctx == nil || leaderPID <= 0 || pidfdFile == nil {
		return D09SettlementEvidence{}, ErrProductionLauncherUnavailable
	}
	pidfd := int(pidfdFile.Fd())
	epollFD, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return D09SettlementEvidence{LeaderPID: leaderPID}, fmt.Errorf("create pidfd epoll: %w", err)
	}
	defer syscall.Close(epollFD)
	event := &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(pidfd)}
	if err := syscall.EpollCtl(epollFD, syscall.EPOLL_CTL_ADD, pidfd, event); err != nil {
		return D09SettlementEvidence{LeaderPID: leaderPID}, fmt.Errorf("register D09 leader pidfd: %w", err)
	}
	evidence := D09SettlementEvidence{LeaderPID: leaderPID}
	settled, err := l.scope.Settle(ctx) // writes cgroup.kill then observes populated=0
	evidence.PopulatedZero = settled
	if err != nil {
		return evidence, err
	}
	if !settled {
		return evidence, fmt.Errorf("%w: cgroup did not reach populated=0", ErrProductionLauncherUnavailable)
	}
	evidence.CgroupKilled = true
	for {
		var events [1]syscall.EpollEvent
		count, pollErr := syscall.EpollWait(epollFD, events[:], 50)
		if pollErr != nil && pollErr != syscall.EINTR {
			return evidence, fmt.Errorf("poll D09 leader pidfd: %w", pollErr)
		}
		if count > 0 {
			evidence.PidfdObserved = true
			break
		}
		if err := ctx.Err(); err != nil {
			return evidence, err
		}
	}
	return evidence, nil
}

// openPidfd opens a pidfd for the leader (linux/amd64 pidfd_open).
func openPidfd(pid int) (int, error) {
	fd, _, errno := syscall.Syscall(434, uintptr(pid), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

// d09NewToken returns a unique per-cell container-name suffix.
func d09NewToken() string {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(token[:])
}

// d09DetachedScopePrefix names the command cgroups of the no-reap/v1 cell. The operator reset
// recognises them by this prefix.
const d09DetachedScopePrefix = "tbound-nr-"

// abandon releases a detached command scope from the lease without killing or removing it: the
// no-reap/v1 cell is left running by design. It closes the held descriptors and forgets the scope
// in the launcher, so a later launcher Close does not wait for it. The cgroup directory and its
// processes stay for the operator reset.
func (s *d09OwnedCommandScope) abandon() error {
	if s == nil || s.launcher == nil || !s.detached {
		return ErrCgroupScopeRefused
	}
	s.mu.Lock()
	if s.closed || s.removing {
		s.mu.Unlock()
		return ErrUnknownSettlement
	}
	s.closed, s.settled = true, false
	s.mu.Unlock()
	closeErr := s.scope.Close()
	s.launcher.commandMu.Lock()
	delete(s.launcher.commandScopes, s.name)
	s.launcher.commandMu.Unlock()
	return closeErr
}

// populated reads cgroup.events of the scope without any kill.
func (s *d09OwnedCommandScope) populated() (bool, error) {
	if s == nil || s.scope == nil || s.scope.file == nil {
		return false, ErrCgroupScopeRefused
	}
	return readCgroupPopulated(s.scope.file)
}
