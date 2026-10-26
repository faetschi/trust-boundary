//go:build linux

package productsession

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
)

const (
	uid1   Principal = 1000
	uid2   Principal = 1001
	uidBad Principal = 4242
)

var (
	b0 = dg("baseline-0")
	b1 = dg("baseline-1")

	sessionBudget = Budget{12, 100, 90}
	bigBudget     = Budget{30, 3000, 180}
)

// privateDir returns a directory the audit journal accepts (private, with
// trusted ancestry), or skips when this host has none.
func privateDir(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, base := range []string{home, os.TempDir()} {
		if base == "" {
			continue
		}
		dir, err := os.MkdirTemp(base, ".productsession-test-")
		if err != nil {
			continue
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			continue
		}
		probe, err := audit.Open(filepath.Join(dir, "probe.jsonl"))
		if err != nil {
			_ = os.RemoveAll(dir)
			continue
		}
		_ = probe.Close()
		_ = os.Remove(filepath.Join(dir, "probe.jsonl"))
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	t.Skip("no private directory with trusted ancestry for the audit journal")
	return ""
}

func openAt(t *testing.T, path string, reg Registration) *Catalog {
	t.Helper()
	c, err := Open(path, reg)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func newCatalog(t *testing.T) (*Catalog, string) {
	t.Helper()
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	return openAt(t, path, signedRegistration(t, nil)), path
}

func wantCode(t *testing.T, err error, code Code) {
	t.Helper()
	if !errors.Is(err, ErrCode(code)) {
		t.Fatalf("err=%v, want code %s", err, code)
	}
}

func recordCount(t *testing.T, c *Catalog) int {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	trace, err := c.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return len(trace.Records)
}

func mustSession(t *testing.T, c *Catalog, p Principal, project, mode string, budget Budget, key string) Session {
	t.Helper()
	s, replayed, err := c.CreateSession(p, CreateSessionRequest{
		ProjectID: project, ProviderProfile: "p1", PublicationMode: mode, Budget: budget, RequestKey: key,
	}, b0)
	if err != nil || replayed {
		t.Fatalf("create session: %v replayed=%v", err, replayed)
	}
	return s
}

func submit(c *Catalog, p Principal, sessionID string, revision uint64, key, prompt, baseline string) (Admission, error) {
	return c.SubmitTurn(p, SubmitTurnRequest{SessionID: sessionID, ExpectedRevision: revision, RequestKey: key, Prompt: prompt}, baseline)
}

func mustSubmit(t *testing.T, c *Catalog, p Principal, sessionID string, revision uint64, key, prompt string) Turn {
	t.Helper()
	a, err := submit(c, p, sessionID, revision, key, prompt, b0)
	if err != nil || a.Replayed {
		t.Fatalf("submit %s: %v replayed=%v", key, err, a.Replayed)
	}
	return a.Turn
}

func mustLink(t *testing.T, c *Catalog, turnID string) {
	t.Helper()
	n := strings.ReplaceAll(turnID, "/", "-")
	err := c.LinkTurn(turnID, TurnLink{
		WorkflowID: "wf-" + n, RunID: "run-" + n, SessionID: "rt-" + n, ConversationID: "conv-" + n,
		ProviderJournalPath: "/var/lib/x/" + n + "/provider.jsonl", StoreJournalPath: "/var/lib/x/" + n + "/store.jsonl",
		CommandJournalPath: "/var/lib/x/" + n + "/command.jsonl",
	})
	if err != nil {
		t.Fatalf("link %s: %v", turnID, err)
	}
}

func rawReceipt(turnID string) []byte { return []byte(`{"turn_id":"` + turnID + `"}`) }

// receiptFor builds the v1 receipt the catalog requires for a linked turn.
func receiptFor(c *Catalog, turnID, outcome string, usage Budget, result string) []byte {
	turn, _ := c.Turn(turnID)
	r := TurnReceipt{
		SchemaVersion: ReceiptSchema, SessionID: turn.SessionID, TurnID: turn.ID, Ordinal: turn.Ordinal,
		WorkerKind: WorkerSupervisorLoop, WorkerInstance: "worker-" + strings.ReplaceAll(turnID, "/", "-"),
		ProviderJournalHead: strings.Repeat("a", 64), ProviderJournalCount: 3,
		StoreJournalHead: strings.Repeat("b", 64), StoreJournalCount: 2,
		PredecessorReceiptDigest: turn.PredecessorReceiptDigest, BaselineDigest: turn.BaselineDigest,
		ResultBaselineDigest: result, Outcome: outcome, Settlement: SettlementSupervisorLoopClosed, Usage: usage,
		ToolSequence: []string{"read"}, Denials: []ReceiptDenial{{ToolCallID: "call-1", Tool: "write", ReasonCode: "policy_rule_deny"}},
	}
	if turn.Link != nil {
		r.WorkflowID, r.RunID, r.ConversationID = turn.Link.WorkflowID, turn.Link.RunID, turn.Link.ConversationID
		r.ImportedPrefixDigest = turn.Link.ImportedPrefixDigest
	}
	if outcome == OutcomePublished {
		r.PublicationTransactionID = "tx-" + strings.ReplaceAll(turnID, "/", "-")
	}
	data, err := json.Marshal(r)
	if err != nil {
		panic(err)
	}
	return data
}

func reviewFor(c *Catalog, turnID string, usage Budget, candidate string) TurnReview {
	turn, _ := c.Turn(turnID)
	return TurnReview{Settlement: SettlementSupervisorLoopClosed, Usage: usage, CandidateDigest: candidate,
		Receipt: receiptFor(c, turnID, receiptOutcomeAwaitReview, usage, turn.BaselineDigest)}
}

func complete(c *Catalog, turnID, outcome string, usage Budget, result string) error {
	return c.CompleteTurn(turnID, TurnTerminal{
		Outcome: outcome, Settlement: SettlementSupervisorLoopClosed, Receipt: receiptFor(c, turnID, outcome, usage, result), Usage: usage,
		ResultBaselineDigest: result, ContextEligible: true,
	})
}

func finishNoDiff(t *testing.T, c *Catalog, turnID string, usage Budget) {
	t.Helper()
	mustLink(t, c, turnID)
	if err := complete(c, turnID, OutcomeNoDiff, usage, b0); err != nil {
		t.Fatalf("complete %s: %v", turnID, err)
	}
}

func TestCreateSessionLimitsAndPermissions(t *testing.T) {
	c, _ := newCatalog(t)
	create := func(p Principal, r CreateSessionRequest) error {
		_, _, err := c.CreateSession(p, r, b0)
		return err
	}
	ok := CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "auto", Budget: bigBudget, RequestKey: "k"}
	with := func(f func(*CreateSessionRequest)) CreateSessionRequest { r := ok; f(&r); return r }
	cases := []struct {
		name string
		p    Principal
		r    CreateSessionRequest
		code Code
	}{
		{"unknown project", uid1, with(func(r *CreateSessionRequest) { r.ProjectID = "nope" }), CodeUnknownProject},
		{"unknown profile", uid1, with(func(r *CreateSessionRequest) { r.ProviderProfile = "p9" }), CodeUnknownProviderProfile},
		{"mode not permitted", uid1, with(func(r *CreateSessionRequest) { r.ProjectID = "beta" }), CodePublicationModeNotAllowed},
		{"invalid mode", uid1, with(func(r *CreateSessionRequest) { r.PublicationMode = "yolo" }), CodeInvalidRequest},
		{"budget above limit", uid1, with(func(r *CreateSessionRequest) { r.Budget.Tokens = 3001 }), CodeBudgetExceedsLimit},
		{"zero budget", uid1, with(func(r *CreateSessionRequest) { r.Budget.HostCalls = 0 }), CodeInvalidRequest},
		{"bad request key", uid1, with(func(r *CreateSessionRequest) { r.RequestKey = "a b" }), CodeInvalidRequest},
		{"unauthorized principal", uidBad, ok, CodeUnauthorizedPrincipal},
	}
	for _, tc := range cases {
		before := recordCount(t, c)
		wantCode(t, create(tc.p, tc.r), tc.code)
		if recordCount(t, c) != before {
			t.Errorf("%s: a refused request wrote a record", tc.name)
		}
	}
	s, replayed, err := c.CreateSession(uid1, ok, b0)
	if err != nil || replayed || s.Revision != 0 || s.Budget != bigBudget || s.BaselineDigest != b0 {
		t.Fatalf("valid create: %+v replayed=%v err=%v", s, replayed, err)
	}
	if _, _, err := c.CreateSession(uid1, ok, "not-a-digest"); !errors.Is(err, ErrCode(CodeInvalidRequest)) {
		// the replay lookup comes first, so use a fresh key to reach the baseline check
		ok.RequestKey = "k2"
		_, _, err = c.CreateSession(uid1, ok, "not-a-digest")
		wantCode(t, err, CodeInvalidRequest)
	}
}

func TestCreateSessionIdempotency(t *testing.T) {
	c, _ := newCatalog(t)
	r := CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "review", Budget: sessionBudget, RequestKey: "k"}
	first, replayed, err := c.CreateSession(uid1, r, b0)
	if err != nil || replayed {
		t.Fatal(err, replayed)
	}
	n := recordCount(t, c)
	again, replayed, err := c.CreateSession(uid1, r, b0)
	if err != nil || !replayed || again.ID != first.ID || recordCount(t, c) != n {
		t.Fatalf("replay: %+v replayed=%v err=%v records %d->%d", again, replayed, err, n, recordCount(t, c))
	}
	r.Budget.Tokens = 99
	_, _, err = c.CreateSession(uid1, r, b0)
	wantCode(t, err, CodeRequestConflict)
	other, replayed, err := c.CreateSession(uid2, CreateSessionRequest{
		ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "review", Budget: sessionBudget, RequestKey: "k",
	}, b0)
	if err != nil || replayed || other.ID == first.ID {
		t.Fatalf("the same key from another principal must be independent: %+v %v %v", other, replayed, err)
	}
}

