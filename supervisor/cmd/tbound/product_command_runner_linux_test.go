//go:build linux

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

func TestProductD09CleanupFailureRetainsOwnersAndBlocksDisposition(t *testing.T) {
	for _, path := range []string{"close", "accept", "discard", "expiry", "cancel", "shutdown"} {
		t.Run(path, func(t *testing.T) {
			f := newProductDriverFixture(t, productsession.PublicationReview)
			_, turn, err := f.run(t, f.submit(t, "review", "write task"), &productDriverScript{turns: []providerbridge.ScriptTurn{
				{ResponseID: "write-response", CallID: "write-call", Tool: "write", Arguments: []byte(`{"path":"task.txt","content":"new\n"}`)},
				scopeDriverFinal("review", scopeDriverAnswer)}})
			if err != nil || turn.State != productsession.StateAwaitingReview { t.Fatalf("review: %+v %v", turn, err) }
			review := f.env.pending[turn.ID]
			failure := errors.New("retained cell owner")
			fail := true
			runner := newProductD09CommandRunnerWithFactory(func(string) (sessionrepo.CommandRunner, error) { return nil, errors.New("unused") }, func(context.Context) error {
				if fail { return failure }; return nil
			})
			review.w.commandLifecycle = runner
			closed := false
			review.w.closers = append(review.w.closers, func() error { closed = true; return nil })
			switch path {
			case "close": err = review.w.close()
			case "accept", "discard":
				decision := productsession.DecisionPublish
				if path == "discard" { decision = productsession.DecisionDiscard }
				_, err = f.resolve(t, turn.ID, "resolve", turn.CandidateDigest, decision)
			case "expiry": err = f.env.ExpireReviews(time.Now().Add(time.Hour))
			case "cancel": _, err = f.cancel(t, turn.ID, "cancel")
			case "shutdown": err = f.env.Close()
			}
			if !errors.Is(err, failure) || closed || len(f.env.retainedCommands) != 1 || runner.Pending() == 0 {
				t.Fatalf("cleanup did not retain owners: err=%v closed=%v retained=%d pending=%d", err, closed, len(f.env.retainedCommands), runner.Pending())
			}
			got, _ := f.env.Catalog.Turn(turn.ID)
			if path != "close" && got.State != productsession.StateUnknown { t.Fatalf("disposition escaped cleanup: %+v", got) }
			fail = false
			if err := f.env.Close(); err != nil || !closed || runner.Pending() != 0 || len(f.env.retainedCommands) != 0 { t.Fatalf("retry: closed=%v err=%v", closed, err) }
		})
	}
}

func TestProductD09FactoryAndRetainedHistoryComposition(t *testing.T) {
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, []string{"task.txt"}, nil)
	profile, template, _ := productionProofFixture()
	template.CommandExecutable = "/bin/bash"
	templateDigest, _ := template.Digest()
	profile.ContainmentProfileDigest = templateDigest
	var runner *productD09TurnRunner
	f.env.NewCommandRunner = func(c productTurnCommandContext) (productTurnCommandRunner, error) {
		runner = newProductD09CommandRunnerWithFactory(func(lease string) (sessionrepo.CommandRunner, error) {
			return newProductionD09BashRunnerWithInvoker(lease, &sealedD09Cell{leaseID: lease, template: template, templateDigest: templateDigest}, c.Journal,
				c.RunID, c.SessionID, c.WorkflowID, c.ConversationID, profile, template), nil
		}, func(context.Context) error { return nil })
		return runner, nil
	}
	admission := f.submit(t, "command", "append task")
	f.script = &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("append", `printf 'bash-stage\n' >> task.txt`), scopeDriverFinal("done", scopeDriverAnswer)}}
	w, err := f.env.composeProductTurnWorkflow(context.Background(), admission.Turn, productDriverProfile, providerbridge.PredecessorContext{})
	if err != nil { t.Fatal(err) }
	defer w.close()
	worker, _ := newProductTurnWorker(8, func() bool { return false }, func(string) {})
	work, err := f.env.driveProductTurn(context.Background(), admission.Turn, w, worker)
	if err != nil || !work.finalAnswer { t.Fatalf("command chain: %+v %v", work, err) }
	if runner.Settlements() != 1 || runner.productionCommandRunner.Pending() != 0 { t.Fatalf("settlement not bound: %+v", runner) }
	if err := w.settleCommands(); err != nil || runner.Pending() != 0 { t.Fatalf("settle: %v", err) }
	trace, err := w.commandJournal.Trace()
	if err != nil || len(trace.Records) != 1 || !validBareWorkflowDigest(auditHeadHash(trace.Records)) { t.Fatalf("command commitment: %+v %v", trace, err) }
	bundle, err := w.store.EvidenceBundle()
	if err != nil { t.Fatal(err) }
	link := w.link(providerbridge.PredecessorContext{})
	history, err := productD09CommandHistory(profile, template)(w.commandJournal, link)
	if err != nil { t.Fatal(err) }
	if err := history.VerifyStoreBundle(bundle); err != nil { t.Fatal(err) }
	// Reopen from disk after releasing the writer; no live runner or lease is replay authority.
	path := filepath.Join(w.turnDir, productTurnCommandJournal)
	if err := w.close(); err != nil { t.Fatal(err) }
	journal, err := audit.OpenExisting(path)
	if err != nil { t.Fatal(err) }
	defer journal.Close()
	replayed, err := productD09CommandHistory(profile, template)(journal, link)
	if err != nil { t.Fatal(err) }
	if err := replayed.VerifyStoreBundle(bundle); err != nil { t.Fatal(err) }
}
