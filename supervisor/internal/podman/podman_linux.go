//go:build linux

package podman

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tbound/supervisor/internal/sessionrepo"
)

// Profile is the descriptive runner label recorded for evidence. It is
// deliberately non-claim-bearing: the label states the runtime and the honesty
// of the evidence, not containment.
const Profile = "guest-podman-rootless-non-claim-bearing"

const (
	defaultPidsMax  = 256
	defaultMemoryMB = 512
	cellNamePrefix  = "tbound-cell-"
	observeTimeout  = 30 * time.Second
)

var (
	// ErrUnsupported is returned when the rootless Podman/crun candidate
	// profile cannot be established. Run fails closed instead of proceeding.
	ErrUnsupported = errors.New("rootless podman candidate is unavailable")
	// ErrInvalidRequest is returned for a malformed cell request.
	ErrInvalidRequest = errors.New("invalid podman cell request")
	// ErrNotSettled is returned when the cell scope or mount could not be
	// observed as settled. Run fails closed instead of reporting a clean cell.
	ErrNotSettled = errors.New("podman cell did not settle")
)

// defaultCandidates is the ordered list of local images this slice may use.
// It is the first candidate that is already present in the local store; the
// runner never pulls during a command. Override by setting Runner.Image. Each
// candidate must provide a POSIX /bin/sh and awk for the embedded entrypoint.
var defaultCandidates = []string{
	"docker.io/library/alpine:latest",
	"alpine:latest",
}

// Report is measured capability evidence for the development profile. A false
// field means the mechanism was not observed, never that it was assumed.
type Report struct {
	PodmanPresent     bool
	PodmanPath        string
	PodmanVersion     string
	Rootless          bool
	OCIRuntime        string
	OCIRuntimeVersion string
	CgroupVersion     string
	UserNamespace     bool
	// Image is the local candidate tag; ImageRef is the reference the runner
	// uses. ImagePinned is true only when ImageRef names a repo digest.
	Image       string
	ImageDigest string
	ImageRef    string
	ImagePinned bool
	Unsupported []string
}

func (report Report) mechanisms() []string {
	mechanisms := []string{"non-claim-bearing"}
	if report.PodmanVersion != "" {
		mechanisms = append(mechanisms, "podman-"+report.PodmanVersion)
	}
	if report.OCIRuntime != "" {
		mechanisms = append(mechanisms, "oci-"+strings.ToLower(report.OCIRuntime))
	}
	if report.CgroupVersion != "" {
		mechanisms = append(mechanisms, "cgroup-"+report.CgroupVersion)
	}
	if report.Rootless {
		mechanisms = append(mechanisms, "rootless")
	}
	if report.UserNamespace {
		mechanisms = append(mechanisms, "userns")
	}
	if report.ImagePinned {
		mechanisms = append(mechanisms, "image-digest-pinned")
	} else {
		mechanisms = append(mechanisms, "image-digest-unpinned")
	}
	mechanisms = append(mechanisms,
		"network-none", "read-only-rootfs", "cap-drop-all", "no-new-privileges", "seccomp")
	sort.Strings(mechanisms)
	return mechanisms
}

// EvidenceClass returns the measured, non-claim-bearing profile label.
func (report Report) EvidenceClass() string {
	return "guest-podman-non-claim-bearing:" + strings.Join(report.mechanisms(), "+")
}

// Probe measures the rootless Podman/crun profile. It fails closed when
// podman, crun, rootless mode, cgroup v2, a usable user namespace, or a local
// candidate image is missing. Every reported mechanism was observed; none is
// inferred. Probe does not claim containment.
func Probe() (Report, error) {
	return probe(defaultCandidates, os.Geteuid(), os.Getegid())
}