func TestTwoPromptsFormTurnsOneAndTwo(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s")
	t1 := mustSubmit(t, c, uid1, s.ID, 0, "k1", "first prompt")
	if t1.ID != s.ID+"/turn-1" || t1.Ordinal != 1 || t1.State != StateAdmitted || t1.PredecessorTurnID != "" ||
		t1.BaselineDigest != b0 || t1.AdmittedRevision != 1 || t1.PromptDigest != dg("first prompt") {
		t.Fatalf("turn 1: %+v", t1)
	}
	if !reflect.DeepEqual(t1.BashOutputPaths, []string{"out/a.txt", "out/b.txt"}) || t1.PolicyDigest != dg("policy-alpha") {
		t.Fatalf("turn 1 pins: %+v", t1)
	}
	finishNoDiff(t, c, t1.ID, Budget{1, 1, 1})
	done, _ := c.Turn(t1.ID)
	wantReceipt := receiptFor(c, t1.ID, OutcomeNoDiff, Budget{1, 1, 1}, b0)
	t2 := mustSubmit(t, c, uid1, s.ID, 2, "k2", "second prompt")
	if t2.ID != s.ID+"/turn-2" || t2.PredecessorTurnID != t1.ID || t2.PredecessorReceiptDigest != done.ReceiptDigest ||
		done.ReceiptDigest != ReceiptDigest(wantReceipt) || !strings.HasPrefix(done.ReceiptDigest, "sha256:") {
		t.Fatalf("turn 2: %+v (turn 1 receipt %s)", t2, done.ReceiptDigest)
	}
	got, err := c.Session(uid1, s.ID)
	if err != nil || !reflect.DeepEqual(got.Turns, []string{t1.ID, t2.ID}) || got.Revision != 3 {
		t.Fatalf("session: %+v %v", got, err)
	}
}

