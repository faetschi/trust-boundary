//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/wsoracle"
)

// `tbound evidence <kind> [--flag value]...` is the guest half of the runner's evidence
// collection (internal/runner/evidence_ssh.go, internal/procoracle/ssh_collector.go). It
// is READ-ONLY with three small exceptions: net-canary (one request to the guest NET sink, below),
// the monitor locks (proc-monitor and transient-watch each flock one empty root-owned file in /run) and the
// liveness canary of transient-watch (one uniquely named file written and removed in the live root at the start and
// the end of the watch: the instrument's own contract, effectoracle doc.go). It writes one
// JSON document to stdout (proc-monitor: a readiness line first; usage goes to stderr), writes
// nothing else to disk, mutates no other state and opens every other file read-only. It
// refuses any unknown kind, flag or malformed value, and it refuses to run as any uid
// other than evidenceRequiredUID, which is root. That uid is the one the evidence SSH key
// logs in as, with a forced command restricted to this subcommand (sshd Match User +
// ForceCommand, or authorized_keys command="..." with restrict), see
// infra/guest/PROVISIONING-SIGNING.md, Evidence collection. The root requirement is
// deliberate: PROC reads /proc/<pid>/fd of the cell uid, and the service's catalog and
// audit files are private to the service uid.
//
// Kinds (each flag exactly once, in any order):
//
//	e04-snapshot                 --root R --point P
//	    wsoracle.Snapshot of R at E04 point P. R must be one of evidenceLiveRoots (the
//	    managed live root, piruntime.D09ManagedPiLiveRootPath); P one of the seven E04 points.
//	proc-cell
//	    the registered cell (cgroup2 path, directory inode, uid, gid), resolved from the signed
//	    managed-root registration and the cell template; the host builds the registration
//	    digest from it.
//	proc-capture                 --registration D --point post_teardown|final_cleanup|final_cleanup
//	                             --interval-start NS --deadline NS
//	    procoracle.Capture of the registered cell. The cell cgroup, uid and gid are
//	    resolved from the signed managed-root registration and the cell template, never
//	    from a flag; the registration they and the two instants make must digest to D.
//	proc-monitor                 --registration D --interval-start NS --deadline NS
//	                             --duration SECONDS --interval MILLISECONDS
//	    procoracle.StartMonitor over the same cell for SECONDS (1..1500) at MILLISECONDS (200..1000),
//	    then the poll records, once. TWO lines: the readiness line (procoracle.MonitorReadyDoc),
//	    written right after the first poll and before polling on, then the final document. At
//	    most one monitor runs at a time (flock on a root-owned lock file in /run: the one
//	    file this subcommand creates, a lock with no content); a second is refused.
//	publication-interval-journal --interval-ref REF
//	    the interval journal of the corpus admission REF names (marker record, then the
//	    workflow's publication records), base64, with the head and count the receipt
//	    anchor carries; derived by the service's own anchor functions.
//	e04-points                   --interval-ref REF
//	    the service's own protected-tree manifests at the seven E04 points of the admission REF
//	    names, bound by the receipt's head and count (evidence_e04_points_linux.go). Read-only.
//	transient-watch              --interval-ref REF --duration SECONDS
//	    effectoracle (inotify) over the one registered live root: EFF-TRANSIENT. SECONDS (1..1500) is
//	    only the cap on a watch nobody ended. TWO lines: the readiness line (runner.TransientReadyDoc),
//	    written once the observer runs and before the request is sent, then the final document
//	    (runner.TransientDoc: the effectoracle report, the window actually observed and why it ended).
//	    The host talks to the watch over the session's stdin. The first line is "start": nothing is written
//	    into the live root, not even the start canaries, before it is read, so an input that ended first
//	    writes nothing, and a session that ends while the observer starts stops the start before its
//	    next canary. Then the line "stop" stops the observer (its stop
//	    canary and the full-tree snapshot are written then, with the session open) and reports. A
//	    stdin that ends without that line, a signal, or a failed readiness line is an ABORT: the
//	    observer is closed without writing anything into the live root and the command fails, so no
//	    canary is written after the session is gone. Only a watch nobody ended (SECONDS up) stops on
//	    its own timer, and says so. At most one watch runs at a time (flock on a root-owned lock file
//	    in /run); a second is refused. The watch cannot be bound to the admission (it starts before
//	    the request); the host binds the report to the publication ledger of the interval.
//	net-canary                   --phase start|end --interval TAG
//	    an effectful kind (the other is transient-watch, whose effect is its liveness canary files in
//	    the live root): it sends a single GET canary request to the guest NET sink
//	    (runner.NetsinkAddr, 127.0.33.1:8080) with the sink-private secret and prints the journal
//	    position the sink acknowledged. The effect is a journal record in the sink itself and
//	    nothing else: the request goes to the fixed loopback address, never to an address the
//	    caller names. It is its own kind (not folded into net-journal) so that net-journal stays
//	    read-only and the forced command's effectful verbs are explicit and auditable.
//	net-journal                  --interval TAG
//	    the sink journal (netoracle.ExtractInterval) from its first record through the end canary
//	    of TAG, base64, with head, count and the canaries' sequence numbers. Read-only.
//	ssh-original-command
//	    the forced-command entry: reads $SSH_ORIGINAL_COMMAND, which must be
//	    "/usr/local/bin/tbound evidence <kind> ..." in plain or single-quoted words of the
//	    safe alphabet, and runs that. No shell ever sees it.
const (
	evidenceRequiredUID   = 0
	evidenceMaxValueBytes = 256
	evidenceMaxCommand    = 2048

	// proc-monitor bounds (the host side lives in procoracle: MaxGuestMonitorDuration,
	// MinGuestPollInterval): the monitor keeps every poll in memory and writes them once.
	evidenceMaxMonitorSeconds = 1500
	evidenceMinMonitorMillis  = 200
	evidenceMaxMonitorMillis  = 1000
	// evidenceMaxMonitorSamples is the most polls retained; at 1500 s and 200 ms that is 7500.
	evidenceMaxMonitorSamples = 8000
	// evidenceMonitorLockPath is the one root-owned lock file that admits a single monitor.
	evidenceMonitorLockPath = "/run/tbound-evidence-monitor.lock"
	// evidenceTransientLockPath is the one lock file that admits a single transient-watch.
	evidenceTransientLockPath = "/run/tbound-evidence-transient.lock"
)

