package procoracle

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
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
}

func flagsOf(argv []string) map[string]string {
	m := map[string]string{}
	for i := 1; i+1 < len(argv); i += 2 {
		m[argv[i]] = argv[i+1]
	}
	return m
}

func (g *fakeGuest) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
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
		if release != nil {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, nil, 0, ctx.Err()
			}
		}
		if g.failMon {
			return nil, []byte("monitor failed"), 2, nil
		}
		var polls []Snapshot
		for i := 1; i <= 6; i++ {
			s := base
			s.Seq, s.Point, s.ObservedAt = i, PointMonitor, sshT0.Add(time.Duration(i)*time.Second)
			polls = append(polls, s)
		}
		return reply(MonitorDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceProcMonitor, RegistrationDigest: reg.Digest(), Polls: polls})
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
	g := &fakeGuest{cell: sshCell, failMon: true}
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
		if err := c.BeginInterval(context.Background(), sshRef); err != nil {
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
