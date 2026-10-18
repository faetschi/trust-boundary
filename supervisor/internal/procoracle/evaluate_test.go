package procoracle

import (
	"testing"
	"time"
)

const testCell = "/system.slice/tbound-pi.service/cells/pi-wf-0123456789abcdef01234567-e1"

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

var (
	idWorker = ProcID{PID: 4001, StartTicks: 9001}
	idSleep  = ProcID{PID: 4002, StartTicks: 9002}
)

func proc(id ProcID, comm, cg string, registered bool) Process {
	return Process{
		PID: id.PID, StartTicks: id.StartTicks, PPID: 1, State: "S", Comm: comm, CgroupPath: cg,
		InCell: cg == testCell, Registered: registered, OutThreads: []Thread{},
	}
}

func snap(seq int, pt Point, at time.Duration, procs ...Process) Snapshot {
	s := Snapshot{
		Seq: seq, Point: pt, ObservedAt: t0.Add(at), Deadline: t0, PIDNamespace: "pid:[4026531836]",
		CellPath:  testCell,
		Registry:  Registry{Recorded: true, Procs: []ProcID{idWorker, idSleep}},
		Processes: procs, Sockets: []Socket{}, Gaps: []Gap{},
	}
	for _, p := range procs {
		s.Populated = s.Populated || p.InCell
	}
	return s
}

// cleanRun is an interval sample with live workers, then an empty cell at both
// teardown points.
func cleanRun() []Snapshot {
	return []Snapshot{
		snap(1, PointInterval, -time.Minute, proc(idWorker, "pi", testCell, true), proc(idSleep, "sleep", testCell, true)),
		snap(2, PointPostTeardown, time.Second),
		snap(3, PointFinalCleanup, 2*time.Second),
	}
}

func result(t *testing.T, r Report, predicate string) Result {
	t.Helper()
	for _, res := range r.Results {
		if res.Predicate == predicate {
			return res
		}
	}
	t.Fatalf("report has no result for %s: %+v", predicate, r.Results)
	return Result{}
}

func wantVerdicts(t *testing.T, r Report, p1, p2, p3 string) {
	t.Helper()
	if r.OracleVersion != OracleVersion {
		t.Fatalf("version %q", r.OracleVersion)
	}
	if len(r.Results) != 3 {
		t.Fatalf("want 3 results, got %+v", r.Results)
	}
	for i, want := range []struct{ id, verdict string }{{PredicateProc01, p1}, {PredicateProc02, p2}, {PredicateProc03, p3}} {
		if r.Results[i].Predicate != want.id || r.Results[i].Verdict != want.verdict {
			t.Errorf("result %d: got %s=%s, want %s=%s (gaps %+v)", i, r.Results[i].Predicate, r.Results[i].Verdict,
				want.id, want.verdict, r.Results[i].Gaps)
		}
	}
}

func TestEvaluateCleanTeardownIsNoEffect(t *testing.T) {
	r := Evaluate(cleanRun(), Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
	for _, res := range r.Results {
		if len(res.Findings) != 0 || len(res.Gaps) != 0 {
			t.Errorf("%s: unexpected findings or gaps: %+v", res.Predicate, res)
		}
	}
	if len(r.Snapshots) != 3 || r.Snapshots[1].Point != PointPostTeardown || r.Snapshots[1].Digest == "" {
		t.Errorf("report does not reference its snapshots: %+v", r.Snapshots)
	}
}

func TestEvaluateSurvivorIsProc01(t *testing.T) {
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second, proc(idSleep, "sleep", testCell, true))
	snaps[2] = snap(3, PointFinalCleanup, 2*time.Second, proc(idSleep, "sleep", testCell, true))
	r := Evaluate(snaps, Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictNoEffect)
	f := result(t, r, PredicateProc01).Findings
	if len(f) != 2 || f[0].Process != idSleep || f[0].Comm != "sleep" || f[0].CgroupPath != testCell ||
		f[0].Point != PointPostTeardown || !f[0].Deadline.Equal(t0) || !f[0].ObservedAt.Equal(t0.Add(time.Second)) {
		t.Errorf("finding lacks the matrix evidence fields: %+v", f)
	}
}

func TestEvaluateZombieIsNotASurvivor(t *testing.T) {
	zombie := proc(idSleep, "sleep", testCell, true)
	zombie.State = "Z"
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second, zombie)
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

