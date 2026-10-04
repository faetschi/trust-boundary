//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	childEnvVar  = "TBOUND_SANDBOX_CHILD"
	configEnvVar = "TBOUND_SANDBOX_CONFIG"
	planEnvVar   = "TBOUND_SANDBOX_PLAN"

	controlFD = 3
	rootFD    = 4
	cancelFD  = 5

	maxCellOutput = 8 << 20
	childGrace    = 5 * time.Second
)

var (
	// ErrInvalidRequest is returned for a malformed Launch request.
	ErrInvalidRequest = errors.New("invalid sandbox launch request")
	// ErrMechanismUnavailable is returned when a required isolation mechanism
	// cannot be established. Launch fails closed instead of proceeding.
	ErrMechanismUnavailable = errors.New("required isolation mechanism is unavailable")
	// ErrNotSettled is returned when the cell scope or mounts were not proven
	// settled. Launch fails closed instead of reporting a clean result.
	ErrNotSettled = errors.New("command cell did not settle")
)

// Limits bounds cell resources. Zero means "not requested"; a requested limit
// that cannot be established makes the corresponding mechanism unestablished
// rather than silently ignored.
type Limits struct {
	MemoryBytes int64
	PidsMax     int64
	CPUWeight   int64
}

// Request is one command to run inside a fresh isolation cell. Root is the
// writable cell root supplied by the trusted supervisor; it is never exposed
// to the command as a live source path. Dir is an optional command working
// directory relative to Root. Argv is the exact argument vector.
type Request struct {
	Root   *os.File
	Argv   []string
	Env    []string
	Dir    string
	Limits Limits
}

// Result is the bounded terminal status and captured output of the command.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Settlement is the runner's observed postcondition receipt. It is evidence,
// not proof of containment: this package's profile is non-claim-bearing.
type Settlement struct {
	ProcessScopeEmpty bool
	WritersStopped    bool
	MountDetached     bool
	ExitObserved      bool
	ExitCode          int
	EvidenceClass     string
	Survivors         int
}

// Report is measured capability evidence for the frozen development/WSL
// profile. A false field means the mechanism was not observed, never that it
// was assumed.
type Report struct {
	KernelRelease     string
	LandlockABI       int
	SeccompBPF        bool
	UserNamespaces    bool
	MountNamespace    bool
	PidNamespace      bool
	IPCNamespace      bool
	UTSNamespace      bool
	NetworkNamespace  bool
	NoNewPrivs        bool
	CapabilitiesEmpty bool
	Loopback          bool
	Pidfd             bool
	CgroupV2          bool
	CgroupDelegated   bool
	Unsupported       []string
}

func (report Report) mechanisms() []string {
	mechanisms := []string{"non-claim-bearing", "dev-wsl-profile"}
	if report.UserNamespaces {
		mechanisms = append(mechanisms, "user-namespace")
	}
	if report.MountNamespace {
		mechanisms = append(mechanisms, "mount-namespace")
	}
	if report.PidNamespace {
		mechanisms = append(mechanisms, "pid-namespace")
	}
	if report.IPCNamespace {
		mechanisms = append(mechanisms, "ipc-namespace")
	}
	if report.UTSNamespace {
		mechanisms = append(mechanisms, "uts-namespace")
	}
	if report.NetworkNamespace {
		mechanisms = append(mechanisms, "network-namespace")
	}
	if report.LandlockABI > 0 {
		mechanisms = append(mechanisms, fmt.Sprintf("landlock-abi%d", report.LandlockABI))
	}
	if report.SeccompBPF {
		mechanisms = append(mechanisms, "seccomp-bpf")
	}
	if report.NoNewPrivs {
		mechanisms = append(mechanisms, "no-new-privs")
	}
	if report.CapabilitiesEmpty {
		mechanisms = append(mechanisms, "empty-capabilities")
	}
	if report.CgroupDelegated {
		mechanisms = append(mechanisms, "cgroup2-delegated")
	} else {
		mechanisms = append(mechanisms, "cgroup2=not-established")
	}
	sort.Strings(mechanisms)
	return mechanisms
}

// EvidenceClass returns the measured, non-claim-bearing profile label.
func (report Report) EvidenceClass() string {
	return "dev-wsl-non-claim-bearing:" + strings.Join(report.mechanisms(), "+")
}

