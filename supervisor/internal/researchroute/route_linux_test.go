//go:build linux

package researchroute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/researchprofile"
	"tbound/supervisor/internal/usdledger"
)

const testKey = "research-route-test-key-123"

// frozenProfileJSON is a fully decided synthetic profile (fake provider slug,
// fake prices). Worst case per request with a 64-token completion cap is
// ceil((20000*150000 + 64*600000)/1e6) = 3039 micro-USD; five requests per unit
// make the unit worst case 15195.
const frozenProfileJSON = `{
  "schema_version": 1,
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "deepseek/deepseek-v4.1-flash",
  "routing": {"order": ["example-provider"], "allow_fallbacks": false, "data_collection": "deny",
              "quantizations": [], "require_parameters": true, "zdr": true},
  "input_micro_usd_per_1m_tokens": 150000,
  "output_micro_usd_per_1m_tokens": 600000,
  "max_price_micro_usd_per_1m_tokens_in": null,
  "max_price_micro_usd_per_1m_tokens_out": null,
  "max_input_tokens": 20000,
  "max_output_tokens": 2000,
  "max_requests_per_unit": 5,
  "temperature_milli": 0,
  "seed": 7,
  "reasoning": {"enabled": false},
  "timeout_ms": 5000,
  "frozen_at": "2026-10-17T00:00:00Z"
}`

const (
	requestReserve = int64(3039)
	unitReserve    = int64(15195)
)

func ledgerConfigJSON(capMicro, minPerUnit int64, basis string) string {
	return fmt.Sprintf(`{"schema_version":1,"ledger_id":"research-test","cap_micro_usd":%d,"min_per_unit_worst_case_micro_usd":%d,"reserve_basis":%q}`,
		capMicro, minPerUnit, basis)
}

type fixture struct {
	t     *testing.T
	paths Paths
}

// newFixture writes a frozen profile, a ledger configuration and a 0400 key into
// a private directory. The files are owned by the test user, so the fixture
// states that uid as the expected owner (it is root in production).
func newFixture(t *testing.T, profile, ledgerConfig string) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, paths: Paths{
		Profile:      filepath.Join(dir, "research-profile.json"),
		LedgerConfig: filepath.Join(dir, "research-ledger.json"),
		Ledger:       filepath.Join(dir, "usd-ledger"),
		Key:          filepath.Join(dir, "openrouter.key"),
		OwnerUID:     uint32(os.Getuid()),
	}}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.write(f.paths.Profile, profile, 0o600)
	f.write(f.paths.LedgerConfig, ledgerConfig, 0o600)
	f.write(f.paths.Key, testKey+"\n", 0o400)
	return f
}

func (f *fixture) write(path, content string, mode os.FileMode) {
	f.t.Helper()
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) genesis() {
	f.t.Helper()
	if _, err := Genesis(f.paths); err != nil {
		f.t.Fatalf("genesis: %v", err)
	}
}

func (f *fixture) open(endpoint string) *Bundle {
	f.t.Helper()
	b, err := open(f.paths, endpoint)
	if err != nil {
		f.t.Fatalf("open: %v", err)
	}
	f.t.Cleanup(func() { _ = b.Close() })
	return b
}

// open2 opens the fixture without failing the test, for refusal cases.
func open2(f *fixture) (*Bundle, error) {
	b, err := open(f.paths, "")
	if err == nil {
		f.t.Cleanup(func() { _ = b.Close() })
	}
	return b, err
}

