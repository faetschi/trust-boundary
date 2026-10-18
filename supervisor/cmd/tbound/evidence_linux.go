//go:build linux

package main

import (
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
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/wsoracle"
)

// `tbound evidence <kind> [--flag value]...` is the guest half of the runner's evidence
// collection (internal/runner/evidence_ssh.go, internal/procoracle/ssh_collector.go). It
// is READ-ONLY: it writes exactly one JSON document to stdout (usage goes to stderr), writes
// nothing to disk, mutates no state, takes no lock and opens every file read-only. It
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
//	    procoracle.StartMonitor over the same cell for SECONDS, then the poll records, once.
//	publication-interval-journal --interval-ref REF
//	    the interval journal of the corpus admission REF names (marker record, then the
//	    workflow's publication records), base64, with the head and count the receipt
//	    anchor carries; derived by the service's own anchor functions.
//	ssh-original-command
//	    the forced-command entry: reads $SSH_ORIGINAL_COMMAND, which must be
//	    "/usr/local/bin/tbound evidence <kind> ..." in plain or single-quoted words of the
//	    safe alphabet, and runs that. No shell ever sees it.
const (
	evidenceRequiredUID   = 0
	evidenceMaxValueBytes = 256
	evidenceMaxCommand    = 2048
)

const evidenceUsage = `usage: tbound evidence <kind> [--flag value]...   (read-only; root only; one JSON document on stdout)
  e04-snapshot                 --root <protected root> --point <e04 point>
  proc-cell
  proc-capture                 --registration <digest> --point <post_teardown|final_cleanup> --interval-start <ns> --deadline <ns>
  proc-monitor                 --registration <digest> --interval-start <ns> --deadline <ns> --duration <s> --interval <ms>
  publication-interval-journal --interval-ref <ref>
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
	UID, GID   uint32
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
	// capture and monitor observe it (procoracle.Capture and StartMonitor).
	capture func(c evidenceCell, reg procoracle.Registration, p procoracle.Point, seq int) (procoracle.Snapshot, error)
	monitor func(ctx context.Context, c evidenceCell, reg procoracle.Registration, d, interval time.Duration) ([]procoracle.Snapshot, error)
	// readTrace reads and verifies an audit journal without opening it for writing. A
	// missing file is an empty trace unless mustExist.
	readTrace func(path string, mustExist bool) (audit.Trace, error)
}

func productionEvidenceEnv() evidenceEnv {
	return evidenceEnv{
		geteuid: os.Geteuid, getenv: os.Getenv, snapshot: wsoracle.Snapshot,
		cell: loadEvidenceCell, capture: captureEvidenceCell, monitor: monitorEvidenceCell, readTrace: readEvidenceTrace,
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
	if _, ok := evidenceFlags[kind]; !ok {
		return fmt.Errorf("unknown evidence kind %q", kind)
	}
	if kind == runner.EvidenceSSHOriginalCommand {
		if len(rest) != 0 {
			return fmt.Errorf("%s takes no argument", kind)
		}
		words, err := parseEvidenceOriginalCommand(env.getenv("SSH_ORIGINAL_COMMAND"))
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
		doc, err = evidenceProcMonitor(ctx, env, flags)
	case runner.EvidencePublicationJournal:
		doc, err = evidencePublicationJournal(env, flags["--interval-ref"])
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

// parseEvidenceOriginalCommand reads the forced command's $SSH_ORIGINAL_COMMAND: words
// separated by single spaces, each plain or wrapped in one pair of single quotes, all of the
// safe alphabet, the first two being the guest binary and "evidence". It returns the
// words after those two.
func parseEvidenceOriginalCommand(s string) ([]string, error) {
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
	if len(words) < 3 || words[0] != runner.EvidenceBinaryPath || words[1] != "evidence" {
		return nil, errors.New("SSH_ORIGINAL_COMMAND is not an evidence command")
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

func evidenceProcMonitor(ctx context.Context, env evidenceEnv, f map[string]string) (procoracle.MonitorDoc, error) {
	seconds, err := strconv.Atoi(f["--duration"])
	if err != nil || seconds < 1 || seconds > 3600 || strconv.Itoa(seconds) != f["--duration"] {
		return procoracle.MonitorDoc{}, errors.New("--duration must be 1..3600 seconds")
	}
	millis, err := strconv.Atoi(f["--interval"])
	if err != nil || millis < 50 || millis > 1000 || strconv.Itoa(millis) != f["--interval"] {
		return procoracle.MonitorDoc{}, errors.New("--interval must be 50..1000 milliseconds")
	}
	cell, reg, err := evidenceRegistration(ctx, env, f)
	if err != nil {
		return procoracle.MonitorDoc{}, err
	}
	polls, err := env.monitor(ctx, cell, reg, time.Duration(seconds)*time.Second, time.Duration(millis)*time.Millisecond)
	if err != nil {
		return procoracle.MonitorDoc{}, err
	}
	return procoracle.MonitorDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcMonitor,
		RegistrationDigest: reg.Digest(), Polls: polls}, nil
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
	reg, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return evidenceCell{}, err
	}
	tmpl, _, err := piruntime.LoadD09CellTemplate(ctx)
	if err != nil {
		return evidenceCell{}, err
	}
	cell, err := evidenceCellFrom(reg, tmpl)
	if err != nil {
		return evidenceCell{}, err
	}
	// The identity Capture will compare against: the hierarchy path and the inode of the
	// cell cgroup directory, read from the directory itself.
	dir, err := openEvidenceCellDir(cell.CgroupPath)
	if err != nil {
		return evidenceCell{}, err
	}
	defer dir.Close()
	facts, err := procoracle.HeldCell(dir).Observe()
	if err != nil || facts.Removed || facts.Inode == 0 || facts.HierarchyPath != cell.Hierarchy {
		return evidenceCell{}, fmt.Errorf("the registered cell cgroup is not observable as %q: %v", cell.Hierarchy, err)
	}
	cell.Inode = facts.Inode
	return cell, nil
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

func monitorEvidenceCell(ctx context.Context, c evidenceCell, reg procoracle.Registration, d, interval time.Duration) ([]procoracle.Snapshot, error) {
	dir, err := openEvidenceCellDir(c.CgroupPath)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	m, err := procoracle.StartMonitor(procoracle.HeldCell(dir), procoracle.HostProc(), procoracle.MonitorConfig{
		Registration: reg, Interval: interval, Registry: evidenceRegistry})
	if err != nil {
		return nil, err
	}
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
