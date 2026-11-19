//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/runend"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/wsoracle"
)

// The launch chains the rehearsals measured (F23 m3, F28), built the way the launcher builds them: the site's message
// with its sentinel and its syscall cause both wrapped with %w (containment_launch_linux.go d09ExitedBeforeAdmission;
// TestD09ExitedBeforeAdmissionReportsPodmanExitStatus pins that both stay in the real chain), the launcher's phase
// suffix, the joined nil cleanup, and the settled typed launch error that settledFailedLaunch returns.
func preAdmissionLaunchChains() map[string]error {
	settled := piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true}
	launch := func(site error) error {
		withPhases := fmt.Errorf("%w (%s)", site, "phases_ms[podman_started=4399 pidfd_pinned=4543 map_check_start=5210]")
		return productionLaunchFailure(&piruntime.PiWorkerLaunchError{Cause: errors.Join(withPhases, nil, nil), Settlement: settled}, nil)
	}
	gone := &os.PathError{Op: "open", Path: "/proc/663609/uid_map", Err: syscall.ENOENT}
	denied := &os.PathError{Op: "stat", Path: "/proc/663609/ns/user", Err: syscall.EACCES}
	return map[string]error{
		"F23 m3 cell exited, final state read": launch(fmt.Errorf("%w: podman_exit=%s status=%q exit_code=%d oom_killed=%t error=%q: %w",
			piruntime.ErrD09WorkerExitedBeforeAdmission, "running(after 3s)", "exited", 0, false, "", gone)),
		"F23 m3 cell exited, state unavailable": launch(fmt.Errorf("%w: podman_exit=%s (final container state unavailable: %s): %w",
			piruntime.ErrD09WorkerExitedBeforeAdmission, "137", "no such container", gone)),
		"F28 uid/gid map permission denied": launch(fmt.Errorf("verify exact host-visible D09 uid/gid map before launch admission: %w", denied)),
	}
}

// R1 of the #209 verification: a launch that failed before the task was admitted, with its cell settled and its
// evidence recorded (launchNotAdmitted), is an infrastructure end the host may rerun, whatever its leaf. The same
// chain without that proof, or beside any failure the registry does not explain, still ends unclassified.
func TestPreAdmissionLaunchFailureAnswersLaunchNotAdmitted(t *testing.T) {
	for name, chain := range preAdmissionLaunchChains() {
		if answer := unknownAnswerOf(t, launchNotAdmitted{err: chain}); answer.ErrorCode != runend.CodeLaunchNotAdmitted {
			t.Errorf("%s: answer %q, want %q; chain %v", name, answer.ErrorCode, runend.CodeLaunchNotAdmitted, chain)
		}
		for what, runErr := range map[string]error{
			"no proof":                   chain,
			"beside a cleanup failure":   errors.Join(launchNotAdmitted{err: chain}, errors.New("close session before receipt: synthetic")),
			"beside an unsettled close":  errors.Join(launchNotAdmitted{err: chain}, ErrProductionLifecycleUnsettled),
			"beside a ceiling":           errors.Join(launchNotAdmitted{err: chain}, runend.ErrCeiling),
			"inside an unexplained wrap": fmt.Errorf("%w: %w", launchNotAdmitted{err: chain}, errors.New("synthetic")),
		} {
			want := runend.CodeUnclassified
			if what == "beside a ceiling" {
				want = runend.CodeBudgetEnded
			}
			if answer := unknownAnswerOf(t, runErr); answer.ErrorCode != want {
				t.Errorf("%s, %s: answer %q, want %q", name, what, answer.ErrorCode, want)
			}
		}
	}
}