func TestSubmitTurnIdempotencyAndConflict(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s")
	first := mustSubmit(t, c, uid1, s.ID, 0, "k1", "secret prompt text")
	n := recordCount(t, c)
	again, err := submit(c, uid1, s.ID, 0, "k1", "secret prompt text", b0)
	if err != nil || !again.Replayed || again.Turn.ID != first.ID || recordCount(t, c) != n {
		t.Fatalf("duplicate: %+v err=%v records %d->%d", again, err, n, recordCount(t, c))
	}
	_, err = submit(c, uid1, s.ID, 0, "k1", "a different prompt", b0)
	wantCode(t, err, CodeRequestConflict)
	if recordCount(t, c) != n {
		t.Fatal("a conflicting request wrote a record")
	}
	_, err = c.SubmitTurn(uid1, SubmitTurnRequest{SessionID: s.ID, ExpectedRevision: 0, RequestKey: "k1", Prompt: "secret prompt text", ExpectedBaselineDigest: b0}, b0)
	wantCode(t, err, CodeRequestConflict) // the digest covers expected_baseline_digest too

	finishNoDiff(t, c, first.ID, Budget{1, 1, 1}) // the revision moves on
	late, err := submit(c, uid1, s.ID, 0, "k1", "secret prompt text", b0)
	if err != nil || !late.Replayed || late.Turn.ID != first.ID {
		t.Fatalf("a retried request after the revision moved must return its admission: %+v %v", late, err)
	}
	_, err = submit(c, uid1, s.ID, 0, "k-new", "secret prompt text", b0)
	wantCode(t, err, CodeStaleRevision)
	if strings.Contains(err.Error(), "secret prompt") {
		t.Fatalf("error detail leaks the prompt: %v", err)
	}
}

func TestSubmitTurnCheckOrder(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s")
	other := mustSession(t, c, uid2, "alpha", "review", bigBudget, "s2")

	_, err := submit(c, uid1, "psess-"+strings.Repeat("0", 32), 0, "k", "p", b0)
	wantCode(t, err, CodeUnknownSession)
	_, err = submit(c, uid2, s.ID, 0, "k", "p", b0)
	wantCode(t, err, CodeUnauthorizedPrincipal)
	_, err = submit(c, uidBad, s.ID, 0, "k", "p", b0)
	wantCode(t, err, CodeUnauthorizedPrincipal)
	_, err = c.Session(uid2, s.ID)
	wantCode(t, err, CodeUnauthorizedPrincipal)
	_, err = submit(c, uid1, s.ID, 0, "k", "p", "bad")
	wantCode(t, err, CodeInvalidRequest)

	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	_, err = submit(c, uid1, s.ID, 99, "k2", "p", b0)
	wantCode(t, err, CodeTurnActive) // admitted, before the revision check
	mustLink(t, c, turn.ID)
	_, err = submit(c, uid1, s.ID, 99, "k2", "p", b0)
	wantCode(t, err, CodeTurnActive) // linked
	_, err = submit(c, uid2, other.ID, 0, "k3", "p", b0)
	wantCode(t, err, CodeProjectBusy)
	if err := complete(c, turn.ID, OutcomeNoDiff, Budget{1, 1, 1}, b0); err != nil {
		t.Fatal(err)
	}
	_, err = submit(c, uid1, s.ID, 1, "k4", "p", b0)
	wantCode(t, err, CodeStaleRevision)
	_, err = submit(c, uid1, s.ID, 2, "k5", "p", b1)
	wantCode(t, err, CodeStaleBaseline)
	_, err = c.SubmitTurn(uid1, SubmitTurnRequest{SessionID: s.ID, ExpectedRevision: 2, RequestKey: "k6", Prompt: "p", ExpectedBaselineDigest: b1}, b0)
	wantCode(t, err, CodeStaleBaseline)
	_, err = submit(c, uid1, s.ID, 2, "k7", "p", b0)
	if err != nil {
		t.Fatalf("valid resubmission: %v", err)
	}
}

