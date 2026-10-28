//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/wsoracle"
)

// e04Lifecycle drives settleProductionTurn, the production lifecycle from the end of the turn to the
// publication outcome, through fakes for the turn, the worker teardown, the loops, the store, the
// publication and the cleanup closers. Barrier events (what the fakes do) and E04 points (what the
// real recorder snapshots) go into one ordered log.
type e04Lifecycle struct {
	t       *testing.T
	w       *e04FakeWorkflow
	session *productionPiSession
	events  []string
	ops     productionLifecycleOps
}

func e04Pt(point string) string { return "point:" + point }

func newE04Lifecycle(t *testing.T, bundle sessionrepo.EvidenceBundle, result publication.Result, finalizeErr error) *e04Lifecycle {
	t.Helper()
	l := &e04Lifecycle{t: t, w: newE04FakeWorkflow(t)}
	realSnapshot := l.w.rec.snapshot
	l.w.rec.snapshot = func(root, label string) (wsoracle.Manifest, error) {
		l.step(e04Pt(label))
		return realSnapshot(root, label)
	}
	l.session = &productionPiSession{E04: l.w.rec, Lifecycle: &productionLifecycleEvidence{}}
	stopped := piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true}
	l.ops = productionLifecycleOps{
		waitTurn:         func() error { l.step("turn-ended"); return nil },
		closeWorker:      func() (piruntime.Settlement, error) { l.step("worker-closed"); return stopped, nil },
		joinChannels:     func() error { l.step("channels-joined"); return nil },
		releaseAdmission: func() { l.step("admission-released") },
		evidenceBundle:   func() (sessionrepo.EvidenceBundle, error) { l.step("bundle-read"); return bundle, nil },
		closePublication: func() error { l.step("publication-closed"); return nil },
		markNoPublication: func() error {
			l.step("lease-marked-no-publication")
			return nil
		},
		finalize: func(context.Context) (publication.Result, error) {
			l.step("finalize")
			return result, finalizeErr
		},
		recordTerminal: func(context.Context, publication.Status, string) error {
			l.step("terminal-recorded")
			return nil
		},
	}
	return l
}

func (l *e04Lifecycle) step(event string) { l.events = append(l.events, event) }

// binderPoints stands in for the two points the binders take before the session runs; their place in
// the binders is pinned by TestE04TheBinderPointsAreTakenBetweenTheirBarriers.
func (l *e04Lifecycle) binderPoints() {
	l.step("lease-recovered")
	l.session.recordE04(wsoracle.PointBaseline)
	l.step("materialized")
	l.session.recordE04(wsoracle.PointPostMaterialization)
}

// cleanup runs the closer stack the binders register (the lease, the live root, then the E04 store
// and its final-cleanup closer), in the reverse order the session closes it.
func (l *e04Lifecycle) cleanup() {
	l.step("cleanup-start")
	closers := []func() error{
		func() error { l.step("lease-released"); return nil },
		func() error { l.step("live-root-closed"); return nil },
		l.w.rec.close, l.w.rec.finalCleanup,
	}
	for i := len(closers) - 1; i >= 0; i-- {
		_ = closers[i]()
	}
}

// recordedPoints are the points in the store, in order.
func (l *e04Lifecycle) recordedPoints() []string {
	var points []string
	for _, record := range l.w.trace().Records {
		points = append(points, e04Decode(l.t, record).Point)
	}
	return points
}

func e04ChangedBundle() sessionrepo.EvidenceBundle {
	return sessionrepo.EvidenceBundle{ApprovedDeltaLedger: []delta.Transition{{Changes: []delta.ObjectChange{{Path: "protected.txt"}}}}}
}

func e04Equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got  %q\n want %q", what, got, want)
	}
}

