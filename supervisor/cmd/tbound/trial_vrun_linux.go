//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/researchprofile"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/vrun"
)

// `tbound trial v-run <case_id>` runs Configuration V for one compliant case: the pinned
// native Pi, without the TBound extension, gate, service or oracles, once, on a fresh copy of
// the case fixture, in a plain rootless Podman container of the pinned cell image. It is
// descriptive: nothing here measures an effect. It prints one tbound-v-run/v1 document
// (vrun.Doc) with the identities, the container's exit, the usage the forwarder recorded and
// the acceptance result on the run's workspace.
//
//	work dir   <workRoot>/<random token>, root:root 0700 while root builds it, then given to
//	           the cell uid and gid. The workspace is <work dir>/ws, populated from the case's
//	           fixture artifact (it must recompute to the digest the pinned manifest gives), and
//	           mounted read-write at /work. Every run gets a new directory and a new copy, and
//	           removes it at the end, so no run sees another's files. The live root is not used.
//	podman     run with the identity of the cell uid and gid, no supplementary groups, no new
//	           privileges (setpriv), the service's own podman profile (HOME, XDG_*, the signed
//	           containers.conf and storage.conf), and a new process group, over the digest-pinned
//	           image reference taken from the registered cell template and never pulled.
//	container  read-only root file system, a tmpfs at /tmp, all capabilities dropped, the
//	           template's pids, memory and cpu limits, the cell uid mapped alone. Pi is the
//	           official CLI of the runtime bundle (mounted read-only at /runtime) run by the
//	           image's Node with `--mode json` and the case's trial prompt; see vrun.PodmanArgs.
//	provider   no provider key: the provider is reached through the unix socket of a forwarder
//	           that the research service (it holds the USD ledger and the key) opens for this unit,
//	           mounted read-only at /run/tbound/fwd with a per-unit credential; a relay joins Pi to it.
//	deadline   the manifest's duration is a hard deadline twice over: podman's own --timeout in
//	           the container and, a grace later, a SIGKILL to podman's process group followed by
//	           `podman rm -f` of the named container. A run that hit it reports timed_out.
//
// Stated limits of V: the network is podman's default and V has no egress filter; the provider key is
// replaced by a per-unit credential for the forwarder, which the agent can use within its request shape
// and the request allowance; Pi and its bash tool run as the cell uid with the image's tools and the
// default podman seccomp profile, without Landlock or the cell entrypoint; token and cost are the
// ledger's, not Pi's price table; a unit is admitted once.
const (
	// vWorkRoot holds the per-run work directories.
	vWorkRoot = "/var/lib/tbound/v-work"
	// vKillGrace is how long the host waits after the container deadline for podman to
	// exit by itself before it kills podman.
	vKillGrace = 15 * time.Second
	// vMaxDuration bounds the duration a manifest may ask of one V run.
	vMaxDuration = 24 * 3600
	// vTimedOutExit is the exit code reported for a run that was killed at the deadline.
	vTimedOutExit  = 137
	vCleanupWindow = 30 * time.Second
)

// vTemplate is what v-run takes from the registered cell template.
type vTemplate struct {
	// ImageRef is <repository>@<digest>, ImageDigest the digest the template pins.
	ImageRef, ImageDigest string
	PodmanPath, CrunPath  string
	CPUs                  string
	PidsMax, MemoryMB     int
	// HostUID and HostGID are the host identity podman runs as and the workspace belongs to: the
	// service's own uid and gid, which the single-own-uid-gid mapping gives the cell's processes.
	// CellUID and CellGID are the identity inside the container (--user and the id maps).
	HostUID, HostGID, CellUID, CellGID uint32
}

// loadVTemplate reads the registered D09 cell template: the same signed values the governed
// launcher uses for the image, podman, crun and the cell's limits and identity.
func loadVTemplate(ctx context.Context) (vTemplate, error) {
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return vTemplate{}, err
	}
	tmpl, _, err := piruntime.LoadD09CellTemplate(ctx)
	if err != nil {
		return vTemplate{}, err
	}
	return vTemplateFrom(registration, tmpl)
}

