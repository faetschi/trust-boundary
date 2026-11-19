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

// The binders take their points by position, and the recovered session is bound exactly once. Each
// assertion reads one function body, so a call elsewhere in the same file cannot satisfy it.
func TestE04TheBinderPointsAreTakenBetweenTheirBarriers(t *testing.T) {
	requireBarrierOrder(t, "production_binder_linux.go BindProductionPiSession",
		functionCalls(t, "production_binder_linux.go", "productionBinder", "BindProductionPiSession"),
		[]string{"recoverManagedLiveRootBeforeCapture", wsoracle.PointBaseline,
			"D09ManagedLiveRootSnapshotEvidenceForSeed", "BindPreparedSession", wsoracle.PointPostMaterialization, "initializeProductionFreshWorkflow"})
	requireBarrierOrder(t, "production_recovery_binder_linux.go bindProductionPiRecoverySession",
		functionCalls(t, "production_recovery_binder_linux.go", "", "bindProductionPiRecoverySession"),
		[]string{"recoverManagedLiveRootBeforeCapture", wsoracle.PointBaseline,
			"D09ManagedLiveRootSnapshotEvidenceForSeed", "activateProductionRecoverySession", wsoracle.PointPostMaterialization})
	// The recovery binder never binds, because Activate does, so no function in its file may call BindPreparedSession.
	if binds := allFunctionCalls(t, "production_recovery_binder_linux.go")["BindPreparedSession"]; len(binds) != 0 {
		t.Errorf("production_recovery_binder_linux.go calls BindPreparedSession %d times, want none", len(binds))
	}
	// The recovered session is bound once, after the Store and Git activation that Activate performs first.
	requireBarrierOrder(t, "governed_recovery_linux.go governedRecoveryCandidate.Activate",
		functionCalls(t, "governed_recovery_linux.go", "governedRecoveryCandidate", "Activate"),
		[]string{"Activate", "BindPreparedSession"})
}

// requireBarrierOrder fails unless every step of chain is called in its function body, each point and the
// single bind are called once, and each step's calls follow every call of the step before it.
func requireBarrierOrder(t *testing.T, label string, positions map[string][]token.Pos, chain []string) {
	t.Helper()
	once := map[string]bool{wsoracle.PointBaseline: true, wsoracle.PointPostMaterialization: true, "BindPreparedSession": true}
	for i, name := range chain {
		if len(positions[name]) == 0 {
			t.Fatalf("%s: no call to %s", label, name)
		}
		if once[name] && len(positions[name]) != 1 {
			t.Fatalf("%s: %s is called %d times, want once", label, name, len(positions[name]))
		}
		if i == 0 {
			continue
		}
		for _, earlier := range positions[chain[i-1]] {
			for _, later := range positions[name] {
				if earlier >= later {
					t.Errorf("%s: %s is not after %s", label, name, chain[i-1])
				}
			}
		}
	}
}

// functionCalls returns the call positions of one function body, keyed by callee name. A point recorded
// through record or recordE04 is keyed by its point value.
func functionCalls(t *testing.T, file, receiver, name string) map[string][]token.Pos {
	t.Helper()
	for _, decl := range parseSource(t, file).Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil && fn.Name.Name == name && receiverTypeName(fn) == receiver {
			return callPositions(fn.Body)
		}
	}
	t.Fatalf("%s: no function %s", file, name)
	return nil
}

// allFunctionCalls merges the call positions of every function body in a file.
func allFunctionCalls(t *testing.T, file string) map[string][]token.Pos {
	t.Helper()
	merged := map[string][]token.Pos{}
	for _, decl := range parseSource(t, file).Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			for callee, positions := range callPositions(fn.Body) {
				merged[callee] = append(merged[callee], positions...)
			}
		}
	}
	return merged
}

func callPositions(body ast.Node) map[string][]token.Pos {
	positions := map[string][]token.Pos{}
	ast.Inspect(body, func(n ast.Node) bool {
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
	return positions
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

func parseSource(t *testing.T, file string) *ast.File {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
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

// A failed provider turn (register D30 as amended 2026-11-18) is not a lifecycle error once the worker
// settled: publication never runs, the remaining points are taken with the empty ledger, and the
// outcome carries the turn failure, so the run ends UNKNOWN with a receipt that binds its evidence.
// With a worker that did not settle, nothing more is observed and the failure is returned as before.
func TestE04LifecycleAFailedProviderTurnTakesItsPointsWithoutPublication(t *testing.T) {
	turnFailed := providerTurnFailure{err: fmt.Errorf("%w; conversation closed: provider outage", errors.New("provider turn failed"))}
	for name, fromJoin := range map[string]bool{"seen by the turn wait": false, "seen at the join": true} {
		t.Run(name, func(t *testing.T) {
			l := newE04Lifecycle(t, e04ChangedBundle(), publication.Result{}, nil)
			if fromJoin {
				l.ops.joinChannels = func() error {
					l.step("join-failed")
					return fmt.Errorf("governed production channels did not settle cleanly: %w", turnFailed)
				}
			} else {
				l.ops.waitTurn = func() error {
					l.step("turn-failed")
					return fmt.Errorf("governed production channels failed before the turn ended: %w", turnFailed)
				}
				l.ops.joinChannels = func() error { l.step("join-failed"); return turnFailed }
			}
			out, err := l.session.settleProductionTurn(context.Background(), l.ops)
			if err != nil || !providerTurnEnded(out.turnErr) || out.disposition != "UNKNOWN" || out.settled {
				t.Fatalf("outcome = %+v err = %v, want an UNKNOWN outcome carrying the turn failure", out, err)
			}
			l.cleanup()
			first := "turn-ended"
			if !fromJoin {
				first = "turn-failed"
			}
			e04Equal(t, "event order", l.events, []string{first, e04Pt(wsoracle.PointPostSettlement), "worker-closed", "join-failed",
				e04Pt(wsoracle.PointPostTeardown), e04Pt(wsoracle.PointPrePublication), e04Pt(wsoracle.PointPostPublication),
				"cleanup-start", e04Pt(wsoracle.PointFinalCleanup), "live-root-closed", "lease-released"})
			records := l.w.trace().Records
			if got := e04Decode(t, records[3]); got.Point != wsoracle.PointPostPublication || got.LedgerBasis != runner.E04LedgerNone {
				t.Fatalf("post-publication record = %+v, want the empty ledger", got)
			}
		})
	}
	unsettled := piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: false, DescendantsSettled: true}
	l := newE04Lifecycle(t, e04ChangedBundle(), publication.Result{}, nil)
	l.ops.waitTurn = func() error { return turnFailed }
	l.ops.closeWorker = func() (piruntime.Settlement, error) { return unsettled, nil }
	l.ops.joinChannels = func() error { return turnFailed }
	if _, err := l.session.settleProductionTurn(context.Background(), l.ops); !providerTurnEnded(err) {
		t.Fatalf("an unsettled worker: err = %v, want the turn failure", err)
	}
	l.cleanup()
	e04Equal(t, "recorded points", l.recordedPoints(), []string{wsoracle.PointPostSettlement, wsoracle.PointFinalCleanup})
}
