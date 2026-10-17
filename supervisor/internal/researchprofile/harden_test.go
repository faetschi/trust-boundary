package researchprofile

import (
	"errors"
	"strings"
	"testing"
)

const datedSlug = "deepseek/deepseek-v4.1-flash-20260910"

func TestReasoningIsRequiredAndFrozen(t *testing.T) {
	// A missing key is a malformed profile; a null value is OPEN and not frozen.
	_, err := Load(strings.NewReader(replaceOnce(t, syntheticJSON, "  \"reasoning\": {\"enabled\": false},\n", "")))
	requireMessage(t, err, `missing research profile field "reasoning"`)
	open := load(t, replaceOnce(t, syntheticJSON, `"reasoning": {"enabled": false}`, `"reasoning": null`))
	if open.Frozen() {
		t.Fatal("a profile with reasoning OPEN is frozen")
	}
	requireReason(t, open.FrozenReason(), ErrOpenField, "reasoning")
	if _, err := open.RequestMembers(); !errors.Is(err, ErrNotFrozen) {
		t.Fatalf("members rendered with reasoning OPEN: %v", err)
	}
}

func TestReasoningValueIsStrict(t *testing.T) {
	for name, value := range map[string]string{
		"empty object":      `{}`,
		"both members":      `{"enabled": false, "effort": "low"}`,
		"unknown effort":    `{"effort": "extreme"}`,
		"unknown member":    `{"enabled": false, "max_tokens": 10}`,
		"duplicate member":  `{"enabled": false, "enabled": true}`,
		"null enabled":      `{"enabled": null}`,
		"string enabled":    `{"enabled": "false"}`,
		"not an object":     `false`,
		"empty effort text": `{"effort": ""}`,
	} {
		doc := replaceOnce(t, syntheticJSON, `"reasoning": {"enabled": false}`, `"reasoning": `+value)
		if _, err := Load(strings.NewReader(doc)); err == nil {
			t.Errorf("Load accepted reasoning %s", name)
		}
	}
	for _, value := range []string{`{"enabled": true}`, `{"enabled": false}`, `{"effort": "none"}`, `{"effort": "low"}`, `{"effort": "xhigh"}`} {
		doc := replaceOnce(t, syntheticJSON, `"reasoning": {"enabled": false}`, `"reasoning": `+value)
		if p := load(t, doc); !p.Frozen() {
			t.Errorf("reasoning %s is not frozen: %v", value, p.FrozenReason())
		}
	}
}

func TestReasoningIsRenderedAndDigestBound(t *testing.T) {
	base := load(t, syntheticJSON)
	effort := load(t, replaceOnce(t, syntheticJSON, `"reasoning": {"enabled": false}`, `"reasoning": {"effort": "low"}`))
	members, err := effort.RequestMembers()
	if err != nil || !strings.HasSuffix(members, `,"reasoning":{"effort":"low"}}`) {
		t.Fatalf("effort members %q (%v)", members, err)
	}
	enabled := load(t, replaceOnce(t, syntheticJSON, `"reasoning": {"enabled": false}`, `"reasoning": {"enabled": true}`))
	digests := map[string]bool{mustDigest(t, base): true, mustDigest(t, effort): true, mustDigest(t, enabled): true}
	if len(digests) != 3 {
		t.Fatal("reasoning value does not change the profile digest")
	}
	if strings.Contains(members, `"usage"`) {
		t.Fatalf("deprecated usage member is still sent: %s", members)
	}
}

