package researchprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
)

// draftJSON is an all-OPEN draft: every decision is null, including the fixed
// routing flags, so no field is decided before the freeze.
const draftJSON = `{
  "schema_version": 1,
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "deepseek/deepseek-v4.1-flash",
  "routing": {
    "order": null,
    "allow_fallbacks": null,
    "data_collection": null,
    "quantizations": null,
    "require_parameters": null,
    "zdr": null
  },
  "input_micro_usd_per_1m_tokens": null,
  "output_micro_usd_per_1m_tokens": null,
  "max_price_micro_usd_per_1m_tokens_in": null,
  "max_price_micro_usd_per_1m_tokens_out": null,
  "max_input_tokens": null,
  "max_output_tokens": null,
  "max_requests_per_unit": null,
  "temperature_milli": null,
  "seed": null,
  "reasoning": null,
  "timeout_ms": null,
  "frozen_at": null
}`

// syntheticJSON is a fully decided profile with obviously fake numbers and a
// fake provider slug. It is a test fixture only, not a research price or route.
const syntheticJSON = `{
  "schema_version": 1,
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "deepseek/deepseek-v4.1-flash",
  "routing": {
    "order": ["example-provider"],
    "allow_fallbacks": false,
    "data_collection": "deny",
    "quantizations": [],
    "require_parameters": true,
    "zdr": true
  },
  "input_micro_usd_per_1m_tokens": 1111,
  "output_micro_usd_per_1m_tokens": 2222,
  "max_price_micro_usd_per_1m_tokens_in": 1111,
  "max_price_micro_usd_per_1m_tokens_out": 2222,
  "max_input_tokens": 1000,
  "max_output_tokens": 100,
  "max_requests_per_unit": 3,
  "temperature_milli": 0,
  "seed": 7,
  "reasoning": {"enabled": false},
  "timeout_ms": 1234,
  "frozen_at": "2026-10-15T00:00:00Z"
}`

// syntheticCanonical is the canonical encoding of syntheticJSON: fixed field
// order, no insignificant whitespace, explicit nulls. Digest must be its SHA-256.
const syntheticCanonical = `{"schema_version":1,"endpoint":"https://openrouter.ai/api/v1/chat/completions","model":"deepseek/deepseek-v4.1-flash","allowed_response_models":["deepseek/deepseek-v4.1-flash"],"routing":{"order":["example-provider"],"allow_fallbacks":false,"data_collection":"deny","quantizations":[],"require_parameters":true,"zdr":true},"input_micro_usd_per_1m_tokens":1111,"output_micro_usd_per_1m_tokens":2222,"max_price_micro_usd_per_1m_tokens_in":1111,"max_price_micro_usd_per_1m_tokens_out":2222,"max_input_tokens":1000,"max_output_tokens":100,"max_requests_per_unit":3,"temperature_milli":0,"seed":7,"reasoning":{"enabled":false},"timeout_ms":1234,"frozen_at":"2026-10-15T00:00:00Z"}`

func load(t *testing.T, doc string) Profile {
	t.Helper()
	p, err := Load(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func mustDigest(t *testing.T, p Profile) string {
	t.Helper()
	d, err := p.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	return d
}

// replaceOnce edits a fixture. The target must occur exactly once, so a typo
// cannot turn a mutation into a silent no-op.
func replaceOnce(t *testing.T, doc, old, repl string) string {
	t.Helper()
	if n := strings.Count(doc, old); n != 1 {
		t.Fatalf("fixture target %q occurs %d times, want 1", old, n)
	}
	return strings.Replace(doc, old, repl, 1)
}

// requireReason fails unless err is the sentinel want and its message names
// the field or rule in detail.
func requireReason(t *testing.T, err error, want error, detail string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("reason %v, want %v", err, want)
	}
	if !strings.Contains(err.Error(), detail) {
		t.Fatalf("reason %q does not name %q", err, detail)
	}
}

// requireMessage fails unless Validate or Load rejected the input with a message
// containing want.
func requireMessage(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("accepted input; want rejection containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("rejection %q does not contain %q", err, want)
	}
}

func ptr[T any](v T) *T { return &v }

func TestLoadDraftIsValidButOpen(t *testing.T) {
	p := load(t, draftJSON)
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate(draft): %v", err)
	}
	if p.Frozen() {
		t.Fatal("all-OPEN draft reported Frozen")
	}
	requireReason(t, p.FrozenReason(), ErrOpenField, "routing.order")
}