func TestPromptValidation(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s")
	bad := map[string]string{
		"empty": "", "whitespace": " \n\t ", "nul": "a\x00b", "carriage return": "a\rb", "escape": "a\x1bb",
		"invalid utf8": "a\xffb", "oversize": strings.Repeat("x", MaxPromptBytes+1),
	}
	for name, prompt := range bad {
		_, err := submit(c, uid1, s.ID, 0, "k-"+strings.ReplaceAll(name, " ", "-"), prompt, b0)
		if !errors.Is(err, ErrCode(CodeInvalidRequest)) {
			t.Errorf("%s: err=%v, want INVALID_REQUEST", name, err)
		}
	}
	mustSubmit(t, c, uid1, s.ID, 0, "good", strings.Repeat("x", MaxPromptBytes-4)+"\n\té")
}

func TestProjectBusyAcrossSessions(t *testing.T) {
	c, _ := newCatalog(t)
	s1 := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s1")
	s2 := mustSession(t, c, uid2, "alpha", "review", bigBudget, "s2")
	s3 := mustSession(t, c, uid1, "beta", "review", bigBudget, "s3")
	t1 := mustSubmit(t, c, uid1, s1.ID, 0, "k", "p")
	_, err := submit(c, uid2, s2.ID, 0, "k", "p", b0)
	wantCode(t, err, CodeProjectBusy)
	if _, err := submit(c, uid1, s3.ID, 0, "k", "p", b0); err != nil {
		t.Fatalf("another project must not be busy: %v", err)
	}
	mustLink(t, c, t1.ID)
	_, err = submit(c, uid2, s2.ID, 0, "k", "p", b0)
	wantCode(t, err, CodeProjectBusy)
	if err := complete(c, t1.ID, OutcomeNoDiff, Budget{1, 1, 1}, b0); err != nil {
		t.Fatal(err)
	}
	mustSubmit(t, c, uid2, s2.ID, 0, "k", "p")
}

func TestConcurrentSubmitAdmitsExactlyOne(t *testing.T) {
	c, _ := newCatalog(t)
	for round := 0; round < 8; round++ {
		key := string(rune('a' + round))
		sa := mustSession(t, c, uid1, "alpha", "review", bigBudget, "a"+key)
		sb := mustSession(t, c, uid2, "alpha", "review", bigBudget, "b"+key)
		results := make([]error, 2)
		admitted := make([]Admission, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, sp := range []struct {
			p Principal
			s Session
		}{{uid1, sa}, {uid2, sb}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				admitted[i], results[i] = submit(c, sp.p, sp.s.ID, 0, "k", "p", b0)
			}()
		}
		close(start)
		wg.Wait()
		winner := -1
		for i, err := range results {
			switch {
			case err == nil && winner < 0:
				winner = i
			case err == nil:
				t.Fatalf("round %d: both sessions were admitted", round)
			default:
				wantCode(t, err, CodeProjectBusy)
			}
		}
		if winner < 0 {
			t.Fatalf("round %d: no session was admitted", round)
		}
		finishNoDiff(t, c, admitted[winner].Turn.ID, Budget{1, 1, 1})
	}
}

func TestBudgetReservationAndRefundArithmetic(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "review", sessionBudget, "s") // {12,100,90}
	t1 := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p1")
	if t1.Reservation != (Budget{10, 100, 60}) {
		t.Fatalf("reservation = %+v, want min(turn limit, remaining)", t1.Reservation)
	}
	got, _ := c.Session(uid1, s.ID)
	if got.Reserved != t1.Reservation || got.Consumed != (Budget{}) {
		t.Fatalf("after admission: %+v", got)
	}
	mustLink(t, c, t1.ID)
	if err := complete(c, t1.ID, OutcomeNoDiff, Budget{4, 30, 20}, b0); err != nil {
		t.Fatal(err)
	}
	done, _ := c.Turn(t1.ID)
	got, _ = c.Session(uid1, s.ID)
	if done.Refund != (Budget{6, 70, 40}) || got.Consumed != (Budget{4, 30, 20}) || got.Reserved != (Budget{}) {
		t.Fatalf("after turn 1: refund=%+v session=%+v", done.Refund, got)
	}
	t2 := mustSubmit(t, c, uid1, s.ID, 2, "k2", "p2")
	if t2.Reservation != (Budget{8, 70, 60}) {
		t.Fatalf("turn 2 reservation = %+v", t2.Reservation)
	}
	mustLink(t, c, t2.ID)
	if err := complete(c, t2.ID, OutcomeFailed, Budget{8, 70, 60}, b0); err != nil { // usage equal to the reservation is allowed
		t.Fatal(err)
	}
	got, _ = c.Session(uid1, s.ID)
	if got.Consumed != (Budget{12, 100, 80}) {
		t.Fatalf("consumed = %+v", got.Consumed)
	}
	_, err := submit(c, uid1, s.ID, 4, "k3", "p3", b0)
	wantCode(t, err, CodeBudgetExhausted)
}

func TestUsageAboveReservationRefused(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "review", sessionBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k", "p") // reservation {10,100,60}
	mustLink(t, c, turn.ID)
	for name, usage := range map[string]Budget{
		"host calls": {11, 1, 1}, "tokens": {1, 101, 1}, "duration": {1, 1, 61}, "negative": {-1, 0, 0},
	} {
		wantCode(t, complete(c, turn.ID, OutcomeNoDiff, usage, b0), CodeInvalidRequest)
		if got, _ := c.Turn(turn.ID); got.State != StateLinked {
			t.Fatalf("%s: a refused completion changed the turn to %s", name, got.State)
		}
	}
	if err := complete(c, turn.ID, OutcomeNoDiff, Budget{10, 100, 60}, b0); err != nil {
		t.Fatal(err)
	}
}