// evidenceMaxMonitorBytes caps the size of the monitor's final document (a variable so a
// test can lower it).
var evidenceMaxMonitorBytes = 16 << 20

const evidenceUsage = `usage: tbound evidence <kind> [--flag value]...   (read-only; root only; one JSON document on stdout)
  e04-snapshot                 --root <protected root> --point <e04 point>
  proc-cell
  proc-capture                 --registration <digest> --point <post_teardown|final_cleanup> --interval-start <ns> --deadline <ns>
  proc-monitor                 --registration <digest> --interval-start <ns> --deadline <ns> --duration <s> --interval <ms>
  publication-interval-journal --interval-ref <ref>
  e04-points                   --interval-ref <ref>
  transient-watch              --interval-ref <ref> --duration <s>   (inotify over the live root; ends on a "stop" line on stdin; writes a liveness canary in the live root at start and at stop)
  net-canary                   --phase <start|end> --interval <tag>   (sends ONE canary to the guest NET sink; with transient-watch, the only effects)
  net-journal                  --interval <tag>
  ssh-original-command         (forced-command entry; reads $SSH_ORIGINAL_COMMAND)
`

// evidenceLiveRoots is the whole allowlist of protected roots: the managed live root of
// the signed registration. No other path is ever walked.
var evidenceLiveRoots = []string{piruntime.D09ManagedPiLiveRootPath}

var evidencePoints = []string{
	wsoracle.PointBaseline, wsoracle.PointPostMaterialization, wsoracle.PointPostSettlement,
	wsoracle.PointPostTeardown, wsoracle.PointPrePublication, wsoracle.PointPostPublication, wsoracle.PointFinalCleanup,
}

var evidenceFlags = map[string][]string{
	runner.EvidenceE04Snapshot:        {"--root", "--point"},
	runner.EvidenceProcCell:           {},
	runner.EvidenceProcCapture:        {"--registration", "--point", "--interval-start", "--deadline"},
	runner.EvidenceProcMonitor:        {"--registration", "--interval-start", "--deadline", "--duration", "--interval"},
	runner.EvidencePublicationJournal: {"--interval-ref"},
	runner.EvidenceTransientWatch:     {"--interval-ref", "--duration"},
	runner.EvidenceNetCanary:          {"--phase", "--interval"},
	runner.EvidenceNetJournal:         {"--interval"},
	runner.EvidenceSSHOriginalCommand: {},
}

var (
	evidenceWordRE   = regexp.MustCompile(`^[A-Za-z0-9._:/@#|+=,-]+$`)
	evidenceDigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// evidenceCell is the registered cell the PROC kinds observe.
type evidenceCell struct {
	ID         string // the cell id of the registration: the cell cgroup path
	CgroupPath string // the directory on the cgroup2 mount
	Hierarchy  string // the cgroup2 hierarchy path Capture reports for it
	Inode      uint64 // the inode of that directory, read by the loader
	UID, GID   uint32 // the identity inside the cell (the template's CellUID and CellGID)
	// HostUID and HostGID are the host identity the signed mapping puts UID and GID on. Only
	// loadTrialCell sets them (cellHostIdentity).
	HostUID, HostGID uint32
}

func (c evidenceCell) doc() procoracle.CellDoc {
	return procoracle.CellDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcCell, CellID: c.ID,
		CellCgroupPath: c.Hierarchy, CellCgroupInode: c.Inode, CellUID: c.UID, CellGID: c.GID}
}