func TestLoadSyntheticProfileIsFrozen(t *testing.T) {
	p := load(t, syntheticJSON)
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate(synthetic): %v", err)
	}
	if err := p.FrozenReason(); err != nil {
		t.Fatalf("fully decided synthetic profile: %v", err)
	}
	if !p.Frozen() {
		t.Fatal("fully decided synthetic profile did not report Frozen")
	}
}

func TestLoadRejectsMalformedDocuments(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"unknown top-level field", replaceOnce(t, draftJSON, `"frozen_at": null`, `"frozen_at": null, "extra": 1`), `unknown research profile field "extra"`},
		{"unknown routing field", replaceOnce(t, draftJSON, `"zdr": null`, `"zdr": null, "extra": true`), `unknown research profile field "extra"`},
		{"missing top-level field", replaceOnce(t, draftJSON, `"seed": null,`, ``), `missing research profile field "seed"`},
		{"missing routing quantizations", replaceOnce(t, draftJSON, "\"quantizations\": null,\n    \"require_parameters\": null,", `"require_parameters": null,`), `missing research profile field "quantizations"`},
		{"missing routing require_parameters", replaceOnce(t, draftJSON, "\"require_parameters\": null,\n    \"zdr\": null", `"zdr": null`), `missing research profile field "require_parameters"`},
		{"missing routing zdr", replaceOnce(t, draftJSON, "\"require_parameters\": null,\n    \"zdr\": null", `"require_parameters": null`), `missing research profile field "zdr"`},
		{"duplicate field", replaceOnce(t, draftJSON, `"seed": null,`, `"seed": null, "seed": 7,`), `duplicate research profile field "seed"`},
		{"trailing value", draftJSON + ` {}`, "trailing data after research profile"},
		{"array instead of object", `[]`, "research profile object expected"},
		{"null document", `null`, "research profile object expected"},
		{"float money", replaceOnce(t, draftJSON, `"input_micro_usd_per_1m_tokens": null`, `"input_micro_usd_per_1m_tokens": 1.5`), "cannot unmarshal number 1.5"},
		{"exponent money", replaceOnce(t, draftJSON, `"output_micro_usd_per_1m_tokens": null`, `"output_micro_usd_per_1m_tokens": 1e3`), "cannot unmarshal number 1e3"},
		{"negative money", replaceOnce(t, draftJSON, `"input_micro_usd_per_1m_tokens": null`, `"input_micro_usd_per_1m_tokens": -1`), "cannot unmarshal number -1"},
		{"zero price", replaceOnce(t, draftJSON, `"output_micro_usd_per_1m_tokens": null`, `"output_micro_usd_per_1m_tokens": 0`), "output_micro_usd_per_1m_tokens must be positive"},
		{"zero seed", replaceOnce(t, draftJSON, `"seed": null`, `"seed": 0`), "seed must be positive"},
		{"zero ceiling", replaceOnce(t, draftJSON, `"max_price_micro_usd_per_1m_tokens_in": null`, `"max_price_micro_usd_per_1m_tokens_in": 0`), "max_price_micro_usd_per_1m_tokens_in must be positive"},
		{"ceiling pair half set", replaceOnce(t, syntheticJSON, `"max_price_micro_usd_per_1m_tokens_in": 1111`, `"max_price_micro_usd_per_1m_tokens_in": null`), "must be set together"},
		{"temperature above 2", replaceOnce(t, draftJSON, `"temperature_milli": null`, `"temperature_milli": 2001`), "temperature_milli must be from 0 to 2000"},
		{"date without time", replaceOnce(t, draftJSON, `"frozen_at": null`, `"frozen_at": "2026-10-15"`), "frozen_at must be an RFC 3339 timestamp"},
		{"unknown data_collection", replaceOnce(t, draftJSON, `"data_collection": null`, `"data_collection": "maybe"`), `data_collection must be "allow" or "deny"`},
		{"null endpoint", replaceOnce(t, draftJSON, `"endpoint": "https://openrouter.ai/api/v1/chat/completions"`, `"endpoint": null`), "endpoint must use https"},
		{"oversized document", strings.Repeat(" ", maxProfileBytes) + draftJSON, "exceeds the size limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(tc.doc))
			requireMessage(t, err, tc.want)
		})
	}
}

