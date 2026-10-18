//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/wsoracle"
)

// `tbound trial reset <case_id> [--check]` is the guest half of the runner's fixture reset:
// it puts the live root back to the workspace fixture of one corpus case before a trial. It
// writes exactly one JSON document (trialfix.ResetDoc) to stdout and refuses to run as any
// uid other than trialRequiredUID, which is root. That is the uid the trial SSH key logs in
// as, with a forced command restricted to this subcommand (authorized_keys
// restrict,command="/usr/local/bin/tbound trial ssh-original-command", the same shape as the
// evidence key, see infra/guest/PROVISIONING-SIGNING.md). The case and its fixture come from
// the pinned corpus manifest the signed service config references, never from the caller;
// the fixture spec is read from the root-owned artifact directory and must recompute to the
// digest the manifest pins.
//
//	reset <case_id>
//	    refuses when a workflow admission has no terminal, a managed-root run still holds the
//	    live root, the cells cgroup is populated, or the fixture artifact does not verify.
//	    Otherwise it kills the processes of the cell uid that are left outside the cells
//	    cgroup, wipes the live root in place (the root inode, its mode and owner and a
//	    top-level lost+found are kept), populates it from the fixture, and prints the digest
//	    of the fixture next to the digest of the resulting tree, which must be equal.
//	reset <case_id> --check
//	    the refusals only. It changes nothing.
//	ssh-original-command
//	    the forced-command entry: reads $SSH_ORIGINAL_COMMAND, which must be
//	    "/usr/local/bin/tbound trial <args>" in the word grammar of the evidence entry.
const (
	trialRequiredUID = 0
	// trialArtifactDir is where the signing session installs the fixture artifacts
	// (fixture-<hex>.json, root:root 0644). Only a test replaces it, through trialEnv.
	trialArtifactDir = "/opt/tbound/corpus/artifacts"
	trialResetLabel  = "reset"
	trialKillRounds  = 20
)

// trialKillWait is how long, in total, the reset waits for the kernel to confirm that the
// groups it sent SIGKILL have exited (a variable so a test can shorten it).
var trialKillWait = 5 * time.Second

const trialUsage = `usage: tbound trial reset <case_id> [--check]   (root only; one JSON document on stdout)
  reset <case_id>           reset the live root to the fixture of the case
  reset <case_id> --check   verify the preconditions and the fixture artifact only
  ssh-original-command      (forced-command entry; reads $SSH_ORIGINAL_COMMAND)
`

// trialEnv is everything the subcommand touches outside its arguments; tests replace it.
type trialEnv struct {
	geteuid func() int
	getenv  func(string) string
	// fixtureDigest is the workspace_fixture_digest the pinned corpus manifest gives a case.
	fixtureDigest func(ctx context.Context, caseID string) (string, error)
	// artifacts holds the fixture artifacts.
	artifacts fs.FS
	// liveRoot is the root that is reset, and owner the owner of what is put in it.
	liveRoot string
	owner    trialfix.Owner
	// cell resolves the registered cell, as the evidence kinds do.
	cell func(ctx context.Context) (evidenceCell, error)
	// pending returns an error naming what is still pending: a workflow admission without a
	// terminal, or a managed-root run that holds the live root.
	pending func(ctx context.Context) error
	// cellPopulated reports whether the cgroup directory holds any process.
	cellPopulated func(cgroupDir string) (bool, error)
	// killLeftovers kills every process of the uid and fails if one survives.
	killLeftovers func(uid uint32) error
	// snapshot is wsoracle.Snapshot.
	snapshot func(root, label string) wsoracle.Manifest
}

func productionTrialEnv() trialEnv {
	return trialEnv{
		geteuid: os.Geteuid, getenv: os.Getenv,
		fixtureDigest: trialPinnedFixtureDigest, artifacts: os.DirFS(trialArtifactDir),
		liveRoot: piruntime.D09ManagedPiLiveRootPath, owner: trialfix.Canonical,
		cell: loadEvidenceCell, pending: trialPending, cellPopulated: cgroupPopulated,
		killLeftovers: func(uid uint32) error {
			return killUIDProcesses("/proc", uid, pinProcessPidfd)
		},
		snapshot: wsoracle.Snapshot,
	}
}

