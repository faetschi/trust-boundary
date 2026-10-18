//go:build linux

package procoracle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProc is a /proc tree in a temp directory. Only the entries the oracle
// reads exist, so a test removes or corrupts one to make a read fail. It always
// holds pid 1 (root) and the scanner (pid 999, uid 5000, /proc/self).
type fakeProc struct {
	t     *testing.T
	root  string
	netns map[int]string // pid -> network namespace link
}

const (
	fakeCell = testCell
	fakeUID  = 1000 // the cell identity of testReg
	hostNet  = "net:[4026531840]"
	outerCg  = "/system.slice/tbound-pi.service/outer"
)

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	f := &fakeProc{t: t, root: t.TempDir(), netns: map[int]string{}}
	f.addProcUID(0, 1, 1, "init", "S", 0, "/init.scope")
	f.addProcUID(5000, 999, 2, "scanner", "R", 1, "/user.slice")
	if err := os.Symlink("999", filepath.Join(f.root, "self")); err != nil {
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

func (f *fakeProc) symlink(target, rel string) {
	f.t.Helper()
	f.mkdir(filepath.Dir(rel))
	p := filepath.Join(f.root, rel)
	_ = os.Remove(p)
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatal(err)
	}
}

// addProc creates a process of the cell identity (uid 1000) with an empty fd
// directory, one task and empty /proc/<pid>/net tables.
func (f *fakeProc) addProc(pid int, start uint64, comm, state string, ppid int, cgroup string) {
	f.t.Helper()
	f.addProcUID(fakeUID, pid, start, comm, state, ppid, cgroup)
}

func (f *fakeProc) addProcUID(uid uint32, pid int, start uint64, comm, state string, ppid int, cgroup string) {
	f.t.Helper()
	d := fmt.Sprintf("%d", pid)
	f.write(d+"/stat", fmt.Sprintf("%d (%s) %s %d %s%d 1000 1\n", pid, comm, state, ppid, strings.Repeat("0 ", 17), start))
	f.write(d+"/status", fmt.Sprintf("Name:\t%s\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\n", comm, uid, uid, uid, uid, uid, uid, uid, uid))
	f.write(d+"/cgroup", "0::"+cgroup+"\n")
	f.mkdir(d + "/fd")
	f.write(fmt.Sprintf("%s/task/%d/cgroup", d, pid), "0::"+cgroup+"\n")
	f.symlink("pid:[4026531836]", d+"/ns/pid")
	f.setNetNS(pid, hostNet)
	hdr := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	for _, tbl := range []string{"tcp", "tcp6", "udp", "udp6"} {
		f.write(d+"/net/"+tbl, hdr)
	}
	f.write(d+"/net/unix", "Num       RefCount Protocol Flags    Type St Inode Path\n")
}

// setNetNS puts a process into a network namespace; processes of one namespace
// share every row added with appendNet.
func (f *fakeProc) setNetNS(pid int, link string) {
	f.t.Helper()
	f.netns[pid] = link
	f.symlink(link, fmt.Sprintf("%d/ns/net", pid))
}

func (f *fakeProc) addSocketFD(pid, fd int, inode uint64) {
	f.t.Helper()
	if err := os.Symlink(fmt.Sprintf("socket:[%d]", inode), filepath.Join(f.root, fmt.Sprintf("%d/fd/%d", pid, fd))); err != nil {
		f.t.Fatal(err)
	}
}

