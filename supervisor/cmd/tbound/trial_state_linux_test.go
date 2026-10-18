//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/workflow"
)

func trialTestTrace(events ...audit.Event) audit.Trace {
	trace := audit.Trace{Records: []audit.Record{}, Effects: []audit.EffectTrace{}}
	for i, event := range events {
		trace.Records = append(trace.Records, audit.Record{Version: 1, Sequence: uint64(i + 1), Event: event, Hash: "h" + strconv.Itoa(i+1)})
	}
	return trace
}

func trialCatalogEvent(t *testing.T, kind, admissionHash string) audit.Event {
	t.Helper()
	data, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash})
	if err != nil {
		t.Fatal(err)
	}
	return audit.Event{Kind: kind, ID: kind + "/" + admissionHash, Data: data}
}

var trialTestRoot = workflow.RootIdentity{Device: 7, Inode: 42}

const trialTestRegistration = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// trialAdmitted is the lifecycle record of a managed-root run that holds the live root.
func trialAdmitted(t *testing.T, root workflow.RootIdentity, runID string) (audit.Event, managedLiveRootRunAdmittedEvent) {
	t.Helper()
	admitted := managedLiveRootRunAdmittedEvent{SchemaVersion: "tbound-managed-live-root-run-admitted/v1", Root: root,
		RegistrationDigest: trialTestRegistration,
		Descriptor:         managedLiveRootDescriptorIdentity{Path: "/var/lib/tbound/live", Device: root.Device, Inode: root.Inode, MountID: 9, Mode: 0o700},
		ServiceCgroup:      piruntime.D09ManagedLiveRootCgroupIdentity{Path: "/system.slice/tbound-pi.service", Device: 1, Inode: 2, MountID: 3},
		RunID:              runID, WorkflowID: "wf-" + runID, PublicationID: "pub-" + runID, OwnerEpoch: 1, ProcessID: 100, ServiceUID: 990, ServiceGID: 990}
	data, err := json.Marshal(admitted)
	if err != nil {
		t.Fatal(err)
	}
	return audit.Event{Kind: "managed_live_root_run_admitted", ID: managedLiveRootRunEventID(root, trialTestRegistration, runID, 1), Data: data}, admitted
}

func trialReleased(t *testing.T, admitted managedLiveRootRunAdmittedEvent, sequence uint64, hash string) audit.Event {
	t.Helper()
	key := managedLiveRootRunEventID(admitted.Root, admitted.RegistrationDigest, admitted.RunID, admitted.OwnerEpoch)
	data, err := json.Marshal(managedLiveRootRunResolvedEvent{SchemaVersion: "tbound-managed-live-root-run-resolved/v1", Root: admitted.Root,
		RegistrationDigest: admitted.RegistrationDigest, RunID: admitted.RunID, WorkflowID: admitted.WorkflowID, PublicationID: admitted.PublicationID,
		OwnerEpoch: admitted.OwnerEpoch, AdmissionSequence: sequence, AdmissionRecordHash: hash, Outcome: "no-publication"})
	if err != nil {
		t.Fatal(err)
	}
	return audit.Event{Kind: "managed_live_root_run_released", ID: key + "/released", Data: data}
}

func TestTrialPendingFromIsQuietWhenEverythingIsSettled(t *testing.T) {
	admittedEvent, admitted := trialAdmitted(t, trialTestRoot, "run-1")
	catalog := trialTestTrace(trialCatalogEvent(t, "pi_workflow_admission_reserved", "a1"), trialCatalogEvent(t, "pi_workflow_terminal", "a1"))
	lifecycle := trialTestTrace(admittedEvent, trialReleased(t, admitted, 1, "h1"))
	if err := trialPendingFrom(catalog, lifecycle, trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("settled state: %v", err)
	}
	if err := trialPendingFrom(trialTestTrace(), trialTestTrace(), trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("empty journals: %v", err)
	}
}