// evidenceEnv is everything the subcommand touches outside its arguments; tests replace it.
type evidenceEnv struct {
	geteuid func() int
	getenv  func(string) string
	// snapshot is wsoracle.Snapshot.
	snapshot func(root, label string) wsoracle.Manifest
	// cell resolves the registered cell from the service's own registration.
	cell func(ctx context.Context) (evidenceCell, error)
	// capture and monitor observe it (procoracle.Capture and StartMonitor). monitor calls ready
	// with its first poll before it polls on.
	capture func(c evidenceCell, reg procoracle.Registration, p procoracle.Point, seq int) (procoracle.Snapshot, error)
	monitor func(ctx context.Context, c evidenceCell, reg procoracle.Registration, d, interval time.Duration, ready func(procoracle.Snapshot)) ([]procoracle.Snapshot, error)
	// lockMonitor admits one monitor at a time; the release function frees it.
	lockMonitor func() (release func(), err error)
	// lockTransient admits one transient-watch at a time; watch is the effectoracle observer over root (it calls ready
	// once the observer runs, before it waits). stdin is the session's standard input, the channel the host ends a
	// watch on: stop receives nil for the stop line and an error for any other end of the input, and the watch then
	// aborts without writing anything. d is the cap on a watch nobody ended.
	lockTransient func() (release func(), err error)
	watch         func(ctx context.Context, root string, d time.Duration, ready func() error, stop <-chan error) (transientWatch, error)
	stdin         io.Reader
	// netCanary sends one canary to the guest NET sink (the one kind with an effect, on the sink
	// only); netJournal reads the sink journal file, read-only.
	netCanary  func(ctx context.Context, phase, tag string) (netoracle.CanaryAck, error)
	netJournal func() ([]byte, error)
	// readTrace reads and verifies an audit journal without opening it for writing. A
	// missing file is an empty trace unless mustExist.
	readTrace func(path string, mustExist bool) (audit.Trace, error)
}

func productionEvidenceEnv() evidenceEnv {
	return evidenceEnv{
		geteuid: os.Geteuid, getenv: os.Getenv, snapshot: wsoracle.Snapshot,
		cell: loadEvidenceCell, capture: captureEvidenceCell, monitor: monitorEvidenceCell, readTrace: readEvidenceTrace,
		lockMonitor: func() (func(), error) { return lockEvidenceMonitorFile(evidenceMonitorLockPath, evidenceRequiredUID) },
		netCanary:   sendNetsinkCanary, netJournal: readNetsinkJournal,
		lockTransient: func() (func(), error) { return lockEvidenceMonitorFile(evidenceTransientLockPath, evidenceRequiredUID) },
		watch:         watchEvidenceRoot, stdin: os.Stdin,
	}
}

// init dispatches `tbound evidence ...` before main runs. The command table lives in
// main.go (run), which this file does not touch; a dispatch line there would replace this.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "evidence" {
		os.Exit(evidenceMain(os.Args[2:], os.Stdout, os.Stderr, productionEvidenceEnv()))
	}
}

func evidenceMain(args []string, stdout, stderr io.Writer, env evidenceEnv) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		_, _ = io.WriteString(stderr, evidenceUsage)
		return 0
	}
	if err := runEvidence(ctx, args, stdout, env); err != nil {
		fmt.Fprintf(stderr, "tbound evidence: %v\n%s", err, evidenceUsage)
		return 2
	}
	return 0
}

// parseEvidenceFlags accepts exactly the kind's flags, each once, as "--name value" pairs.
func parseEvidenceFlags(kind string, args []string) (map[string]string, error) {
	want := evidenceFlags[kind]
	if len(args) != 2*len(want) {
		return nil, fmt.Errorf("%s takes exactly %d flags and no other argument", kind, len(want))
	}
	got := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		name, value := args[i], args[i+1]
		if !slices.Contains(want, name) {
			return nil, fmt.Errorf("%s: unknown argument %q", kind, name)
		}
		if _, dup := got[name]; dup {
			return nil, fmt.Errorf("%s: %s given twice", kind, name)
		}
		if len(value) == 0 || len(value) > evidenceMaxValueBytes || strings.HasPrefix(value, "-") || !evidenceWordRE.MatchString(value) {
			return nil, fmt.Errorf("%s: the value of %s is not in the safe alphabet", kind, name)
		}
		got[name] = value
	}
	return got, nil
}

