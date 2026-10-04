//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// init dispatches the re-executed cell helper. The same package is linked into
// the supervisor and the test binary, so init is the only place that can run
// between clone and execve for stage one and stage two.
func init() {
	switch os.Getenv(childEnvVar) {
	case "init":
		runInitCell()
	case "stage2":
		runStage2()
	case "probe":
		runProbeChild()
	}
}

type cellLimits struct {
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
	PidsMax     int64 `json:"pids_max,omitempty"`
	CPUWeight   int64 `json:"cpu_weight,omitempty"`
}

type childConfig struct {
	Mode   string     `json:"mode"`
	Token  string     `json:"token"`
	Argv   []string   `json:"argv,omitempty"`
	Env    []string   `json:"env,omitempty"`
	Dir    string     `json:"dir,omitempty"`
	Limits cellLimits `json:"limits,omitempty"`
}

type landlockRulePlan struct {
	Path   string `json:"path"`
	Access uint64 `json:"access"`
}

type stagePlan struct {
	RunPath     string             `json:"run_path"`
	Argv        []string           `json:"argv"`
	Env         []string           `json:"env"`
	Cwd         string             `json:"cwd"`
	Rules       []landlockRulePlan `json:"rules"`
	LandlockABI int                `json:"landlock_abi"`
}

type controlMessage struct {
	Stage               string   `json:"stage"`
	Error               string   `json:"error,omitempty"`
	ExitCode            int      `json:"exit_code,omitempty"`
	ExitObserved        bool     `json:"exit_observed,omitempty"`
	Interrupted         bool     `json:"interrupted,omitempty"`
	ProcessScopeEmpty   bool     `json:"process_scope_empty,omitempty"`
	Survivors           int      `json:"survivors,omitempty"`
	WritersStopped      bool     `json:"writers_stopped,omitempty"`
	MountDetached       bool     `json:"mount_detached,omitempty"`
	LandlockABI         int      `json:"landlock_abi,omitempty"`
	CgroupEstablished   bool     `json:"cgroup_established,omitempty"`
	CgroupKillConfirmed bool     `json:"cgroup_kill_confirmed,omitempty"`
	Notes               []string `json:"notes,omitempty"`
}

type probePayload struct {
	UID                int    `json:"uid"`
	Pid                int    `json:"pid"`
	Private            bool   `json:"private"`
	Tmpfs              bool   `json:"tmpfs"`
	Loopback           bool   `json:"loopback"`
	CapDrop            bool   `json:"cap_drop"`
	CapabilitiesEmpty  bool   `json:"capabilities_empty"`
	NoNewPrivs         bool   `json:"no_new_privs"`
	NoNewPrivsObserved bool   `json:"no_new_privs_observed"`
	LandlockABI        int    `json:"landlock_abi"`
	LandlockApplied    bool   `json:"landlock_applied"`
	Seccomp            bool   `json:"seccomp"`
	Error              string `json:"error,omitempty"`
}

type cellLayout struct {
	CellDir     string
	Workspace   string
	HelperPath  string
	RunPath     string
	LandlockABI int
	Rules       []landlockRulePlan
	mounts      []string
}

func (layout *cellLayout) recordMount(path string) {
	layout.mounts = append(layout.mounts, path)
}

// unmountAll detaches every cell mount and reports whether all detachments
// succeeded, with per-mount diagnostics. /proc is detached last so mountinfo
// remains readable while the other trees are removed.
func (layout *cellLayout) unmountAll() (bool, []string) {
	detached := true
	var notes []string
	for index := len(layout.mounts) - 1; index >= 0; index-- {
		if layout.mounts[index] == "/proc" {
			continue
		}
		if failures := detachMountTree(layout.mounts[index]); len(failures) > 0 {
			detached = false
			notes = append(notes, failures...)
		}
	}
	for _, mount := range layout.mounts {
		if mount != "/proc" {
			continue
		}
		if failures := detachMountTree(mount); len(failures) > 0 {
			detached = false
			notes = append(notes, failures...)
		}
	}
	if layout.CellDir != "" {
		if err := removeTree(layout.CellDir); err != nil {
			detached = false
			notes = append(notes, fmt.Sprintf("remove %s: %v", layout.CellDir, err))
		}
	}
	return detached, notes
}

func decodeConfig() (childConfig, error) {
	var config childConfig
	encoded := os.Getenv(configEnvVar)
	if encoded == "" {
		return config, errors.New("missing child configuration")
	}
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		return config, fmt.Errorf("decode child configuration: %w", err)
	}
	return config, nil
}

