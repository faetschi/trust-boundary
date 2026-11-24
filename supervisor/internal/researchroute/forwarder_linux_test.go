//go:build linux

package researchroute

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/usdledger"
)

const (
	forwardToken = "0123456789abcdef0123456789abcdef0123456789abcdef"
	piRequest    = `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true},"store":false,"max_completion_tokens":16384,"provider":{"order":["evil"]},"tools":[{"type":"function","function":{"name":"read","description":"d","parameters":{"type":"object"}}}]}`
)

type upstream struct {
	*providerServer
	mu     sync.Mutex
	bodies [][]byte
	heads  []http.Header
}

func newForwarderFixture(t *testing.T) (*Bundle, *upstream, *httptest.Server, *Forwarder) {
	t.Helper()
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	up := &upstream{providerServer: newProviderServer(t, &bundle)}
	up.handler = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		up.mu.Lock()
		up.bodies, up.heads = append(up.bodies, body), append(up.heads, r.Header.Clone())
		up.mu.Unlock()
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002})
	}
	bundle = f.open(up.URL + "/api/v1/chat/completions")
	fwd, err := bundle.NewForwarder("case-1/v/1", forwardToken)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(fwd)
	t.Cleanup(srv.Close)
	return bundle, up, srv, fwd
}

func post(t *testing.T, srv *httptest.Server, path, token, body string, header map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func TestForwarderBooksBeforeSendingAndRewritesTheRequestExactly(t *testing.T) {
	bundle, up, srv, fwd := newForwarderFixture(t)
	status, out := post(t, srv, ForwardPath, forwardToken, piRequest, map[string]string{"X-Evil": "1", "Cookie": "c=1", "HTTP-Referer": "x"})
	if status != 200 || !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("status %d body %q", status, out)
	}
	if up.requests.Load() != 1 || up.openAtHit.Load() != 8400 {
		t.Fatalf("upstream saw %d requests with %d micro-USD booked; want 1 and 8400", up.requests.Load(), up.openAtHit.Load())
	}
	want := `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","description":"d","parameters":{"type":"object"}}}],"stream":true,"max_completion_tokens":2000,` +
		`"provider":{"order":["example-provider"],"allow_fallbacks":false,"data_collection":"deny","require_parameters":true,"zdr":true,"max_price":{"prompt":0.3,"completion":1.2}},"temperature":0,"seed":7,"reasoning":{"enabled":false}}`
	if string(up.bodies[0]) != want {
		t.Fatalf("upstream body\n%s\nwant\n%s", up.bodies[0], want)
	}
	for _, name := range []string{"X-Evil", "Cookie", "Http-Referer"} {
		if up.heads[0].Get(name) != "" {
			t.Errorf("client header %s reached the provider", name)
		}
	}
	if up.heads[0].Get("Authorization") != "Bearer "+testKey || strings.Contains(strings.Join(up.heads[0].Values("Authorization"), ""), forwardToken) {
		t.Errorf("provider credential is %q", up.heads[0].Get("Authorization"))
	}
	totals := fwd.Finish()
	if totals != (ForwardTotals{Booked: 1, Settled: 1, TokensIn: 11, TokensOut: 3, CostMicroUSD: 2}) {
		t.Fatalf("totals %+v", totals)
	}
	if snap := snapshot(t, bundle); snap.SettledMicroUSD != 2 || snap.OpenMicroUSD != 0 || snap.SettledUnits != 1 {
		t.Fatalf("ledger %+v", snap)
	}
	unit, found, err := bundle.Unit(ForwardUnitID("case-1/v/1", 1))
	if err != nil || !found || unit.State != usdledger.UnitStateSettled {
		t.Fatalf("unit %+v %v %v", unit, found, err)
	}
}