// The points are taken in the lifecycle's own order, each after the event it follows and before the
// next barrier, for a run that publishes.
func TestE04LifecyclePointsFollowTheirBarriersForAPublishedRun(t *testing.T) {
	result := publication.Result{Status: publication.StatusSucceeded, TransactionID: "tx-1",
		Objects: []publication.ObjectResult{{Path: "protected.txt", Status: publication.ObjectSucceeded}},
		Ledger: []wsoracle.LedgerEntry{{ID: "tx-1/0000", Sequence: 1, Changes: []wsoracle.LedgerChange{
			{Path: "protected.txt", After: &wsoracle.Entry{Path: "protected.txt", Type: wsoracle.TypeFile}}}}}}
	l := newE04Lifecycle(t, e04ChangedBundle(), result, nil)
	l.binderPoints()
	out, err := l.session.settleProductionTurn(context.Background(), l.ops)
	if err != nil || out.disposition != "PUBLISHED" || out.publishErr != nil {
		t.Fatalf("outcome = %+v err = %v", out, err)
	}
	l.cleanup()
	e04Equal(t, "event order", l.events, []string{
		"lease-recovered", e04Pt(wsoracle.PointBaseline),
		"materialized", e04Pt(wsoracle.PointPostMaterialization),
		"turn-ended", e04Pt(wsoracle.PointPostSettlement),
		"worker-closed", "channels-joined", "admission-released", "bundle-read", e04Pt(wsoracle.PointPostTeardown),
		e04Pt(wsoracle.PointPrePublication), "finalize", "terminal-recorded", e04Pt(wsoracle.PointPostPublication),
		"cleanup-start", e04Pt(wsoracle.PointFinalCleanup), "live-root-closed", "lease-released",
	})
	e04Equal(t, "recorded points", l.recordedPoints(), []string{wsoracle.PointBaseline, wsoracle.PointPostMaterialization,
		wsoracle.PointPostSettlement, wsoracle.PointPostTeardown, wsoracle.PointPrePublication, wsoracle.PointPostPublication, wsoracle.PointFinalCleanup})
}

// A run with nothing to publish takes the publication points around the no-diff release.
func TestE04LifecyclePointsFollowTheirBarriersForANoDiffRun(t *testing.T) {
	l := newE04Lifecycle(t, sessionrepo.EvidenceBundle{}, publication.Result{}, nil)
	l.binderPoints()
	out, err := l.session.settleProductionTurn(context.Background(), l.ops)
	if err != nil || out.disposition != "NO_DIFF" || !out.noChangeReleased {
		t.Fatalf("outcome = %+v err = %v", out, err)
	}
	l.cleanup()
	e04Equal(t, "event order", l.events, []string{
		"lease-recovered", e04Pt(wsoracle.PointBaseline),
		"materialized", e04Pt(wsoracle.PointPostMaterialization),
		"turn-ended", e04Pt(wsoracle.PointPostSettlement),
		"worker-closed", "channels-joined", "admission-released", "bundle-read", e04Pt(wsoracle.PointPostTeardown),
		e04Pt(wsoracle.PointPrePublication), "publication-closed", "lease-marked-no-publication", e04Pt(wsoracle.PointPostPublication),
		"cleanup-start", e04Pt(wsoracle.PointFinalCleanup), "live-root-closed", "lease-released",
	})
}

// A failed or cancelled lifecycle keeps the points taken before the failure, takes none of the
// stages it never reached, and still takes the final-cleanup point.
func TestE04LifecycleFailureAndCancellationKeepTheEarlierPointsAndDropTheLaterOnes(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	unsettled := piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: false, DescendantsSettled: true}
	base := []string{wsoracle.PointBaseline, wsoracle.PointPostMaterialization}
	cases := []struct {
		name       string
		ctx        context.Context
		change     func(*e04Lifecycle)
		wantErr    bool
		wantEvents []string
		wantPoints []string
	}{
		{name: "the turn fails", ctx: context.Background(),
			change: func(l *e04Lifecycle) {
				l.ops.waitTurn = func() error { l.step("turn-failed"); return errors.New("worker error") }
			},
			wantErr:    true,
			wantEvents: []string{"turn-failed"},
			wantPoints: base},
		{name: "the turn is cancelled", ctx: canceled,
			change: func(l *e04Lifecycle) {
				l.ops.waitTurn = func() error {
					l.step("turn-cancelled")
					return fmt.Errorf("wait for production Pi turn: %w", canceled.Err())
				}
			},
			wantErr:    true,
			wantEvents: []string{"turn-cancelled"},
			wantPoints: base},
		{name: "the loops do not join after the turn", ctx: context.Background(),
			change: func(l *e04Lifecycle) {
				l.ops.joinChannels = func() error { l.step("join-failed"); return errors.New("loops failed") }
			},
			wantErr:    true,
			wantEvents: []string{"turn-ended", e04Pt(wsoracle.PointPostSettlement), "worker-closed", "join-failed"},
			wantPoints: append(append([]string(nil), base...), wsoracle.PointPostSettlement)},
		{name: "the worker is not observed to exit", ctx: context.Background(),
			change: func(l *e04Lifecycle) {
				l.ops.closeWorker = func() (piruntime.Settlement, error) { l.step("worker-closed"); return unsettled, nil }
			},
			wantEvents: []string{"turn-ended", e04Pt(wsoracle.PointPostSettlement), "worker-closed", "channels-joined"},
			wantPoints: append(append([]string(nil), base...), wsoracle.PointPostSettlement)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newE04Lifecycle(t, e04ChangedBundle(), publication.Result{}, nil)
			c.change(l)
			l.binderPoints()
			l.events = nil // the binder points are not under test here
			out, err := l.session.settleProductionTurn(c.ctx, l.ops)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want error %t (outcome %+v)", err, c.wantErr, out)
			}
			if !c.wantErr && (out.disposition != "UNKNOWN" || out.publishErr == nil) {
				t.Fatalf("outcome = %+v, want an UNKNOWN disposition and a publication error", out)
			}
			l.cleanup()
			e04Equal(t, "event order", l.events, append(c.wantEvents, "cleanup-start", e04Pt(wsoracle.PointFinalCleanup), "live-root-closed", "lease-released"))
			e04Equal(t, "recorded points", l.recordedPoints(), append(c.wantPoints, wsoracle.PointFinalCleanup))
		})
	}
}