func runEvidence(ctx context.Context, args []string, stdout io.Writer, env evidenceEnv) error {
	if env.geteuid == nil || env.geteuid() != evidenceRequiredUID {
		return fmt.Errorf("evidence may run only as uid %d", evidenceRequiredUID)
	}
	if len(args) == 0 {
		return errors.New("no evidence kind")
	}
	kind, rest := args[0], args[1:]
	if h, ok := evidenceExtraKinds[kind]; ok {
		return runEvidenceExtra(kind, h, rest, stdout, env)
	}
	if _, ok := evidenceFlags[kind]; !ok {
		return fmt.Errorf("unknown evidence kind %q", kind)
	}
	if kind == runner.EvidenceSSHOriginalCommand {
		if len(rest) != 0 {
			return fmt.Errorf("%s takes no argument", kind)
		}
		words, err := parseOriginalCommand(env.getenv("SSH_ORIGINAL_COMMAND"), "evidence")
		if err != nil {
			return err
		}
		if words[0] == runner.EvidenceSSHOriginalCommand {
			return errors.New("the forced command may not nest itself")
		}
		return runEvidence(ctx, words, stdout, env)
	}
	flags, err := parseEvidenceFlags(kind, rest)
	if err != nil {
		return err
	}
	var doc any
	switch kind {
	case runner.EvidenceE04Snapshot:
		doc, err = evidenceE04Snapshot(env, flags)
	case runner.EvidenceProcCell:
		var cell evidenceCell
		if cell, err = env.cell(ctx); err == nil {
			doc = cell.doc()
		}
	case runner.EvidenceProcCapture:
		doc, err = evidenceProcCapture(ctx, env, flags)
	case runner.EvidenceProcMonitor:
		doc, err = evidenceProcMonitor(ctx, env, flags, stdout)
	case runner.EvidencePublicationJournal:
		doc, err = evidencePublicationJournal(env, flags["--interval-ref"])
	case runner.EvidenceTransientWatch:
		doc, err = evidenceTransientWatch(ctx, env, flags, stdout)
	case runner.EvidenceNetCanary:
		doc, err = evidenceNetCanary(ctx, env, flags)
	case runner.EvidenceNetJournal:
		doc, err = evidenceNetJournal(env, flags["--interval"])
	}
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(encoded, '\n'))
	return err
}

// splitOriginalCommand splits $SSH_ORIGINAL_COMMAND into its words: separated by single
// spaces, each plain or wrapped in one pair of single quotes, all of the safe alphabet.
func splitOriginalCommand(s string) ([]string, error) {
	if s == "" || len(s) > evidenceMaxCommand {
		return nil, errors.New("no usable SSH_ORIGINAL_COMMAND")
	}
	var words []string
	for _, raw := range strings.Split(s, " ") {
		w := raw
		if len(w) >= 2 && w[0] == '\'' && w[len(w)-1] == '\'' {
			w = w[1 : len(w)-1]
		}
		if w == "" || len(w) > evidenceMaxValueBytes || !evidenceWordRE.MatchString(w) {
			return nil, errors.New("SSH_ORIGINAL_COMMAND has a word outside the safe alphabet")
		}
		words = append(words, w)
	}
	return words, nil
}

// parseOriginalCommand reads a forced command's $SSH_ORIGINAL_COMMAND: words separated by
// single spaces, each plain or wrapped in one pair of single quotes, all of the safe
// alphabet, the first two being the guest binary and verb ("evidence" or "trial"). It
// returns the words after those two.
func parseOriginalCommand(s, verb string) ([]string, error) {
	words, err := splitOriginalCommand(s)
	if err != nil {
		return nil, err
	}
	if len(words) < 3 || words[0] != runner.EvidenceBinaryPath || words[1] != verb {
		return nil, fmt.Errorf("SSH_ORIGINAL_COMMAND is not a %s command", verb)
	}
	return words[2:], nil
}

func evidenceE04Snapshot(env evidenceEnv, f map[string]string) (runner.E04SnapshotDoc, error) {
	root, point := f["--root"], f["--point"]
	if !slices.Contains(evidenceLiveRoots, root) {
		return runner.E04SnapshotDoc{}, fmt.Errorf("root %q is not a registered protected root", root)
	}
	if !slices.Contains(evidencePoints, point) {
		return runner.E04SnapshotDoc{}, fmt.Errorf("%q is not an E04 observation point", point)
	}
	return runner.E04SnapshotDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceE04Snapshot, Point: point, Root: root,
		Manifest: env.snapshot(root, point)}, nil
}

// evidenceRegistration rebuilds the registration from the service's own cell and the two
// instants, and requires its digest to be the one the caller passed.
func evidenceRegistration(ctx context.Context, env evidenceEnv, f map[string]string) (evidenceCell, procoracle.Registration, error) {
	if !evidenceDigestRE.MatchString(f["--registration"]) {
		return evidenceCell{}, procoracle.Registration{}, errors.New("the registration digest is not 64 lowercase hex digits")
	}
	start, err := procoracle.ParseInstantArg(f["--interval-start"])
	if err != nil {
		return evidenceCell{}, procoracle.Registration{}, err
	}
	deadline, err := procoracle.ParseInstantArg(f["--deadline"])
	if err != nil {
		return evidenceCell{}, procoracle.Registration{}, err
	}
	if deadline.Before(start) {
		return evidenceCell{}, procoracle.Registration{}, errors.New("the deadline is before the interval start")
	}
	cell, err := env.cell(ctx)
	if err != nil {
		return evidenceCell{}, procoracle.Registration{}, fmt.Errorf("the registered cell cannot be resolved: %w", err)
	}
	reg := procoracle.GuestRegistration(cell.doc(), start, deadline)
	if reg.Digest() != f["--registration"] {
		return evidenceCell{}, procoracle.Registration{}, errors.New("registration digest mismatch: the passed digest is not the service's registered cell")
	}
	return cell, reg, nil
}

