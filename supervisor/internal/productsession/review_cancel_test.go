//go:build linux

package productsession

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
)

var (
	reviewUsage = Budget{2, 10, 5}
	candidate   = dg("candidate")
)

func revisionOf(t *testing.T, c *Catalog, p Principal, sessionID string) uint64 {
	t.Helper()
	s, err := c.Session(p, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return s.Revision
}

func cancelReq(t *testing.T, c *Catalog, turn Turn, key string) CancelTurnRequest {
	t.Helper()
	return CancelTurnRequest{SessionID: turn.SessionID, TurnID: turn.ID, ExpectedRevision: revisionOf(t, c, uid1, turn.SessionID), RequestKey: key}
}

func resolveReq(t *testing.T, c *Catalog, turn Turn, key, decision string) ResolveTurnRequest {
	t.Helper()
	return ResolveTurnRequest{SessionID: turn.SessionID, TurnID: turn.ID, ExpectedRevision: revisionOf(t, c, uid1, turn.SessionID),
		RequestKey: key, CandidateDigest: candidate, Decision: decision}
}

// reviewedTurn leaves a turn of a fresh review-mode session on project beta awaiting review.
func reviewedTurn(t *testing.T, c *Catalog, key string) Turn {
	t.Helper()
	s := mustSession(t, c, uid1, "beta", "review", bigBudget, "s-"+key)
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k-"+key, "p")
	mustLink(t, c, turn.ID)
	if err := c.MarkAwaitingReview(turn.ID, reviewFor(c, turn.ID, reviewUsage, candidate)); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Turn(turn.ID)
	return got
}

// linkedTurn leaves a turn of a fresh auto-mode session on project gamma linked.
func linkedTurn(t *testing.T, c *Catalog, key string) Turn {
	t.Helper()
	s := mustSession(t, c, uid1, "gamma", "auto", bigBudget, "s-"+key)
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k-"+key, "p")
	mustLink(t, c, turn.ID)
	got, _ := c.Turn(turn.ID)
	return got
}

func mustCancel(t *testing.T, c *Catalog, turn Turn, key string) Turn {
	t.Helper()
	got, replayed, err := c.CancelTurn(uid1, cancelReq(t, c, turn, key))
	if err != nil || replayed || !got.CancelRequested {
		t.Fatalf("cancel: %+v replayed=%v err=%v", got, replayed, err)
	}
	return got
}

func mustResolve(t *testing.T, c *Catalog, turn Turn, key, decision string) Turn {
	t.Helper()
	got, replayed, err := c.ResolveTurn(uid1, resolveReq(t, c, turn, key, decision))
	if err != nil || replayed || got.Resolution != decision || got.State != StateAwaitingReview {
		t.Fatalf("resolve: %+v replayed=%v err=%v", got, replayed, err)
	}
	return got
}

// terminalFor builds the terminal of a reviewed or linked turn with a receipt that matches it.
func terminalFor(c *Catalog, turnID, outcome, reason, result string, usage Budget) TurnTerminal {
	return TurnTerminal{Outcome: outcome, Settlement: SettlementHeadlessClosed, Receipt: receiptFor(c, turnID, outcome, usage, result),
		Usage: usage, ResultBaselineDigest: result, ContextEligible: true, Reason: reason}
}

func TestCancelAdmittedTurnIsRefusedCancelledWithFullRefund(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "auto", bigBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	wantCode(t, c.RefuseUnlinkedTurn(turn.ID, CodeCancelled), CodeInvalidRequest) // no cancel request yet
	cancelled := mustCancel(t, c, turn, "c1")
	if cancelled.State != StateAdmitted || revisionOf(t, c, uid1, s.ID) != 2 {
		t.Fatalf("a cancel request must not settle the turn: %+v", cancelled)
	}
	if err := c.RefuseUnlinkedTurn(turn.ID, CodeCancelled); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Turn(turn.ID)
	sess, _ := c.Session(uid1, s.ID)
	if got.State != StateTerminal || got.Outcome != OutcomeCancelled || got.Refund != turn.Reservation || got.RefusalCode != CodeCancelled ||
		got.ResultBaselineDigest != b0 || !got.CancelRequested || sess.Reserved != (Budget{}) || sess.Consumed != (Budget{}) || sess.Blocked {
		t.Fatalf("cancelled refusal: turn=%+v session=%+v", got, sess)
	}
	next := mustSubmit(t, c, uid1, s.ID, sess.Revision, "k2", "again")
	if next.Ordinal != 2 || next.PredecessorTurnID != turn.ID {
		t.Fatalf("a cancelled turn must not block the next submit: %+v", next)
	}
}

func TestCancelLinkedTurnSettlesCancelledWithRefund(t *testing.T) {
	c, _ := newCatalog(t)
	turn := linkedTurn(t, c, "a")
	usage := Budget{1, 4, 2}
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeCancelled, "", b0, usage)), CodeInvalidRequest) // not requested
	mustCancel(t, c, turn, "c1")
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeCancelled, "", b1, usage)), CodeInvalidRequest)     // baseline moved
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeCancelled, "user", b0, usage)), CodeInvalidRequest) // reason does not belong
	if err := c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeCancelled, "", b0, usage)); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Turn(turn.ID)
	sess, _ := c.Session(uid1, turn.SessionID)
	if got.State != StateTerminal || got.Outcome != OutcomeCancelled || got.Refund != turn.Reservation.minus(usage) || got.Usage != usage ||
		sess.Consumed != usage || sess.Reserved != (Budget{}) || sess.Blocked {
		t.Fatalf("cancelled settlement: turn=%+v session=%+v", got, sess)
	}
	mustSubmit(t, c, uid1, turn.SessionID, sess.Revision, "k2", "again")
}

