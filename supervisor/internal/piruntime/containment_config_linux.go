//go:build linux

// containment_config_linux.go: D09 containment configuration: command runner, prerequisites, config, identity validation and pinned environment.

package piruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/productsetup"
)

// This file is the concrete D09 containment backend: a rootless Podman/crun
// cell for the governed Pi worker. It implements the sealed
// ExecutableLauncher.StartVerified seam that StartChild/LaunchPiCLIWorker drive,
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

// execD09CommandRunner runs the probe and cosign commands. A product runner carries the fixed product environment. The
// thesis runner is the zero value, which keeps d09ProbeEnv.
type execD09CommandRunner struct {
	environment []string
}

func newExecD09CommandRunner() D09ProbeRunner { return execD09CommandRunner{} }

func (execD09CommandRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// commandEnvironment is the environment of one host command this runner starts.
func (r execD09CommandRunner) commandEnvironment() []string {
	if r.environment != nil {
		return r.environment
	}
	return d09ProbeEnv()
}

func (r execD09CommandRunner) Run(ctx context.Context, name string, args ...string) D09CommandResult {
	if ctx == nil {
		return D09CommandResult{ExitCode: -1, Err: errors.New("nil context")}
	}
	command := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.Env = r.commandEnvironment()
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
	return validateD09CellIdentityMeasurements(config)
}

func validateD09CellIdentityMeasurements(config D09Config) error {
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
	productCommandOwner func(D09ProductCommandOwnership) error
	runner              D09ProbeRunner
	config              D09Config
	scope               *LinuxCgroupScope

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

	// productHost is the verified product command host of a product launcher. It is nil for the thesis launcher.
	productHost *D09ProductCommandHost
}

func (l *D09PodmanCrunLauncher) productionRunner() bool {
	if l == nil || l.runner == nil {
		return false
	}
	_, ok := l.runner.(execD09CommandRunner)
	return ok
}

// commandEnvironment is the environment of every Podman child this launcher starts. A product launcher holds only a
// verified host, so its children get the fixed product environment and nothing inherited.
func (l *D09PodmanCrunLauncher) commandEnvironment() []string {
	if l != nil && l.productHost.verified() {
		return productsetup.CommandCellEnvironment()
	}
	return d09ProbeEnv()
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

// withRegisteredServiceIdentity fills the user-namespace host uid/gid from the
// verified registration when the launcher config, built from the registered
// template, does not carry them yet. It only fills zero values and only for the
// registration the config is bound to; validateD09CellIdentity then still
// requires them to reproduce the signed mapping digest.
func withRegisteredServiceIdentity(config D09Config, registration D09ManagedLiveRootRegistration) D09Config {
	if config.UserNamespaceHostUID == 0 && config.UserNamespaceHostGID == 0 &&
		registration.Digest != "" && registration.Digest == config.ManagedLiveRootRegistrationDigest {
		config.UserNamespaceHostUID = registration.ServiceUID
		config.UserNamespaceHostGID = registration.ServiceGID
	}
	return config
}

// d09PinnedPodmanEnv are the podman variables the signed managed service config
// pins (containers_conf_path, storage_conf_path, xdg_cache_home).
var d09PinnedPodmanEnv = map[string]string{
	"CONTAINERS_CONF":         D09ManagedPiContainersConfPath,
	"CONTAINERS_STORAGE_CONF": D09ManagedPiStorageConfPath,
	"XDG_CACHE_HOME":          D09ManagedPiPodmanCacheHome,
}
