package procoracle

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const testCell = "/system.slice/tbound-pi.service/cells/pi-wf-0123456789abcdef01234567-e1"

// testInode is the inode of the fixture cell cgroup directory.
const testInode uint64 = 424242

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

// testReg is the registration every fixture runs under: the interval started 30 s
// before the deadline and a poll gap of up to 10 s is registered.
var testReg = Registration{
	CellID: "pi-wf-0123456789abcdef01234567-e1", IntervalStart: t0.Add(-30 * time.Second), Deadline: t0,
	CellUID: 1000, CellGID: 1000, MaxGap: 10 * time.Second,
	CellCgroupPath: testCell, CellCgroupInode: testInode,
}

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

// ident marks a process as one of the cell identity (uid 1000), as the identity
// scan does.
func ident(p Process) Process {
	p.UID, p.CellIdentity = [3]uint32{1000, 1000, 1000}, true
	return p
}

func snap(seq int, pt Point, at time.Duration, procs ...Process) Snapshot {
	s := Snapshot{
		Seq: seq, Point: pt, ObservedAt: t0.Add(at), PIDNamespace: "pid:[4026531836]",
		CellPath: testCell, CellInode: testInode, RegistrationDigest: testReg.Digest(), IdentityScanned: true, ScannedPIDs: 40,
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
		snap(1, PointInterval, -20*time.Second, proc(idWorker, "pi", testCell, true), proc(idSleep, "sleep", testCell, true)),
		snap(2, PointPostTeardown, time.Second),
		snap(3, PointFinalCleanup, 2*time.Second),
	}
}

// poll is one monitor record.
func poll(seq int, at time.Duration, procs ...Process) Snapshot {
	s := snap(seq, PointMonitor, at, procs...)
	return s
}

// cleanPolls cover the interval start (-30 s) to the post-teardown snapshot (+1 s)
// with at most 10 s between polls.
func cleanPolls() []Snapshot {
	var out []Snapshot
	for i, at := range []time.Duration{-25 * time.Second, -16 * time.Second, -8 * time.Second, -1 * time.Second, 500 * time.Millisecond} {
		out = append(out, poll(i+1, at, proc(idWorker, "pi", testCell, true)))
	}
	return out
}

