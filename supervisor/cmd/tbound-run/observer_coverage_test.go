package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/wsoracle"
)

// The observer-coverage test runs GovernedC.Execute over the production collector wiring (plan.collectorsOver)
// against a fake guest that answers every evidence kind with a genuine document: the E04 point store and the
// publication journal as audit chains over snapshots of a real directory tree, the NET documents from a real
// guest-mode sink, the PROC documents as the guest monitor writes them, and the transient-watch report from a real
// effectoracle (inotify) observer that watches the same tree the service "publishes" into. Only the client
// transport (the SSH hop to the governed service) is faked, and the host attestation (route, channel, E01) is set
// by hand: it belongs to runner.Attest, not to the collectors.

const (
	obsSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	obsRunID  = "production-run-obs"
	obsWF     = "wf-obs"
)

var obsT0 = time.Unix(1_800_000_000, 0).UTC()

var obsCell = procoracle.CellDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcCell, CellID: "/sys/fs/cgroup/system.slice/tbound-pi.service/cells",
	CellCgroupPath: "/system.slice/tbound-pi.service/cells", CellCgroupInode: 4242, CellUID: 1000, CellGID: 1000}

// obsPublication mirrors runner.E04PublicationEvidence on the wire (the contract is tested here as JSON).
type obsPublication struct {
	TransactionID        string            `json:"transaction_id"`
	PublicationID        string            `json:"publication_id"`
	Status               string            `json:"status"`
	LockIdentity         string            `json:"lock_identity"`
	AffectedPaths        []string          `json:"affected_paths"`
	Sealed               wsoracle.Manifest `json:"sealed"`
	SealedIdentityDigest string            `json:"sealed_identity_digest"`
	FenceDir             string            `json:"fence_dir"`
	FenceBursts          int               `json:"fence_bursts"`
}

type obsFixture struct {
	t         *testing.T
	step      runner.Step
	published bool
	// rogue writes a file (rogueName, default rogue.bin) into the live root and removes it again between two E04 points
	// (an effect no snapshot sees). With a published interval rogueName may be a path the publication ledger names.
	rogue     bool
	rogueName string
	// hostileRenames, with a published interval, renames files onto the approved path and away again inside the
	// publication window.
	hostileRenames bool
	// renamesBeforeFences and renamesAfterFences, with a published interval, rename a file in and away again just
	// before publication's first fence and just after its second.
	renamesBeforeFences, renamesAfterFences bool
	// thirdFence, with a published interval, makes a fence of its own between publication's two.
	thirdFence bool
	// failRequest makes the request transport fail before the service does anything.
	failRequest bool
	// watchEnds receives how the guest transient-watch ended: "stop" or "abort".
	watchEnds chan string
	live      string
	sealed    string

	sink     *netoracle.Sink
	sinkPath string

	mu         sync.Mutex
	calls      [][]string
	e04        runner.E04PointsDoc
	pubJournal runner.PublicationJournalDoc
	receipt    map[string]any
	release    chan struct{}
}

func newObsFixture(t *testing.T, published bool) *obsFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the guest instruments (inotify, the sink) are Linux-only")
	}
	dir := t.TempDir()
	f := &obsFixture{t: t, published: published, live: filepath.Join(dir, "live"), sealed: filepath.Join(dir, "sealed"),
		release: make(chan struct{}), watchEnds: make(chan string, 4),
		step: runner.Step{Index: 1, CaseID: "obs-case", Kind: evalcorpus.KindAdversarial, ThreatClass: evalcorpus.ClassTMA,
			Configuration: runner.CfgC, Role: runner.RoleTrajectory, Attempt: 1, Predicate: "EFF-WS-01", WitnessID: "w-obs"}}
	for _, root := range []string{f.live, f.sealed} {
		if err := os.MkdirAll(filepath.Join(root, "protected"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, effectoracle.FenceDir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, effectoracle.FenceDir, "found.0"), []byte("fsck\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"a.txt": "alpha\n", "protected/p.cfg": "key=1\n"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if published {
		if err := os.WriteFile(filepath.Join(f.sealed, "new.txt"), []byte("published\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.sinkPath = filepath.Join(dir, "sink.jsonl")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if f.sink, err = netoracle.NewSink(netoracle.SinkConfig{Listener: ln, JournalPath: f.sinkPath, Secret: obsSecret}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.sink.Close() })
	return f
}

func (f *obsFixture) record(argv []string) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), argv...))
	f.mu.Unlock()
}

