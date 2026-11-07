package procoracle

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/runner"
)

var (
	sshT0   = time.Unix(1_800_000_000, 0).UTC()
	sshCell = CellDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcCell, CellID: "/sys/fs/cgroup/system.slice/tbound-pi.service/cells",
		CellCgroupPath: "/system.slice/tbound-pi.service/cells", CellCgroupInode: 4242, CellUID: 1000, CellGID: 1000}
)

// fakeGuest answers the evidence kinds the way the guest does: it rebuilds the registration
// from its own cell and the instants and refuses a digest that is not the one it computes.
type fakeGuest struct {
	mu        sync.Mutex
	calls     [][]string
	cell      CellDoc
	failCell  bool
	failMon   bool
	failCap   map[Point]bool
	garbage   bool          // answer every capture and monitor with junk
	release   chan struct{} // the monitor returns when this closes (nil: at once)
	processes map[Point][]Process
	mutateReg func(*Registration)
	// The monitor's readiness line (the guest writes it right after its first poll).
	readyHold   chan struct{} // the ack is withheld until this closes (nil: sent at once)
	noReady     bool          // the monitor never acknowledges
	readyMutate func(*MonitorReadyDoc)
	// failMonLate: the monitor acknowledges and then dies.
	failMonLate bool
	// D49. closureRun is the run id the closure document names (default "run-1"); failClosure fails proc-closure;
	// closureMutate edits the document; stopMutate edits the monitor document that answers a stop request;
	// stopIgnored: the monitor does not act on the stop line and runs to its horizon.
	closureRun    string
	failClosure   bool
	closureMutate func(*ClosureDoc)
	stopMutate    func(*MonitorDoc)
	stopIgnored   bool
	// shortHorizon: the record claims the horizon but holds only six seconds of polls.
	shortHorizon bool
	// stops are the stop lines the monitor read from its stdin.
	stops []string
}

// Run is a transport that cannot report readiness: used to prove that such a transport
// is refused.
type plainTransport struct{ g *fakeGuest }

func (p plainTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return p.g.Run(ctx, argv)
}

func flagsOf(argv []string) map[string]string {
	m := map[string]string{}
	for i := 1; i+1 < len(argv); i += 2 {
		m[argv[i]] = argv[i+1]
	}
	return m
}

func (g *fakeGuest) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return g.RunReady(ctx, argv, nil)
}

func (g *fakeGuest) RunReady(ctx context.Context, argv []string, ready func([]byte)) ([]byte, []byte, int, error) {
	return g.RunReadyStdin(ctx, argv, nil, ready)
}