func decodePlan() (stagePlan, error) {
	var plan stagePlan
	encoded := os.Getenv(planEnvVar)
	if encoded == "" {
		return plan, errors.New("missing stage-two plan")
	}
	if err := json.Unmarshal([]byte(encoded), &plan); err != nil {
		return plan, fmt.Errorf("decode stage-two plan: %w", err)
	}
	return plan, nil
}

func firstArg(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

func resolveExecutable(argv0 string) (string, error) {
	if argv0 == "" {
		return "", errors.New("empty executable path")
	}
	if argv0 == "/proc/self/exe" {
		resolved, err := os.Executable()
		if err != nil {
			return "", err
		}
		argv0 = resolved
	}
	if !filepath.IsAbs(argv0) {
		return "", fmt.Errorf("executable path is not absolute: %q", argv0)
	}
	resolved, err := filepath.EvalSymlinks(argv0)
	if err != nil {
		return "", fmt.Errorf("resolve executable %q: %w", argv0, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("executable is a directory: %q", resolved)
	}
	return resolved, nil
}

func cellWorkingDirectory(workspace, dir string) (string, error) {
	if dir == "" {
		return workspace, nil
	}
	clean := filepath.Clean(dir)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid working directory %q", dir)
	}
	return filepath.Join(workspace, clean), nil
}

// setupCell mounts the writable cell root, read-only runtime binds, a private
// tmpfs /tmp and procfs, stages the executable, and brings up loopback.
func setupCell(token string, rootFD int, argv0 string) (*cellLayout, error) {
	layout := &cellLayout{}
	if err := makeMountsPrivate(); err != nil {
		return nil, err
	}
	base := ""
	for _, candidate := range []string{"/dev/shm", "/run/shm"} {
		if existingDirectory(candidate) {
			base = candidate
			break
		}
	}
	if base == "" {
		return nil, errors.New("no private staging directory (/dev/shm or /run/shm) is available")
	}
	layout.CellDir = filepath.Join(base, "tbound-cell-"+token)
	if err := os.Mkdir(layout.CellDir, 0o700); err != nil {
		return nil, fmt.Errorf("create cell directory: %w", err)
	}
	layout.Workspace = filepath.Join(layout.CellDir, "workspace")
	if err := os.Mkdir(layout.Workspace, 0o700); err != nil {
		return nil, fmt.Errorf("create cell workspace: %w", err)
	}
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", rootFD))
	if err != nil {
		return nil, fmt.Errorf("resolve supplied cell root: %w", err)
	}
	if err := bindMount(rootPath, layout.Workspace, false); err != nil {
		return nil, err
	}
	layout.recordMount(layout.Workspace)

	helperSource, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate helper executable: %w", err)
	}
	layout.HelperPath = filepath.Join(layout.CellDir, ".tbound-helper")
	if err := os.WriteFile(layout.HelperPath, nil, 0o700); err != nil {
		return nil, fmt.Errorf("create staged helper: %w", err)
	}
	if err := bindMount(helperSource, layout.HelperPath, false); err != nil {
		return nil, err
	}
	layout.recordMount(layout.HelperPath)

	realExec, err := resolveExecutable(argv0)
	if err != nil {
		return nil, err
	}
	execStaged := filepath.Join(layout.CellDir, "exec")
	if err := os.Mkdir(execStaged, 0o700); err != nil {
		return nil, fmt.Errorf("create executable stage: %w", err)
	}
	if err := bindMount(filepath.Dir(realExec), execStaged, false); err != nil {
		return nil, err
	}
	if err := remountReadOnly(execStaged); err != nil {
		return nil, err
	}
	layout.recordMount(execStaged)
	layout.RunPath = filepath.Join(execStaged, filepath.Base(realExec))

	readOnlyPaths := make([]string, 0, 4)
	for _, path := range []string{"/usr", "/etc", "/opt", "/var"} {
		if !existingDirectory(path) {
			continue
		}
		if err := bindMount(path, path, true); err != nil {
			return nil, err
		}
		if err := remountReadOnly(path); err != nil {
			return nil, err
		}
		layout.recordMount(path)
		readOnlyPaths = append(readOnlyPaths, path)
	}
	if err := mountTmpfs("/tmp", "mode=1777"); err != nil {
		return nil, err
	}
	layout.recordMount("/tmp")
	if err := mountProc("/proc"); err != nil {
		return nil, err
	}
	layout.recordMount("/proc")
	if err := bringUpLoopback(); err != nil {
		return nil, err
	}
	abi, err := landlockABI()
	if err != nil {
		return nil, err
	}
	layout.LandlockABI = abi
	readOnlyAccess := uint64(landlockFSReadFile | landlockFSReadDir | landlockFSExecute)
	layout.Rules = []landlockRulePlan{
		{Path: layout.Workspace, Access: landlockAccessAllABI1},
		{Path: "/tmp", Access: landlockAccessAllABI1},
		{Path: layout.CellDir, Access: readOnlyAccess},
		{Path: execStaged, Access: readOnlyAccess},
		{Path: "/dev", Access: uint64(landlockFSReadFile | landlockFSWriteFile | landlockFSReadDir)},
		{Path: "/proc", Access: readOnlyAccess},
	}
	for _, path := range readOnlyPaths {
		layout.Rules = append(layout.Rules, landlockRulePlan{Path: path, Access: readOnlyAccess})
	}
	return layout, nil
}