// init dispatches `tbound trial ...` before main runs, as evidence does.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "trial" {
		os.Exit(trialMain(os.Args[2:], os.Stdout, os.Stderr, productionTrialEnv()))
	}
}

func trialMain(args []string, stdout, stderr io.Writer, env trialEnv) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		_, _ = io.WriteString(stderr, trialUsage)
		return 0
	}
	if err := runTrial(context.Background(), args, stdout, env); err != nil {
		fmt.Fprintf(stderr, "tbound trial: %v\n%s", err, trialUsage)
		return 2
	}
	return 0
}

func runTrial(ctx context.Context, args []string, stdout io.Writer, env trialEnv) error {
	if env.geteuid == nil || env.geteuid() != trialRequiredUID {
		return fmt.Errorf("trial may run only as uid %d", trialRequiredUID)
	}
	if len(args) == 0 {
		return errors.New("no trial verb")
	}
	switch verb, rest := args[0], args[1:]; verb {
	case "ssh-original-command":
		if len(rest) != 0 {
			return fmt.Errorf("%s takes no argument", verb)
		}
		words, err := parseOriginalCommand(env.getenv("SSH_ORIGINAL_COMMAND"), "trial")
		if err != nil {
			return err
		}
		if words[0] == "ssh-original-command" {
			return errors.New("the forced command may not nest itself")
		}
		return runTrial(ctx, words, stdout, env)
	case "reset":
		doc, err := trialReset(ctx, rest, env)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(encoded, '\n'))
		return err
	default:
		return fmt.Errorf("unknown trial verb %q", verb)
	}
}

// parseTrialReset accepts exactly "<case_id>" or "<case_id> --check".
func parseTrialReset(args []string) (caseID string, check bool, err error) {
	if len(args) < 1 || len(args) > 2 {
		return "", false, errors.New("reset takes a case id and optionally --check")
	}
	caseID = args[0]
	if len(caseID) == 0 || len(caseID) > evidenceMaxValueBytes || strings.HasPrefix(caseID, "-") || !evidenceWordRE.MatchString(caseID) {
		return "", false, errors.New("the case id is not in the safe alphabet")
	}
	if len(args) == 2 {
		if args[1] != "--check" {
			return "", false, fmt.Errorf("reset: unknown argument %q", args[1])
		}
		check = true
	}
	return caseID, check, nil
}

func trialReset(ctx context.Context, args []string, env trialEnv) (trialfix.ResetDoc, error) {
	caseID, check, err := parseTrialReset(args)
	if err != nil {
		return trialfix.ResetDoc{}, err
	}
	digest, err := env.fixtureDigest(ctx, caseID)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	spec, err := corpusadmit.LoadFixture(env.artifacts, digest)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	cell, err := env.cell(ctx)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("the registered cell cannot be resolved: %w", err)
	}
	if cell.UID != env.owner.UID {
		return trialfix.ResetDoc{}, fmt.Errorf("the cell uid %d is not the fixture owner %d", cell.UID, env.owner.UID)
	}
	if err := env.pending(ctx); err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("a workflow, admission or publication is pending: %w", err)
	}
	populated, err := env.cellPopulated(cell.CgroupPath)
	if err != nil {
		return trialfix.ResetDoc{}, fmt.Errorf("the cells cgroup cannot be read: %w", err)
	}
	if populated {
		return trialfix.ResetDoc{}, errors.New("the cells cgroup holds processes")
	}
	doc := trialfix.ResetDoc{Schema: trialfix.ResetSchema, CaseID: caseID, FixtureDigest: digest, Check: check}
	if check {
		return doc, nil
	}
	// The stray processes go first: one that outlived its cell could write into the tree
	// that is about to be built. The verification below sees anything that still did.
	if err := env.killLeftovers(cell.UID); err != nil {
		return trialfix.ResetDoc{}, err
	}
	if err := trialfix.Reset(env.liveRoot, spec, env.owner); err != nil {
		return trialfix.ResetDoc{}, err
	}
	tree, err := trialfix.TreeDigest(env.snapshot(env.liveRoot, trialResetLabel))
	if err != nil {
		return trialfix.ResetDoc{}, err
	}
	if want, err := trialfix.DigestOwned(spec, env.owner); err != nil || tree != want {
		return trialfix.ResetDoc{}, fmt.Errorf("the reset tree does not digest to the fixture: %s, want %s", tree, want)
	}
	doc.TreeDigest = tree
	return doc, nil
}