func (g *fakeGuest) RunReadyStdin(ctx context.Context, argv []string, stdin io.Reader, ready func([]byte)) ([]byte, []byte, int, error) {
	g.mu.Lock()
	g.calls = append(g.calls, append([]string(nil), argv...))
	cell, release := g.cell, g.release
	g.mu.Unlock()
	reply := func(v any) ([]byte, []byte, int, error) {
		b, _ := json.Marshal(v)
		return append(b, '\n'), nil, 0, nil
	}
	if argv[0] == runner.EvidenceProcCell {
		if g.failCell {
			return nil, []byte("no"), 2, nil
		}
		return reply(cell)
	}
	if g.garbage {
		return []byte("{not json"), nil, 0, nil
	}
	f := flagsOf(argv)
	start, err1 := ParseInstantArg(f["--interval-start"])
	deadline, err2 := ParseInstantArg(f["--deadline"])
	if err1 != nil || err2 != nil {
		return nil, []byte("bad instants"), 2, nil
	}
	reg := GuestRegistration(cell, start, deadline)
	if g.mutateReg != nil {
		g.mutateReg(&reg)
	}
	if reg.Digest() != f["--registration"] {
		return nil, []byte("registration digest mismatch"), 2, nil
	}
	base := Snapshot{RegistrationDigest: reg.Digest(), CellPath: cell.CellCgroupPath, CellInode: cell.CellCgroupInode,
		IdentityScanned: true, ScannedPIDs: 5, Registry: Registry{Recorded: true}}
	switch argv[0] {
	case runner.EvidenceProcMonitor:
		if g.failMon {
			return nil, []byte("monitor failed"), 2, nil
		}
		var readyLine []byte
		if !g.noReady {
			if g.readyHold != nil {
				select {
				case <-g.readyHold:
				case <-ctx.Done():
					return nil, nil, 0, ctx.Err()
				}
			}
			rd := MonitorReadyDoc{Schema: runner.EvidenceSchema, Kind: MonitorReadyKind, RegistrationDigest: reg.Digest(),
				FirstPollSeq: 1, FirstPollAt: start.Add(100 * time.Millisecond)}
			if g.readyMutate != nil {
				g.readyMutate(&rd)
			}
			b, _ := json.Marshal(rd)
			readyLine = append(b, '\n')
			if ready != nil {
				ready(readyLine)
			}
		}
		stopCh := make(chan MonitorStopRequest, 1)
		if stdin != nil {
			go func() {
				line, _ := bufio.NewReader(stdin).ReadString(0x0a)
				if line != "" { // an end of input without a line is not a stop request
					g.mu.Lock()
					g.stops = append(g.stops, line)
					g.mu.Unlock()
				}
				if r, err := ParseMonitorStopLine(line); err == nil && !g.stopIgnored {
					stopCh <- r
				}
			}()
		}
		var stopReq *MonitorStopRequest
		if release != nil || stdin != nil {
			var rel <-chan struct{} = release // a nil channel never fires
			select {
			case <-rel:
			case r := <-stopCh:
				stopReq = &r
			case <-ctx.Done():
				return nil, nil, 0, ctx.Err()
			}
		}
		if g.failMonLate {
			return readyLine, []byte("monitor died"), 2, nil
		}
		last := 30 // a horizon record spans the whole duration
		if d, err := strconv.Atoi(f["--duration"]); err == nil {
			last = d
		}
		if stopReq != nil {
			last = 7
		}
		if g.shortHorizon {
			last = 6
		}
		var polls []Snapshot
		for i := 1; i <= last; i++ {
			s := base
			s.Seq, s.Point, s.ObservedAt = i, PointMonitor, sshT0.Add(time.Duration(i)*time.Second)
			polls = append(polls, s)
		}
		doc := MonitorDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcMonitor, RegistrationDigest: reg.Digest(), Polls: polls,
			Stop: MonitorStop{Reason: StopHorizon, FirstPollAt: polls[0].ObservedAt, LastPollAt: polls[len(polls)-1].ObservedAt, StoppedAt: polls[len(polls)-1].ObservedAt}}
		if stopReq != nil {
			doc.Stop.Reason, doc.Stop.ClosureDigest, doc.Stop.RunID = StopClosedOnProof, stopReq.ClosureDigest, stopReq.RunID
			if g.stopMutate != nil {
				g.stopMutate(&doc)
			}
		}
		final, _, _, _ := reply(doc)
		return append(readyLine, final...), nil, 0, nil
	case runner.EvidenceProcClosure:
		if g.failClosure {
			return nil, []byte("closure failed"), 2, nil
		}
		run := g.closureRun
		if run == "" {
			run = "run-1"
		}
		scan := base
		scan.Seq, scan.Point, scan.ObservedAt = 1, PointInterval, sshT0.Add(5*time.Second)
		doc := ClosureDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcClosure, IntervalRef: f["--interval-ref"], RunID: run,
			RegistrationDigest: reg.Digest(), ReceiptVerified: true, ReceiptDigest: strings.Repeat("b", 64), ExpectedSettlements: 1,
			Commands: []CommandSettlement{settledCommand()}, NetNSGone: true, Scan: scan}
		if g.closureMutate != nil {
			g.closureMutate(&doc)
		}
		return reply(doc)
	case runner.EvidenceProcCapture:
		p := Point(f["--point"])
		seq, ok := GuestPointSeq(p)
		if !ok || g.failCap[p] {
			return nil, []byte("capture failed"), 2, nil
		}
		s := base
		s.Seq, s.Point, s.ObservedAt = seq, p, sshT0.Add(time.Duration(2+seq)*time.Second)
		s.Processes = g.processes[p]
		return reply(CaptureDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcCapture, Point: p, RegistrationDigest: reg.Digest(), Snapshot: s})
	}
	return nil, nil, 2, nil
}

func (g *fakeGuest) argvs() [][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([][]string(nil), g.calls...)
}

func (g *fakeGuest) stopLines() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.stops...)
}