func runInitCell() {
	config, err := decodeConfig()
	if err != nil {
		os.Exit(120)
	}
	control := os.NewFile(controlFD, "cell-control")
	root := os.NewFile(rootFD, "cell-root")
	cancelFile := os.NewFile(cancelFD, "cell-cancel")
	if control == nil || root == nil {
		os.Exit(120)
	}
	// The cancel descriptor is not inherited across the stage-two execve.
	syscall.CloseOnExec(cancelFD)
	layout, err := setupCell(config.Token, int(root.Fd()), firstArg(config.Argv))
	_ = root.Close()
	if err != nil {
		emitControl(control, controlMessage{Stage: "stage2-error", Error: "cell setup: " + err.Error()})
		_ = control.Close()
		os.Exit(121)
	}
	fail := func(message string, code int) {
		layout.unmountAll()
		emitControl(control, controlMessage{Stage: "stage2-error", Error: message})
		_ = control.Close()
		os.Exit(code)
	}
	cwd, err := cellWorkingDirectory(layout.Workspace, config.Dir)
	if err != nil {
		fail(err.Error(), 122)
	}
	scope, scopeErr := establishCgroup(config.Token, Limits{
		MemoryBytes: config.Limits.MemoryBytes,
		PidsMax:     config.Limits.PidsMax,
		CPUWeight:   config.Limits.CPUWeight,
	})
	if scopeErr != nil {
		scope = nil
	}
	if scope != nil {
		if err := scope.addProcess(os.Getpid()); err != nil {
			scope.close()
			scope = nil
		}
	}
	plan := stagePlan{
		RunPath: layout.RunPath, Argv: config.Argv, Env: config.Env, Cwd: cwd,
		Rules: layout.Rules, LandlockABI: layout.LandlockABI,
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		fail("encode stage-two plan: "+err.Error(), 123)
	}
	stage2 := exec.Command(layout.HelperPath)
	stage2.Args = []string{layout.HelperPath}
	stage2.Env = append(os.Environ(), childEnvVar+"=stage2", planEnvVar+"="+string(encoded))
	stage2.Stdout = os.Stdout
	stage2.Stderr = os.Stderr
	stage2.ExtraFiles = []*os.File{control}
	stage2.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := stage2.Start(); err != nil {
		scope.close()
		fail("start target: "+err.Error(), 124)
	}
	wait := make(chan error, 1)
	go func() { wait <- stage2.Wait() }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGUSR1)
	var cancelCh <-chan struct{}
	if cancelFile != nil {
		channel := make(chan struct{}, 1)
		go func() {
			buffer := make([]byte, 1)
			_, _ = cancelFile.Read(buffer)
			channel <- struct{}{}
		}()
		cancelCh = channel
	}
	interrupted := false
	var waitErr error
	select {
	case waitErr = <-wait:
	case <-cancelCh:
		interrupted = true
		scopeKill(scope, stage2.Process.Pid)
		waitErr = <-wait
	case received := <-signals:
		interrupted = true
		emitControl(control, controlMessage{Stage: "interrupted", Error: received.String()})
		scopeKill(scope, stage2.Process.Pid)
		waitErr = <-wait
	}
	// Revoke the process group even on a clean exit so background survivors
	// cannot be mistaken for an empty scope.
	scopeKill(scope, stage2.Process.Pid)
	reapZombies()
	survivors, liveErr := countLiveProcesses()
	empty := liveErr == nil && survivors == 0
	if liveErr != nil {
		survivors = -1
	}
	exitCode, observed := exitStatusFromError(waitErr, stage2)
	detached, notes := layout.unmountAll()
	if scope != nil {
		scope.close()
	}
	emitControl(control, controlMessage{
		Stage:             "settlement",
		ExitCode:          exitCode,
		ExitObserved:      observed,
		Interrupted:       interrupted,
		ProcessScopeEmpty: empty,
		Survivors:         survivors,
		WritersStopped:    empty,
		MountDetached:     detached,
		LandlockABI:       layout.LandlockABI,
		CgroupEstablished: scope != nil,
		Notes:             notes,
	})
	_ = control.Close()
	os.Exit(exitCode)
}