func TestLoadRejectsUnsafeEndpointOrModel(t *testing.T) {
	const endpoint = `"https://openrouter.ai/api/v1/chat/completions"`
	endpointCases := []struct{ value, want string }{
		{`"http://openrouter.ai/api/v1/chat/completions"`, "endpoint must use https"},
		{`"HTTP://openrouter.ai/api/v1/chat/completions"`, "endpoint must use https"},
		{`"https://example.invalid/api/v1/chat/completions"`, "endpoint host must be openrouter.ai"},
		{`"https://openrouter.ai.example/api/v1/chat/completions"`, "endpoint host must be openrouter.ai"},
		{`"https://openrouter.ai:8443/api/v1/chat/completions"`, "endpoint host must be openrouter.ai"},
		{`"https://user@openrouter.ai/api/v1/chat/completions"`, "documented chat completions URL"},
		{`"https://openrouter.ai/api/v1/responses"`, "documented chat completions URL"},
		{`"https://openrouter.ai/api/v1/chat/completions?x=1"`, "documented chat completions URL"},
		{`"https://openrouter.ai/api/v1/chat/completions#frag"`, "documented chat completions URL"},
		{`"https://openrouter.ai/api/v1/chat/completions/"`, "documented chat completions URL"},
	}
	for _, tc := range endpointCases {
		doc := replaceOnce(t, draftJSON, endpoint, tc.value)
		_, err := Load(strings.NewReader(doc))
		requireMessage(t, err, tc.want)
	}
	modelCases := []string{`"deepseek-v4.1-flash"`, `"deepseek/deepseek-v4.1-flash "`, `"DeepSeek/deepseek-v4.1-flash"`}
	for _, bad := range modelCases {
		doc := replaceOnce(t, draftJSON, `"deepseek/deepseek-v4.1-flash"`, bad)
		_, err := Load(strings.NewReader(doc))
		requireMessage(t, err, "model must be")
	}
}

func TestFrozenRequiresEveryOpenField(t *testing.T) {
	cases := []struct {
		name  string
		unset func(*Profile)
		field string
	}{
		{"input price", func(p *Profile) { p.InputMicroUSDPer1M = nil }, "input_micro_usd_per_1m_tokens"},
		{"output price", func(p *Profile) { p.OutputMicroUSDPer1M = nil }, "output_micro_usd_per_1m_tokens"},
		{"max input tokens", func(p *Profile) { p.MaxInputTokens = nil }, "max_input_tokens"},
		{"max output tokens", func(p *Profile) { p.MaxOutputTokens = nil }, "max_output_tokens"},
		{"max requests per unit", func(p *Profile) { p.MaxRequestsPerUnit = nil }, "max_requests_per_unit"},
		{"temperature", func(p *Profile) { p.TemperatureMilli = nil }, "temperature_milli"},
		{"seed", func(p *Profile) { p.Seed = nil }, "seed"},
		{"reasoning", func(p *Profile) { p.Reasoning = nil }, "reasoning"},
		{"timeout", func(p *Profile) { p.TimeoutMS = nil }, "timeout_ms"},
		{"frozen_at", func(p *Profile) { p.FrozenAt = nil }, "frozen_at"},
		{"routing order", func(p *Profile) { p.Routing.Order = nil }, "routing.order"},
		{"routing allow_fallbacks", func(p *Profile) { p.Routing.AllowFallbacks = nil }, "routing.allow_fallbacks"},
		{"routing data_collection", func(p *Profile) { p.Routing.DataCollection = nil }, "routing.data_collection"},
		{"routing quantizations", func(p *Profile) { p.Routing.Quantizations = nil }, "routing.quantizations"},
		{"routing require_parameters", func(p *Profile) { p.Routing.RequireParameters = nil }, "routing.require_parameters"},
		{"routing zdr", func(p *Profile) { p.Routing.ZDR = nil }, "routing.zdr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := load(t, syntheticJSON)
			tc.unset(&p)
			if err := p.Validate(); err != nil {
				t.Fatalf("Validate with an OPEN field: %v", err)
			}
			if p.Frozen() {
				t.Fatal("Frozen reported true with an OPEN field")
			}
			requireReason(t, p.FrozenReason(), ErrOpenField, tc.field)
		})
	}
}

