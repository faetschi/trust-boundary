//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Regression tests for a thread-group leader that exited while worker threads keep running.
// When the main thread of a process exits (SYS_exit, not exit_group) the leader is a zombie
// ("State: Z" in /proc/<tgid>/status) although the process still executes code, can write
// files and must be killed. Which threads still run cannot be read from /proc/<tgid>/task: a
// worker that exits and is replaced by a new one between the listing and the read hides the
// replacement, and the group looked dead. `tbound trial reset` therefore kills every group of
// the cell uid through its pidfd and trusts only the kernel's answer that the whole thread
// group exited (the pidfd of a leader is readable then and not before).

const zombieLeaderHelperEnv = "TBOUND_TEST_ZOMBIE_LEADER_HELPER"

// init turns this test binary into the zombie-leader helper when the parent re-executes it
// with zombieLeaderHelperEnv set. Package init functions run locked to the main OS thread
// (the thread whose tid is the tgid), so the raw SYS_exit below ends exactly the leader
// thread. The worker goroutine is locked to its own thread and keeps the process alive; a
// watchdog bounds the helper's life if the parent never kills it.
func init() {
	if os.Getenv(zombieLeaderHelperEnv) != "1" {
		return
	}
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		close(ready)
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}()
	<-ready
	runtime.LockOSThread()
	// syscall.Syscall (not RawSyscall) so the runtime hands the P of the dying thread over.
	syscall.Syscall(syscall.SYS_EXIT, 0, 0, 0)
	os.Exit(3) // not reached
}

// churnHelperScript is a zombie-leader process whose only threads are a chain: each worker
// sleeps briefly, starts its replacement and exits. Python has no runtime thread of its own, so
// at any moment the only live task is the current worker. The alarm bounds the helper's life.
const churnHelperScript = `
import _thread, ctypes, signal, sys, time
signal.alarm(120)
def worker():
    _thread.start_new_thread(worker, ())
_thread.start_new_thread(worker, ())
ctypes.CDLL(None).syscall(int(sys.argv[1]), 0)
time.sleep(3600)
`

// startZombieLeader starts cmd and waits until the kernel reports its leader thread as a
// zombie. done is closed when the process is reaped.
func startZombieLeader(t *testing.T, cmd *exec.Cmd) (pid int, done chan struct{}, waitErr *error) {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid = cmd.Process.Pid
	done = make(chan struct{})
	waitErr = new(error)
	go func() { *waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	statusPath := filepath.Join("/proc", strconv.Itoa(pid), "status")
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err := os.ReadFile(statusPath)
		if err == nil && strings.Contains(string(status), "\nState:\tZ") {
			return pid, done, waitErr
		}
		if time.Now().After(deadline) {
			t.Fatalf("the helper never became a zombie leader (status %q, err %v)", status, err)
		}
		select {
		case <-done:
			t.Fatalf("the helper exited by itself: %v", *waitErr)
		case <-time.After(time.Millisecond):
		}
	}
}

// scanRootFor returns a scan root that holds only a symlink to the helper's /proc entry, so a
// test never reaches the other processes of the user running it.
func scanRootFor(t *testing.T, pid int) string {
	t.Helper()
	scan := t.TempDir()
	if err := os.Symlink(filepath.Join("/proc", strconv.Itoa(pid)), filepath.Join(scan, strconv.Itoa(pid))); err != nil {
		t.Fatal(err)
	}
	return scan
}

// pidfdReadable polls a pidfd without waiting: true once the whole thread group has exited.
func pidfdReadable(t *testing.T, fd int) bool {
	t.Helper()
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	if err != nil {
		t.Fatalf("poll pidfd: %v", err)
	}
	return n == 1 && fds[0].Revents&unix.POLLIN != 0
}

func requireDeathBySIGKILL(t *testing.T, done chan struct{}, waitErr *error) {
	t.Helper()
	select {
	case <-done:
		ee, ok := (*waitErr).(*exec.ExitError)
		if !ok {
			t.Fatalf("wait = %v, want death by SIGKILL", *waitErr)
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
			t.Fatalf("wait = %v, want death by SIGKILL", *waitErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the process is still running")
	}
}

// requireCellUID returns the uid of the test process or skips: killUIDProcesses never acts for
// uid 0, and the real-process tests need a uid they may signal.
func requireCellUID(t *testing.T) uint32 {
	t.Helper()
	uid := uint32(os.Getuid())
	if uid == 0 {
		t.Skip("killUIDProcesses never acts for uid 0; run this test as an unprivileged user")
	}
	return uid
}

// requireRealUIDOfZombieLeader checks on the real kernel that the status of a zombie leader
// still reports the credentials the reset attributes the group by.
func requireRealUIDOfZombieLeader(t *testing.T, pid int, want uint32) {
	t.Helper()
	got, found, err := readProcRealUID("/proc", pid)
	if err != nil || !found || got != want {
		t.Fatalf("zombie leader pid %d: real uid %d found %v err %v, want %d", pid, got, found, err, want)
	}
}

// The scan root holds a zombie leader of the cell uid. Task directories are not consulted:
// one that lists nothing, or only a thread that already exited, must not make the group dead.
func TestKillUIDProcessesSignalsAZombieLeaderUntilTheKernelSaysItExited(t *testing.T) {
	shortKillWait(t)
	zombie := procStatus("Z (zombie)", 995, 995, 995, 995)
	for name, prepare := range map[string]func(t *testing.T, root string){
		"no task directory": func(*testing.T, string) {},
		"only a worker that vanished": func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "10", "task", "11"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"only the zombie leader task": func(t *testing.T, root string) {
			dir := filepath.Join(root, "10", "task", "10")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "status"), []byte(zombie), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := fakeProc(t, map[int]string{10: zombie})
			prepare(t, root)
			var signaled []int
			var closed int
			pin := func(pid int) (procPin, error) {
				return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed}, nil
			}
			if err := killUIDProcesses(root, 995, pin); err != nil {
				t.Fatalf("killUIDProcesses: %v", err)
			}
			if len(signaled) != 1 || signaled[0] != 10 {
				t.Fatalf("signaled %v, want the zombie leader's group of pid 10: the pidfd, not /proc/task, says it is dead", signaled)
			}
		})
	}
}