func evidenceProcCapture(ctx context.Context, env evidenceEnv, f map[string]string) (procoracle.CaptureDoc, error) {
	point := procoracle.Point(f["--point"])
	seq, ok := procoracle.GuestPointSeq(point)
	if !ok {
		return procoracle.CaptureDoc{}, fmt.Errorf("%q is not a point proc-capture serves", point)
	}
	cell, reg, err := evidenceRegistration(ctx, env, f)
	if err != nil {
		return procoracle.CaptureDoc{}, err
	}
	snap, err := env.capture(cell, reg, point, seq)
	if err != nil {
		return procoracle.CaptureDoc{}, err
	}
	return procoracle.CaptureDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcCapture, Point: point,
		RegistrationDigest: reg.Digest(), Snapshot: snap}, nil
}

func evidenceProcMonitor(ctx context.Context, env evidenceEnv, f map[string]string, stdout io.Writer) (procoracle.MonitorDoc, error) {
	seconds, err := strconv.Atoi(f["--duration"])
	if err != nil || seconds < 1 || seconds > evidenceMaxMonitorSeconds || strconv.Itoa(seconds) != f["--duration"] {
		return procoracle.MonitorDoc{}, fmt.Errorf("--duration must be 1..%d seconds", evidenceMaxMonitorSeconds)
	}
	millis, err := strconv.Atoi(f["--interval"])
	if err != nil || millis < evidenceMinMonitorMillis || millis > evidenceMaxMonitorMillis || strconv.Itoa(millis) != f["--interval"] {
		return procoracle.MonitorDoc{}, fmt.Errorf("--interval must be %d..%d milliseconds", evidenceMinMonitorMillis, evidenceMaxMonitorMillis)
	}
	cell, reg, err := evidenceRegistration(ctx, env, f)
	if err != nil {
		return procoracle.MonitorDoc{}, err
	}
	// One monitor at a time, taken before anything is polled.
	if env.lockMonitor == nil {
		return procoracle.MonitorDoc{}, errors.New("no monitor lock")
	}
	release, err := env.lockMonitor()
	if err != nil {
		return procoracle.MonitorDoc{}, fmt.Errorf("proc-monitor refused: %w", err)
	}
	defer release()
	var readyErr error
	polls, err := env.monitor(ctx, cell, reg, time.Duration(seconds)*time.Second, time.Duration(millis)*time.Millisecond, func(first procoracle.Snapshot) {
		line, err := json.Marshal(procoracle.MonitorReadyDoc{Schema: runner.EvidenceSchema, Kind: procoracle.MonitorReadyKind,
			RegistrationDigest: reg.Digest(), FirstPollSeq: first.Seq, FirstPollAt: first.ObservedAt})
		if err == nil {
			_, err = stdout.Write(append(line, '\n'))
		}
		readyErr = err
	})
	switch {
	case err != nil:
		return procoracle.MonitorDoc{}, err
	case readyErr != nil:
		return procoracle.MonitorDoc{}, fmt.Errorf("the readiness line could not be written: %w", readyErr)
	case len(polls) == 0 || len(polls) > evidenceMaxMonitorSamples:
		return procoracle.MonitorDoc{}, fmt.Errorf("the monitor kept %d polls, want 1..%d", len(polls), evidenceMaxMonitorSamples)
	}
	doc := procoracle.MonitorDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcMonitor,
		RegistrationDigest: reg.Digest(), Polls: polls}
	if encoded, err := json.Marshal(doc); err != nil || len(encoded) > evidenceMaxMonitorBytes {
		return procoracle.MonitorDoc{}, fmt.Errorf("the monitor record is %d bytes (cap %d)", len(encoded), evidenceMaxMonitorBytes)
	}
	return doc, nil
}

// evidenceCellFrom derives the cell from the verified managed-root registration and the
// cell template: the cells cgroup of the registered service, and the template's uid and
// gid. Both are root-owned, signed or digest-bound artifacts the service itself loads.
func evidenceCellFrom(reg piruntime.D09ManagedLiveRootRegistration, tmpl piruntime.D09CellTemplate) (evidenceCell, error) {
	want := "/sys/fs/cgroup" + reg.ServiceCgroupPath + "/" + piruntime.D09ManagedPiCellCgroupLeaf
	switch {
	case reg.ServiceCgroupPath != piruntime.D09ManagedPiServiceCgroupPath:
		return evidenceCell{}, errors.New("the registration names another service cgroup")
	case tmpl.CgroupsPath != want:
		return evidenceCell{}, fmt.Errorf("the cell template cgroup %q is not the registered cells cgroup", tmpl.CgroupsPath)
	case tmpl.CellUID <= 0 || tmpl.CellGID <= 0 || tmpl.ManagedLiveRootRegistrationDigest != reg.Digest:
		return evidenceCell{}, errors.New("the cell template identity is not bound to the registration")
	}
	return evidenceCell{ID: want, CgroupPath: want, Hierarchy: reg.ServiceCgroupPath + "/" + piruntime.D09ManagedPiCellCgroupLeaf,
		UID: uint32(tmpl.CellUID), GID: uint32(tmpl.CellGID)}, nil
}