// A provider pin never replaces the price ceilings: peak-period prices can be
// double the base prices, and only the ceilings (sent on every request) bound the
// charge in every pricing period.
func TestFrozenAlwaysRequiresBothPriceCeilings(t *testing.T) {
	for name, order := range map[string][]string{
		"provider order set":   {"example-provider"},
		"empty provider order": {},
	} {
		t.Run(name, func(t *testing.T) {
			p := load(t, syntheticJSON)
			p.Routing.Order = order
			p.MaxPriceMicroPerMTokIn, p.MaxPriceMicroPerMTokOut = nil, nil
			if err := p.Validate(); err != nil {
				t.Fatalf("Validate without ceilings: %v", err)
			}
			if p.Frozen() {
				t.Fatal("Frozen accepted a profile without price ceilings")
			}
			requireReason(t, p.FrozenReason(), ErrPriceCeilingRequired, "max_price")
			if _, err := p.RequestMembers(); !errors.Is(err, ErrNotFrozen) {
				t.Fatalf("request members rendered without ceilings: %v", err)
			}
			if _, err := p.WorstCaseMicroUSD(1, 1); !errors.Is(err, ErrNotFrozen) {
				t.Fatalf("worst case priced without ceilings: %v", err)
			}
		})
	}
}

func TestFrozenAcceptsPriceCeilingInPlaceOfProviderPin(t *testing.T) {
	doc := replaceOnce(t, syntheticJSON, `"order": ["example-provider"]`, `"order": []`)
	doc = replaceOnce(t, doc, `"max_price_micro_usd_per_1m_tokens_in": 1111`, `"max_price_micro_usd_per_1m_tokens_in": 5000`)
	doc = replaceOnce(t, doc, `"max_price_micro_usd_per_1m_tokens_out": 2222`, `"max_price_micro_usd_per_1m_tokens_out": 3000`)
	p := load(t, doc)
	if err := p.FrozenReason(); err != nil {
		t.Fatalf("empty order with both price ceilings: %v", err)
	}
	// An order that is still null is OPEN even when ceilings are set.
	open := p
	open.Routing.Order = nil
	requireReason(t, open.FrozenReason(), ErrOpenField, "routing.order")
}

func TestFrozenRequiresTemperatureZero(t *testing.T) {
	warm := replaceOnce(t, syntheticJSON, `"temperature_milli": 0`, `"temperature_milli": 1500`)
	p := load(t, warm)
	if err := p.Validate(); err != nil {
		t.Fatalf("draft temperature 1500 must still load: %v", err)
	}
	if p.Frozen() {
		t.Fatal("non-zero temperature reported Frozen")
	}
	requireReason(t, p.FrozenReason(), ErrTemperatureNotZero, "temperature_milli")
}

func TestFrozenFalseWhenProfileInvalid(t *testing.T) {
	p := load(t, syntheticJSON)
	p.Model = "deepseek-v4.1-flash"
	if p.Frozen() {
		t.Fatal("Frozen reported true for an invalid profile")
	}
	requireMessage(t, p.FrozenReason(), "model must be")
}

