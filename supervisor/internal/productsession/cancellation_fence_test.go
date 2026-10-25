//go:build linux

package productsession

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
)

func cancellationFenceLink() TurnLink {
	return TurnLink{WorkflowID: "wf", RunID: "run", SessionID: "runtime", ConversationID: "conv",
		ProviderJournalPath: "/var/lib/x/provider.jsonl", StoreJournalPath: "/var/lib/x/store.jsonl", CommandJournalPath: "/var/lib/x/command.jsonl"}
}

func TestCancellationFencesLinkAndPublish(t *testing.T) {
	c, _ := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", PublicationAuto, bigBudget, "s")
	admitted := mustSubmit(t, c, uid1, s.ID, 0, "k", "p")
	mustCancel(t, c, admitted, "cancel")
	n := recordCount(t, c)
	wantCode(t, c.LinkTurn(admitted.ID, cancellationFenceLink()), CodeCancelled)
	if got, _ := c.Turn(admitted.ID); got.State != StateAdmitted || got.Link != nil || recordCount(t, c) != n {
		t.Fatalf("cancelled admission was linked: %+v", got)
	}
	review := reviewedTurn(t, c, "review")
	mustCancel(t, c, review, "cancel")
	n = recordCount(t, c)
	_, _, err := c.ResolveTurn(uid1, resolveReq(t, c, review, "resolve", DecisionPublish))
	wantCode(t, err, CodeCancelled)
	if got, _ := c.Turn(review.ID); got.Resolution != "" || recordCount(t, c) != n {
		t.Fatalf("cancelled review was authorized: %+v", got)
	}
	// A refused publish does not consume its key, and cancellation still permits discard.
	mustResolve(t, c, review, "resolve", DecisionDiscard)
}

func TestChildCancellationFenceReplay(t *testing.T) {
	path := os.Getenv("TB_PRODUCT_CANCEL_FENCE_PATH")
	if path == "" {
		return
	}
	c, err := Open(path, signedRegistration(t, nil))
	if os.Getenv("TB_PRODUCT_CANCEL_FENCE_CORRUPT") == "1" {
		wantCode(t, err, CodeCatalogCorrupt)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	admitted, _ := c.Turn(os.Getenv("TB_PRODUCT_CANCEL_FENCE_ADMITTED"))
	review, _ := c.Turn(os.Getenv("TB_PRODUCT_CANCEL_FENCE_REVIEW"))
	if !admitted.CancelRequested || !review.CancelRequested {
		t.Fatal("fresh process lost cancellation records")
	}
	n := recordCount(t, c)
	wantCode(t, c.LinkTurn(admitted.ID, cancellationFenceLink()), CodeCancelled)
	_, _, err = c.ResolveTurn(uid1, resolveReq(t, c, review, "resolve", DecisionPublish))
	wantCode(t, err, CodeCancelled)
	if recordCount(t, c) != n {
		t.Fatal("refused transitions appended records after replay")
	}
	mustResolve(t, c, review, "resolve", DecisionDiscard)
}

func runCancellationFenceChild(t *testing.T, path string, env ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChildCancellationFenceReplay$", "-test.count=1")
	child.Env = append(os.Environ(), append([]string{"TB_PRODUCT_CANCEL_FENCE_PATH=" + path}, env...)...)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("fresh-process cancellation fence: %v\n%s", err, out)
	}
}

func TestCancellationFencesSurviveFreshProcessReplay(t *testing.T) {
	c, path := newCatalog(t)
	s := mustSession(t, c, uid1, "alpha", PublicationAuto, bigBudget, "s")
	admitted := mustSubmit(t, c, uid1, s.ID, 0, "k", "p")
	mustCancel(t, c, admitted, "cancel")
	review := reviewedTurn(t, c, "review")
	mustCancel(t, c, review, "cancel")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	runCancellationFenceChild(t, path, "TB_PRODUCT_CANCEL_FENCE_ADMITTED="+admitted.ID, "TB_PRODUCT_CANCEL_FENCE_REVIEW="+review.ID)
	reopened := openAt(t, path, signedRegistration(t, nil))
	got, _ := reopened.Turn(review.ID)
	if got.Resolution != DecisionDiscard {
		t.Fatalf("discard after cancellation was not durable: %+v", got)
	}
}

func TestCancellationFencesRejectInvalidHistoryInFreshProcess(t *testing.T) {
	for _, transition := range []string{"link", "publish"} {
		t.Run(transition, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "catalog.jsonl")
			c := openAt(t, path, signedRegistration(t, nil))
			var kind string
			var record any
			if transition == "link" {
				s := mustSession(t, c, uid1, "alpha", PublicationAuto, bigBudget, "s")
				turn := mustSubmit(t, c, uid1, s.ID, 0, "k", "p")
				mustCancel(t, c, turn, "cancel")
				kind, record = kindTurnLinked, turnLinkedRecord{header: hdr, TurnID: turn.ID, Link: cancellationFenceLink()}
			} else {
				turn := reviewedTurn(t, c, "review")
				mustCancel(t, c, turn, "cancel")
				r := resolveReq(t, c, turn, "publish", DecisionPublish)
				kind, record = kindTurnResolutionRequested, turnResolutionRequestedRecord{header: hdr, TurnID: r.TurnID,
					SessionID: r.SessionID, Principal: uid1, RequestKey: r.RequestKey, PayloadDigest: payloadDigest(opResolveTurn, r),
					CandidateDigest: r.CandidateDigest, Decision: r.Decision, SessionRevision: r.ExpectedRevision + 1}
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			journal, err := audit.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := journal.Append(audit.Event{Kind: kind, ID: "invalid-transition", Data: data}); err != nil {
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			runCancellationFenceChild(t, path, "TB_PRODUCT_CANCEL_FENCE_CORRUPT=1")
		})
	}
}