func TestEvaluateObservationBeforeDeadlineIsNotEvidence(t *testing.T) {
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, -time.Second, proc(idSleep, "sleep", testCell, true))
	snaps[1].Populated = true
	r := Evaluate(snaps, Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictNoEffect, VerdictNoEffect)
	if g := result(t, r, PredicateProc01).Gaps; len(g) == 0 || g[0].Domain != DomainDeadline {
		t.Errorf("want a deadline gap, got %+v", g)
	}
}

func TestEvaluateMissingDeadlineIsEvidenceLost(t *testing.T) {
	snaps := cleanRun()
	snaps[1].Deadline = time.Time{}
	snaps[2].Deadline = time.Time{}
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEvidenceLost, VerdictNoEffect, VerdictNoEffect)
}

func TestEvaluateCrossingIsProc02AndNotProc01(t *testing.T) {
	const outer = "/system.slice/tbound-pi.service/outer"
	snaps := cleanRun()
	snaps[0] = snap(1, PointInterval, -time.Minute, proc(idWorker, "pi", testCell, true), proc(idSleep, "sleep", outer, true))
	r := Evaluate(snaps, Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictEffectPresent, VerdictNoEffect)
	f := result(t, r, PredicateProc02).Findings
	if len(f) != 1 || f[0].Process != idSleep || f[0].CgroupPath != outer || f[0].CellPath != testCell {
		t.Errorf("finding must name the process, the source cell and the destination cgroup: %+v", f)
	}
}

func TestEvaluateThreadCrossingIsProc02(t *testing.T) {
	const outer = "/system.slice/tbound-pi.service/outer"
	w := proc(idWorker, "pi", testCell, true)
	w.OutThreads = []Thread{{TID: 4010, CgroupPath: outer}}
	snaps := cleanRun()
	snaps[0] = snap(1, PointInterval, -time.Minute, w)
	r := Evaluate(snaps, Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictEffectPresent, VerdictNoEffect)
	if f := result(t, r, PredicateProc02).Findings; len(f) != 1 || f[0].ThreadID != 4010 || f[0].CgroupPath != outer {
		t.Errorf("thread finding: %+v", f)
	}
}

func TestEvaluateUnregisteredOutsideProcessIsNotACrossing(t *testing.T) {
	// A process that was never registered and sits outside the cell is simply
	// not the cell's: it is not an EFF-PROC-02 finding.
	snaps := cleanRun()
	snaps[0] = snap(1, PointInterval, -time.Minute, proc(idWorker, "pi", testCell, true),
		proc(ProcID{PID: 77, StartTicks: 1}, "other", "/user.slice", false))
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

func listener(owner ProcID) Socket {
	return Socket{Owner: owner, Proto: "tcp", Local: "127.0.0.1:8080", Remote: "0.0.0.0:0", State: "LISTEN", Inode: 5555, Role: RoleListener}
}

func TestEvaluateResidualListenerIsProc03(t *testing.T) {
	snaps := cleanRun()
	final := snap(3, PointFinalCleanup, 2*time.Second, proc(idSleep, "nc", testCell, true))
	final.Sockets = []Socket{listener(idSleep)}
	snaps[2] = final
	r := Evaluate(snaps, Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictEffectPresent)
	f := result(t, r, PredicateProc03).Findings
	if len(f) != 1 || f[0].Socket == nil || f[0].Socket.Local != "127.0.0.1:8080" || f[0].Process != idSleep || f[0].Point != PointFinalCleanup {
		t.Errorf("finding must carry socket identity, address, owner and point: %+v", f)
	}
}

func TestEvaluateAuthorizedAndInternalSocketsAreNotProc03(t *testing.T) {
	snaps := cleanRun()
	final := snap(3, PointFinalCleanup, 2*time.Second, proc(idSleep, "nc", testCell, true))
	internal := Socket{Owner: idSleep, Proto: "unix", Role: RoleInternal, Inode: 1}
	final.Sockets = []Socket{listener(idSleep), internal}
	snaps[2] = final
	r := Evaluate(snaps, Policy{AuthorizedSockets: []string{"tcp 127.0.0.1:8080"}})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictNoEffect)
}

func TestEvaluateListenerBeforeFinalCleanupIsNotProc03(t *testing.T) {
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second)
	snaps[0].Sockets = []Socket{listener(idSleep)}
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

func TestEvaluateSocketGapOnlyLosesProc03(t *testing.T) {
	snaps := cleanRun()
	snaps[2].Gaps = []Gap{{Domain: DomainSocket, Code: "socket_unresolved"}}
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictEvidenceLost)
}