func snapshot(t *testing.T, b *Bundle) usdledger.Snapshot {
	t.Helper()
	s, err := b.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// providerServer is a fake OpenRouter. It counts requests and, when a request
// arrives, records what the ledger showed at that moment.
type providerServer struct {
	*httptest.Server
	requests  atomic.Int32
	openAtHit atomic.Int64
	handler   func(w http.ResponseWriter, r *http.Request)
}

func newProviderServer(t *testing.T, b **Bundle) *providerServer {
	s := &providerServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		if b != nil && *b != nil {
			snap, _ := (*b).Snapshot()
			s.openAtHit.Store(snap.OpenMicroUSD)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testKey {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		s.handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func sse(w http.ResponseWriter, usage map[string]any) {
	sseModel(w, usage, "deepseek/deepseek-v4.1-flash")
}

func sseModel(w http.ResponseWriter, usage map[string]any, model string) {
	chunk := func(delta map[string]any, finish any, extra map[string]any) string {
		value := map[string]any{"id": "resp-1", "model": model, "created": 1,
			"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		for k, v := range extra {
			value[k] = v
		}
		raw, _ := json.Marshal(value)
		return "data: " + string(raw) + "\n\n"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(chunk(map[string]any{"role": "assistant", "content": "ok"}, nil, nil)))
	_, _ = w.Write([]byte(chunk(map[string]any{}, "stop", nil)))
	if usage != nil {
		trailer := map[string]any{"id": "resp-1", "model": model, "created": 1,
			"object": "chat.completion.chunk", "choices": []any{}, "usage": usage}
		raw, _ := json.Marshal(trailer)
		_, _ = w.Write([]byte("data: " + string(raw) + "\n\n"))
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

// conversation builds a real providerbridge conversation on the bundle's route,
// with the same managed host-call callbacks the production authority installs.
func conversation(t *testing.T, f *fixture, b *Bundle) *providerbridge.Conversation {
	t.Helper()
	journal, err := audit.Open(filepath.Join(t.TempDir(), "provider.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	credentials, err := b.Credentials()
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	c, err := providerbridge.NewFromCredentials(providerbridge.Config{
		ConversationID: "conversation-1", WorkflowID: "workflow-1", ProfileID: "profile-1",
		InitialGenerationID: "generation-1", InitialTreeDigest: "sha256:" + strings.Repeat("0", 64),
		SystemPrompt: "Research system prompt.", DeveloperPrompt: "Research developer prompt.",
		Route: b.Route(), Spend: b,
		SelectCompletionTokenLimit: func(context.Context) (uint64, error) { return 64, nil },
		ReserveHostCall: func(_ context.Context, request broker.RequestRecord, limit uint64) (providerbridge.HostCallBinding, error) {
			return providerbridge.HostCallBinding{CallID: "call-1", RequestDigest: broker.DigestRequestRecord(request), TokenUpperBound: limit}, nil
		},
		CompleteHostCall: func(context.Context, providerbridge.HostCallCompletion) error { return nil },
	}, credentials, journal)
	if err != nil {
		t.Fatalf("NewFromCredentials: %v", err)
	}
	if err := c.AdmitPrompt("task-1", "Say ok."); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResearchRequestIsReservedBeforeSendAndSettledAfter(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		if !bytes.Contains(body.Bytes(), []byte(`"provider":{"order":["example-provider"]`)) ||
			!bytes.Contains(body.Bytes(), []byte(`"model":"deepseek/deepseek-v4.1-flash"`)) {
			http.Error(w, "request does not carry the frozen profile", http.StatusBadRequest)
			return
		}
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	c := conversation(t, f, bundle)

	if _, err := c.NextTurn(context.Background()); err != nil {
		t.Fatalf("NextTurn: %v", err)
	}
	if server.requests.Load() != 1 {
		t.Fatalf("provider saw %d requests", server.requests.Load())
	}
	if got := server.openAtHit.Load(); got != requestReserve {
		t.Fatalf("ledger showed %d micro-USD reserved when the request arrived; want %d", got, requestReserve)
	}
	snap := snapshot(t, bundle)
	// max(provider cost 2, token price ceil((11*150000+3*600000)/1e6)=4) = 4.
	if snap.OpenMicroUSD != 0 || snap.SettledUnits != 1 || snap.SettledMicroUSD != 4 || snap.UncertainMicroUSD != 0 {
		t.Fatalf("settlement differs: %+v", snap)
	}
}

func TestSettlementUsesTheProviderCostWhenItExceedsTheTokenPrice(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) {
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.0000123})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	if _, err := conversation(t, f, bundle).NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snap := snapshot(t, bundle); snap.SettledMicroUSD != 13 {
		t.Fatalf("settled %d micro-USD; want the provider cost 12.3 rounded up to 13", snap.SettledMicroUSD)
	}
}

func TestUnitBasisReservesTheFullUnitWorstCase(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "unit"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) {
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	if _, err := conversation(t, f, bundle).NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := server.openAtHit.Load(); got != unitReserve {
		t.Fatalf("unit basis reserved %d; want %d", got, unitReserve)
	}
	if snap := snapshot(t, bundle); snap.SettledMicroUSD != 4 || snap.OpenMicroUSD != 0 {
		t.Fatalf("unit-basis settlement differs: %+v", snap)
	}
}

func TestOverCapIsRefusedBeforeAnyNetworkCall(t *testing.T) {
	for name, tc := range map[string]struct {
		cap, minPerUnit int64
		basis           string
	}{
		"reservation above remaining cap": {cap: 3000, minPerUnit: 1000, basis: "request"},
		"unit reservation above cap":      {cap: 10_000, minPerUnit: 1000, basis: "unit"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(tc.cap, tc.minPerUnit, tc.basis))
			f.genesis()
			var bundle *Bundle
			server := newProviderServer(t, &bundle)
			server.handler = func(w http.ResponseWriter, _ *http.Request) { sse(w, nil) }
			bundle = f.open(server.URL + "/api/v1/chat/completions")
			c := conversation(t, f, bundle)
			if _, err := c.NextTurn(context.Background()); err == nil {
				t.Fatal("over-cap request was released")
			}
			if n := server.requests.Load(); n != 0 {
				t.Fatalf("fake provider received %d requests; want 0", n)
			}
			if snap := snapshot(t, bundle); snap.CommittedMicroUSD != 0 {
				t.Fatalf("a refused request left %d micro-USD committed", snap.CommittedMicroUSD)
			}
		})
	}
}

func TestStopRuleRefusesBeforeAnyNetworkCall(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(10_000, 9_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) { sse(w, nil) }
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	// Spend 3039 of the 10000 cap: 6961 remain, below the 9000 stop-rule threshold,
	// although a single 3039 reservation would still fit under the cap.
	if err := bundle.ledger.Reserve("earlier-unit", requestReserve); err != nil {
		t.Fatal(err)
	}
	if err := bundle.ledger.Settle("earlier-unit", requestReserve); err != nil {
		t.Fatal(err)
	}
	if !snapshot(t, bundle).StopRule {
		t.Fatal("fixture did not reach the stop rule")
	}
	_, err := conversation(t, f, bundle).NextTurn(context.Background())
	if !errors.Is(err, ErrStopRule) {
		t.Fatalf("stop rule did not refuse the turn: %v", err)
	}
	if n := server.requests.Load(); n != 0 {
		t.Fatalf("fake provider received %d requests; want 0", n)
	}
}

func TestOverageRecordedByTheLedgerStopsAllFurtherRequests(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	// 100000 completion tokens cost far more than the 3039 reserved.
	server.handler = func(w http.ResponseWriter, _ *http.Request) {
		sse(w, map[string]any{"prompt_tokens": 20000, "completion_tokens": 100000})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	if _, err := conversation(t, f, bundle).NextTurn(context.Background()); !errors.Is(err, ErrSpendOverage) {
		t.Fatalf("overage released the turn: %v", err)
	}
	snap := snapshot(t, bundle)
	if !snap.Overspent || snap.SettledMicroUSD != 63000 {
		t.Fatalf("overage not recorded: %+v", snap)
	}
	before := server.requests.Load()
	_, err := bundle.ReserveSpend(context.Background(), providerbridge.SpendReservation{WorkflowID: "w", ConversationID: "c2", Turn: 1, CompletionLimit: 64})
	if !errors.Is(err, usdledger.ErrOverCap) {
		t.Fatalf("reserve after an overage: %v", err)
	}
	if server.requests.Load() != before {
		t.Fatal("request sent after an overage")
	}
}

func TestAmbiguousProviderOutcomeMarksTheUnitUncertain(t *testing.T) {
	for name, handler := range map[string]func(http.ResponseWriter, *http.Request){
		"server error": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "upstream", http.StatusBadGateway) },
		"connection dropped": func(w http.ResponseWriter, _ *http.Request) {
			hijacker, _ := w.(http.Hijacker)
			conn, _, _ := hijacker.Hijack()
			_ = conn.Close()
		},
		"no usage reported": func(w http.ResponseWriter, _ *http.Request) { sse(w, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
			f.genesis()
			var bundle *Bundle
			server := newProviderServer(t, &bundle)
			server.handler = handler
			bundle = f.open(server.URL + "/api/v1/chat/completions")
			if _, err := conversation(t, f, bundle).NextTurn(context.Background()); err == nil {
				t.Fatal("ambiguous outcome released a turn")
			}
			snap := snapshot(t, bundle)
			if snap.UncertainUnits != 1 || snap.UncertainMicroUSD != requestReserve || snap.OpenMicroUSD != 0 || snap.SettledUnits != 0 {
				t.Fatalf("ambiguous outcome not marked uncertain: %+v", snap)
			}
			// The full reservation stays committed: a crash or an unknown cost never frees budget.
			if snap.CommittedMicroUSD != requestReserve {
				t.Fatalf("committed %d, want %d", snap.CommittedMicroUSD, requestReserve)
			}
		})
	}
}

func TestOpenRefusesAnUnfrozenProfile(t *testing.T) {
	open := strings.Replace(frozenProfileJSON, `"frozen_at": "2026-10-17T00:00:00Z"`, `"frozen_at": null`, 1)
	f := newFixture(t, open, ledgerConfigJSON(5_000_000, 20_000, "request"))
	if _, err := open2(f); !errors.Is(err, researchprofile.ErrNotFrozen) {
		t.Fatalf("unfrozen profile opened: %v", err)
	}
	if _, err := Genesis(f.paths); !errors.Is(err, researchprofile.ErrNotFrozen) {
		t.Fatalf("genesis ran for an unfrozen profile: %v", err)
	}
	if _, err := os.Lstat(f.paths.Ledger); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unfrozen profile created a ledger: %v", err)
	}
}

func TestOpenRefusesAnUnpinnedProfile(t *testing.T) {
	unpinned := strings.Replace(frozenProfileJSON, `"order": ["example-provider"]`, `"order": []`, 1)
	f := newFixture(t, unpinned, ledgerConfigJSON(5_000_000, 20_000, "request"))
	if _, err := open2(f); !errors.Is(err, researchprofile.ErrUnpinnedRouting) {
		t.Fatalf("profile with neither a provider pin nor price ceilings opened: %v", err)
	}
}

func TestOpenRefusesAMissingOrUnsafeKey(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
		f.genesis()
		if err := os.Remove(f.paths.Key); err != nil {
			t.Fatal(err)
		}
		if _, err := open2(f); !errors.Is(err, ErrKey) {
			t.Fatalf("missing key opened: %v", err)
		}
	})
	for _, mode := range []os.FileMode{0o644, 0o440, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
			f.genesis()
			f.write(f.paths.Key, testKey, mode)
			if _, err := open2(f); !errors.Is(err, ErrKey) {
				t.Fatalf("mode %v key opened: %v", mode, err)
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
		f.genesis()
		target := f.paths.Key + ".real"
		f.write(target, testKey, 0o400)
		_ = os.Remove(f.paths.Key)
		if err := os.Symlink(target, f.paths.Key); err != nil {
			t.Fatal(err)
		}
		if _, err := open2(f); !errors.Is(err, ErrKey) {
			t.Fatalf("symlinked key opened: %v", err)
		}
	})
}

func TestCredentialsIgnoreTheEnvironment(t *testing.T) {
	t.Setenv(openrouter.APIKeyEnv, "environment-key-that-must-be-ignored")
	t.Setenv(openrouter.APIKeyFileEnv, "/nonexistent/other.key")
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	b := f.open("")
	credentials, err := b.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Redact(testKey) != "[REDACTED]" || credentials.Redact("environment-key-that-must-be-ignored") == "[REDACTED]" {
		t.Fatal("research credentials were not read from the fixed key file")
	}
}

func TestMissingLedgerIsRefusedWithoutAnExplicitGenesis(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	if _, err := open2(f); !errors.Is(err, usdledger.ErrMissingLedger) {
		t.Fatalf("missing ledger opened: %v", err)
	}
	if _, err := os.Lstat(f.paths.Ledger); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused open created the ledger: %v", err)
	}
	f.genesis()
	b := f.open("")
	if snap := snapshot(t, b); snap.CapMicroUSD != 5_000_000 || snap.LedgerID != "research-test" || snap.Records != 1 {
		t.Fatalf("genesis snapshot differs: %+v", snap)
	}
}