func TestCancelAwaitingReviewTurnIsDiscardedWithReasonCancelled(t *testing.T) {
	c, _ := newCatalog(t)
	turn := reviewedTurn(t, c, "a")
	before, _ := c.Session(uid1, turn.SessionID)
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeDiscarded, ReasonCancelled, b0, reviewUsage)), CodeInvalidRequest)
	mustCancel(t, c, turn, "c1")
	if err := c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeDiscarded, ReasonCancelled, b0, reviewUsage)); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Turn(turn.ID)
	sess, _ := c.Session(uid1, turn.SessionID)
	if got.State != StateTerminal || got.Outcome != OutcomeDiscarded || got.TerminalReason != ReasonCancelled || got.Usage != reviewUsage ||
		got.Refund != turn.Refund || got.ResultBaselineDigest != b0 || got.CandidateDigest != candidate {
		t.Fatalf("discarded turn: %+v", got)
	}
	if sess.Consumed != before.Consumed || sess.Reserved != before.Reserved {
		t.Fatalf("the reviewed terminal must not settle again: %+v -> %+v", before, sess)
	}
	mustSubmit(t, c, uid1, turn.SessionID, sess.Revision, "k2", "again")
}

func TestCancelRules(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "auto", bigBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	other := mustSession(t, c, uid2, "beta", "review", bigBudget, "s2")
	otherTurn := mustSubmit(t, c, uid2, other.ID, 0, "k", "p")

	_, _, err := c.CancelTurn(uid2, cancelReq(t, c, turn, "c0"))
	wantCode(t, err, CodeUnauthorizedPrincipal)
	_, _, err = c.CancelTurn(uid1, CancelTurnRequest{SessionID: s.ID, TurnID: otherTurn.ID, ExpectedRevision: 1, RequestKey: "c0"})
	wantCode(t, err, CodeUnknownTurn) // another session's turn
	_, _, err = c.CancelTurn(uid1, CancelTurnRequest{SessionID: s.ID, TurnID: s.ID + "/turn-9", ExpectedRevision: 1, RequestKey: "c0"})
	wantCode(t, err, CodeUnknownTurn)
	stale := cancelReq(t, c, turn, "c1")
	stale.ExpectedRevision = 0
	_, _, err = c.CancelTurn(uid1, stale)
	wantCode(t, err, CodeStaleRevision)

	n := recordCount(t, c)
	first := mustCancel(t, c, turn, "c1")
	req := CancelTurnRequest{SessionID: s.ID, TurnID: turn.ID, ExpectedRevision: 1, RequestKey: "c1"}
	if n2 := recordCount(t, c); n2 != n+1 {
		t.Fatalf("a cancel request writes exactly one record: %d -> %d", n, n2)
	}
	again, replayed, err := c.CancelTurn(uid1, req)
	if err != nil || !replayed || !reflect.DeepEqual(again, first) || recordCount(t, c) != n+1 {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	req.ExpectedRevision = 2
	_, _, err = c.CancelTurn(uid1, req)
	wantCode(t, err, CodeRequestConflict) // same key, different payload
	next, replayed, err := c.CancelTurn(uid1, CancelTurnRequest{SessionID: s.ID, TurnID: turn.ID, ExpectedRevision: 2, RequestKey: "c2"})
	if err != nil || !replayed || !next.CancelRequested || recordCount(t, c) != n+1 || revisionOf(t, c, uid1, s.ID) != 2 {
		t.Fatalf("a second cancel with a new key is already in effect: %+v replayed=%v err=%v", next, replayed, err)
	}

	mustLink(t, c, turn.ID)
	if err := c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeCancelled, "", b0, Budget{1, 1, 1})); err != nil {
		t.Fatal(err)
	}
	_, _, err = c.CancelTurn(uid1, CancelTurnRequest{SessionID: s.ID, TurnID: turn.ID, ExpectedRevision: revisionOf(t, c, uid1, s.ID), RequestKey: "c3"})
	wantCode(t, err, CodeCancelNotAllowed)
	late, replayed, err := c.CancelTurn(uid1, CancelTurnRequest{SessionID: s.ID, TurnID: turn.ID, ExpectedRevision: 1, RequestKey: "c1"})
	if err != nil || !replayed || late.State != StateTerminal {
		t.Fatalf("a retried request after the turn ended returns its turn: %+v replayed=%v err=%v", late, replayed, err)
	}
}