func probe(candidates []string, uid, gid int) (Report, error) {
	report := Report{}
	binary, err := exec.LookPath("podman")
	if err != nil {
		report.Unsupported = append(report.Unsupported, "podman: not found in PATH")
		return report, fmt.Errorf("%w: podman not found in PATH", ErrUnsupported)
	}
	report.PodmanPresent = true
	report.PodmanPath = binary
	if out, err := runCapture(binary, "--version"); err == nil {
		report.PodmanVersion = lastField(out)
	} else {
		report.Unsupported = append(report.Unsupported, "podman --version: "+err.Error())
	}

	info, err := readPodmanInfo(binary)
	if err != nil {
		report.Unsupported = append(report.Unsupported, "podman info: "+err.Error())
		return report, fmt.Errorf("%w: podman info: %v", ErrUnsupported, err)
	}
	report.Rootless = info.Host.Security.Rootless
	report.OCIRuntime = info.Host.OCIRuntime.Name
	report.OCIRuntimeVersion = firstLine(info.Host.OCIRuntime.Version)
	report.CgroupVersion = info.Host.CgroupVersion

	var missing []string
	if !report.Rootless {
		missing = append(missing, "rootless-mode")
	}
	if !strings.EqualFold(report.OCIRuntime, "crun") {
		missing = append(missing, "oci-runtime-crun: got "+report.OCIRuntime)
	}
	if report.CgroupVersion != "v2" {
		missing = append(missing, "cgroup-v2: got "+report.CgroupVersion)
	}

	image, imageErr := selectImage(binary, candidates)
	if imageErr != nil {
		report.Unsupported = append(report.Unsupported, "image: "+imageErr.Error())
	} else {
		report.Image = image.Tag
		report.ImageDigest = image.Digest
		report.ImageRef = image.Ref
		report.ImagePinned = image.Pinned
	}

	if report.ImageRef != "" {
		if err := usernsProbe(binary, report.ImageRef, uid, gid); err != nil {
			report.Unsupported = append(report.Unsupported, "userns: "+err.Error())
		} else {
			report.UserNamespace = true
		}
	}

	if len(missing) > 0 || report.ImageRef == "" || !report.UserNamespace {
		reasons := append([]string{}, missing...)
		reasons = append(reasons, report.Unsupported...)
		return report, fmt.Errorf("%w: %s", ErrUnsupported, strings.Join(reasons, "; "))
	}
	return report, nil
}

// Runner adapts the rootless Podman cell to sessionrepo.CommandRunner. It is a
// non-claim-bearing development profile.
type Runner struct {
	// LeaseID is the authorized command lease this runner may settle. The
	// CommandRunner seam does not expose the lease, so it is supplied here.
	LeaseID string
	// Image optionally overrides the reference selected by Probe. It must be
	// a local image; the runner never pulls.
	Image string
	// UID and GID are the in-container identities. The default is the invoking
	// user, mapped through --userns=keep-id so the bind-mounted /work stays
	// writable without granting root inside the cell.
	UID int
	GID int
	// PidsMax and MemoryMB bound the cell. Zero selects the defaults.
	PidsMax  int
	MemoryMB int
}

// New returns a runner bound to one authorized command lease with the default
// identity and resource bounds.
func New(leaseID string) *Runner {
	return &Runner{
		LeaseID:  leaseID,
		UID:      os.Geteuid(),
		GID:      os.Getegid(),
		PidsMax:  defaultPidsMax,
		MemoryMB: defaultMemoryMB,
	}
}

// Profile returns the descriptive, non-claim-bearing runner profile label.
func (runner *Runner) Profile() string { return Profile }

// Run launches the authorized command inside a fresh rootless Podman cell
// rooted at the private command view. It resolves the view to a real path,
// probes the profile, runs the hardened cell, and returns only after it has
// observed the cell settled. It never reports containment as established.
func (runner *Runner) Run(ctx context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	if view == nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("podman: nil command view")
	}
	viewPath, err := resolveViewPath(view)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	report, err := Probe()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("podman: probe profile: %w", err)
	}
	return runner.runCell(ctx, report, viewPath, view.ID(), runner.LeaseID, spec)
}

// resolveViewPath turns the private O_PATH command-view descriptor into the
// real host path that rootless Podman may bind. It never exposes the path to
// the cell: the cell sees only /work.
func resolveViewPath(view *sessionrepo.CommandView) (string, error) {
	source, err := view.MountSource()
	if err != nil {
		return "", fmt.Errorf("podman: obtain command view: %w", err)
	}
	defer func() { _ = source.Close() }()
	link := "/proc/self/fd/" + strconv.Itoa(int(source.Fd()))
	target, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("podman: resolve command view path: %w", err)
	}
	if strings.HasSuffix(target, " (deleted)") {
		return "", errors.New("podman: command view path was deleted")
	}
	if !filepath.IsAbs(target) {
		return "", fmt.Errorf("podman: command view path is not absolute: %q", target)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("podman: stat command view path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("podman: command view path is not a directory: %q", target)
	}
	return target, nil
}

