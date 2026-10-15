package researchprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"testing"
)

// draftJSON is an all-OPEN draft. Only the fixed endpoint, the fixed model and
// allow_fallbacks=false are set; every other decision is null until frozen.
const draftJSON = `{
  "schema_version": 1,
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "deepseek/deepseek-v4.1-flash",
  "routing": {
    "order": null,
    "allow_fallbacks": false,
    "data_collection": null,
    "quantizations": null
  },
  "input_micro_usd_per_1m_tokens": null,
  "output_micro_usd_per_1m_tokens": null,
  "max_input_tokens": null,
  "max_output_tokens": null,
  "max_requests_per_unit": null,
  "temperature_milli": null,
  "seed": null,
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
    "quantizations": []
  },
  "input_micro_usd_per_1m_tokens": 1111,
  "output_micro_usd_per_1m_tokens": 2222,
  "max_input_tokens": 1000,
  "max_output_tokens": 100,
  "max_requests_per_unit": 3,
  "temperature_milli": 0,
  "seed": 7,
  "timeout_ms": 1234,
  "frozen_at": "2026-10-15T00:00:00Z"
}`

// syntheticCanonical is the canonical encoding of syntheticJSON: fixed field
// order, no insignificant whitespace, explicit nulls. Digest must be its SHA-256.
const syntheticCanonical = `{"schema_version":1,"endpoint":"https://openrouter.ai/api/v1/chat/completions","model":"deepseek/deepseek-v4.1-flash","routing":{"order":["example-provider"],"allow_fallbacks":false,"data_collection":"deny","quantizations":[]},"input_micro_usd_per_1m_tokens":1111,"output_micro_usd_per_1m_tokens":2222,"max_input_tokens":1000,"max_output_tokens":100,"max_requests_per_unit":3,"temperature_milli":0,"seed":7,"timeout_ms":1234,"frozen_at":"2026-10-15T00:00:00Z"}`

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

func ptr[T any](v T) *T { return &v }

func TestLoadDraftIsValidButOpen(t *testing.T) {
	p := load(t, draftJSON)
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate(draft): %v", err)
	}
	if p.Frozen() {
		t.Fatal("draft with OPEN prices, limits, settings and routing reported Frozen")
	}
}

func TestLoadSyntheticProfileIsFrozen(t *testing.T) {
	p := load(t, syntheticJSON)
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate(synthetic): %v", err)
	}
	if !p.Frozen() {
		t.Fatal("fully decided synthetic profile did not report Frozen")
	}
}

func TestLoadRejectsMalformedDocuments(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"unknown top-level field", replaceOnce(t, draftJSON, `"frozen_at": null`, `"frozen_at": null, "extra": 1`)},
		{"unknown routing field", replaceOnce(t, draftJSON, `"quantizations": null`, `"quantizations": null, "extra": true`)},
		{"missing top-level field", replaceOnce(t, draftJSON, `"seed": null,`, ``)},
		{"missing routing field", replaceOnce(t, draftJSON, "\"data_collection\": null,\n    \"quantizations\": null", `"data_collection": null`)},
		{"duplicate field", replaceOnce(t, draftJSON, `"seed": null,`, `"seed": null, "seed": 7,`)},
		{"trailing value", draftJSON + ` {}`},
		{"array instead of object", `[]`},
		{"null document", `null`},
		{"float money", replaceOnce(t, draftJSON, `"input_micro_usd_per_1m_tokens": null`, `"input_micro_usd_per_1m_tokens": 1.5`)},
		{"exponent money", replaceOnce(t, draftJSON, `"output_micro_usd_per_1m_tokens": null`, `"output_micro_usd_per_1m_tokens": 1e3`)},
		{"negative money", replaceOnce(t, draftJSON, `"input_micro_usd_per_1m_tokens": null`, `"input_micro_usd_per_1m_tokens": -1`)},
		{"zero price", replaceOnce(t, draftJSON, `"output_micro_usd_per_1m_tokens": null`, `"output_micro_usd_per_1m_tokens": 0`)},
		{"zero seed", replaceOnce(t, draftJSON, `"seed": null`, `"seed": 0`)},
		{"temperature above 2", replaceOnce(t, draftJSON, `"temperature_milli": null`, `"temperature_milli": 2001`)},
		{"date without time", replaceOnce(t, draftJSON, `"frozen_at": null`, `"frozen_at": "2026-10-15"`)},
		{"unknown data_collection", replaceOnce(t, draftJSON, `"data_collection": null`, `"data_collection": "maybe"`)},
		{"null endpoint", replaceOnce(t, draftJSON, `"endpoint": "https://openrouter.ai/api/v1/chat/completions"`, `"endpoint": null`)},
		{"oversized document", strings.Repeat(" ", maxProfileBytes) + draftJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(strings.NewReader(tc.doc)); err == nil {
				t.Fatal("Load accepted a document that must be rejected")
			}
		})
	}
}