func (f *obsFixture) called(kind string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == kind {
			return true
		}
	}
	return false
}

// journalFile appends events to a fresh audit journal and returns its bytes, head and record count.
func (f *obsFixture) journalFile(name string, events []audit.Event) ([]byte, string, int) {
	f.t.Helper()
	path := filepath.Join(filepath.Dir(f.live), name)
	j, err := audit.Open(path)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, e := range events {
		if _, err := j.Append(e); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		f.t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	trace, err := audit.Verify(bytes.NewReader(raw))
	if err != nil || len(trace.Records) != len(events) {
		f.t.Fatalf("journal %s: %v (%d records)", name, err, len(trace.Records))
	}
	return raw, trace.Records[len(trace.Records)-1].Hash, len(trace.Records)
}

// serve is what the governed service does in the interval: it takes the E04 manifests of the live root at its
// seven points, publishes (for a published interval) and journals everything it observed.
func (f *obsFixture) serve() {
	t := f.t
	interval := runner.IntervalRef(f.step)
	snap := func(point string) wsoracle.Manifest { return wsoracle.Snapshot(f.live, point) }
	type pt struct {
		name string
		m    wsoracle.Manifest
	}
	var points []pt
	for _, name := range runner.E04PointOrder[:5] { // baseline .. pre-publication
		points = append(points, pt{name, snap(name)})
	}
	var ledger []wsoracle.LedgerEntry
	fenceBursts := 0
	if f.rogue {
		name := f.rogueName
		if name == "" {
			name = "rogue.bin"
		}
		rogue := filepath.Join(f.live, name)
		if err := os.WriteFile(rogue, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(rogue); err != nil {
			t.Fatal(err)
		}
	}
	if f.published {
		// Publication applies an object by renaming a staged file into the live root, as the real one does (the
		// staged file is outside the watched tree, so the observer sees one moved_to), never by writing it in place.
		staged := filepath.Join(filepath.Dir(f.live), "staged-new.txt")
		if f.renamesBeforeFences {
			f.renamePair("early", "extra.txt")
		}
		f.openFence(&fenceBursts)
		if f.thirdFence {
			// Another process: it reads a file inside the directory (a record that is not an open or a read of the directory
			// itself, which ends the first fence) and makes a fence of its own, before the rename.
			if _, err := os.ReadFile(filepath.Join(f.live, effectoracle.FenceDir, "found.0")); err != nil {
				t.Fatal(err)
			}
			var third int
			f.openFence(&third)
		}
		if f.hostileRenames {
			// A writer that renames files onto the approved path and away again inside the publication window, twice:
			// every rename has an unwatched other side, like publication's own. One ledger change explains one rename.
			for i := 0; i < 2; i++ {
				evil := filepath.Join(filepath.Dir(f.live), fmt.Sprintf("evil-%d.txt", i))
				if err := os.WriteFile(evil, []byte("evil\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(evil, filepath.Join(f.live, "new.txt")); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(f.live, "new.txt"), evil); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := os.WriteFile(staged, []byte("published\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(staged, filepath.Join(f.live, "new.txt")); err != nil {
			t.Fatal(err)
		}
		// Publication does not wait for the observer: the second fence is the next thing it does.
		f.openFence(&fenceBursts)
		if f.renamesAfterFences {
			f.renamePair("late", "extra.txt")
		}
	}
	for _, name := range runner.E04PointOrder[5:] {
		points = append(points, pt{name, snap(name)})
	}
	if f.published {
		for _, e := range points[5].m.Entries {
			if e.Path == "new.txt" {
				entry := e
				ledger = []wsoracle.LedgerEntry{{ID: "pub-obs/0000", Sequence: 1, Changes: []wsoracle.LedgerChange{{Path: "new.txt", After: &entry}}}}
			}
		}
		if len(ledger) != 1 {
			t.Fatal("the published file is not in the post-publication manifest")
		}
	}
	var events []audit.Event
	for i, p := range points {
		m := p.m
		rec := runner.E04PointRecord{Schema: runner.E04PointSchema, Seq: uint64(i + 1), WorkflowID: obsWF, RunID: obsRunID, Point: p.name,
			Status: runner.E04StatusSnapshot, Manifest: &m, ManifestIdentityDigest: m.IdentityDigest(),
			LedgerApplied: 0, LedgerPrefixDigest: wsoracle.LedgerPrefixDigest(nil, 0), LedgerBasis: runner.E04LedgerNone}
		if f.published && i >= 5 {
			rec.LedgerApplied, rec.LedgerPrefixDigest, rec.LedgerBasis, rec.Ledger = 1, wsoracle.LedgerPrefixDigest(ledger, 1), runner.E04LedgerPublication, ledger
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if f.published && p.name == wsoracle.PointPostPublication {
			var generic map[string]any
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatal(err)
			}
			sealed := wsoracle.Snapshot(f.sealed, "sealed")
			generic["publication"] = obsPublication{TransactionID: "pub-obs", PublicationID: "pubid-obs", Status: "SUCCEEDED",
				LockIdentity: "0000000000000001:0000000000000002", AffectedPaths: []string{"new.txt"}, Sealed: sealed, SealedIdentityDigest: sealed.IdentityDigest(),
				FenceDir: effectoracle.FenceDir, FenceBursts: fenceBursts}
			if raw, err = json.Marshal(generic); err != nil {
				t.Fatal(err)
			}
		}
		events = append(events, audit.Event{Kind: runner.E04PointRecordKind, ID: fmt.Sprintf("%s/%d", obsWF, i+1), Data: raw})
	}
	store, head, count := f.journalFile("e04-store.jsonl", events)
	f.e04 = runner.E04PointsDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceE04Points, IntervalRef: interval, WorkflowID: obsWF, RunID: obsRunID,
		JournalB64: base64.StdEncoding.EncodeToString(store), Head: head, Count: count}

	journal := []audit.Event{{Kind: runner.IntervalMarkerKind, ID: interval}}
	if f.published {
		prepare, _ := json.Marshal(map[string]string{"transaction_id": "pub-obs", "publication_id": "pubid-obs"})
		journal = append(journal, audit.Event{Kind: "publication_prepare", ID: "pub-obs", Data: prepare}, audit.Event{Kind: "publication_terminal", ID: "pub-obs"})
	}
	// The anchor counts the publication records of the journal, not the interval marker before them.
	jbytes, jhead, jcount := f.journalFile("publication.jsonl", journal)
	f.pubJournal = runner.PublicationJournalDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidencePublicationJournal, IntervalRef: interval,
		JournalB64: base64.StdEncoding.EncodeToString(jbytes), Head: jhead, Count: jcount}
	f.receipt = map[string]any{
		"schema_version": "tbound-production-pi-receipt/v1", "run_id": obsRunID, "success": true, "disposition": "NO_DIFF",
		"tool_sequence": []string{"read"}, "publication_interval_ref": interval, "publication_journal_head": jhead,
		"publication_journal_record_count": jcount - 1, "e04_evidence_head": head, "e04_evidence_record_count": count, "e04_gap_count": 0}
}

// renamePair renames a file onto target in the live root and away again: two renames whose other side is
// outside the watched tree, like publication's own.
func (f *obsFixture) renamePair(name, target string) {
	t := f.t
	evil := filepath.Join(filepath.Dir(f.live), name+".txt")
	if err := os.WriteFile(evil, []byte("evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(evil, filepath.Join(f.live, target)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.live, target), evil); err != nil {
		t.Fatal(err)
	}
}

// openFence is what publication does before and after it applies: a fence on the live root (effectoracle.FenceRoot).
func (f *obsFixture) openFence(count *int) {
	root, err := os.Open(f.live)
	if err != nil {
		f.t.Fatal(err)
	}
	defer root.Close()
	if err := effectoracle.FenceRoot(root); err != nil {
		f.t.Fatal(err)
	}
	*count++
}

// obsClient is the governed client transport: the service runs inside Run.
type obsClient struct{ f *obsFixture }

func (c obsClient) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	if c.f.failRequest {
		return nil, nil, 0, errors.New("ssh: connection to the service lost")
	}
	c.f.serve()
	req := runner.GovernedRequestFor("plan-obs", c.f.step)
	line, err := json.Marshal(map[string]any{"schema_version": "tbound-pi-service-response/v1", "request_id": req.RequestID,
		"status": "completed", "receipt": c.f.receipt})
	if err != nil {
		return nil, nil, 0, err
	}
	close(c.f.release) // the request is over: the watcher's time is up
	return append(line, '\n'), nil, 0, nil
}

// obsEvidence is the evidence channel: the guest's answers to `tbound evidence <kind>`.
type obsEvidence struct{ f *obsFixture }

func (e obsEvidence) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return e.RunReadyStdin(ctx, argv, nil, nil)
}

func (e obsEvidence) RunReady(ctx context.Context, argv []string, ready func([]byte)) ([]byte, []byte, int, error) {
	return e.RunReadyStdin(ctx, argv, nil, ready)
}

func obsFlags(argv []string) map[string]string {
	m := map[string]string{}
	for i := 1; i+1 < len(argv); i += 2 {
		m[argv[i]] = argv[i+1]
	}
	return m
}

func obsReply(v any) ([]byte, []byte, int, error) {
	b, _ := json.Marshal(v)
	return append(b, '\n'), nil, 0, nil
}

func (e obsEvidence) RunReadyStdin(ctx context.Context, argv []string, stdin io.Reader, ready func([]byte)) ([]byte, []byte, int, error) {
	f := e.f
	f.record(argv)
	flags := obsFlags(argv)
	fail := func(msg string) ([]byte, []byte, int, error) { return nil, []byte(msg), 2, nil }
	switch argv[0] {
	case runner.EvidenceE04Points:
		if f.e04.IntervalRef != flags["--interval-ref"] {
			return fail("no such interval")
		}
		return obsReply(f.e04)
	case runner.EvidencePublicationJournal:
		if f.pubJournal.IntervalRef != flags["--interval-ref"] {
			return fail("no such interval")
		}
		return obsReply(f.pubJournal)
	case runner.EvidenceNetCanary:
		ack, err := netoracle.SendCanary(ctx, f.sink.Addr(), obsSecret, flags["--phase"], flags["--interval"])
		if err != nil {
			return fail(err.Error())
		}
		return obsReply(runner.NetCanaryDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetCanary, Phase: flags["--phase"],
			Interval: flags["--interval"], Seq: ack.Seq, Hash: ack.Hash})
	case runner.EvidenceNetJournal:
		raw, err := os.ReadFile(f.sinkPath)
		if err != nil {
			return fail(err.Error())
		}
		iv, err := netoracle.ExtractInterval(raw, flags["--interval"])
		if err != nil {
			return fail(err.Error())
		}
		return obsReply(runner.NetJournalDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetJournal, Interval: flags["--interval"],
			JournalB64: base64.StdEncoding.EncodeToString(iv.Journal), Head: iv.Head, Count: iv.Count, StartSeq: iv.StartSeq, EndSeq: iv.EndSeq})
	case runner.EvidenceProcCell:
		return obsReply(obsCell)
	case runner.EvidenceProcMonitor, runner.EvidenceProcCapture:
		start, err1 := procoracle.ParseInstantArg(flags["--interval-start"])
		deadline, err2 := procoracle.ParseInstantArg(flags["--deadline"])
		if err1 != nil || err2 != nil {
			return fail("bad instants")
		}
		reg := procoracle.GuestRegistration(obsCell, start, deadline)
		if reg.Digest() != flags["--registration"] {
			return fail("registration digest mismatch")
		}
		base := procoracle.Snapshot{RegistrationDigest: reg.Digest(), CellPath: obsCell.CellCgroupPath, CellInode: obsCell.CellCgroupInode,
			IdentityScanned: true, ScannedPIDs: 5, Registry: procoracle.Registry{Recorded: true}}
		if argv[0] == runner.EvidenceProcCapture {
			p := procoracle.Point(flags["--point"])
			seq, ok := procoracle.GuestPointSeq(p)
			if !ok {
				return fail("point")
			}
			s := base
			s.Seq, s.Point, s.ObservedAt = seq, p, obsT0.Add(time.Duration(2+seq)*time.Second)
			return obsReply(procoracle.CaptureDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcCapture, Point: p, RegistrationDigest: reg.Digest(), Snapshot: s})
		}
		line, _, _, _ := obsReply(procoracle.MonitorReadyDoc{Schema: runner.EvidenceSchema, Kind: procoracle.MonitorReadyKind,
			RegistrationDigest: reg.Digest(), FirstPollSeq: 1, FirstPollAt: start.Add(100 * time.Millisecond)})
		if ready != nil {
			ready(line)
		}
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, nil, 0, ctx.Err()
		}
		var polls []procoracle.Snapshot
		for i := 1; i <= 6; i++ {
			s := base
			s.Seq, s.Point, s.ObservedAt = i, procoracle.PointMonitor, obsT0.Add(time.Duration(i)*time.Second)
			polls = append(polls, s)
		}
		final, _, _, _ := obsReply(procoracle.MonitorDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcMonitor, RegistrationDigest: reg.Digest(), Polls: polls})
		return append(line, final...), nil, 0, nil
	case "transient-watch":
		return e.transientWatch(ctx, flags, stdin, ready)
	}
	return fail("unknown evidence kind")
}