func TestMaxTurnsPerSession(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	c := openAt(t, path, signedRegistration(t, func(r *Registration) { r.Limits.MaxTurnsPerSession = 2 }))
	s := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s")
	for i, key := range []string{"k1", "k2"} {
		turn := mustSubmit(t, c, uid1, s.ID, uint64(i*2), key, "p")
		finishNoDiff(t, c, turn.ID, Budget{1, 1, 1})
	}
	_, err := submit(c, uid1, s.ID, 4, "k3", "p", b0)
	wantCode(t, err, CodeBudgetExhausted)
}

func TestPublishedMovesProjectBaseline(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "auto", bigBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	mustLink(t, c, turn.ID)
	wantCode(t, complete(c, turn.ID, OutcomeNoDiff, Budget{1, 1, 1}, b1), CodeInvalidRequest) // no_diff keeps the baseline
	wantCode(t, complete(c, turn.ID, OutcomeFailed, Budget{1, 1, 1}, b1), CodeInvalidRequest)
	for _, outcome := range []string{"cancelled", "discarded", "unknown", "awaiting_review", ""} {
		wantCode(t, complete(c, turn.ID, outcome, Budget{1, 1, 1}, b0), CodeInvalidRequest)
	}
	wantCode(t, c.CompleteTurn(turn.ID, TurnTerminal{Outcome: OutcomePublished, Settlement: "verified", Receipt: []byte("not json"),
		Usage: Budget{1, 1, 1}, ResultBaselineDigest: b1}), CodeInvalidRequest)
	if err := complete(c, turn.ID, OutcomePublished, Budget{1, 1, 1}, b1); err != nil {
		t.Fatal(err)
	}

	_, err := submit(c, uid1, s.ID, 2, "k2", "p", b0)
	wantCode(t, err, CodeStaleBaseline)
	_, _, err = c.CreateSession(uid2, CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "auto", Budget: bigBudget, RequestKey: "s2"}, b0)
	wantCode(t, err, CodeStaleBaseline)
	if _, err := submit(c, uid1, s.ID, 2, "k3", "p", b1); err != nil {
		t.Fatalf("the next turn must use the published baseline: %v", err)
	}
	next, _ := c.Turn(s.ID + "/turn-2")
	if next.BaselineDigest != b1 {
		t.Fatalf("turn 2 baseline = %s", next.BaselineDigest)
	}

	review := mustSession(t, c, uid1, "beta", "review", bigBudget, "s3")
	rt := mustSubmit(t, c, uid1, review.ID, 0, "k", "p")
	mustLink(t, c, rt.ID)
	wantCode(t, complete(c, rt.ID, OutcomePublished, Budget{1, 1, 1}, b1), CodeInvalidRequest) // review mode cannot publish
}

func TestAwaitingReviewBlocksSessionAndProject(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "beta", "review", bigBudget, "s")
	other := mustSession(t, c, uid2, "beta", "review", bigBudget, "s2")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	unlinked := TurnReview{Settlement: SettlementSupervisorLoopClosed, Receipt: rawReceipt(turn.ID), Usage: Budget{2, 10, 5}, CandidateDigest: dg("candidate")}
	wantCode(t, c.MarkAwaitingReview(turn.ID, unlinked), CodeWrongTurnState) // not linked yet
	mustLink(t, c, turn.ID)
	review := reviewFor(c, turn.ID, Budget{2, 10, 5}, dg("candidate"))
	bad := review
	bad.Usage = Budget{11, 1, 1}
	wantCode(t, c.MarkAwaitingReview(turn.ID, bad), CodeInvalidRequest)
	bad = review
	bad.CandidateDigest = "x"
	wantCode(t, c.MarkAwaitingReview(turn.ID, bad), CodeInvalidRequest)
	if err := c.MarkAwaitingReview(turn.ID, review); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Turn(turn.ID)
	sess, _ := c.Session(uid1, s.ID)
	if got.State != StateAwaitingReview || got.Refund != (Budget{8, 990, 55}) || got.CandidateDigest != dg("candidate") ||
		sess.Consumed != (Budget{2, 10, 5}) || sess.Reserved != (Budget{}) || sess.Revision != 2 {
		t.Fatalf("awaiting review: turn=%+v session=%+v", got, sess)
	}
	_, err := submit(c, uid1, s.ID, 2, "k2", "p", b0)
	wantCode(t, err, CodePendingReview)
	_, err = submit(c, uid2, other.ID, 0, "k", "p", b0)
	wantCode(t, err, CodeProjectBusy)
	wantCode(t, complete(c, turn.ID, OutcomeNoDiff, Budget{2, 10, 5}, b0), CodeInvalidRequest) // a reviewed turn cannot end no_diff

	autoSession := mustSession(t, c, uid1, "gamma", "auto", bigBudget, "s3")
	at := mustSubmit(t, c, uid1, autoSession.ID, 0, "k", "p")
	mustLink(t, c, at.ID)
	wantCode(t, c.MarkAwaitingReview(at.ID, reviewFor(c, at.ID, Budget{1, 1, 1}, dg("c"))), CodeInvalidRequest)
}