func TestLoadRejectsUnsafeEndpointOrModel(t *testing.T) {
	const endpoint = `"https://openrouter.ai/api/v1/chat/completions"`
	for _, bad := range []string{
		`"http://openrouter.ai/api/v1/chat/completions"`,
		`"HTTP://openrouter.ai/api/v1/chat/completions"`,
		`"https://example.invalid/api/v1/chat/completions"`,
		`"https://openrouter.ai.example/api/v1/chat/completions"`,
		`"https://openrouter.ai:8443/api/v1/chat/completions"`,
		`"https://user@openrouter.ai/api/v1/chat/completions"`,
		`"https://openrouter.ai/api/v1/responses"`,
		`"https://openrouter.ai/api/v1/chat/completions?x=1"`,
		`"https://openrouter.ai/api/v1/chat/completions#frag"`,
		`"https://openrouter.ai/api/v1/chat/completions/"`,
	} {
		doc := replaceOnce(t, draftJSON, endpoint, bad)
		if _, err := Load(strings.NewReader(doc)); err == nil {
			t.Fatalf("Load accepted endpoint %s", bad)
		}
	}
	for _, bad := range []string{`"deepseek-v4.1-flash"`, `"deepseek/deepseek-v4.1-flash "`, `"DeepSeek/deepseek-v4.1-flash"`} {
		doc := replaceOnce(t, draftJSON, `"deepseek/deepseek-v4.1-flash"`, bad)
		if _, err := Load(strings.NewReader(doc)); err == nil {
			t.Fatalf("Load accepted model %s", bad)
		}
	}
}

func TestFrozenRequiresEveryOpenField(t *testing.T) {
	cases := []struct {
		name  string
		unset func(*Profile)
	}{
		{"input price", func(p *Profile) { p.InputMicroUSDPer1M = nil }},
		{"output price", func(p *Profile) { p.OutputMicroUSDPer1M = nil }},
		{"max input tokens", func(p *Profile) { p.MaxInputTokens = nil }},
		{"max output tokens", func(p *Profile) { p.MaxOutputTokens = nil }},
		{"max requests per unit", func(p *Profile) { p.MaxRequestsPerUnit = nil }},
		{"temperature", func(p *Profile) { p.TemperatureMilli = nil }},
		{"seed", func(p *Profile) { p.Seed = nil }},
		{"timeout", func(p *Profile) { p.TimeoutMS = nil }},
		{"frozen_at", func(p *Profile) { p.FrozenAt = nil }},
		{"routing order", func(p *Profile) { p.Routing.Order = nil }},
		{"routing allow_fallbacks", func(p *Profile) { p.Routing.AllowFallbacks = nil }},
		{"routing data_collection", func(p *Profile) { p.Routing.DataCollection = nil }},
		{"routing quantizations", func(p *Profile) { p.Routing.Quantizations = nil }},
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
		})
	}
}

func TestFrozenFalseWhenProfileInvalid(t *testing.T) {
	p := load(t, syntheticJSON)
	p.Model = "deepseek-v4.1-flash"
	if p.Frozen() {
		t.Fatal("Frozen reported true for an invalid profile")
	}
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

func TestWorstCaseMicroUSDRefusesOpenOverLimitAndOverflow(t *testing.T) {
	draft := load(t, draftJSON)
	if _, err := draft.WorstCaseMicroUSD(1, 1); err == nil {
		t.Fatal("WorstCaseMicroUSD computed a reservation for an OPEN draft")
	}

	p := load(t, syntheticJSON)
	if _, err := p.WorstCaseMicroUSD(1001, 0); err == nil {
		t.Fatal("input bound above max_input_tokens was accepted")
	}
	if _, err := p.WorstCaseMicroUSD(0, 101); err == nil {
		t.Fatal("output bound above max_output_tokens was accepted")
	}

	big := load(t, syntheticJSON)
	big.MaxInputTokens = ptr[uint64](math.MaxUint64)
	big.InputMicroUSDPer1M = ptr[uint64](math.MaxUint64)
	if _, err := big.WorstCaseMicroUSD(2, 0); err == nil {
		t.Fatal("multiplication overflow was not reported")
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
	if _, err := sum.WorstCaseMicroUSD(1, 1); err == nil {
		t.Fatal("addition overflow was not reported")
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

func TestDigestCoversDraftsAndRejectsInvalid(t *testing.T) {
	d := mustDigest(t, load(t, draftJSON))
	if len(d) != 64 || strings.ToLower(d) != d {
		t.Fatalf("draft digest %q is not 64 lowercase hex characters", d)
	}
	p := load(t, syntheticJSON)
	p.Model = "other/model"
	if _, err := p.Digest(); err == nil {
		t.Fatal("Digest accepted an invalid profile")
	}
}