// vTemplateFrom is loadVTemplate over a registration and its template.
func vTemplateFrom(registration piruntime.D09ManagedLiveRootRegistration, tmpl piruntime.D09CellTemplate) (vTemplate, error) {
	// The container identity is only meaningful under the one admitted mapping (cellHostIdentity,
	// the same resolution reset uses).
	hostUID, hostGID, err := cellHostIdentity(registration, tmpl)
	if err != nil {
		return vTemplate{}, err
	}
	repository, _, _ := strings.Cut(tmpl.Image, ":")
	if at := strings.Index(repository, "@"); at >= 0 {
		repository = repository[:at]
	}
	if repository == "" || tmpl.CellUID <= 0 || tmpl.CellGID <= 0 {
		return vTemplate{}, errors.New("the registered cell template names no image or no cell identity")
	}
	return vTemplate{ImageRef: repository + "@" + tmpl.ExpectedImageDigest, ImageDigest: tmpl.ExpectedImageDigest,
		PodmanPath: tmpl.PodmanPath, CrunPath: tmpl.CrunPath, CPUs: tmpl.CPUs, PidsMax: tmpl.PidsMax, MemoryMB: tmpl.MemoryMB,
		HostUID: hostUID, HostGID: hostGID,
		CellUID: uint32(tmpl.CellUID), CellGID: uint32(tmpl.CellGID)}, nil
}

// vPreflight checks the guest state podman needs: the service's podman runtime directory.
// The libpod database is bound to it, so v-run uses that directory and does not make its own.
func vPreflight() error {
	info, err := os.Stat(piruntime.D09ManagedPiRuntimeDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("podman's runtime directory %s is not there (the tbound-pi service owns it): %v", piruntime.D09ManagedPiRuntimeDir, err)
	}
	return nil
}

func randomVToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// vPodmanEnv is the explicit environment of podman: the service's podman profile (the values
// the signed service unit fixes) and a plain PATH. Nothing is inherited, so no key or
// variable of the SSH session or of root reaches podman.
func vPodmanEnv() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + piruntime.D09ManagedPiPodmanHome,
		"USER=" + piruntime.D09ManagedPiServiceName, "LOGNAME=" + piruntime.D09ManagedPiServiceName,
		"XDG_RUNTIME_DIR=" + piruntime.D09ManagedPiRuntimeDir,
		"XDG_CONFIG_HOME=" + piruntime.D09ManagedPiPodmanConfigHome,
		"XDG_DATA_HOME=" + piruntime.D09ManagedPiPodmanDataHome,
		"XDG_CACHE_HOME=" + piruntime.D09ManagedPiPodmanCacheHome,
		"TMPDIR=" + piruntime.D09ManagedPiTempDir,
		"CONTAINERS_CONF=" + piruntime.D09ManagedPiContainersConfPath,
		"CONTAINERS_STORAGE_CONF=" + piruntime.D09ManagedPiStorageConfPath,
		"LANG=C", "LC_ALL=C",
	}
}

// podmanInvocation is one podman process: the binary, its arguments and environment, the
// identity it runs as and where its output goes.
type podmanInvocation struct {
	Path     string
	Args     []string
	Env      []string
	Dir      string
	UID, GID uint32
	Stdout   io.Writer
	Stderr   io.Writer
}

// podmanRunner is the seam between v-run and the host's podman. Run returns the exit code of
// podman. It returns a non-nil error when podman could not be run or observed, and ctx.Err()
// when ctx ended first (after it killed the process).
type podmanRunner interface {
	Run(ctx context.Context, inv podmanInvocation) (exit int, err error)
}

// execPodmanRunner runs podman as a child process of the invocation's identity. wrap is a
// command prefix (setpriv in production, to set no_new_privs once the identity is dropped).
type execPodmanRunner struct{ wrap []string }

var noNewPrivsWrap = []string{"/usr/bin/setpriv", "--no-new-privs", "--"}

func productionPodmanRunner() podmanRunner {
	return execPodmanRunner{wrap: noNewPrivsWrap}
}