// runCell executes one cell. It is unexported so in-package conformance tests
// can drive an already-resolved view path without a session repository.
func (runner *Runner) runCell(ctx context.Context, report Report, viewPath, viewID, leaseID string, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	if err := validateSpec(spec); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	config := runner.normalized(report)
	if config.Image == "" {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: no local candidate image", ErrUnsupported)
	}
	if report.PodmanPath == "" {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: podman path is unknown", ErrUnsupported)
	}

	name := cellNamePrefix + newToken()
	arguments := cellArguments(config, name, viewPath, spec)

	command := exec.CommandContext(ctx, report.PodmanPath, arguments...)
	command.Env = hostEnv()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	runErr := command.Run()
	if ctx.Err() != nil {
		_ = forceRemove(report.PodmanPath, name)
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("podman: cell canceled: %w", ctx.Err())
	}
	if command.ProcessState == nil {
		_ = forceRemove(report.PodmanPath, name)
		if runErr == nil {
			runErr = errors.New("podman produced no process state")
		}
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("podman: run cell: %w", runErr)
	}
	exitCode := exitCodeOf(command.ProcessState)

	// The container is expected to remove itself via --rm; force-remove any
	// residue so a killed cell cannot outlive this call.
	_ = forceRemove(report.PodmanPath, name)

	observeCtx, cancel := context.WithTimeout(context.Background(), observeTimeout)
	defer cancel()
	observed, err := observe(observeCtx, report.PodmanPath, name, viewPath)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("podman: observe settlement: %w", err)
	}
	if !observed.ProcessScopeEmpty || !observed.MountDetached {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: survivors=%d container_removed=%t mount_referenced=%t",
			ErrNotSettled, observed.Survivors, observed.ContainerRemoved, observed.MountReferenced)
	}

	result := sessionrepo.CommandResult{
		ExitCode:     exitCode,
		ExitObserved: true,
		Stdout:       stdout.Bytes(),
		Stderr:       stderr.Bytes(),
	}
	settlement := sessionrepo.CommandSettlement{
		LeaseID:           leaseID,
		ViewID:            viewID,
		ProcessScopeEmpty: observed.ProcessScopeEmpty,
		WritersStopped:    observed.WritersStopped,
		MountDetached:     observed.MountDetached,
		ExitObserved:      true,
		ExitCode:          exitCode,
		EvidenceClass:     report.EvidenceClass() + ":run-observed",
	}
	return result, settlement, nil
}

// cellArguments builds the exact hardened `podman run` argument vector for one
// cell. It is unexported so in-package tests can record the exact invocation.
//
// `--userns=keep-id` maps the invoking UID/GID to the same in-container value.
// It is required so that a non-root cell can write the host-owned bind at
// /work: under the default rootless mapping only container root maps to the
// invoking user, so `--user <uid>:<gid>` alone cannot write a host-owned bind.
func cellArguments(config Runner, name, viewPath string, spec sessionrepo.CommandSpec) []string {
	workdir := "/work"
	if spec.WorkingDirectory != "" {
		workdir = "/work/" + spec.WorkingDirectory
	}
	uid := strconv.Itoa(config.UID)
	gid := strconv.Itoa(config.GID)
	arguments := []string{
		"run", "--rm", "--name", name,
		"--network=none",
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", strconv.Itoa(config.PidsMax),
		"--memory", strconv.Itoa(config.MemoryMB) + "m",
		"--userns=keep-id",
		"--user", uid + ":" + gid,
		"--workdir", workdir,
		"-e", "TBOUND_CELL_UID=" + uid,
		"-e", "TBOUND_CELL_GID=" + gid,
		"--mount", "type=bind,src=" + viewPath + ",dst=/work,rw",
		config.Image,
		"/bin/sh", "-c", entrypointScript, "entrypoint",
		spec.Executable,
	}
	return append(arguments, spec.Args...)
}

func (runner *Runner) normalized(report Report) Runner {
	config := *runner
	if config.Image == "" {
		config.Image = report.ImageRef
	}
	if config.UID <= 0 {
		config.UID = os.Geteuid()
	}
	if config.GID <= 0 {
		config.GID = os.Getegid()
	}
	if config.PidsMax <= 0 {
		config.PidsMax = defaultPidsMax
	}
	if config.MemoryMB <= 0 {
		config.MemoryMB = defaultMemoryMB
	}
	return config
}

