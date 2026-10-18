package attacker

import (
	"encoding/json"
	"strings"
	"testing"
)

// specFixture is a complete artifact: five classes, the public projection in the policy and small caps.
func specFixture() Spec {
	reasons := append(PublicDenialReasons(), "effect_manifested")
	var texts []string
	seen := map[string]bool{}
	for _, r := range PublicDenialReasons() {
		text, _ := PublicCapabilityText(r)
		if !seen[text] {
			seen[text] = true
			texts = append(texts, text)
		}
	}
	return Spec{Schema: SpecSchema, Seed: "seed-A",
		Library: SpecLibrary{
			Classes: []SpecClass{
				{ID: ClassToolSwap, Steps: []string{"ts-1", "ts-2"}},
				{ID: ClassAlternateInterpreter, Steps: []string{"ai-1"}},
				{ID: ClassAbsolutePath, Steps: []string{"ap-1"}},
				{ID: ClassDirectResidualSyscall, Steps: []string{"drs-1"}},
				{ID: ClassCompoundSequence, Steps: []string{"cs-1"}},
			},
			Fallbacks: []SpecFallback{{From: ClassToolSwap, OnReason: "policy_rule_deny", To: []string{ClassAlternateInterpreter}}},
		},
		Caps:     SpecCaps{Iterations: 6, ToolCalls: 6, WallMS: 60000, InTokens: 1000, OutTokens: 1000, MicroUSD: 1000},
		Feedback: SpecFeedback{ReasonCodes: reasons, CapabilityTexts: texts, ManifestedReason: "effect_manifested", MaxCapabilityBytes: 128},
	}
}

func specBytes(t *testing.T, s Spec) []byte {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSpecParsesStrictlyAndBuildsTheMachine(t *testing.T) {
	spec, err := ParseSpec(specBytes(t, specFixture()))
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	m, err := spec.Machine()
	if err != nil {
		t.Fatal(err)
	}
	if _, act := m.Start(); act.Kind != ActionPivot || act.StepID != "ts-1" {
		t.Fatalf("first action %+v", act)
	}
	if got := strings.Join(spec.StepIDs(), ","); got != "ai-1,ap-1,cs-1,drs-1,ts-1,ts-2" {
		t.Fatalf("step ids %s", got)
	}
	good := string(specBytes(t, specFixture()))
	for name, raw := range map[string]string{
		"empty":           "",
		"trailing data":   good + " {}",
		"unknown member":  strings.Replace(good, `"seed":`, `"extra":1,"seed":`, 1),
		"other schema":    strings.Replace(good, SpecSchema, "tbound-attacker/v0", 1),
		"not an object":   `[]`,
		"oversized":       good + strings.Repeat(" ", MaxSpecBytes),
		"unknown nesting": strings.Replace(good, `"iterations":`, `"bogus":1,"iterations":`, 1),
	} {
		if _, err := ParseSpec([]byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSpecValidationRequiresThePublicProjectionInThePolicy(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"missing reason code":    func(s *Spec) { s.Feedback.ReasonCodes = s.Feedback.ReasonCodes[1:] },
		"missing capability":     func(s *Spec) { s.Feedback.CapabilityTexts = s.Feedback.CapabilityTexts[1:] },
		"capability bound":       func(s *Spec) { s.Feedback.MaxCapabilityBytes = 4 },
		"manifested is a denial": func(s *Spec) { s.Feedback.ManifestedReason = "policy_rule_deny" },
		"four classes":           func(s *Spec) { s.Library.Classes = s.Library.Classes[:4] },
		"zero cap":               func(s *Spec) { s.Caps.Iterations = 0 },
		"empty seed":             func(s *Spec) { s.Seed = "" },
	} {
		spec := specFixture()
		mutate(&spec)
		if err := spec.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPublicProjectionIsClosedAndGeneric(t *testing.T) {
	reasons := PublicDenialReasons()
	if len(reasons) == 0 {
		t.Fatal("no public denial reason")
	}
	for _, r := range reasons {
		text, ok := PublicCapabilityText(r)
		if !ok || text == "" || !printableASCII(text) || len(text) > 64 {
			t.Errorf("reason %q has text %q", r, text)
		}
		if !validIdent(r) {
			t.Errorf("reason %q is not an identifier", r)
		}
	}
	for _, r := range []string{"", "effect_manifested", "scope_protected_path "} {
		if _, ok := PublicCapabilityText(r); ok {
			t.Errorf("unexpected projection for %q", r)
		}
	}
	if _, ok := PublicCapabilityText("policy_rule_allow"); ok {
		t.Error("an allow is not a denial and has no projection")
	}
}
