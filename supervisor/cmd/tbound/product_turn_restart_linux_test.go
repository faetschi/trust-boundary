//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/sessionrepo"
)

const (
	productCrashRootEnv    = "TB_PRODUCT_CRASH_ROOT"
	productCrashPointEnv   = "TB_PRODUCT_CRASH_POINT"
	productCrashSessionEnv = "TB_PRODUCT_CRASH_SESSION"
	productCrashExit       = 7
)

// TestProductTurnCrashChild runs only as the fresh process the crash matrix starts. It drives turn 1
// to the named boundary and exits there without any cleanup.
func TestProductTurnCrashChild(t *testing.T) {
	root := os.Getenv(productCrashRootEnv)
	if root == "" {
		t.Skip("fresh-process crash helper")
	}
	point := os.Getenv(productCrashPointEnv)
	f := openProductDriverFixture(t, root)
	f.session.ID = os.Getenv(productCrashSessionEnv)
	f.env.crashAt = func(at string) {
		if at == point {
			os.Exit(productCrashExit)
		}
	}
	admission := f.submit(t, "turn-1", productDriverPromptA)
	if point == "after-admission" {
		os.Exit(productCrashExit)
	}
	_, turn, err := f.run(t, admission, productDriverTurnOne(false))
	if err != nil || turn.State != productsession.StateAwaitingReview {
		t.Fatalf("turn 1 before the crash point %s: %+v %v", point, turn, err)
	}
	_, err = f.resolve(t, turn.ID, "resolve-1", turn.CandidateDigest, productsession.DecisionPublish)
	t.Fatalf("the child reached no crash point %s: %v", point, err)
}

func TestProductTurnRestartReconcilesEveryCrashBoundary(t *testing.T) {
	for _, c := range []struct {
		point, mode string
		state       productsession.TurnState
		outcome     string
		reason      string
		attempts    int // tool operations in the retained Store; -1 for an unlinked turn
		published   bool
	}{
		{"after-admission", productsession.PublicationAuto, productsession.StateAbandoned, "", "", -1, false},
		{"before-first-exchange", productsession.PublicationAuto, productsession.StateTerminal, productsession.OutcomeFailed, "", 0, false},
		{"after-effect", productsession.PublicationAuto, productsession.StateTerminal, productsession.OutcomeFailed, "", 1, false},
		{"after-awaiting-review", productsession.PublicationReview, productsession.StateTerminal, productsession.OutcomeDiscarded,
			productsession.ReasonRestart, 1, false},
		{"after-resolution", productsession.PublicationReview, productsession.StateTerminal, productsession.OutcomeDiscarded,
			productsession.ReasonRestart, 1, false},
		{"after-publication", productsession.PublicationReview, productsession.StateTerminal, productsession.OutcomePublished, "", 1, true},
	} {
		t.Run(c.point, func(t *testing.T) {
			created := newProductDriverFixture(t, c.mode)
			baseline := created.measure(t)
			if err := created.env.Catalog.Close(); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestProductTurnCrashChild$", "-test.count=1")
			child.Env = append(os.Environ(), productCrashRootEnv+"="+created.root, productCrashPointEnv+"="+c.point,
				productCrashSessionEnv+"="+created.session.ID)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != productCrashExit {
				t.Fatalf("child did not crash at %s: %v\n%s", c.point, err, output)
			}

			f := openProductDriverFixture(t, created.root)
			f.session = created.session
			id := f.session.ID + "/turn-1"
			before := f.turn(t, id)
			if before.State != productsession.StateAdmitted && before.State != productsession.StateLinked &&
				before.State != productsession.StateAwaitingReview {
				t.Fatalf("the child settled the turn before crashing: %+v", before)
			}
			if err := f.env.ReconcileAfterRestart(context.Background()); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			turn, session := f.turn(t, id), f.currentSession(t)
			if turn.State != c.state || turn.Outcome != c.outcome || turn.TerminalReason != c.reason ||
				f.notesPublished(t) != c.published || session.Reserved != (productsession.Budget{}) || session.Blocked ||
				session.Consumed != turn.Usage {
				t.Fatalf("after restart: turn=%+v session=%+v", turn, session)
			}
			if c.published {
				if f.measure(t) != turn.CandidateDigest {
					t.Fatal("the published project is not the candidate")
				}
			} else if f.measure(t) != baseline {
				t.Fatal("the project changed")
			}
			if c.attempts >= 0 {
				paths, err := productTurnPathsFromLink(*turn.Link)
				if err != nil {
					t.Fatal(err)
				}
				retained, err := openRetainedProductTurn(paths, f.env.storeOptions(turn))
				if err != nil {
					t.Fatal(err)
				}
				bundle, err := retained.Store.EvidenceBundle()
				disposition, reconstructErr := sessionrepo.Reconstruct(bundle)
				_ = retained.Close()
				if err != nil || reconstructErr != nil || len(disposition.Operations) != c.attempts || disposition.Successes != disposition.Attempts {
					t.Fatalf("retained Store operations %d, successes %d of %d: %v %v", len(disposition.Operations), disposition.Successes, disposition.Attempts, err, reconstructErr)
				}
				if before.State == productsession.StateAwaitingReview &&
					sha256DigestFromBarePin(bareStoreTreeDigest(bundle.ValidatedChain.SealedTreeDigest)) != turn.CandidateDigest {
					t.Fatal("the discarded candidate is not retained in the Store")
				}
				if c.outcome == productsession.OutcomeFailed {
					hostCalls, tokens := productDriverExpectedUsage(t, paths.ProviderJournalPath, c.attempts)
					if turn.Settlement != productsession.SettlementReconciledAfterRestart || turn.Usage.HostCalls != hostCalls ||
						turn.Usage.Tokens != tokens || turn.Usage.DurationSeconds != turn.Reservation.DurationSeconds {
						t.Fatalf("reconciled usage %+v settlement %s, ledger model host calls %d tokens %d",
							turn.Usage, turn.Settlement, hostCalls, tokens)
					}
				}
			}
			f.followUp(t, "turn-2", c.outcome)
		})
	}
}