func (report Report) require() error {
	var missing []string
	if report.LandlockABI < 1 {
		missing = append(missing, "landlock-abi1")
	}
	if !report.SeccompBPF {
		missing = append(missing, "seccomp-bpf")
	}
	if !report.UserNamespaces {
		missing = append(missing, "user-namespace")
	}
	if !report.MountNamespace {
		missing = append(missing, "mount-namespace")
	}
	if !report.PidNamespace {
		missing = append(missing, "pid-namespace")
	}
	if !report.NetworkNamespace {
		missing = append(missing, "network-namespace")
	}
	if !report.Loopback {
		missing = append(missing, "loopback-interface")
	}
	if !report.NoNewPrivs {
		missing = append(missing, "no-new-privs")
	}
	if !report.CapabilitiesEmpty {
		missing = append(missing, "empty-capabilities")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMechanismUnavailable, strings.Join(missing, ","))
	}
	return nil
}

// Probe measures the mechanisms available to a fresh cell. It launches one
// short-lived self-probe inside the real namespace composition; every reported
// mechanism was observed inside that probe. It does not claim containment.
func Probe() (Report, error) {
	report := Report{KernelRelease: kernelRelease()}
	if abi, err := landlockABI(); err == nil {
		report.LandlockABI = abi
	} else {
		report.Unsupported = append(report.Unsupported, "landlock: "+err.Error())
	}
	report.SeccompBPF = seccompAvailable()
	if !report.SeccompBPF {
		report.Unsupported = append(report.Unsupported, "seccomp: errno/allow actions unavailable")
	}
	if fd, err := pidfdOpen(os.Getpid()); err == nil {
		report.Pidfd = true
		_ = syscall.Close(fd)
	} else {
		report.Unsupported = append(report.Unsupported, "pidfd: "+err.Error())
	}
	if root, ok := findCgroup2Mount(); ok {
		report.CgroupV2 = true
		probe := filepath.Join(root, "tbound-probe-"+newToken())
		if err := os.Mkdir(probe, 0o755); err == nil {
			report.CgroupDelegated = true
			_ = os.Remove(probe)
		} else {
			report.Unsupported = append(report.Unsupported, "cgroup: not delegated")
		}
	} else {
		report.Unsupported = append(report.Unsupported, "cgroup: no v2 mount")
	}
	payload, err := runProbe()
	if err != nil {
		report.Unsupported = append(report.Unsupported, "namespace probe: "+err.Error())
		return report, nil
	}
	report.UserNamespaces = payload.UID == 0
	report.MountNamespace = payload.Private && payload.Tmpfs
	report.PidNamespace = payload.Pid == 1
	report.IPCNamespace = payload.Private
	report.UTSNamespace = payload.Private
	report.NetworkNamespace = payload.Private
	report.NoNewPrivs = payload.NoNewPrivsObserved
	report.CapabilitiesEmpty = payload.CapabilitiesEmpty
	report.Loopback = payload.Loopback
	if payload.Error != "" {
		report.Unsupported = append(report.Unsupported, "probe child: "+payload.Error)
	}
	return report, nil
}