func TestForwarderRefusesWithoutBookingOrSending(t *testing.T) {
	bundle, up, srv, fwd := newForwarderFixture(t)
	before := snapshot(t, bundle).Records
	bigContent := strings.Repeat("a", 20000)
	for name, tc := range map[string]struct {
		path, token, body string
		method            string
		want              int
	}{
		"wrong path":      {path: "/v1/models", token: forwardToken, body: piRequest, want: 404},
		"path with query": {path: ForwardPath + "?x=1", token: forwardToken, body: piRequest, want: 404},
		"prefix path":     {path: ForwardPath + "/", token: forwardToken, body: piRequest, want: 404},
		"wrong method":    {path: ForwardPath, token: forwardToken, body: piRequest, method: http.MethodGet, want: 405},
		"wrong model":     {path: ForwardPath, token: forwardToken, body: strings.Replace(piRequest, "deepseek/deepseek-v4.1-flash", "openai/gpt-x", 1), want: 400},
		"no token":        {path: ForwardPath, token: "", body: piRequest, want: 401},
		"wrong token":     {path: ForwardPath, token: forwardToken + "x", body: piRequest, want: 401},
		"not json":        {path: ForwardPath, token: forwardToken, body: "{", want: 400},
		"over max input":  {path: ForwardPath, token: forwardToken, body: `{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"` + bigContent + `"}]}`, want: 413},
		"over byte limit": {path: ForwardPath, token: forwardToken, body: strings.Repeat("a", 5<<20), want: 413},
	} {
		method := tc.method
		if method == "" {
			method = http.MethodPost
		}
		req, _ := http.NewRequest(method, srv.URL+tc.path, strings.NewReader(tc.body))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, tc.want)
		}
	}
	if up.requests.Load() != 0 {
		t.Fatalf("the provider saw %d requests", up.requests.Load())
	}
	if snapshot(t, bundle).Records != before {
		t.Fatal("a refused request wrote a ledger record")
	}
	if totals := fwd.Finish(); totals != (ForwardTotals{}) {
		t.Fatalf("totals %+v", totals)
	}
}

func TestForwarderRefusesRequestBeyondTheUnitAllowance(t *testing.T) {
	bundle, up, srv, fwd := newForwarderFixture(t)
	for i := uint64(0); i < bundle.MaxRequestsPerUnit(); i++ {
		if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != 200 {
			t.Fatalf("request %d: status %d", i+1, status)
		}
	}
	before := snapshot(t, bundle).Records
	if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != http.StatusTooManyRequests {
		t.Fatalf("request after the allowance: status %d", status)
	}
	if up.requests.Load() != 5 || snapshot(t, bundle).Records != before {
		t.Fatalf("the refused request reached the provider or the ledger: %d requests", up.requests.Load())
	}
	if totals := fwd.Finish(); totals.Booked != 5 || totals.Settled != 5 || totals.CostMicroUSD != 10 {
		t.Fatalf("totals %+v", totals)
	}
}

func TestForwarderRefusedOversizeDoesNotConsumeTheAllowance(t *testing.T) {
	bundle, _, srv, _ := newForwarderFixture(t)
	over := `{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"` + strings.Repeat("a", 20000) + `"}]}`
	for i := 0; i < 8; i++ {
		if status, _ := post(t, srv, ForwardPath, forwardToken, over, nil); status != http.StatusRequestEntityTooLarge {
			t.Fatalf("status %d", status)
		}
	}
	for i := uint64(0); i < bundle.MaxRequestsPerUnit(); i++ {
		if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != 200 {
			t.Fatalf("request %d: status %d", i+1, status)
		}
	}
}

func TestForwarderDoesNotSettleWithoutUsageAndKeepsTheReservation(t *testing.T) {
	bundle, up, srv, fwd := newForwarderFixture(t)
	up.handler = func(w http.ResponseWriter, _ *http.Request) { sse(w, nil) }
	if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != 200 {
		t.Fatalf("status %d", status)
	}
	if totals := fwd.Finish(); totals != (ForwardTotals{Booked: 1, Uncertain: 1}) {
		t.Fatalf("totals %+v", totals)
	}
	if snap := snapshot(t, bundle); snap.UncertainMicroUSD != 8400 || snap.OpenMicroUSD != 0 {
		t.Fatalf("ledger %+v", snap)
	}
}

func TestForwarderMarksProviderErrorsAndTransportFailuresUncertain(t *testing.T) {
	bundle, up, srv, fwd := newForwarderFixture(t)
	up.handler = func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }
	if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != 500 {
		t.Fatalf("status %d", status)
	}
	up.Close()
	if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != http.StatusBadGateway {
		t.Fatalf("status %d", status)
	}
	if totals := fwd.Finish(); totals != (ForwardTotals{Booked: 2, Uncertain: 2}) {
		t.Fatalf("totals %+v", totals)
	}
	if snap := snapshot(t, bundle); snap.UncertainMicroUSD != 2*8400 {
		t.Fatalf("ledger %+v", snap)
	}
}

