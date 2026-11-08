package procoracle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/runner"
)

// Review round 1 of D49: coverage through the endpoint, positive observations that survive defects, retention and
// binding of the termination record, and the whole-input stop request.

var escapedProc = Process{PID: 9, StartTicks: 1, PPID: 1, State: "S", Comm: "sleep", CgroupPath: "/user.slice", CellIdentity: true, UID: [3]uint32{1000, 1000, 1000}}

func TestCoverageHealthy(t *testing.T) {
	at := func(secs ...int) []Snapshot {
		var out []Snapshot
		for _, s := range secs {
			out = append(out, Snapshot{Seq: len(out) + 1, IdentityScanned: true, ScannedPIDs: 1, Registry: Registry{Recorded: true},
				ObservedAt: sshT0.Add(time.Duration(s) * time.Second)})
		}
		return out
	}
	if err := CoverageHealthy(at(1, 2, 3, 4), GuestMaxGap); err != nil {
		t.Fatal(err)
	}
	withGap := at(1, 2, 3)
	withGap[1].Gaps = []Gap{gapf(DomainProcess, "x", "y")}
	unscanned := at(1, 2, 3)
	unscanned[2].IdentityScanned = false
	for name, polls := range map[string][]Snapshot{"recorded scan gap": withGap, "no identity scan": unscanned, "nine second gap": at(1, 2, 3, 12), "regressed": at(1, 3, 2), "equal": at(1, 1), "none": nil} {
		if CoverageHealthy(polls, GuestMaxGap) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Polls at 1, 2, 3 and 12 s with the post-teardown snapshot at 3 s used to pass: the nine seconds after it were never
// checked. The record is judged through its last poll.
func TestEarlyCloseCoverageGapAfterPostTeardownIsNeverClean(t *testing.T) {
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), stopPollSecs: []int{1, 2, 3, 12}}
	c, _ := newEarlyCollector(g)
	begin(t, c)
	ev, err := runProc(t, c, closureRef)
	if err != nil || ev.Findings[runner.OracleProc02] != runner.FindingLost {
		t.Fatalf("a nine second gap after the post-teardown snapshot: %v %v", ev.Findings, err)
	}
	// A closure the guest could not give only leaves the early close unproven (D49 class a): the horizon record is whole,
	// so the negatives stand exactly as before D49. (Asserted lost before the two-class split.)
	g = &fakeGuest{cell: sshCell, release: make(chan struct{}), failClosure: true, pollProcs: nil}
	close(g.release)
	c, _ = newEarlyCollector(g)
	begin(t, c)
	if ev, err := runProc(t, c, closureRef); err != nil || ev.Findings[runner.OracleProc01] != runner.FindingNone {
		t.Fatalf("a failed closure must not lose a fully observed window: %v %v", ev.Findings, err)
	}
}

// Termination and coverage defects only invalidate negative claims: a poll that saw an escaped process still counts.
func TestEarlyClosePositiveObservationsSurviveDefects(t *testing.T) {
	g := &fakeGuest{cell: sshCell, release: make(chan struct{}), pollProcs: map[int][]Process{3: {escapedProc}},
		stopMutate: func(d *MonitorDoc) { d.Stop.RunID = "run-9" }} // invalid stop metadata
	c, _ := newEarlyCollector(g)
	begin(t, c)
	ev, err := runProc(t, c, closureRef)
	if err != nil || ev.Findings[runner.OracleProc02] != runner.FindingObserved {
		t.Fatalf("a valid poll with an escaped process was lost with bad stop metadata: %v %v", ev.Findings, err)
	}
	// An escaped process seen only by the closure scan refuses the closure and still enters the evaluation.
	g = &fakeGuest{cell: sshCell, release: make(chan struct{}), closureMutate: func(d *ClosureDoc) { d.Scan.Processes = []Process{escapedProc} }}
	close(g.release)
	c, _ = newEarlyCollector(g)
	begin(t, c)
	ev, err = runProc(t, c, closureRef)
	if err != nil || ev.Findings[runner.OracleProc02] != runner.FindingObserved {
		t.Fatalf("an escaped process seen by the closure scan never reached the evaluation: %v %v", ev.Findings, err)
	}
}

func retainedFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*"+RetainedSuffix))
	return m
}

func TestEarlyCloseRetainsAndBindsTheTerminationRecord(t *testing.T) {
	run := func(mutate func(*ClosureDoc)) (runner.ObserverEvidence, string) {
		dir := t.TempDir()
		g := &fakeGuest{cell: sshCell, release: make(chan struct{}), closureMutate: mutate}
		c, _ := newEarlyCollector(g)
		c.RetainDir = dir
		begin(t, c)
		ev, err := runProc(t, c, closureRef)
		if err != nil {
			t.Fatal(err)
		}
		files := retainedFiles(t, dir)
		if len(files) != 1 || filepath.Base(files[0]) != RetainedName(closureRef.Interval) {
			t.Fatalf("retained files = %v", files)
		}
		raw, _ := os.ReadFile(files[0])
		return ev, string(raw)
	}
	ev1, raw1 := run(nil)
	rec, err := ParseRetained([]byte(raw1))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Interval != closureRef.Interval || rec.RunID != "run-1" || !rec.Proven || rec.Closure == nil || rec.Closure.RunID != "run-1" ||
		rec.Stop.Reason != StopClosedOnProof || rec.Stop.ClosureDigest != rec.ClosureDigest || rec.PollCount != 13 || len(rec.Digest()) != 64 {
		t.Fatalf("retained record = %+v", rec)
	}
	// Another closure document gives another reference: the closure is bound into the oracle reference.
	ev2, _ := run(func(d *ClosureDoc) { d.ReceiptDigest = strings.Repeat("c", 64) })
	if ev1.Refs[runner.OracleProc01].Report == ev2.Refs[runner.OracleProc01].Report {
		t.Fatal("the PROC reference does not depend on the closure record")
	}
	// An interval is retained once, and a failing retention loses the evidence instead of dropping the record.
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	g := &fakeGuest{cell: sshCell, release: make(chan struct{})}
	c, _ := newEarlyCollector(g)
	c.RetainDir = file // a file, not a directory
	begin(t, c)
	if _, err := runProc(t, c, closureRef); !errors.Is(err, runner.ErrEvidenceAbsent) {
		t.Fatalf("retention failed and the evidence stood: %v", err)
	}
}

func TestReadMonitorStopTakesTheWholeBoundedInput(t *testing.T) {
	d := strings.Repeat("a", 64)
	good := MonitorStopLine(d, "run-1")
	if r, err := ReadMonitorStop(strings.NewReader(good)); err != nil || r.RunID != "run-1" {
		t.Fatalf("%+v %v", r, err)
	}
	for name, in := range map[string]string{"more text": good + "more\n", "second stop": good + good, "text first": "x\n" + good,
		"empty": "", "no newline": strings.TrimSuffix(good, "\n"), "too long": good + strings.Repeat("a", 600)} {
		if _, err := ReadMonitorStop(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

var _ = context.Background
