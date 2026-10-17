//go:build linux

package researchroute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// fake prices; base 150000/600000, peak ceilings 300000/1200000 micro-USD per 1M
// tokens). Worst case per request with a 64-token completion cap is
// ceil((20000*300000 + 64*1200000)/1e6) = 6077 micro-USD (the ceilings, not the base
// prices, which would give 3039); five requests per unit
// make the unit worst case 30385.
const frozenProfileJSON = `{
  "schema_version": 1,
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "deepseek/deepseek-v4.1-flash",
  "routing": {"order": ["example-provider"], "allow_fallbacks": false, "data_collection": "deny",
              "quantizations": [], "require_parameters": true, "zdr": true},
  "input_micro_usd_per_1m_tokens": 150000,
  "output_micro_usd_per_1m_tokens": 600000,
  "max_price_micro_usd_per_1m_tokens_in": 300000,
  "max_price_micro_usd_per_1m_tokens_out": 1200000,
  "max_input_tokens": 20000,
  "input_framing_reserve_tokens": 512,
  "max_output_tokens": 2000,
  "max_requests_per_unit": 5,
  "temperature_milli": 0,
  "seed": 7,
  "reasoning": {"enabled": false},
  "timeout_ms": 5000,
  "frozen_at": "2026-10-17T00:00:00Z"
}`

const (
	requestReserve = int64(6077)
	unitReserve    = int64(30385)
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
	return conversationWithSystem(t, f, b, "Research system prompt.")
}

// conversationWithSystem is conversation with a chosen system prompt, to size the
// outbound request body.
func conversationWithSystem(t *testing.T, f *fixture, b *Bundle, system string) *providerbridge.Conversation {
	t.Helper()
	journalDir := t.TempDir()
	if err := os.Chmod(journalDir, 0o700); err != nil { // t.TempDir is 0755 under umask 022
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(journalDir, "provider.jsonl"))
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
		SystemPrompt: system, DeveloperPrompt: "Research developer prompt.",
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
	// The provider cost 2 is within the ceiling price of the tokens, ceil((11*300000+3*1200000)/1e6) = 7.
	if snap.OpenMicroUSD != 0 || snap.SettledUnits != 1 || snap.SettledMicroUSD != 2 || snap.UncertainMicroUSD != 0 {
		t.Fatalf("settlement differs: %+v", snap)
	}
}

