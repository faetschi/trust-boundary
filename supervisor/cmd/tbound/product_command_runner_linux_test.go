//go:build linux

package main

import (
	"context"

	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

func TestProductD09CleanupFailureRetainsOwnersAndBlocksDisposition(t *testing.T) {
	for _, path := range []string{"close", "accept", "discard", "expiry", "cancel", "shutdown"} {
		t.Run(path, func(t *testing.T) {
			f := newProductDriverFixture(t, productsession.PublicationReview)
			_, turn, err := f.run(t, f.submit(t, "review", "write task"), &productDriverScript{turns: []providerbridge.ScriptTurn{
				{ResponseID: "write-response", CallID: "write-call", Tool: "write", Arguments: []byte(`{"path":"task.txt","content":"new\n"}`)},
				scopeDriverFinal("review", scopeDriverAnswer)}})
			if err != nil || turn.State != productsession.StateAwaitingReview {
				t.Fatalf("review: %+v %v", turn, err)
			}
			review := f.env.pending[turn.ID]
			failure := errors.New("retained cell owner")
			fail := true
			runner := newProductD09CommandRunnerWithFactory(func(string) (sessionrepo.CommandRunner, error) { return nil, errors.New("unused") }, func(context.Context) error {
				if fail {
					return failure
				}
				return nil
			})
			review.w.commandLifecycle = runner
			closed := false
			review.w.closers = append(review.w.closers, func() error { closed = true; return nil })
			switch path {
			case "close":
				err = review.w.close()
			case "accept", "discard":
				decision := productsession.DecisionPublish
				if path == "discard" {
					decision = productsession.DecisionDiscard
				}
				_, err = f.resolve(t, turn.ID, "resolve", turn.CandidateDigest, decision)
			case "expiry":
				err = f.env.ExpireReviews(time.Now().Add(time.Hour))
			case "cancel":
				_, err = f.cancel(t, turn.ID, "cancel")
			case "shutdown":
				err = f.env.Close()
			}
			if !errors.Is(err, failure) || closed || len(f.env.retainedCommands) != 1 || runner.Pending() == 0 {
				t.Fatalf("cleanup did not retain owners: err=%v closed=%v retained=%d pending=%d", err, closed, len(f.env.retainedCommands), runner.Pending())
			}
			got, _ := f.env.Catalog.Turn(turn.ID)
			if path != "close" && got.State != productsession.StateUnknown {
				t.Fatalf("disposition escaped cleanup: %+v", got)
			}
			fail = false
			if err := f.env.Close(); err != nil || !closed || runner.Pending() != 0 || len(f.env.retainedCommands) != 0 {
				t.Fatalf("retry: closed=%v err=%v", closed, err)
			}
		})
	}
}

// A failed command settlement keeps the owner and its handles. The first cleanup attempt fails and the
// retry would succeed, so a deferred teardown that retried it would release a retained owner.
func TestProductD09FailedTurnSettlementKeepsOwnerAgainstDeferredTeardown(t *testing.T) {
	failure := errors.New("retained cell owner")
	attempts, cleaned := 0, false
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, []string{"task.txt"}, nil)
	f.env.NewCommandRunner = func(productTurnCommandContext) (productTurnCommandRunner, error) {
		return newProductD09CommandRunnerWithFactory(func(string) (sessionrepo.CommandRunner, error) {
			return nil, errors.New("unused")
		}, func(context.Context) error {
			attempts++
			if attempts == 1 {
				return failure
			}
			cleaned = true
			return nil
		}), nil
	}
	admission := f.submit(t, "settle", "read the task")
	_, turn, err := f.run(t, admission, &productDriverScript{turns: []providerbridge.ScriptTurn{scopeDriverFinal("done", scopeDriverAnswer)}})
	if !errors.Is(err, failure) || turn.State != productsession.StateUnknown {
		t.Fatalf("failed settlement: state=%s err=%v", turn.State, err)
	}
	if attempts != 1 || cleaned || len(f.env.retainedCommands) != 1 {
		t.Fatalf("deferred teardown retried the owner: attempts=%d cleaned=%v retained=%d", attempts, cleaned, len(f.env.retainedCommands))
	}
	for w := range f.env.retainedCommands {
		if len(w.closers) == 0 {
			t.Fatal("retained workflow released its handles")
		}
	}
	if err := f.env.Close(); err != nil || !cleaned || len(f.env.retainedCommands) != 0 {
		t.Fatalf("explicit retained cleanup: cleaned=%v err=%v retained=%d", cleaned, err, len(f.env.retainedCommands))
	}
	if got := f.turn(t, admission.Turn.ID); got.State != productsession.StateUnknown {
		t.Fatalf("retained cleanup changed the disposition: %s", got.State)
	}
}