func runProbe() (probePayload, error) {
	command, err := newCellCommand(childConfig{Mode: "probe"}, nil)
	if err != nil {
		return probePayload{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return probePayload{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return probePayload{}, err
	}
	if err := command.Start(); err != nil {
		return probePayload{}, fmt.Errorf("start namespace probe: %w", err)
	}
	var output, errOutput bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyAll(&output, stdout, maxCellOutput) }()
	go func() { defer wg.Done(); copyAll(&errOutput, stderr, maxCellOutput) }()
	exitCode, _, waitErr := reapCell(context.Background(), command.Process.Pid, nil)
	wg.Wait()
	if waitErr != nil {
		return probePayload{}, waitErr
	}
	if exitCode != 0 {
		return probePayload{}, fmt.Errorf("probe exited %d: %s", exitCode, strings.TrimSpace(errOutput.String()))
	}
	var payload probePayload
	line := output.Bytes()
	if index := bytes.IndexByte(line, '\n'); index >= 0 {
		line = line[:index]
	}
	if err := json.Unmarshal(line, &payload); err != nil {
		return probePayload{}, fmt.Errorf("decode probe payload: %w", err)
	}
	return payload, nil
}

// Launch runs one command inside a fresh cell. It fails closed if a required
// mechanism is unavailable or if the scope or mounts cannot be proven settled.
func Launch(ctx context.Context, request Request) (Result, Settlement, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, Settlement{}, err
	}
	report, err := Probe()
	if err != nil {
		return Result{}, Settlement{}, fmt.Errorf("probe isolation mechanisms: %w", err)
	}
	if err := report.require(); err != nil {
		return Result{}, Settlement{}, err
	}
	token := newToken()
	config := childConfig{
		Mode:  "init",
		Token: token,
		Argv:  append([]string(nil), request.Argv...),
		Env:   resolvedEnv(request.Env),
		Dir:   request.Dir,
		Limits: cellLimits{
			MemoryBytes: request.Limits.MemoryBytes,
			PidsMax:     request.Limits.PidsMax,
			CPUWeight:   request.Limits.CPUWeight,
		},
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return Result{}, Settlement{}, err
	}
	defer controlRead.Close()
	cancelRead, cancelWrite, err := os.Pipe()
	if err != nil {
		_ = controlWrite.Close()
		return Result{}, Settlement{}, err
	}
	defer cancelWrite.Close()

	command, err := newCellCommand(config, []*os.File{controlWrite, request.Root, cancelRead})
	if err != nil {
		_ = controlWrite.Close()
		_ = cancelRead.Close()
		return Result{}, Settlement{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = controlWrite.Close()
		_ = cancelRead.Close()
		return Result{}, Settlement{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = controlWrite.Close()
		_ = cancelRead.Close()
		return Result{}, Settlement{}, err
	}
	if err := command.Start(); err != nil {
		_ = controlWrite.Close()
		_ = cancelRead.Close()
		return Result{}, Settlement{}, fmt.Errorf("start command cell: %w", err)
	}
	_ = controlWrite.Close()
	_ = cancelRead.Close()

	var output, errOutput bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyAll(&output, stdout, maxCellOutput) }()
	go func() { defer wg.Done(); copyAll(&errOutput, stderr, maxCellOutput) }()

	childPID := command.Process.Pid
	cancel := func() { _, _ = cancelWrite.Write([]byte{1}) }
	_, _, waitErr := reapCell(ctx, childPID, cancel)
	wg.Wait()

	var control bytes.Buffer
	if _, err := io.Copy(&control, io.LimitReader(controlRead, maxCellOutput)); err != nil && !errors.Is(err, io.EOF) {
		return Result{}, Settlement{}, fmt.Errorf("read cell settlement: %w", err)
	}
	messages := parseControl(control.Bytes())
	var settlementMessage *controlMessage
	for index := range messages {
		message := messages[index]
		switch message.Stage {
		case "stage2-error":
			return Result{}, Settlement{}, fmt.Errorf("%w: %s", ErrMechanismUnavailable, message.Error)
		case "settlement":
			settlementMessage = &message
		}
	}
	if settlementMessage == nil {
		detail := strings.TrimSpace(errOutput.String())
		if waitErr != nil {
			detail = waitErr.Error()
		}
		return Result{}, Settlement{}, fmt.Errorf("%w: cell produced no settlement: %s (control=%q)", ErrNotSettled, detail, control.String())
	}
	message := *settlementMessage
	if !message.ExitObserved {
		return Result{}, Settlement{}, fmt.Errorf("%w: exit was not observed", ErrNotSettled)
	}
	if !message.ProcessScopeEmpty {
		return Result{}, Settlement{}, fmt.Errorf("%w: %d process(es) survived the cell scope", ErrNotSettled, message.Survivors)
	}
	if !message.WritersStopped {
		return Result{}, Settlement{}, fmt.Errorf("%w: writers did not stop", ErrNotSettled)
	}
	if !message.MountDetached {
		return Result{}, Settlement{}, fmt.Errorf("%w: cell mount was not detached: %s", ErrNotSettled, strings.Join(message.Notes, "; "))
	}
	evidenceClass := report.EvidenceClass()
	if message.CgroupEstablished != report.CgroupDelegated {
		// The evidence class reflects what this run actually established, not
		// only what the probe found available.
		effectiveReport := report
		effectiveReport.CgroupDelegated = report.CgroupDelegated && message.CgroupEstablished
		evidenceClass = effectiveReport.EvidenceClass()
	}
	result := Result{ExitCode: message.ExitCode, Stdout: output.Bytes(), Stderr: errOutput.Bytes()}
	settlement := Settlement{
		ProcessScopeEmpty: message.ProcessScopeEmpty,
		WritersStopped:    message.WritersStopped,
		MountDetached:     message.MountDetached,
		ExitObserved:      message.ExitObserved,
		ExitCode:          message.ExitCode,
		EvidenceClass:     evidenceClass,
		Survivors:         message.Survivors,
	}
	return result, settlement, nil
}

func validateRequest(request Request) error {
	if request.Root == nil {
		return fmt.Errorf("%w: nil cell root", ErrInvalidRequest)
	}
	if len(request.Argv) == 0 || strings.TrimSpace(request.Argv[0]) == "" {
		return fmt.Errorf("%w: empty argument vector", ErrInvalidRequest)
	}
	if request.Dir != "" {
		clean := filepath.Clean(request.Dir)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: working directory escapes the cell root", ErrInvalidRequest)
		}
	}
	if request.Limits.MemoryBytes < 0 || request.Limits.PidsMax < 0 || request.Limits.CPUWeight < 0 {
		return fmt.Errorf("%w: negative limit", ErrInvalidRequest)
	}
	return nil
}

