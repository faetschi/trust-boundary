//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
)

// D49 guest side. These tests are Linux-only (the package is) and were written on a host that cannot run them.

func closureEnvFor(t *testing.T, receipt productionPiReceipt, status string, commands ...piruntime.D09CommandReceipt) *evTestEnv {
	t.Helper()
	w, base := e04CompletedWorkflow(t)
	receipt.E04EvidenceHead, receipt.E04EvidenceRecordCount = base.E04EvidenceHead, base.E04EvidenceRecordCount
	inner := e04KindEnvWith(t, w, receipt, status, 1, nil)
	var events []audit.Event
	for i, r := range commands {
		data, err := json.Marshal(map[string]any{"run_id": e04TestRun, "receipt_digest": r.ReceiptDigest, "receipt": r})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, audit.Event{Kind: productionD09SettledKind, ID: "settled/" + string(rune('a'+i)), Data: data})
	}
	journal := evTestTrace(t, events...)
	env := newEvTestEnv(0)
	env.readTrace = func(path string, mustExist bool) (audit.Trace, error) {
		if path == filepath.Join(productionAuditRootDefault, e04TestRun+"-command-audit.jsonl") {
			if len(events) == 0 && mustExist {
				return audit.Trace{}, os.ErrNotExist
			}
			return journal, nil
		}
		return inner.readTrace(path, mustExist)
	}
	env.capture = func(c evidenceCell, reg procoracle.Registration, p procoracle.Point, seq int) (procoracle.Snapshot, error) {
		return procoracle.Snapshot{Seq: seq, Point: p, RegistrationDigest: reg.Digest(), CellPath: c.Hierarchy, CellInode: c.Inode,
			IdentityScanned: true, ScannedPIDs: 3, Registry: procoracle.Registry{Recorded: true}, ObservedAt: reg.Deadline.Add(time.Second)}, nil
	}
	return env
}

func settledRunReceipt(commands int) productionPiReceipt {
	return productionPiReceipt{Settlement: string(piruntime.SettlementStopped), ExitObserved: true, DescendantsSettled: true, CommandCellSettlements: commands}
}

func closureArgs(t *testing.T) []string {
	reg, start, deadline := evTestRegistration()
	return []string{"proc-closure", "--registration", reg.Digest(), "--interval-start", procoracle.InstantArg(start),
		"--deadline", procoracle.InstantArg(deadline), "--interval-ref", e04TestInterval}
}

func TestEvidenceProcClosureReadsTheReceiptSettlementAndAFreshScan(t *testing.T) {
	cmd := structurallyValidReceiptSnapshot(t)
	env := closureEnvFor(t, settledRunReceipt(1), "completed", cmd)
	out, err := runEvTest(t, env, closureArgs(t)...)
	if err != nil {
		t.Fatal(err)
	}
	var doc procoracle.ClosureDoc
	singleDoc(t, out, &doc)
	if doc.Kind != runner.EvidenceProcClosure || doc.RunID != e04TestRun || doc.IntervalRef != e04TestInterval || !doc.ReceiptVerified ||
		doc.ExpectedSettlements != 1 || len(doc.Commands) != 1 || !doc.Commands[0].OutputWritersDrained || !doc.Commands[0].CommandCgroupRemoved ||
		!doc.NetNSGone || doc.CellChildren != 0 || doc.Scan.Point != procoracle.PointInterval || doc.Scan.Seq != 1 {
		t.Fatalf("doc = %+v", doc)
	}
	// The structural snapshot names no network namespace, which the host's predicate refuses: no proof from this fixture.
	reg, _, _ := evTestRegistration()
	if v := procoracle.EvaluateClosure(reg, doc, e04TestInterval, e04TestRun); v.Proven {
		t.Fatal("a command without a network namespace inode was proven")
	}
	// A settlement the receipt counts but the journal does not hold is left for the host to refuse.
	env = closureEnvFor(t, settledRunReceipt(2), "completed", cmd)
	out, err = runEvTest(t, env, closureArgs(t)...)
	if err != nil {
		t.Fatal(err)
	}
	singleDoc(t, out, &doc)
	if v := procoracle.EvaluateClosure(reg, doc, e04TestInterval, e04TestRun); v.Proven || doc.ExpectedSettlements != 2 || len(doc.Commands) != 1 {
		t.Fatalf("a missing settlement was proven: %+v", v)
	}
}

func TestEvidenceProcClosureRefusesWhatIsNotACompletedSettledRun(t *testing.T) {
	cmd := structurallyValidReceiptSnapshot(t)
	unsettled := settledRunReceipt(1)
	unsettled.DescendantsSettled = false
	tampered := cmd
	tampered.Evidence.OutputWritersDrained = false // the receipt digest no longer matches
	for name, env := range map[string]*evTestEnv{
		"request not completed":   closureEnvFor(t, settledRunReceipt(1), "unknown", cmd),
		"run not settled":         closureEnvFor(t, unsettled, "completed", cmd),
		"tampered settlement":     closureEnvFor(t, settledRunReceipt(1), "completed", tampered),
		"no command journal":      closureEnvFor(t, settledRunReceipt(1), "completed"),
		"cells cgroup unlistable": nil,
	} {
		if env == nil {
			env = closureEnvFor(t, settledRunReceipt(1), "completed", cmd)
			env.cellChildren = func(evidenceCell) (int, error) { return 0, os.ErrPermission }
		}
		if out, err := runEvTest(t, env, closureArgs(t)...); err == nil || out != "" {
			t.Errorf("%s: err = %v, stdout %q", name, err, out)
		}
	}
	// A namespace lookup that fails is an error, not "not held". The structural snapshot names no namespace, so the
	// lookup is asked about nothing and the document is still produced (the host refuses the missing inode).
	cmdEnv := closureEnvFor(t, settledRunReceipt(1), "completed", cmd)
	cmdEnv.netnsHeld = func(inodes []uint64) (bool, error) {
		if len(inodes) != 0 {
			t.Errorf("asked about inodes %v", inodes)
		}
		return false, os.ErrPermission
	}
	if _, err := runEvTest(t, cmdEnv, closureArgs(t)...); err == nil {
		t.Error("a failing namespace lookup was not an error")
	}
	if out, err := runEvTest(t, closureEnvFor(t, settledRunReceipt(0), "completed"), append(closureArgs(t)[:7:7], "--interval-ref", "other|trajectory|C|0#1")...); err == nil || out != "" {
		t.Errorf("another interval: err = %v", err)
	}
}