func TestUnknownBlocksSessionAndProject(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	reg := signedRegistration(t, nil)
	c := openAt(t, path, reg)
	s := mustSession(t, c, uid1, "alpha", "review", sessionBudget, "s")
	other := mustSession(t, c, uid2, "alpha", "review", bigBudget, "s2")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	wantCode(t, c.MarkTurnUnknown(turn.ID, "no verdict"), CodeWrongTurnState) // not linked
	mustLink(t, c, turn.ID)
	wantCode(t, c.MarkTurnUnknown(turn.ID, ""), CodeInvalidRequest)
	if err := c.MarkTurnUnknown(turn.ID, "no verifiable disposition"); err != nil {
		t.Fatal(err)
	}
	check := func(c *Catalog) {
		t.Helper()
		sess, _ := c.Session(uid1, s.ID)
		got, _ := c.Turn(turn.ID)
		if got.State != StateUnknown || !sess.Blocked || sess.Consumed != turn.Reservation || sess.Reserved != (Budget{}) {
			t.Fatalf("unknown: turn=%+v session=%+v", got, sess)
		}
		_, err := submit(c, uid1, s.ID, sess.Revision, "k2", "p", b0)
		wantCode(t, err, CodePredecessorUnresolved)
		_, err = submit(c, uid2, other.ID, 0, "k", "p", b0)
		wantCode(t, err, CodeProjectBlocked)
		_, _, err = c.CreateSession(uid2, CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "review", Budget: bigBudget, RequestKey: "s3"}, b0)
		wantCode(t, err, CodeProjectBlocked)
		wantCode(t, complete(c, turn.ID, OutcomeNoDiff, Budget{1, 1, 1}, b0), CodeWrongTurnState)
	}
	check(c)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	check(openAt(t, path, reg))
}

func TestLifecycleWrongStatesAndUnknownTurns(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k", "p")
	missing := s.ID + "/turn-9"
	wantCode(t, c.LinkTurn(missing, TurnLink{}), CodeUnknownTurn)
	wantCode(t, complete(c, missing, OutcomeNoDiff, Budget{}, b0), CodeUnknownTurn)
	wantCode(t, c.AbandonUnlinkedTurn(missing), CodeUnknownTurn)
	wantCode(t, c.MarkTurnUnknown(missing, "x"), CodeUnknownTurn)
	wantCode(t, complete(c, turn.ID, OutcomeNoDiff, Budget{1, 1, 1}, b0), CodeWrongTurnState) // admitted, not linked
	mustLink(t, c, turn.ID)
	n := strings.ReplaceAll(turn.ID, "/", "-")
	wantCode(t, c.LinkTurn(turn.ID, TurnLink{}), CodeWrongTurnState)
	wantCode(t, c.AbandonUnlinkedTurn(turn.ID), CodeWrongTurnState)
	if err := complete(c, turn.ID, OutcomeNoDiff, Budget{1, 1, 1}, b0); err != nil {
		t.Fatal(err)
	}
	wantCode(t, complete(c, turn.ID, OutcomeNoDiff, Budget{1, 1, 1}, b0), CodeWrongTurnState)

	next := mustSubmit(t, c, uid1, s.ID, 2, "k2", "p")
	bad := TurnLink{WorkflowID: "wf-" + n, RunID: "r", SessionID: "s", ConversationID: "c",
		ProviderJournalPath: "/p", StoreJournalPath: "/s", CommandJournalPath: "/c"}
	wantCode(t, c.LinkTurn(next.ID, bad), CodeInvalidRequest) // workflow IDs are fresh per turn
	bad.WorkflowID = "wf-new"
	bad.StoreJournalPath = "relative/path"
	wantCode(t, c.LinkTurn(next.ID, bad), CodeInvalidRequest)
}

func TestRefusedLaunchFailsWithRefundAndBlocks(t *testing.T) {
	c, path := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "auto", sessionBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	wantCode(t, c.RefuseUnlinkedTurn(turn.ID, CodeTurnActive), CodeInvalidRequest)
	if err := c.RefuseUnlinkedTurn(turn.ID, CodePredecessorEvidenceInvalid); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Turn(turn.ID)
	sess, _ := c.Session(uid1, s.ID)
	if got.State != StateTerminal || got.Outcome != OutcomeFailed || got.RefusalCode != CodePredecessorEvidenceInvalid ||
		got.Refund != turn.Reservation || sess.Reserved != (Budget{}) || sess.Consumed != (Budget{}) || !sess.Blocked {
		t.Fatalf("refused: turn=%+v session=%+v", got, sess)
	}
	_, err := submit(c, uid1, s.ID, sess.Revision, "k2", "p", b0)
	wantCode(t, err, CodePredecessorUnresolved)
	other := mustSession(t, c, uid1, "alpha", "auto", sessionBudget, "s2")
	stale := mustSubmit(t, c, uid1, other.ID, 0, "k3", "p")
	if err := c.RefuseUnlinkedTurn(stale.ID, CodeStaleBaseline); err != nil {
		t.Fatal(err)
	}
	_, _, err = c.CreateSession(uid1, CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "auto", Budget: sessionBudget, RequestKey: "s4"}, b0)
	wantCode(t, err, CodeProjectBlocked)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openAt(t, path, signedRegistration(t, nil))
	if again, _ := reopened.Turn(turn.ID); !reflect.DeepEqual(again, got) {
		t.Fatalf("refused turn after reopen = %+v, want %+v", again, got)
	}
}