func runStage2() {
	runtime.LockOSThread()
	control := os.NewFile(controlFD, "cell-control")
	plan, err := decodePlan()
	if err != nil {
		if control != nil {
			emitControl(control, controlMessage{Stage: "stage2-error", Error: err.Error()})
		}
		os.Exit(125)
	}
	if err := setupTarget(plan); err != nil {
		if control != nil {
			emitControl(control, controlMessage{Stage: "stage2-error", Error: err.Error()})
		}
		os.Exit(125)
	}
	if control != nil {
		emitControl(control, controlMessage{Stage: "stage2-ready"})
	}
	// The control descriptor must not survive execve; it stays open so a
	// failed exec can still be reported.
	syscall.CloseOnExec(controlFD)
	if err := syscall.Exec(plan.RunPath, plan.Argv, plan.Env); err != nil {
		if control != nil {
			emitControl(control, controlMessage{Stage: "stage2-error", Error: "execve: " + err.Error()})
		}
		os.Exit(126)
	}
}

func setupTarget(plan stagePlan) error {
	if err := os.Chdir(plan.Cwd); err != nil {
		return fmt.Errorf("change to cell working directory: %w", err)
	}
	if err := dropCapabilities(); err != nil {
		return err
	}
	if err := setNoNewPrivs(); err != nil {
		return err
	}
	rules := make([]landlockRule, 0, len(plan.Rules))
	for _, rule := range plan.Rules {
		rules = append(rules, landlockRule{path: rule.Path, allowedAccess: rule.Access})
	}
	if err := applyLandlock(rules); err != nil {
		return err
	}
	if err := installSeccomp(); err != nil {
		return err
	}
	return nil
}

func runProbeChild() {
	payload := probePayload{UID: syscall.Getuid(), Pid: syscall.Getpid()}
	if err := makeMountsPrivate(); err == nil {
		payload.Private = true
	} else {
		payload.Error = err.Error()
	}
	if err := mountTmpfs("/tmp", "mode=1777"); err == nil {
		payload.Tmpfs = true
	}
	if err := bringUpLoopback(); err == nil {
		payload.Loopback = true
	}
	if err := dropCapabilities(); err == nil {
		payload.CapDrop = true
	}
	if err := setNoNewPrivs(); err == nil {
		payload.NoNewPrivs = true
	}
	if status, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(status), "\n") {
			switch {
			case strings.HasPrefix(line, "NoNewPrivs:"):
				payload.NoNewPrivsObserved = strings.HasSuffix(strings.TrimSpace(line), "1")
			case strings.HasPrefix(line, "CapEff:"):
				fields := strings.Fields(line)
				if len(fields) == 2 && strings.Trim(fields[1], "0") == "" {
					payload.CapabilitiesEmpty = true
				}
			}
		}
	}
	if abi, err := landlockABI(); err == nil {
		payload.LandlockABI = abi
	}
	rules := []landlockRule{
		{path: "/tmp", allowedAccess: landlockAccessAllABI1},
		{path: "/usr", allowedAccess: landlockFSReadFile | landlockFSReadDir | landlockFSExecute},
	}
	if err := applyLandlock(rules); err == nil {
		payload.LandlockApplied = true
	} else if payload.Error == "" {
		payload.Error = err.Error()
	}
	if err := installSeccomp(); err == nil {
		payload.Seccomp = true
	} else if payload.Error == "" {
		payload.Error = err.Error()
	}
	encoded, _ := json.Marshal(payload)
	_, _ = os.Stdout.Write(append(encoded, '\n'))
	os.Exit(0)
}

func emitControl(file *os.File, message controlMessage) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return
	}
	_, _ = file.Write(append(encoded, '\n'))
}

func parseControl(data []byte) []controlMessage {
	var messages []controlMessage
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var message controlMessage
		if err := json.Unmarshal(line, &message); err == nil && message.Stage != "" {
			messages = append(messages, message)
		}
	}
	return messages
}

// scopeKill revokes the cell scope. It prefers cgroup.kill when the scope is
// writable and otherwise falls back to process-group SIGKILL.
func scopeKill(scope *cgroupScope, pgid int) {
	if scope != nil && scope.killWritable {
		if err := scope.kill(); err == nil {
			_ = scope.waitEmpty(2 * time.Second)
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

func reapZombies() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

func countLiveProcesses() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if pid == 1 {
			continue
		}
		count++
	}
	return count, nil
}

func exitStatusFromError(waitErr error, command *exec.Cmd) (int, bool) {
	if command.ProcessState != nil {
		return exitStatus(command.ProcessState.Sys().(syscall.WaitStatus))
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		return exitStatus(exitError.Sys().(syscall.WaitStatus))
	}
	if waitErr != nil {
		return 0, false
	}
	return 0, false
}

func removeTree(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return nil
}