// launchFailedSession is a fresh run whose launch failed before task admission: the real E04 recorder with the two
// binder points taken, an empty provider journal, and the binders' closer order (final-cleanup, then the store).
func launchFailedSession(t *testing.T, launchErr error) (*productionPiSession, *e04FakeWorkflow) {
	t.Helper()
	w := newE04FakeWorkflow(t)
	providerJournal, err := audit.Open(filepath.Join(incrementalTestRoot(t), "provider-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = providerJournal.Close() })
	session := &productionPiSession{E04: w.rec, Lifecycle: &productionLifecycleEvidence{}, RunID: e04TestRun,
		ProviderJournal: providerJournal}
	session.Close = func() error { return errors.Join(w.rec.finalCleanup(), w.rec.close()) }
	session.recordE04(wsoracle.PointBaseline)
	session.recordE04(wsoracle.PointPostMaterialization)
	session.Lifecycle.markWorkerLaunchAttempted()
	session.Lifecycle.markLaunchFailed(launchErr)
	return session, w
}

func settledLaunchError() *piruntime.PiWorkerLaunchError {
	return &piruntime.PiWorkerLaunchError{Cause: errors.New("D09 worker cell exited before launch admission"),
		Settlement: piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true}}
}

// A settled launch failure before task admission writes a non-success receipt that binds all seven E04 points with the
// empty ledger, the host's evidence kind serves them, they judge as no effect, and the service answers
// launch-not-admitted. That is the evidence the host's rerun rule needs for a C trajectory without an agent turn.
func TestLaunchBeforeAdmissionWritesAReceiptWhoseE04PointsShowNoEffect(t *testing.T) {
	launchErr := settledLaunchError()
	session, w := launchFailedSession(t, launchErr)
	var output bytes.Buffer
	runErr := session.endLaunchBeforeAdmission(&output, productionServeConfig{}, launchErr, productionLaunchFailure(launchErr, nil), true)
	if answer := unknownAnswerOf(t, runErr); answer.ErrorCode != runend.CodeLaunchNotAdmitted {
		t.Fatalf("answer %q, want %q; run error %v", answer.ErrorCode, runend.CodeLaunchNotAdmitted, runErr)
	}
	var receipt productionPiReceipt
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatalf("receipt %q: %v", output.String(), err)
	}
	if receipt.Success || receipt.WorkerReady || receipt.TaskAdmitted || receipt.TurnCompleted || receipt.RunID != e04TestRun ||
		receipt.Settlement != string(piruntime.SettlementStopped) || !receipt.ExitObserved || !receipt.DescendantsSettled ||
		receipt.E04EvidenceRecordCount != 7 || receipt.E04GapCount != 0 {
		t.Fatalf("receipt = %+v", receipt)
	}
	records := w.trace().Records
	var points []string
	for _, record := range records {
		points = append(points, e04Decode(t, record).Point)
	}
	e04Equal(t, "recorded points", points, []string{wsoracle.PointBaseline, wsoracle.PointPostMaterialization,
		wsoracle.PointPostSettlement, wsoracle.PointPostTeardown, wsoracle.PointPrePublication, wsoracle.PointPostPublication,
		wsoracle.PointFinalCleanup})
	if got := e04Decode(t, records[5]); got.LedgerBasis != runner.E04LedgerNone {
		t.Fatalf("post-publication record = %+v, want the empty ledger", got)
	}
	doc, err := evidenceE04Points(e04KindEnvWith(t, w, receipt, "unknown", 1, nil), e04TestInterval)
	if err != nil {
		t.Fatalf("the evidence kind refuses the UNKNOWN terminal's receipt: %v", err)
	}
	in, err := runner.E04PointsInput(doc, e04TestInterval, receipt.E04EvidenceHead, receipt.E04EvidenceRecordCount)
	if err != nil {
		t.Fatal(err)
	}
	in.ThreatClass = wsoracle.ClassTMA
	report := wsoracle.EvaluateE04(in)
	for _, predicate := range []string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS04} {
		if got := report.VerdictOf(predicate); got != wsoracle.VerdictNoEffect {
			t.Errorf("%s = %s, want no_effect (gaps %+v findings %+v)", predicate, got, report.Gaps, report.Findings)
		}
	}
}

// Nothing after task admission, and nothing the lifecycle does not prove settled, takes the path: no point is taken,
// no receipt is written, and the plain failure is returned, which ends unclassified.
func TestOnlyASettledFreshLaunchBeforeAdmissionTakesThePath(t *testing.T) {
	unsettled := settledLaunchError()
	unsettled.Settlement.DescendantsSettled = false
	probe := settledLaunchError()
	probe.RouteProbe = &piruntime.D09RouteProbeLaunch{}
	for name, tc := range map[string]struct {
		launchErr error
		edit      func(*productionPiSession)
	}{
		"task admitted":          {settledLaunchError(), func(s *productionPiSession) { s.Lifecycle.markTaskAdmitted() }},
		"worker launched":        {settledLaunchError(), func(s *productionPiSession) { s.Lifecycle.markWorkerLaunched(42) }},
		"channels started":       {settledLaunchError(), func(s *productionPiSession) { s.Lifecycle.markChannelsStarted() }},
		"turn completed":         {settledLaunchError(), func(s *productionPiSession) { s.Lifecycle.markTurnCompleted() }},
		"recovered workflow":     {settledLaunchError(), func(s *productionPiSession) { s.recoveryPrompt = "resume" }},
		"cell not settled":       {unsettled, func(*productionPiSession) {}},
		"untyped launch failure": {errors.New("start D09 rootless Podman/crun cell: exec: no such file"), func(*productionPiSession) {}},
		"route probe":            {probe, func(*productionPiSession) {}},
	} {
		session, w := launchFailedSession(t, tc.launchErr)
		tc.edit(session)
		failure := fmt.Errorf("launch admitted production Pi worker: %w", tc.launchErr)
		var output bytes.Buffer
		if runErr := session.endLaunchBeforeAdmission(&output, productionServeConfig{}, tc.launchErr, failure, true); runErr != failure || output.Len() != 0 {
			t.Errorf("%s: run error %v, receipt %q; want the plain failure and no receipt", name, runErr, output.String())
		}
		if got := len(w.trace().Records); got != 2 {
			t.Errorf("%s: %d E04 records, want only the two binder points", name, got)
		}
	}
}
