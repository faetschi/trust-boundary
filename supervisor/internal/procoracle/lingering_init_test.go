package procoracle

import (
	"testing"
	"time"
)

// no-reap/v1 leaves the command cell's init (tbound-cell-entrypoint, pid 1 of the cell's PID namespace)
// running after the command exits, on purpose: its exit would end the namespace and kill every survivor
// the delta is meant to leave. That init is not a surviving descendant. A Policy that says the run is a
// no-reap one (LingeringInit) excludes exactly the init, identified by what the kernel and the process
// tree say and not by its name alone: pid 1 of a PID namespace below the observer's, named like the
// entrypoint, and the root of the observed process tree (its parent is not an observed process).
// Everything that descends from the init still counts.

const (
	initPID       = 5000
	conmonPID     = 777 // the runtime monitor, outside the cell identity: never in the snapshot
	detachedScope = "/system.slice/tbound-pi.service/tbound-nr-0123456789abcdef"
)

// initProc is a command cell's init as the host sees it: a cell-identity process in a detached scope, pid
// 1 in its own namespace, started by the runtime monitor.
func initProc(pid int) Process {
	p := ident(proc(ProcID{PID: pid, StartTicks: uint64(10000 + pid)}, CellInitComm, detachedScope, false))
	p.PPID, p.NSPid = conmonPID, []int{pid, 1}
	return p
}

// descendant is a process the command started, in the init's namespace.
func descendant(pid, ppid int, comm string) Process {
	p := ident(proc(ProcID{PID: pid, StartTicks: uint64(10000 + pid)}, comm, detachedScope, false))
	p.PPID, p.NSPid = ppid, []int{pid, 3}
	return p
}

func noReapRun(procs ...Process) []Snapshot {
	return []Snapshot{
		snap(1, PointInterval, -20*time.Second, procs...),
		snap(2, PointPostTeardown, time.Second, procs...),
		snap(3, PointFinalCleanup, 2*time.Second, procs...),
	}
}

func proc01(t *testing.T, r Report) Result { return result(t, r, PredicateProc01) }

// A null stimulus under no-reap (the command exits at once) leaves only the init.
func TestNoReapNullStimulusLeavesOnlyTheInitAndIsNoEffect(t *testing.T) {
	r := evalT(noReapRun(initProc(initPID)), Policy{LingeringInit: true})
	if res := proc01(t, r); res.Verdict != VerdictNoEffect || len(res.Findings) != 0 {
		t.Fatalf("PROC-01 = %+v, want no_effect with no finding", res)
	}
	if !r.LingeringInit {
		t.Error("the report does not say that it ran under a lingering-init policy")
	}
}

// A detached descendant of the init survives the deadline: that is the effect the delta exists to show.
func TestNoReapDetachedSleepIsStillProc01(t *testing.T) {
	r := evalT(noReapRun(initProc(initPID), descendant(5100, initPID, "sleep")), Policy{LingeringInit: true})
	res := proc01(t, r)
	if res.Verdict != VerdictEffectPresent || len(res.Findings) != 2 {
		t.Fatalf("PROC-01 = %+v, want effect_present with the sleep at both points", res)
	}
	for _, f := range res.Findings {
		if f.Process.PID != 5100 || f.Comm != "sleep" {
			t.Errorf("finding %+v is not the sleep", f)
		}
	}
}

// Two commands leave two cells and two inits.
func TestNoReapExcludesEveryCellInit(t *testing.T) {
	r := evalT(noReapRun(initProc(initPID), initProc(initPID+10)), Policy{LingeringInit: true})
	if res := proc01(t, r); res.Verdict != VerdictNoEffect {
		t.Fatalf("PROC-01 = %+v", res)
	}
}

// Without the policy the run is not a no-reap one, and a lingering init is a process that outlived the teardown.
func TestLingeringInitCountsWithoutTheNoReapPolicy(t *testing.T) {
	r := evalT(noReapRun(initProc(initPID)), Policy{})
	res := proc01(t, r)
	if res.Verdict != VerdictEffectPresent || res.Findings[0].Process.PID != initPID {
		t.Fatalf("PROC-01 = %+v, want the init as a finding", res)
	}
	if r.LingeringInit {
		t.Error("the report claims a lingering-init policy")
	}
}

// What the exclusion does not cover, however it is named.
func TestOnlyTheCellInitIsExcludedUnderNoReap(t *testing.T) {
	cases := map[string]func(Process) Process{
		"another name": func(p Process) Process { p.Comm = "sh"; return p },
		"no namespace record": func(p Process) Process {
			p.NSPid = nil
			return p
		},
		"host-level process named like the entrypoint": func(p Process) Process {
			p.NSPid = []int{p.PID}
			return p
		},
		"not pid 1 in its namespace": func(p Process) Process {
			p.NSPid = []int{p.PID, 5}
			return p
		},
		// A nested PID namespace made by a process of the cell: its init is pid 1 too, and may carry
		// the entrypoint's name, but its parent is a process of the cell.
		"init of a namespace a cell process made": func(p Process) Process {
			p.PPID, p.NSPid = 5100, []int{p.PID, 1, 1}
			return p
		},
	}
	for name, mutate := range cases {
		lookalike := mutate(initProc(initPID + 200))
		r := evalT(noReapRun(initProc(initPID), descendant(5100, initPID, "bash"), lookalike), Policy{LingeringInit: true})
		res := proc01(t, r)
		if res.Verdict != VerdictEffectPresent {
			t.Errorf("%s: PROC-01 = %s, want effect_present", name, res.Verdict)
			continue
		}
		for _, f := range res.Findings {
			if f.Process.PID == initPID {
				t.Errorf("%s: the real init is a finding", name)
			}
		}
	}
}

// The init of a namespace that a surviving descendant created, with the entrypoint's name and a parent that
// is itself observed, is a descendant and not the cell init.
func TestSurvivorThatImpersonatesTheInitIsStillCounted(t *testing.T) {
	fake := initProc(6000)
	fake.PPID, fake.NSPid = 5100, []int{6000, 1, 1}
	r := evalT(noReapRun(initProc(initPID), descendant(5100, initPID, "bash"), fake), Policy{LingeringInit: true})
	var found bool
	for _, f := range proc01(t, r).Findings {
		found = found || f.Process.PID == 6000
	}
	if !found {
		t.Fatalf("the impersonating process is not a PROC-01 finding: %+v", proc01(t, r))
	}
}

// The policy changes nothing about a run that left nothing behind, except that the report names it.
func TestPolicyDoesNotChangeTheReportOfAnOtherwiseCleanRun(t *testing.T) {
	a := evalT(cleanRun(), Policy{})
	b := evalT(cleanRun(), Policy{LingeringInit: true})
	if da, _ := a.Digest(); da == "" {
		t.Fatal("no digest")
	}
	for i := range a.Results {
		if a.Results[i].Verdict != b.Results[i].Verdict {
			t.Errorf("%s: %s without the policy, %s with it", a.Results[i].Predicate, a.Results[i].Verdict, b.Results[i].Verdict)
		}
	}
	da, _ := a.Digest()
	db, _ := b.Digest()
	if da == db {
		t.Error("the report digest does not bind the policy")
	}
}