// transientWatch is `tbound evidence transient-watch`: a real inotify observer over the live root, started before the
// request and ended by the host over stdin: the stop line stops it (stop canary and snapshot, then the report), an end
// of stdin without it aborts it and writes nothing.
func (e obsEvidence) transientWatch(ctx context.Context, flags map[string]string, stdin io.Reader, ready func([]byte)) ([]byte, []byte, int, error) {
	f := e.f
	seconds, err := strconv.Atoi(flags["--duration"])
	if err != nil {
		return nil, []byte("bad duration"), 2, nil
	}
	// The host's start line comes first: nothing is written into the live root before it.
	if stdin == nil {
		return nil, []byte("aborted: no stdin"), 2, nil
	}
	input := bufio.NewReader(stdin)
	if l, _ := input.ReadString('\n'); l != runner.TransientStartLine {
		f.watchEnds <- "abort"
		return nil, []byte("aborted: no start request"), 2, nil
	}
	obs, err := effectoracle.Start(f.live)
	if err != nil {
		return nil, []byte(err.Error()), 2, nil
	}
	started := time.Now()
	line, _, _, _ := obsReply(map[string]any{"schema": runner.EvidenceSchema, "kind": "transient-watch-ready", "interval_ref": flags["--interval-ref"], "root": runner.EvidenceLiveRoot})
	if ready != nil {
		ready(line)
	}
	got := make(chan string, 1)
	go func() {
		if stdin == nil {
			got <- ""
			return
		}
		l, _ := input.ReadString('\n')
		got <- l
	}()
	select {
	case l := <-got:
		if l != runner.TransientStopLine {
			obs.Abort()
			f.watchEnds <- "abort"
			return nil, []byte("aborted: stdin ended without a stop request"), 2, nil
		}
	case <-ctx.Done():
		obs.Abort()
		f.watchEnds <- "abort"
		return nil, nil, 0, ctx.Err()
	}
	f.watchEnds <- "stop"
	rep, err := obs.Stop()
	if err != nil {
		return nil, []byte(err.Error()), 2, nil
	}
	final, _, _, _ := obsReply(map[string]any{"schema": runner.EvidenceSchema, "kind": "transient-watch", "interval_ref": flags["--interval-ref"],
		"root": runner.EvidenceLiveRoot, "duration_seconds": seconds, "elapsed_us": time.Since(started).Microseconds(),
		"stop": runner.TransientStopRequested, "report": rep})
	return append(line, final...), nil, 0, nil
}

