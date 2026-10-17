//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
)

// incrementalJournal is a request journal over a real audit journal plus the count of
// settlement verifications the journal asked for, per request id.
type incrementalJournal struct {
	path     string
	audit    *audit.Journal
	requests *piServiceRequestJournal
	calls    map[string]int
	total    int
	failID   string // a verification of this request id fails
	// reconciler is the fake workflow reconciliation behind abandon-unknown.
	reconciler *fakeAbandonReconciler
}

func openIncrementalJournal(t testing.TB, path string) *incrementalJournal {
	t.Helper()
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := newPiServiceRequestJournal(journal)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	h := &incrementalJournal{path: path, audit: journal, requests: requests, calls: make(map[string]int),
		reconciler: newFakeAbandonReconciler()}
	requests.setReconciler(h.reconciler)
	requests.settlementVerifier = func(_ context.Context, prior piServiceRequestState) error {
		h.calls[prior.accepted.RequestID]++
		h.total++
		if prior.accepted.RequestID == h.failID {
			return errors.New("settlement evidence of an old request no longer verifies")
		}
		return nil
	}
	return h
}

func incrementalPeer() piServicePeerCredentials {
	return piServicePeerCredentials{PID: 424242, UID: os.Geteuid(), GID: os.Getegid()}
}

func incrementalRequest(index int) (piServiceRequest, string) {
	request := piServiceRequest{RequestID: fmt.Sprintf("request-%04d", index), TaskID: fmt.Sprintf("task-%04d", index),
		Prompt: "synthetic settled request"}
	digest, err := piServicePayloadDigest(request)
	if err != nil {
		panic(err)
	}
	return request, digest
}

// admit journals request index as accepted and, when settle is true, as completed.
func (h *incrementalJournal) admit(t testing.TB, index int, settle bool) error {
	t.Helper()
	request, digest := incrementalRequest(index)
	if _, err := h.requests.begin(context.Background(), incrementalPeer(), request, digest); err != nil {
		return err
	}
	if !settle {
		return nil
	}
	response := piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID, Status: "completed",
		Receipt: []byte(`{"synthetic":true}`)}
	if err := h.requests.complete(incrementalPeer().UID, request, digest, response); err != nil {
		t.Fatal(err)
	}
	return nil
}

func (h *incrementalJournal) close(t testing.TB) {
	t.Helper()
	if err := h.requests.close(); err != nil {
		t.Fatal(err)
	}
}

// Each settled request is verified once per service lifetime, not once per later
// request: the begin of request n+1 verifies only the request that settled since the
// previous begin.
func TestPiServiceJournalBeginVerifiesEachSettlementOncePerServiceStart(t *testing.T) {
	const priors = 300
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	for index := 1; index <= priors; index++ {
		if err := h.admit(t, index, true); err != nil {
			t.Fatalf("admit %d: %v", index, err)
		}
	}
	if want := priors - 1; h.total != want {
		t.Fatalf("seeding %d settled requests ran %d settlement verifications, want %d (one per newly settled prior; "+
			"the O(n^2) re-verification would have run %d)", priors, h.total, want, priors*(priors-1)/2)
	}
	h.total = 0
	if err := h.admit(t, priors+1, true); err != nil {
		t.Fatal(err)
	}
	if h.total != 1 || h.calls[fmt.Sprintf("request-%04d", priors)] != 1 {
		t.Fatalf("begin %d verified %d priors (%v), want exactly the one newly settled request", priors+1, h.total, h.calls)
	}
}

// A service start begins with no verified settlements, so its first admission verifies
// every prior, and a prior whose evidence no longer verifies refuses the admission and
// the journal gains no record. A refusal is not remembered as verified.
func TestPiServiceJournalServiceStartVerifiesEveryPriorAndRefusesTamperedOldEntry(t *testing.T) {
	const priors = 40
	path := filepath.Join(incrementalTestRoot(t), "requests.jsonl")
	first := openIncrementalJournal(t, path)
	for index := 1; index <= priors; index++ {
		if err := first.admit(t, index, true); err != nil {
			t.Fatalf("admit %d: %v", index, err)
		}
	}
	first.close(t)

	restarted := openIncrementalJournal(t, path)
	defer restarted.close(t)
	restarted.failID = "request-0003"
	for attempt := 0; attempt < 2; attempt++ {
		err := restarted.admit(t, priors+1, true)
		if !errors.Is(err, ErrPiServiceUnresolvedPrior) {
			t.Fatalf("attempt %d: tampered old entry did not refuse the new admission after a restart: %v", attempt, err)
		}
	}
	if restarted.calls["request-0003"] != 2 {
		t.Fatalf("a failed settlement verification was remembered: %v", restarted.calls)
	}
	trace, err := restarted.audit.Trace()
	if err != nil || len(trace.Records) != 2*priors {
		t.Fatalf("a refused admission appended to the journal: records=%d err=%v", len(trace.Records), err)
	}

	restarted.failID = ""
	restarted.total = 0
	if err := restarted.admit(t, priors+1, true); err != nil {
		t.Fatal(err)
	}
	// The service start verified every prior, each one passing exactly once. Only the
	// tampered one was asked again, after its failures. The next admission visits only
	// the request that settled since.
	for index := 1; index <= priors; index++ {
		id := fmt.Sprintf("request-%04d", index)
		want := 1
		if id == "request-0003" {
			want = 3
		}
		if restarted.calls[id] != want {
			t.Fatalf("restart verified %s %d times, want %d: %v", id, restarted.calls[id], want, restarted.calls)
		}
	}
	restarted.total = 0
	if err := restarted.admit(t, priors+2, true); err != nil || restarted.total != 1 {
		t.Fatalf("steady-state admission verified %d priors (err=%v), want 1", restarted.total, err)
	}
}