// A review decision whose settlement fails keeps its owner the same way, on accept and on discard.
func TestProductD09FailedReviewSettlementKeepsOwnerAgainstDeferredTeardown(t *testing.T) {
	for _, path := range []string{"accept", "discard"} {
		t.Run(path, func(t *testing.T) {
			f := newProductDriverFixture(t, productsession.PublicationReview)
			_, turn, err := f.run(t, f.submit(t, "review", "write task"), &productDriverScript{turns: []providerbridge.ScriptTurn{
				{ResponseID: "write-response", CallID: "write-call", Tool: "write", Arguments: []byte(`{"path":"task.txt","content":"new\n"}`)},
				scopeDriverFinal("review", scopeDriverAnswer)}})
			if err != nil || turn.State != productsession.StateAwaitingReview {
				t.Fatalf("review: %+v %v", turn, err)
			}
			review := f.env.pending[turn.ID]
			failure := errors.New("retained cell owner")
			attempts, closed := 0, false
			review.w.commandLifecycle = newProductD09CommandRunnerWithFactory(func(string) (sessionrepo.CommandRunner, error) {
				return nil, errors.New("unused")
			}, func(context.Context) error {
				attempts++
				if attempts == 1 {
					return failure
				}
				return nil
			})
			review.w.closers = append(review.w.closers, func() error { closed = true; return nil })
			decision := productsession.DecisionPublish
			if path == "discard" {
				decision = productsession.DecisionDiscard
			}
			if _, err := f.resolve(t, turn.ID, "resolve", turn.CandidateDigest, decision); !errors.Is(err, failure) {
				t.Fatalf("failed settlement not reported: %v", err)
			}
			if got := f.turn(t, turn.ID); got.State != productsession.StateUnknown || attempts != 1 || closed || len(f.env.retainedCommands) != 1 {
				t.Fatalf("deferred teardown retried the owner: state=%s attempts=%d closed=%v retained=%d", got.State, attempts, closed, len(f.env.retainedCommands))
			}
			if err := f.env.Close(); err != nil || !closed || len(f.env.retainedCommands) != 0 {
				t.Fatalf("explicit retained cleanup: closed=%v err=%v retained=%d", closed, err, len(f.env.retainedCommands))
			}
		})
	}
}