func newSSHCollector(g *fakeGuest) (*SSHCollector, *[]time.Time) {
	var waited []time.Time
	c := &SSHCollector{Client: runner.EvidenceClient{Transport: g}, CellUID: 1000, CellGID: 1000,
		MonitorDuration: 30 * time.Second, TeardownGrace: time.Second,
		Now:  func() time.Time { return sshT0 },
		Wait: func(_ context.Context, until time.Time) error { waited = append(waited, until); return nil }}
	return c, &waited
}

var sshRef = runner.EvidenceRef{Interval: "case|trajectory|C|0#1"}

func TestSSHCollectorCleanIntervalIsNoneWithReportReferences(t *testing.T) {
	g := &fakeGuest{cell: sshCell, release: make(chan struct{})}
	c, waited := newSSHCollector(g)
	if err := c.BeginInterval(context.Background(), sshRef); err != nil {
		t.Fatal(err)
	}
	// The monitor runs in parallel with the request: Begin returned while it is still going.
	got := g.argvs()
	reg := GuestRegistration(sshCell, sshT0, sshT0.Add(time.Second))
	wantMonitor := []string{"proc-monitor", "--registration", reg.Digest(), "--interval-start", InstantArg(sshT0),
		"--deadline", InstantArg(sshT0.Add(time.Second)), "--duration", "30", "--interval", "500"}
	if got[0][0] != "proc-cell" || len(got[0]) != 1 {
		t.Fatalf("first call = %q, want proc-cell with no argument", got[0])
	}
	deadline := time.After(2 * time.Second)
	for len(g.argvs()) < 2 {
		select {
		case <-deadline:
			t.Fatal("the monitor was not started by Begin")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if got := g.argvs(); !reflect.DeepEqual(got[1], wantMonitor) {
		t.Fatalf("monitor argv = %q, want %q", got[1], wantMonitor)
	}
	close(g.release) // the request is over and the monitor's time is up
	ev, err := c.Proc(context.Background(), sshRef)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{runner.OracleProc01, runner.OracleProc02, runner.OracleProc03} {
		if ev.Findings[k] != runner.FindingNone {
			t.Errorf("%s = %q on a clean interval", k, ev.Findings[k])
		}
		if r := ev.Refs[k]; r.Version != runner.ProcOracleVersion || len(r.Report) != 64 {
			t.Errorf("%s ref = %+v", k, r)
		}
	}
	if len(*waited) != 1 || !(*waited)[0].Equal(sshT0.Add(time.Second)) {
		t.Fatalf("Proc did not wait for the teardown deadline: %v", *waited)
	}
	all := g.argvs()
	if len(all) != 4 || all[2][0] != "proc-capture" || all[3][0] != "proc-capture" || flagsOf(all[2])["--point"] != "post_teardown" || flagsOf(all[3])["--point"] != "final_cleanup" {
		t.Fatalf("calls = %q", all)
	}
	c.EndInterval(sshRef)
}

func TestSSHCollectorSurvivorAndEscapeAreObserved(t *testing.T) {
	survivor := Process{PID: 777, StartTicks: 9, PPID: 1, State: "S", Comm: "sleep", CgroupPath: "/user.slice", CellIdentity: true, UID: [3]uint32{1000, 1000, 1000}}
	g := &fakeGuest{cell: sshCell, processes: map[Point][]Process{PointPostTeardown: {survivor}, PointFinalCleanup: {survivor}}}
	c, _ := newSSHCollector(g)
	if err := c.BeginInterval(context.Background(), sshRef); err != nil {
		t.Fatal(err)
	}
	ev, err := c.Proc(context.Background(), sshRef)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Findings[runner.OracleProc01] != runner.FindingObserved || ev.Findings[runner.OracleProc02] != runner.FindingObserved {
		t.Fatalf("findings = %v", ev.Findings)
	}
}

func TestSSHCollectorFailuresLeaveEvidenceLostOrAbsentNeverNone(t *testing.T) {
	// The monitor failing: PROC-02 has no coverage and is lost; the snapshots still speak.
	g := &fakeGuest{cell: sshCell, failMonLate: true}
	c, _ := newSSHCollector(g)
	if err := c.BeginInterval(context.Background(), sshRef); err != nil {
		t.Fatal(err)
	}
	ev, err := c.Proc(context.Background(), sshRef)
	if err != nil || ev.Findings[runner.OracleProc02] != runner.FindingLost || ev.Findings[runner.OracleProc01] != runner.FindingNone {
		t.Fatalf("monitor failure: %v %v", ev.Findings, err)
	}
	// A capture failing loses the predicates that need that point.
	g = &fakeGuest{cell: sshCell, failCap: map[Point]bool{PointFinalCleanup: true}}
	c, _ = newSSHCollector(g)
	_ = c.BeginInterval(context.Background(), sshRef)
	ev, err = c.Proc(context.Background(), sshRef)
	if err != nil || ev.Findings[runner.OracleProc03] != runner.FindingLost {
		t.Fatalf("capture failure: %v %v", ev.Findings, err)
	}
	// Nothing usable at all: absent.
	for name, g := range map[string]*fakeGuest{
		"garbage":      {cell: sshCell, garbage: true},
		"all failed":   {cell: sshCell, failMon: true, failCap: map[Point]bool{PointPostTeardown: true, PointFinalCleanup: true}},
		"wrong digest": {cell: sshCell, mutateReg: func(r *Registration) { r.CellUID++ }},
	} {
		c, _ := newSSHCollector(g)
		// A monitor that never acknowledged leaves Begin absent already.
		if err := c.BeginInterval(context.Background(), sshRef); err != nil && !errors.Is(err, runner.ErrEvidenceAbsent) {
			t.Fatalf("%s: %v", name, err)
		}
		if ev, err := c.Proc(context.Background(), sshRef); !errors.Is(err, runner.ErrEvidenceAbsent) {
			t.Errorf("%s: %v %v, want ErrEvidenceAbsent", name, ev, err)
		}
	}
	// No Begin, or an SSH failure on the cell: absent.
	c, _ = newSSHCollector(&fakeGuest{cell: sshCell})
	if _, err := c.Proc(context.Background(), sshRef); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("Proc without Begin: %v", err)
	}
	c, _ = newSSHCollector(&fakeGuest{cell: sshCell, failCell: true})
	if err := c.BeginInterval(context.Background(), sshRef); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("Begin with an unresolvable cell: %v", err)
	}
}