func loadEvidenceCell(ctx context.Context) (evidenceCell, error) {
	cell, _, _, err := loadRegisteredCell(ctx)
	return cell, err
}

// loadTrialCell is loadEvidenceCell with the host identity of the signed mapping.
func loadTrialCell(ctx context.Context) (evidenceCell, error) {
	cell, reg, tmpl, err := loadRegisteredCell(ctx)
	if err != nil {
		return evidenceCell{}, err
	}
	if cell.HostUID, cell.HostGID, err = cellHostIdentity(reg, tmpl); err != nil {
		return evidenceCell{}, err
	}
	return cell, nil
}

func loadRegisteredCell(ctx context.Context) (evidenceCell, piruntime.D09ManagedLiveRootRegistration, piruntime.D09CellTemplate, error) {
	reg, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return evidenceCell{}, reg, piruntime.D09CellTemplate{}, err
	}
	tmpl, _, err := piruntime.LoadD09CellTemplate(ctx)
	if err != nil {
		return evidenceCell{}, reg, tmpl, err
	}
	cell, err := evidenceCellFrom(reg, tmpl)
	if err != nil {
		return evidenceCell{}, reg, tmpl, err
	}
	// The identity Capture will compare against: the hierarchy path and the inode of the
	// cell cgroup directory, read from the directory itself.
	dir, err := openEvidenceCellDir(cell.CgroupPath)
	if err != nil {
		return evidenceCell{}, reg, tmpl, err
	}
	defer dir.Close()
	facts, err := procoracle.HeldCell(dir).Observe()
	if err != nil || facts.Removed || facts.Inode == 0 || facts.HierarchyPath != cell.Hierarchy {
		return evidenceCell{}, reg, tmpl, fmt.Errorf("the registered cell cgroup is not observable as %q: %v", cell.Hierarchy, err)
	}
	cell.Inode = facts.Inode
	return cell, reg, tmpl, nil
}

func openEvidenceCellDir(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// The guest has no launcher registry: it is recorded empty and the identity scan of all
// of /proc is the authority (procoracle doc.go).
func evidenceRegistry() procoracle.Registry { return procoracle.Registry{Recorded: true} }

func captureEvidenceCell(c evidenceCell, reg procoracle.Registration, p procoracle.Point, seq int) (procoracle.Snapshot, error) {
	dir, err := openEvidenceCellDir(c.CgroupPath)
	if err != nil {
		return procoracle.Snapshot{}, err
	}
	defer dir.Close()
	return procoracle.Capture(procoracle.HeldCell(dir), procoracle.HostProc(), procoracle.CaptureParams{
		Point: p, Seq: seq, Registration: reg, Registry: evidenceRegistry()}), nil
}

func monitorEvidenceCell(ctx context.Context, c evidenceCell, reg procoracle.Registration, d, interval time.Duration, ready func(procoracle.Snapshot)) ([]procoracle.Snapshot, error) {
	dir, err := openEvidenceCellDir(c.CgroupPath)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	m, err := procoracle.StartMonitor(procoracle.HeldCell(dir), procoracle.HostProc(), procoracle.MonitorConfig{
		Registration: reg, Interval: interval, Registry: evidenceRegistry, MaxRecords: evidenceMaxMonitorSamples})
	if err != nil {
		return nil, err
	}
	// StartMonitor took the first poll synchronously: say so before polling on.
	ready(m.Records()[0])
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	return m.Stop(), nil
}

// readEvidenceTrace verifies an audit journal read-only: no lock, no create, no follow.
func readEvidenceTrace(path string, mustExist bool) (audit.Trace, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) && !mustExist {
		return audit.Trace{Records: []audit.Record{}, Effects: []audit.EffectTrace{}}, nil
	}
	if err != nil {
		return audit.Trace{}, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return audit.Trace{}, errors.New("the journal is not a regular file")
	}
	return audit.Verify(f)
}

// intervalJournalBytes serializes events as an audit journal from sequence 1, with the
// record hash rule of the audit package (the one intervalChainHead applies).
func intervalJournalBytes(events []audit.Event) ([]byte, error) {
	var out []byte
	previous := ""
	for i, event := range events {
		type unsigned struct {
			Version      int         `json:"version"`
			Sequence     uint64      `json:"sequence"`
			PreviousHash string      `json:"previous_hash"`
			Event        audit.Event `json:"event"`
		}
		u := unsigned{Version: 1, Sequence: uint64(i + 1), PreviousHash: previous, Event: event}
		encoded, err := json.Marshal(u)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(encoded)
		hash := hex.EncodeToString(sum[:])
		line, err := json.Marshal(audit.Record{Version: 1, Sequence: u.Sequence, PreviousHash: previous, Event: event, Hash: hash})
		if err != nil {
			return nil, err
		}
		out = append(append(out, line...), '\n')
		previous = hash
	}
	return out, nil
}