// A prior that is still unsettled blocks every later admission, with or without a
// verified prefix before it.
func TestPiServiceJournalUnsettledPriorStillBlocksAfterVerifiedPrefix(t *testing.T) {
	h := openIncrementalJournal(t, filepath.Join(incrementalTestRoot(t), "requests.jsonl"))
	defer h.close(t)
	for index, settle := range []bool{true, true, false} {
		if err := h.admit(t, index+1, settle); err != nil {
			t.Fatalf("admit %d: %v", index+1, err)
		}
	}
	if err := h.admit(t, 4, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
		t.Fatalf("an unsettled prior did not block: %v", err)
	}
}

// incrementalTestRoot is a private 0700 directory that audit.Open accepts under any umask.
func incrementalTestRoot(tb testing.TB) string {
	tb.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		tb.Fatal(err)
	}
	root, err := os.MkdirTemp(home, "tbound-journal-incremental-")
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

// BenchmarkPiServiceJournalBeginAt1000SettledRequests times begin() with 1,000 settled
// priors. Run with -benchtime=1x. Three numbers are reported:
//
//   - stub-ms: the 1,001st begin with a no-cost verifier, the loop overhead alone;
//   - proxy-steady-ms: the 1,002nd begin with a verifier that opens and fully traces a
//     3,000-record audit journal, the size of the shared publication journal after 1,000
//     workflows (one of several costs of the real verifier, so a lower bound);
//   - proxy-first-ms: the first begin after a service start with that verifier.
func BenchmarkPiServiceJournalBeginAt1000SettledRequests(b *testing.B) {
	const priors = 1000
	root := incrementalTestRoot(b)
	path := filepath.Join(root, "requests.jsonl")
	h := openIncrementalJournal(b, path)
	for index := 1; index <= priors; index++ {
		if err := h.admit(b, index, true); err != nil {
			b.Fatal(err)
		}
	}
	start := time.Now()
	if err := h.admit(b, priors+1, true); err != nil {
		b.Fatal(err)
	}
	stub := time.Since(start)

	shared, err := audit.Open(filepath.Join(root, "publication.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	for index := 0; index < 3*priors; index++ {
		if _, err := shared.Append(audit.Event{Kind: "synthetic_publication", ID: fmt.Sprintf("publication-%05d", index),
			Data: []byte(`{"synthetic":true}`)}); err != nil {
			b.Fatal(err)
		}
	}
	if err := shared.Close(); err != nil {
		b.Fatal(err)
	}
	proxy := func(_ context.Context, _ piServiceRequestState) error {
		journal, err := audit.Open(filepath.Join(root, "publication.jsonl"))
		if err != nil {
			return err
		}
		_, traceErr := journal.Trace()
		return errors.Join(traceErr, journal.Close())
	}
	h.requests.settlementVerifier = proxy
	start = time.Now()
	if err := h.admit(b, priors+2, true); err != nil {
		b.Fatal(err)
	}
	steady := time.Since(start)
	h.close(b)

	h = openIncrementalJournal(b, path)
	h.requests.settlementVerifier = proxy
	start = time.Now()
	if err := h.admit(b, priors+3, true); err != nil {
		b.Fatal(err)
	}
	first := time.Since(start)
	h.close(b)
	for i := 0; i < b.N; i++ {
	}
	b.ReportMetric(float64(stub.Microseconds())/1000, "stub-ms")
	b.ReportMetric(float64(steady.Microseconds())/1000, "proxy-steady-ms")
	b.ReportMetric(float64(first.Microseconds())/1000, "proxy-first-ms")
}