func TestKillUIDProcessesLeavesAnExitedZombieAlone(t *testing.T) {
	shortKillWait(t)
	root := fakeProc(t, map[int]string{10: procStatus("Z (zombie)", 995, 995, 995, 995)})
	var signaled []int
	var closed int
	pin := func(pid int) (procPin, error) {
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed, exited: true}, nil
	}
	if err := killUIDProcesses(root, 995, pin); err != nil {
		t.Fatalf("killUIDProcesses: %v", err)
	}
	if len(signaled) != 0 || closed != 1 {
		t.Fatalf("signaled %v, closed %d: a group whose pidfd is readable is dead", signaled, closed)
	}
}

func TestKillUIDProcessesTakesTheUIDFromTheLeaderNotFromTasks(t *testing.T) {
	shortKillWait(t)
	root := fakeProc(t, map[int]string{
		10: procStatus("Z (zombie)", 1000, 1000, 1000, 1000),
		11: procStatus("Z (zombie)", 995, 0, 0, 0),
	})
	// A task list is no evidence of anything: this one claims a worker of the cell uid.
	task := filepath.Join(root, "10", "task", "12")
	if err := os.MkdirAll(task, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task, "status"), []byte(procStatus("S (sleeping)", 995, 995, 995, 995)), 0o600); err != nil {
		t.Fatal(err)
	}
	var signaled []int
	var closed int
	pin := func(pid int) (procPin, error) {
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed}, nil
	}
	if err := killUIDProcesses(root, 995, pin); err != nil {
		t.Fatalf("killUIDProcesses: %v", err)
	}
	if len(signaled) != 1 || signaled[0] != 11 {
		t.Fatalf("signaled %v, want only pid 11, whose leader has the cell real uid", signaled)
	}
}

func TestKillUIDProcessesRefusesWhenAGroupDoesNotExit(t *testing.T) {
	shortKillWait(t)
	root := fakeProc(t, map[int]string{10: procStatus("Z (zombie)", 995, 995, 995, 995)})
	var signaled []int
	var closed int
	pin := func(pid int) (procPin, error) {
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed, noKill: true}, nil
	}
	start := time.Now()
	err := killUIDProcesses(root, 995, pin)
	if err == nil || !strings.Contains(err.Error(), "10") {
		t.Fatalf("err = %v, want a refusal naming pid 10", err)
	}
	if len(signaled) != 1 || closed != 1 {
		t.Fatalf("signaled %v, closed %d, want one signal and one closed pidfd", signaled, closed)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the wait took %v: it must be bounded by trialKillWait", time.Since(start))
	}
}

func TestKillUIDProcessesRefusesAStatusWithoutARealUID(t *testing.T) {
	shortKillWait(t)
	for name, status := range map[string]string{
		"no Uid line":   "Name:\tx\nState:\tZ (zombie)\n",
		"unparsable":    "State:\tS (sleeping)\nUid:\tabc\n",
		"empty Uid":     "State:\tS (sleeping)\nUid:\n",
		"negative uid":  "State:\tS (sleeping)\nUid:\t-1\t-1\t-1\t-1\n",
		"truncated uid": "State:\tS (sleeping)\nUid:\t99999999999\t0\t0\t0\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := fakeProc(t, map[int]string{10: status})
			pin := func(int) (procPin, error) { t.Fatal("pinned a process whose uid cannot be read"); return nil, nil }
			if err := killUIDProcesses(root, 995, pin); err == nil {
				t.Fatal("a status without a readable real uid did not refuse the reset")
			}
		})
	}
}