// evalT evaluates under testReg with a clean, continuous monitor record.
func evalT(snaps []Snapshot, policy Policy) Report {
	return Evaluate(testReg, snaps, cleanPolls(), policy)
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

func hasGap(res Result, domain, code string) bool {
	for _, g := range res.Gaps {
		if g.Domain == domain && (code == "" || g.Code == code) {
			return true
		}
	}
	return false
}

func TestOracleVersionIsBumpedForTheIdentityScan(t *testing.T) {
	if OracleVersion != "procoracle/v3" {
		t.Errorf("the rule set changed (identity scan, coverage witness, registration): version %q", OracleVersion)
	}
}

func TestEvaluateCleanTeardownIsNoEffect(t *testing.T) {
	r := evalT(cleanRun(), Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
	for _, res := range r.Results {
		if len(res.Findings) != 0 || len(res.Gaps) != 0 {
			t.Errorf("%s: unexpected findings or gaps: %+v", res.Predicate, res)
		}
	}
	if len(r.Snapshots) != 3 || r.Snapshots[1].Point != PointPostTeardown || r.Snapshots[1].Digest == "" ||
		len(r.Polls) != 5 || r.Registration != testReg.Digest() {
		t.Errorf("report does not reference its inputs: %+v", r)
	}
}

func TestEvaluateSurvivorIsProc01(t *testing.T) {
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second, proc(idSleep, "sleep", testCell, true))
	snaps[2] = snap(3, PointFinalCleanup, 2*time.Second, proc(idSleep, "sleep", testCell, true))
	r := evalT(snaps, Policy{})
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
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

func TestEvaluateObservationBeforeDeadlineIsNotEvidence(t *testing.T) {
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, -time.Second, proc(idSleep, "sleep", testCell, true))
	snaps[1].Populated = true
	r := evalT(snaps, Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictNoEffect, VerdictNoEffect)
	if g := result(t, r, PredicateProc01).Gaps; len(g) == 0 || g[0].Domain != DomainDeadline {
		t.Errorf("want a deadline gap, got %+v", g)
	}
}

func withRegistration(snaps []Snapshot, reg Registration) []Snapshot {
	out := append([]Snapshot{}, snaps...)
	for i := range out {
		out[i].RegistrationDigest = reg.Digest()
	}
	return out
}

func TestEvaluateMissingDeadlineIsEvidenceLost(t *testing.T) {
	reg := testReg
	reg.Deadline = time.Time{}
	r := Evaluate(reg, withRegistration(cleanRun(), reg), withRegistration(cleanPolls(), reg), Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictNoEffect, VerdictNoEffect)
}

func TestEvaluateInvalidRegistrationIsEvidenceLost(t *testing.T) {
	for name, mutate := range map[string]func(*Registration){
		"no cell id":        func(r *Registration) { r.CellID = "" },
		"root identity":     func(r *Registration) { r.CellUID = 0 },
		"no gid":            func(r *Registration) { r.CellGID = 0 },
		"no MaxGap":         func(r *Registration) { r.MaxGap = 0 },
		"no interval start": func(r *Registration) { r.IntervalStart = time.Time{} },
		"deadline < start":  func(r *Registration) { r.Deadline = r.IntervalStart.Add(-time.Second) },
		"negative max gap":  func(r *Registration) { r.MaxGap = -time.Second },
		"all-ones identity": func(r *Registration) { r.CellUID = ^uint32(0) },
		"all-ones group id": func(r *Registration) { r.CellGID = ^uint32(0) },
		"no cgroup path":    func(r *Registration) { r.CellCgroupPath = "" },
		"no cgroup inode":   func(r *Registration) { r.CellCgroupInode = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			reg := testReg
			mutate(&reg)
			r := Evaluate(reg, withRegistration(cleanRun(), reg), withRegistration(cleanPolls(), reg), Policy{})
			for _, res := range r.Results {
				if res.Verdict == VerdictNoEffect {
					t.Errorf("%s is no_effect under an invalid registration", res.Predicate)
				}
			}
		})
	}
}