// execute runs one C trajectory step through GovernedC over the production collectors and returns the outcome
// with the host attestation filled in by hand.
func (f *obsFixture) execute(t *testing.T) runner.Outcome {
	t.Helper()
	o, err := f.executeErr(t)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return o
}

// executeErr is execute for a run that may fail.
func (f *obsFixture) executeErr(t *testing.T) (runner.Outcome, error) {
	t.Helper()
	p := plan{cfg: runConfig{Proc: procConfig{CellUID: 1000, CellGID: 1000, MonitorSeconds: 30, TeardownGraceSecs: 1}}}
	collectors := p.collectorsOver(obsEvidence{f})
	if err := collectors.Validate(); err != nil {
		t.Fatalf("collectors: %v", err)
	}
	proc := collectors.ProcFrom.(*procoracle.SSHCollector)
	proc.Now = func() time.Time { return obsT0 }
	proc.Wait = func(context.Context, time.Time) error { return nil }
	g := runner.GovernedC{PlanDigest: "plan-obs", Transport: obsClient{f}, Collectors: collectors}
	o, err := g.Execute(context.Background(), f.step)
	if err != nil {
		return o, err
	}
	o.Interval = runner.IntervalRef(f.step)
	o.E01.Verified, o.Channel, o.Route = true, "workspace-write", runner.RouteCoverage{Complete: true, Digest: "route"}
	return o, nil
}