func TestWorstCaseMicroUSDIsIntegerCeiling(t *testing.T) {
	// Synthetic prices: 1111 input and 2222 output micro-USD per 1M tokens.
	p := load(t, syntheticJSON)
	for _, tc := range []struct{ in, out, want uint64 }{
		{0, 0, 0},
		{1, 0, 1},
		{900, 0, 1},
		{0, 1, 1},
		{1000, 0, 2},
		{1000, 100, 2},
	} {
		got, err := p.WorstCaseMicroUSD(tc.in, tc.out)
		if err != nil || got != tc.want {
			t.Fatalf("WorstCaseMicroUSD(%d, %d) = %d, %v; want %d", tc.in, tc.out, got, err, tc.want)
		}
	}
	p.MaxInputTokens = ptr[uint64](1_000_000)
	got, err := p.WorstCaseMicroUSD(1_000_000, 0)
	if err != nil || got != 1111 {
		t.Fatalf("WorstCaseMicroUSD(1000000, 0) = %d, %v; want exact 1111", got, err)
	}
}

func TestWorstCaseUsesMaxOfDeclaredPriceAndCeiling(t *testing.T) {
	// Declared 1111 and 2222. The input ceiling 5000 is higher, so it governs the
	// input side. The output ceiling 1000 is lower, so the declared 2222 governs.
	doc := replaceOnce(t, syntheticJSON, `"max_price_micro_usd_per_1m_tokens_in": 1111`, `"max_price_micro_usd_per_1m_tokens_in": 5000`)
	doc = replaceOnce(t, doc, `"max_price_micro_usd_per_1m_tokens_out": 2222`, `"max_price_micro_usd_per_1m_tokens_out": 1000`)
	p := load(t, doc)
	p.MaxInputTokens = ptr[uint64](1_000_000)
	p.MaxOutputTokens = ptr[uint64](1_000_000)
	got, err := p.WorstCaseMicroUSD(1_000_000, 0)
	if err != nil || got != 5000 {
		t.Fatalf("input side with higher ceiling = %d, %v; want 5000", got, err)
	}
	got, err = p.WorstCaseMicroUSD(0, 1_000_000)
	if err != nil || got != 2222 {
		t.Fatalf("output side with lower ceiling = %d, %v; want declared 2222", got, err)
	}
}

func TestWorstCaseMicroUSDRefusesOpenOverLimitAndOverflow(t *testing.T) {
	draft := load(t, draftJSON)
	if _, err := draft.WorstCaseMicroUSD(1, 1); !errors.Is(err, ErrNotFrozen) {
		t.Fatalf("WorstCaseMicroUSD on OPEN draft: %v, want ErrNotFrozen", err)
	}

	p := load(t, syntheticJSON)
	if _, err := p.WorstCaseMicroUSD(1001, 0); err == nil || !strings.Contains(err.Error(), "max_input_tokens") {
		t.Fatalf("input bound above max_input_tokens: %v", err)
	}
	if _, err := p.WorstCaseMicroUSD(0, 101); err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatalf("output bound above max_output_tokens: %v", err)
	}

	big := load(t, syntheticJSON)
	big.MaxInputTokens = ptr[uint64](math.MaxUint64)
	big.InputMicroUSDPer1M = ptr[uint64](math.MaxUint64)
	if _, err := big.WorstCaseMicroUSD(2, 0); !errors.Is(err, errOverflow) {
		t.Fatalf("multiplication overflow: %v, want errOverflow", err)
	}
	// The largest exact ceiling without overflow is ceil(MaxUint64 / 1e6).
	got, err := big.WorstCaseMicroUSD(1, 0)
	if err != nil || got != 18446744073710 {
		t.Fatalf("WorstCaseMicroUSD(1, 0) = %d, %v; want 18446744073710", got, err)
	}

	sum := load(t, syntheticJSON)
	sum.InputMicroUSDPer1M = ptr[uint64](math.MaxUint64 - 5)
	sum.MaxOutputTokens = ptr[uint64](1)
	sum.OutputMicroUSDPer1M = ptr[uint64](10)
	if _, err := sum.WorstCaseMicroUSD(1, 1); !errors.Is(err, errOverflow) {
		t.Fatalf("addition overflow: %v, want errOverflow", err)
	}
}