func TestResolveTurnRules(t *testing.T) {
	c, _ := newCatalog(t)
	turn := reviewedTurn(t, c, "a")
	linked := linkedTurn(t, c, "b")
	if turn.ReviewTimeoutSeconds != int64(c.st.reg.Limits.ReviewTimeoutSeconds) || turn.ReviewTimeoutSeconds == 0 {
		t.Fatalf("review timeout = %d, want the registration's %d", turn.ReviewTimeoutSeconds, c.st.reg.Limits.ReviewTimeoutSeconds)
	}

	bad := resolveReq(t, c, turn, "r0", DecisionPublish)
	bad.CandidateDigest = dg("another candidate")
	_, _, err := c.ResolveTurn(uid1, bad)
	wantCode(t, err, CodeCandidateMismatch)
	bad = resolveReq(t, c, turn, "r0", "merge")
	_, _, err = c.ResolveTurn(uid1, bad)
	wantCode(t, err, CodeInvalidRequest)
	bad = resolveReq(t, c, turn, "r0", DecisionPublish)
	bad.ExpectedRevision--
	_, _, err = c.ResolveTurn(uid1, bad)
	wantCode(t, err, CodeStaleRevision)
	_, _, err = c.ResolveTurn(uid2, resolveReq(t, c, turn, "r0", DecisionPublish))
	wantCode(t, err, CodeUnauthorizedPrincipal)
	_, _, err = c.ResolveTurn(uid1, resolveReq(t, c, linked, "r0", DecisionPublish))
	wantCode(t, err, CodeReviewNotPending) // linked, not awaiting review

	n := recordCount(t, c)
	first := mustResolve(t, c, turn, "r1", DecisionPublish)
	req := ResolveTurnRequest{SessionID: turn.SessionID, TurnID: turn.ID, ExpectedRevision: 2, RequestKey: "r1", CandidateDigest: candidate, Decision: DecisionPublish}
	again, replayed, err := c.ResolveTurn(uid1, req)
	if err != nil || !replayed || !reflect.DeepEqual(again, first) || recordCount(t, c) != n+1 {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	req.Decision = DecisionDiscard
	_, _, err = c.ResolveTurn(uid1, req)
	wantCode(t, err, CodeRequestConflict) // same key, different decision
	_, _, err = c.ResolveTurn(uid1, resolveReq(t, c, turn, "r2", DecisionDiscard))
	wantCode(t, err, CodeRequestConflict) // one resolution per turn
	_, _, err = c.ResolveTurn(uid1, resolveReq(t, c, turn, "r3", DecisionPublish))
	wantCode(t, err, CodeRequestConflict)
	if recordCount(t, c) != n+1 || revisionOf(t, c, uid1, turn.SessionID) != 3 {
		t.Fatal("refused resolutions must write nothing and leave the revision")
	}

	if err := c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomePublished, "", candidate, reviewUsage)); err != nil {
		t.Fatal(err)
	}
	_, _, err = c.ResolveTurn(uid1, resolveReq(t, c, turn, "r4", DecisionPublish))
	wantCode(t, err, CodeReviewNotPending)
	late, replayed, err := c.ResolveTurn(uid1, req0(turn, "r1"))
	if err != nil || !replayed || late.State != StateTerminal {
		t.Fatalf("a retried resolution after the turn ended returns its turn: %+v replayed=%v err=%v", late, replayed, err)
	}
}

