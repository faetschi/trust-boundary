//go:build linux

package procoracle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProc is a /proc tree in a temp directory. Only the entries the oracle
// reads exist, so a test removes or corrupts one to make a read fail.
type fakeProc struct {
	t    *testing.T
	root string
}

const fakeCell = "/system.slice/tbound-pi.service/cells/pi-wf-0123456789abcdef01234567-e1"

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	f := &fakeProc{t: t, root: t.TempDir()}
	f.mkdir("self/ns")
	if err := os.Symlink("pid:[4026531836]", filepath.Join(f.root, "self/ns/pid")); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeProc) mkdir(rel string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.root, rel), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeProc) write(rel, content string) {
	f.t.Helper()
	f.mkdir(filepath.Dir(rel))
	if err := os.WriteFile(filepath.Join(f.root, rel), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// addProc creates a process with an empty fd directory, one task and empty
// /proc/<pid>/net tables.
func (f *fakeProc) addProc(pid int, start uint64, comm, state string, ppid int, cgroup string) {
	f.t.Helper()
	d := fmt.Sprintf("%d", pid)
	f.write(d+"/stat", fmt.Sprintf("%d (%s) %s %d %s%d 1000 1\n", pid, comm, state, ppid, strings.Repeat("0 ", 17), start))
	f.write(d+"/cgroup", "0::"+cgroup+"\n")
	f.mkdir(d + "/fd")
	f.write(fmt.Sprintf("%s/task/%d/cgroup", d, pid), "0::"+cgroup+"\n")
	hdr := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	for _, tbl := range []string{"tcp", "tcp6", "udp", "udp6"} {
		f.write(d+"/net/"+tbl, hdr)
	}
	f.write(d+"/net/unix", "Num       RefCount Protocol Flags    Type St Inode Path\n")
}

func (f *fakeProc) addSocketFD(pid, fd int, inode uint64) {
	f.t.Helper()
	if err := os.Symlink(fmt.Sprintf("socket:[%d]", inode), filepath.Join(f.root, fmt.Sprintf("%d/fd/%d", pid, fd))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeProc) appendNet(pid int, table, row string) {
	f.t.Helper()
	p := filepath.Join(f.root, fmt.Sprintf("%d/net/%s", pid, table))
	cur, err := os.ReadFile(p)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, append(cur, []byte(row+"\n")...), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

type fakeCellReader struct {
	facts CellFacts
	err   error
}

func (c fakeCellReader) Observe() (CellFacts, error) { return c.facts, c.err }

func (f *fakeProc) view() HostView {
	return HostView{ProcRoot: f.root, Now: func() time.Time { return t0.Add(time.Second) }}
}

func cellOf(pids ...int) fakeCellReader {
	return fakeCellReader{facts: CellFacts{HierarchyPath: fakeCell, Populated: len(pids) > 0, PIDs: pids}}
}

func params(pt Point, reg ...ProcID) CaptureParams {
	return CaptureParams{Point: pt, Seq: 1, Deadline: t0, Registry: Registry{Recorded: true, Procs: reg}}
}

func gapDomains(s Snapshot) []string {
	var out []string
	for _, g := range s.Gaps {
		out = append(out, g.Domain+"/"+g.Code)
	}
	return out
}

func wantGap(t *testing.T, s Snapshot, domain string) {
	t.Helper()
	for _, g := range s.Gaps {
		if g.Domain == domain {
			return
		}
	}
	t.Errorf("want a %s gap, got %v", domain, gapDomains(s))
}

func TestCaptureRecordsProcessesAndSocketsFromProcfs(t *testing.T) {
	f := newFakeProc(t)
	// The comm may hold spaces and parentheses: the parser anchors on the last ")".
	f.addProc(100, 5000, "pi (x) y", "S", 1, fakeCell)
	f.addSocketFD(100, 3, 5555)
	f.addSocketFD(100, 4, 6666)
	f.addSocketFD(100, 5, 7777)
	f.addSocketFD(100, 6, 8888)
	f.appendNet(100, "tcp", "   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 5555 1 0000000000000000 100 0 0 10 0")
	f.appendNet(100, "udp6", "   1: 00000000000000000000000001000000:0050 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 6666 2 0000000000000000 0")
	f.appendNet(100, "unix", "0000000000000000: 00000002 00000000 00010000 0001 01 7777 /tmp/svc.sock")
	f.appendNet(100, "unix", "0000000000000000: 00000003 00000000 00000000 0001 03 8888")

	s := Capture(cellOf(100), f.view(), params(PointFinalCleanup, ProcID{PID: 100, StartTicks: 5000}))

	if len(s.Gaps) != 0 {
		t.Fatalf("gaps: %v", gapDomains(s))
	}
	if s.Point != PointFinalCleanup || s.Seq != 1 || s.CellPath != fakeCell || !s.Populated || s.PIDNamespace != "pid:[4026531836]" ||
		!s.ObservedAt.Equal(t0.Add(time.Second)) || !s.Deadline.Equal(t0) || !s.Registry.Recorded {
		t.Errorf("header: %+v", s)
	}
	if len(s.Processes) != 1 {
		t.Fatalf("processes: %+v", s.Processes)
	}
	p := s.Processes[0]
	if p.PID != 100 || p.StartTicks != 5000 || p.PPID != 1 || p.State != "S" || p.Comm != "pi (x) y" ||
		p.CgroupPath != fakeCell || !p.InCell || !p.Registered || len(p.OutThreads) != 0 {
		t.Errorf("process: %+v", p)
	}
	byInode := map[uint64]Socket{}
	for _, so := range s.Sockets {
		byInode[so.Inode] = so
	}
	if len(byInode) != 4 {
		t.Fatalf("sockets: %+v", s.Sockets)
	}
	owner := ProcID{PID: 100, StartTicks: 5000}
	if so := byInode[5555]; so.Proto != "tcp" || so.Local != "127.0.0.1:8080" || so.State != "LISTEN" || so.Role != RoleListener || so.Owner != owner {
		t.Errorf("tcp: %+v", so)
	}
	if so := byInode[6666]; so.Proto != "udp6" || so.Local != "[::1]:80" || so.Role != RoleListener {
		t.Errorf("udp6: %+v", so)
	}
	if so := byInode[7777]; so.Proto != "unix" || so.Local != "/tmp/svc.sock" || so.Role != RoleListener {
		t.Errorf("unix listener: %+v", so)
	}
	if so := byInode[8888]; so.Proto != "unix" || so.Local != "" || so.Role != RoleInternal {
		t.Errorf("unnamed unix: %+v", so)
	}
}

func TestCaptureRegisteredProcessOutsideTheCell(t *testing.T) {
	const outer = "/system.slice/tbound-pi.service/outer"
	f := newFakeProc(t)
	f.addProc(100, 5000, "pi", "S", 1, fakeCell)
	f.addProc(200, 6000, "sleep", "S", 100, outer)
	// pid 300 is registered with another start time: pid reuse, not our process.
	f.addProc(300, 7001, "reuse", "S", 1, outer)
	// pid 400 was registered and has exited: no /proc entry.
	s := Capture(cellOf(100), f.view(), params(PointInterval,
		ProcID{PID: 100, StartTicks: 5000}, ProcID{PID: 200, StartTicks: 6000},
		ProcID{PID: 300, StartTicks: 7000}, ProcID{PID: 400, StartTicks: 8000}))
	if len(s.Gaps) != 0 {
		t.Fatalf("gaps: %v", gapDomains(s))
	}
	if len(s.Processes) != 2 {
		t.Fatalf("processes: %+v", s.Processes)
	}
	var out Process
	for _, p := range s.Processes {
		if p.PID == 200 {
			out = p
		}
	}
	if out.InCell || !out.Registered || out.CgroupPath != outer || out.PPID != 100 {
		t.Errorf("escaped process: %+v", out)
	}
	wantVerdicts(t, Evaluate([]Snapshot{s}, Policy{}), VerdictEvidenceLost, VerdictEffectPresent, VerdictEvidenceLost)
	r := Evaluate([]Snapshot{s, finish(f, PointPostTeardown, 2), finish(f, PointFinalCleanup, 3)}, Policy{})
	if result(t, r, PredicateProc02).Verdict != VerdictEffectPresent {
		t.Errorf("crossing not detected: %+v", r.Results)
	}
}

// finish captures a later point of the same fake host with the same cell.
func finish(f *fakeProc, pt Point, seq int) Snapshot {
	p := params(pt, ProcID{PID: 100, StartTicks: 5000}, ProcID{PID: 200, StartTicks: 6000})
	p.Seq = seq
	return Capture(cellOf(100), f.view(), p)
}

func TestCaptureThreadOutsideTheCell(t *testing.T) {
	const outer = "/system.slice/tbound-pi.service/outer"
	f := newFakeProc(t)
	f.addProc(100, 5000, "pi", "S", 1, fakeCell)
	f.write("100/task/101/cgroup", "0::"+outer+"\n")
	s := Capture(cellOf(100), f.view(), params(PointInterval, ProcID{PID: 100, StartTicks: 5000}))
	if len(s.Gaps) != 0 || len(s.Processes) != 1 || len(s.Processes[0].OutThreads) != 1 ||
		s.Processes[0].OutThreads[0] != (Thread{TID: 101, CgroupPath: outer}) {
		t.Errorf("snapshot: gaps=%v %+v", gapDomains(s), s.Processes)
	}
}

func TestCaptureMissingRegistryIsAGap(t *testing.T) {
	f := newFakeProc(t)
	f.addProc(100, 5000, "pi", "S", 1, fakeCell)
	p := params(PointFinalCleanup)
	p.Registry = Registry{}
	s := Capture(cellOf(100), f.view(), p)
	wantGap(t, s, DomainRegistry)
	if s.Registry.Recorded {
		t.Error("a missing registry must stay missing")
	}
}

func TestCaptureUnreadableEvidenceIsAGap(t *testing.T) {
	type mutate func(f *fakeProc)
	cases := map[string]struct {
		domain string
		mutate mutate
	}{
		"stat unparsable": {DomainProcess, func(f *fakeProc) {
			f.write("100/stat", "garbage")
		}},
		"stat truncated": {DomainProcess, func(f *fakeProc) { f.write("100/stat", "100 (pi) S 1\n") }},
		"cgroup file unreadable": {DomainProcess, func(f *fakeProc) {
			_ = os.Remove(filepath.Join(f.root, "100/cgroup"))
		}},
		"cgroup file without a v2 line": {DomainProcess, func(f *fakeProc) { f.write("100/cgroup", "1:cpu:/x\n") }},
		"fd directory unreadable": {DomainProcess, func(f *fakeProc) {
			_ = os.RemoveAll(filepath.Join(f.root, "100/fd"))
		}},
		"task directory unreadable": {DomainProcess, func(f *fakeProc) {
			_ = os.RemoveAll(filepath.Join(f.root, "100/task"))
		}},
		"net table unreadable": {DomainSocket, func(f *fakeProc) {
			f.addSocketFD(100, 3, 5555)
			_ = os.Remove(filepath.Join(f.root, "100/net/tcp"))
		}},
		"net table malformed": {DomainSocket, func(f *fakeProc) {
			f.addSocketFD(100, 3, 5555)
			f.appendNet(100, "tcp", "0: nonsense")
		}},
		"socket inode in no table": {DomainSocket, func(f *fakeProc) { f.addSocketFD(100, 3, 4242) }},
		"pid namespace unreadable": {DomainCell, func(f *fakeProc) {
			_ = os.Remove(filepath.Join(f.root, "self/ns/pid"))
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeProc(t)
			f.addProc(100, 5000, "pi", "S", 1, fakeCell)
			tc.mutate(f)
			s := Capture(cellOf(100), f.view(), params(PointFinalCleanup, ProcID{PID: 100, StartTicks: 5000}))
			wantGap(t, s, tc.domain)
			r := Evaluate([]Snapshot{s}, Policy{})
			for _, res := range r.Results {
				if res.Verdict == VerdictNoEffect {
					t.Errorf("%s: %s must not be no_effect with a gap", name, res.Predicate)
				}
			}
		})
	}
}

func TestCaptureUnreadableProcEntryWithoutPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny reads, so an EACCES /proc entry cannot be made")
	}
	f := newFakeProc(t)
	f.addProc(100, 5000, "pi", "S", 1, fakeCell)
	fd := filepath.Join(f.root, "100/fd")
	if err := os.Chmod(fd, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fd, 0o755) })
	s := Capture(cellOf(100), f.view(), params(PointFinalCleanup, ProcID{PID: 100, StartTicks: 5000}))
	wantGap(t, s, DomainProcess)
}

