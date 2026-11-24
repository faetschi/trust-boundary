package researchroute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/usdledger"
)

const (
	ForwardPath = "/v1/chat/completions"

	VStubEndpoint = "http://127.0.0.1:18443/api/v1/chat/completions"

	maxTapLine = 1 << 20
)

type ForwardTotals struct {
	Booked       int   `json:"booked"`
	Settled      int   `json:"settled"`
	Uncertain    int   `json:"uncertain"`
	TokensIn     int64 `json:"tokens_in"`
	TokensOut    int64 `json:"tokens_out"`
	CostMicroUSD int64 `json:"cost_micro_usd"`
	Overage      bool  `json:"overage"`
}

type Forwarder struct {
	bundle      *Bundle
	doer        *openrouter.HTTPDoer
	admissionID string
	token       []byte
	closed      atomic.Bool

	run      sync.Mutex
	mu       sync.Mutex
	calls    uint64
	maxCalls uint64
	totals   ForwardTotals
}

func (b *Bundle) NewForwarder(admissionID, token string, maxHostCalls uint64) (*Forwarder, error) {
	if admissionID == "" || len(token) < 32 || maxHostCalls == 0 {
		return nil, errors.New("a forwarder needs an admission id, a credential of at least 32 bytes and the case's max_host_calls")
	}
	credentials, err := b.Credentials()
	if err != nil {
		return nil, err
	}
	doer, err := credentials.NewHTTPDoerForEndpoint(b.route.Endpoint, b.route.Timeout)
	if err != nil {
		return nil, err
	}
	return &Forwarder{bundle: b, doer: doer, admissionID: admissionID, token: []byte(token),
		maxCalls: min(maxHostCalls, b.MaxRequestsPerUnit())}, nil
}

func (b *Bundle) MaxCompletionTokens() uint64 { return *b.profile.MaxOutputTokens }

func (b *Bundle) Unit(unit string) (usdledger.UnitInfo, bool, error) { return b.ledger.Unit(unit) }

func ForwardUnitID(admissionID string, n uint64) string {
	sum := sha256.New()
	fmt.Fprintf(sum, "tbound-research-v/v1\x00%s\x00%d", admissionID, n)
	return "research-call/" + hex.EncodeToString(sum.Sum(nil))
}

func (f *Forwarder) Finish() ForwardTotals {
	f.closed.Store(true)
	f.run.Lock()
	defer f.run.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.totals
}