func req0(turn Turn, key string) ResolveTurnRequest {
	return ResolveTurnRequest{SessionID: turn.SessionID, TurnID: turn.ID, ExpectedRevision: 2, RequestKey: key, CandidateDigest: candidate, Decision: DecisionPublish}
}

func TestReviewedPublishMovesBaselineWithoutSettlingAgain(t *testing.T) {
	c, _ := newCatalog(t)
	turn := reviewedTurn(t, c, "a")
	before, _ := c.Session(uid1, turn.SessionID)
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomePublished, "", candidate, reviewUsage)), CodeInvalidRequest) // no resolution
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeFailed, "", b0, reviewUsage)), CodeInvalidRequest)
	mustResolve(t, c, turn, "r1", DecisionPublish)
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomePublished, "", candidate, Budget{1, 10, 5})), CodeInvalidRequest) // usage differs
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomePublished, "", b1, reviewUsage)), CodeInvalidRequest)             // not the candidate
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeDiscarded, ReasonUser, b0, reviewUsage)), CodeInvalidRequest)     // needs a discard resolution
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomePublished, ReasonUser, candidate, reviewUsage)), CodeInvalidRequest)
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeNoDiff, "", b0, reviewUsage)), CodeInvalidRequest)
	if err := c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomePublished, "", candidate, reviewUsage)); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Turn(turn.ID)
	sess, _ := c.Session(uid1, turn.SessionID)
	if got.State != StateTerminal || got.Outcome != OutcomePublished || got.ResultBaselineDigest != candidate || got.TerminalReason != "" ||
		got.ReceiptDigest == turn.ReceiptDigest {
		t.Fatalf("published turn: %+v", got)
	}
	if sess.Consumed != before.Consumed || sess.Reserved != before.Reserved || sess.Blocked {
		t.Fatalf("the reviewed terminal must not settle again: %+v -> %+v", before, sess)
	}
	_, err := submit(c, uid1, turn.SessionID, sess.Revision, "k2", "p", b0)
	wantCode(t, err, CodeStaleBaseline)
	next, err := submit(c, uid1, turn.SessionID, sess.Revision, "k3", "p", candidate)
	if err != nil || next.Turn.PredecessorReceiptDigest != got.ReceiptDigest {
		t.Fatalf("the next turn must start from the published candidate: %+v %v", next, err)
	}
}

func TestReviewedPublicationFailureKeepsBaseline(t *testing.T) {
	c, _ := newCatalog(t)
	turn := reviewedTurn(t, c, "a")
	mustResolve(t, c, turn, "r1", DecisionPublish)
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeFailed, "", candidate, reviewUsage)), CodeInvalidRequest)
	if err := c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeFailed, "", b0, reviewUsage)); err != nil {
		t.Fatal(err)
	}
	sess, _ := c.Session(uid1, turn.SessionID)
	if _, err := submit(c, uid1, turn.SessionID, sess.Revision, "k2", "p", b0); err != nil {
		t.Fatalf("a failed publication leaves the admitted baseline: %v", err)
	}
}