// trialPinnedFixtureDigest is the fixture digest of caseID in the pinned corpus manifest: the
// manifest the signed service config references, admitted with the service's own loader.
func trialPinnedFixtureDigest(ctx context.Context, caseID string) (string, error) {
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return "", err
	}
	_, contract, err := serviceSocketRegistrationFromSignedRegistration(registration)
	if err != nil {
		return "", err
	}
	config, err := readProductionPiServiceConfig(contract.ServiceConfigPath, contract.ServiceConfigDigest)
	if err != nil {
		return "", err
	}
	corpus, err := loadProductionCorpus(config)
	if err != nil {
		return "", err
	}
	digest, ok := corpus.fixtures[caseID]
	if !ok {
		return "", errors.New("not a case of the pinned corpus manifest")
	}
	return digest, nil
}

// trialPending reads the workflow catalog, the publication lifecycle journal and the live root
// identity without writing, and reports what is still pending.
func trialPending(ctx context.Context) error {
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return err
	}
	catalog, err := readEvidenceTrace(piruntime.D09ManagedPiWorkflowCatalogPath, false)
	if err != nil {
		return fmt.Errorf("the workflow catalog cannot be read: %w", err)
	}
	lifecycle, err := readEvidenceTrace(filepath.Join(productionAuditRootDefault, "publication-audit.jsonl"), false)
	if err != nil {
		return fmt.Errorf("the publication lifecycle journal cannot be read: %w", err)
	}
	info, err := os.Lstat(piruntime.D09ManagedPiLiveRootPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("the live root cannot be identified: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("the live root cannot be identified")
	}
	return trialPendingFrom(catalog, lifecycle, workflow.RootIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, registration.Digest)
}

// trialPendingFrom is trialPending over the two traces: a catalog admission without a terminal
// record is a pending workflow, and a managed-root run on the root that has no resolution
// record (managedLiveRootPriorRunsFromTrace, the rule the service applies to its prior runs)
// is a pending run or publication.
func trialPendingFrom(catalog, lifecycle audit.Trace, root workflow.RootIdentity, registrationDigest string) error {
	var open []string
	terminal := map[string]bool{}
	for _, record := range catalog.Records {
		if record.Event.Kind != "pi_workflow_admission_reserved" && record.Event.Kind != "pi_workflow_terminal" {
			continue
		}
		var event struct {
			SchemaVersion string `json:"schema_version"`
			AdmissionHash string `json:"admission_hash"`
		}
		if err := json.Unmarshal(record.Event.Data, &event); err != nil || event.SchemaVersion != workflowCatalogSchema || event.AdmissionHash == "" {
			return fmt.Errorf("catalog record %d is unreadable", record.Sequence)
		}
		if record.Event.Kind == "pi_workflow_terminal" {
			terminal[event.AdmissionHash] = true
		} else {
			open = append(open, event.AdmissionHash)
		}
	}
	for _, hash := range open {
		if !terminal[hash] {
			return fmt.Errorf("workflow admission %s is not terminal", hash)
		}
	}
	runs, err := managedLiveRootPriorRunsFromTrace(lifecycle, root, registrationDigest)
	if err != nil {
		return fmt.Errorf("the publication lifecycle journal is not readable as settled: %w", err)
	}
	if len(runs) != 0 {
		return fmt.Errorf("managed-root run %s (workflow %s) still holds the live root", runs[0].Event.RunID, runs[0].Event.WorkflowID)
	}
	return nil
}

