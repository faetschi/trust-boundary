package attacker

import "testing"

// A command that the gate allowed and that failed is shown to the attacker as the public reason
// command_failed with a generic text; the artifact's policy must cover it like every denial reason.
func TestPublicProjectionCoversAFailedCommand(t *testing.T) {
	text, ok := PublicCapabilityText(ReasonCommandFailed)
	if !ok || text == "" {
		t.Fatalf("command_failed has no public capability text")
	}
	if ReasonCommandFailed != "command_failed" {
		t.Fatalf("the public reason code is %q", ReasonCommandFailed)
	}
	spec := validSpecForStatusTest(t)
	if err := spec.Validate(); err != nil {
		t.Fatalf("a policy that covers the projection must validate: %v", err)
	}
	var without []string
	for _, r := range spec.Feedback.ReasonCodes {
		if r != ReasonCommandFailed {
			without = append(without, r)
		}
	}
	spec.Feedback.ReasonCodes = without
	if err := spec.Validate(); err == nil {
		t.Fatal("an artifact whose policy lacks command_failed must be refused")
	}
}

func validSpecForStatusTest(t *testing.T) Spec {
	t.Helper()
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
	return Spec{Schema: SpecSchema, Seed: "seed", Library: SpecLibrary{Classes: []SpecClass{
		{ID: ClassToolSwap, Steps: []string{"a"}}, {ID: ClassAlternateInterpreter, Steps: []string{"b"}},
		{ID: ClassAbsolutePath, Steps: []string{"c"}}, {ID: ClassDirectResidualSyscall, Steps: []string{"d"}},
		{ID: ClassCompoundSequence, Steps: []string{"e"}}}},
		Caps:     SpecCaps{Iterations: 5, ToolCalls: 5, WallMS: 1000, InTokens: 1, OutTokens: 1, MicroUSD: 1},
		Feedback: SpecFeedback{ReasonCodes: reasons, CapabilityTexts: texts, ManifestedReason: "effect_manifested", MaxCapabilityBytes: 128}}
}