func TestCaptureVanishedMemberIsNotAGapButUnregisteredMoverIs(t *testing.T) {
	f := newFakeProc(t)
	f.addProc(100, 5000, "pi", "S", 1, fakeCell)
	// pid 101 is listed by cgroup.procs but exited before it was read.
	s := Capture(cellOf(100, 101), f.view(), params(PointInterval, ProcID{PID: 100, StartTicks: 5000}))
	if len(s.Gaps) != 0 {
		t.Errorf("an exited member is not a gap: %v", gapDomains(s))
	}
	// pid 102 is listed as a member, but by the time it is read it sits outside
	// the cell and the registry never recorded it: it left unobserved.
	f.addProc(102, 5100, "mover", "S", 1, "/system.slice/elsewhere")
	s = Capture(cellOf(100, 102), f.view(), params(PointInterval, ProcID{PID: 100, StartTicks: 5000}))
	wantGap(t, s, DomainRegistry)
}

func TestCaptureCellReadFailureIsAGap(t *testing.T) {
	f := newFakeProc(t)
	s := Capture(fakeCellReader{err: errors.New("fstatfs: not cgroup2")}, f.view(), params(PointFinalCleanup))
	wantGap(t, s, DomainCell)
	wantVerdicts(t, Evaluate([]Snapshot{s}, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestCaptureVanishedCellRequiresDrainProof(t *testing.T) {
	f := newFakeProc(t)
	removed := fakeCellReader{facts: CellFacts{Removed: true}}
	proof := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	s := Capture(removed, f.view(), params(PointFinalCleanup))
	if !s.CellRemoved || s.DrainProof != "" {
		t.Fatalf("snapshot: %+v", s)
	}
	wantVerdicts(t, Evaluate([]Snapshot{s}, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)

	p := params(PointFinalCleanup)
	p.DrainProof = proof
	s = Capture(removed, f.view(), p)
	if !s.CellRemoved || s.DrainProof != proof || s.Populated || len(s.Processes) != 0 {
		t.Errorf("snapshot: %+v", s)
	}
	if r := Evaluate([]Snapshot{s}, Policy{}); result(t, r, PredicateProc03).Verdict != VerdictNoEffect {
		t.Errorf("final cleanup with a drain proof: %+v", r.Results)
	}
}

func TestCaptureThenEvaluateDetectsASurvivingListener(t *testing.T) {
	f := newFakeProc(t)
	f.addProc(100, 5000, "nc", "S", 1, fakeCell)
	f.addSocketFD(100, 3, 5555)
	f.appendNet(100, "tcp", "   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 5555 1 0000000000000000 100 0 0 10 0")
	reg := ProcID{PID: 100, StartTicks: 5000}
	snaps := []Snapshot{
		Capture(cellOf(100), f.view(), withSeq(params(PointInterval, reg), 1)),
		Capture(cellOf(100), f.view(), withSeq(params(PointPostTeardown, reg), 2)),
		Capture(cellOf(100), f.view(), withSeq(params(PointFinalCleanup, reg), 3)),
	}
	r := Evaluate(snaps, Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictEffectPresent)
	if f := result(t, r, PredicateProc03).Findings; len(f) == 0 || f[0].Socket == nil || f[0].Socket.Local != "127.0.0.1:8080" {
		t.Errorf("findings: %+v", f)
	}
}

func withSeq(p CaptureParams, seq int) CaptureParams { p.Seq = seq; return p }
