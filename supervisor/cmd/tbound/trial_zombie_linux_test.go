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
)

// Regression tests for a zombie thread-group leader with live worker threads. When the main
// thread of a process exits (SYS_exit, not exit_group) while another thread keeps running,
// /proc/<tgid>/status reports "State: Z (zombie)" for the group although the process still
// executes code, and it stays reapable only after the last thread is gone. Treating that
// state as dead let `tbound trial reset` skip a leftover cell process that could still
// mutate the live root.

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

// addTasks gives the fake process pid a task directory: the leader (tid == pid) plus others.
func addTasks(t *testing.T, root string, pid int, tasks map[int]string) {
	t.Helper()
	for tid, status := range tasks {
		dir := filepath.Join(root, strconv.Itoa(pid), "task", strconv.Itoa(tid))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProcOwnershipClassifiesZombieLeaderGroups(t *testing.T) {
	const uid = 995
	zombie := func(u int) string { return procStatus("Z (zombie)", u, u, u, u) }
	sleeping := func(u int) string { return procStatus("S (sleeping)", u, u, u, u) }
	cases := []struct {
		name                string
		leader              string
		tasks               map[int]string // nil: no task directory at all
		wantLive, wantOwned bool
		wantErr             bool
	}{
		{"live leader keeps the old rule", sleeping(uid), nil, true, true, false},
		{"live leader of another uid", sleeping(1000), nil, true, false, false},
		{"plain zombie without a task directory", zombie(uid), nil, false, false, false},
		{"zombie leader, only the leader task", zombie(uid), map[int]string{10: zombie(uid)}, false, false, false},
		{"zombie leader, all tasks zombies or dead", zombie(uid), map[int]string{10: zombie(uid), 11: procStatus("X (dead)", uid, uid, uid, uid)}, false, false, false},
		{"zombie leader with a live worker", zombie(uid), map[int]string{10: zombie(uid), 11: sleeping(uid)}, true, true, false},
		{"zombie leader with a running worker", zombie(uid), map[int]string{10: zombie(uid), 11: procStatus("R (running)", uid, uid, uid, uid)}, true, true, false},
		{"zombie leader with a worker of another uid", zombie(uid), map[int]string{10: zombie(uid), 11: sleeping(1000)}, true, false, false},
		{"zombie leader with a root worker", zombie(uid), map[int]string{10: zombie(uid), 11: sleeping(0)}, true, false, false},
		{"leader uid does not decide, the live tasks do", zombie(1000), map[int]string{10: zombie(1000), 11: sleeping(uid)}, true, true, false},
		{"mixed uids among live tasks refuse", zombie(uid), map[int]string{10: zombie(uid), 11: sleeping(uid), 12: sleeping(1000)}, false, false, true},
		{"live task without a uid refuses", zombie(uid), map[int]string{10: zombie(uid), 11: "Name:\tx\nState:\tS (sleeping)\n"}, false, false, true},
		{"live task with an unparsable uid refuses", zombie(uid), map[int]string{10: zombie(uid), 11: "State:\tS (sleeping)\nUid:\tabc\n"}, false, false, true},
		{"live task without a state refuses", zombie(uid), map[int]string{10: zombie(uid), 11: "Uid:\t995\t995\t995\t995\n"}, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeProc(t, map[int]string{10: tc.leader})
			if tc.tasks != nil {
				addTasks(t, root, 10, tc.tasks)
			}
			live, owned, err := procOwnership(root, 10, uid)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if live != tc.wantLive || owned != tc.wantOwned {
				t.Fatalf("live=%v owned=%v, want live=%v owned=%v", live, owned, tc.wantLive, tc.wantOwned)
			}
		})
	}
}

func TestProcOwnershipRefusesAnUnreadableTaskDirectory(t *testing.T) {
	root := fakeProc(t, map[int]string{10: procStatus("Z (zombie)", 995, 995, 995, 995)})
	// A regular file where the task directory belongs: the group cannot be inspected.
	if err := os.WriteFile(filepath.Join(root, "10", "task"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := procOwnership(root, 10, 995); err == nil {
		t.Fatal("an unreadable task directory of a zombie leader did not refuse")
	}
}

func TestProcOwnershipSkipsATaskThatVanishes(t *testing.T) {
	root := fakeProc(t, map[int]string{10: procStatus("Z (zombie)", 995, 995, 995, 995)})
	addTasks(t, root, 10, map[int]string{10: procStatus("Z (zombie)", 995, 995, 995, 995)})
	// A task directory without a status file: the thread exited between listing and reading.
	if err := os.Mkdir(filepath.Join(root, "10", "task", "11"), 0o700); err != nil {
		t.Fatal(err)
	}
	live, owned, err := procOwnership(root, 10, 995)
	if err != nil || live || owned {
		t.Fatalf("live=%v owned=%v err=%v, want a dead group", live, owned, err)
	}
}

func TestKillUIDProcessesSignalsAZombieLeaderGroupWithALiveThread(t *testing.T) {
	trialKillPoll = time.Millisecond
	root := fakeProc(t, map[int]string{
		10: procStatus("Z (zombie)", 995, 995, 995, 995),
		11: procStatus("Z (zombie)", 995, 995, 995, 995),
		12: procStatus("Z (zombie)", 1000, 1000, 1000, 1000),
		13: procStatus("Z (zombie)", 995, 995, 995, 995),
	})
	addTasks(t, root, 10, map[int]string{
		10: procStatus("Z (zombie)", 995, 995, 995, 995),
		14: procStatus("S (sleeping)", 995, 995, 995, 995),
	})
	addTasks(t, root, 11, map[int]string{11: procStatus("Z (zombie)", 995, 995, 995, 995)})
	addTasks(t, root, 12, map[int]string{
		12: procStatus("Z (zombie)", 1000, 1000, 1000, 1000),
		15: procStatus("S (sleeping)", 1000, 1000, 1000, 1000),
	})
	var signaled []int
	var closed int
	pin := func(pid int) (procPin, error) {
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed}, nil
	}
	if err := killUIDProcesses(root, 995, pin); err != nil {
		t.Fatalf("killUIDProcesses: %v", err)
	}
	if len(signaled) != 1 || signaled[0] != 10 {
		t.Fatalf("signaled %v, want exactly the group of pid 10", signaled)
	}
	for _, pid := range []int{11, 12, 13} {
		if _, err := os.Stat(filepath.Join(root, strconv.Itoa(pid))); err != nil {
			t.Errorf("pid %d was removed", pid)
		}
	}
	if closed != 1 {
		t.Errorf("closed %d pidfds, want 1", closed)
	}
}

func TestKillUIDProcessesFailsWhileAZombieLeaderGroupSurvives(t *testing.T) {
	trialKillPoll = time.Millisecond
	root := fakeProc(t, map[int]string{10: procStatus("Z (zombie)", 995, 995, 995, 995)})
	addTasks(t, root, 10, map[int]string{
		10: procStatus("Z (zombie)", 995, 995, 995, 995),
		11: procStatus("S (sleeping)", 995, 995, 995, 995),
	})
	var signaled []int
	var closed int
	pin := func(pid int) (procPin, error) {
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed, noKill: true}, nil
	}
	if err := killUIDProcesses(root, 995, pin); err == nil || !strings.Contains(err.Error(), "10") {
		t.Fatalf("err = %v, want a failure naming pid 10", err)
	}
}

func TestKillUIDProcessesRefusesAnAmbiguousZombieLeaderGroup(t *testing.T) {
	trialKillPoll = time.Millisecond
	root := fakeProc(t, map[int]string{10: procStatus("Z (zombie)", 995, 995, 995, 995)})
	addTasks(t, root, 10, map[int]string{
		10: procStatus("Z (zombie)", 995, 995, 995, 995),
		11: procStatus("S (sleeping)", 995, 995, 995, 995),
		12: procStatus("S (sleeping)", 0, 0, 0, 0),
	})
	var signaled []int
	var closed int
	pin := func(pid int) (procPin, error) {
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed}, nil
	}
	if err := killUIDProcesses(root, 995, pin); err == nil {
		t.Fatal("a group with live threads of two uids did not refuse the reset")
	}
	if len(signaled) != 0 {
		t.Fatalf("signaled %v: a group that mixes uids must not be signaled", signaled)
	}
}