func TestReviewedDiscardReasons(t *testing.T) {
	c, _ := newCatalog(t)
	turn := reviewedTurn(t, c, "a")
	mustResolve(t, c, turn, "r1", DecisionDiscard)
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomePublished, "", candidate, reviewUsage)), CodeInvalidRequest)
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeFailed, "", b0, reviewUsage)), CodeInvalidRequest)
	for _, reason := range []string{"", "bogus", ReasonCancelled} {
		wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeDiscarded, reason, b0, reviewUsage)), CodeInvalidRequest)
	}
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeDiscarded, ReasonUser, candidate, reviewUsage)), CodeInvalidRequest)
	if err := c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeDiscarded, ReasonUser, b0, reviewUsage)); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Turn(turn.ID); got.TerminalReason != ReasonUser || got.Outcome != OutcomeDiscarded {
		t.Fatalf("discarded: %+v", got)
	}

	// expired and restart need no resolution, and a restart may follow a publish request.
	for i, tc := range []struct{ reason, decision string }{{ReasonExpired, ""}, {ReasonRestart, ""}, {ReasonRestart, DecisionPublish}} {
		turn := reviewedTurn(t, c, string(rune('b'+i)))
		if tc.decision != "" {
			mustResolve(t, c, turn, "r1", tc.decision)
		}
		terminal := terminalFor(c, turn.ID, OutcomeDiscarded, tc.reason, b0, reviewUsage)
		if tc.reason == ReasonRestart {
			receipt, _ := DecodeReceipt(terminal.Receipt)
			receipt.Settlement = SettlementReconciledAfterRestart
			terminal.Receipt, _ = json.Marshal(receipt)
			terminal.Settlement = SettlementReconciledAfterRestart
		}
		if err := c.CompleteTurn(turn.ID, terminal); err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		if got, _ := c.Turn(turn.ID); got.TerminalReason != tc.reason || got.Settlement != terminal.Settlement {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
}

func TestReasonOnlyBelongsToDiscarded(t *testing.T) {
	c, _ := newCatalog(t)
	turn := linkedTurn(t, c, "a")
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeNoDiff, ReasonUser, b0, Budget{1, 1, 1})), CodeInvalidRequest)
	wantCode(t, c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomeDiscarded, ReasonRestart, b0, Budget{1, 1, 1})), CodeInvalidRequest) // not reviewed
}

func TestReconciledAfterRestartSettlement(t *testing.T) {
	c, _ := newCatalog(t)
	turn := linkedTurn(t, c, "a")
	usage := Budget{1, 1, 1}
	with := func(mutate func(*TurnReceipt)) TurnTerminal {
		terminal := terminalFor(c, turn.ID, OutcomeFailed, "", b0, usage)
		receipt, _ := DecodeReceipt(terminal.Receipt)
		receipt.Settlement = SettlementReconciledAfterRestart
		mutate(&receipt)
		terminal.Receipt, _ = json.Marshal(receipt)
		terminal.Settlement = SettlementReconciledAfterRestart
		return terminal
	}
	wantCode(t, c.CompleteTurn(turn.ID, with(func(r *TurnReceipt) { r.WorkerKind = WorkerPi })), CodeInvalidRequest)
	if err := c.CompleteTurn(turn.ID, with(func(*TurnReceipt) {})); err != nil {
		t.Fatalf("a headless fixture turn settles reconciled-after-restart: %v", err)
	}
}

