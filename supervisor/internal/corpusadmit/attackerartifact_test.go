package corpusadmit

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
)

func attackerSpecForTest() attacker.Spec {
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
			{ID: attacker.ClassToolSwap, Steps: []string{"ts-1", "ts-2"}}, {ID: attacker.ClassAlternateInterpreter, Steps: []string{"ai-1"}},
			{ID: attacker.ClassAbsolutePath, Steps: []string{"ap-1"}}, {ID: attacker.ClassDirectResidualSyscall, Steps: []string{"drs-1"}},
			{ID: attacker.ClassCompoundSequence, Steps: []string{"cs-1"}}}},
		Caps:     attacker.SpecCaps{Iterations: 4, ToolCalls: 4, WallMS: 60000, InTokens: 10, OutTokens: 10, MicroUSD: 10},
		Feedback: attacker.SpecFeedback{ReasonCodes: reasons, CapabilityTexts: texts, ManifestedReason: "effect_manifested", MaxCapabilityBytes: 128}}
}

// pivotedCases is the artifact fixture whose adversarial case also carries pivots, with a manifest that pins
// the attacker artifact held by the returned file system.
func pivotedCases(t *testing.T, mutate func(*attacker.Spec)) (evalcorpus.Manifest, fstest.MapFS) {
	t.Helper()
	cases, fsys := artifactCases(t)
	spec := attackerSpecForTest()
	if mutate != nil {
		mutate(&spec)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	digest := taskaccept.DigestOf(raw)
	name, ok := AttackerArtifactName(digest)
	if !ok {
		t.Fatalf("no artifact name for %s", digest)
	}
	fsys[name] = &fstest.MapFile{Data: raw}
	cases[0].Trial = &evalcorpus.Trial{MaxHostCalls: 6, DurationSeconds: 60, Pivots: map[string]evalcorpus.ScriptTurn{
		"ts-1": {Tool: "bash", Arguments: json.RawMessage(`{"command":"true"}`)},
		"ai-1": {Tool: "bash", Arguments: json.RawMessage(`{"command":"true"}`)}}}
	return evalcorpus.Manifest{Pins: evalcorpus.Pins{AttackerDigest: digest}, Cases: cases}, fsys
}

func TestAttackerArtifactName(t *testing.T) {
	hex := strings.Repeat("cd", 32)
	if got, ok := AttackerArtifactName("sha256:" + hex); !ok || got != "attacker-"+hex+".json" {
		t.Fatalf("AttackerArtifactName = %q, %v", got, ok)
	}
	for _, bad := range []string{"", hex, "sha256:" + hex[1:], "sha256:../" + hex[3:]} {
		if _, ok := AttackerArtifactName(bad); ok {
			t.Errorf("AttackerArtifactName(%q) accepted", bad)
		}
	}
}

func TestCheckManifestArtifactsAcceptsAMatchingAttacker(t *testing.T) {
	m, fsys := pivotedCases(t, nil)
	if err := CheckManifestArtifacts(m, fsys); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadAttacker(fsys, m.Pins.AttackerDigest)
	if err != nil || spec.Seed != "seed-A" {
		t.Fatalf("LoadAttacker = %+v, %v", spec, err)
	}
	// a manifest without pivots needs no attacker artifact, and CheckArtifacts itself is unchanged
	plain, plainFS := artifactCases(t)
	if err := CheckManifestArtifacts(evalcorpus.Manifest{Cases: plain}, plainFS); err != nil {
		t.Fatal(err)
	}
}

func TestCheckManifestArtifactsRefusesAnAttackerThatDoesNotFit(t *testing.T) {
	for name, tc := range map[string]struct {
		mutateSpec func(*attacker.Spec)
		mutate     func(*evalcorpus.Manifest, fstest.MapFS)
	}{
		"no pin": {mutate: func(m *evalcorpus.Manifest, _ fstest.MapFS) { m.Pins.AttackerDigest = "" }},
		"missing artifact": {mutate: func(m *evalcorpus.Manifest, f fstest.MapFS) {
			name, _ := AttackerArtifactName(m.Pins.AttackerDigest)
			delete(f, name)
		}},
		"artifact bytes differ from the pin": {mutate: func(m *evalcorpus.Manifest, f fstest.MapFS) {
			name, _ := AttackerArtifactName(m.Pins.AttackerDigest)
			f[name] = &fstest.MapFile{Data: append(append([]byte(nil), f[name].Data...), ' ')}
		}},
		"pivot is not a library step": {mutate: func(m *evalcorpus.Manifest, _ fstest.MapFS) {
			m.Cases[0].Trial.Pivots["zz-9"] = m.Cases[0].Trial.Pivots["ts-1"]
		}},
		"host calls below the replay": {mutate: func(m *evalcorpus.Manifest, _ fstest.MapFS) { m.Cases[0].Trial.MaxHostCalls = 4 }},
		// the artifact's wall cap (60000 ms) must be reachable within one attempt, or the service ceiling would
		// silently be the binding cap; and a cap of under a second leaves no whole-second run deadline
		"duration below the wall cap": {mutate: func(m *evalcorpus.Manifest, _ fstest.MapFS) { m.Cases[0].Trial.DurationSeconds = 59 }},
		"wall cap below one second":   {mutateSpec: func(s *attacker.Spec) { s.Caps.WallMS = 999 }},
		"policy lacks the projection": {mutateSpec: func(s *attacker.Spec) { s.Feedback.ReasonCodes = s.Feedback.ReasonCodes[1:] }},
		"invalid caps":                {mutateSpec: func(s *attacker.Spec) { s.Caps.Iterations = 0 }},
	} {
		m, fsys := pivotedCases(t, tc.mutateSpec)
		if tc.mutate != nil {
			tc.mutate(&m, fsys)
		}
		if err := CheckManifestArtifacts(m, fsys); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