func TestSettlementUsesTheProviderCostWhenItIsWithinTheCeilingPricedTokens(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) {
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.0000043})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	if _, err := conversation(t, f, bundle).NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snap := snapshot(t, bundle); snap.SettledMicroUSD != 5 {
		t.Fatalf("settled %d micro-USD; want the provider cost 4.3 rounded up to 5, not the ceiling price 7", snap.SettledMicroUSD)
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
	if snap := snapshot(t, bundle); snap.SettledMicroUSD != 7 || snap.OpenMicroUSD != 0 {
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
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(20_000, 15_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) { sse(w, nil) }
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	// Spend 6077 of the 20000 cap: 13923 remain, below the 15000 stop-rule threshold,
	// although a single 6077 reservation would still fit under the cap.
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
	// 100000 completion tokens cost far more than the 6077 reserved.
	server.handler = func(w http.ResponseWriter, _ *http.Request) {
		sse(w, map[string]any{"prompt_tokens": 20000, "completion_tokens": 100000})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	if _, err := conversation(t, f, bundle).NextTurn(context.Background()); !errors.Is(err, ErrSpendOverage) {
		t.Fatalf("overage released the turn: %v", err)
	}
	snap := snapshot(t, bundle)
	if !snap.Overspent || snap.SettledMicroUSD != 126000 {
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

func TestOpenRefusesAProfileWithoutPriceCeilingsEvenWithAProviderOrder(t *testing.T) {
	noCeilings := func(profile string) string {
		profile = strings.Replace(profile, `"max_price_micro_usd_per_1m_tokens_in": 300000`, `"max_price_micro_usd_per_1m_tokens_in": null`, 1)
		return strings.Replace(profile, `"max_price_micro_usd_per_1m_tokens_out": 1200000`, `"max_price_micro_usd_per_1m_tokens_out": null`, 1)
	}
	for name, profile := range map[string]string{
		"provider order set":   noCeilings(frozenProfileJSON),
		"empty provider order": strings.Replace(noCeilings(frozenProfileJSON), `"order": ["example-provider"]`, `"order": []`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, profile, ledgerConfigJSON(5_000_000, 20_000, "request"))
			if _, err := open2(f); !errors.Is(err, researchprofile.ErrPriceCeilingRequired) || !errors.Is(err, researchprofile.ErrNotFrozen) {
				t.Fatalf("profile without price ceilings opened: %v", err)
			}
			if _, err := Genesis(f.paths); !errors.Is(err, researchprofile.ErrPriceCeilingRequired) {
				t.Fatalf("genesis ran for a profile without price ceilings: %v", err)
			}
			if _, err := os.Lstat(f.paths.Ledger); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a profile without ceilings created a ledger: %v", err)
			}
		})
	}
}

func TestOpenAcceptsPriceCeilingsInPlaceOfAProviderPin(t *testing.T) {
	unpinned := strings.Replace(frozenProfileJSON, `"order": ["example-provider"]`, `"order": []`, 1)
	f := newFixture(t, unpinned, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	f.open("")
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

// repeatedWords is a system prompt of n five-byte words ("word "), n*5-1 bytes.
func repeatedWords(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }

// ceilingMicro is the worst-case price of tokens at the 300000/1200000 ceilings.
func ceilingMicro(prompt, completion uint64) int64 {
	return int64((prompt*300000 + completion*1200000 + 999_999) / 1_000_000)
}

func withMaxInput(max int) string {
	return strings.Replace(frozenProfileJSON, `"max_input_tokens": 20000`, fmt.Sprintf(`"max_input_tokens": %d`, max), 1)
}

// refusedBeforeAnyReservation asserts that NextTurn refused with want, that the
// provider saw no request, and that the ledger gained no record.
func refusedBeforeAnyReservation(t *testing.T, err, want error, server *providerServer, before, after usdledger.Snapshot) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("NextTurn error %v; want %v", err, want)
	}
	if n := server.requests.Load(); n != 0 {
		t.Fatalf("fake provider received %d requests; want 0", n)
	}
	if after.Records != before.Records || after.CommittedMicroUSD != 0 || after.OpenMicroUSD != 0 {
		t.Fatalf("a refused request touched the ledger: before %+v after %+v", before, after)
	}
}

func TestOversizedRequestBodyIsRefusedBeforeReservationAndDispatch(t *testing.T) {
	// The reviewer's scenario: a 100,000-word system prompt against
	// max_input_tokens=20000 used to be accepted with the reservation unchanged.
	for _, basis := range []string{"request", "unit"} {
		t.Run(basis, func(t *testing.T) {
			f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, basis))
			f.genesis()
			var bundle *Bundle
			server := newProviderServer(t, &bundle)
			server.handler = func(w http.ResponseWriter, _ *http.Request) { sse(w, nil) }
			bundle = f.open(server.URL + "/api/v1/chat/completions")
			before := snapshot(t, bundle)
			_, err := conversationWithSystem(t, f, bundle, repeatedWords(100_000)).NextTurn(context.Background())
			refusedBeforeAnyReservation(t, err, ErrInputOverBound, server, before, snapshot(t, bundle))
		})
	}
}

// framingReserve is input_framing_reserve_tokens in frozenProfileJSON.
const framingReserve = 512