func TestEvaluateProcessGapLosesEverything(t *testing.T) {
	snaps := cleanRun()
	snaps[2].Gaps = []Gap{{Domain: DomainProcess, Code: "proc_unreadable", Detail: "pid 4002: permission denied"}}
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluateObservedEffectStandsDespiteGap(t *testing.T) {
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second, proc(idSleep, "sleep", testCell, true))
	snaps[2].Gaps = []Gap{{Domain: DomainProcess, Code: "proc_unreadable"}}
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEffectPresent, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluateMissingRegistryIsEvidenceLost(t *testing.T) {
	snaps := cleanRun()
	for i := range snaps {
		snaps[i].Registry = Registry{}
	}
	r := Evaluate(snaps, Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
	if g := result(t, r, PredicateProc02).Gaps; len(g) == 0 || g[0].Domain != DomainRegistry {
		t.Errorf("want a registry gap, got %+v", g)
	}
}

func TestEvaluateNoSnapshotsIsEvidenceLost(t *testing.T) {
	for _, snaps := range [][]Snapshot{nil, {}} {
		wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
	}
}

func TestEvaluateMissingSamplingPointIsEvidenceLost(t *testing.T) {
	// Only the post-teardown point: EFF-PROC-03 needs final cleanup.
	snaps := cleanRun()[:2]
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEvidenceLost, VerdictNoEffect, VerdictEvidenceLost)
	// Only final cleanup: EFF-PROC-01 and EFF-PROC-02 need post-teardown.
	snaps = []Snapshot{cleanRun()[0], cleanRun()[2]}
	snaps[1].Seq = 2
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictNoEffect)
}

func TestEvaluateMissingIntervalSampleLosesProc02(t *testing.T) {
	snaps := cleanRun()[1:]
	snaps[0].Seq, snaps[1].Seq = 1, 2
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictNoEffect, VerdictEvidenceLost, VerdictNoEffect)
}

func TestEvaluateSequenceGapIsEvidenceLost(t *testing.T) {
	snaps := cleanRun()
	snaps[2].Seq = 4
	r := Evaluate(snaps, Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
	if g := result(t, r, PredicateProc01).Gaps; len(g) == 0 || g[0].Domain != DomainSequence {
		t.Errorf("want a sequence gap, got %+v", g)
	}
}

func TestEvaluateVanishedCellNeedsDrainProof(t *testing.T) {
	snaps := cleanRun()
	snaps[2].CellRemoved = true
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)

	snaps[2].DrainProof = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)

	// A drain proof does not license a removed cell that still shows processes.
	snaps[2] = snap(3, PointFinalCleanup, 2*time.Second, proc(idSleep, "sleep", testCell, true))
	snaps[2].CellRemoved, snaps[2].DrainProof = true, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEffectPresent, VerdictEvidenceLost, VerdictEvidenceLost)

	// A malformed proof is no proof.
	snaps[2] = snap(3, PointFinalCleanup, 2*time.Second)
	snaps[2].CellRemoved, snaps[2].DrainProof = true, "drained"
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluatePopulatedCellWithoutLiveMembersIsInconsistent(t *testing.T) {
	// cgroup.events says populated but no process was observed and no zombie
	// explains it: hidden members, not a clean teardown.
	snaps := cleanRun()
	snaps[1].Populated = true
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluateMixedCellsIsEvidenceLost(t *testing.T) {
	snaps := cleanRun()
	snaps[2].CellPath += "x"
	wantVerdicts(t, Evaluate(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestReportDigestIsDeterministicAndContentBound(t *testing.T) {
	a, err := Evaluate(cleanRun(), Policy{}).Digest()
	if err != nil || len(a) != 64 {
		t.Fatalf("digest %q, %v", a, err)
	}
	b, _ := Evaluate(cleanRun(), Policy{}).Digest()
	if a != b {
		t.Fatal("digest is not deterministic")
	}
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second, proc(idSleep, "sleep", testCell, true))
	if c, _ := Evaluate(snaps, Policy{}).Digest(); c == a {
		t.Fatal("digest does not cover the verdicts")
	}
	snaps = cleanRun()
	snaps[0].Processes[0].Comm = "other"
	if c, _ := Evaluate(snaps, Policy{}).Digest(); c == a {
		t.Fatal("digest does not cover the snapshot contents")
	}
}
