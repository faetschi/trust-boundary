//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/cgroupguard"
	"tbound/supervisor/internal/trialfix"
)

// Tests of the process cleanup of `tbound trial reset`: cgroup.kill on the cells cgroup and the
// wait for populated 0.
//
// The kernel semantics are tested for real when TBOUND_REAL_CGROUP_TESTS=1 and the test runs
// in a cgroup2 subtree it may write (a systemd user scope with Delegate=yes, for example: systemd-run --user --scope -p
// Delegate=yes go test ...); otherwise that test skips and says so. The test refuses a base
// cgroup that is / or holds init.scope or session-*, and caps pids (512) and memory (2G) on
// the cgroup it creates before any churn (internal/cgroupguard). The churn processes are
// started inside the cgroup (clone3 with CLONE_INTO_CGROUP) and keep forking and exiting.

const testRoleEnv = "TBOUND_TEST_ROLE"

// forkerMaxChildren bounds the live children of one forker role.
const forkerMaxChildren = 16

// init turns this test binary into a churn process when it is re-executed with testRoleEnv.
// Every role is bounded by a watchdog. Children are started from /proc/self/exe and inherit
// the cgroup of their parent.
func init() {
	role := os.Getenv(testRoleEnv)
	if role == "" {
		return
	}
	time.AfterFunc(2*time.Minute, func() { os.Exit(0) })
	switch role {
	case "chain":
		// Forks its successor and exits at once: the daisy chain of the reviewer's race.
		_ = startRole("chain")
		os.Exit(0)
	case "forker":
		// Forks sleeping children without pause. At most forkerMaxChildren are alive at once:
		// past that the oldest is killed before the next fork, so the churn goes on with a
		// fixed number of processes.
		var live []*exec.Cmd
		for {
			if len(live) >= forkerMaxChildren {
				_ = live[0].Process.Kill()
				go func(old *exec.Cmd) { _ = old.Wait() }(live[0])
				live = live[1:]
			}
			if cmd := startRole("sleeper"); cmd != nil {
				live = append(live, cmd)
			}
			time.Sleep(time.Millisecond)
		}
	case "sleeper":
		select {}
	case "threads":
		// Thread churn: a goroutine that ends while locked to its thread ends the thread.
		for {
			go func() { runtime.LockOSThread() }()
			time.Sleep(50 * time.Microsecond)
		}
	case "zombie":
		// Package init runs on the main OS thread (the tid that is the tgid), so the raw
		// SYS_exit below ends exactly the leader. The worker thread keeps the process alive.
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			close(ready)
			select {}
		}()
		<-ready
		runtime.LockOSThread()
		// syscall.Syscall (not RawSyscall) so the runtime hands the P of the dying thread over.
		syscall.Syscall(syscall.SYS_EXIT, 0, 0, 0)
		os.Exit(3) // not reached
	}
	os.Exit(4)
}

func roleCmd(role string) *exec.Cmd {
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = []string{testRoleEnv + "=" + role}
	return cmd
}

func startRole(role string) *exec.Cmd {
	cmd := roleCmd(role)
	if cmd.Start() != nil {
		return nil
	}
	return cmd
}

// cgroup2MountPoint finds the cgroup2 mount in the content of /proc/self/mountinfo.
func cgroup2MountPoint(mountinfo string) string {
	for _, line := range strings.Split(mountinfo, "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok || !strings.HasPrefix(after, "cgroup2 ") {
			continue
		}
		if fields := strings.Fields(before); len(fields) > 4 {
			return fields[4]
		}
	}
	return ""
}