func TestRequestBodyExactlyAtTheBoundIsAcceptedAndOneByteOverIsRefused(t *testing.T) {
	system := repeatedWords(1500)
	// Measure the exact serialized body this conversation sends. The body does not
	// depend on max_input_tokens.
	var length int
	{
		f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
		f.genesis()
		var bundle *Bundle
		server := newProviderServer(t, &bundle)
		server.handler = func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			length = len(raw)
			sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3})
		}
		bundle = f.open(server.URL + "/api/v1/chat/completions")
		if _, err := conversationWithSystem(t, f, bundle, system).NextTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if length < 7000 {
		t.Fatalf("measured body of %d bytes is not the large body this test needs", length)
	}

	t.Run("at the bound", func(t *testing.T) {
		f := newFixture(t, withMaxInput(length+framingReserve), ledgerConfigJSON(5_000_000, 20_000, "request"))
		f.genesis()
		var bundle *Bundle
		server := newProviderServer(t, &bundle)
		server.handler = func(w http.ResponseWriter, _ *http.Request) {
			sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3})
		}
		bundle = f.open(server.URL + "/api/v1/chat/completions")
		if _, err := conversationWithSystem(t, f, bundle, system).NextTurn(context.Background()); err != nil {
			t.Fatalf("a body at max_input_tokens minus the framing reserve was refused: %v", err)
		}
		if got, want := server.openAtHit.Load(), ceilingMicro(uint64(length+framingReserve), 64); got != want {
			t.Fatalf("reserved %d micro-USD for a %d-byte body; want %d (max_input_tokens in full)", got, length, want)
		}
	})
	t.Run("one byte over", func(t *testing.T) {
		f := newFixture(t, withMaxInput(length+framingReserve-1), ledgerConfigJSON(5_000_000, 20_000, "request"))
		f.genesis()
		var bundle *Bundle
		server := newProviderServer(t, &bundle)
		server.handler = func(w http.ResponseWriter, _ *http.Request) { sse(w, nil) }
		bundle = f.open(server.URL + "/api/v1/chat/completions")
		before := snapshot(t, bundle)
		_, err := conversationWithSystem(t, f, bundle, system).NextTurn(context.Background())
		refusedBeforeAnyReservation(t, err, ErrInputOverBound, server, before, snapshot(t, bundle))
	})
}

func TestNearCapLedgerWithALargeAllowedBodyReservesTheCeilingAmount(t *testing.T) {
	system := repeatedWords(3000) // about 15 KB, allowed under max_input_tokens=20000
	// Base prices would reserve 3039, which fits the 6076 cap; only ceiling prices
	// (6077) are refused there.
	t.Run("cap equals the ceiling reservation", func(t *testing.T) {
		f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(requestReserve, 1000, "request"))
		f.genesis()
		var bundle *Bundle
		server := newProviderServer(t, &bundle)
		var length int
		server.handler = func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			length = len(raw)
			sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3})
		}
		bundle = f.open(server.URL + "/api/v1/chat/completions")
		if _, err := conversationWithSystem(t, f, bundle, system).NextTurn(context.Background()); err != nil {
			t.Fatalf("near-cap request refused: %v", err)
		}
		if length < 15_000 || length > 20_000 {
			t.Fatalf("body of %d bytes is not large-but-allowed", length)
		}
		if got := server.openAtHit.Load(); got != requestReserve {
			t.Fatalf("reserved %d micro-USD; want %d", got, requestReserve)
		}
	})
	t.Run("cap one micro-USD below", func(t *testing.T) {
		f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(requestReserve-1, 1000, "request"))
		f.genesis()
		var bundle *Bundle
		server := newProviderServer(t, &bundle)
		server.handler = func(w http.ResponseWriter, _ *http.Request) { sse(w, nil) }
		bundle = f.open(server.URL + "/api/v1/chat/completions")
		before := snapshot(t, bundle)
		_, err := conversationWithSystem(t, f, bundle, system).NextTurn(context.Background())
		refusedBeforeAnyReservation(t, err, usdledger.ErrCapExceeded, server, before, snapshot(t, bundle))
	})
}

func TestPriceCeilingsAreSentOnEveryRequest(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	var body []byte
	server.handler = func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	if _, err := conversation(t, f, bundle).NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"max_price":{"prompt":0.3,"completion":1.2}`)) {
		t.Fatalf("outbound body does not carry the price ceilings: %s", body)
	}
	var decoded struct {
		Provider struct {
			MaxPrice struct{ Prompt, Completion json.Number } `json:"max_price"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Provider.MaxPrice.Prompt != "0.3" || decoded.Provider.MaxPrice.Completion != "1.2" {
		t.Fatalf("provider.max_price is not {prompt 0.3, completion 1.2}: %+v, %v", decoded, err)
	}
}

func TestMissingCostIsSettledAtTheCeilingPrices(t *testing.T) {
	// 20000 prompt + 64 completion tokens: 6077 micro-USD at the 0.30/1.20 peak
	// ceilings, 3039 at the 0.15/0.60 base prices. Settlement must not release the
	// reservation at the lower figure.
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) {
		sse(w, map[string]any{"prompt_tokens": 20000, "completion_tokens": 64})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	if _, err := conversation(t, f, bundle).NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, bundle)
	if snap.SettledMicroUSD != 6077 || snap.OpenMicroUSD != 0 || snap.Overspent || snap.UncertainUnits != 0 {
		t.Fatalf("token-only settlement is not at ceiling prices: %+v", snap)
	}
}