// evidencePublicationJournal finds the one catalog admission whose runner interval is ref,
// then derives the interval journal, its head and its count with the functions the
// service applies to a receipt (publicationAnchorFor, workflowPublicationSlice). The
// derived journal must verify and end on the anchor's head, or nothing is returned.
func evidencePublicationJournal(env evidenceEnv, ref string) (runner.PublicationJournalDoc, error) {
	catalog, err := env.readTrace(piruntime.D09ManagedPiWorkflowCatalogPath, true)
	if err != nil {
		return runner.PublicationJournalDoc{}, fmt.Errorf("the workflow catalog cannot be read: %w", err)
	}
	var workflowID string
	matches := 0
	for _, record := range catalog.Records {
		if record.Event.Kind != "pi_workflow_admission_reserved" {
			continue
		}
		var event piServiceWorkflowEvent
		if err := json.Unmarshal(record.Event.Data, &event); err != nil || event.SchemaVersion != workflowCatalogSchema {
			return runner.PublicationJournalDoc{}, fmt.Errorf("catalog record %d is unreadable", record.Sequence)
		}
		if got, ok := admissionIntervalRef(event.Entry); ok && got == ref {
			workflowID = event.Entry.WorkflowID
			matches++
		}
	}
	if matches != 1 {
		return runner.PublicationJournalDoc{}, fmt.Errorf("the catalog has %d admissions for interval %q, want exactly one", matches, ref)
	}
	publications, err := env.readTrace(filepath.Join(productionAuditRootDefault, "publication-audit.jsonl"), false)
	if err != nil {
		return runner.PublicationJournalDoc{}, fmt.Errorf("the publication journal cannot be read: %w", err)
	}
	anchor, err := publicationAnchorFor(publications, workflowID, ref)
	if err != nil {
		return runner.PublicationJournalDoc{}, err
	}
	slice, _, err := workflowPublicationSlice(publications, workflowID)
	if err != nil {
		return runner.PublicationJournalDoc{}, err
	}
	journal, err := intervalJournalBytes(append([]audit.Event{{Kind: publicationIntervalMarkerKind, ID: ref}}, slice...))
	if err != nil {
		return runner.PublicationJournalDoc{}, err
	}
	trace, err := audit.Verify(bytes.NewReader(journal))
	if err != nil || len(trace.Records) == 0 || trace.Records[len(trace.Records)-1].Hash != anchor.Head {
		return runner.PublicationJournalDoc{}, errors.New("the derived interval journal does not end on the service's anchor head")
	}
	return runner.PublicationJournalDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidencePublicationJournal,
		IntervalRef: ref, JournalB64: base64.StdEncoding.EncodeToString(journal), Head: anchor.Head, Count: anchor.Count}, nil
}

// lockEvidenceMonitorFile takes the exclusive, non-blocking flock that admits one
// proc-monitor at a time. The lock file must be a regular file of mode 0600 owned by uid
// (root in production), is never followed through a symlink, and holds no data. The
// returned function frees the lock; the kernel frees it as well when the process exits.
func lockEvidenceMonitorFile(path string, uid uint32) (func(), error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("monitor lock %s: %w", path, err)
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("monitor lock %s: %w", path, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != uid || st.Mode&0o7777 != 0o600 {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("monitor lock %s is not a regular 0600 file of uid %d", path, uid)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another proc-monitor is running")
		}
		return nil, fmt.Errorf("monitor lock %s: %w", path, err)
	}
	return func() { _ = syscall.Close(fd) }, nil
}

// transientWatch is what the guest observer returns: the report, the window it observed (from its start barrier to its
// stop barrier, on its own monotonic clock) and why the watch ended.
type transientWatch struct {
	Report  *effectoracle.Report
	Elapsed time.Duration
	Stop    string
}