func (f *Forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.closed.Load() {
		http.Error(w, "the unit has ended", http.StatusServiceUnavailable)
		return
	}
	got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), f.token) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.RequestURI != ForwardPath {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !f.run.TryLock() {
		http.Error(w, "one request at a time", http.StatusTooManyRequests)
		return
	}
	defer f.run.Unlock()
	if f.closed.Load() {
		http.Error(w, "the unit has ended", http.StatusServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, openrouter.MaxRequestBytes))
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	built, err := openrouter.BuildForwardedRequest(f.bundle.route, raw, f.bundle.MaxCompletionTokens())
	switch {
	case errors.Is(err, openrouter.ErrForwardedModel):
		http.Error(w, "model not allowed", http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, "request not acceptable", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	over := f.calls >= f.maxCalls
	if !over {
		f.calls++
	}
	n := f.calls
	f.mu.Unlock()
	if over {
		http.Error(w, "request allowance of the unit is used", http.StatusTooManyRequests)
		return
	}
	body := built.Body()
	unit := ForwardUnitID(f.admissionID, n)
	if _, err := f.bundle.reserve(unit, built.RequestURL(), body, f.bundle.MaxCompletionTokens()); err != nil {
		f.mu.Lock()
		f.calls--
		f.mu.Unlock()
		status := http.StatusServiceUnavailable
		if errors.Is(err, ErrInputOverBound) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "request refused before sending", status)
		return
	}
	f.count(func(t *ForwardTotals) { t.Booked++ })
	f.send(w, context.WithoutCancel(r.Context()), unit, built, body)
}

func (f *Forwarder) send(w http.ResponseWriter, ctx context.Context, unit string, built openrouter.BuiltRequest, body []byte) {
	final := false
	defer func() {
		if !final {
			f.uncertain(ctx, unit)
		}
	}()
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, built.RequestURL(), bytes.NewReader(body))
	if err != nil {
		f.release(ctx, unit)
		final = true
		http.Error(w, "request refused before sending", http.StatusInternalServerError)
		return
	}
	upstream.Header = f.bundle.route.RequestHeaders("")
	response, err := f.doer.Do(upstream)
	if err != nil {
		if errors.Is(err, openrouter.ErrNotSent) {
			f.release(ctx, unit)
		} else {
			f.uncertain(ctx, unit)
		}
		final = true
		http.Error(w, "provider unreachable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		f.uncertain(ctx, unit)
		final = true
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(response.Body, 64<<10))
		return
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	var tap usageTap
	var clientErr bool
	source := io.LimitReader(response.Body, openrouter.MaxResponseBytes)
	chunk := make([]byte, 32<<10)
	for {
		n, readErr := source.Read(chunk)
		if n > 0 {
			_, _ = tap.Write(chunk[:n])
			if !clientErr {
				if _, werr := w.Write(chunk[:n]); werr != nil {
					clientErr = true
				} else if flusher != nil {
					flusher.Flush()
				}
			}
		}
		if readErr != nil {
			tap.flush()
			break
		}
	}
	if len(tap.usage) == 0 {
		f.uncertain(ctx, unit)
		final = true
		return
	}
	f.settle(ctx, unit, tap.usage)
	final = true
}

func (f *Forwarder) count(update func(*ForwardTotals)) {
	f.mu.Lock()
	update(&f.totals)
	f.mu.Unlock()
}

func (f *Forwarder) release(ctx context.Context, unit string) {
	if err := f.bundle.ReleaseSpend(ctx, unit); err != nil {
		f.uncertain(ctx, unit)
	}
}

func (f *Forwarder) uncertain(ctx context.Context, unit string) {
	_ = f.bundle.UncertainSpend(ctx, unit)
	f.count(func(t *ForwardTotals) { t.Uncertain++ })
}

func (f *Forwarder) settle(ctx context.Context, unit string, usage json.RawMessage) {
	err := f.bundle.SettleSpend(ctx, unit, usage)
	overage := errors.Is(err, ErrSpendOverage)
	info, found, infoErr := f.bundle.Unit(unit)
	if (err != nil && !overage) || infoErr != nil || !found || info.State != usdledger.UnitStateSettled {
		f.count(func(t *ForwardTotals) { t.Uncertain++ })
		return
	}
	var members map[string]json.RawMessage
	_ = json.Unmarshal(usage, &members)
	in, _ := tokenCount(members["prompt_tokens"])
	out, _ := tokenCount(members["completion_tokens"])
	f.count(func(t *ForwardTotals) {
		t.Settled++
		t.TokensIn += int64(in)
		t.TokensOut += int64(out)
		t.CostMicroUSD += info.SettledMicroUSD
		t.Overage = t.Overage || overage
	})
}

type usageTap struct {
	partial  []byte
	skipping bool
	usage    json.RawMessage
}

func (t *usageTap) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if !t.skipping {
				t.partial = append(t.partial, p...)
				if len(t.partial) > maxTapLine {
					t.partial, t.skipping = nil, true
				}
			}
			break
		}
		if !t.skipping {
			t.partial = append(t.partial, p[:i]...)
			t.line(t.partial)
		}
		t.partial, t.skipping = t.partial[:0], false
		p = p[i+1:]
	}
	return total, nil
}

func (t *usageTap) flush() {
	if !t.skipping && len(t.partial) > 0 {
		t.line(t.partial)
	}
	t.partial = nil
}

func (t *usageTap) line(line []byte) {
	payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	if !ok || !bytes.Contains(payload, []byte(`"usage"`)) {
		return
	}
	var chunk struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(payload, &chunk) == nil && len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
		t.usage = append(json.RawMessage(nil), chunk.Usage...)
	}
}

func StubPaths() Paths {
	return Paths{Profile: "/etc/tbound/v-stub/research-profile.json", LedgerConfig: "/etc/tbound/v-stub/research-ledger.json",
		Ledger: "/var/lib/tbound/sessions/v-stub-ledger", Checkpoint: "/etc/tbound/v-stub/research-ledger-checkpoint.json",
		Key: "/etc/tbound/v-stub/stub.key"}
}

func OpenStub(paths Paths, endpoint string) (*Bundle, error) {
	production := DefaultPaths()
	for _, own := range []string{paths.Profile, paths.LedgerConfig, paths.Ledger, paths.Checkpoint, paths.Key} {
		absolute, err := filepath.Abs(own)
		if err != nil {
			return nil, err
		}
		for _, prod := range []string{production.Ledger, production.Checkpoint, production.LedgerConfig, production.Key} {
			if absolute == filepath.Clean(prod) {
				return nil, errors.New("the v-stub route may not use a research ledger, checkpoint, ledger configuration or key path")
			}
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil {
		return nil, errors.New("the v-stub route needs a loopback http endpoint")
	}
	if ip := net.ParseIP(parsed.Hostname()); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("the v-stub route needs a loopback http endpoint")
	}
	return open(paths, endpoint)
}