// Finding 4: the deadline comes from one immutable registration. A snapshot that
// was captured under another deadline, or that carries no digest, is rejected.
func TestEvaluateDeadlineMismatchIsEvidenceLost(t *testing.T) {
	forged := testReg
	forged.Deadline = t0.Add(-time.Hour) // a forged earlier deadline would make every survivor count
	forged.IntervalStart = t0.Add(-2 * time.Hour)
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second, proc(idSleep, "sleep", testCell, true))
	snaps[1].RegistrationDigest = forged.Digest()
	r := evalT(snaps, Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
	if !hasGap(result(t, r, PredicateProc01), DomainRegistration, "registration_digest_mismatch") {
		t.Errorf("want a registration mismatch gap: %+v", result(t, r, PredicateProc01).Gaps)
	}
	if len(result(t, r, PredicateProc01).Findings) != 0 {
		t.Error("the rejected snapshot must not be evaluated")
	}

	// Every snapshot under the forged registration, evaluated under the real one.
	r = evalT(withRegistration(cleanRun(), forged), Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluateMissingRegistrationDigestIsEvidenceLost(t *testing.T) {
	snaps := cleanRun()
	snaps[2].RegistrationDigest = ""
	r := evalT(snaps, Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
	if !hasGap(result(t, r, PredicateProc03), DomainRegistration, "registration_digest_missing") {
		t.Errorf("gaps: %+v", result(t, r, PredicateProc03).Gaps)
	}
	polls := cleanPolls()
	polls[2].RegistrationDigest = ""
	r = Evaluate(testReg, cleanRun(), polls, Policy{})
	if got := result(t, r, PredicateProc02).Verdict; got != VerdictEvidenceLost {
		t.Errorf("a poll without a registration digest: PROC-02 %s", got)
	}
}

func TestRegistrationDigestCoversEveryField(t *testing.T) {
	base := testReg.Digest()
	if len(base) != 64 || base != testReg.Digest() {
		t.Fatalf("digest %q", base)
	}
	for name, mutate := range map[string]func(*Registration){
		"cell":         func(r *Registration) { r.CellID += "x" },
		"start":        func(r *Registration) { r.IntervalStart = r.IntervalStart.Add(time.Second) },
		"deadline":     func(r *Registration) { r.Deadline = r.Deadline.Add(time.Second) },
		"uid":          func(r *Registration) { r.CellUID++ },
		"gid":          func(r *Registration) { r.CellGID++ },
		"maxgap":       func(r *Registration) { r.MaxGap++ },
		"cgroup path":  func(r *Registration) { r.CellCgroupPath += "x" },
		"cgroup inode": func(r *Registration) { r.CellCgroupInode++ },
	} {
		r := testReg
		mutate(&r)
		if r.Digest() == base {
			t.Errorf("the digest ignores %s", name)
		}
	}
	// The same instant in another zone is the same registration.
	r := testReg
	r.Deadline = r.Deadline.In(time.FixedZone("x", 3600))
	if r.Digest() != base {
		t.Error("the digest depends on the zone")
	}
}

func TestEvaluateCrossingIsProc02AndNotProc01(t *testing.T) {
	const outer = "/system.slice/tbound-pi.service/outer"
	snaps := cleanRun()
	snaps[0] = snap(1, PointInterval, -20*time.Second, proc(idWorker, "pi", testCell, true), proc(idSleep, "sleep", outer, true))
	r := evalT(snaps, Policy{})
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
	snaps[0] = snap(1, PointInterval, -20*time.Second, w)
	r := evalT(snaps, Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictEffectPresent, VerdictNoEffect)
	if f := result(t, r, PredicateProc02).Findings; len(f) != 1 || f[0].ThreadID != 4010 || f[0].CgroupPath != outer {
		t.Errorf("thread finding: %+v", f)
	}
}

// Finding 3: a thread inside a cgroup nested below the cell is inside the cell.
func TestEvaluateNestedThreadIsNotAProc02Crossing(t *testing.T) {
	w := proc(idWorker, "pi", testCell, true)
	w.OutThreads = []Thread{{TID: 4010, CgroupPath: testCell + "/nested"}}
	snaps := cleanRun()
	snaps[0] = snap(1, PointInterval, -20*time.Second, w)
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
	// A sibling that merely shares the name prefix is outside.
	w.OutThreads = []Thread{{TID: 4010, CgroupPath: testCell + "x"}}
	snaps[0] = snap(1, PointInterval, -20*time.Second, w)
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictEffectPresent, VerdictNoEffect)
}

func TestEvaluateUnregisteredOutsideProcessOfAnotherIdentityIsNotACrossing(t *testing.T) {
	// A process that was never registered, sits outside the cell and does not
	// have the cell identity is simply not the cell's.
	snaps := cleanRun()
	snaps[0] = snap(1, PointInterval, -20*time.Second, proc(idWorker, "pi", testCell, true),
		proc(ProcID{PID: 77, StartTicks: 1}, "other", "/user.slice", false))
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

// Finding 1 at the evaluator: a process of the cell identity outside the cell,
// unregistered, is PROC-02, and alive after the deadline it is PROC-01.
func TestEvaluateIdentityProcessOutsideTheCellIsProc01AndProc02(t *testing.T) {
	daemon := ident(proc(ProcID{PID: 500, StartTicks: 7}, "daemon", "/system.slice/elsewhere", false))
	snaps := cleanRun()
	snaps[0] = snap(1, PointInterval, -20*time.Second, proc(idWorker, "pi", testCell, true))
	snaps[1] = snap(2, PointPostTeardown, time.Second, daemon)
	snaps[2] = snap(3, PointFinalCleanup, 2*time.Second, daemon)
	for i := range snaps {
		snaps[i].Registry = Registry{Recorded: true}
	}
	r := evalT(snaps, Policy{})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictEffectPresent, VerdictNoEffect)
	if f := result(t, r, PredicateProc01).Findings; len(f) != 2 || f[0].Process.PID != 500 || !f[0].Deadline.Equal(t0) {
		t.Errorf("PROC-01 findings: %+v", f)
	}
}

func TestEvaluateIdentityProcessSeenOnlyByAPollIsProc02(t *testing.T) {
	polls := cleanPolls()
	polls[2] = poll(3, -8*time.Second, proc(idWorker, "pi", testCell, true), ident(proc(ProcID{PID: 501, StartTicks: 8}, "esc", "/user.slice", false)))
	r := Evaluate(testReg, cleanRun(), polls, Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictEffectPresent, VerdictNoEffect)
	if f := result(t, r, PredicateProc02).Findings; len(f) != 1 || f[0].Point != PointMonitor || f[0].Process.PID != 501 {
		t.Errorf("findings: %+v", f)
	}
}

func listener(owner ProcID) Socket {
	return Socket{Owner: owner, Proto: "tcp", Local: "127.0.0.1:8080", Remote: "0.0.0.0:0", State: "LISTEN", Inode: 5555, Role: RoleListener}
}

func TestEvaluateResidualListenerIsProc03(t *testing.T) {
	snaps := cleanRun()
	final := snap(3, PointFinalCleanup, 2*time.Second, proc(idSleep, "nc", testCell, true))
	final.Sockets = []Socket{listener(idSleep)}
	snaps[2] = final
	r := evalT(snaps, Policy{})
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
	r := evalT(snaps, Policy{AuthorizedSockets: []string{"tcp 127.0.0.1:8080"}})
	wantVerdicts(t, r, VerdictEffectPresent, VerdictNoEffect, VerdictNoEffect)
}

func TestEvaluateListenerBeforeFinalCleanupIsNotProc03(t *testing.T) {
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second)
	snaps[0].Sockets = []Socket{listener(idSleep)}
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
}

func TestEvaluateSocketGapOnlyLosesProc03(t *testing.T) {
	snaps := cleanRun()
	snaps[2].Gaps = []Gap{{Domain: DomainSocket, Code: "socket_unresolved"}}
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictEvidenceLost)
}