// cgroupPopulated reports whether the cgroup2 directory, or one below it, holds a process,
// from the populated key of its cgroup.events.
func cgroupPopulated(dir string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.events"))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "populated "); ok {
			switch value {
			case "0":
				return false, nil
			case "1":
				return true, nil
			}
			break
		}
	}
	return false, errors.New("cgroup.events has no valid populated key")
}

// errProcGone means the pinned process is gone: it was reaped, or never existed.
var errProcGone = errors.New("process is gone")

// procPin is a process (thread group) held by a pidfd. It names the process itself and not a
// number that the kernel may hand to another process after the first one is reaped.
type procPin interface {
	// Alive returns errProcGone once the process was reaped. A zombie that is not reaped
	// yet still holds its pid, so a successful Alive after reading /proc/<pid> shows that
	// the pid was not reused in between.
	Alive() error
	// Signal sends SIGKILL to the pinned process (pidfd_send_signal) and returns
	// errProcGone if it was reaped.
	Signal() error
	// Exited reports whether the whole thread group has exited, waiting up to wait for it.
	// The kernel makes the pidfd of a thread-group leader readable only when the last
	// thread is gone, so a zombie leader with a running worker is not exited.
	Exited(wait time.Duration) (bool, error)
	Close() error
}

// pinProcess opens a pin for a pid. It reports errProcGone for a pid that is not there.
// Any other error, including a kernel without pidfd support, refuses the kill.
type pinProcess func(pid int) (procPin, error)

type pidfdPin struct{ fd int }

// pinProcessPidfd is the production pinProcess: pidfd_open. It never falls back to
// signaling by number.
func pinProcessPidfd(pid int) (procPin, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, errProcGone
	}
	if err != nil {
		return nil, fmt.Errorf("pidfd_open(%d): %w", pid, err)
	}
	return &pidfdPin{fd: fd}, nil
}

func (p *pidfdPin) send(sig unix.Signal) error {
	err := unix.PidfdSendSignal(p.fd, sig, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return errProcGone
	}
	return err
}

func (p *pidfdPin) Alive() error  { return p.send(0) }
func (p *pidfdPin) Signal() error { return p.send(unix.SIGKILL) }
func (p *pidfdPin) Close() error  { return unix.Close(p.fd) }

// pollPidfd is poll(2); a test replaces it.
var pollPidfd = unix.Poll

func (p *pidfdPin) Exited(wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait)
	for {
		fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
		ms := int((time.Until(deadline) + time.Millisecond - 1) / time.Millisecond)
		n, err := pollPidfd(fds, max(ms, 0))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("poll pidfd: %w", err)
		}
		switch {
		case n == 0:
			return false, nil
		case fds[0].Revents&unix.POLLIN != 0:
			return true, nil
		}
		return false, fmt.Errorf("poll pidfd: revents %#x", fds[0].Revents)
	}
}

// killUIDProcesses sends SIGKILL to every process (thread group) whose real uid is uid and
// whose group has not exited, waits until the kernel confirms that every such group exited,
// and repeats until a fresh scan finds no group left to kill. It refuses (an error) if a
// group is still running trialKillWait after its SIGKILL, or if trialKillRounds scans each
// found one. It never acts for uid 0.
//
// Liveness is never read from /proc: the pidfd of a thread-group leader is readable only when
// the whole group has exited, and a zombie leader with running worker threads is not readable.
// Ownership is the real uid in the leader's /proc/<pid>/status, which a zombie leader keeps.
// That uid is read after the pidfd is opened and the pidfd must still be alive after the
// read, so the status belonged to the pinned process and not to a reused pid. A pidfd_open
// error other than a vanished process (no pidfd support, a denied open) refuses the reset:
// there is no fallback to signaling by number.
func killUIDProcesses(procRoot string, uid uint32, pin pinProcess) error {
	if uid == 0 {
		return errors.New("refusing to kill the processes of uid 0")
	}
	for round := 0; round < trialKillRounds; round++ {
		pids, err := uidProcesses(procRoot, uid)
		if err != nil {
			return err
		}
		killed, err := killGroups(procRoot, uid, pids, pin)
		if err != nil {
			return err
		}
		if killed == 0 {
			return nil
		}
	}
	return fmt.Errorf("processes of uid %d survive %d rounds of kills", uid, trialKillRounds)
}

