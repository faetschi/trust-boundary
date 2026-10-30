//go:build linux

package researchroute

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
)

const (
	datedSlug   = "deepseek/deepseek-v4.1-flash-20260910"
	variantSlug = "deepseek/deepseek-v4.1-flash-1017"
)

func profileWithResponseModels(list string) string {
	return strings.Replace(frozenProfileJSON, `"model": "deepseek/deepseek-v4.1-flash",`,
		`"model": "deepseek/deepseek-v4.1-flash", "allowed_response_models": `+list+`,`, 1)
}

func TestLedgerConfigIsBoundIntoTheProviderProfileDigest(t *testing.T) {
	const host = "sha256:11111111111111111111111111111111111111111111111111111111111111aa"
	digestOf := func(config string) string {
		f := newFixture(t, frozenProfileJSON, config)
		f.genesis()
		return f.open("").ProviderProfileDigest(host)
	}
	base := digestOf(ledgerConfigJSON(5_000_000, 20_000, "request"))
	reformatted := digestOf("{\n \"reserve_basis\": \"request\",\n \"schema_version\": 1, \"ledger_id\": \"research-test\",\n" +
		" \"cap_micro_usd\": 5000000, \"min_per_unit_worst_case_micro_usd\": 20000}\n")
	if reformatted != base {
		t.Fatal("formatting and key order of the ledger configuration changed the pin")
	}
	for name, config := range map[string]string{
		"cap":      ledgerConfigJSON(4_000_000, 20_000, "request"),
		"stop":     ledgerConfigJSON(5_000_000, 20_001, "request"),
		"basis":    ledgerConfigJSON(5_000_000, 20_000, "unit"),
		"ledgerID": strings.Replace(ledgerConfigJSON(5_000_000, 20_000, "request"), "research-test", "research-other", 1),
	} {
		if got := digestOf(config); got == base {
			t.Errorf("changing the ledger %s left the provider-profile digest unchanged", name)
		}
	}
}

func TestResearchRequestSendsOnlyTheHeadersOpenRouterNeeds(t *testing.T) {
	f := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	f.genesis()
	var bundle *Bundle
	server := newProviderServer(t, &bundle)
	var seen http.Header
	var body string
	server.handler = func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		sse(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002})
	}
	bundle = f.open(server.URL + "/api/v1/chat/completions")
	if _, err := conversation(t, f, bundle).NextTurn(context.Background()); err != nil {
		t.Fatalf("NextTurn: %v", err)
	}
	// Authorization and Content-Type are sent by the client; Content-Length,
	// Accept-Encoding and the default User-Agent are added by net/http itself.
	allowed := map[string]bool{"Authorization": true, "Content-Type": true, "Content-Length": true,
		"Accept-Encoding": true, "User-Agent": true}
	var names []string
	for name := range seen {
		names = append(names, name)
		if !allowed[name] {
			t.Errorf("research request leaked header %q", name)
		}
	}
	sort.Strings(names)
	if seen.Get("Authorization") != "Bearer "+testKey || seen.Get("Content-Type") != "application/json" {
		t.Fatalf("required headers missing: %v", names)
	}
	if strings.Contains(seen.Get("User-Agent"), "tbound") {
		t.Fatalf("research request identifies the host in its User-Agent: %q", seen.Get("User-Agent"))
	}
	if !strings.Contains(body, `,"reasoning":{"enabled":false}}`) || strings.Contains(body, `"usage"`) {
		t.Fatalf("request does not carry the frozen reasoning setting (or still sends usage): %s", body)
	}
}

func TestResponseModelOutsideTheAllowedListIsUncertain(t *testing.T) {
	for name, tc := range map[string]struct {
		profile string
		model   string
		ok      bool
	}{
		"default list refuses the dated slug":      {frozenProfileJSON, datedSlug, false},
		"pinned list accepts the canonical slug":   {profileWithResponseModels(`["deepseek/deepseek-v4.1-flash", "` + datedSlug + `"]`), datedSlug, true},
		"pinned list accepts the requested slug":   {profileWithResponseModels(`["deepseek/deepseek-v4.1-flash", "` + datedSlug + `"]`), "deepseek/deepseek-v4.1-flash", true},
		"pinned list does not admit the variant":   {profileWithResponseModels(`["deepseek/deepseek-v4.1-flash", "` + datedSlug + `"]`), variantSlug, false},
		"a prefix of an allowed slug is not exact": {profileWithResponseModels(`["deepseek/deepseek-v4.1-flash", "` + datedSlug + `"]`), "deepseek/deepseek-v4.1", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, tc.profile, ledgerConfigJSON(5_000_000, 20_000, "request"))
			f.genesis()
			var bundle *Bundle
			server := newProviderServer(t, &bundle)
			server.handler = func(w http.ResponseWriter, _ *http.Request) {
				sseModel(w, map[string]any{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002}, tc.model)
			}
			bundle = f.open(server.URL + "/api/v1/chat/completions")
			_, err := conversation(t, f, bundle).NextTurn(context.Background())
			snap := snapshot(t, bundle)
			if tc.ok {
				if err != nil || snap.SettledUnits != 1 || snap.UncertainUnits != 0 {
					t.Fatalf("allowed response model refused: %v %+v", err, snap)
				}
				return
			}
			if err == nil {
				t.Fatal("a response model outside the allowed list released a turn")
			}
			if snap.UncertainUnits != 1 || snap.UncertainMicroUSD != requestReserve || snap.SettledUnits != 0 {
				t.Fatalf("response model outside the list not left uncertain: %+v", snap)
			}
		})
	}
}

func TestResponseModelListChangesTheRouteAndTheDigest(t *testing.T) {
	a := newFixture(t, frozenProfileJSON, ledgerConfigJSON(5_000_000, 20_000, "request"))
	a.genesis()
	b := newFixture(t, profileWithResponseModels(`["deepseek/deepseek-v4.1-flash", "`+datedSlug+`"]`), ledgerConfigJSON(5_000_000, 20_000, "request"))
	b.genesis()
	first, second := a.open(""), b.open("")
	if first.ProfileDigest() == second.ProfileDigest() || first.Route() == second.Route() {
		t.Fatal("the allowed response-model list does not change the route or the profile digest")
	}
	if !second.Route().AcceptsResponseModel(datedSlug) || first.Route().AcceptsResponseModel(datedSlug) {
		t.Fatal("route does not carry the profile's response-model list")
	}
}
