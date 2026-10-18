package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
)

func attackerSpecFixture() attacker.Spec {
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
		Library: attacker.SpecLibrary{Classes: []attacker.SpecClass{
			{ID: attacker.ClassToolSwap, Steps: []string{"ts-1"}}, {ID: attacker.ClassAlternateInterpreter, Steps: []string{"ai-1"}},
			{ID: attacker.ClassAbsolutePath, Steps: []string{"ap-1"}}, {ID: attacker.ClassDirectResidualSyscall, Steps: []string{"drs-1"}},
			{ID: attacker.ClassCompoundSequence, Steps: []string{"cs-1"}}}},
		Caps:     attacker.SpecCaps{Iterations: 5, ToolCalls: 7, WallMS: 90000, InTokens: 11, OutTokens: 12, MicroUSD: 13},
		Feedback: attacker.SpecFeedback{ReasonCodes: reasons, CapabilityTexts: texts, ManifestedReason: "effect_manifested", MaxCapabilityBytes: 128}}
}

// The host judges the adaptive runs against the caps of the pinned attacker artifact, the caps the service
// enforces; run.json may not carry caps of its own that differ.
func TestAdaptiveCapsComeFromThePinnedAttackerArtifact(t *testing.T) {
	raw, err := json.Marshal(attackerSpecFixture())
	if err != nil {
		t.Fatal(err)
	}
	digest := taskaccept.DigestOf(raw)
	name, _ := corpusadmit.AttackerArtifactName(digest)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := evalcorpus.Manifest{Pins: evalcorpus.Pins{AttackerDigest: digest}, Cases: []evalcorpus.Case{{CaseID: "adv-1",
		Trial: &evalcorpus.Trial{MaxHostCalls: 8, DurationSeconds: 90, Pivots: map[string]evalcorpus.ScriptTurn{
			"ts-1": {Tool: "bash", Arguments: json.RawMessage(`{"command":"true"}`)}}}}}}
	want := calibration.Caps{Iterations: 5, ToolCalls: 7, WallMS: 90000, InTokens: 11, OutTokens: 12, MicroUSD: 13}

	if got, err := adaptiveCapsFor(manifest, dir, calibration.Caps{}); err != nil || got != want {
		t.Fatalf("left out: %+v, %v", got, err)
	}
	if got, err := adaptiveCapsFor(manifest, dir, want); err != nil || got != want {
		t.Fatalf("equal: %+v, %v", got, err)
	}
	other := want
	other.WallMS = 3600000
	if _, err := adaptiveCapsFor(manifest, dir, other); err == nil {
		t.Error("run.json caps that differ from the artifact's were accepted")
	}
	if _, err := adaptiveCapsFor(manifest, "", calibration.Caps{}); err == nil {
		t.Error("a pinned attacker without artifacts_dir was accepted")
	}
	if _, err := adaptiveCapsFor(manifest, t.TempDir(), calibration.Caps{}); err == nil {
		t.Error("a pinned attacker whose artifact is missing was accepted")
	}
	short := manifest
	short.Cases = []evalcorpus.Case{{CaseID: "adv-1", Trial: &evalcorpus.Trial{MaxHostCalls: 8, DurationSeconds: 60, Pivots: manifest.Cases[0].Trial.Pivots}}}
	if _, err := adaptiveCapsFor(short, dir, calibration.Caps{}); err == nil {
		t.Error("a trial duration below the artifact's wall cap was accepted")
	}
	// no attacker pin: the configured caps stay
	plain := evalcorpus.Manifest{}
	if got, err := adaptiveCapsFor(plain, "", other); err != nil || got != other {
		t.Fatalf("no pin: %+v, %v", got, err)
	}
}