func TestChildReviewCancelHelper(t *testing.T) {
	path := os.Getenv("TB_PRODUCT_P4_PATH")
	if path == "" {
		return
	}
	c, err := Open(path, signedRegistration(t, nil))
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	admitted := CancelTurnRequest{SessionID: os.Getenv("TB_PRODUCT_P4_ADMITTED"), TurnID: os.Getenv("TB_PRODUCT_P4_ADMITTED") + "/turn-1", ExpectedRevision: 1, RequestKey: "child-cancel"}
	if _, _, err := c.CancelTurn(uid1, admitted); err != nil {
		t.Fatalf("child cancel: %v", err)
	}
	reviewed := ResolveTurnRequest{SessionID: os.Getenv("TB_PRODUCT_P4_REVIEWED"), TurnID: os.Getenv("TB_PRODUCT_P4_REVIEWED") + "/turn-1",
		ExpectedRevision: 2, RequestKey: "child-resolve", CandidateDigest: candidate, Decision: DecisionPublish}
	if _, _, err := c.ResolveTurn(uid1, reviewed); err != nil {
		t.Fatalf("child resolve: %v", err)
	}
	// The child exits without closing: the two requests are all that is durable.
}

func TestFreshProcessRestoresCancelAndResolutionRequests(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	reg := signedRegistration(t, nil)
	c := openAt(t, path, reg)
	admittedSession := mustSession(t, c, uid1, "gamma", "auto", bigBudget, "s1")
	admitted := mustSubmit(t, c, uid1, admittedSession.ID, 0, "k1", "p")
	reviewed := reviewedTurn(t, c, "r")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChildReviewCancelHelper$")
	child.Env = append(os.Environ(), "TB_PRODUCT_P4_PATH="+path, "TB_PRODUCT_P4_ADMITTED="+admitted.SessionID, "TB_PRODUCT_P4_REVIEWED="+reviewed.SessionID)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child process: %v\n%s", err, out)
	}

	c = openAt(t, path, reg)
	gotAdmitted, _ := c.Turn(admitted.ID)
	gotReviewed, _ := c.Turn(reviewed.ID)
	if gotAdmitted.State != StateAdmitted || !gotAdmitted.CancelRequested || revisionOf(t, c, uid1, admitted.SessionID) != 2 {
		t.Fatalf("cancel request after restart: %+v", gotAdmitted)
	}
	if gotReviewed.State != StateAwaitingReview || gotReviewed.Resolution != DecisionPublish || gotReviewed.CandidateDigest != candidate ||
		gotReviewed.CancelRequested || revisionOf(t, c, uid1, reviewed.SessionID) != 3 {
		t.Fatalf("resolution request after restart: %+v", gotReviewed)
	}
	n := recordCount(t, c)
	again, replayed, err := c.CancelTurn(uid1, CancelTurnRequest{SessionID: admitted.SessionID, TurnID: admitted.ID, ExpectedRevision: 1, RequestKey: "child-cancel"})
	if err != nil || !replayed || !reflect.DeepEqual(again, gotAdmitted) {
		t.Fatalf("cancel replay in a new process: %+v replayed=%v err=%v", again, replayed, err)
	}
	again, replayed, err = c.ResolveTurn(uid1, ResolveTurnRequest{SessionID: reviewed.SessionID, TurnID: reviewed.ID, ExpectedRevision: 2,
		RequestKey: "child-resolve", CandidateDigest: candidate, Decision: DecisionPublish})
	if err != nil || !replayed || !reflect.DeepEqual(again, gotReviewed) || recordCount(t, c) != n {
		t.Fatalf("resolve replay in a new process: %+v replayed=%v err=%v", again, replayed, err)
	}

	if err := c.RefuseUnlinkedTurn(admitted.ID, CodeCancelled); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteTurn(reviewed.ID, terminalFor(c, reviewed.ID, OutcomePublished, "", candidate, reviewUsage)); err != nil {
		t.Fatalf("the restored publish resolution must authorize the terminal: %v", err)
	}
}