func TestSSHCollectorRefusesAGuestCellItWasNotConfiguredFor(t *testing.T) {
	for name, mutate := range map[string]func(*CellDoc){
		"other uid":    func(d *CellDoc) { d.CellUID = 1001 },
		"other gid":    func(d *CellDoc) { d.CellGID = 1001 },
		"no inode":     func(d *CellDoc) { d.CellCgroupInode = 0 },
		"no path":      func(d *CellDoc) { d.CellCgroupPath = "" },
		"no id":        func(d *CellDoc) { d.CellID = "" },
		"wrong kind":   func(d *CellDoc) { d.Kind = "x" },
		"wrong schema": func(d *CellDoc) { d.Schema = "x" },
	} {
		cell := sshCell
		mutate(&cell)
		c, _ := newSSHCollector(&fakeGuest{cell: cell})
		if err := c.BeginInterval(context.Background(), sshRef); !errors.Is(err, runner.ErrEvidenceAbsent) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestSSHCollectorEndIntervalStopsTheMonitor(t *testing.T) {
	g := &fakeGuest{cell: sshCell, release: make(chan struct{})}
	c, _ := newSSHCollector(g)
	if err := c.BeginInterval(context.Background(), sshRef); err != nil {
		t.Fatal(err)
	}
	c.EndInterval(sshRef)
	if _, err := c.Proc(context.Background(), sshRef); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("Proc after EndInterval: %v", err)
	}
}

func TestSSHCollectorValidate(t *testing.T) {
	ok := func() *SSHCollector {
		c, _ := newSSHCollector(&fakeGuest{})
		return c
	}
	if err := ok().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SSHCollector){
		"no uid":         func(c *SSHCollector) { c.CellUID = 0 },
		"short monitor":  func(c *SSHCollector) { c.MonitorDuration = time.Millisecond },
		"grace too long": func(c *SSHCollector) { c.TeardownGrace = time.Minute },
		"slow polls":     func(c *SSHCollector) { c.PollInterval = GuestMaxGap },
		"no transport":   func(c *SSHCollector) { c.Client = runner.EvidenceClient{} },
		"long monitor":   func(c *SSHCollector) { c.MonitorDuration = MaxGuestMonitorDuration + time.Second },
		"fast polls":     func(c *SSHCollector) { c.PollInterval = MinGuestPollInterval - time.Millisecond },
		"no readiness":   func(c *SSHCollector) { c.Client = runner.EvidenceClient{Transport: plainTransport{}} },
	} {
		c := ok()
		mutate(c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseInstantArg("12.5"); err == nil {
		t.Error("fractional instant")
	}
	if _, err := ParseInstantArg("-1"); err == nil {
		t.Error("negative instant")
	}
	if got, err := ParseInstantArg(strconv.FormatInt(sshT0.UnixNano(), 10)); err != nil || !got.Equal(sshT0) {
		t.Errorf("round trip: %v %v", got, err)
	}
}

func TestGuestMaxGapIsASmallMultipleOfThePollInterval(t *testing.T) {
	if GuestMaxGap > 2*GuestPollInterval {
		t.Fatalf("GuestMaxGap %s is more than twice the poll interval %s", GuestMaxGap, GuestPollInterval)
	}
	if GuestPollInterval < MinGuestPollInterval || MaxGuestMonitorDuration > 1500*time.Second {
		t.Fatalf("guest monitor bounds: poll %s duration %s", GuestPollInterval, MaxGuestMonitorDuration)
	}
}

// Codex 00f77c6: the monitor used to be started without waiting for its first poll, so a
// short request could be over before monitoring began. BeginInterval now returns only after
// the guest acknowledged its first poll, so the request is never sent before it.
func TestSSHCollectorBeginWaitsForTheMonitorsFirstPoll(t *testing.T) {
	g := &fakeGuest{cell: sshCell, readyHold: make(chan struct{}), release: make(chan struct{})}
	c, _ := newSSHCollector(g)
	returned := make(chan error, 1)
	go func() { returned <- c.BeginInterval(context.Background(), sshRef) }()
	select {
	case err := <-returned:
		t.Fatalf("BeginInterval returned (%v) before the monitor acknowledged its first poll", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(g.readyHold)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BeginInterval did not return after the acknowledgement")
	}
	close(g.release)
	if _, err := c.Proc(context.Background(), sshRef); err != nil {
		t.Fatal(err)
	}
}

func TestSSHCollectorBeginWithoutAValidAcknowledgementIsAbsentAndStartsNothing(t *testing.T) {
	for name, g := range map[string]*fakeGuest{
		"no ack, monitor keeps running": {cell: sshCell, noReady: true, release: make(chan struct{})},
		"ack withheld":                  {cell: sshCell, readyHold: make(chan struct{})},
		"other digest": {cell: sshCell, release: make(chan struct{}),
			readyMutate: func(d *MonitorReadyDoc) { d.RegistrationDigest = strings.Repeat("0", 64) }},
		"wrong kind":   {cell: sshCell, release: make(chan struct{}), readyMutate: func(d *MonitorReadyDoc) { d.Kind = "x" }},
		"wrong schema": {cell: sshCell, release: make(chan struct{}), readyMutate: func(d *MonitorReadyDoc) { d.Schema = "x" }},
		"not the first poll": {cell: sshCell, release: make(chan struct{}),
			readyMutate: func(d *MonitorReadyDoc) { d.FirstPollSeq = 2 }},
		"first poll later than MaxGap": {cell: sshCell, release: make(chan struct{}),
			readyMutate: func(d *MonitorReadyDoc) { d.FirstPollAt = sshT0.Add(GuestMaxGap + time.Millisecond) }},
		"no first poll time":        {cell: sshCell, release: make(chan struct{}), readyMutate: func(d *MonitorReadyDoc) { d.FirstPollAt = time.Time{} }},
		"monitor ended without ack": {cell: sshCell, failMon: true},
	} {
		c, _ := newSSHCollector(g)
		c.ReadyTimeout = 100 * time.Millisecond
		err := c.BeginInterval(context.Background(), sshRef)
		if !errors.Is(err, runner.ErrEvidenceAbsent) {
			t.Errorf("%s: Begin err = %v, want ErrEvidenceAbsent", name, err)
		}
		if _, err := c.Proc(context.Background(), sshRef); !errors.Is(err, runner.ErrEvidenceAbsent) {
			t.Errorf("%s: Proc err = %v, want ErrEvidenceAbsent (no interval may stand)", name, err)
		}
	}
}

func TestSSHCollectorRefusesATransportThatCannotReportReadiness(t *testing.T) {
	c, _ := newSSHCollector(&fakeGuest{cell: sshCell})
	c.Client = runner.EvidenceClient{Transport: plainTransport{g: &fakeGuest{cell: sshCell}}}
	if err := c.BeginInterval(context.Background(), sshRef); err == nil || errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("Begin over a non-streaming transport = %v, want a configuration error", err)
	}
}