// cgroupProcs lists the pids in dir and every cgroup below it.
func cgroupProcs(t *testing.T, dir string) []string {
	t.Helper()
	var pids []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		data, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
		if err != nil {
			return err
		}
		pids = append(pids, strings.Fields(string(data))...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return pids
}

// testCgroup is a fresh child cgroup of the test's own cgroup, or the test skips.
type testCgroup struct {
	path string
	dir  *os.File
}

func newTestCgroup(t *testing.T) testCgroup {
	t.Helper()
	if !cgroupguard.Enabled() {
		t.Skip("set " + cgroupguard.Env + "=1 to run the real-cgroup churn tests (in a delegated scope, not a login session)")
	}
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	mount := cgroup2MountPoint(string(mountinfo))
	if mount == "" {
		t.Skip("no cgroup2 mount: the real cgroup.kill test cannot run")
	}
	own, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	var rel string
	for _, line := range strings.Split(string(own), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			rel = p
		}
	}
	if rel == "" {
		t.Skip("the test process has no cgroup2 membership: the real cgroup.kill test cannot run")
	}
	if why := cgroupguard.RefuseBase(rel); why != "" {
		t.Fatalf("%s: refusing to churn processes below %q; run under systemd-run --user --scope -p Delegate=yes", why, rel)
	}
	path := filepath.Join(mount, rel, fmt.Sprintf("tbound-trial-test-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Skipf("no writable delegated cgroup2 subtree (%v): run under systemd-run --user --scope -p Delegate=yes", err)
	}
	t.Cleanup(func() {
		var dirs []string
		_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				dirs = append(dirs, p)
			}
			return nil
		})
		slices.Reverse(dirs)
		for _, d := range dirs {
			_ = os.WriteFile(filepath.Join(d, "cgroup.kill"), []byte("1"), 0o644)
		}
		time.Sleep(100 * time.Millisecond)
		for _, d := range dirs {
			_ = os.Remove(d)
		}
	})
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dir.Close() })
	if err := cgroupguard.ApplyLimits(path, filepath.Join(mount, rel)); err != nil {
		t.Fatalf("refusing to churn processes in an uncapped cgroup: %v", err)
	}
	if err := writeCgroupFile(dir, "cgroup.kill", ""); err != nil && !errors.Is(err, syscall.EINVAL) {
		t.Skipf("cgroup.kill is not writable in the test cgroup: %v", err)
	}
	return testCgroup{path: path, dir: dir}
}

// inCgroup starts cmd directly in the cgroup dir (no window in which it runs outside).
func inCgroup(cmd *exec.Cmd, dir *os.File) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(dir.Fd())}
	return cmd
}

// churnRoles is what runs when the kill comes: four chains of forks that exit, two forkers and
// two thread churners.
var churnRoles = []string{"chain", "chain", "chain", "chain", "forker", "forker", "threads", "threads"}

func TestKillCgroupEndsForkAndThreadChurnInARealCgroup(t *testing.T) {
	cg := newTestCgroup(t)
	sub := filepath.Join(cg.path, "below")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	subDir, err := os.Open(sub)
	if err != nil {
		t.Fatal(err)
	}
	defer subDir.Close()

	// A bystander in the test's own cgroup, outside the one that is killed.
	bystander := roleCmd("sleeper")
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bystander.Process.Kill(); _ = bystander.Wait() })

	const rounds = 8
	for round := 0; round < rounds; round++ {
		for _, role := range churnRoles {
			cmd := inCgroup(roleCmd(role), cg.dir)
			if err := cmd.Start(); err != nil {
				t.Fatalf("start %s in the cgroup: %v", role, err)
			}
			go func() { _ = cmd.Wait() }()
		}
		for i := 0; i < 2; i++ { // processes in a cgroup below the killed one
			cmd := inCgroup(roleCmd("forker"), subDir)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			go func() { _ = cmd.Wait() }()
		}
		time.Sleep(300 * time.Millisecond)
		if populated, err := cgroupPopulated(cg.dir); err != nil || !populated {
			t.Fatalf("round %d before the kill: populated %v, err %v: the churn did not start", round, populated, err)
		}
		if err := killCgroup(cg.dir, 5*time.Second); err != nil {
			dumpCgroup(t, cg.path)
			t.Fatalf("round %d: killCgroup: %v", round, err)
		}
		// Quiet and stays quiet: nothing was forked or moved in after the kill returned.
		time.Sleep(200 * time.Millisecond)
		if pids := cgroupProcs(t, cg.path); len(pids) != 0 {
			t.Fatalf("round %d: processes %v in the cgroup subtree after a successful kill", round, pids)
		}
		if populated, err := cgroupPopulated(cg.dir); err != nil || populated {
			t.Fatalf("round %d after the kill: populated %v, err %v", round, populated, err)
		}
	}
	if err := bystander.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("a process outside the cgroup did not survive: %v", err)
	}
}

