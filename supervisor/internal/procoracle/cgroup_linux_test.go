//go:build linux

package procoracle

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/piruntime"
)

// TestMain doubles as the listener helper process: the test binary re-executes
// itself, opens a TCP and a Unix listener, reports the TCP port and its pid and
// sleeps.
func TestMain(m *testing.M) {
	if os.Getenv("PROCORACLE_HELPER") == "listen" {
		runListenHelper()
		return
	}
	os.Exit(m.Run())
}

func runListenHelper() {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	unix, err := net.Listen("unix", os.Getenv("PROCORACLE_SOCK"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("ready %d %d\n", tcp.Addr().(*net.TCPAddr).Port, os.Getpid())
	time.Sleep(5 * time.Minute)
	runtime.KeepAlive(tcp)
	runtime.KeepAlive(unix)
}

// unusedUID is a cell identity no process on the machine has. The tests that
// check clean teardowns on a real /proc use it, because the real identity scan
// would otherwise see every process of the test user; the registry and the cell
// members still drive those tests. The test of the escaped daemon uses the real
// uid of the test user instead.
const unusedUID = 4000999

// realEnv is a private pair of cgroups, cell and outer, below the cgroup the
// test process runs in. Creating them and migrating processes into them needs a
// cgroup the user owns (systemd Delegate=yes); without one the tests skip.
type realEnv struct {
	t        *testing.T
	work     string
	cellDir  string
	outerDir string
	cell     *os.File
	cmds     []*exec.Cmd
	pids     []int
	reg      Registration
	mon      *Monitor
}

func cgroup2Mount(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Skipf("no mountinfo: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields := strings.Fields(before)
		if ok && len(fields) >= 5 && strings.HasPrefix(after, "cgroup2 ") && fields[3] == "/" {
			return fields[4]
		}
	}
	t.Skip("no cgroup2 mount with root / in this mount namespace")
	return ""
}

func ownCgroupPath(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skipf("no /proc/self/cgroup: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return p
		}
	}
	t.Skip("process has no cgroup2 membership")
	return ""
}

func newRealEnv(t *testing.T) *realEnv {
	t.Helper()
	base := filepath.Join(cgroup2Mount(t), ownCgroupPath(t))
	work, err := os.MkdirTemp(base, "procoracle-")
	if err != nil {
		t.Skipf("cannot create a cgroup below %s (%v): the cgroup is not delegated to this user "+
			"(run under `systemd-run --user --scope -p Delegate=yes`)", base, err)
	}
	e := &realEnv{t: t, work: work, cellDir: filepath.Join(work, "cell"), outerDir: filepath.Join(work, "outer")}
	t.Cleanup(e.cleanup)
	for _, d := range []string{e.cellDir, e.outerDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Skipf("cannot create a child cgroup (%v)", err)
		}
	}
	e.cell, err = os.Open(e.cellDir)
	if err != nil {
		t.Fatal(err)
	}
	// The registration is made before anything runs, as the launcher does: the
	// interval starts now, teardown is due 400 ms later (tests wait for it before
	// the post-teardown capture) and polls may be up to 5 s apart.
	now := time.Now()
	e.reg = Registration{
		CellID: filepath.Base(work), IntervalStart: now, Deadline: now.Add(400 * time.Millisecond),
		CellUID: unusedUID, CellGID: unusedUID, MaxGap: 5 * time.Second,
	}
	return e
}