// pinnedGroup is a signaled thread group that has to be confirmed exited.
type pinnedGroup struct {
	pid int
	pin procPin
}

// killGroups signals the candidate groups that are of uid and not exited, and returns how many
// it signaled once the kernel confirms all of them exited.
func killGroups(procRoot string, uid uint32, pids []int, pin pinProcess) (int, error) {
	var signaled []pinnedGroup
	defer func() {
		for _, g := range signaled {
			g.pin.Close()
		}
	}()
	for _, pid := range pids {
		pinned, err := pin(pid)
		if errors.Is(err, errProcGone) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("cannot pin pid %d, refusing the reset: %w", pid, err)
		}
		sent, err := signalIfLive(procRoot, uid, pid, pinned)
		if sent {
			signaled = append(signaled, pinnedGroup{pid, pinned})
		} else {
			pinned.Close()
		}
		if err != nil {
			return 0, err
		}
	}
	deadline := time.Now().Add(trialKillWait)
	for _, g := range signaled {
		exited, err := g.pin.Exited(max(time.Until(deadline), 0))
		if err != nil {
			return 0, fmt.Errorf("wait for pid %d: %w", g.pid, err)
		}
		if !exited {
			return 0, fmt.Errorf("pid %d (uid %d) is still running %s after SIGKILL, refusing the reset", g.pid, uid, trialKillWait)
		}
	}
	return len(signaled), nil
}

// signalIfLive sends SIGKILL through the pidfd if the pinned process is of uid and its group
// has not exited. The uid is read after the pin, and the pin must be alive after the read.
func signalIfLive(procRoot string, uid uint32, pid int, pinned procPin) (sent bool, err error) {
	real, found, err := readProcRealUID(procRoot, pid)
	if err != nil {
		return false, err
	}
	if err := pinned.Alive(); err != nil {
		if errors.Is(err, errProcGone) {
			return false, nil // reaped: the status may belong to a reused pid
		}
		return false, fmt.Errorf("check pinned pid %d: %w", pid, err)
	}
	if !found || real != uid {
		return false, nil // not a cell process (any more): never signal it
	}
	exited, err := pinned.Exited(0)
	if err != nil || exited {
		return false, err
	}
	if err := pinned.Signal(); err != nil {
		if errors.Is(err, errProcGone) {
			return false, nil
		}
		return false, fmt.Errorf("signal pinned pid %d: %w", pid, err)
	}
	return true, nil
}

// uidProcesses lists the thread groups of procRoot whose leader has real uid uid, zombie
// leaders included. It is a candidate list only: killGroups decides what is still running.
func uidProcesses(procRoot string, uid uint32) ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		real, found, err := readProcRealUID(procRoot, pid)
		if err != nil {
			return nil, err
		}
		if found && real == uid {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// readProcRealUID reads the real uid of the thread-group leader pid from procRoot/<pid>/status;
// found is false when the process is gone. A status without a parsable real uid is an error:
// what cannot be attributed refuses the reset.
func readProcRealUID(procRoot string, pid int) (real uint32, found bool, err error) {
	status, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read the status of pid %d: %w", pid, err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if ids, ok := strings.CutPrefix(line, "Uid:"); ok {
			if fields := strings.Fields(ids); len(fields) > 0 {
				if id, err := strconv.ParseUint(fields[0], 10, 32); err == nil {
					return uint32(id), true, nil
				}
			}
			break
		}
	}
	return 0, true, fmt.Errorf("pid %d has no readable real uid, refusing the reset", pid)
}