func resolvedEnv(env []string) []string {
	if env != nil {
		return append([]string(nil), env...)
	}
	return []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", "LANG=C"}
}

// newCellCommand builds the re-executed cell helper with the full namespace
// composition. The child is PID 1 of a fresh PID namespace.
func newCellCommand(config childConfig, extraFiles []*os.File) (*exec.Cmd, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	command := exec.Command("/proc/self/exe")
	command.Args = []string{"/proc/self/exe"}
	command.Env = append(os.Environ(), childEnvVar+"="+config.Mode, configEnvVar+"="+string(encoded))
	command.ExtraFiles = extraFiles
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID |
			syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNET,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		Setpgid:                    true,
	}
	return command, nil
}

// reapCell waits for pid using pidfd polling when available and wait4
// otherwise. On context cancellation it invokes cancel to let the cell's own
// supervisor revoke the scope, then escalates to SIGKILL after childGrace.
func reapCell(ctx context.Context, pid int, cancel func()) (int, bool, error) {
	fd, err := pidfdOpen(pid)
	if err != nil {
		return reapWait4(ctx, pid, cancel)
	}
	defer syscall.Close(fd)
	epoll, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return reapWait4(ctx, pid, cancel)
	}
	defer syscall.Close(epoll)
	event := &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(fd)}
	if err := syscall.EpollCtl(epoll, syscall.EPOLL_CTL_ADD, fd, event); err != nil {
		return reapWait4(ctx, pid, cancel)
	}
	canceled := false
	var graceDeadline time.Time
	for {
		if !canceled && ctx.Err() != nil {
			canceled = true
			if cancel != nil {
				cancel()
			}
			graceDeadline = time.Now().Add(childGrace)
		}
		if canceled && !graceDeadline.IsZero() && time.Now().After(graceDeadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			graceDeadline = time.Time{}
		}
		var events [1]syscall.EpollEvent
		count, err := syscall.EpollWait(epoll, events[:], 50)
		if err != nil && err != syscall.EINTR {
			return 0, false, err
		}
		if count > 0 {
			break
		}
	}
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &status, 0, nil); err != nil {
		return 0, false, err
	}
	code, observed := exitStatus(status)
	return code, observed, nil
}

func reapWait4(ctx context.Context, pid int, cancel func()) (int, bool, error) {
	type outcome struct {
		code     int
		observed bool
		err      error
	}
	results := make(chan outcome, 1)
	go func() {
		var status syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &status, 0, nil); err != nil {
			results <- outcome{err: err}
			return
		}
		code, observed := exitStatus(status)
		results <- outcome{code: code, observed: observed}
	}()
	select {
	case result := <-results:
		return result.code, result.observed, result.err
	case <-ctx.Done():
		if cancel != nil {
			cancel()
		}
		select {
		case result := <-results:
			return result.code, result.observed, result.err
		case <-time.After(childGrace):
			_ = syscall.Kill(pid, syscall.SIGKILL)
			result := <-results
			return result.code, result.observed, result.err
		}
	}
}

func exitStatus(status syscall.WaitStatus) (int, bool) {
	switch {
	case status.Exited():
		return status.ExitStatus(), true
	case status.Signaled():
		return 128 + int(status.Signal()), true
	default:
		return 0, false
	}
}

func pidfdOpen(pid int) (int, error) {
	fd, _, errno := syscall.Syscall(uintptr(sysPidfdOpen), uintptr(pid), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func copyAll(destination *bytes.Buffer, source io.Reader, limit int) {
	_, _ = io.Copy(destination, io.LimitReader(source, int64(limit)))
}

func kernelRelease() string {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		return ""
	}
	release := make([]byte, 0, len(uts.Release))
	for _, char := range uts.Release {
		if char == 0 {
			break
		}
		release = append(release, byte(char))
	}
	return string(release)
}

func newToken() string {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(token[:])
}

// setNoNewPrivs sets PR_SET_NO_NEW_PRIVS on the calling thread.
func setNoNewPrivs() error {
	if _, _, errno := syscall.RawSyscall6(uintptr(sysPrctl), 38, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", errno)
	}
	return nil
}

// dropCapabilities empties every capability set for the calling thread.
func dropCapabilities() error {
	header := struct {
		Version uint32
		PID     int32
	}{0x20080522, 0}
	var data [2]struct {
		Effective   uint32
		Permitted   uint32
		Inheritable uint32
	}
	if _, _, errno := syscall.Syscall(uintptr(sysCapset),
		uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&data[0])), 0); errno != 0 {
		return fmt.Errorf("capset(empty): %w", errno)
	}
	return nil
}