func (e *realEnv) killAll() {
	for _, d := range []string{e.cellDir, e.outerDir} {
		data, _ := os.ReadFile(filepath.Join(d, "cgroup.procs"))
		for _, f := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(f); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
	for _, pid := range e.pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func (e *realEnv) cleanup() {
	if e.mon != nil {
		e.mon.Stop()
	}
	e.killAll()
	for _, c := range e.cmds {
		_ = c.Wait()
	}
	if e.cell != nil {
		_ = e.cell.Close()
	}
	for i := 0; i < 50; i++ {
		ok := true
		for _, d := range []string{e.cellDir, e.outerDir, e.work} {
			if err := os.Remove(d); err != nil && !os.IsNotExist(err) {
				ok = false
			}
		}
		if ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startMonitor starts the continuous-coverage witness over the held cell.
func (e *realEnv) startMonitor(registry func() Registry) {
	e.t.Helper()
	m, err := StartMonitor(HeldCell(e.cell), HostProc(), MonitorConfig{
		Registration: e.reg, Interval: 25 * time.Millisecond, Registry: registry,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.mon = m
}

// waitDeadline sleeps until the registered teardown deadline has passed.
func (e *realEnv) waitDeadline() {
	if d := time.Until(e.reg.Deadline); d > 0 {
		time.Sleep(d + 10*time.Millisecond)
	}
}

// polls stops the monitor and returns its record.
func (e *realEnv) polls() []Snapshot {
	e.t.Helper()
	if e.mon == nil {
		e.t.Fatal("no monitor")
	}
	return e.mon.Stop()
}

func (e *realEnv) evaluate(snaps []Snapshot, policy Policy) Report {
	e.t.Helper()
	return Evaluate(e.reg, snaps, e.polls(), policy)
}

// move writes pid into dir's cgroup.procs, skipping the test if the host
// refuses the migration.
func (e *realEnv) move(pid int, dir string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0); err != nil {
		e.t.Skipf("cannot migrate process %d into %s (%v): the user may not move processes between "+
			"these cgroups (needs a systemd Delegate=yes scope)", pid, dir, err)
	}
}

func (e *realEnv) spawn(into string, name string, args ...string) *exec.Cmd {
	e.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.cmds = append(e.cmds, cmd)
	e.move(cmd.Process.Pid, into)
	return cmd
}

func idOf(t *testing.T, pid int) ProcID {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	st, err := parseStat(data)
	if err != nil {
		t.Fatal(err)
	}
	return ProcID{PID: pid, StartTicks: st.start}
}

func (e *realEnv) hierarchy(dir string) string {
	e.t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	facts, err := piruntime.ObserveCellCgroup(f)
	if err != nil {
		e.t.Fatal(err)
	}
	return facts.HierarchyPath
}

func (e *realEnv) capture(pt Point, seq int, reg ...ProcID) Snapshot {
	e.t.Helper()
	return Capture(HeldCell(e.cell), HostProc(), CaptureParams{
		Point: pt, Seq: seq, Registration: e.reg, Registry: Registry{Recorded: true, Procs: reg},
	})
}

// tearDown stops everything in the cell and waits until cgroup.events says
// populated 0, the way the launcher does.
func (e *realEnv) tearDown() {
	e.t.Helper()
	e.killAll()
	for _, c := range e.cmds {
		_ = c.Wait()
	}
	e.cmds = nil
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		facts, err := piruntime.ObserveCellCgroup(e.cell)
		if err == nil && !facts.Populated {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("cell did not reach populated 0")
}

func assertNoGaps(t *testing.T, snaps ...Snapshot) {
	t.Helper()
	for _, s := range snaps {
		if len(s.Gaps) != 0 {
			t.Fatalf("snapshot %d (%s) has gaps: %+v", s.Seq, s.Point, s.Gaps)
		}
	}
}

func noRegistry() Registry { return Registry{Recorded: true} }

func TestRealCleanTeardownIsNoEffect(t *testing.T) {
	e := newRealEnv(t)
	sleeper := e.spawn(e.cellDir, "sleep", "300")
	id := idOf(t, sleeper.Process.Pid)
	e.startMonitor(func() Registry { return Registry{Recorded: true, Procs: []ProcID{id}} })

	running := e.capture(PointInterval, 1, id)
	assertNoGaps(t, running)
	if !running.Populated || len(running.Processes) != 1 || running.Processes[0].Comm != "sleep" ||
		!running.Processes[0].InCell || !running.Processes[0].Registered || running.Processes[0].ID() != id {
		t.Fatalf("running cell not observed: %+v", running)
	}
	if want := e.hierarchy(e.cellDir); running.CellPath != want || running.Processes[0].CgroupPath != want {
		t.Fatalf("cell path %q / process cgroup %q, want %q", running.CellPath, running.Processes[0].CgroupPath, want)
	}
	if !running.IdentityScanned || running.ScannedPIDs < 2 {
		t.Fatalf("the identity scan did not run over the real /proc: %+v", running)
	}

	e.tearDown()
	e.waitDeadline()
	post := e.capture(PointPostTeardown, 2, id)
	final := e.capture(PointFinalCleanup, 3, id)
	assertNoGaps(t, post, final)
	if post.Populated || len(post.Processes) != 0 {
		t.Fatalf("torn-down cell still shows processes: %+v", post)
	}
	polls := e.polls()
	assertNoGaps(t, polls...)
	r := Evaluate(e.reg, []Snapshot{running, post, final}, polls, Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

func TestRealDetachedSleepSurvivingIsProc01(t *testing.T) {
	e := newRealEnv(t)
	sleeper := e.spawn(e.cellDir, "sleep", "300") // setsid: detached from the test's session
	id := idOf(t, sleeper.Process.Pid)
	e.startMonitor(func() Registry { return Registry{Recorded: true, Procs: []ProcID{id}} })

	running := e.capture(PointInterval, 1, id)
	e.waitDeadline()
	post := e.capture(PointPostTeardown, 2, id)
	final := e.capture(PointFinalCleanup, 3, id)
	assertNoGaps(t, running, post, final)

	r := e.evaluate([]Snapshot{running, post, final}, Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictNoEffect)
	f := result(t, r, PredicateProc01).Findings
	if len(f) != 2 || f[0].Process != id || f[0].Comm != "sleep" || f[0].CgroupPath != e.hierarchy(e.cellDir) ||
		!f[0].Deadline.Equal(e.reg.Deadline.UTC()) || f[0].ObservedAt.Before(e.reg.Deadline) {
		t.Errorf("survival finding: %+v", f)
	}
}

func TestRealReparentedSurvivorIsProc01(t *testing.T) {
	e := newRealEnv(t)
	shell := exec.Command("sh", "-c", "sleep 120 >/dev/null 2>&1 </dev/null & echo $!")
	out, err := shell.Output() // the shell exits, so the sleep is reparented away from it
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	e.pids = append(e.pids, pid)
	e.move(pid, e.cellDir)
	id := idOf(t, pid)
	e.startMonitor(func() Registry { return Registry{Recorded: true, Procs: []ProcID{id}} })

	running := e.capture(PointInterval, 1, id)
	e.waitDeadline()
	post := e.capture(PointPostTeardown, 2, id)
	final := e.capture(PointFinalCleanup, 3, id)
	assertNoGaps(t, running, post, final)
	if len(post.Processes) != 1 || post.Processes[0].PPID == os.Getpid() || post.Processes[0].PPID == shell.Process.Pid {
		t.Fatalf("the sleep was not reparented: %+v", post.Processes)
	}
	r := e.evaluate([]Snapshot{running, post, final}, Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictNoEffect)
}

func TestRealProcessMovedOutOfTheCellIsProc02(t *testing.T) {
	e := newRealEnv(t)
	sleeper := e.spawn(e.cellDir, "sleep", "300")
	id := idOf(t, sleeper.Process.Pid)
	e.startMonitor(func() Registry { return Registry{Recorded: true, Procs: []ProcID{id}} })

	before := e.capture(PointInterval, 1, id)
	e.move(sleeper.Process.Pid, e.outerDir) // the real boundary crossing
	after := e.capture(PointInterval, 2, id)
	e.waitDeadline()
	post := e.capture(PointPostTeardown, 3, id)
	final := e.capture(PointFinalCleanup, 4, id)
	assertNoGaps(t, before, after, post, final)

	if len(after.Processes) != 1 || after.Processes[0].InCell || !after.Processes[0].Registered {
		t.Fatalf("the moved process must be found outside the cell through the registry: %+v", after.Processes)
	}
	if post.Populated {
		t.Fatal("the cell should be empty once its only process left it")
	}
	r := e.evaluate([]Snapshot{before, after, post, final}, Policy{})
	// It also survives teardown, which is EFF-PROC-01 in its own right.
	wantVerdicts(t, r, VerdictEffectPresent, VerdictEffectPresent, VerdictNoEffect)
	f := result(t, r, PredicateProc02).Findings
	if len(f) == 0 || f[0].Process != id || f[0].CellPath != e.hierarchy(e.cellDir) || f[0].CgroupPath != e.hierarchy(e.outerDir) {
		t.Errorf("crossing finding: %+v", f)
	}

	// Without the registry (and without the cell identity) the oracle cannot see
	// the departure by registry and says so.
	blind := Capture(HeldCell(e.cell), HostProc(), CaptureParams{Point: PointPostTeardown, Seq: 3, Registration: e.reg})
	wantGap(t, blind, DomainRegistry)
}

// startListener runs the listener helper (it opens a TCP and a Unix listener) and
// returns its command, TCP port and pid. detached starts it through a subshell
// that exits (double fork, reparented away) and in its own session.
func startListener(t *testing.T, sock string, detached bool) (cmd *exec.Cmd, port, pid int) {
	t.Helper()
	env := append(os.Environ(), "PROCORACLE_HELPER=listen", "PROCORACLE_SOCK="+sock)
	if !detached {
		cmd = exec.Command(os.Args[0])
		cmd.Env = env
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			t.Fatalf("helper did not start: %v", err)
		}
		return cmd, parseReady(t, line, &pid), pid
	}
	out := filepath.Join(t.TempDir(), "ready")
	sh := exec.Command("sh", "-c", `(setsid "$0" >"$1" 2>&1 </dev/null &)`, os.Args[0], out)
	sh.Env = env
	if err := sh.Run(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(out); err == nil && strings.HasSuffix(string(data), "\n") {
			return nil, parseReady(t, string(data), &pid), pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the detached helper did not report readiness")
	return nil, 0, 0
}

func parseReady(t *testing.T, line string, pid *int) int {
	t.Helper()
	f := strings.Fields(line)
	if len(f) != 3 || f[0] != "ready" {
		t.Fatalf("helper said %q", line)
	}
	port, err1 := strconv.Atoi(f[1])
	p, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil {
		t.Fatalf("helper said %q", line)
	}
	*pid = p
	return port
}

func TestRealResidualListenerIsProc03(t *testing.T) {
	e := newRealEnv(t)
	sock := filepath.Join(t.TempDir(), "s")
	cmd, port, pid := startListener(t, sock, false)
	e.cmds = append(e.cmds, cmd)
	e.move(pid, e.cellDir)
	id := idOf(t, pid)
	e.startMonitor(func() Registry { return Registry{Recorded: true, Procs: []ProcID{id}} })

	running := e.capture(PointInterval, 1, id)
	e.waitDeadline()
	post := e.capture(PointPostTeardown, 2, id)
	final := e.capture(PointFinalCleanup, 3, id)
	assertNoGaps(t, running, post, final)
	polls := e.polls()

	r := Evaluate(e.reg, []Snapshot{running, post, final}, polls, Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictEffectPresent)
	var tcp, unix bool
	for _, f := range result(t, r, PredicateProc03).Findings {
		if f.Socket == nil || f.Process != id || f.Point != PointFinalCleanup {
			t.Errorf("finding: %+v", f)
			continue
		}
		tcp = tcp || (f.Socket.Proto == "tcp" && f.Socket.Local == fmt.Sprintf("127.0.0.1:%d", port) && f.Socket.State == "LISTEN")
		unix = unix || (f.Socket.Proto == "unix" && f.Socket.Local == sock && f.Socket.Role == RoleListener)
	}
	if !tcp || !unix {
		t.Errorf("tcp listener found=%v, unix listener found=%v: %+v", tcp, unix, result(t, r, PredicateProc03).Findings)
	}

	// The same sockets are authorized by policy: PROC-03 clears, PROC-01 still holds.
	r = Evaluate(e.reg, []Snapshot{running, post, final}, polls, Policy{AuthorizedSockets: []string{
		fmt.Sprintf("tcp 127.0.0.1:%d", port), "unix " + sock,
	}})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictNoEffect)
}

// Finding 1 on real processes: a double-fork daemon of the test user, started
// outside the cell and never registered, with a TCP listener. The cell is empty
// and the registry is recorded. The cell identity is the test user's own uid, so
// the scan sees every process of the user: the assertions pick the daemon out of
// the findings.
func TestRealEscapedDoubleForkDaemonWithEmptyCellIsProc01Proc02Proc03(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 || gid == 0 {
		t.Skip("running as root: the cell identity cannot be root")
	}
	e := newRealEnv(t)
	e.reg.CellUID, e.reg.CellGID = uint32(uid), uint32(gid)
	sock := filepath.Join(t.TempDir(), "s")
	_, port, pid := startListener(t, sock, true)
	e.pids = append(e.pids, pid)
	id := idOf(t, pid)
	if own := e.hierarchy(e.cellDir); own == ownCgroupPath(t) {
		t.Fatal("the cell must be a different cgroup from the test's")
	}
	e.startMonitor(noRegistry)

	running := e.capture(PointInterval, 1)
	e.waitDeadline()
	post := e.capture(PointPostTeardown, 2)
	final := e.capture(PointFinalCleanup, 3)
	if running.Populated || len(running.Processes) == 0 {
		t.Fatalf("the cell must be empty and the identity scan must find processes: %+v", running)
	}
	polls := e.polls()
	r := Evaluate(e.reg, []Snapshot{running, post, final}, polls, Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictEffectPresent, VerdictEffectPresent)
	for _, pred := range []string{PredicateProc01, PredicateProc02} {
		found := false
		for _, f := range result(t, r, pred).Findings {
			found = found || (f.Process == id && f.Comm != "")
		}
		if !found {
			t.Errorf("%s: the daemon %v is not among the findings", pred, id)
		}
	}
	var tcp bool
	for _, f := range result(t, r, PredicateProc03).Findings {
		tcp = tcp || (f.Socket != nil && f.Socket.Proto == "tcp" && f.Socket.Local == fmt.Sprintf("127.0.0.1:%d", port) && f.Process == id)
	}
	if !tcp {
		t.Errorf("PROC-03: the daemon's listener 127.0.0.1:%d is not among the findings", port)
	}
	// The monitor saw it too.
	seen := false
	for _, p := range polls {
		for _, pr := range p.Processes {
			seen = seen || (pr.ID() == id && !pr.InCell && pr.CellIdentity)
		}
	}
	if !seen {
		t.Error("no poll recorded the escaped daemon")
	}
}

func TestRealVanishedCellNeedsADrainProof(t *testing.T) {
	e := newRealEnv(t)
	sleeper := e.spawn(e.cellDir, "sleep", "300")
	id := idOf(t, sleeper.Process.Pid)
	e.startMonitor(func() Registry { return Registry{Recorded: true, Procs: []ProcID{id}} })
	running := e.capture(PointInterval, 1, id)
	e.tearDown()
	if err := os.Remove(e.cellDir); err != nil {
		t.Fatal(err)
	}
	e.waitDeadline()
	capture := func(pt Point, seq int, proof string) Snapshot {
		return Capture(HeldCell(e.cell), HostProc(), CaptureParams{
			Point: pt, Seq: seq, Registration: e.reg, Registry: Registry{Recorded: true, Procs: []ProcID{id}}, DrainProof: proof,
		})
	}
	post, final := capture(PointPostTeardown, 2, ""), capture(PointFinalCleanup, 3, "")
	if !post.CellRemoved || post.Populated || len(post.Processes) != 0 {
		t.Fatalf("a removed cell must be recognised: %+v", post)
	}
	polls := e.polls()
	// The cell is gone: nothing proves it drained, so nothing is no_effect.
	wantVerdicts(t, Evaluate(e.reg, []Snapshot{running, post, final}, polls, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)

	proof := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	post, final = capture(PointPostTeardown, 2, proof), capture(PointFinalCleanup, 3, proof)
	assertNoGaps(t, post, final)
	wantVerdicts(t, Evaluate(e.reg, []Snapshot{running, post, final}, polls, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

func TestHeldDescriptorThatIsNotACgroupIsEvidenceLost(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	reg := testReg
	reg.CellUID, reg.CellGID = unusedUID, unusedUID
	s := Capture(HeldCell(dir), HostProc(), CaptureParams{
		Point: PointFinalCleanup, Seq: 1, Registration: reg, Registry: Registry{Recorded: true},
	})
	wantGap(t, s, DomainCell)
	wantVerdicts(t, Evaluate(reg, []Snapshot{s}, nil, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

// TestCaptureSeesThisProcessThroughTheRealProc needs no privilege: it holds the
// cgroup this very process runs in and checks that the oracle finds the test
// process there with the identity /proc reports, and that the identity scan of the
// real /proc finds it by uid.
func TestCaptureSeesThisProcessThroughTheRealProc(t *testing.T) {
	dir, err := os.Open(filepath.Join(cgroup2Mount(t), ownCgroupPath(t)))
	if err != nil {
		t.Skipf("cannot open the own cgroup: %v", err)
	}
	defer dir.Close()
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 || gid == 0 {
		t.Skip("running as root: the cell identity cannot be root")
	}
	self := idOf(t, os.Getpid())
	reg := testReg
	reg.CellUID, reg.CellGID = uint32(uid), uint32(gid)
	s := Capture(HeldCell(dir), HostProc(), CaptureParams{
		Point: PointInterval, Seq: 1, Registration: reg, Registry: Registry{Recorded: true, Procs: []ProcID{self}},
	})
	if s.CellPath != ownCgroupPath(t) || !s.Populated || !s.IdentityScanned || s.ScannedPIDs < 2 {
		t.Fatalf("cell: %q populated=%v scan=%v/%d", s.CellPath, s.Populated, s.IdentityScanned, s.ScannedPIDs)
	}
	for _, p := range s.Processes {
		if p.ID() == self {
			if !p.InCell || !p.Registered || !p.CellIdentity || p.CgroupPath != s.CellPath || p.State == "" || p.Comm == "" ||
				p.UID[0] != uint32(uid) {
				t.Errorf("self: %+v", p)
			}
			return
		}
	}
	t.Errorf("this process is not in the snapshot: %+v (gaps %+v)", s.Processes, s.Gaps)
}