func TestOpenRefusesInvalidReviewRecords(t *testing.T) {
	type forged func(t *testing.T, c *Catalog) audit.Event
	event := func(t *testing.T, kind string, record any) audit.Event {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		return audit.Event{Kind: kind, ID: "evidence", Data: data}
	}
	terminal := func(t *testing.T, c *Catalog, turn Turn, outcome, reason, result string, usage, refund Budget) audit.Event {
		receipt := receiptFor(c, turn.ID, outcome, usage, result)
		return event(t, kindTurnTerminal, turnTerminalRecord{header: hdr, TurnID: turn.ID, Outcome: outcome, Settlement: SettlementHeadlessClosed,
			Receipt: receipt, ReceiptDigest: ReceiptDigest(receipt), Usage: usage, ResultBaselineDigest: result, Refund: refund, Reason: reason})
	}
	cases := map[string]forged{
		"published without a publish resolution": func(t *testing.T, c *Catalog) audit.Event {
			return terminal(t, c, reviewedTurn(t, c, "a"), OutcomePublished, "", candidate, reviewUsage, Budget{})
		},
		"reviewed terminal that refunds again": func(t *testing.T, c *Catalog) audit.Event {
			turn := reviewedTurn(t, c, "a")
			return terminal(t, c, turn, OutcomeDiscarded, ReasonRestart, b0, reviewUsage, turn.Reservation.minus(reviewUsage))
		},
		"reviewed terminal with other usage": func(t *testing.T, c *Catalog) audit.Event {
			return terminal(t, c, reviewedTurn(t, c, "a"), OutcomeDiscarded, ReasonRestart, b0, Budget{1, 1, 1}, Budget{})
		},
		"user discard without a discard resolution": func(t *testing.T, c *Catalog) audit.Event {
			return terminal(t, c, reviewedTurn(t, c, "a"), OutcomeDiscarded, ReasonUser, b0, reviewUsage, Budget{})
		},
		"discard without a reason": func(t *testing.T, c *Catalog) audit.Event {
			return terminal(t, c, reviewedTurn(t, c, "a"), OutcomeDiscarded, "", b0, reviewUsage, Budget{})
		},
		"cancelled without a cancel request": func(t *testing.T, c *Catalog) audit.Event {
			turn := linkedTurn(t, c, "a")
			return terminal(t, c, turn, OutcomeCancelled, "", b0, Budget{1, 1, 1}, turn.Reservation.minus(Budget{1, 1, 1}))
		},
		"cancelled refusal without a cancel request": func(t *testing.T, c *Catalog) audit.Event {
			s := mustSession(t, c, uid1, "alpha", "auto", bigBudget, "s")
			turn := mustSubmit(t, c, uid1, s.ID, 0, "k", "p")
			return event(t, kindTurnRefused, turnRefusedRecord{header: hdr, TurnID: turn.ID, Code: CodeCancelled, Refund: turn.Reservation})
		},
		"cancel request for a terminal turn": func(t *testing.T, c *Catalog) audit.Event {
			s := mustSession(t, c, uid1, "alpha", "auto", bigBudget, "s")
			turn := mustSubmit(t, c, uid1, s.ID, 0, "k", "p")
			if err := c.AbandonUnlinkedTurn(turn.ID); err != nil {
				t.Fatal(err)
			}
			req := CancelTurnRequest{SessionID: s.ID, TurnID: turn.ID, ExpectedRevision: 2, RequestKey: "c"}
			return event(t, kindTurnCancelRequested, turnCancelRequestedRecord{header: hdr, TurnID: turn.ID, SessionID: s.ID, Principal: uid1,
				RequestKey: "c", PayloadDigest: payloadDigest(opCancelTurn, req), SessionRevision: 3})
		},
		"resolution for another candidate": func(t *testing.T, c *Catalog) audit.Event {
			turn := reviewedTurn(t, c, "a")
			req := ResolveTurnRequest{SessionID: turn.SessionID, TurnID: turn.ID, ExpectedRevision: 2, RequestKey: "r", CandidateDigest: b1, Decision: DecisionPublish}
			return event(t, kindTurnResolutionRequested, turnResolutionRequestedRecord{header: hdr, TurnID: turn.ID, SessionID: turn.SessionID, Principal: uid1,
				RequestKey: "r", PayloadDigest: payloadDigest(opResolveTurn, req), CandidateDigest: b1, Decision: DecisionPublish, SessionRevision: 3})
		},
		"resolution with a stale payload digest": func(t *testing.T, c *Catalog) audit.Event {
			turn := reviewedTurn(t, c, "a")
			req := ResolveTurnRequest{SessionID: turn.SessionID, TurnID: turn.ID, ExpectedRevision: 2, RequestKey: "r", CandidateDigest: candidate, Decision: DecisionDiscard}
			return event(t, kindTurnResolutionRequested, turnResolutionRequestedRecord{header: hdr, TurnID: turn.ID, SessionID: turn.SessionID, Principal: uid1,
				RequestKey: "r", PayloadDigest: payloadDigest(opResolveTurn, req), CandidateDigest: candidate, Decision: DecisionPublish, SessionRevision: 3})
		},
		"review timeout that differs from the registration": func(t *testing.T, c *Catalog) audit.Event {
			s := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s")
			turn := mustSubmit(t, c, uid1, s.ID, 0, "k", "p")
			mustLink(t, c, turn.ID)
			review := reviewFor(c, turn.ID, reviewUsage, candidate)
			return event(t, kindTurnAwaitingReview, turnAwaitingReviewRecord{header: hdr, TurnID: turn.ID, Settlement: review.Settlement,
				Receipt: review.Receipt, ReceiptDigest: ReceiptDigest(review.Receipt), Usage: reviewUsage, Refund: turn.Reservation.minus(reviewUsage),
				CandidateDigest: candidate, ReviewTimeoutSeconds: int64(c.st.reg.Limits.ReviewTimeoutSeconds) + 1})
		},
	}
	for name, build := range cases {
		path := filepath.Join(privateDir(t), "catalog.jsonl")
		reg := signedRegistration(t, nil)
		c, err := Open(path, reg)
		if err != nil {
			t.Fatal(err)
		}
		forgedEvent := build(t, c)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		j, err := audit.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.Append(forgedEvent); err != nil {
			t.Fatalf("%s: append: %v", name, err)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, reg); !isCode(err, CodeCatalogCorrupt) {
			t.Errorf("%s: Open err=%v, want CATALOG_CORRUPT", name, err)
		}
	}
}