func TestTrialPendingFromRefusesAWorkflowWithoutTerminal(t *testing.T) {
	catalog := trialTestTrace(
		trialCatalogEvent(t, "pi_workflow_admission_reserved", "a1"), trialCatalogEvent(t, "pi_workflow_terminal", "a1"),
		trialCatalogEvent(t, "pi_workflow_admission_reserved", "a2"), trialCatalogEvent(t, "pi_workflow_fresh_authorized", "a2"))
	err := trialPendingFrom(catalog, trialTestTrace(), trialTestRoot, trialTestRegistration)
	if err == nil || !strings.Contains(err.Error(), "a2") || strings.Contains(err.Error(), "a1") {
		t.Fatalf("err = %v, want a refusal naming a2 only", err)
	}
}

func TestTrialPendingFromRefusesARunThatHoldsTheRoot(t *testing.T) {
	admittedEvent, admitted := trialAdmitted(t, trialTestRoot, "run-1")
	lifecycle := trialTestTrace(admittedEvent)
	err := trialPendingFrom(trialTestTrace(), lifecycle, trialTestRoot, trialTestRegistration)
	if err == nil || !strings.Contains(err.Error(), "run-1") {
		t.Fatalf("err = %v, want a refusal naming run-1", err)
	}
	// A run on another root is not this root's concern.
	other, _ := trialAdmitted(t, workflow.RootIdentity{Device: 7, Inode: 43}, "run-2")
	if err := trialPendingFrom(trialTestTrace(), trialTestTrace(other), trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("a run on another root: %v", err)
	}
	// Resolving it settles it.
	lifecycle = trialTestTrace(admittedEvent, trialReleased(t, admitted, 1, "h1"))
	if err := trialPendingFrom(trialTestTrace(), lifecycle, trialTestRoot, trialTestRegistration); err != nil {
		t.Fatalf("a released run: %v", err)
	}
}

func TestTrialPendingFromRefusesUnreadableRecords(t *testing.T) {
	bad := audit.Event{Kind: "pi_workflow_admission_reserved", ID: "x", Data: []byte("{")}
	if err := trialPendingFrom(trialTestTrace(bad), trialTestTrace(), trialTestRoot, trialTestRegistration); err == nil {
		t.Fatal("an unreadable catalog record was accepted")
	}
	badRun := audit.Event{Kind: "managed_live_root_run_admitted", ID: "x", Data: []byte("{}")}
	if err := trialPendingFrom(trialTestTrace(), trialTestTrace(badRun), trialTestRoot, trialTestRegistration); err == nil {
		t.Fatal("an unreadable lifecycle record was accepted")
	}
}

func TestCgroupPopulated(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "cgroup.events"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("populated 0\nfrozen 0\n")
	if got, err := cgroupPopulated(dir); err != nil || got {
		t.Fatalf("empty cgroup: %v, %v", got, err)
	}
	write("populated 1\nfrozen 0\n")
	if got, err := cgroupPopulated(dir); err != nil || !got {
		t.Fatalf("populated cgroup: %v, %v", got, err)
	}
	for _, bad := range []string{"", "frozen 0\n", "populated 2\n", "populated x\n"} {
		write(bad)
		if _, err := cgroupPopulated(dir); err == nil {
			t.Errorf("cgroup.events %q was accepted", bad)
		}
	}
	if _, err := cgroupPopulated(filepath.Join(dir, "absent")); err == nil {
		t.Error("a missing cgroup was accepted")
	}
}