func TestOpenCellsCgroupChecksTheFilesystemAndTheInode(t *testing.T) {
	plain := t.TempDir()
	if f, err := openCellsCgroup(evidenceCell{CgroupPath: plain}); err == nil {
		f.Close()
		t.Error("a directory that is not on cgroup2 was accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	if f, err := openCellsCgroup(evidenceCell{CgroupPath: link}); err == nil {
		f.Close()
		t.Error("a symlink to the cells cgroup was followed")
	}
	if f, err := openCellsCgroup(evidenceCell{CgroupPath: filepath.Join(plain, "absent")}); err == nil {
		f.Close()
		t.Error("a missing directory was accepted")
	}

	cg := newTestCgroup(t)
	var st syscall.Stat_t
	if err := syscall.Fstat(int(cg.dir.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	f, err := openCellsCgroup(evidenceCell{CgroupPath: cg.path, Inode: st.Ino})
	if err != nil {
		t.Fatalf("the registered cgroup was refused: %v", err)
	}
	f.Close()
	if f, err := openCellsCgroup(evidenceCell{CgroupPath: cg.path, Inode: st.Ino + 1}); err == nil {
		f.Close()
		t.Error("a cgroup with another inode than the registration was accepted")
	}
	if populated, err := cellsCgroupPopulated(evidenceCell{CgroupPath: cg.path, Inode: st.Ino}); err != nil || populated {
		t.Errorf("cellsCgroupPopulated of an empty cgroup = %v, %v", populated, err)
	}
}

func shortKillWait(t *testing.T) {
	t.Helper()
	old := trialKillWait
	trialKillWait = 30 * time.Millisecond
	t.Cleanup(func() { trialKillWait = old })
}

// fakeCgroupDir is a directory with the two files the kill uses, as plain files.
func fakeCgroupDir(t *testing.T, kill bool, events string) (*os.File, string) {
	t.Helper()
	path := t.TempDir()
	if kill {
		if err := os.WriteFile(filepath.Join(path, "cgroup.kill"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if events != "" {
		if err := os.WriteFile(filepath.Join(path, "cgroup.events"), []byte(events), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dir.Close() })
	return dir, path
}

func TestKillCgroupWritesOneAndSucceedsOnPopulatedZero(t *testing.T) {
	dir, path := fakeCgroupDir(t, true, "populated 0\nfrozen 0\n")
	if err := killCgroup(dir, time.Second); err != nil {
		t.Fatalf("killCgroup: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(path, "cgroup.kill")); string(got) != "1" {
		t.Fatalf("cgroup.kill holds %q, want \"1\"", got)
	}
}

func TestKillCgroupRefuses(t *testing.T) {
	shortKillWait(t)
	for name, tc := range map[string]struct {
		kill   bool
		events string
		want   string
	}{
		"no cgroup.kill (old kernel)":     {false, "populated 0\n", "cgroup.kill cannot be written"},
		"still populated at the deadline": {true, "populated 1\n", "still holds a live process"},
		"events unreadable":               {true, "", "cgroup.events"},
		"events without a populated key":  {true, "frozen 0\n", "populated"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, path := fakeCgroupDir(t, tc.kill, tc.events)
			start := time.Now()
			err := killCgroup(dir, trialKillWait)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "refusing the reset") {
				t.Fatalf("err = %v, want a refusal containing %q", err, tc.want)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatalf("the refusal took %v: the wait must be bounded", time.Since(start))
			}
			if !tc.kill {
				if _, err := os.Stat(filepath.Join(path, "cgroup.kill")); !os.IsNotExist(err) {
					t.Fatalf("a missing cgroup.kill was created: %v", err)
				}
			}
		})
	}
}

// The wait is for the processes to leave: populated 1 at first, 0 a little later.
func TestKillCgroupWaitsForTheProcessesToLeave(t *testing.T) {
	dir, path := fakeCgroupDir(t, true, "populated 1\n")
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(path, "cgroup.events"), []byte("populated 0\n"), 0o600)
	}()
	if err := killCgroup(dir, 5*time.Second); err != nil {
		t.Fatalf("killCgroup: %v", err)
	}
}

// Nothing in the reset signals a process by number, through a pidfd, by scan or by uid: the only
// kill is the write to cgroup.kill.
func TestTrialResetSignalsOnlyThroughCgroupKill(t *testing.T) {
	src, err := os.ReadFile("trial_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	if calls := regexp.MustCompile(`(?:unix|syscall)\.(?:Kill|Tgkill|Tkill|PidfdSendSignal|PidfdOpen|Setuid|Setresuid)\(|\.Process\.(?:Kill|Signal)\(|os/exec`).FindAllString(string(src), -1); len(calls) != 0 {
		t.Fatalf("trial_linux.go signals or spawns outside cgroup.kill: %q", calls)
	}
	if !strings.Contains(string(src), `"cgroup.kill"`) {
		t.Fatal("trial_linux.go does not write cgroup.kill")
	}
}

// dumpCgroup logs what is left in the cgroup subtree: pid, state, name and live task count.
func dumpCgroup(t *testing.T, path string) {
	t.Helper()
	for _, pid := range cgroupProcs(t, path) {
		status, err := os.ReadFile(filepath.Join("/proc", pid, "status"))
		if err != nil {
			t.Logf("pid %s: %v", pid, err)
			continue
		}
		var name, state string
		for _, line := range strings.Split(string(status), "\n") {
			if v, ok := strings.CutPrefix(line, "Name:"); ok {
				name = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(line, "State:"); ok {
				state = strings.TrimSpace(v)
			}
		}
		tasks, _ := os.ReadDir(filepath.Join("/proc", pid, "task"))
		t.Logf("pid %s %s %s, %d task(s)", pid, name, state, len(tasks))
	}
}

// A zombie thread-group leader with running threads: the kernel's cgroup.kill signals the
// leader as one task, and on some kernels (5.15 here) a leader that is exiting does not pass
// the signal on to its threads, so the group keeps running. The reset must then refuse and
// never report success while a process is left; on a kernel that kills the group, success
// with an empty cgroup is the other allowed outcome.
func TestKillCgroupNeverSucceedsWhileAZombieLeaderStillRuns(t *testing.T) {
	cg := newTestCgroup(t)
	var started []*exec.Cmd
	for i := 0; i < 3; i++ {
		cmd := inCgroup(roleCmd("zombie"), cg.dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		started = append(started, cmd)
		go func() { _ = cmd.Wait() }()
	}
	t.Cleanup(func() { // the group is killed by pid: a signal to the process, not to the exiting leader task
		for _, cmd := range started {
			_ = cmd.Process.Kill()
		}
	})
	time.Sleep(300 * time.Millisecond)
	err := killCgroup(cg.dir, time.Second)
	pids := cgroupProcs(t, cg.path)
	switch {
	case err == nil && len(pids) != 0:
		dumpCgroup(t, cg.path)
		t.Fatalf("killCgroup succeeded with processes %v left", pids)
	case err == nil:
		t.Logf("this kernel's cgroup.kill ends zombie leaders with running threads")
	default:
		t.Logf("this kernel's cgroup.kill leaves zombie leaders with running threads; the reset refuses: %v", err)
	}
}

// The cleanup of the no-reap/v1 leftovers and the net-sink parent on a real cgroup2 filesystem
// (skips unless the test runs in a delegated subtree, like the cgroup.kill tests above).
func TestResetHostRemovesNrCgroupsAndCreatesTheNetSinkParentOnRealCgroups(t *testing.T) {
	cg := newTestCgroup(t)
	host := trialHost{
		openCells:  func(evidenceCell) (*os.File, error) { return os.Open(cg.path) },
		checkFS:    requireCgroup2Dir,
		runtimeDir: filepath.Join(t.TempDir(), "absent"),
		owner:      trialfix.Owner{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())},
	}
	for _, name := range []string{"tbound-nr-aa/child", "tbound-nr-bb", "tbound-cmd-cc"} {
		if err := os.MkdirAll(filepath.Join(cg.path, name), 0o755); err != nil {
			t.Skipf("cannot create cgroups here: %v", err)
		}
	}
	parent, err := host.ensureNetSinkParent(evidenceCell{})
	if err != nil {
		t.Fatal(err)
	}
	parent.Close()
	if err := host.cleanNoReap(context.Background(), evidenceCell{}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"tbound-nr-aa": false, "tbound-nr-bb": false, "tbound-cmd-cc": true, "net-sink": true} {
		if _, err := os.Stat(filepath.Join(cg.path, name)); (err == nil) != want {
			t.Errorf("%s present=%v, want %v", name, err == nil, want)
		}
	}
}

// A no-reap/v1 run leaves its survivors in tbound-nr-* scopes by design, and only the reset kills
// them. `reset --check` (the runner's Prepare, which runs before the reset) must therefore not count
// them, or the next trial after a no-reap run is refused forever; a live process anywhere else in
// the cells subtree still refuses.
func TestCellsLiveOutsideNoReapIgnoresOnlyNoReapScopes(t *testing.T) {
	for name, tc := range map[string]struct {
		events, procs string
		children      map[string]string // name -> cgroup.events
		want          bool
	}{
		"idle":                          {"populated 0\n", "", map[string]string{"net-sink": "populated 0\n"}, false},
		"only no-reap survivors":        {"populated 1\n", "", map[string]string{"tbound-nr-aa": "populated 1\n", "net-sink": "populated 0\n"}, false},
		"a live command scope":          {"populated 1\n", "", map[string]string{"tbound-nr-aa": "populated 1\n", "tbound-cmd-bb": "populated 1\n"}, true},
		"a live net-sink scope":         {"populated 1\n", "", map[string]string{"net-sink": "populated 1\n"}, true},
		"a process in the cells itself": {"populated 1\n", "42\n", map[string]string{"tbound-nr-aa": "populated 1\n"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			dir, path := fakeCgroupDir(t, true, tc.events)
			if err := os.WriteFile(filepath.Join(path, "cgroup.procs"), []byte(tc.procs), 0o600); err != nil {
				t.Fatal(err)
			}
			for child, events := range tc.children {
				if err := os.Mkdir(filepath.Join(path, child), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, child, "cgroup.events"), []byte(events), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := cgroupLiveOutsideNoReap(dir); err != nil || got != tc.want {
				t.Fatalf("cgroupLiveOutsideNoReap = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	// A child without readable cgroup.events is an error, not idle.
	dir, path := fakeCgroupDir(t, true, "populated 1\n")
	_ = os.WriteFile(filepath.Join(path, "cgroup.procs"), nil, 0o600)
	_ = os.Mkdir(filepath.Join(path, "tbound-cmd-x"), 0o700)
	if _, err := cgroupLiveOutsideNoReap(dir); err == nil {
		t.Fatal("an unreadable child cgroup was taken as idle")
	}
}

// The same on the kernel: a live process in a tbound-nr-* scope does not make --check refuse, one in
// any other child does, and the reset's kill ends both.
func TestCellsLiveOutsideNoReapOnARealCgroup(t *testing.T) {
	cg := newTestCgroup(t)
	liveOutsideNoReap := func() (bool, error) {
		dir, err := os.Open(cg.path)
		if err != nil {
			return false, err
		}
		defer dir.Close()
		return cgroupLiveOutsideNoReap(dir)
	}
	start := func(child string) {
		path := filepath.Join(cg.path, child)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		d, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		cmd := inCgroup(roleCmd("sleeper"), d)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { _ = cmd.Wait() }()
		time.Sleep(100 * time.Millisecond)
	}
	start("tbound-nr-aa")
	if live, err := liveOutsideNoReap(); err != nil || live {
		t.Fatalf("a no-reap survivor alone: live %v, err %v", live, err)
	}
	start("tbound-cmd-bb")
	if live, err := liveOutsideNoReap(); err != nil || !live {
		t.Fatalf("a live command scope: live %v, err %v", live, err)
	}
	if err := killCgroup(cg.dir, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}