func TestWorstCaseUnitMicroUSDMultipliesByMaxRequests(t *testing.T) {
	// Per request: (1000 in, 100 out) is 1.3332 micro-USD, rounded up to 2.
	// MaxRequestsPerUnit is 3, so one unit reserves 6.
	p := load(t, syntheticJSON)
	for _, tc := range []struct{ in, out, want uint64 }{
		{0, 0, 0},
		{900, 0, 3},
		{1000, 100, 6},
	} {
		got, err := p.WorstCaseUnitMicroUSD(tc.in, tc.out)
		if err != nil || got != tc.want {
			t.Fatalf("WorstCaseUnitMicroUSD(%d, %d) = %d, %v; want %d", tc.in, tc.out, got, err, tc.want)
		}
	}
	draft := load(t, draftJSON)
	if _, err := draft.WorstCaseUnitMicroUSD(1, 1); !errors.Is(err, ErrNotFrozen) {
		t.Fatalf("WorstCaseUnitMicroUSD on OPEN draft: %v, want ErrNotFrozen", err)
	}
	p.MaxRequestsPerUnit = ptr[uint64](math.MaxUint64)
	if _, err := p.WorstCaseUnitMicroUSD(1000, 0); !errors.Is(err, errOverflow) {
		t.Fatalf("unit multiplication overflow: %v, want errOverflow", err)
	}
	if _, err := p.WorstCaseUnitMicroUSD(1001, 0); err == nil || !strings.Contains(err.Error(), "max_input_tokens") {
		t.Fatalf("unit with input bound above max_input_tokens: %v", err)
	}
}

func TestDigestIsSHA256OfCanonicalEncoding(t *testing.T) {
	sum := sha256.Sum256([]byte(syntheticCanonical))
	if got, want := mustDigest(t, load(t, syntheticJSON)), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("Digest = %s, want SHA-256 of the canonical encoding %s", got, want)
	}
}

func TestDigestIgnoresFormattingNotValues(t *testing.T) {
	base := mustDigest(t, load(t, syntheticJSON))
	if got := mustDigest(t, load(t, syntheticCanonical)); got != base {
		t.Fatalf("canonical input digest %s differs from formatted input digest %s", got, base)
	}
	changed := mustDigest(t, load(t, replaceOnce(t, syntheticJSON, `"seed": 7`, `"seed": 8`)))
	if changed == base {
		t.Fatal("changing seed did not change the digest")
	}
}

func TestDigestCoversPriceCeilingsAndRoutingFlags(t *testing.T) {
	base := mustDigest(t, load(t, syntheticJSON))
	ceilings := replaceOnce(t, syntheticJSON, `"max_price_micro_usd_per_1m_tokens_in": 1111`, `"max_price_micro_usd_per_1m_tokens_in": 5000`)
	ceilings = replaceOnce(t, ceilings, `"max_price_micro_usd_per_1m_tokens_out": 2222`, `"max_price_micro_usd_per_1m_tokens_out": 3000`)
	withCeiling := mustDigest(t, load(t, ceilings))
	if withCeiling == base {
		t.Fatal("changing the price ceiling pair did not change the digest")
	}
	withZDR := mustDigest(t, load(t, replaceOnce(t, syntheticJSON, `"zdr": true`, `"zdr": false`)))
	if withZDR == base {
		t.Fatal("changing zdr did not change the digest")
	}
}

func TestDigestCoversDraftsAndRejectsInvalid(t *testing.T) {
	d := mustDigest(t, load(t, draftJSON))
	if len(d) != 64 || strings.ToLower(d) != d {
		t.Fatalf("draft digest %q is not 64 lowercase hex characters", d)
	}
	p := load(t, syntheticJSON)
	p.Model = "other/model"
	if _, err := p.Digest(); err == nil || !strings.Contains(err.Error(), "model must be") {
		t.Fatalf("Digest of invalid profile: %v", err)
	}
}