func TestAllowedResponseModelsDefaultsToTheRequestedSlug(t *testing.T) {
	// syntheticJSON omits the member.
	p := load(t, syntheticJSON)
	if got := p.ResponseModels(); len(got) != 1 || got[0] != Model {
		t.Fatalf("default response models = %v", got)
	}
	explicit := load(t, replaceOnce(t, syntheticJSON, `  "model": "deepseek/deepseek-v4.1-flash",`,
		`  "model": "deepseek/deepseek-v4.1-flash", "allowed_response_models": ["deepseek/deepseek-v4.1-flash"],`))
	if mustDigest(t, explicit) != mustDigest(t, p) {
		t.Fatal("the explicit default list digests differently from the omitted member")
	}
	var literal Profile = p
	literal.AllowedResponseModels = nil
	if mustDigest(t, literal) != mustDigest(t, p) {
		t.Fatal("a nil list digests differently from the default")
	}
}

func TestAllowedResponseModelsAreDigestBoundAndHoldTheCanonicalSlug(t *testing.T) {
	doc := replaceOnce(t, syntheticJSON, `  "model": "deepseek/deepseek-v4.1-flash",`,
		`  "model": "deepseek/deepseek-v4.1-flash", "allowed_response_models": ["deepseek/deepseek-v4.1-flash", "`+datedSlug+`"],`)
	p := load(t, doc)
	if !p.Frozen() || len(p.ResponseModels()) != 2 || p.ResponseModels()[1] != datedSlug {
		t.Fatalf("pinned list: frozen=%v list=%v", p.Frozen(), p.ResponseModels())
	}
	if mustDigest(t, p) == mustDigest(t, load(t, syntheticJSON)) {
		t.Fatal("the allowed response-model list does not change the digest")
	}
	reordered := load(t, replaceOnce(t, doc, `["deepseek/deepseek-v4.1-flash", "`+datedSlug+`"]`, `["`+datedSlug+`", "deepseek/deepseek-v4.1-flash"]`))
	if mustDigest(t, reordered) == mustDigest(t, p) {
		t.Fatal("the list order is not part of the digest")
	}
}

func TestAllowedResponseModelsAreStrictAndPartOfFrozen(t *testing.T) {
	member := func(value string) string {
		return replaceOnce(t, syntheticJSON, `  "model": "deepseek/deepseek-v4.1-flash",`,
			`  "model": "deepseek/deepseek-v4.1-flash", "allowed_response_models": `+value+`,`)
	}
	for name, value := range map[string]string{
		"null":          `null`,
		"empty list":    `[]`,
		"empty entry":   `[""]`,
		"duplicate":     `["a/b", "a/b"]`,
		"padded":        `[" a/b"]`,
		"control":       `["a/\u0001b"]`,
		"not a list":    `"a/b"`,
		"too many":      `["a/1","a/2","a/3","a/4","a/5","a/6","a/7","a/8","a/9"]`,
		"non-string":    `[1]`,
		"wildcard text": `["deepseek/*"]`,
	} {
		if _, err := Load(strings.NewReader(member(value))); err == nil {
			t.Errorf("Load accepted allowed_response_models %s", name)
		}
	}
	p := load(t, syntheticJSON)
	p.AllowedResponseModels = []string{}
	if p.Frozen() {
		t.Fatal("a profile with an empty response-model list is frozen")
	}
}

func TestWorstCaseUnitUsesThePeakCeilingNotTheBasePrice(t *testing.T) {
	// Declared (base) 1111/2222; a peak ceiling of 5000/9000 must govern both sides.
	doc := replaceOnce(t, syntheticJSON, `"max_price_micro_usd_per_1m_tokens_in": 1111`, `"max_price_micro_usd_per_1m_tokens_in": 5000`)
	doc = replaceOnce(t, doc, `"max_price_micro_usd_per_1m_tokens_out": 2222`, `"max_price_micro_usd_per_1m_tokens_out": 9000`)
	p := load(t, doc)
	p.MaxInputTokens = ptr[uint64](1_000_000)
	p.MaxOutputTokens = ptr[uint64](1_000_000)
	got, err := p.WorstCaseUnitMicroUSD(1_000_000, 1_000_000)
	if want := uint64(5000+9000) * 3; err != nil || got != want {
		t.Fatalf("unit worst case = %d, %v; want %d", got, err, want)
	}
}