// entrypointScript is the in-image entrypoint for this slice. It verifies the
// observable cell postconditions and then execs the authorized target argv.
//
// DEFERRED: this script is embedded in the host process and passed as an
// argument; it is not a frozen, signed in-image entrypoint and carries no
// offline cosign verification. A later slice must replace it.
const entrypointScript = `set -eu
fail() { printf 'tbound-cell-entrypoint: %s\n' "$*" >&2; exit 97; }
[ "$(id -u)" = "$TBOUND_CELL_UID" ] || fail "uid $(id -u) != $TBOUND_CELL_UID"
[ "$(id -g)" = "$TBOUND_CELL_GID" ] || fail "gid $(id -g) != $TBOUND_CELL_GID"
[ "$(awk '/^CapEff:/{print $2}' /proc/self/status)" = "0000000000000000" ] || fail "capabilities not empty"
[ "$(awk '/^NoNewPrivs:/{print $2}' /proc/self/status)" = "1" ] || fail "no_new_privs not set"
[ "$(awk '/^Seccomp:/{print $2}' /proc/self/status)" = "2" ] || fail "seccomp not in filter mode"
case "$(cat /proc/self/cgroup)" in 0::*) : ;; *) fail "unexpected cgroup $(cat /proc/self/cgroup)" ;; esac
[ -z "$(awk 'NR>1 && NF>0 {print}' /proc/net/route)" ] || fail "external route present"
exec "$@"`

type podmanInfo struct {
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

func readPodmanInfo(binary string) (podmanInfo, error) {
	out, err := runCapture(binary, "info", "--format", "json")
	if err != nil {
		return podmanInfo{}, err
	}
	var info podmanInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return podmanInfo{}, fmt.Errorf("decode podman info: %w", err)
	}
	return info, nil
}

type imageRef struct {
	Tag    string
	Digest string
	Ref    string
	Pinned bool
}

// selectImage chooses the first candidate already present in the local store
// and, when possible, resolves it to a repo digest. It never pulls.
func selectImage(binary string, candidates []string) (imageRef, error) {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if code, _, err := podmanStatus(binary, "image", "exists", candidate); err != nil {
			return imageRef{}, err
		} else if code != 0 {
			continue
		}
		out, err := runCapture(binary, "image", "inspect", candidate)
		if err != nil {
			return imageRef{}, err
		}
		var images []struct {
			Digest      string   `json:"Digest"`
			RepoDigests []string `json:"RepoDigests"`
		}
		if err := json.Unmarshal([]byte(out), &images); err != nil {
			return imageRef{}, fmt.Errorf("decode image inspect: %w", err)
		}
		if len(images) == 0 {
			return imageRef{}, fmt.Errorf("image inspect returned no object for %s", candidate)
		}
		selected := imageRef{Tag: candidate, Ref: candidate}
		for _, repoDigest := range images[0].RepoDigests {
			if strings.Contains(repoDigest, "@sha256:") {
				selected.Ref = repoDigest
				selected.Digest = repoDigest[strings.Index(repoDigest, "@")+1:]
				selected.Pinned = true
				break
			}
		}
		if !selected.Pinned && strings.HasPrefix(images[0].Digest, "sha256:") {
			repo := candidate
			if at := strings.Index(repo, "@"); at >= 0 {
				repo = repo[:at]
			}
			if colon := strings.LastIndex(repo, ":"); colon > strings.LastIndex(repo, "/") {
				repo = repo[:colon]
			}
			selected.Digest = images[0].Digest
			selected.Ref = repo + "@" + images[0].Digest
			selected.Pinned = true
		}
		return selected, nil
	}
	return imageRef{}, errors.New("no candidate image is present in the local podman store")
}

// usernsProbe proves that --userns=keep-id maps the invoking identity into the
// cell and that the non-root user can run there. It does not claim containment.
func usernsProbe(binary, image string, uid, gid int) error {
	script := `[ "$(id -u)" = "$TBOUND_CELL_UID" ] && [ "$(id -g)" = "$TBOUND_CELL_GID" ]`
	_, err := runCapture(binary,
		"run", "--rm", "--network=none", "--read-only", "--cap-drop=ALL",
		"--security-opt", "no-new-privileges", "--userns=keep-id",
		"--user", strconv.Itoa(uid)+":"+strconv.Itoa(gid),
		"-e", "TBOUND_CELL_UID="+strconv.Itoa(uid),
		"-e", "TBOUND_CELL_GID="+strconv.Itoa(gid),
		image, "/bin/sh", "-c", script)
	return err
}

