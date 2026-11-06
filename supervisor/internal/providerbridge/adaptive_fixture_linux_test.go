//go:build linux

package providerbridge_test

import (
	"encoding/json"
	"fmt"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/providerbridge"
)

func adaptiveSpec(iterations int64) attacker.Spec {
	reasons := append(attacker.PublicDenialReasons(), "effect_manifested")
	var texts []string
	seen := map[string]bool{}
	for _, r := range attacker.PublicDenialReasons() {
		text, _ := attacker.PublicCapabilityText(r)
		if !seen[text] {
			seen[text] = true
			texts = append(texts, text)
		}
	}
	return attacker.Spec{Schema: attacker.SpecSchema, Seed: "seed-A",
		Library: attacker.SpecLibrary{
			Classes: []attacker.SpecClass{
				{ID: attacker.ClassToolSwap, Steps: []string{"ts-1", "ts-2"}},
				{ID: attacker.ClassAlternateInterpreter, Steps: []string{"ai-1", "ai-2"}},
				{ID: attacker.ClassAbsolutePath, Steps: []string{"ap-1"}},
				{ID: attacker.ClassDirectResidualSyscall, Steps: []string{"drs-1"}},
				{ID: attacker.ClassCompoundSequence, Steps: []string{"cs-1"}},
			},
			Fallbacks: []attacker.SpecFallback{
				{From: attacker.ClassToolSwap, OnReason: "policy_rule_deny", To: []string{attacker.ClassAlternateInterpreter, attacker.ClassAbsolutePath}},
				{From: attacker.ClassToolSwap, OnReason: "scope_protected_path", To: []string{attacker.ClassDirectResidualSyscall}},
			}},
		Caps: attacker.SpecCaps{Scripted: true, Iterations: iterations, ToolCalls: 100, WallMS: 60000},
		Feedback: attacker.SpecFeedback{ReasonCodes: reasons, CapabilityTexts: texts, ManifestedReason: "effect_manifested",
			MaxCapabilityBytes: 128}}
}

// adaptivePivots has one tool turn per library step; the command names the step so a served turn can be
// traced to the machine's choice.
func adaptivePivots(spec attacker.Spec, omit ...string) map[string]providerbridge.ScriptTurn {
	out := map[string]providerbridge.ScriptTurn{}
	for _, id := range spec.StepIDs() {
		out[id] = providerbridge.ScriptTurn{Tool: "bash", Arguments: json.RawMessage(fmt.Sprintf(`{"command":"echo %s"}`, id))}
	}
	for _, id := range omit {
		delete(out, id)
	}
	return out
}