func TestNetnsHeldInSeesProcessesThreadsAndMounts(t *testing.T) {
	proc := t.TempDir()
	mk := func(pid, tid, link string) {
		dir := filepath.Join(proc, pid, "task", tid, "ns")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(link, filepath.Join(dir, "net")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(proc, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "self/mountinfo"), []byte("36 35 0:4 net:[4026531840] /run/x rw - nsfs nsfs rw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk("1", "1", "net:[4026531840]")
	mk("200", "200", "net:[4026532100]")
	mk("200", "201", "net:[4026532999]") // a thread in another namespace than its process
	if held, err := netnsHeldIn(proc, []uint64{4026532001}); err != nil || held {
		t.Errorf("an unheld namespace: held=%v err=%v", held, err)
	}
	for name, ino := range map[string]uint64{"a process": 4026532100, "a thread": 4026532999, "a mount": 4026531840} {
		if held, err := netnsHeldIn(proc, []uint64{4026532001, ino}); err != nil || !held {
			t.Errorf("%s: held=%v err=%v", name, held, err)
		}
	}
	if _, err := netnsHeldIn(t.TempDir(), []uint64{1}); err == nil {
		t.Error("an unreadable /proc was read as not held")
	}
}

func TestEvidenceProcMonitorEndsOnlyOnAWellFormedStopLine(t *testing.T) {
	reg, start, deadline := evTestRegistration()
	args := []string{"proc-monitor", "--registration", reg.Digest(), "--interval-start", procoracle.InstantArg(start),
		"--deadline", procoracle.InstantArg(deadline), "--duration", "30", "--interval", "500"}
	digest := strings.Repeat("a", 64)
	for name, tc := range map[string]struct {
		stdin string
		want  string
	}{
		"stop line":           {procoracle.MonitorStopLine(digest, "run-1"), procoracle.StopClosedOnProof},
		"end of input":        {"", procoracle.StopHorizon},
		"malformed":           {"stop closed-on-proof abc run-1\n", procoracle.StopHorizon},
		"no newline":          {"stop closed-on-proof " + digest + " run-1", procoracle.StopHorizon},
		"another reason":      {"stop horizon " + digest + " run-1\n", procoracle.StopHorizon},
		"unrelated input":     {"hello\n", procoracle.StopHorizon},
		"stop after garbage":  {"hello\n" + procoracle.MonitorStopLine(digest, "run-1"), procoracle.StopHorizon},
		"stop plus more text": {procoracle.MonitorStopLine(digest, "run-1") + "more\n", procoracle.StopClosedOnProof},
	} {
		env := newEvTestEnv(0)
		env.awaitStop = true
		env.stdin = strings.NewReader(tc.stdin)
		out, err := runEvTest(t, env, args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		lines := strings.SplitAfter(out, "\n")
		var mon procoracle.MonitorDoc
		if err := runner.DecodeEvidenceDoc([]byte(lines[1]), &mon); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if mon.Stop.Reason != tc.want || (tc.want == procoracle.StopClosedOnProof && (mon.Stop.ClosureDigest != digest || mon.Stop.RunID != "run-1")) ||
			(tc.want == procoracle.StopHorizon && (mon.Stop.ClosureDigest != "" || mon.Stop.RunID != "")) ||
			!mon.Stop.FirstPollAt.Equal(mon.Polls[0].ObservedAt) || !mon.Stop.LastPollAt.Equal(mon.Polls[len(mon.Polls)-1].ObservedAt) ||
			mon.Stop.StoppedAt.Before(mon.Stop.LastPollAt) {
			t.Errorf("%s: stop = %+v, want %s", name, mon.Stop, tc.want)
		}
	}
}

func TestEvidenceProcMonitorSignalIsAnErrorNotAShortRecord(t *testing.T) {
	// monitorEvidenceCell itself needs a cgroup; the contract is on the seam: a monitor that returns an error yields no document.
	reg, start, deadline := evTestRegistration()
	env := newEvTestEnv(0)
	env.monitor = func(context.Context, evidenceCell, procoracle.Registration, time.Duration, time.Duration, func(procoracle.Snapshot),
		<-chan procoracle.MonitorStopRequest) ([]procoracle.Snapshot, *procoracle.MonitorStopRequest, error) {
		return nil, nil, context.Canceled
	}
	args := []string{"proc-monitor", "--registration", reg.Digest(), "--interval-start", procoracle.InstantArg(start),
		"--deadline", procoracle.InstantArg(deadline), "--duration", "30", "--interval", "500"}
	if out, err := runEvTest(t, env, args...); err == nil || strings.Contains(out, "proc-monitor\"") {
		t.Fatalf("err = %v, stdout %q", err, out)
	}
}