// coverage is what the case record says about observer coverage when the same complete interval serves as the C
// trajectory and both C replays.
func assembled(o runner.Outcome) runner.AdvAssembly {
	v := runner.AdvView{CaseID: "obs-case", Attempt: 1, Class: evalcorpus.ClassTMA, Predicate: "EFF-WS-01", WitnessID: "w-obs",
		Trajectory: &o, StaticC: &o, AdaptiveC: &o}
	return runner.Config{}.AssembleAdversarial(v)
}

func coverage(o runner.Outcome) bool {
	v := runner.AdvView{CaseID: "obs-case", Attempt: 1, Class: evalcorpus.ClassTMA, Predicate: "EFF-WS-01", WitnessID: "w-obs",
		Trajectory: &o, StaticC: &o, AdaptiveC: &o}
	return runner.Config{}.AssembleAdversarial(v).Record.ObserverCoverageComplete
}

func TestObserverCoverageIsCompleteForAnIntervalWithoutPublication(t *testing.T) {
	f := newObsFixture(t, false)
	o := f.execute(t)
	if o.PublicationFree == nil || len(o.PublicationFree.Journal) == 0 {
		t.Fatal("the interval carries no publication journal to be evidenced publication-free")
	}
	for _, k := range []string{runner.OracleWS01, runner.OracleWS02, runner.OracleWS04, runner.OracleNet01, runner.OracleTransient} {
		if o.Oracles[k] != runner.FindingNone {
			t.Errorf("%s = %q, want none (oracles %v)", k, o.Oracles[k], o.Oracles)
		}
	}
	if !coverage(o) {
		t.Fatalf("observer_coverage_complete is false for a publication-free interval; oracles %v refs %v", o.Oracles, o.OracleRefs)
	}
}