// A linked turn whose project changed while its process was gone is unknown: the reservation stays
// consumed and the project stays blocked.
func TestProductTurnRestartRecordsAChangedProjectAsUnknown(t *testing.T) {
	created := newProductDriverFixture(t, productsession.PublicationAuto)
	if err := created.env.Catalog.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProductTurnCrashChild$", "-test.count=1")
	child.Env = append(os.Environ(), productCrashRootEnv+"="+created.root, productCrashPointEnv+"=after-effect",
		productCrashSessionEnv+"="+created.session.ID)
	if output, err := child.CombinedOutput(); err == nil {
		t.Fatalf("child did not crash:\n%s", output)
	}
	writeProductDriverFile(t, created.projectDir, "outside.txt", "written while the service was down\n")
	f := openProductDriverFixture(t, created.root)
	f.session = created.session
	if err := f.env.ReconcileAfterRestart(context.Background()); err == nil {
		t.Fatal("a changed project reconciled without an error")
	}
	turn, session := f.turn(t, f.session.ID+"/turn-1"), f.currentSession(t)
	if turn.State != productsession.StateUnknown || !session.Blocked || session.Consumed != turn.Reservation {
		t.Fatalf("changed project after restart: turn=%+v session=%+v", turn, session)
	}
	if _, err := f.env.Catalog.SubmitTurn(f.principal, productsession.SubmitTurnRequest{SessionID: f.session.ID,
		ExpectedRevision: session.Revision, RequestKey: "turn-2", Prompt: productDriverPromptB}, f.measure(t)); !errors.Is(err,
		productsession.ErrCode(productsession.CodePredecessorUnresolved)) {
		t.Fatalf("submit after an unknown turn: %v", err)
	}
}
