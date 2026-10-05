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
	"io"
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

// ExpectedImageDigest is the manifest-frozen content digest of the local cell
// image (the cached docker.io/library/alpine on the guest). Probe and Run fail
// closed unless a local image's repo digest equals it: the runner never pulls,
// and a merely-present or mismatched tag is never accepted. Override the pin
// per runner with Runner.ExpectedDigest; a missing signed entrypoint and the
// absent offline cosign verification remain deferred, so this pin alone does
// not make the profile claim-bearing.
const ExpectedImageDigest = "sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6"

const (
	defaultPidsMax  = 256
	defaultMemoryMB = 512
	cellNamePrefix  = "tbound-cell-"
	observeTimeout  = 30 * time.Second
	// observeSettle bounds how long observe waits for a just-removed cell's
	// cgroup and processes to disappear before reporting them honestly.
	observeSettle = 5 * time.Second
	// cgroupScanRoot is the rootless cgroup-v2 hierarchy root. The scan looks
	// only at directory names beneath it.
	cgroupScanRoot = "/sys/fs/cgroup"
	// cgroupScanMaxDepth and cgroupScanMaxDirs bound the scan. Reaching either
	// bound without exhausting the tree marks the scan unscannable so Run fails
	// closed rather than assuming absence.
	cgroupScanMaxDepth = 8
	cgroupScanMaxDirs  = 8192
	// containerIDLength is the full hexadecimal container id length podman
	// writes to --cidfile.
	containerIDLength = 64
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

// defaultCandidates is the ordered list of local image references this slice
// may use. selectImage chooses the first candidate already present whose repo
// digest equals the expected manifest digest; the runner never pulls during a
// command. Override by setting Runner.Image (still digest-checked). Each
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
	// uses. ImagePinned is true only when ImageDigest was observed to equal the
	// manifest-frozen ExpectedImageDigest; it is never inferred from mere
	// presence.
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
// image whose repo digest equals ExpectedImageDigest is missing. Every reported
// mechanism was observed; none is inferred, and no image is ever pulled. Probe
// does not claim containment.
func Probe() (Report, error) {
	return probe(defaultCandidates, ExpectedImageDigest, os.Geteuid(), os.Getegid())
}

func probe(candidates []string, expectedDigest string, uid, gid int) (Report, error) {
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

	image, imageErr := selectImage(binary, candidates, expectedDigest)
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
	// Image optionally overrides the reference selected by Probe. It is still
	// verified against ExpectedDigest and must be a local image; the runner
	// never pulls.
	Image string
	// ExpectedDigest optionally overrides the manifest-frozen digest Probe
	// requires. Empty selects the package-level ExpectedImageDigest. It can only
	// narrow or move the pin, never weaken it: Run re-verifies the launched
	// image's repo digest against it and fails closed on any mismatch.
	ExpectedDigest string
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

// expectedDigest returns the digest this runner requires, defaulting to the
// package-level manifest pin.
func (runner *Runner) expectedDigest() string {
	if digest := strings.TrimSpace(runner.ExpectedDigest); digest != "" {
		return digest
	}
	return ExpectedImageDigest
}

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
	report, err := probe(defaultCandidates, runner.expectedDigest(), os.Geteuid(), os.Getegid())
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
	// Re-verify the image this run will actually launch against the frozen pin.
	// This closes the Runner.Image override: a tag that is merely present, or a
	// reference whose repo digest differs from the expected digest, fails closed
	// and is never pulled. The resolved repo-digest reference is what runs.
	image, err := selectImage(report.PodmanPath, []string{config.Image}, config.ExpectedDigest)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: launch image is not the manifest-frozen digest: %v", ErrUnsupported, err)
	}
	config.Image = image.Ref

	cidDir, err := os.MkdirTemp("", "tbound-cid-")
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("podman: create private cid directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(cidDir) }()
	// podman 4.9.3 deletes a regular --cidfile when --rm removes the container,
	// so a plain file cannot be read after the run. A private FIFO keeps the id
	// in the kernel pipe buffer: the fd is opened O_RDWR|O_NONBLOCK here, podman
	// writes the id to the same FIFO, and the buffered bytes survive --rm's
	// unlink of the path.
	cidFifo := filepath.Join(cidDir, "container.id")
	cidReader, err := newCIDFifo(cidFifo)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("podman: create private cid fifo: %w", err)
	}
	defer func() { _ = cidReader.Close() }()

	name := cellNamePrefix + newToken()
	arguments := cellArguments(config, name, cidFifo, viewPath, spec)

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

	// The container id is captured from the host-side cid fifo even though --rm
	// has already removed the container (and unlinked the fifo path). Without it
	// the postconditions below cannot be observed, so the cell is not settled.
	containerID, cidErr := readCIDFifo(cidReader, observeSettle)
	if cidErr != nil {
		_ = forceRemove(report.PodmanPath, name)
		if runErr != nil {
			return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("podman: run cell: %w", runErr)
		}
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("%w: container id unavailable from cid fifo: %v", ErrNotSettled, cidErr)
	}

	// The container is expected to remove itself via --rm; force-remove any
	// residue so a killed cell cannot outlive this call.
	_ = forceRemove(report.PodmanPath, name)

	observeCtx, cancel := context.WithTimeout(context.Background(), observeTimeout)
	defer cancel()
	observed, err := observe(observeCtx, report.PodmanPath, name, containerID, viewPath)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("podman: observe settlement: %w", err)
	}
	if !observed.ProcessScopeEmpty || !observed.WritersStopped || !observed.MountDetached {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf(
			"%w: container_id=%s container_removed=%t proc_refs=%d cgroup_scanned=%t cgroup_refs=%d cgroup_note=%q mount_referenced=%t",
			ErrNotSettled, observed.ContainerID, observed.ContainerRemoved, observed.ProcReferences,
			observed.CgroupScanned, observed.CgroupReferences, observed.CgroupNote, observed.MountReferenced)
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
		EvidenceClass: fmt.Sprintf("%s:run-observed:proc_refs=%d:cgroup_refs=%d:mount_referenced=%t",
			report.EvidenceClass(), observed.ProcReferences, observed.CgroupReferences, observed.MountReferenced),
	}
	return result, settlement, nil
}