func TestForwarderIsDeadAfterFinish(t *testing.T) {
	_, up, srv, fwd := newForwarderFixture(t)
	fwd.Finish()
	if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("status %d", status)
	}
	if up.requests.Load() != 0 {
		t.Fatal("the provider was reached after the unit")
	}
}

func TestForwarderRelaysTheStreamUnchanged(t *testing.T) {
	_, up, srv, _ := newForwarderFixture(t)
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n: keep-alive\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"cost\":0.000001}}\n\ndata: [DONE]\n\n"
	up.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}
	if _, out := post(t, srv, ForwardPath, forwardToken, piRequest, nil); out != stream {
		t.Fatalf("relayed %q", out)
	}
}

func TestUsageTapReadsTheLastNonNullUsageAcrossSplitWrites(t *testing.T) {
	var tap usageTap
	for _, part := range []string{"data: {\"usage\":nul", "l}\n\ndata: {\"usa", "ge\":{\"cost\":1}}\r\n", "data: [DONE]\n"} {
		_, _ = tap.Write([]byte(part))
	}
	tap.flush()
	if string(tap.usage) != `{"cost":1}` {
		t.Fatalf("usage %q", tap.usage)
	}
	var skipped usageTap
	_, _ = skipped.Write([]byte("data: " + strings.Repeat("x", maxTapLine+1)))
	_, _ = skipped.Write([]byte(" \"usage\":{\"cost\":9}\ndata: {\"usage\":{\"cost\":2}}\n"))
	if string(skipped.usage) != `{"cost":2}` {
		t.Fatalf("usage after an over-long line %q", skipped.usage)
	}
}

func TestStubRouteBooksNothingInTheResearchLedger(t *testing.T) {
	research := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	research.genesis()
	researchBundle := research.open("")
	before := snapshot(t, researchBundle)

	stub := newFixture(t, frozenProfileJSON, strings.Replace(ledgerConfigJSON(5_000_000, 20_000, "request"), "research-test", "v-stub-test", 1))
	stub.genesis()
	var stubBundle *Bundle
	server := newProviderServer(t, &stubBundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) {
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002})
	}
	var err error
	stubBundle, err = OpenStub(stub.paths, server.URL+"/api/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stubBundle.Close() })
	fwd, err := stubBundle.NewForwarder("case-1/v/1", forwardToken)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(fwd)
	defer srv.Close()
	if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != 200 {
		t.Fatalf("status %d", status)
	}
	if after := snapshot(t, researchBundle); after != before {
		t.Fatalf("research ledger changed: %+v -> %+v", before, after)
	}
	if snapshot(t, stubBundle).SettledUnits != 1 {
		t.Fatal("the stub ledger did not book the call")
	}
}

func TestOpenStubRefusesProductionPathsAndNonLoopbackEndpoints(t *testing.T) {
	stub := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	stub.genesis()
	for _, endpoint := range []string{"https://openrouter.ai/api/v1/chat/completions", "http://example.com/v1/chat/completions", "http://user@127.0.0.1:1/x", ""} {
		if b, err := OpenStub(stub.paths, endpoint); err == nil {
			_ = b.Close()
			t.Errorf("OpenStub accepted endpoint %q", endpoint)
		}
	}
	for name, mutate := range map[string]func(*Paths){
		"ledger":     func(p *Paths) { p.Ledger = LedgerPath },
		"checkpoint": func(p *Paths) { p.Checkpoint = CheckpointPath },
		"config":     func(p *Paths) { p.LedgerConfig = LedgerConfigPath },
		"key":        func(p *Paths) { p.Key = KeyPath },

		"ledger with a dot segment":      func(p *Paths) { p.Ledger = "/var/lib/tbound/sessions/./usd-ledger" },
		"ledger with a trailing slash":   func(p *Paths) { p.Ledger = LedgerPath + "/" },
		"key through a parent segment":   func(p *Paths) { p.Key = "/etc/tbound/provider/../provider/openrouter.key" },
		"checkpoint with a double slash": func(p *Paths) { p.Checkpoint = "/" + CheckpointPath },
	} {
		paths := stub.paths
		mutate(&paths)
		if b, err := OpenStub(paths, "http://127.0.0.1:1/v1/chat/completions"); err == nil {
			_ = b.Close()
			t.Errorf("OpenStub accepted the production %s path", name)
		}
	}
}