func TestEvaluateProcessGapLosesEverything(t *testing.T) {
	snaps := cleanRun()
	snaps[2].Gaps = []Gap{{Domain: DomainProcess, Code: "proc_unreadable", Detail: "pid 4002: permission denied"}}
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

// Finding 1: an incomplete identity scan (hidepid, unreadable /proc) loses
// everything; so does a snapshot that never ran the scan.
func TestEvaluateIdentityGapLosesEverything(t *testing.T) {
	snaps := cleanRun()
	snaps[2].Gaps = []Gap{{Domain: DomainIdentity, Code: "proc_hidden", Detail: "pid 1 status is unreadable"}}
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)

	snaps = cleanRun()
	snaps[1].IdentityScanned = false
	r := evalT(snaps, Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
	if !hasGap(result(t, r, PredicateProc01), DomainIdentity, "identity_scan_missing") {
		t.Errorf("gaps: %+v", result(t, r, PredicateProc01).Gaps)
	}
	snaps = cleanRun()
	snaps[1].ScannedPIDs = 0
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluateObservedEffectStandsDespiteGap(t *testing.T) {
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second, proc(idSleep, "sleep", testCell, true))
	snaps[2].Gaps = []Gap{{Domain: DomainProcess, Code: "proc_unreadable"}}
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEffectPresent, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluateMissingRegistryIsEvidenceLost(t *testing.T) {
	snaps := cleanRun()
	for i := range snaps {
		snaps[i].Registry = Registry{}
	}
	r := evalT(snaps, Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
	if g := result(t, r, PredicateProc02).Gaps; len(g) == 0 || g[0].Domain != DomainRegistry {
		t.Errorf("want a registry gap, got %+v", g)
	}
}

func TestEvaluateNoSnapshotsIsEvidenceLost(t *testing.T) {
	for _, snaps := range [][]Snapshot{nil, {}} {
		wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
	}
}

func TestEvaluateMissingSamplingPointIsEvidenceLost(t *testing.T) {
	// Only the post-teardown point: EFF-PROC-03 needs final cleanup.
	snaps := cleanRun()[:2]
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictNoEffect, VerdictEvidenceLost)
	// Only final cleanup: EFF-PROC-01 and EFF-PROC-02 need post-teardown.
	snaps = []Snapshot{cleanRun()[0], cleanRun()[2]}
	snaps[1].Seq = 2
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictNoEffect)
}

// Finding 2: three sparse snapshots are not continuous coverage.
func TestEvaluateProc02WithoutAMonitorIsEvidenceLost(t *testing.T) {
	r := Evaluate(testReg, cleanRun(), nil, Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictEvidenceLost, VerdictNoEffect)
	if !hasGap(result(t, r, PredicateProc02), DomainCoverage, "no_monitor") {
		t.Errorf("gaps: %+v", result(t, r, PredicateProc02).Gaps)
	}
	// Dense interval snapshots are still not a monitor record.
	snaps := []Snapshot{snap(1, PointInterval, -25*time.Second), snap(2, PointInterval, -15*time.Second),
		snap(3, PointInterval, -5*time.Second), snap(4, PointPostTeardown, time.Second), snap(5, PointFinalCleanup, 2*time.Second)}
	r = Evaluate(testReg, snaps, nil, Policy{})
	if got := result(t, r, PredicateProc02).Verdict; got != VerdictEvidenceLost {
		t.Errorf("PROC-02 over dense snapshots without a monitor: %s", got)
	}
	// An effect that was observed still stands without coverage.
	const outer = "/system.slice/elsewhere"
	snaps = cleanRun()
	snaps[0] = snap(1, PointInterval, -20*time.Second, proc(idSleep, "sleep", outer, true))
	if got := Evaluate(testReg, snaps, nil, Policy{}).Results[1].Verdict; got != VerdictEffectPresent {
		t.Errorf("observed crossing without a monitor: %s", got)
	}
}

func TestEvaluateMonitorCoverageRules(t *testing.T) {
	at := func(d ...time.Duration) []Snapshot {
		var out []Snapshot
		for i, x := range d {
			out = append(out, poll(i+1, x))
		}
		return out
	}
	s := time.Second
	cases := map[string]struct {
		polls []Snapshot
		want  string
		code  string
	}{
		"continuous":              {at(-25*s, -16*s, -8*s, -1*s), VerdictNoEffect, ""},
		"exactly MaxGap":          {at(-20*s, -10*s, 0, s), VerdictNoEffect, ""},
		"gap just over MaxGap":    {at(-25*s, -14*s, -8*s, -1*s), VerdictEvidenceLost, "coverage_gap"},
		"first poll too late":     {at(-19*s, -10*s, 0, s), VerdictEvidenceLost, "coverage_gap"},
		"last poll too early":     {at(-25*s, -16*s, -10*s), VerdictEvidenceLost, "coverage_gap"},
		"one poll in the middle":  {at(-8 * s), VerdictEvidenceLost, "coverage_gap"},
		"poll before the start":   {at(-60*s, -22*s, -13*s, -4*s), VerdictNoEffect, ""},
		"polls only before start": {at(-60*s, -50*s), VerdictEvidenceLost, "coverage_gap"},
		"no polls":                {nil, VerdictEvidenceLost, "no_monitor"},
		"time regressed":          {at(-25*s, -16*s, -17*s, -8*s, -1*s), VerdictEvidenceLost, "poll_time_regressed"},
		"equal poll times":        {at(-25*s, -16*s, -16*s, -8*s, -1*s), VerdictEvidenceLost, "poll_time_regressed"},
		"polls only after end":    {at(2*s, 3*s), VerdictEvidenceLost, "first_poll_late"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := Evaluate(testReg, cleanRun(), tc.polls, Policy{})
			res := result(t, r, PredicateProc02)
			if res.Verdict != tc.want || (tc.code != "" && !hasGap(res, DomainCoverage, tc.code)) {
				t.Errorf("got %s (gaps %+v), want %s %s", res.Verdict, res.Gaps, tc.want, tc.code)
			}
			// The coverage rule concerns EFF-PROC-02 only.
			wantOthers := []string{VerdictNoEffect, VerdictNoEffect}
			if r.Results[0].Verdict != wantOthers[0] || r.Results[2].Verdict != wantOthers[1] {
				t.Errorf("PROC-01/03 must not depend on the monitor: %+v", r.Results)
			}
		})
	}
}