func TestAbandonRefundsFullReservation(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", "review", sessionBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	if err := c.AbandonUnlinkedTurn(turn.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Turn(turn.ID)
	sess, _ := c.Session(uid1, s.ID)
	if got.State != StateAbandoned || got.Refund != turn.Reservation || sess.Reserved != (Budget{}) || sess.Consumed != (Budget{}) || sess.Revision != 2 {
		t.Fatalf("abandon: turn=%+v session=%+v", got, sess)
	}
	next := mustSubmit(t, c, uid1, s.ID, 2, "k2", "p")
	if next.ID != s.ID+"/turn-2" || next.PredecessorTurnID != "" || next.Reservation != turn.Reservation {
		t.Fatalf("resubmission: %+v", next)
	}
}

func TestOpenReplaysEverySessionTurnAndBudgetField(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	reg := signedRegistration(t, nil)
	c := openAt(t, path, reg)

	s1 := mustSession(t, c, uid1, "alpha", "auto", bigBudget, "s1")
	t1 := mustSubmit(t, c, uid1, s1.ID, 0, "k1", "one")
	finishNoDiff(t, c, t1.ID, Budget{3, 30, 7})
	t2 := mustSubmit(t, c, uid1, s1.ID, 2, "k2", "two")
	if err := c.AbandonUnlinkedTurn(t2.ID); err != nil {
		t.Fatal(err)
	}
	t3 := mustSubmit(t, c, uid1, s1.ID, 4, "k3", "three")
	mustLink(t, c, t3.ID)
	if err := complete(c, t3.ID, OutcomePublished, Budget{5, 50, 9}, b1); err != nil {
		t.Fatal(err)
	}
	s2 := mustSession(t, c, uid2, "beta", "review", bigBudget, "s2")
	t4 := mustSubmit(t, c, uid2, s2.ID, 0, "k4", "four")
	mustLink(t, c, t4.ID)
	if err := c.MarkAwaitingReview(t4.ID, reviewFor(c, t4.ID, Budget{1, 2, 3}, dg("cand"))); err != nil {
		t.Fatal(err)
	}
	s3 := mustSession(t, c, uid1, "gamma", "auto", bigBudget, "s3")
	t5 := mustSubmit(t, c, uid1, s3.ID, 0, "k5", "five")
	mustLink(t, c, t5.ID)
	if err := c.MarkTurnUnknown(t5.ID, "lost"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openAt(t, path, reg)
	if reopened.st.epoch != 2 {
		t.Fatalf("owner epoch = %d, want 2", reopened.st.epoch)
	}
	if !reflect.DeepEqual(c.st.sessions, reopened.st.sessions) || !reflect.DeepEqual(c.st.turns, reopened.st.turns) ||
		!reflect.DeepEqual(c.st.projects, reopened.st.projects) || !reflect.DeepEqual(c.st.keys, reopened.st.keys) ||
		!reflect.DeepEqual(c.st.linkIDs, reopened.st.linkIDs) {
		t.Fatal("reopened state differs from the state that wrote the journal")
	}
	if got := reopened.st.projects["alpha"].baseline; got != b1 {
		t.Fatalf("alpha baseline after reopen = %s, want %s", got, b1)
	}
	got, _ := reopened.Turn(t3.ID)
	if got.State != StateTerminal || got.Outcome != OutcomePublished || got.ResultBaselineDigest != b1 || got.Prompt != "three" {
		t.Fatalf("turn 3 after reopen: %+v", got)
	}
	if _, err := submit(reopened, uid1, s1.ID, 6, "k6", "six", b1); err != nil {
		t.Fatalf("a reopened catalog must keep serving the session: %v", err)
	}
}

func TestChildAdmitHelper(t *testing.T) {
	path := os.Getenv("TB_PRODUCT_CHILD_PATH")
	if path == "" {
		return
	}
	c, err := Open(path, signedRegistration(t, nil))
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	if _, err := submit(c, uid1, os.Getenv("TB_PRODUCT_CHILD_SESSION"), 0, "child-key", "from the child", b0); err != nil {
		t.Fatalf("child submit: %v", err)
	}
	// The child exits without linking or closing: the admission is all that is durable.
}

func TestFreshProcessReconcilesAnUnlinkedAdmission(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	reg := signedRegistration(t, nil)
	c := openAt(t, path, reg)
	s := mustSession(t, c, uid1, "alpha", "review", sessionBudget, "s")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChildAdmitHelper$")
	child.Env = append(os.Environ(), "TB_PRODUCT_CHILD_PATH="+path, "TB_PRODUCT_CHILD_SESSION="+s.ID)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child process: %v\n%s", err, out)
	}

	c = openAt(t, path, reg)
	turn, ok := c.Turn(s.ID + "/turn-1")
	if !ok || turn.State != StateAdmitted || turn.Link != nil || turn.Prompt != "from the child" {
		t.Fatalf("the admitted, unlinked turn must survive the restart: %+v ok=%v", turn, ok)
	}
	if _, err := submit(c, uid1, s.ID, 1, "new-key", "again", b0); !errors.Is(err, ErrCode(CodeTurnActive)) {
		t.Fatalf("an unlinked admission blocks the session until reconciled: %v", err)
	}
	if err := c.AbandonUnlinkedTurn(turn.ID); err != nil {
		t.Fatal(err)
	}
	sess, _ := c.Session(uid1, s.ID)
	if sess.Reserved != (Budget{}) || sess.Consumed != (Budget{}) || sess.Revision != 2 {
		t.Fatalf("abandon must refund fully: %+v", sess)
	}
	again, err := submit(c, uid1, s.ID, 2, "new-key", "again", b0)
	if err != nil || again.Replayed || again.Turn.ID != s.ID+"/turn-2" {
		t.Fatalf("resubmission with a new key: %+v %v", again, err)
	}
	old, err := submit(c, uid1, s.ID, 0, "child-key", "from the child", b0)
	if err != nil || !old.Replayed || old.Turn.State != StateAbandoned {
		t.Fatalf("the abandoned admission's key stays consumed: %+v %v", old, err)
	}
}