func TestOpenReplaysReviewAndCancelRecordsIdentically(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	reg := signedRegistration(t, nil)
	c := openAt(t, path, reg)
	published := reviewedTurn(t, c, "a")
	mustCancel(t, c, published, "c1")
	mustResolve(t, c, published, "r1", DecisionPublish)
	if err := c.CompleteTurn(published.ID, terminalFor(c, published.ID, OutcomePublished, "", candidate, reviewUsage)); err != nil {
		t.Fatal(err)
	}
	s := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s-b") // still awaiting review when the catalog closes
	pending := mustSubmit(t, c, uid1, s.ID, 0, "k-b", "p")
	mustLink(t, c, pending.ID)
	if err := c.MarkAwaitingReview(pending.ID, reviewFor(c, pending.ID, reviewUsage, candidate)); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openAt(t, path, reg)
	if !reflect.DeepEqual(c.st.sessions, reopened.st.sessions) || !reflect.DeepEqual(c.st.turns, reopened.st.turns) ||
		!reflect.DeepEqual(c.st.projects, reopened.st.projects) || !reflect.DeepEqual(c.st.keys, reopened.st.keys) {
		t.Fatal("reopened state differs from the state that wrote the journal")
	}
}

func TestAwaitingReviewTurnCanBeMarkedUnknownWithoutSettlingAgain(t *testing.T) {
	c, _ := newCatalog(t)
	turn := reviewedTurn(t, c, "a")
	before, _ := c.Session(uid1, turn.SessionID)
	if err := c.MarkTurnUnknown(turn.ID, "publication outcome could not be established"); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Turn(turn.ID)
	sess, _ := c.Session(uid1, turn.SessionID)
	if got.State != StateUnknown || sess.Consumed != before.Consumed || sess.Reserved != before.Reserved || !sess.Blocked {
		t.Fatalf("unknown after review: turn=%+v session=%+v", got, sess)
	}
	_, err := submit(c, uid1, turn.SessionID, sess.Revision, "k2", "p", b0)
	wantCode(t, err, CodePredecessorUnresolved)
}