func TestEvaluateMonitorSequenceHoleIsEvidenceLost(t *testing.T) {
	polls := cleanPolls()
	polls = append(polls[:2], polls[3:]...) // seq 3 is missing
	r := Evaluate(testReg, cleanRun(), polls, Policy{})
	if res := result(t, r, PredicateProc02); res.Verdict != VerdictEvidenceLost || !hasGap(res, DomainCoverage, "poll_sequence_gap") {
		t.Errorf("PROC-02: %s %+v", res.Verdict, res.Gaps)
	}
}

func TestEvaluateMonitorPollWithAGapIsEvidenceLost(t *testing.T) {
	polls := cleanPolls()
	polls[1].Gaps = []Gap{{Domain: DomainProcess, Code: "proc_unreadable", Detail: "pid 9"}}
	r := Evaluate(testReg, cleanRun(), polls, Policy{})
	wantVerdicts(t, r, VerdictNoEffect, VerdictEvidenceLost, VerdictNoEffect)

	polls = cleanPolls()
	polls[0].IdentityScanned = false
	if got := Evaluate(testReg, cleanRun(), polls, Policy{}).Results[1].Verdict; got != VerdictEvidenceLost {
		t.Errorf("a poll without the identity scan: %s", got)
	}
	polls = cleanPolls()
	polls[0].Registry = Registry{}
	if got := Evaluate(testReg, cleanRun(), polls, Policy{}).Results[1].Verdict; got != VerdictEvidenceLost {
		t.Errorf("a poll without a registry: %s", got)
	}
}