// fakeProc builds a /proc with one directory per pid.
func fakeProc(t *testing.T, procs map[int]string) string {
	t.Helper()
	root := t.TempDir()
	for pid, status := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "self"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func procStatus(state string, uids ...int) string {
	u := make([]string, len(uids))
	for i, id := range uids {
		u[i] = strconv.Itoa(id)
	}
	return "Name:\tx\nState:\t" + state + "\nUid:\t" + strings.Join(u, "\t") + "\nGid:\t1\t1\t1\t1\n"
}

// fakePin is a pinned process of a fake /proc: Signal removes its directory unless noKill.
type fakePin struct {
	root    string
	pid     int
	signals *[]int
	closed  *int
	// gone makes Alive and Signal report the process as reaped.
	gone bool
	// noKill leaves the process alive after a signal.
	noKill bool
	// exited makes the whole thread group exited from the start (a readable pidfd), as a
	// zombie leader without running threads is.
	exited bool
}

// shortKillWait bounds the wait for the kernel to confirm an exit, so a refusing test is fast.
func shortKillWait(t *testing.T) {
	t.Helper()
	old := trialKillWait
	trialKillWait = 20 * time.Millisecond
	t.Cleanup(func() { trialKillWait = old })
}

func (p *fakePin) Alive() error {
	if p.gone {
		return errProcGone
	}
	return nil
}

func (p *fakePin) Signal() error {
	if p.gone {
		return errProcGone
	}
	*p.signals = append(*p.signals, p.pid)
	if p.noKill {
		return nil
	}
	return os.RemoveAll(filepath.Join(p.root, strconv.Itoa(p.pid)))
}

// Exited is true once the fake process directory is gone (killed and reaped) or the fake was
// created exited.
func (p *fakePin) Exited(time.Duration) (bool, error) {
	if p.exited {
		return true, nil
	}
	_, err := os.Stat(filepath.Join(p.root, strconv.Itoa(p.pid)))
	return os.IsNotExist(err), nil
}

func (p *fakePin) Close() error { *p.closed++; return nil }

func TestKillUIDProcessesKillsOnlyLiveProcessesOfThatUID(t *testing.T) {
	shortKillWait(t)
	root := fakeProc(t, map[int]string{
		10: procStatus("S (sleeping)", 995, 995, 995, 995),
		11: procStatus("R (running)", 995, 995, 995, 995),
		12: procStatus("S (sleeping)", 1000, 1000, 1000, 1000),
		13: procStatus("Z (zombie)", 995, 995, 995, 995),
		14: procStatus("S (sleeping)", 0, 995, 995, 995),
		15: procStatus("S (sleeping)", 995, 0, 0, 0),
	})
	var signaled []int
	var closed int
	pin := func(pid int) (procPin, error) {
		// Pid 13 is a zombie leader without threads: its group exited, its pidfd is readable.
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed, exited: pid == 13}, nil
	}
	if err := killUIDProcesses(root, 995, pin); err != nil {
		t.Fatalf("killUIDProcesses: %v", err)
	}
	// A process whose real uid is the cell uid is killed; an exited group and another uid are not.
	want := map[int]bool{10: true, 11: true, 15: true}
	if len(signaled) != len(want) {
		t.Fatalf("signaled %v, want pids 10, 11 and 15", signaled)
	}
	for _, pid := range signaled {
		if !want[pid] {
			t.Fatalf("signaled %v, want pids 10, 11 and 15", signaled)
		}
	}
	for _, pid := range []int{12, 13, 14} {
		if _, err := os.Stat(filepath.Join(root, strconv.Itoa(pid))); err != nil {
			t.Errorf("pid %d was removed", pid)
		}
	}
	// Every opened pidfd is closed: 10, 11, 13 and 15 in the first scan, 13 again in the rescan.
	if closed != 5 {
		t.Errorf("closed %d pidfds, want 5", closed)
	}
}

func TestKillUIDProcessesFailsWhileAProcessSurvives(t *testing.T) {
	shortKillWait(t)
	root := fakeProc(t, map[int]string{10: procStatus("S (sleeping)", 995, 995, 995, 995)})
	var signaled []int
	var closed int
	pin := func(pid int) (procPin, error) {
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed, noKill: true}, nil
	}
	if err := killUIDProcesses(root, 995, pin); err == nil || !strings.Contains(err.Error(), "10") {
		t.Fatalf("err = %v, want a failure naming pid 10", err)
	}
}

func TestKillUIDProcessesNeverTargetsRoot(t *testing.T) {
	root := fakeProc(t, map[int]string{10: procStatus("S (sleeping)", 0, 0, 0, 0)})
	called := false
	pin := func(int) (procPin, error) { called = true; return nil, errors.New("must not be called") }
	if err := killUIDProcesses(root, 0, pin); err == nil || called {
		t.Fatalf("uid 0: err %v, pin called %v", err, called)
	}
}

