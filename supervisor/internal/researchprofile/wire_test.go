package researchprofile

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExportedEndpointAndModelAreTheFixedIdentity(t *testing.T) {
	if Endpoint != "https://openrouter.ai/api/v1/chat/completions" || Model != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("exported identity drifted: %q %q", Endpoint, Model)
	}
}

func TestRequestMembersRendersTheFrozenProviderParameters(t *testing.T) {
	p := load(t, syntheticJSON)
	got, err := p.RequestMembers()
	if err != nil {
		t.Fatalf("RequestMembers: %v", err)
	}
	const want = `{"provider":{"order":["example-provider"],"allow_fallbacks":false,"data_collection":"deny","require_parameters":true,"zdr":true},"temperature":0,"seed":7,"usage":{"include":true}}`
	if got != want {
		t.Fatalf("members\n got %s\nwant %s", got, want)
	}
	if !json.Valid([]byte(got)) {
		t.Fatal("members are not valid JSON")
	}
}

func TestRequestMembersRendersPriceCeilingsInUSDPerMillionTokens(t *testing.T) {
	doc := replaceOnce(t, syntheticJSON, `"order": ["example-provider"]`, `"order": []`)
	doc = replaceOnce(t, doc, `"max_price_micro_usd_per_1m_tokens_in": null`, `"max_price_micro_usd_per_1m_tokens_in": 270000`)
	doc = replaceOnce(t, doc, `"max_price_micro_usd_per_1m_tokens_out": null`, `"max_price_micro_usd_per_1m_tokens_out": 1100000`)
	doc = replaceOnce(t, doc, `"quantizations": []`, `"quantizations": ["fp8"]`)
	got, err := load(t, doc).RequestMembers()
	if err != nil {
		t.Fatalf("RequestMembers: %v", err)
	}
	const want = `{"provider":{"allow_fallbacks":false,"data_collection":"deny","quantizations":["fp8"],"require_parameters":true,"zdr":true,"max_price":{"prompt":0.27,"completion":1.1}},"temperature":0,"seed":7,"usage":{"include":true}}`
	if got != want {
		t.Fatalf("members\n got %s\nwant %s", got, want)
	}
}

func TestRequestMembersRefusesAnUnfrozenProfile(t *testing.T) {
	if _, err := load(t, draftJSON).RequestMembers(); !errors.Is(err, ErrNotFrozen) {
		t.Fatalf("draft profile rendered request members: %v", err)
	}
}

func TestActualMicroUSDUsesTheWorstPriceAndRoundsUp(t *testing.T) {
	p := load(t, syntheticJSON)
	// (500*1111 + 50*2222) / 1e6 = 0.6666 micro-USD, rounded up to 1.
	got, err := p.ActualMicroUSD(500, 50)
	if err != nil || got != 1 {
		t.Fatalf("ActualMicroUSD = %d, %v; want 1", got, err)
	}
	// Unlike the worst case, actual usage may exceed the frozen token bounds: the
	// caller needs the true cost to record an overage.
	got, err = p.ActualMicroUSD(2_000_000, 1_000_000)
	if err != nil || got != 2_000_000*1111/1_000_000+1_000_000*2222/1_000_000 {
		t.Fatalf("ActualMicroUSD above the bounds = %d, %v", got, err)
	}
	if _, err := load(t, draftJSON).ActualMicroUSD(1, 1); !errors.Is(err, ErrNotFrozen) {
		t.Fatalf("unfrozen profile priced usage: %v", err)
	}
	huge := ^uint64(0)
	if _, err := p.ActualMicroUSD(huge, huge); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflow not refused: %v", err)
	}
}