// pidfdPin.Exited is poll(2) for POLLIN with a bounded wait; pollPidfd is the seam.
func TestPidfdExitedFollowsPoll(t *testing.T) {
	old := pollPidfd
	t.Cleanup(func() { pollPidfd = old })
	type result struct {
		n       int
		revents int16
		err     error
	}
	for name, tc := range map[string]struct {
		results    []result // consumed in order; the last one repeats
		wantExited bool
		wantErr    bool
		wantCalls  int
	}{
		"not readable":         {[]result{{0, 0, nil}}, false, false, 1},
		"readable":             {[]result{{1, unix.POLLIN, nil}}, true, false, 1},
		"interrupted then yes": {[]result{{-1, 0, unix.EINTR}, {1, unix.POLLIN, nil}}, true, false, 2},
		"error condition":      {[]result{{1, unix.POLLERR, nil}}, false, true, 1},
		"invalid descriptor":   {[]result{{1, unix.POLLNVAL, nil}}, false, true, 1},
		"poll fails":           {[]result{{-1, 0, unix.EBADF}}, false, true, 1},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			pollPidfd = func(fds []unix.PollFd, timeout int) (int, error) {
				if len(fds) != 1 || fds[0].Events != unix.POLLIN || timeout < 0 || timeout > 1000 {
					t.Fatalf("poll(%v, %d)", fds, timeout)
				}
				r := tc.results[min(calls, len(tc.results)-1)]
				calls++
				fds[0].Revents = r.revents
				return r.n, r.err
			}
			exited, err := (&pidfdPin{fd: 99}).Exited(time.Second)
			if exited != tc.wantExited || (err != nil) != tc.wantErr || calls != tc.wantCalls {
				t.Fatalf("exited=%v err=%v calls=%d, want exited=%v err=%v calls=%d", exited, err, calls, tc.wantExited, tc.wantErr, tc.wantCalls)
			}
		})
	}
}

// A kernel that never reports the exit (poll says not ready) refuses the reset even though the
// SIGKILL was sent: only the kernel's confirmation counts.
func TestKillUIDProcessesRefusesWhenPollNeverReportsTheExit(t *testing.T) {
	uid := requireCellUID(t)
	shortKillWait(t)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	old := pollPidfd
	t.Cleanup(func() { pollPidfd = old })
	pollPidfd = func([]unix.PollFd, int) (int, error) { return 0, nil }
	err := killUIDProcesses(scanRootFor(t, cmd.Process.Pid), uid, pinProcessPidfd)
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(cmd.Process.Pid)) {
		t.Fatalf("err = %v, want a refusal naming pid %d", err, cmd.Process.Pid)
	}
}

// The real thing: a process whose leader thread exited while a worker thread runs. The
// kernel reports the leader as a zombie and its pidfd is not readable; the reset must still
// kill the group through its pidfd and see the pidfd turn readable.
func TestKillUIDProcessesKillsARealZombieLeaderWithALiveThread(t *testing.T) {
	uid := requireCellUID(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), zombieLeaderHelperEnv+"=1")
	pid, done, waitErr := startZombieLeader(t, cmd)
	requireRealUIDOfZombieLeader(t, pid, uid)
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatalf("pidfd_open of a zombie leader with a live thread: %v", err)
	}
	defer unix.Close(fd)
	if pidfdReadable(t, fd) {
		t.Fatal("the pidfd of a zombie leader with a running worker is readable: the test would not prove anything")
	}
	if err := killUIDProcesses(scanRootFor(t, pid), uid, pinProcessPidfd); err != nil {
		t.Fatalf("killUIDProcesses: %v", err)
	}
	if !pidfdReadable(t, fd) {
		t.Fatal("killUIDProcesses returned while the thread group is still running")
	}
	requireDeathBySIGKILL(t, done, waitErr)
}

// Thread churn: the zombie leader's only tasks are workers that each start a replacement and
// exit. A /proc/<tgid>/task listing taken between a worker's creation and its replacement's
// misses the replacement, and a read of the listed worker fails with ENOENT. The reset must
// not conclude from that that the group is dead: it returns only when the kernel says the
// whole group exited. Repeated, because the old classification lost the race only sometimes.
func TestKillUIDProcessesKillsAZombieLeaderUnderThreadChurn(t *testing.T) {
	uid := requireCellUID(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed for the thread-churn helper")
	}
	const attempts = 60
	for attempt := 0; attempt < attempts; attempt++ {
		cmd := exec.Command(python, "-I", "-c", churnHelperScript, strconv.Itoa(syscall.SYS_EXIT))
		pid, done, waitErr := startZombieLeader(t, cmd)
		requireRealUIDOfZombieLeader(t, pid, uid)
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			t.Fatalf("pidfd_open of a zombie leader under churn: %v", err)
		}
		if pidfdReadable(t, fd) {
			unix.Close(fd)
			t.Fatal("the churning group already exited: the test would not prove anything")
		}
		err = killUIDProcesses(scanRootFor(t, pid), uid, pinProcessPidfd)
		readable := pidfdReadable(t, fd)
		unix.Close(fd)
		if err != nil {
			t.Fatalf("attempt %d: killUIDProcesses: %v", attempt, err)
		}
		if !readable {
			t.Fatalf("attempt %d: killUIDProcesses returned success while the thread group of pid %d still runs", attempt, pid)
		}
		requireDeathBySIGKILL(t, done, waitErr)
	}
}