func TestCostAboveTheCeilingPricedTokensIsAnOverageAnomaly(t *testing.T) {
	// 11 prompt + 3 completion tokens cost at most 7 micro-USD at the ceilings.
	run := func(t *testing.T, usage map[string]any) (*Bundle, error) {
		f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
		f.genesis()
		var bundle *Bundle
		server := newProviderServer(t, &bundle)
		server.handler = func(w http.ResponseWriter, _ *http.Request) { sse(w, usage) }
		bundle = f.open(server.URL + "/api/v1/chat/completions")
		_, err := conversation(t, f, bundle).NextTurn(context.Background())
		return bundle, err
	}
	refusesFurtherRequests := func(t *testing.T, bundle *Bundle) {
		t.Helper()
		_, err := bundle.ReserveSpend(context.Background(), providerbridge.SpendReservation{WorkflowID: "w", ConversationID: "c2", Turn: 1, CompletionLimit: 64})
		if !errors.Is(err, usdledger.ErrOverCap) {
			t.Fatalf("reserve after an anomaly: %v", err)
		}
	}
	t.Run("within the reservation", func(t *testing.T) {
		bundle, err := run(t, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000013})
		if !errors.Is(err, ErrSpendOverage) {
			t.Fatalf("anomalous cost released the turn: %v", err)
		}
		// The ledger records a stop only through SettleOverage, which needs an amount
		// above the reservation: the anomaly is booked at reservation+1.
		if snap := snapshot(t, bundle); !snap.Overspent || snap.SettledMicroUSD != requestReserve+1 || snap.OpenMicroUSD != 0 {
			t.Fatalf("anomaly not recorded as an overage: %+v", snap)
		}
		refusesFurtherRequests(t, bundle)
	})
	t.Run("above the reservation", func(t *testing.T) {
		bundle, err := run(t, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.01})
		if !errors.Is(err, ErrSpendOverage) {
			t.Fatalf("anomalous cost released the turn: %v", err)
		}
		if snap := snapshot(t, bundle); !snap.Overspent || snap.SettledMicroUSD != 10_000 {
			t.Fatalf("overage not recorded at the reported cost: %+v", snap)
		}
		refusesFurtherRequests(t, bundle)
	})
	t.Run("cost equal to the ceiling price is genuine", func(t *testing.T) {
		bundle, err := run(t, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000007})
		if err != nil {
			t.Fatal(err)
		}
		if snap := snapshot(t, bundle); snap.Overspent || snap.SettledMicroUSD != 7 {
			t.Fatalf("genuine cost mis-settled: %+v", snap)
		}
	})
	t.Run("cost without token counts is settled as reported", func(t *testing.T) {
		bundle, err := run(t, map[string]any{"cost": 0.000013})
		if err != nil {
			t.Fatal(err)
		}
		if snap := snapshot(t, bundle); snap.Overspent || snap.SettledMicroUSD != 13 {
			t.Fatalf("cost without tokens mis-settled: %+v", snap)
		}
	})
}

// declaredAboveCeilingProfileJSON declares 500000 micro-USD per 1M input tokens
// but transmits a ceiling of 300000, so ActualMicroUSD (the larger of the two)
// prices 1000 input tokens at 500 while the ceiling allows only 300.
func declaredAboveCeilingProfileJSON() string {
	return strings.Replace(frozenProfileJSON, `"input_micro_usd_per_1m_tokens": 150000`, `"input_micro_usd_per_1m_tokens": 500000`, 1)
}