func TestOpenRefusesRecordsTheReducerRejects(t *testing.T) {
	good := func(kind string, record any) audit.Event {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		return audit.Event{Kind: kind, ID: "evidence", Data: data}
	}
	cases := map[string]audit.Event{
		"terminal for an unknown turn": good(kindTurnTerminal, turnTerminalRecord{header: hdr, TurnID: "psess-x/turn-1",
			Outcome: OutcomeNoDiff, Settlement: "verified", Receipt: rawReceipt("x"), ReceiptDigest: ReceiptDigest(rawReceipt("x")), ResultBaselineDigest: b0}),
		"link for an unknown turn": good(kindTurnLinked, turnLinkedRecord{header: hdr, TurnID: "psess-x/turn-1"}),
		"session on an unknown project": good(kindSessionCreated, sessionCreatedRecord{header: hdr,
			SessionID: "psess-" + strings.Repeat("a", 32), Principal: uid1, ProjectID: "nope", ProviderProfile: "p1",
			PublicationMode: "auto", Budget: bigBudget, BaselineDigest: b0, RequestKey: "k",
			PayloadDigest: payloadDigest(opCreateSession, CreateSessionRequest{ProjectID: "nope", ProviderProfile: "p1",
				PublicationMode: "auto", Budget: bigBudget, RequestKey: "k"})}),
		"unknown kind":         {Kind: "product_something_else", ID: "evidence", Data: []byte(`{"schema_version":"tbound-product-catalog/v1"}`)},
		"unknown field":        {Kind: kindTurnAbandoned, ID: "evidence", Data: []byte(`{"schema_version":"tbound-product-catalog/v1","turn_id":"a","refund":{},"extra":1}`)},
		"wrong schema version": {Kind: kindTurnAbandoned, ID: "evidence", Data: []byte(`{"schema_version":"v0","turn_id":"a","refund":{}}`)},
		"skipped owner epoch":  good(kindOpened, openedRecord{header: hdr, OwnerEpoch: 5, RegistrationDigest: dg("x")}),
		"non-JSON record body": {Kind: kindTurnAbandoned, ID: "evidence", Data: []byte("nope")},
	}
	for name, event := range cases {
		path := filepath.Join(privateDir(t), "catalog.jsonl")
		reg := signedRegistration(t, nil)
		c, err := Open(path, reg)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		j, err := audit.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.Append(event); err != nil {
			t.Fatalf("%s: append: %v", name, err)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, reg); !errors.Is(err, ErrCode(CodeCatalogCorrupt)) {
			t.Errorf("%s: Open err=%v, want CATALOG_CORRUPT", name, err)
		}
	}
}

func TestOpenRefusesARecordBeforeTheFirstOpen(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	j, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(turnAbandonedRecord{header: hdr, TurnID: "a"})
	if _, err := j.Append(audit.Event{Kind: kindTurnAbandoned, ID: "a", Data: data}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	_, err = Open(path, signedRegistration(t, nil))
	wantCode(t, err, CodeCatalogCorrupt)
}

func TestRegistrationChangeRefusedWhileATurnIsUnresolved(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	regA := signedRegistration(t, nil)
	regB := signedRegistration(t, func(r *Registration) { r.Limits.ReviewTimeoutSeconds = 601 })
	if regA.Digest == regB.Digest {
		t.Fatal("the fixture registrations must differ")
	}
	c := openAt(t, path, regA)
	s := mustSession(t, c, uid1, "alpha", "review", bigBudget, "s")
	turn := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path, regB)
	wantCode(t, err, CodePredecessorUnresolved)

	c = openAt(t, path, regA)
	if err := c.AbandonUnlinkedTurn(turn.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c = openAt(t, path, regB) // nothing is unresolved, so the new registration is accepted
	if _, err := submit(c, uid1, s.ID, 2, "k2", "p", b0); err != nil {
		t.Fatalf("a session created under the old registration keeps working under the new one: %v", err)
	}
}

func TestSessionsOfListsOnlyThePrincipalsOwnSessions(t *testing.T) {
	c, _ := newCatalog(t)
	mine := mustSession(t, c, uid1, "alpha", "review", sessionBudget, "k1")
	_, _, err := c.CreateSession(uid2, CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "review",
		Budget: sessionBudget, RequestKey: "k1"}, b0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.SessionsOf(uid1)
	if err != nil || len(got) != 1 || got[0].ID != mine.ID {
		t.Fatalf("SessionsOf(uid1) = %+v, %v", got, err)
	}
	got, err = c.SessionsOf(uidBad)
	if got != nil {
		t.Fatalf("an unregistered principal listed sessions: %+v", got)
	}
	wantCode(t, err, CodeUnauthorizedPrincipal)
}