// The scan saw pid 10 as a cell process. By the time the pidfd is opened the pid belongs to a
// root process: it is validated against the pinned process and must not be signaled.
func TestKillUIDProcessesDoesNotSignalAReusedPID(t *testing.T) {
	shortKillWait(t)
	root := fakeProc(t, map[int]string{10: procStatus("S (sleeping)", 995, 995, 995, 995)})
	var signaled []int
	var closed int
	pin := func(pid int) (procPin, error) {
		// The cell process exited and a root process reused the pid before the open.
		status := filepath.Join(root, strconv.Itoa(pid), "status")
		if err := os.WriteFile(status, []byte(procStatus("S (sleeping)", 0, 0, 0, 0)), 0o600); err != nil {
			t.Fatal(err)
		}
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed}, nil
	}
	if err := killUIDProcesses(root, 995, pin); err != nil {
		t.Fatalf("killUIDProcesses: %v", err)
	}
	if len(signaled) != 0 {
		t.Fatalf("signaled %v: a root process was signaled by a stale pid", signaled)
	}
	if closed != 1 {
		t.Fatalf("closed %d pidfds, want 1", closed)
	}
}

// The pidfd must still refer to a process after its uid was read: if it was reaped in
// between, the status read may belong to a reused pid and nothing is signaled.
func TestKillUIDProcessesDoesNotSignalAReapedPinnedProcess(t *testing.T) {
	shortKillWait(t)
	root := fakeProc(t, map[int]string{10: procStatus("S (sleeping)", 995, 995, 995, 995)})
	var signaled []int
	var closed int
	opens := 0
	pin := func(pid int) (procPin, error) {
		opens++
		if opens > 1 { // the rescan: the cell process is gone for good
			_ = os.RemoveAll(filepath.Join(root, strconv.Itoa(pid)))
			return nil, errProcGone
		}
		return &fakePin{root: root, pid: pid, signals: &signaled, closed: &closed, gone: true}, nil
	}
	if err := killUIDProcesses(root, 995, pin); err != nil {
		t.Fatalf("killUIDProcesses: %v", err)
	}
	if len(signaled) != 0 || closed != 1 {
		t.Fatalf("signaled %v, closed %d: the pinned process was gone", signaled, closed)
	}
}

// With no pidfd support the reset fails closed: no numeric-pid fallback signals anything.
func TestKillUIDProcessesFailsClosedWithoutPidfd(t *testing.T) {
	shortKillWait(t)
	root := fakeProc(t, map[int]string{10: procStatus("S (sleeping)", 995, 995, 995, 995)})
	for name, pinErr := range map[string]error{"enosys": syscall.ENOSYS, "eperm": syscall.EPERM, "other": errors.New("boom")} {
		pin := func(int) (procPin, error) { return nil, pinErr }
		if err := killUIDProcesses(root, 995, pin); err == nil {
			t.Fatalf("%s: a failed pidfd open did not refuse the reset", name)
		}
		if _, err := os.Stat(filepath.Join(root, "10")); err != nil {
			t.Fatalf("%s: the process directory vanished", name)
		}
	}
}

// The production pin signals through a pidfd and notices when its process is reaped.
func TestPidfdPinSignalsAndDetectsReaping(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	defer func() {
		if !reaped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	pin, err := pinProcessPidfd(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("pidfd_open on this kernel: %v", err)
	}
	defer pin.Close()
	if err := pin.Alive(); err != nil {
		t.Fatalf("a live process is not alive: %v", err)
	}
	if err := pin.Signal(); err != nil {
		t.Fatalf("signal: %v", err)
	}
	err = cmd.Wait()
	reaped = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != -1 {
		t.Fatalf("the process was not killed by a signal: %v", err)
	}
	if err := pin.Alive(); !errors.Is(err, errProcGone) {
		t.Fatalf("a reaped process is still alive: %v", err)
	}
	if err := pin.Signal(); !errors.Is(err, errProcGone) {
		t.Fatalf("signal after reaping: %v", err)
	}
}

// Nothing in the reset may signal a bare pid: a number can be reused between scan and kill.
func TestTrialResetNeverSignalsByNumericPID(t *testing.T) {
	src, err := os.ReadFile("trial_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "syscall.Kill(") || strings.Contains(string(src), "unix.Kill(") {
		t.Fatal("trial_linux.go signals a process by its numeric pid")
	}
}