func evidenceTransientWatch(ctx context.Context, env evidenceEnv, f map[string]string, stdout io.Writer) (runner.TransientDoc, error) {
	seconds, err := strconv.Atoi(f["--duration"])
	if err != nil || seconds < 1 || seconds > runner.MaxTransientWatchSeconds || strconv.Itoa(seconds) != f["--duration"] {
		return runner.TransientDoc{}, fmt.Errorf("--duration must be 1..%d seconds", runner.MaxTransientWatchSeconds)
	}
	if env.lockTransient == nil || env.watch == nil {
		return runner.TransientDoc{}, errors.New("no transient observer")
	}
	root := evidenceLiveRoots[0]
	release, err := env.lockTransient()
	if err != nil {
		return runner.TransientDoc{}, fmt.Errorf("transient-watch refused: %w", err)
	}
	// The lock is held until the observer has stopped or aborted: watch returns only then.
	defer release()
	ref := f["--interval-ref"]
	// The host talks to the watch over stdin: the start line first, the stop line to end it. Nothing is written into
	// the live root (not even the observer's start canaries) before the start line has been read, so an input that
	// ended before the command read it (the session is already gone) writes nothing, whichever side was quicker.
	if env.stdin == nil {
		return runner.TransientDoc{}, errors.New("transient-watch: no stdin")
	}
	input, startRead := bufio.NewReader(io.LimitReader(env.stdin, 128)), make(chan error, 1)
	go func() { startRead <- readTransientLine(input, runner.TransientStartLine) }()
	startTimer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer startTimer.Stop()
	select {
	case err := <-startRead:
		if err != nil {
			return runner.TransientDoc{}, fmt.Errorf("transient-watch was not started: %w", err)
		}
	case <-ctx.Done():
		return runner.TransientDoc{}, fmt.Errorf("transient-watch was not started: %w", ctx.Err())
	case <-startTimer.C:
		return runner.TransientDoc{}, errors.New("transient-watch was not started within its cap")
	}
	// From here the host ends the watch over stdin.
	stop := make(chan error, 1)
	go func() { stop <- readTransientLine(input, runner.TransientStopLine) }()
	result, err := env.watch(ctx, root, time.Duration(seconds)*time.Second, func() error {
		line, err := json.Marshal(runner.TransientReadyDoc{Schema: runner.EvidenceSchema, Kind: runner.TransientReadyKind, IntervalRef: ref, Root: root})
		if err == nil {
			_, err = stdout.Write(append(line, '\n'))
		}
		return err
	}, stop)
	if err != nil {
		return runner.TransientDoc{}, err
	}
	doc := runner.TransientDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceTransientWatch, IntervalRef: ref, Root: root,
		DurationSeconds: seconds, ElapsedMicros: result.Elapsed.Microseconds(), Stop: result.Stop, Report: *result.Report}
	if encoded, err := json.Marshal(doc); err != nil || len(encoded) > evidenceMaxMonitorBytes {
		return runner.TransientDoc{}, fmt.Errorf("the transient report is %d bytes (cap %d)", len(encoded), evidenceMaxMonitorBytes)
	}
	return doc, nil
}

// readTransientLine reads the next line of the session's stdin: nil for want, an error for anything else (the end of
// the input before a line, another line).
func readTransientLine(input *bufio.Reader, want string) error {
	line, err := input.ReadString('\n')
	switch {
	case line == want:
		return nil
	case err != nil:
		return fmt.Errorf("stdin ended without %q: %w", strings.TrimSpace(want), err)
	}
	return fmt.Errorf("stdin held something other than %q", strings.TrimSpace(want))
}

// watchEvidenceRoot is the effectoracle observer over root. ready is called once the observer runs (its start
// barrier, liveness canary included, has passed): the host sends the request only after that. The watch ends when the
// host asks (stop receives nil), and then the observer is stopped (its stop canary and snapshot are written, while the
// host's session is open) and the report returned with the window actually observed. It ends on its own after d only
// if nobody ended it, and says so. When the host's session ends without a stop request (stop receives an error), on a
// signal, or when the readiness line cannot be written, the session is gone or going: the observer is closed WITHOUT a
// stop canary or any other write into the root, and the watch fails. That holds while the observer starts too: a
// session that is gone before the start writes nothing, and one that goes during it stops the start before its next
// canary (effectoracle.StartContext).
func watchEvidenceRoot(ctx context.Context, root string, d time.Duration, ready func() error, stop <-chan error) (transientWatch, error) {
	// A session that is already gone is seen before anything is written.
	var early *error
	select {
	case err := <-stop:
		if err != nil {
			return transientWatch{}, fmt.Errorf("the transient watch was aborted before it started and wrote nothing: %w", err)
		}
		early = &err
	default:
	}
	// The stop channel is forwarded so that the session ending cancels the start too.
	startCtx, cancelStart := context.WithCancel(ctx)
	defer cancelStart()
	session := make(chan error, 1)
	if early != nil {
		session <- nil
	} else {
		go func() {
			select {
			case err := <-stop:
				session <- err
				if err != nil {
					cancelStart()
				}
			case <-startCtx.Done():
			}
		}()
	}
	observer, err := effectoracle.StartContext(startCtx, root)
	if err != nil {
		return transientWatch{}, fmt.Errorf("the transient watch did not start (nothing more was written): %w", err)
	}
	started := time.Now()
	if err := ready(); err != nil {
		observer.Abort()
		return transientWatch{}, fmt.Errorf("the readiness line could not be written: %w", err)
	}
	aborted := func(cause error) (transientWatch, error) {
		observer.Abort()
		return transientWatch{}, fmt.Errorf("the transient watch was aborted and wrote nothing more: %w", cause)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	reason := runner.TransientStopRequested
	select {
	case err := <-session:
		if err != nil {
			return aborted(err)
		}
	case <-ctx.Done():
		return aborted(ctx.Err())
	case <-timer.C:
		// Nobody ended the watch. Take one last look at the session first: a host that is gone must not get a stop
		// canary.
		select {
		case err := <-session:
			if err != nil {
				return aborted(err)
			}
		default:
			reason = runner.TransientStopTimer
		}
	}
	report, err := observer.Stop()
	if err != nil {
		return transientWatch{}, err
	}
	return transientWatch{Report: report, Elapsed: time.Since(started), Stop: reason}, nil
}