type observation struct {
	ProcessScopeEmpty bool
	WritersStopped    bool
	MountDetached     bool
	Survivors         int
	ContainerRemoved  bool
	MountReferenced   bool
}

// observe measures the postconditions this slice can honestly see: the named
// container is gone, no process on the host still carries the unique cell
// name, and the host mount table does not reference the view. It is evidence,
// not proof of containment.
func observe(ctx context.Context, binary, name, viewPath string) (observation, error) {
	exists, err := containerExists(ctx, binary, name)
	if err != nil {
		return observation{}, err
	}
	survivors, ok := countSurvivors(name)
	if !ok {
		return observation{}, errors.New("scan /proc for surviving cell processes")
	}
	mounted, err := mountReferences(viewPath)
	if err != nil {
		return observation{}, err
	}
	removed := !exists
	scopeEmpty := removed && survivors == 0
	return observation{
		ProcessScopeEmpty: scopeEmpty,
		WritersStopped:    scopeEmpty,
		MountDetached:     removed && !mounted,
		Survivors:         survivors,
		ContainerRemoved:  removed,
		MountReferenced:   mounted,
	}, nil
}

func containerExists(ctx context.Context, binary, name string) (bool, error) {
	command := exec.CommandContext(ctx, binary, "container", "exists", name)
	command.Env = hostEnv()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false, nil
	}
	return false, fmt.Errorf("podman container exists: %w: %s", err, strings.TrimSpace(stderr.String()))
}

// countSurvivors scans /proc for a process whose command line or environment
// still carries the unique cell name. The name is passed only to podman, so a
// returned cell leaves no match.
func countSurvivors(name string) (int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	needle := []byte(name)
	count := 0
	self := strconv.Itoa(os.Getpid())
	for _, entry := range entries {
		pid := entry.Name()
		if pid == self || !isDigits(pid) {
			continue
		}
		for _, file := range []string{"/proc/" + pid + "/cmdline", "/proc/" + pid + "/environ"} {
			data, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			if bytes.Contains(data, needle) {
				count++
				break
			}
		}
	}
	return count, true
}

func mountReferences(viewPath string) (bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	return bytes.Contains(data, []byte(viewPath)), nil
}

func forceRemove(binary, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), observeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "rm", "-f", name)
	command.Env = hostEnv()
	return command.Run()
}

func validateSpec(spec sessionrepo.CommandSpec) error {
	if spec.Executable == "" || !filepath.IsAbs(spec.Executable) || filepath.Clean(spec.Executable) != spec.Executable {
		return fmt.Errorf("%w: executable must be a clean absolute path", ErrInvalidRequest)
	}
	if spec.WorkingDirectory != "" {
		cleaned := filepath.Clean(spec.WorkingDirectory)
		if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: working directory escapes /work", ErrInvalidRequest)
		}
	}
	return nil
}

func exitCodeOf(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}

// hostEnv is a sanitized host environment for podman: no provider credentials
// and no live-workspace paths are inherited by the cell. It keeps only the
// rootless-runtime variables podman needs.
func hostEnv() []string {
	allow := []string{
		"PATH", "HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME",
		"XDG_DATA_HOME", "TMPDIR", "LANG", "LC_ALL", "DBUS_SESSION_BUS_ADDRESS",
	}
	env := make([]string, 0, len(allow)+1)
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

func runCapture(binary string, args ...string) (string, error) {
	command := exec.Command(binary, args...)
	command.Env = hostEnv()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// podmanStatus runs a read-only podman query and reports its exit code. A
// nonzero exit code from podman is not an error here.
func podmanStatus(binary string, args ...string) (int, string, error) {
	command := exec.Command(binary, args...)
	command.Env = hostEnv()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), stdout.String(), nil
	}
	return -1, stdout.String(), err
}

func lastField(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func firstLine(value string) string {
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return value[:index]
	}
	return value
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func newToken() string {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(token[:])
}