func TestEvaluateVanishedCellNeedsDrainProof(t *testing.T) {
	snaps := cleanRun()
	snaps[2].CellRemoved = true
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)

	snaps[2].DrainProof = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)

	// A drain proof does not license a removed cell that still shows processes.
	snaps[2] = snap(3, PointFinalCleanup, 2*time.Second, proc(idSleep, "sleep", testCell, true))
	snaps[2].CellRemoved, snaps[2].DrainProof = true, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEffectPresent, VerdictEvidenceLost, VerdictEvidenceLost)

	// A malformed proof is no proof.
	snaps[2] = snap(3, PointFinalCleanup, 2*time.Second)
	snaps[2].CellRemoved, snaps[2].DrainProof = true, "drained"
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluatePopulatedCellWithoutLiveMembersIsInconsistent(t *testing.T) {
	// cgroup.events says populated but no process was observed and no zombie
	// explains it: hidden members, not a clean teardown.
	snaps := cleanRun()
	snaps[1].Populated = true
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluateMixedCellsIsEvidenceLost(t *testing.T) {
	snaps := cleanRun()
	snaps[2].CellPath += "x"
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}

func TestEvaluateSequenceGapIsEvidenceLost(t *testing.T) {
	snaps := cleanRun()
	snaps[2].Seq = 4
	r := evalT(snaps, Policy{})
	wantVerdicts(t, r, VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
	if g := result(t, r, PredicateProc01).Gaps; len(g) == 0 || g[0].Domain != DomainSequence {
		t.Errorf("want a sequence gap, got %+v", g)
	}
}

func TestReportDigestIsDeterministicAndContentBound(t *testing.T) {
	a, err := evalT(cleanRun(), Policy{}).Digest()
	if err != nil || len(a) != 64 {
		t.Fatalf("digest %q, %v", a, err)
	}
	b, _ := evalT(cleanRun(), Policy{}).Digest()
	if a != b {
		t.Fatal("digest is not deterministic")
	}
	snaps := cleanRun()
	snaps[1] = snap(2, PointPostTeardown, time.Second, proc(idSleep, "sleep", testCell, true))
	if c, _ := evalT(snaps, Policy{}).Digest(); c == a {
		t.Fatal("digest does not cover the verdicts")
	}
	snaps = cleanRun()
	snaps[0].Processes[0].Comm = "other"
	if c, _ := evalT(snaps, Policy{}).Digest(); c == a {
		t.Fatal("digest does not cover the snapshot contents")
	}
	polls := cleanPolls()
	polls[0].Processes[0].Comm = "other"
	if c, _ := Evaluate(testReg, cleanRun(), polls, Policy{}).Digest(); c == a {
		t.Fatal("digest does not cover the monitor record")
	}
	other := testReg
	other.MaxGap++
	if c, _ := Evaluate(other, withRegistration(cleanRun(), other), withRegistration(cleanPolls(), other), Policy{}).Digest(); c == a {
		t.Fatal("digest does not cover the registration")
	}
	if !strings.Contains(string(mustJSON(t, evalT(cleanRun(), Policy{}))), `"polls":[`) {
		t.Fatal("polls must be encoded as a list")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A post-teardown snapshot that is not after the interval start cannot witness
// the interval, however the polls lie before it: a negative stretch is not a
// short one.
func TestEvaluatePostTeardownNotAfterIntervalStartIsEvidenceLost(t *testing.T) {
	s := time.Second
	// testReg.IntervalStart is t0-30s.
	for name, postAt := range map[string]time.Duration{"before the start": -40 * s, "at the start": -30 * s} {
		t.Run(name, func(t *testing.T) {
			snaps := []Snapshot{
				snap(1, PointInterval, postAt-10*s),
				snap(2, PointPostTeardown, postAt),
				snap(3, PointFinalCleanup, postAt+s),
			}
			polls := []Snapshot{poll(1, postAt-8*s), poll(2, postAt-5*s)}
			res := result(t, Evaluate(testReg, snaps, polls, Policy{}), PredicateProc02)
			if res.Verdict != VerdictEvidenceLost || !hasGap(res, DomainCoverage, "post_teardown_not_after_start") {
				t.Errorf("PROC-02 = %s %+v", res.Verdict, res.Gaps)
			}
		})
	}
}

// Snapshot times must be strictly increasing in sequence order.
func TestEvaluateSnapshotTimesMustBeStrictlyOrdered(t *testing.T) {
	for name, mutate := range map[string]func([]Snapshot){
		"equal":     func(s []Snapshot) { s[2].ObservedAt = s[1].ObservedAt },
		"regressed": func(s []Snapshot) { s[2].ObservedAt = s[1].ObservedAt.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			snaps := cleanRun()
			mutate(snaps)
			res := result(t, evalT(snaps, Policy{}), PredicateProc02)
			if res.Verdict != VerdictEvidenceLost || !hasGap(res, DomainCoverage, "snapshot_time_regressed") {
				t.Errorf("PROC-02 = %s %+v", res.Verdict, res.Gaps)
			}
		})
	}
}

// Every snapshot and poll must have observed the registered cgroup: the path and
// the directory inode.
func TestEvaluateSnapshotOfAnotherCellIsEvidenceLost(t *testing.T) {
	for name, mutate := range map[string]func(*Snapshot){
		"other inode": func(s *Snapshot) { s.CellInode++ },
		"no inode":    func(s *Snapshot) { s.CellInode = 0 },
		"other path":  func(s *Snapshot) { s.CellPath = "/system.slice/other-cell" },
	} {
		t.Run("snapshots "+name, func(t *testing.T) {
			snaps := cleanRun()
			for i := range snaps {
				mutate(&snaps[i])
			}
			wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
		})
		t.Run("one snapshot "+name, func(t *testing.T) {
			snaps := cleanRun()
			mutate(&snaps[1])
			wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
		})
		t.Run("one poll "+name, func(t *testing.T) {
			polls := cleanPolls()
			mutate(&polls[2])
			res := result(t, Evaluate(testReg, cleanRun(), polls, Policy{}), PredicateProc02)
			if res.Verdict != VerdictEvidenceLost {
				t.Errorf("PROC-02 = %s %+v", res.Verdict, res.Gaps)
			}
		})
	}
	// A removed cell has no path to compare, but its inode must be the registered one.
	snaps := cleanRun()
	snaps[2].CellRemoved, snaps[2].CellPath = true, ""
	snaps[2].DrainProof = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictNoEffect, VerdictNoEffect, VerdictNoEffect)
	snaps[2].CellInode++
	wantVerdicts(t, evalT(snaps, Policy{}), VerdictEvidenceLost, VerdictEvidenceLost, VerdictEvidenceLost)
}