func TestCostAboveTheTransmittedCeilingIsAnAnomalyEvenWhenTheDeclaredPriceIsHigher(t *testing.T) {
	run := func(t *testing.T, usage map[string]any) (*Bundle, error) {
		f := newFixture(t, declaredAboveCeilingProfileJSON(), ledgerConfigJSON(5_000_000, 20_000, "request"))
		f.genesis()
		var bundle *Bundle
		server := newProviderServer(t, &bundle)
		server.handler = func(w http.ResponseWriter, _ *http.Request) { sse(w, usage) }
		bundle = f.open(server.URL + "/api/v1/chat/completions")
		_, err := conversation(t, f, bundle).NextTurn(context.Background())
		return bundle, err
	}
	t.Run("400 micro-USD for 1000 input tokens under a 300 ceiling", func(t *testing.T) {
		bundle, err := run(t, map[string]any{"prompt_tokens": 1000, "completion_tokens": 0, "cost": 0.0004})
		if !errors.Is(err, ErrSpendOverage) {
			t.Fatalf("a charge above the transmitted ceiling was not an anomaly: %v", err)
		}
		if snap := snapshot(t, bundle); !snap.Overspent {
			t.Fatalf("anomaly did not stop the ledger: %+v", snap)
		}
	})
	t.Run("301 micro-USD is above the ceiling", func(t *testing.T) {
		if _, err := run(t, map[string]any{"prompt_tokens": 1000, "completion_tokens": 0, "cost": 0.000301}); !errors.Is(err, ErrSpendOverage) {
			t.Fatalf("301 over a 300 ceiling: %v", err)
		}
	})
	t.Run("a cost equal to the ceiling price is genuine", func(t *testing.T) {
		bundle, err := run(t, map[string]any{"prompt_tokens": 1000, "completion_tokens": 0, "cost": 0.0003})
		if err != nil {
			t.Fatal(err)
		}
		if snap := snapshot(t, bundle); snap.Overspent || snap.SettledMicroUSD != 300 {
			t.Fatalf("genuine cost mis-settled: %+v", snap)
		}
	})
	t.Run("a missing cost still settles at the conservative maximum price", func(t *testing.T) {
		bundle, err := run(t, map[string]any{"prompt_tokens": 1000, "completion_tokens": 0})
		if err != nil {
			t.Fatal(err)
		}
		if snap := snapshot(t, bundle); snap.Overspent || snap.SettledMicroUSD != 500 {
			t.Fatalf("token-only settlement is not at the larger of declared and ceiling: %+v", snap)
		}
	})
}

// WorkflowUnits shows one workflow's units in every lifecycle state: open while the request is
// in flight, settled after a reported cost, uncertain after an ambiguous outcome. Units of other
// workflows and conversations are not reported.
func TestWorkflowUnitsReportOpenSettledAndUncertainUnits(t *testing.T) {
	probe := func(bundle *Bundle) []usdledger.UnitInfo {
		t.Helper()
		units, err := bundle.WorkflowUnits("workflow-1", []string{"conversation-1", "conversation-2"}, 5)
		if err != nil {
			t.Fatal(err)
		}
		return units
	}
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	var inFlight []usdledger.UnitInfo
	server := newProviderServer(t, &bundle)
	server.handler = func(w http.ResponseWriter, _ *http.Request) {
		inFlight = probe(bundle)
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	if units := probe(bundle); len(units) != 0 {
		t.Fatalf("units before any request: %+v", units)
	}
	if _, err := conversation(t, f, bundle).NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(inFlight) != 1 || inFlight[0].State != usdledger.UnitStateOpen || inFlight[0].ReservedMicroUSD != requestReserve {
		t.Fatalf("the in-flight request is not an open unit: %+v", inFlight)
	}
	if units := probe(bundle); len(units) != 1 || units[0].State != usdledger.UnitStateSettled || units[0].SettledMicroUSD != 2 {
		t.Fatalf("the answered request is not a settled unit: %+v", units)
	}
	if other, err := bundle.WorkflowUnits("workflow-2", []string{"conversation-1"}, 5); err != nil || len(other) != 0 {
		t.Fatalf("another workflow's probe reported units: %+v err=%v", other, err)
	}
	for _, bad := range []struct {
		workflow string
		turns    uint64
	}{{"", 5}, {"workflow-1", 0}, {"workflow-1", maxWorkflowUnitTurns + 1}} {
		if _, err := bundle.WorkflowUnits(bad.workflow, []string{"conversation-1"}, bad.turns); err == nil {
			t.Fatalf("probe %+v was accepted", bad)
		}
	}

	g := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	g.genesis()
	var ambiguous *Bundle
	failing := newProviderServer(t, &ambiguous)
	failing.handler = func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "upstream", http.StatusBadGateway) }
	ambiguous = g.open(failing.URL + "/api/v1/chat/completions")
	if _, err := conversation(t, g, ambiguous).NextTurn(context.Background()); err == nil {
		t.Fatal("an ambiguous outcome released a turn")
	}
	if units := probe(ambiguous); len(units) != 1 || units[0].State != usdledger.UnitStateUncertain || units[0].ReservedMicroUSD != requestReserve {
		t.Fatalf("the ambiguous request is not an uncertain unit: %+v", units)
	}
}