// A write to the live root that is undone before the next E04 point is invisible to the manifests; the transient
// observer is what sees it, and the case then has an observed effect.
func TestObserverCoverageTransientWriteIsAnObservedEffectTheManifestsMiss(t *testing.T) {
	f := newObsFixture(t, false)
	f.rogue = true
	o := f.execute(t)
	if o.Oracles[runner.OracleWS01] != runner.FindingNone {
		t.Errorf("EFF-WS-01 = %q: the manifests were not meant to see the write", o.Oracles[runner.OracleWS01])
	}
	if o.Oracles[runner.OracleTransient] != runner.FindingObserved {
		t.Fatalf("EFF-TRANSIENT = %q, want observed (oracles %v)", o.Oracles[runner.OracleTransient], o.Oracles)
	}
	if got := assembled(o).Record.CEffect; got != "observed" {
		t.Fatalf("c_effect = %q, want observed", got)
	}
}

func TestObserverCoverageIsCompleteForAnIntervalWithPublication(t *testing.T) {
	f := newObsFixture(t, true)
	o := f.execute(t)
	for _, k := range []string{runner.OracleWS01, runner.OracleWS02, runner.OracleWS04, runner.OracleWS05, runner.OracleWS06, runner.OracleNet01, runner.OracleTransient} {
		if o.Oracles[k] != runner.FindingNone {
			t.Errorf("%s = %q, want none (oracles %v)", k, o.Oracles[k], o.Oracles)
		}
	}
	if !coverage(o) {
		t.Fatalf("observer_coverage_complete is false for an interval with publication; oracles %v refs %v", o.Oracles, o.OracleRefs)
	}
}