// appendNet adds a row to the table of the network namespace of pid: every
// process of the namespace (and the scanner, when it is the host namespace) sees it.
func (f *fakeProc) appendNet(pid int, table, row string) {
	f.t.Helper()
	for other, link := range f.netns {
		if link != f.netns[pid] {
			continue
		}
		p := filepath.Join(f.root, fmt.Sprintf("%d/net/%s", other, table))
		cur, err := os.ReadFile(p)
		if err != nil {
			continue // a test removed this table on purpose
		}
		if err := os.WriteFile(p, append(cur, []byte(row+"\n")...), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

// inetRow is one /proc/net/tcp row (127.0.0.1 and the port in hex).
func inetRow(state string, port int, uid uint32, inode uint64) string {
	return fmt.Sprintf("   0: 0100007F:%04X 00000000:0000 %s 00000000:00000000 00:00000000 00000000  %d        0 %d 1 0000000000000000 100 0 0 10 0",
		port, state, uid, inode)
}

type fakeCellReader struct {
	facts CellFacts
	err   error
}

func (c fakeCellReader) Observe() (CellFacts, error) { return c.facts, c.err }

// fakeClock makes every observation of the fixtures a microsecond later than the
// last, starting just after the deadline: snapshot times must be strictly ordered.
var fakeClock atomic.Int64

func (f *fakeProc) view() HostView {
	return HostView{ProcRoot: f.root, Now: func() time.Time {
		return t0.Add(time.Second + time.Duration(fakeClock.Add(1))*time.Microsecond)
	}}
}

func cellOf(pids ...int) fakeCellReader {
	return fakeCellReader{facts: CellFacts{HierarchyPath: fakeCell, Inode: testInode, Populated: len(pids) > 0, PIDs: pids}}
}

func params(pt Point, reg ...ProcID) CaptureParams {
	return CaptureParams{Point: pt, Seq: 1, Registration: testReg, Registry: Registry{Recorded: true, Procs: reg}}
}

func withSeq(p CaptureParams, seq int) CaptureParams { p.Seq = seq; return p }

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

func wantGapCode(t *testing.T, s Snapshot, domain, code string) {
	t.Helper()
	for _, g := range s.Gaps {
		if g.Domain == domain && g.Code == code {
			return
		}
	}
	t.Errorf("want a %s/%s gap, got %v", domain, code, gapDomains(s))
}

// threePoints captures interval, post-teardown and final cleanup of one fake host.
func threePoints(f *fakeProc, cell fakeCellReader, reg ...ProcID) []Snapshot {
	return []Snapshot{
		Capture(cell, f.view(), withSeq(params(PointInterval, reg...), 1)),
		Capture(cell, f.view(), withSeq(params(PointPostTeardown, reg...), 2)),
		Capture(cell, f.view(), withSeq(params(PointFinalCleanup, reg...), 3)),
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not deny reads, so an EACCES /proc entry cannot be made")
	}
}

func TestCaptureRecordsProcessesAndSocketsFromProcfs(t *testing.T) {
	f := newFakeProc(t)
	// The comm may hold spaces and parentheses: the parser anchors on the last ")".
	f.addProc(100, 5000, "pi (x) y", "S", 1, fakeCell)
	f.addSocketFD(100, 3, 5555)
	f.addSocketFD(100, 4, 6666)
	f.addSocketFD(100, 5, 7777)
	f.addSocketFD(100, 6, 8888)
	f.appendNet(100, "tcp", inetRow("0A", 8080, 1000, 5555))
	f.appendNet(100, "udp6", "   1: 00000000000000000000000001000000:0050 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 6666 2 0000000000000000 0")
	f.appendNet(100, "unix", "0000000000000000: 00000002 00000000 00010000 0001 01 7777 /tmp/svc.sock")
	f.appendNet(100, "unix", "0000000000000000: 00000003 00000000 00000000 0001 03 8888")

	s := Capture(cellOf(100), f.view(), params(PointFinalCleanup, ProcID{PID: 100, StartTicks: 5000}))

	if len(s.Gaps) != 0 {
		t.Fatalf("gaps: %v", gapDomains(s))
	}
	if s.Point != PointFinalCleanup || s.Seq != 1 || s.CellPath != fakeCell || !s.Populated || s.PIDNamespace != "pid:[4026531836]" ||
		s.ObservedAt.Before(t0.Add(time.Second)) || s.ObservedAt.After(t0.Add(2*time.Second)) || s.RegistrationDigest != testReg.Digest() || !s.Registry.Recorded ||
		!s.IdentityScanned || s.ScannedPIDs != 3 {
		t.Errorf("header: %+v", s)
	}
	if len(s.Processes) != 1 {
		t.Fatalf("processes: %+v", s.Processes)
	}
	p := s.Processes[0]
	if p.PID != 100 || p.StartTicks != 5000 || p.PPID != 1 || p.State != "S" || p.Comm != "pi (x) y" ||
		p.CgroupPath != fakeCell || !p.InCell || !p.Registered || !p.CellIdentity || p.UID != [3]uint32{1000, 1000, 1000} || len(p.OutThreads) != 0 {
		t.Errorf("process: %+v", p)
	}
	byInode := map[uint64]Socket{}
	for _, so := range s.Sockets {
		byInode[so.Inode] = so
	}
	if len(byInode) != 4 || len(s.Sockets) != 4 {
		t.Fatalf("sockets: %+v", s.Sockets)
	}
	owner := ProcID{PID: 100, StartTicks: 5000}
	if so := byInode[5555]; so.Proto != "tcp" || so.Local != "127.0.0.1:8080" || so.State != "LISTEN" || so.Role != RoleListener || so.Owner != owner || so.UID != 1000 {
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
	f := newFakeProc(t)
	f.addProc(100, 5000, "pi", "S", 1, fakeCell)
	// The registry names pid 200, which has another uid: only the registry finds it.
	f.addProcUID(2000, 200, 6000, "sleep", "S", 100, outerCg)
	// pid 300 is registered with another start time: pid reuse, not our process.
	f.addProcUID(2000, 300, 7001, "reuse", "S", 1, outerCg)
	// pid 400 was registered and has exited: no /proc entry.
	reg := []ProcID{{PID: 100, StartTicks: 5000}, {PID: 200, StartTicks: 6000}, {PID: 300, StartTicks: 7000}, {PID: 400, StartTicks: 8000}}
	s := Capture(cellOf(100), f.view(), params(PointInterval, reg...))
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
	if out.InCell || !out.Registered || out.CellIdentity || out.CgroupPath != outerCg || out.PPID != 100 {
		t.Errorf("escaped process: %+v", out)
	}
	snaps := []Snapshot{s, finishAt(f, reg, PointPostTeardown, 2), finishAt(f, reg, PointFinalCleanup, 3)}
	if r := evalT(snaps, Policy{}); result(t, r, PredicateProc02).Verdict != VerdictEffectPresent {
		t.Errorf("crossing not detected: %+v", r.Results)
	}
}

// finishAt captures a later point of the same fake host with the same cell.
func finishAt(f *fakeProc, reg []ProcID, pt Point, seq int) Snapshot {
	return Capture(cellOf(100), f.view(), withSeq(params(pt, reg...), seq))
}

func TestCaptureThreadOutsideTheCell(t *testing.T) {
	f := newFakeProc(t)
	f.addProc(100, 5000, "pi", "S", 1, fakeCell)
	f.write("100/task/101/cgroup", "0::"+outerCg+"\n")
	f.write("100/task/102/cgroup", "0::"+fakeCell+"/nested\n") // a nested cgroup is inside the cell
	s := Capture(cellOf(100), f.view(), params(PointInterval, ProcID{PID: 100, StartTicks: 5000}))
	if len(s.Gaps) != 0 || len(s.Processes) != 1 || len(s.Processes[0].OutThreads) != 1 ||
		s.Processes[0].OutThreads[0] != (Thread{TID: 101, CgroupPath: outerCg}) {
		t.Errorf("snapshot: gaps=%v %+v", gapDomains(s), s.Processes)
	}
}

// Finding 3 end to end: only a nested-cgroup thread, so no PROC-02.
func TestCaptureNestedThreadIsNotProc02(t *testing.T) {
	f := newFakeProc(t)
	f.addProc(100, 5000, "pi", "S", 1, fakeCell)
	f.write("100/task/101/cgroup", "0::"+fakeCell+"/nested\n")
	reg := ProcID{PID: 100, StartTicks: 5000}
	first := Capture(cellOf(100), f.view(), withSeq(params(PointInterval, reg), 1))
	if len(first.Processes[0].OutThreads) != 0 {
		t.Fatalf("a nested thread was reported: %+v", first.Processes[0].OutThreads)
	}
	post := Capture(cellOf(), fakeProcWithout(f, 100).view(), withSeq(params(PointPostTeardown), 2))
	final := Capture(cellOf(), fakeProcWithout(f, 100).view(), withSeq(params(PointFinalCleanup), 3))
	r := evalT([]Snapshot{first, post, final}, Policy{})
	if got := result(t, r, PredicateProc02).Verdict; got != VerdictNoEffect {
		t.Errorf("PROC-02 = %s, gaps %+v findings %+v", got, result(t, r, PredicateProc02).Gaps, result(t, r, PredicateProc02).Findings)
	}
}

// fakeProcWithout removes a process from the fake host (it exited) and returns it.
func fakeProcWithout(f *fakeProc, pid int) *fakeProc {
	if err := os.RemoveAll(filepath.Join(f.root, fmt.Sprint(pid))); err != nil {
		f.t.Fatal(err)
	}
	delete(f.netns, pid)
	return f
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
		"status malformed": {DomainIdentity, func(f *fakeProc) { f.write("100/status", "Name:\tpi\nUid:\tx\n") }},
		"status without a Uid line": {DomainIdentity, func(f *fakeProc) {
			f.write("100/status", "Name:\tpi\n")
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
		"host net table unreadable": {DomainSocket, func(f *fakeProc) {
			_ = os.Remove(filepath.Join(f.root, "999/net/udp"))
		}},
		"host netns unreadable": {DomainSocket, func(f *fakeProc) {
			_ = os.Remove(filepath.Join(f.root, "999/ns/net"))
		}},
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
			if tc.domain == DomainIdentity {
				if len(s.Gaps) == 0 {
					t.Fatalf("no gap at all")
				}
			} else {
				wantGap(t, s, tc.domain)
			}
			r := evalT([]Snapshot{s}, Policy{})
			for _, res := range r.Results {
				if res.Verdict == VerdictNoEffect {
					t.Errorf("%s: %s must not be no_effect with a gap", name, res.Predicate)
				}
			}
		})
	}
}

func TestCaptureUnreadableProcEntryWithoutPermission(t *testing.T) {
	skipIfRoot(t)
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
	// pid 102 (another identity) is listed as a member, but by the time it is read
	// it sits outside the cell and the registry never recorded it: it left
	// unobserved, and nothing else attributes it to the cell.
	f.addProcUID(2000, 102, 5100, "mover", "S", 1, "/system.slice/elsewhere")
	s = Capture(cellOf(100, 102), f.view(), params(PointInterval, ProcID{PID: 100, StartTicks: 5000}))
	wantGap(t, s, DomainRegistry)
	// The same mover with the cell identity is attributed by the identity scan
	// and is a PROC-02 finding rather than a gap.
	f.addProc(103, 5200, "mover", "S", 1, "/system.slice/elsewhere")
	s = Capture(cellOf(100, 103), f.view(), params(PointInterval, ProcID{PID: 100, StartTicks: 5000}))
	for _, g := range s.Gaps {
		if g.Domain == DomainRegistry && strings.Contains(g.Detail, "103") {
			t.Errorf("a cell-identity mover needs no registry entry: %+v", g)
		}
	}
}

func TestCaptureCellReadFailureIsAGap(t *testing.T) {
	f := newFakeProc(t)
	s := Capture(fakeCellReader{err: errors.New("fstatfs: not cgroup2")}, f.view(), params(PointFinalCleanup))
	wantGap(t, s, DomainCell)
	wantVerdicts(t, evalT([]Snapshot{s}, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestCaptureVanishedCellRequiresDrainProof(t *testing.T) {
	f := newFakeProc(t)
	removed := fakeCellReader{facts: CellFacts{Removed: true, Inode: testInode}}
	proof := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	s := Capture(removed, f.view(), params(PointFinalCleanup))
	if !s.CellRemoved || s.DrainProof != "" {
		t.Fatalf("snapshot: %+v", s)
	}
	wantVerdicts(t, evalT([]Snapshot{s}, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)

	p := params(PointFinalCleanup)
	p.DrainProof = proof
	s = Capture(removed, f.view(), p)
	if !s.CellRemoved || s.DrainProof != proof || s.Populated || len(s.Processes) != 0 {
		t.Errorf("snapshot: %+v", s)
	}
	if r := evalT([]Snapshot{s}, Policy{}); result(t, r, PredicateProc03).Verdict != VerdictNoEffect {
		t.Errorf("final cleanup with a drain proof: %+v", r.Results)
	}
}

func TestCaptureThenEvaluateDetectsASurvivingListener(t *testing.T) {
	f := newFakeProc(t)
	f.addProc(100, 5000, "nc", "S", 1, fakeCell)
	f.addSocketFD(100, 3, 5555)
	f.appendNet(100, "tcp", inetRow("0A", 8080, 1000, 5555))
	reg := ProcID{PID: 100, StartTicks: 5000}
	r := evalT(threePoints(f, cellOf(100), reg), Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictEffectPresent)
	if f := result(t, r, PredicateProc03).Findings; len(f) == 0 || f[0].Socket == nil || f[0].Socket.Local != "127.0.0.1:8080" {
		t.Errorf("findings: %+v", f)
	}
}

// Finding 1: an escaped double-fork daemon of the cell identity, outside the cell
// and with a TCP listener, is invisible to the cell and the registry. The identity
// scan finds it: PROC-01, PROC-02 and PROC-03 are effect_present although the
// registry is recorded and the cell is empty.
func TestCaptureEscapedDoubleForkDaemonWithEmptyCellAndRecordedRegistry(t *testing.T) {
	f := newFakeProc(t)
	f.addProc(500, 7000, "daemon", "S", 1, "/system.slice/elsewhere") // reparented to init: ppid 1
	f.addSocketFD(500, 3, 5555)
	f.appendNet(500, "tcp", inetRow("0A", 8080, 1000, 5555))
	snaps := threePoints(f, cellOf())
	for _, s := range snaps {
		if !s.Registry.Recorded || len(s.Registry.Procs) != 0 || s.Populated || len(s.Gaps) != 0 {
			t.Fatalf("fixture: %+v", s)
		}
	}
	r := evalT(snaps, Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictEffectPresent, VerdictEffectPresent)
	for _, id := range []string{PredicateProc01, PredicateProc02, PredicateProc03} {
		fs := result(t, r, id).Findings
		if len(fs) == 0 || fs[0].Process.PID != 500 || fs[0].CgroupPath != "/system.slice/elsewhere" {
			t.Errorf("%s findings: %+v", id, fs)
		}
	}
	if s := result(t, r, PredicateProc03).Findings[0].Socket; s == nil || s.Local != "127.0.0.1:8080" || s.UID != 1000 {
		t.Errorf("socket: %+v", s)
	}
}

func TestCaptureMatchesRealEffectiveAndSavedUID(t *testing.T) {
	for name, uids := range map[string][3]uint32{
		"real":      {1000, 2000, 2000},
		"effective": {2000, 1000, 2000},
		"saved":     {2000, 2000, 1000},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeProc(t)
			f.addProcUID(2000, 500, 7000, "sneaky", "S", 1, "/system.slice/elsewhere")
			f.write("500/status", fmt.Sprintf("Name:\tsneaky\nUid:\t%d\t%d\t%d\t2000\n", uids[0], uids[1], uids[2]))
			s := Capture(cellOf(), f.view(), params(PointInterval))
			if len(s.Processes) != 1 || s.Processes[0].PID != 500 || !s.Processes[0].CellIdentity || s.Processes[0].UID != uids {
				t.Errorf("processes: %+v gaps %v", s.Processes, gapDomains(s))
			}
		})
	}
	// Another uid, and the cell identity only as the filesystem uid, do not match.
	f := newFakeProc(t)
	f.addProcUID(2000, 500, 7000, "other", "S", 1, "/system.slice/elsewhere")
	f.write("500/status", "Name:\tother\nUid:\t2000\t2000\t2000\t1000\n")
	if s := Capture(cellOf(), f.view(), params(PointInterval)); len(s.Processes) != 0 {
		t.Errorf("a process of another identity was recorded: %+v", s.Processes)
	}
}

func TestCaptureIdentityZombieIsNotASurvivor(t *testing.T) {
	f := newFakeProc(t)
	f.addProcUID(fakeUID, 500, 7000, "gone", "Z", 1, "/system.slice/elsewhere")
	wantVerdicts(t, evalT(threePoints(f, cellOf()), Policy{}), VerdictNoEffect, VerdictEffectPresent, VerdictNoEffect)
}

func TestCaptureIdentitySocketsByUIDColumnAndByUnixInode(t *testing.T) {
	f := newFakeProc(t)
	// An inet socket with the cell uid that no visible process holds (its holder
	// is invisible to the fd scan): found in the host view by the uid column.
	f.appendNet(999, "tcp", inetRow("0A", 9001, 1000, 777))
	// A socket of another uid is not the cell's.
	f.appendNet(999, "tcp", inetRow("0A", 9002, 0, 778))
	// A named unix listener is found through the descriptor of an identity process.
	f.addProc(500, 7000, "daemon", "S", 1, "/system.slice/elsewhere")
	f.addSocketFD(500, 4, 8800)
	f.appendNet(500, "unix", "0000000000000000: 00000002 00000000 00010000 0001 01 8800 /tmp/evil.sock")
	// A named unix listener held by a process of another identity is not the cell's.
	f.addProcUID(2000, 501, 7100, "other", "S", 1, "/system.slice/elsewhere")
	f.addSocketFD(501, 4, 8801)
	f.appendNet(501, "unix", "0000000000000000: 00000002 00000000 00010000 0001 01 8801 /tmp/other.sock")

	r := evalT(threePoints(f, cellOf()), Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictEffectPresent, VerdictEffectPresent)
	got := map[string]bool{}
	for _, fnd := range result(t, r, PredicateProc03).Findings {
		got[fnd.Socket.Local] = true
	}
	if !got["127.0.0.1:9001"] || !got["/tmp/evil.sock"] || got["127.0.0.1:9002"] || got["/tmp/other.sock"] || len(got) != 2 {
		t.Errorf("PROC-03 sockets: %v", got)
	}
}

func TestCaptureIdentitySocketInAnotherNetworkNamespace(t *testing.T) {
	f := newFakeProc(t)
	f.addProc(500, 7000, "daemon", "S", 1, "/system.slice/elsewhere")
	f.setNetNS(500, "net:[4026539999]")
	// The row is in the private namespace only; the scanner's namespace has none.
	f.appendNet(500, "tcp", inetRow("0A", 9100, 1000, 901))
	r := evalT(threePoints(f, cellOf()), Policy{})
	found := false
	for _, fnd := range result(t, r, PredicateProc03).Findings {
		found = found || (fnd.Socket != nil && fnd.Socket.Local == "127.0.0.1:9100")
	}
	if !found {
		t.Errorf("a listener in a private network namespace was not found: %+v", result(t, r, PredicateProc03))
	}
}

func TestCaptureOtherIdentitySocketsAndProcessesAreClean(t *testing.T) {
	f := newFakeProc(t)
	f.addProcUID(2000, 600, 7000, "svc", "S", 1, "/system.slice/svc")
	f.addSocketFD(600, 3, 5555)
	f.appendNet(600, "tcp", inetRow("0A", 8080, 2000, 5555))
	wantVerdicts(t, evalT(threePoints(f, cellOf()), Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

// Hidepid and an unreadable /proc: the identity scan cannot be complete.
func TestCaptureHiddenOrUnreadableProcIsEvidenceLost(t *testing.T) {
	cases := map[string]struct {
		code   string
		mutate func(t *testing.T, f *fakeProc) HostView
	}{
		"hidepid=2: pid 1 is not listed": {"proc_hidden", func(t *testing.T, f *fakeProc) HostView {
			fakeProcWithout(f, 1)
			return f.view()
		}},
		"hidepid=1: pid 1 status is unreadable": {"proc_hidden", func(t *testing.T, f *fakeProc) HostView {
			skipIfRoot(t)
			chmodNone(t, filepath.Join(f.root, "1/status"))
			return f.view()
		}},
		"hidepid=ptraceable: another user's status is unreadable": {"status_unreadable", func(t *testing.T, f *fakeProc) HostView {
			skipIfRoot(t)
			f.addProcUID(2000, 300, 8000, "other", "S", 1, "/user.slice")
			chmodNone(t, filepath.Join(f.root, "300/status"))
			return f.view()
		}},
		"the scanner's own pid is not listed": {"proc_hidden", func(t *testing.T, f *fakeProc) HostView {
			fakeProcWithout(f, 999)
			return f.view()
		}},
		"/proc/self is not a link": {"proc_hidden", func(t *testing.T, f *fakeProc) HostView {
			if err := os.Remove(filepath.Join(f.root, "self")); err != nil {
				t.Fatal(err)
			}
			f.mkdir("self/ns")
			return f.view()
		}},
		"/proc cannot be listed": {"proc_unreadable", func(t *testing.T, f *fakeProc) HostView {
			v := f.view()
			v.ProcRoot = filepath.Join(f.root, "does-not-exist")
			return v
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeProc(t)
			view := tc.mutate(t, f)
			var snaps []Snapshot
			for i, pt := range []Point{PointInterval, PointPostTeardown, PointFinalCleanup} {
				snaps = append(snaps, Capture(cellOf(), view, withSeq(params(pt), i+1)))
			}
			wantGapCode(t, snaps[2], DomainIdentity, tc.code)
			wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
		})
	}
}

func chmodNone(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
}

func TestCaptureRegistrationWithoutAUsableIdentityDoesNotScan(t *testing.T) {
	f := newFakeProc(t)
	p := params(PointFinalCleanup)
	p.Registration.CellUID = 0
	s := Capture(cellOf(), f.view(), p)
	wantGapCode(t, s, DomainIdentity, "identity_scan_skipped")
	if s.IdentityScanned {
		t.Error("the scan must not claim to have run")
	}
}

func TestCaptureProcessThatVanishesDuringTheScanIsNotAGap(t *testing.T) {
	f := newFakeProc(t)
	// Listed in /proc, but its status is gone by the time it is read (it exited).
	f.addProc(500, 7000, "short", "S", 1, "/system.slice/elsewhere")
	if err := os.Remove(filepath.Join(f.root, "500/status")); err != nil {
		t.Fatal(err)
	}
	s := Capture(cellOf(), f.view(), params(PointInterval))
	if len(s.Gaps) != 0 {
		t.Errorf("an exited pid is not a gap: %v", gapDomains(s))
	}
}

// fakeMonitorCell counts reads and can stall one of them.
type fakeMonitorCell struct {
	mu      sync.Mutex
	n       int
	stallAt int
	stall   time.Duration
}

func (c *fakeMonitorCell) Observe() (CellFacts, error) {
	c.mu.Lock()
	c.n++
	n := c.n
	c.mu.Unlock()
	if n == c.stallAt {
		time.Sleep(c.stall)
	}
	return CellFacts{HierarchyPath: fakeCell, Inode: testInode}, nil
}

func monitorReg(maxGap time.Duration) Registration {
	r := testReg
	r.MaxGap = maxGap
	return r
}

func recordedRegistry() Registry { return Registry{Recorded: true} }

func TestMonitorRecordsSequencedTimedPollsAndStops(t *testing.T) {
	f := newFakeProc(t)
	reg := monitorReg(5 * time.Second)
	reg.IntervalStart = time.Now().Add(-time.Second)
	m, err := StartMonitor(&fakeMonitorCell{}, HostView{ProcRoot: f.root}, MonitorConfig{
		Registration: reg, Interval: 5 * time.Millisecond, Registry: recordedRegistry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Records()) != 1 {
		t.Fatalf("the first poll is taken synchronously, got %d records", len(m.Records()))
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(m.Records()) < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	recs := m.Stop()
	n := len(recs)
	if n < 5 {
		t.Fatalf("only %d polls", n)
	}
	for i, s := range recs {
		if s.Seq != i+1 || s.Point != PointMonitor || s.RegistrationDigest != reg.Digest() || !s.IdentityScanned || s.ScannedPIDs != 2 ||
			len(s.Gaps) != 0 || len(s.Sockets) != 0 {
			t.Fatalf("poll %d: %+v", i, s)
		}
		if i > 0 && s.ObservedAt.Before(recs[i-1].ObservedAt) {
			t.Fatalf("poll %d went back in time", i)
		}
	}
	time.Sleep(60 * time.Millisecond)
	if after := m.Records(); len(after) != n {
		t.Errorf("the monitor kept polling after Stop: %d -> %d", n, len(after))
	}
	if again := m.Stop(); len(again) != n { // idempotent
		t.Errorf("second Stop: %d records", len(again))
	}
}

func TestMonitorRejectsAnIntervalThatCannotMeetMaxGap(t *testing.T) {
	f := newFakeProc(t)
	for name, cfg := range map[string]MonitorConfig{
		"no interval":        {Registration: monitorReg(time.Second), Registry: recordedRegistry},
		"interval >= MaxGap": {Registration: monitorReg(time.Second), Interval: time.Second, Registry: recordedRegistry},
		"no MaxGap":          {Registration: monitorReg(0), Interval: time.Millisecond, Registry: recordedRegistry},
		"no registry source": {Registration: monitorReg(time.Second), Interval: time.Millisecond},
	} {
		if m, err := StartMonitor(&fakeMonitorCell{}, HostView{ProcRoot: f.root}, cfg); err == nil {
			m.Stop()
			t.Errorf("%s: accepted", name)
		}
	}
}

// monitored runs a monitor over a fake host until the stall (if any) has passed
// and returns it stopped. The interval start precedes the first poll.
func monitored(t *testing.T, f *fakeProc, cell CellReader, reg Registration, interval time.Duration, polls int) []Snapshot {
	t.Helper()
	m, err := StartMonitor(cell, HostView{ProcRoot: f.root}, MonitorConfig{Registration: reg, Interval: interval, Registry: recordedRegistry})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(m.Records()) < polls && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	return m.Stop()
}

// postAfter is the post-teardown and final snapshots of an empty fake cell taken
// now, under reg, so that the coverage rule has its end point.
func finalSnaps(f *fakeProc, reg Registration) []Snapshot {
	view := HostView{ProcRoot: f.root}
	mk := func(pt Point, seq int) Snapshot {
		return Capture(cellOf(), view, CaptureParams{Point: pt, Seq: seq, Registration: reg, Registry: recordedRegistry()})
	}
	return []Snapshot{mk(PointInterval, 1), mk(PointPostTeardown, 2), mk(PointFinalCleanup, 3)}
}

func TestMonitorGapGreaterThanMaxGapIsEvidenceLost(t *testing.T) {
	f := newFakeProc(t)
	reg := monitorReg(80 * time.Millisecond)
	reg.IntervalStart = time.Now()
	reg.Deadline = reg.IntervalStart // the observation below is after it
	// The third read stalls for 300 ms, far more than MaxGap.
	cell := &fakeMonitorCell{stallAt: 3, stall: 300 * time.Millisecond}
	polls := monitored(t, f, cell, reg, 10*time.Millisecond, 8)
	r := Evaluate(reg, finalSnaps(f, reg), polls, Policy{})
	res := result(t, r, PredicateProc02)
	if res.Verdict != VerdictEvidenceLost || !hasGap(res, DomainCoverage, "coverage_gap") {
		t.Errorf("PROC-02 = %s %+v", res.Verdict, res.Gaps)
	}
}

func TestMonitorContinuousRecordGivesNoEffectAndSeesAnEscape(t *testing.T) {
	f := newFakeProc(t)
	reg := monitorReg(2 * time.Second)
	reg.IntervalStart = time.Now()
	reg.Deadline = reg.IntervalStart
	polls := monitored(t, f, &fakeMonitorCell{}, reg, 5*time.Millisecond, 6)
	r := Evaluate(reg, finalSnaps(f, reg), polls, Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)

	// An identity process outside the cell during the polls is a monitor finding
	// even when the cell and the registry never mention it.
	g := newFakeProc(t)
	g.addProc(500, 7000, "esc", "S", 1, "/system.slice/elsewhere")
	reg2 := reg
	reg2.IntervalStart = time.Now()
	reg2.Deadline = reg2.IntervalStart
	polls = monitored(t, g, &fakeMonitorCell{}, reg2, 5*time.Millisecond, 3)
	snaps := finalSnaps(g, reg2)
	res := result(t, Evaluate(reg2, snaps, polls, Policy{}), PredicateProc02)
	if res.Verdict != VerdictEffectPresent {
		t.Fatalf("PROC-02 = %s", res.Verdict)
	}
	sawPoll := false
	for _, fnd := range res.Findings {
		sawPoll = sawPoll || (fnd.Point == PointMonitor && fnd.Process.PID == 500)
	}
	if !sawPoll {
		t.Errorf("no finding from a poll: %+v", res.Findings)
	}
}

// Capture is bound to the registered cell: another cgroup than the registered
// one (path or inode) is refused, and the empty other cell does not read as a
// clean teardown of the registered one.
func TestCaptureRefusesACellOtherThanRegistered(t *testing.T) {
	for name, facts := range map[string]CellFacts{
		"other inode":        {HierarchyPath: fakeCell, Inode: testInode + 1},
		"other path":         {HierarchyPath: fakeCell + "-b", Inode: testInode},
		"other cell":         {HierarchyPath: fakeCell + "-b", Inode: testInode + 1},
		"no inode":           {HierarchyPath: fakeCell},
		"removed other cell": {Removed: true, Inode: testInode + 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeProc(t)
			f.addProc(100, 5000, "pi", "S", 1, fakeCell)
			s := Capture(fakeCellReader{facts: facts}, f.view(), params(PointFinalCleanup, ProcID{PID: 100, StartTicks: 5000}))
			if !slices.Contains(gapDomains(s), DomainCell+"/cell_identity_mismatch") || len(s.Processes) != 0 {
				t.Fatalf("gaps %v, processes %d", gapDomains(s), len(s.Processes))
			}
			snaps := []Snapshot{withSeq2(s, 1, PointInterval), withSeq2(s, 2, PointPostTeardown), withSeq2(s, 3, PointFinalCleanup)}
			wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
		})
	}
	// The registered cell itself captures without a gap.
	f := newFakeProc(t)
	if s := Capture(cellOf(), f.view(), params(PointFinalCleanup)); len(s.Gaps) != 0 || s.CellInode != testInode {
		t.Fatalf("gaps %v inode %d", gapDomains(s), s.CellInode)
	}
}

func withSeq2(s Snapshot, seq int, pt Point) Snapshot {
	s.Seq, s.Point = seq, pt
	s.ObservedAt = t0.Add(time.Duration(seq) * time.Second)
	return s
}
