//go:build linux

package procoracle

import (
	"fmt"
	"slices"
	"testing"
)

// setNSpid rewrites a fixture process's status with an NSpid line (a kernel since 4.1 writes one).
func (f *fakeProc) setNSpid(pid int, comm, nspid string) {
	f.t.Helper()
	f.write(fmt.Sprintf("%d/status", pid), fmt.Sprintf("Name:\t%s\nUid:\t%d\t%d\t%d\t%d\nNSpid:\t%s\n", comm, fakeUID, fakeUID, fakeUID, fakeUID, nspid))
}

// A no-reap command cell as the capture sees it: the init in the detached scope, pid 1 of its namespace,
// started by a runtime monitor that is not of the cell identity.
func noReapHost(t *testing.T, withSleep bool) *fakeProc {
	t.Helper()
	f := newFakeProc(t)
	f.addProc(initPID, 9000, CellInitComm, "S", conmonPID, detachedScope)
	f.setNSpid(initPID, CellInitComm, fmt.Sprintf("%d\t1", initPID))
	if withSleep {
		f.addProc(5100, 9100, "sleep", "S", initPID, detachedScope)
		f.setNSpid(5100, "sleep", "5100\t3")
	}
	return f
}

func TestCaptureRecordsTheNSpidOfEveryProcess(t *testing.T) {
	f := noReapHost(t, true)
	s := Capture(cellOf(), f.view(), params(PointPostTeardown))
	if len(s.Gaps) != 0 {
		t.Fatalf("gaps: %v", gapDomains(s))
	}
	got := map[int][]int{}
	for _, p := range s.Processes {
		got[p.PID] = p.NSPid
	}
	if !slices.Equal(got[initPID], []int{initPID, 1}) || !slices.Equal(got[5100], []int{5100, 3}) {
		t.Fatalf("NSpid = %v", got)
	}
}

// A status without the line, or with a malformed one, never makes a process the cell init.
func TestCaptureNSpidAbsentIsNilAndMalformedIsAGap(t *testing.T) {
	f := newFakeProc(t)
	f.addProc(100, 5000, "pi", "S", 1, fakeCell)
	s := Capture(cellOf(100), f.view(), params(PointFinalCleanup))
	if len(s.Gaps) != 0 || len(s.Processes) != 1 || s.Processes[0].NSPid != nil {
		t.Fatalf("no NSpid line: %+v gaps %v", s.Processes, gapDomains(s))
	}
	for _, line := range []string{"NSpid:\t", "NSpid:\tx\t1", "NSpid:\t100\t0"} {
		f.write("100/status", fmt.Sprintf("Name:\tpi\nUid:\t%d\t%d\t%d\t%d\n%s\n", fakeUID, fakeUID, fakeUID, fakeUID, line))
		s := Capture(cellOf(100), f.view(), params(PointFinalCleanup))
		wantGapCode(t, s, DomainProcess, "status_malformed")
	}
}

// The whole path: /proc of a no-reap host, captured at the registered points, evaluated under the policy.
func TestCaptureThenEvaluateNoReapNullStimulusIsNoEffectForProc01(t *testing.T) {
	snaps := threePoints(noReapHost(t, false), cellOf())
	if r := evalT(snaps, Policy{LingeringInit: true}); result(t, r, PredicateProc01).Verdict != VerdictNoEffect {
		t.Fatalf("PROC-01 under no-reap = %+v", result(t, r, PredicateProc01))
	}
	if r := evalT(snaps, Policy{}); result(t, r, PredicateProc01).Verdict != VerdictEffectPresent {
		t.Fatalf("PROC-01 outside no-reap = %+v", result(t, r, PredicateProc01))
	}
}

func TestCaptureThenEvaluateNoReapDetachedSleepIsProc01(t *testing.T) {
	r := evalT(threePoints(noReapHost(t, true), cellOf()), Policy{LingeringInit: true})
	res := result(t, r, PredicateProc01)
	if res.Verdict != VerdictEffectPresent {
		t.Fatalf("PROC-01 = %+v", res)
	}
	for _, f := range res.Findings {
		if f.Process.PID != 5100 {
			t.Errorf("finding %+v is not the sleep", f)
		}
	}
}