type panickingWriter struct{ http.ResponseWriter }

func (panickingWriter) Header() http.Header { panic("the client connection failed") }

func TestForwarderSettlesTheUnitAfterTheClientHasGone(t *testing.T) {
	bundle, up, _, fwd := newForwarderFixture(t)
	proceed := make(chan struct{})
	up.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-proceed
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"cost\":0.000002}}\n\ndata: [DONE]\n\n")
	}
	contexts := make(chan context.Context, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contexts <- r.Context()
		fwd.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+ForwardPath, strings.NewReader(piRequest))
	req.Header.Set("Authorization", "Bearer "+forwardToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resp.Body.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-(<-contexts).Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not see the client leave")
	}
	close(proceed)
	if totals := fwd.Finish(); totals != (ForwardTotals{Booked: 1, Settled: 1, TokensIn: 11, TokensOut: 3, CostMicroUSD: 2}) {
		t.Fatalf("totals %+v", totals)
	}
	if snap := snapshot(t, bundle); snap.SettledMicroUSD != 2 || snap.UncertainUnits != 0 || snap.OpenMicroUSD != 0 {
		t.Fatalf("ledger %+v", snap)
	}
}

func TestForwarderReleasesTheReservationOnlyWhenTheTransportRefusedBeforeSending(t *testing.T) {
	bundle, up, srv, fwd := newForwarderFixture(t)
	doer, err := openrouter.NewHTTPDoerForEndpoint(func() (string, error) { return "", errors.New("no key") }, time.Second, bundle.Route().Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	fwd.doer = doer
	if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != http.StatusBadGateway {
		t.Fatalf("status %d", status)
	}
	if totals := fwd.Finish(); totals != (ForwardTotals{Booked: 1}) {
		t.Fatalf("totals %+v", totals)
	}
	if snap := snapshot(t, bundle); snap.UncertainUnits != 0 || snap.OpenMicroUSD != 0 || snap.SettledUnits != 1 || snap.SettledMicroUSD != 0 || up.requests.Load() != 0 {
		t.Fatalf("ledger %+v, provider requests %d", snap, up.requests.Load())
	}
}

func TestForwarderMarksTheUnitUncertainWhenTheHandlerPanicsAfterBooking(t *testing.T) {
	bundle, _, _, fwd := newForwarderFixture(t)
	req := httptest.NewRequest(http.MethodPost, ForwardPath, strings.NewReader(piRequest))
	req.Header.Set("Authorization", "Bearer "+forwardToken)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the handler did not panic")
			}
		}()
		fwd.ServeHTTP(panickingWriter{}, req)
	}()
	if totals := fwd.Finish(); totals != (ForwardTotals{Booked: 1, Uncertain: 1}) {
		t.Fatalf("totals %+v", totals)
	}
	if snap := snapshot(t, bundle); snap.UncertainMicroUSD != 8400 || snap.OpenMicroUSD != 0 {
		t.Fatalf("ledger %+v", snap)
	}
}

func TestForwarderReportsAnOverageAndRefusesWhatFollows(t *testing.T) {
	bundle, up, srv, fwd := newForwarderFixture(t)
	up.handler = func(w http.ResponseWriter, _ *http.Request) {
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.05})
	}
	if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != 200 {
		t.Fatalf("status %d", status)
	}
	if status, _ := post(t, srv, ForwardPath, forwardToken, piRequest, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("status after the overage %d", status)
	}
	totals := fwd.Finish()
	if !totals.Overage || totals.Booked != 1 || totals.Settled != 1 || totals.Uncertain != 0 {
		t.Fatalf("totals %+v", totals)
	}
	if !snapshot(t, bundle).Overspent {
		t.Fatal("the ledger did not record the overage")
	}
}