// The real thing: a process whose leader thread exited while a worker thread runs. The
// kernel reports the leader as a zombie; the reset must still kill the group through its
// pidfd. The scan root holds only a symlink to the helper's /proc entry, so the test never
// reaches the other processes of the user running it.
func TestKillUIDProcessesKillsARealZombieLeaderWithALiveThread(t *testing.T) {
	uid := uint32(os.Getuid())
	if uid == 0 {
		t.Skip("killUIDProcesses never acts for uid 0; run this test as an unprivileged user")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), zombieLeaderHelperEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})

	procDir := filepath.Join("/proc", strconv.Itoa(pid))
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err := os.ReadFile(filepath.Join(procDir, "status"))
		if err == nil && strings.Contains(string(status), "\nState:\tZ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the helper never became a zombie leader (status %q, err %v)", status, err)
		}
		select {
		case <-done:
			t.Fatalf("the helper exited by itself: %v", waitErr)
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Precondition: at least one task other than the zombie leader is not a zombie.
	tasks, err := os.ReadDir(filepath.Join(procDir, "task"))
	if err != nil {
		t.Fatal(err)
	}
	liveTasks := 0
	for _, task := range tasks {
		status, err := os.ReadFile(filepath.Join(procDir, "task", task.Name(), "status"))
		if err == nil && !strings.Contains(string(status), "\nState:\tZ") {
			liveTasks++
		}
	}
	if liveTasks == 0 {
		t.Fatal("the helper has no live thread: the test would not prove anything")
	}

	scan := t.TempDir()
	if err := os.Symlink(procDir, filepath.Join(scan, strconv.Itoa(pid))); err != nil {
		t.Fatal(err)
	}
	if err := killUIDProcesses(scan, uid, pinProcessPidfd); err != nil {
		t.Fatalf("killUIDProcesses: %v", err)
	}
	select {
	case <-done:
		err := waitErr
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("wait = %v, want death by SIGKILL", err)
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
			t.Fatalf("wait = %v, want death by SIGKILL", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("killUIDProcesses returned without killing the zombie-leader process: its worker thread is still running")
	}
}