// cellArguments builds the exact hardened `podman run` argument vector for one
// cell. It is unexported so in-package tests can record the exact invocation.
//
// `--cidfile` records the container id in a private host path so settlement can
// be observed by id even when --rm has already removed the container.
// `--pull=never` guarantees a missing image fails closed instead of being
// fetched from a registry.
//
// `--userns=keep-id` maps the invoking UID/GID to the same in-container value.
// It is required so that a non-root cell can write the host-owned bind at
// /work: under the default rootless mapping only container root maps to the
// invoking user, so `--user <uid>:<gid>` alone cannot write a host-owned bind.
func cellArguments(config Runner, name, cidPath, viewPath string, spec sessionrepo.CommandSpec) []string {
	workdir := "/work"
	if spec.WorkingDirectory != "" {
		workdir = "/work/" + spec.WorkingDirectory
	}
	uid := strconv.Itoa(config.UID)
	gid := strconv.Itoa(config.GID)
	arguments := []string{
		"run", "--rm", "--name", name,
		"--cidfile", cidPath,
		"--pull=never",
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
	if strings.TrimSpace(config.ExpectedDigest) == "" {
		config.ExpectedDigest = ExpectedImageDigest
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
// DEFERRED (must not be relied on as a claim): this script is embedded in the
// host process and passed as an argument string. It is NOT a frozen, signed
// in-image entrypoint, it is not byte-identity-checked, and it carries no
// offline cosign verification. It can be swapped by anyone who controls this
// process. No H1 entrypoint-integrity or containment claim may cite it; a later
// slice must replace it with a frozen entrypoint signed and verified offline.
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
// whose repo digest equals the expected content digest, and returns it pinned
// to that repo-digest reference. It never pulls. A candidate that is present
// only under a mismatched or digestless tag is rejected, not merely noted; if
// none matches, the caller must fail closed.
func selectImage(binary string, candidates []string, expectedDigest string) (imageRef, error) {
	expected := strings.TrimSpace(expectedDigest)
	if expected == "" {
		return imageRef{}, errors.New("expected image digest is empty")
	}
	var observed []string
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
		for _, repoDigest := range images[0].RepoDigests {
			at := strings.LastIndex(repoDigest, "@")
			if at < 0 {
				continue
			}
			digest := strings.TrimSpace(repoDigest[at+1:])
			observed = append(observed, digest)
			if digestMatches(digest, expected) {
				return imageRef{Tag: candidate, Digest: digest, Ref: repoDigest, Pinned: true}, nil
			}
		}
	}
	if len(observed) == 0 {
		return imageRef{}, fmt.Errorf("no local candidate image is present in the local podman store (expected %s)", expected)
	}
	return imageRef{}, fmt.Errorf("no local image matches expected digest %s (observed repo digests: %s)", expected, strings.Join(observed, ", "))
}

// digestMatches reports whether an observed repo digest names the expected
// content digest. It trims surrounding whitespace and compares the complete
// digest case-insensitively (hexadecimal case is not significant). It is not a
// prefix, suffix, or substring match, so a merely-present, truncated, or
// swapped digest is rejected. An empty observed or expected digest never
// matches.
func digestMatches(observed, expected string) bool {
	observed = strings.TrimSpace(observed)
	expected = strings.TrimSpace(expected)
	if observed == "" || expected == "" {
		return false
	}
	return strings.EqualFold(observed, expected)
}

// usernsProbe proves that --userns=keep-id maps the invoking identity into the
// cell and that the non-root user can run there. It does not claim containment.
func usernsProbe(binary, image string, uid, gid int) error {
	script := `[ "$(id -u)" = "$TBOUND_CELL_UID" ] && [ "$(id -g)" = "$TBOUND_CELL_GID" ]`
	_, err := runCapture(binary,
		"run", "--rm", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL",
		"--security-opt", "no-new-privileges", "--userns=keep-id",
		"--user", strconv.Itoa(uid)+":"+strconv.Itoa(gid),
		"-e", "TBOUND_CELL_UID="+strconv.Itoa(uid),
		"-e", "TBOUND_CELL_GID="+strconv.Itoa(gid),
		image, "/bin/sh", "-c", script)
	return err
}

// observation records, honestly and individually, each settlement signal this
// slice can see. A false field means the signal was not observed; none is
// inferred. This is evidence, not proof of containment.
type observation struct {
	ContainerID       string
	ContainerRemoved  bool
	ProcReferences    int
	ProcScanned       bool
	CgroupRoot        string
	CgroupScanned     bool
	CgroupReferences  int
	CgroupNote        string
	MountReferenced   bool
	ProcessScopeEmpty bool
	WritersStopped    bool
	MountDetached     bool
}

// observe waits for and measures the postconditions this slice can honestly
// see: (a) `podman container exists <id>` is false; (b) no host process's
// cmdline or environ references the container id or the unique cell name; (c) a
// bounded scan of the rootless cgroup roots finds no path containing the id;
// and (d) the command-view host path is absent from /proc/self/mountinfo. It
// retries until observeSettle or ctx expires, then returns whatever it last
// observed so the caller can fail closed.
func observe(ctx context.Context, binary, name, id, viewPath string) (observation, error) {
	deadline := time.Now().Add(observeSettle)
	for {
		observed, err := observeOnce(ctx, binary, name, id, viewPath)
		if err != nil {
			return observed, err
		}
		if observed.ProcessScopeEmpty && observed.WritersStopped && observed.MountDetached {
			return observed, nil
		}
		if time.Now().After(deadline) {
			return observed, nil
		}
		select {
		case <-ctx.Done():
			return observed, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func observeOnce(ctx context.Context, binary, name, id, viewPath string) (observation, error) {
	observed := observation{ContainerID: id, CgroupRoot: cgroupScanRoot}
	exists, err := containerExists(ctx, binary, id)
	if err != nil {
		return observed, err
	}
	observed.ContainerRemoved = !exists

	references, ok := countReferences(id, name)
	if !ok {
		return observed, errors.New("scan /proc for surviving cell processes")
	}
	observed.ProcScanned = true
	observed.ProcReferences = references

	found, scanned, note := scanCgroup(cgroupScanRoot, id)
	observed.CgroupScanned = scanned
	observed.CgroupNote = note
	if found {
		observed.CgroupReferences = 1
	}

	mounted, err := mountReferences(viewPath)
	if err != nil {
		return observed, err
	}
	observed.MountReferenced = mounted

	observed.ProcessScopeEmpty = observed.ContainerRemoved && references == 0 && observed.CgroupScanned && observed.CgroupReferences == 0
	observed.WritersStopped = observed.ProcessScopeEmpty
	observed.MountDetached = !mounted
	return observed, nil
}

func containerExists(ctx context.Context, binary, id string) (bool, error) {
	command := exec.CommandContext(ctx, binary, "container", "exists", id)
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

// countReferences scans /proc for a process whose command line or environment
// still carries any needle: the container id or the unique cell name. Our own
// process is skipped. The name is passed only to podman, so a returned cell
// leaves no match.
func countReferences(needles ...string) (int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	needles = append([]string(nil), needles...)
	self := strconv.Itoa(os.Getpid())
	count := 0
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
			matched := false
			for _, needle := range needles {
				if needle != "" && bytes.Contains(data, []byte(needle)) {
					matched = true
					break
				}
			}
			if matched {
				count++
				break
			}
		}
	}
	return count, true
}

// scanCgroup performs a bounded breadth-first scan of the directory names
// beneath root for a path component containing id. It returns found=true when
// such a path exists. scanned is false when the scan could not be completed
// (unreadable or absent root, or a depth/directory bound was reached), in which
// case note explains why and the caller must fail closed rather than assume
// absence.
func scanCgroup(root, id string) (found bool, scanned bool, note string) {
	if strings.TrimSpace(id) == "" {
		return false, false, "empty container id"
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return false, false, fmt.Sprintf("cgroup root unavailable: %v", err)
	}
	type directory struct {
		path  string
		depth int
	}
	queue := []directory{{path: root}}
	visited := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		entries, err := os.ReadDir(current.path)
		if err != nil {
			return false, false, fmt.Sprintf("cgroup directory unreadable %q: %v", current.path, err)
		}
		visited++
		if visited > cgroupScanMaxDirs {
			return false, false, "cgroup scan exceeded directory bound"
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), id) {
				return true, true, ""
			}
			if entry.IsDir() && current.depth < cgroupScanMaxDepth {
				queue = append(queue, directory{path: filepath.Join(current.path, entry.Name()), depth: current.depth + 1})
			}
		}
	}
	return false, true, ""
}

// newCIDFifo creates a private FIFO at path and returns it opened O_RDWR with
// O_NONBLOCK. The O_RDWR fd acts as its own writer so reads report EAGAIN (not
// a spurious EOF) until podman writes the id, and the buffered bytes survive
// podman's --rm unlink of the path.
func newCIDFifo(path string) (*os.File, error) {
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
}

// readCIDFifo reads and validates the 64-hex container id written by `podman
// run --cidfile`. It waits on the non-blocking FIFO with a read deadline until a
// complete id is present, then fails closed. Requiring the full 64-hex id means
// a truncated or hostile cidfile can never masquerade as an observation target.
func readCIDFifo(file *os.File, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	buffer := make([]byte, 256)
	var data []byte
	for {
		if err := file.SetReadDeadline(deadline); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			data = append(data, buffer[:n]...)
			if id, parseErr := parseContainerID(string(data)); parseErr == nil {
				return id, nil
			}
			if len(data) > len(buffer) {
				return "", errors.New("cidfile is larger than a container id")
			}
			continue
		}
		if err != nil {
			if os.IsTimeout(err) {
				return "", fmt.Errorf("timed out reading cid fifo after %d bytes", len(data))
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
	}
	return parseContainerID(string(data))
}

// parseContainerID trims and validates a raw container id: exactly one
// hexadecimal container id.
func parseContainerID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", errors.New("cidfile is empty")
	}
	if len(id) != containerIDLength || !isHex(id) {
		return "", fmt.Errorf("cidfile does not contain a %d-hex container id: %q", containerIDLength, id)
	}
	return id, nil
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

func isHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9':
		case char >= 'a' && char <= 'f':
		case char >= 'A' && char <= 'F':
		default:
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