func TestDeletedLedgerAfterGenesisIsRefusedNotReset(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	if err := os.Remove(f.paths.Ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := open2(f); !errors.Is(err, usdledger.ErrMissingLedger) {
		t.Fatalf("deleted ledger reopened as a fresh budget: %v", err)
	}
}

func TestGenesisIsOneTimeAndTheLedgerRejectsAChangedCap(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	if _, err := Genesis(f.paths); !errors.Is(err, ErrLedgerExists) {
		t.Fatalf("second genesis: %v", err)
	}
	f.write(f.paths.LedgerConfig, ledgerConfigJSON(9_000_000, 20_000, "request"), 0o600)
	if _, err := open2(f); !errors.Is(err, usdledger.ErrCapMismatch) {
		t.Fatalf("a raised cap reopened the existing ledger: %v", err)
	}
}

func TestLedgerConfigIsStrict(t *testing.T) {
	good := ledgerConfigJSON(5_000_000, 20_000, "request")
	for name, doc := range map[string]string{
		"unknown field":        strings.Replace(good, `"schema_version":1`, `"schema_version":1,"extra":1`, 1),
		"missing basis":        strings.Replace(good, `,"reserve_basis":"request"`, ``, 1),
		"missing cap":          strings.Replace(good, `"cap_micro_usd":5000000,`, ``, 1),
		"unknown basis":        ledgerConfigJSON(5_000_000, 20_000, "tokens"),
		"zero cap":             ledgerConfigJSON(0, 20_000, "request"),
		"stop rule above cap":  ledgerConfigJSON(1000, 2000, "request"),
		"zero stop rule":       ledgerConfigJSON(1000, 0, "request"),
		"wrong schema":         strings.Replace(good, `"schema_version":1`, `"schema_version":2`, 1),
		"empty ledger id":      strings.Replace(good, `"research-test"`, `""`, 1),
		"trailing data":        good + ` {}`,
		"duplicate field":      strings.Replace(good, `"ledger_id":"research-test",`, `"ledger_id":"a","ledger_id":"research-test",`, 1),
		"not an object":        `[]`,
		"fractional cap":       strings.Replace(good, `5000000`, `5000000.5`, 1),
		"negative stop rule":   ledgerConfigJSON(5000, -1, "request"),
		"invalid ledger chars": strings.Replace(good, `"research-test"`, `"research test"`, 1),
	} {
		if _, err := LoadLedgerConfig(strings.NewReader(doc)); err == nil {
			t.Errorf("ledger config accepted %s", name)
		}
	}
	if _, err := LoadLedgerConfig(strings.NewReader(good)); err != nil {
		t.Fatalf("valid ledger config refused: %v", err)
	}
}

func TestConfigFilesMustBeOwnedByTheExpectedUser(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	f.paths.OwnerUID = uint32(os.Getuid()) + 1
	if _, err := open2(f); err == nil {
		t.Fatal("profile owned by another user accepted")
	}
	f.paths.OwnerUID = uint32(os.Getuid())
	f.write(f.paths.Profile, frozenProfileJSON, 0o666)
	if _, err := open2(f); err == nil {
		t.Fatal("group/world-writable profile accepted")
	}
}

func TestRouteBindsTheProfileDigestAndTheFrozenParameters(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	b := f.open("")
	profile, err := researchprofile.Load(strings.NewReader(frozenProfileJSON))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := profile.Digest()
	route := b.Route()
	if b.ProfileDigest() != want || route.ProfileDigest != want || route.Model != researchprofile.Model ||
		route.Endpoint != researchprofile.Endpoint || !route.IsResearch() {
		t.Fatalf("route does not carry the profile: %+v", route)
	}
	changed := strings.Replace(frozenProfileJSON, `"seed": 7`, `"seed": 8`, 1)
	g := newFixture(t, changed, ledgerConfigJSON(5_000_000, 20_000, "request"))
	g.genesis()
	other := g.open("")
	if other.ProfileDigest() == b.ProfileDigest() || other.Route() == route {
		t.Fatal("a changed profile produced the same route or digest")
	}
	if b.MaxRequestsPerUnit() != 5 {
		t.Fatalf("max requests per unit = %d", b.MaxRequestsPerUnit())
	}
}

func TestProviderProfileDigestSeparatesTheRoutes(t *testing.T) {
	const host = "sha256:" + "11111111111111111111111111111111111111111111111111111111111111aa"
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	b := f.open("")
	bound := b.ProviderProfileDigest(host)
	if bound == host || !strings.HasPrefix(bound, "sha256:") || len(bound) != len(host) {
		t.Fatalf("research provider-profile digest %q does not differ from the host digest", bound)
	}
	if b.ProviderProfileDigest(host) != bound || b.ProviderProfileDigest("sha256:"+strings.Repeat("22", 32)) == bound {
		t.Fatal("provider-profile digest is not a deterministic function of host digest and profile digest")
	}
}