func TestProductD09FactoryAndRetainedHistoryComposition(t *testing.T) {
	if root := os.Getenv("TB_PRODUCT_F_ROOT"); root != "" {
		f := openBashProductDriverFixture(t, root, nil, []string{"task.txt"})
		installProductTrustCell(f, productTrustCell)
		turn, ok := f.env.Catalog.Turn(os.Getenv("TB_PRODUCT_F_NEXT"))
		if !ok {
			t.Fatal("missing successor")
		}
		script := &productDriverScript{turns: []providerbridge.ScriptTurn{scopeDriverFinal("next", "Read it.")}, check: func(history []providerbridge.ScriptMessage) error {
			at := messageIndex(history, 0, func(m providerbridge.ScriptMessage) bool { return m.Role == "tool" && m.ToolCallID == "call-append" })
			if at < 0 || !strings.Contains(history[at].Content, `ok\n`) {
				return errors.New("published Bash output missing")
			}
			return nil
		}}
		receipt, next, err := f.run(t, productsession.Admission{Turn: turn}, script)
		if err != nil || next.Outcome != productsession.OutcomeNoDiff || receipt.PredecessorReceiptDigest == "" || script.requests() != 1 {
			t.Fatalf("fresh next turn: %+v %v", next, err)
		}
		return
	}
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, []string{"task.txt"}, nil)
	installProductTrustCell(f, productTrustCell)
	receipt, turn, err := f.run(t, f.submit(t, "command", "append task"), &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("append", "append task"), scopeDriverFinal("done", scopeDriverAnswer)}})
	if err != nil || turn.Outcome != productsession.OutcomePublished || receipt.PublicationTransactionID == "" || fileContent(t, f.projectDir, "task.txt") != "turn fixture\nbash-stage\n" {
		t.Fatalf("real trust publication: %+v %+v %v", turn, receipt, err)
	}
	journal, err := audit.OpenExisting(turn.Link.CommandJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if receipt.CommandJournalCount != 1 || receipt.CommandJournalHead != auditHeadHash(trace.Records) {
		t.Fatalf("command commitment: %+v", receipt)
	}
	next := f.submit(t, "next", "read task")
	raw, err := os.ReadFile(turn.Link.CommandJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "reused", "altered"} {
		broken := []byte{}
		switch name {
		case "reused":
			broken = append(append([]byte{}, raw...), raw...)
		case "altered":
			broken = append([]byte{}, raw...)
			broken[len(broken)/2] ^= 1
		}
		if err := os.WriteFile(turn.Link.CommandJournalPath, broken, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.env.predecessorContext(next.Turn, productDriverProfile); err == nil {
			t.Fatalf("accepted %s command receipt", name)
		}
	}
	if err := os.WriteFile(turn.Link.CommandJournalPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Catalog.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestProductD09FactoryAndRetainedHistoryComposition$", "-test.v")
	command.Env = append(os.Environ(), "TB_PRODUCT_F_ROOT="+f.root, "TB_PRODUCT_F_NEXT="+next.Turn.ID)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh published predecessor replay: %v\n%s", err, output)
	}
}
func TestProductD09ScopeOwnerJournal(t *testing.T) {
	path := filepath.Join(privateGovernedTestRoot(t), productCommandOwnerJournal)
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := productD09ScopeOwner{Schema: "tbound-product-d09-scope-owner/v1", State: "opened", WorkflowID: "workflow", RunID: "run",
		RegistrationDigest: digestBytes([]byte("registration")), ProfileDigest: digestBytes([]byte("profile")), RootDigest: digestBytes([]byte("root")),
		CommandOwnershipVersion: 1, ScopeIdentity: digestBytes([]byte("scope")), ScopePath: "/sys/fs/cgroup/system.slice/tbound-product.service/cells/turn-owned"}
	if err := persistProductCommandScopeOwner(journal, owner); err != nil {
		t.Fatal(err)
	}
	owner.State = "drained"
	if err := persistProductCommandScopeOwner(journal, owner); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = audit.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if err := reconcileProductCommandScope(context.Background(), nil, journal, map[string]struct{}{}, nil); err != nil {
		t.Fatalf("durable drained owner: %v", err)
	}
	owner.ScopePath = "/sys/fs/cgroup/other/turn-owned"
	if err := persistProductCommandScopeOwner(journal, owner); err == nil {
		t.Fatal("accepted scope outside product cells")
	}
}

// A scope allocation that fails after its owner journal exists leaves an empty journal and no child:
// startup proceeds. A child left behind without an owner record still blocks it.
func TestProductD09EmptyScopeOwnerJournalDoesNotBlockStartup(t *testing.T) {
	retained := privateGovernedTestRoot(t)
	createEmptyProductScopeOwnerJournal(t, retained, "turn-allocation-failed")
	if err := reconcileProductCommandScopesUnder(context.Background(), openProductCellsRoot(t, t.TempDir()), retained); err != nil {
		t.Fatalf("empty owner journal blocked startup: %v", err)
	}
}

func TestProductD09EmptyScopeOwnerJournalStillBlocksAnUnrecordedChild(t *testing.T) {
	retained := privateGovernedTestRoot(t)
	createEmptyProductScopeOwnerJournal(t, retained, "turn-allocation-failed")
	cells := t.TempDir()
	if err := os.Mkdir(filepath.Join(cells, "turn-unrecorded"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := reconcileProductCommandScopesUnder(context.Background(), openProductCellsRoot(t, cells), retained)
	if !errors.Is(err, piruntime.ErrUnknownSettlement) || !strings.Contains(err.Error(), "turn-unrecorded") {
		t.Fatalf("unrecorded child did not block startup: %v", err)
	}
}

func createEmptyProductScopeOwnerJournal(t *testing.T, retained, turn string) {
	t.Helper()
	dir := filepath.Join(retained, turn)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(dir, productCommandOwnerJournal))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
}

func openProductCellsRoot(t *testing.T, dir string) *os.File {
	t.Helper()
	root, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

type unqualifiedProductRunner struct{ *productD09TurnRunner }

func TestProductD09UnqualifiedTrustRefusalSettlesBeforeAbandon(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, []string{"task.txt"}, nil)
			cleanupFailed := fail
			cleaned := false
			factories := 0
			f.env.NewCommandRunner = func(productTurnCommandContext) (productTurnCommandRunner, error) {
				factories++
				return unqualifiedProductRunner{newProductD09CommandRunnerWithFactory(func(string) (sessionrepo.CommandRunner, error) {
					t.Fatal("refused turn launched a command")
					return nil, nil
				}, func(context.Context) error {
					if cleanupFailed {
						return errors.New("scope cleanup failed")
					}
					cleaned = true
					return nil
				})}, nil
			}
			f.env.PublicationTrust = func(a *SessionOperationAuthority) workflow.Trust {
				return &productPublicationTrust{env: f.env, authority: a, xattr: func() (workspace.XattrVisibilityAttestation, error) { return f.env.Xattr, nil }}
			}
			admission := f.submit(t, "refusal", "bash task")
			_, turn, err := f.run(t, admission, &productDriverScript{})
			if err == nil {
				t.Fatal("current trust accepted Bash")
			}
			if fail {
				if turn.State != productsession.StateAdmitted || len(f.env.retainedCommands) != 1 || cleaned || f.currentSession(t).Reserved == (productsession.Budget{}) {
					t.Fatalf("failed cleanup refunded/forgot owner: %+v", turn)
				}
				if _, err := f.env.RunTurn(context.Background(), admission); err == nil || factories != 1 {
					t.Fatalf("retry bypassed retained owner: factories=%d err=%v", factories, err)
				}
				cancelled, err := f.cancel(t, turn.ID, "cancel-retained")
				if err == nil || cancelled.State != productsession.StateAdmitted || f.currentSession(t).Reserved == (productsession.Budget{}) {
					t.Fatalf("cancellation refunded retained owner: %+v %v", cancelled, err)
				}
			} else if turn.State != productsession.StateAbandoned || !cleaned {
				t.Fatalf("refusal was not cleaned before abandon: %+v", turn)
			}
			cleanupFailed = false
			if err := f.env.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