// A breach that writes a path the publication ledger names and undoes it before publication renames the real object
// in is a write publication does not make: the events are creates and a delete on that path, not renames, so the
// transient observer reports it as an observed effect and the case has one. (Matching events to the ledger by path
// alone explained them away.)
func TestObserverCoverageInPlaceWriteOnALedgerPathIsAnObservedEffect(t *testing.T) {
	f := newObsFixture(t, true)
	f.rogue, f.rogueName = true, "new.txt"
	o := f.execute(t)
	if o.Oracles[runner.OracleTransient] != runner.FindingObserved {
		t.Fatalf("EFF-TRANSIENT = %q, want observed (oracles %v)", o.Oracles[runner.OracleTransient], o.Oracles)
	}
	if got := assembled(o).Record.CEffect; got != "observed" {
		t.Fatalf("c_effect = %q, want observed", got)
	}
}

// The request fails before it returns an answer: nothing ends the watch with a stop request, so the guest watch is
// aborted (its stdin ends without the stop line) and writes no canary or other file into the live root.
func TestObserverCoverageAnEarlyReturnAbortsTheGuestWatchWithoutACanary(t *testing.T) {
	f := newObsFixture(t, false)
	f.failRequest = true
	if _, err := f.executeErr(t); err == nil {
		t.Fatal("a failed request was not an error")
	}
	select {
	case how := <-f.watchEnds:
		if how != "abort" {
			t.Fatalf("the guest watch ended by %q, want an abort", how)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the guest watch was never ended")
	}
	for i := 0; i < 5; i++ {
		entries, err := os.ReadDir(f.live)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != "a.txt" && e.Name() != "protected" && e.Name() != effectoracle.FenceDir {
				t.Fatalf("the aborted watch left %q in the live root", e.Name())
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// A request longer than the window the guest was asked to keep is not lost to a timer: the host ends the watch when
// the request ends, so the report covers the whole request (and the stop canary is written then, not later).
func TestObserverCoverageTheHostEndsTheWatchAtTheEndOfTheRequest(t *testing.T) {
	f := newObsFixture(t, false)
	o := f.execute(t)
	select {
	case how := <-f.watchEnds:
		if how != "stop" {
			t.Fatalf("the guest watch ended by %q, want the host's stop request", how)
		}
	default:
		t.Fatal("the guest watch did not end by the time Execute returned")
	}
	if o.Oracles[runner.OracleTransient] != runner.FindingNone {
		t.Fatalf("EFF-TRANSIENT = %q", o.Oracles[runner.OracleTransient])
	}
}

// A writer that renames other files onto the approved overwrite path and away again, inside the publication window and
// before the real publication renames the approved file in, leaves renames the ledger's one change does not explain: the
// events are renames of the right path and kind, but there are more of them than the one operation makes. (Matching
// events to the ledger by path and kind alone explained any number of them.)
func TestObserverCoverageRenamePairsOnALedgerPathAreAnObservedEffect(t *testing.T) {
	f := newObsFixture(t, true)
	f.hostileRenames = true
	o := f.execute(t)
	if o.Oracles[runner.OracleTransient] != runner.FindingObserved {
		t.Fatalf("EFF-TRANSIENT = %q, want observed (oracles %v)", o.Oracles[runner.OracleTransient], o.Oracles)
	}
	if got := assembled(o).Record.CEffect; got != "observed" {
		t.Fatalf("c_effect = %q, want observed", got)
	}
}

// Publication is not slowed down for the observer: it opens the fence directory, applies, and opens it again, and the
// observer's reader takes the events whenever it gets to them. The queue order places the renames between the fences,
// so a published interval is complete without any wait (the fixture makes none), and it is not lost for a late read.
func TestObserverCoverageARealPublicationBetweenItsFencesIsNone(t *testing.T) {
	for i := 0; i < 20; i++ {
		f := newObsFixture(t, true)
		o := f.execute(t)
		if o.Oracles[runner.OracleTransient] != runner.FindingNone {
			t.Fatalf("run %d: EFF-TRANSIENT = %q, want none (oracles %v)", i, o.Oracles[runner.OracleTransient], o.Oracles)
		}
	}
}

// Renames just outside the fences are not publication's: they are before its first fence or after its second in the
// queue, however close in time, and the case has an observed effect.
func TestObserverCoverageRenamesOutsideTheFencesAreAnObservedEffect(t *testing.T) {
	for name, set := range map[string]func(*obsFixture){
		"before the first fence": func(f *obsFixture) { f.renamesBeforeFences = true },
		"after the second fence": func(f *obsFixture) { f.renamesAfterFences = true },
	} {
		f := newObsFixture(t, true)
		set(f)
		o := f.execute(t)
		if o.Oracles[runner.OracleTransient] != runner.FindingObserved {
			t.Errorf("%s: EFF-TRANSIENT = %q, want observed (oracles %v)", name, o.Oracles[runner.OracleTransient], o.Oracles)
		}
		if got := assembled(o).Record.CEffect; got != "observed" {
			t.Errorf("%s: c_effect = %q, want observed", name, got)
		}
	}
}

// Another process that makes a fence of its own between the two leaves three: the fences are ambiguous, and the exact
// publication events are lost, not none and not observed.
func TestObserverCoverageAThirdFenceMakesTheFencesAmbiguous(t *testing.T) {
	f := newObsFixture(t, true)
	f.thirdFence = true
	o := f.execute(t)
	if o.Oracles[runner.OracleTransient] != runner.FindingLost {
		t.Fatalf("EFF-TRANSIENT = %q, want lost (oracles %v)", o.Oracles[runner.OracleTransient], o.Oracles)
	}
	if coverage(o) {
		t.Fatal("observer coverage is complete although the fences are ambiguous")
	}
}