// A publication that reports failure keeps the publication points, and its post-publication record
// does not claim a ledger.
func TestE04LifecycleAFailedPublicationStillTakesThePublicationPointsWithoutALedger(t *testing.T) {
	l := newE04Lifecycle(t, e04ChangedBundle(), publication.Result{Status: publication.StatusFailed, TransactionID: "tx-2"}, errors.New("publish failed"))
	out, err := l.session.settleProductionTurn(context.Background(), l.ops)
	if err != nil || out.publishErr == nil || out.disposition != "PUBLICATION_FAILED" {
		t.Fatalf("outcome = %+v err = %v", out, err)
	}
	l.cleanup()
	points := l.recordedPoints()
	e04Equal(t, "recorded points", points, []string{wsoracle.PointPostSettlement, wsoracle.PointPostTeardown,
		wsoracle.PointPrePublication, wsoracle.PointPostPublication, wsoracle.PointFinalCleanup})
	records := l.w.trace().Records
	if got := e04Decode(t, records[3]); got.LedgerBasis != runner.E04LedgerUnavailable || got.LedgerApplied != 0 || got.LedgerPrefixDigest != "" {
		t.Fatalf("post-publication ledger = %+v, want an unavailable ledger", got)
	}
}

// The two points taken by the binders cannot be driven without the guest, so their place is pinned
// by position: each follows the step it depends on and precedes the next one.
func TestE04TheBinderPointsAreTakenBetweenTheirBarriers(t *testing.T) {
	chains := map[string][]string{
		"production_binder_linux.go": {"recoverManagedLiveRootBeforeCapture", wsoracle.PointBaseline,
			"D09ManagedLiveRootSnapshotEvidenceForSeed", "BindPreparedSession", wsoracle.PointPostMaterialization, "initializeProductionFreshWorkflow"},
		"production_recovery_binder_linux.go": {"recoverManagedLiveRootBeforeCapture", wsoracle.PointBaseline,
			"D09ManagedLiveRootSnapshotEvidenceForSeed", "activateProductionRecoverySession", wsoracle.PointPostMaterialization},
		// The recovered session is bound once, inside Activate, after its Store and Git activation.
		"governed_recovery_linux.go": {"Activate", "BindPreparedSession"},
	}
	isPoint := map[string]bool{}
	for _, p := range []string{wsoracle.PointBaseline, wsoracle.PointPostMaterialization} {
		isPoint[p] = true
	}
	for file, chain := range chains {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		positions := map[string][]token.Pos{}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				name = fun.Name
			case *ast.SelectorExpr:
				name = fun.Sel.Name
				if (name == "record" || name == "recordE04") && len(call.Args) == 1 {
					if arg, ok := call.Args[0].(*ast.SelectorExpr); ok {
						name = pointConstValue(arg.Sel.Name)
					}
				}
			}
			positions[name] = append(positions[name], call.Pos())
			return true
		})
		for i, name := range chain {
			if len(positions[name]) == 0 {
				t.Fatalf("%s: no call to %s", file, name)
			}
			if isPoint[name] && len(positions[name]) != 1 {
				t.Fatalf("%s: %s is taken %d times, want once", file, name, len(positions[name]))
			}
			if i == 0 {
				continue
			}
			for _, earlier := range positions[chain[i-1]] {
				for _, later := range positions[name] {
					if earlier >= later {
						t.Errorf("%s: %s is not after %s", file, name, chain[i-1])
					}
				}
			}
		}
	}
}

func pointConstValue(constName string) string {
	switch constName {
	case "PointBaseline":
		return wsoracle.PointBaseline
	case "PointPostMaterialization":
		return wsoracle.PointPostMaterialization
	}
	return constName
}