func (r execPodmanRunner) Run(ctx context.Context, inv podmanInvocation) (int, error) {
	argv := append(append(append([]string(nil), r.wrap...), inv.Path), inv.Args...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = inv.Env, inv.Dir, inv.Stdout, inv.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if inv.UID != uint32(os.Geteuid()) || inv.GID != uint32(os.Getegid()) {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: inv.UID, Gid: inv.GID, Groups: []uint32{}}
	}
	// A deadline kills the whole process group: podman, conmon's client side and anything
	// they started.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return 128 + int(syscall.SIGKILL), ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.buf)) }

func parseTrialVRun(args []string) (string, error) {
	if len(args) != 1 {
		return "", errors.New("v-run takes exactly a case id")
	}
	if err := checkTrialCaseID(args[0]); err != nil {
		return "", err
	}
	return args[0], nil
}

func trialVRun(ctx context.Context, args []string, env trialEnv) (doc vrun.Doc, err error) {
	caseID, err := parseTrialVRun(args)
	if err != nil {
		return vrun.Doc{}, err
	}
	task := env.task
	c, err := task.pinnedCase(ctx, caseID)
	if err != nil {
		return vrun.Doc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	if c.Kind != evalcorpus.KindCompliant {
		return vrun.Doc{}, fmt.Errorf("case %q is %s: v-run applies to compliant cases only", caseID, c.Kind)
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return vrun.Doc{}, fmt.Errorf("case %q has no trial prompt in the pinned manifest", caseID)
	}
	if c.DurationSeconds == 0 || c.DurationSeconds > vMaxDuration {
		return vrun.Doc{}, fmt.Errorf("case %q has no usable trial duration (%d s)", caseID, c.DurationSeconds)
	}
	fixture, err := corpusadmit.LoadFixture(env.artifacts, c.FixtureDigest)
	if err != nil {
		return vrun.Doc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	accept, err := corpusadmit.LoadAccept(env.artifacts, c.AcceptDigest)
	if err != nil {
		return vrun.Doc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	tmpl, err := task.template(ctx)
	if err != nil {
		return vrun.Doc{}, fmt.Errorf("the registered cell cannot be resolved: %w", err)
	}
	if tmpl.HostUID != env.owner.UID || tmpl.HostGID != env.owner.GID {
		return vrun.Doc{}, fmt.Errorf("the cell's host identity %d:%d is not the fixture owner %d:%d", tmpl.HostUID, tmpl.HostGID, env.owner.UID, env.owner.GID)
	}
	if err := task.preflight(); err != nil {
		return vrun.Doc{}, err
	}
	token, err := task.token()
	if err != nil {
		return vrun.Doc{}, err
	}
	runDir := filepath.Join(task.workRoot, token)
	ws := filepath.Join(runDir, "ws")
	if err := makeVWorkspace(task.workRoot, runDir, ws, fixture, env.owner); err != nil {
		return vrun.Doc{}, errors.Join(err, os.RemoveAll(runDir))
	}
	defer func() {
		if rerr := os.RemoveAll(runDir); rerr != nil {
			err = errors.Join(err, fmt.Errorf("the work directory %s was not removed: %w", runDir, rerr))
		}
	}()
	// The root of the workspace is held from here on: what is evaluated afterwards is this
	// directory, whatever the run does to its path.
	tree, err := taskaccept.OpenRoot(ws)
	if err != nil {
		return vrun.Doc{}, err
	}
	defer tree.Close()

	name := vCellPrefix + token
	forwarder, err := task.forwarder(ctx, caseID, token)
	if err != nil {
		return vrun.Doc{}, err
	}
	defer forwarder.Abort()
	if err := checkVForwarderDir(forwarder.Dir(), tmpl.HostUID); err != nil {
		return vrun.Doc{}, err
	}
	podmanArgs, err := vrun.PodmanArgs(vrun.Spec{
		Name: name, ImageRef: tmpl.ImageRef, CrunPath: tmpl.CrunPath, UID: int(tmpl.CellUID), GID: int(tmpl.CellGID),
		Workspace: ws, RuntimeBundle: task.runtimeBundle, Forwarder: forwarder.Dir(), Model: researchprofile.Model,
		Prompt: c.Prompt, DurationSeconds: c.DurationSeconds, PidsMax: tmpl.PidsMax, MemoryMB: tmpl.MemoryMB, CPUs: tmpl.CPUs,
	})
	if err != nil {
		return vrun.Doc{}, err
	}
	inv := podmanInvocation{Path: tmpl.PodmanPath, Args: podmanArgs, Env: vPodmanEnv(), Dir: runDir, UID: tmpl.HostUID, GID: tmpl.HostGID}

	var usage vrun.UsageScanner
	stderr := &tailBuffer{max: 2048}
	inv.Stdout, inv.Stderr = &usage, stderr
	limit := task.deadline(c.DurationSeconds)
	runCtx, cancel := context.WithTimeout(ctx, limit+task.killGrace)
	startedAt := task.now()
	monotonic := time.Now()
	exit, runErr := task.podman.Run(runCtx, inv)
	elapsed := time.Since(monotonic)
	finishedAt := task.now()
	hitDeadline := errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	usage.Flush()

	killed := hitDeadline || (exit == vTimedOutExit && elapsed >= limit)
	if hitDeadline {
		exit, runErr = vTimedOutExit, nil
	}
	if killed || runErr != nil || ctx.Err() != nil {
		if rerr := removeVContainer(ctx, task, tmpl, name, runDir); rerr != nil {
			return vrun.Doc{}, rerr
		}
	}
	if ctx.Err() != nil {
		return vrun.Doc{}, ctx.Err()
	}
	if runErr != nil {
		return vrun.Doc{}, fmt.Errorf("podman could not be run: %w", runErr)
	}
	if !killed && exit >= 125 && exit <= 127 {
		return vrun.Doc{}, fmt.Errorf("podman failed (exit %d): %s", exit, stderr.String())
	}

	totals, err := forwarder.Finish()
	if err != nil {
		return vrun.Doc{}, fmt.Errorf("the unit's forwarder records are not available, so its cost is unknown: %w", err)
	}
	doc = vrun.Doc{Schema: vrun.Schema, CaseID: caseID, ImageDigest: tmpl.ImageDigest, FixtureDigest: c.FixtureDigest,
		Exit: exit, TimedOut: killed, TokensIn: totals.TokensIn, TokensOut: totals.TokensOut, CostMicroUSD: totals.CostMicroUSD,
		ToolCalls: usage.Usage().ToolCalls, UsageEvents: totals.Settled, LedgerRecords: totals.Booked, UncertainUnits: totals.Uncertain, Overage: totals.Overage,
		StartedNS: startedAt.UnixNano(), FinishedNS: finishedAt.UnixNano()}
	if result, aerr := taskaccept.EvaluateWith(caseID, accept, fixture, tree, cellNodeRunner{ctx: ctx, task: task}); aerr != nil {
		doc.AcceptError = aerr.Error()
	} else {
		doc.Accept = &result
	}
	return doc, nil
}

// makeVWorkspace builds <workRoot>/<token>/ws as root: a new private directory, the fixture in
// it, then the hand-over of both directories to the cell identity. Until the last step nothing
// but root can enter them, so nothing the cell uid runs can redirect the writes.
func makeVWorkspace(workRoot, runDir, ws string, fixture trialfix.Spec, owner trialfix.Owner) error {
	if err := os.MkdirAll(workRoot, 0o711); err != nil {
		return fmt.Errorf("the work root: %w", err)
	}
	if err := os.Chmod(workRoot, 0o711); err != nil {
		return fmt.Errorf("the work root: %w", err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return fmt.Errorf("the work directory: %w", err)
	}
	if err := os.Mkdir(ws, 0o700); err != nil {
		return fmt.Errorf("the workspace: %w", err)
	}
	if err := trialfix.Populate(ws, fixture, owner); err != nil {
		return err
	}
	for _, dir := range []string{ws, runDir} {
		if err := os.Lchown(dir, int(owner.UID), int(owner.GID)); err != nil {
			return fmt.Errorf("give %s to the cell identity: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// removeVContainer removes the named container by force through the podman seam, on a context
// that outlives the request.
func removeVContainer(parent context.Context, task trialTaskEnv, tmpl vTemplate, name, runDir string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), vCleanupWindow)
	defer cancel()
	stderr := &tailBuffer{max: 1024}
	exit, err := task.podman.Run(ctx, podmanInvocation{Path: tmpl.PodmanPath, Args: []string{"rm", "-f", "--time", "0", "--ignore", name},
		Env: vPodmanEnv(), Dir: runDir, UID: tmpl.HostUID, GID: tmpl.HostGID, Stderr: stderr})
	if err != nil || exit != 0 {
		return fmt.Errorf("container %s could not be removed (exit %d, %v): %s", name, exit, err, stderr.String())
	}
	return nil
}
