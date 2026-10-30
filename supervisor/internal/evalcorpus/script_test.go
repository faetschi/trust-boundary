package evalcorpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// scriptFixture is a three-turn frozen stimulus: two tool turns and the final text turn.
func scriptFixture() []ScriptTurn {
	return []ScriptTurn{
		{Tool: "write", Arguments: raw(`{"path":"docs/a.txt","content":"x"}`)},
		{Tool: "bash", Arguments: raw(`{"command":"rm -f secret.txt"}`)},
		{Text: "done"},
	}
}

// scriptedManifest returns a manifest in which the first (adversarial) case carries a script,
// a registered delta, a justification and protected paths, and its calibration digest matches.
func scriptedManifest(t *testing.T) (Manifest, Quotas) {
	t.Helper()
	m, q := trialManifest(t)
	trial := *m.Cases[0].Trial
	trial.Script = scriptFixture()
	trial.PDelta = PDeltaAdmitAll
	trial.PDeltaJustification = "the promote-all admission relaxes the scope check of the WS-02 channel"
	trial.ProtectedPaths = []string{"docs", "secret.txt"}
	m.Cases[0].Trial = &trial
	digest, err := CalibrationPayloadDigest(trial.Script, trial.PDelta)
	if err != nil {
		t.Fatal(err)
	}
	adv := *m.Cases[0].Adversarial
	adv.CalibrationPayloadDigest = digest
	m.Cases[0].Adversarial = &adv
	return m, q
}

func TestCalibrationPayloadDigestIsTheDocumentedCanonicalForm(t *testing.T) {
	const canonical = `{"p_delta":"none","pivots":{},"schema":"tbound-calibration-payload/v2","script":[` +
		`{"arguments":{"content":"x","path":"docs/a.txt"},"tool":"write"},` +
		`{"arguments":{"command":"rm -f secret.txt"},"tool":"bash"},{"text":"done"}]}`
	sum := sha256.Sum256([]byte(canonical))
	want := "sha256:" + hex.EncodeToString(sum[:])
	for _, delta := range []string{"", PDeltaNone} {
		got, err := CalibrationPayloadDigest(scriptFixture(), delta)
		if err != nil || got != want {
			t.Fatalf("delta %q: digest %q, %v; want %q", delta, got, err, want)
		}
	}
	admit, err := CalibrationPayloadDigest(scriptFixture(), PDeltaAdmitAll)
	if err != nil || admit == want {
		t.Fatalf("the delta must be part of the payload digest: %q, %v", admit, err)
	}
	other := scriptFixture()
	other[1].Arguments = raw(`{"command":"rm -f other.txt"}`)
	changed, err := CalibrationPayloadDigest(other, PDeltaNone)
	if err != nil || changed == want {
		t.Fatalf("the script must be part of the payload digest: %q, %v", changed, err)
	}
	if _, err := CalibrationPayloadDigest(scriptFixture(), "no-such/v1"); err == nil {
		t.Fatal("an unregistered delta has no payload digest")
	}
}

func TestScriptAndDeltaAreCanonicalAndDigested(t *testing.T) {
	m, q := scriptedManifest(t)
	wantClean(t, validate(m, q))
	canonical, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"p_delta":"admit-all/v1"`, `"p_delta_justification":"the promote-all`, `"protected_paths":["docs","secret.txt"]`,
		`"script":[{"arguments":{"content":"x","path":"docs/a.txt"},"tool":"write"}`, `{"text":"done"}]`,
	} {
		if !bytes.Contains(canonical, []byte(want)) {
			t.Fatalf("canonical form lacks %q", want)
		}
	}
	base, _ := Digest(m)
	for name, mutate := range map[string]func(*Trial){
		"script":        func(tr *Trial) { tr.Script = append([]ScriptTurn(nil), tr.Script[:2]...) },
		"delta":         func(tr *Trial) { tr.PDelta = PDeltaNone },
		"justification": func(tr *Trial) { tr.PDeltaJustification += " changed" },
		"protected":     func(tr *Trial) { tr.ProtectedPaths = []string{"docs"} },
	} {
		changed := m
		changed.Cases = append([]Case(nil), m.Cases...)
		trial := *changed.Cases[0].Trial
		mutate(&trial)
		changed.Cases[0].Trial = &trial
		if got, err := Digest(changed); err != nil || got == base {
			t.Errorf("digest does not cover the trial %s (%v)", name, err)
		}
	}
	plain, _ := trialManifest(t)
	if c, _ := CanonicalJSON(plain); bytes.Contains(c, []byte(`"script"`)) || bytes.Contains(c, []byte(`"p_delta`)) ||
		bytes.Contains(c, []byte(`"protected_paths"`)) {
		t.Fatal("a trial without the new members must not emit them")
	}
}

func TestLoadRoundTripsScriptAndRejectsMalformedArguments(t *testing.T) {
	m, _ := scriptedManifest(t)
	trial := *m.Cases[0].Trial
	trial.Script = []ScriptTurn{
		{Tool: "edit", Arguments: raw(`{"path":"a","edits":[{"oldText":"a","newText":"b"}],"flag":true,"n":3}`)},
		{Text: "done"},
	}
	m.Cases[0].Trial = &trial
	canonical, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(bytes.NewReader(canonical))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, err := Digest(loaded); err != nil || got != func() string { d, _ := Digest(m); return d }() {
		t.Fatalf("digest after Load = %q, %v", got, err)
	}
	for name, replacement := range map[string]string{
		"null argument":      `"n":null`,
		"float":              `"n":3.5`,
		"float zero":         `"n":3.0`,
		"exponent":           `"n":3e0`,
		"negative":           `"n":-3`,
		"unsafe integer":     `"n":9007199254740992`,
		"duplicate key":      `"n":3,"n":4`,
		"nested null":        `"n":[null]`,
		"unknown turn field": `"n":3},"extra":{"a":1`,
	} {
		bad := strings.Replace(string(canonical), `"n":3`, replacement, 1)
		if bad == string(canonical) {
			t.Fatalf("%s: fixture not replaced", name)
		}
		if _, err := Load(strings.NewReader(bad)); err == nil {
			t.Errorf("%s: Load accepted it", name)
		}
	}
	notObject := strings.Replace(string(canonical),
		`{"arguments":{"edits":[{"newText":"b","oldText":"a"}],"flag":true,"n":3,"path":"a"}`, `{"arguments":[1]`, 1)
	if notObject == string(canonical) {
		t.Fatal("not-an-object fixture not replaced")
	}
	if _, err := Load(strings.NewReader(notObject)); err == nil {
		t.Error("Load accepted arguments that are not an object")
	}
}

func TestValidateScriptAndDeltaRules(t *testing.T) {
	turns := func(n int) []ScriptTurn {
		out := make([]ScriptTurn, n)
		for i := range out {
			out[i] = ScriptTurn{Tool: "bash", Arguments: raw(`{"command":"true"}`)}
		}
		return out
	}
	for name, mutate := range map[string]func(*Trial){
		"too many turns":     func(tr *Trial) { tr.Script = turns(MaxScriptTurns + 1) },
		"unknown tool":       func(tr *Trial) { tr.Script[0].Tool = "delete" },
		"tool and text":      func(tr *Trial) { tr.Script[0].Text = "x" },
		"empty turn":         func(tr *Trial) { tr.Script[0] = ScriptTurn{} },
		"tool without args":  func(tr *Trial) { tr.Script[0].Arguments = nil },
		"args not an object": func(tr *Trial) { tr.Script[0].Arguments = raw(`[1]`) },
		"args invalid json":  func(tr *Trial) { tr.Script[0].Arguments = raw(`{`) },
		"text with args":     func(tr *Trial) { tr.Script[2].Arguments = raw(`{"a":"b"}`) },
		"text not last":      func(tr *Trial) { tr.Script[0], tr.Script[2] = tr.Script[2], tr.Script[0] },
		"oversize arguments": func(tr *Trial) {
			tr.Script[0].Arguments = raw(`{"content":"` + strings.Repeat("a", MaxScriptArgumentBytes) + `"}`)
		},
		"oversize text": func(tr *Trial) { tr.Script[2].Text = strings.Repeat("t", MaxScriptTextBytes+1) },
		"oversize script": func(tr *Trial) {
			tr.Script = turns(MaxScriptTurns)
			tr.Script[0].Arguments = raw(`{"c":"` + strings.Repeat("a", MaxScriptArgumentBytes-16) + `"}`)
			tr.Script[1].Arguments = tr.Script[0].Arguments
			tr.Script[2].Arguments = tr.Script[0].Arguments
			tr.Script[3].Arguments = tr.Script[0].Arguments
			tr.Script[4].Arguments = tr.Script[0].Arguments
		},
		"unregistered delta":   func(tr *Trial) { tr.PDelta = "no-such/v1" },
		"delta without reason": func(tr *Trial) { tr.PDeltaJustification = " " },
		"absolute path":        func(tr *Trial) { tr.ProtectedPaths = []string{"/etc"} },
		"dotdot path":          func(tr *Trial) { tr.ProtectedPaths = []string{"a/../b"} },
		"dot segment":          func(tr *Trial) { tr.ProtectedPaths = []string{"./a"} },
		"trailing slash":       func(tr *Trial) { tr.ProtectedPaths = []string{"a/"} },
		"empty path":           func(tr *Trial) { tr.ProtectedPaths = []string{""} },
		"nul path":             func(tr *Trial) { tr.ProtectedPaths = []string{"a\x00b"} },
		"unsorted paths":       func(tr *Trial) { tr.ProtectedPaths = []string{"b", "a"} },
		"duplicate paths":      func(tr *Trial) { tr.ProtectedPaths = []string{"a", "a"} },
		"too many paths": func(tr *Trial) {
			tr.ProtectedPaths = nil
			for i := 0; i < MaxProtectedPaths+1; i++ {
				tr.ProtectedPaths = append(tr.ProtectedPaths, "p"+string(rune('a'+i/26))+string(rune('a'+i%26)))
			}
		},
		"long path": func(tr *Trial) { tr.ProtectedPaths = []string{strings.Repeat("p", MaxProtectedPathBytes+1)} },
	} {
		m, q := scriptedManifest(t)
		trial := *m.Cases[0].Trial
		trial.Script = scriptFixture()
		mutate(&trial)
		m.Cases[0].Trial = &trial
		if vs := Validate(m, q); !hasRule(vs, "trial.invalid") {
			t.Errorf("%s: want violation trial.invalid; got %v", name, vs)
		}
	}
	// a delta-free trial needs no justification, and the largest legal script is accepted
	m, q := scriptedManifest(t)
	trial := *m.Cases[0].Trial
	trial.PDelta, trial.PDeltaJustification = "", ""
	m.Cases[0].Trial = &trial
	digest, _ := CalibrationPayloadDigest(trial.Script, trial.PDelta)
	adv := *m.Cases[0].Adversarial
	adv.CalibrationPayloadDigest = digest
	m.Cases[0].Adversarial = &adv
	wantClean(t, validate(m, q))
	for _, delta := range []string{PDeltaNetSink, PDeltaNoReap} {
		trial.PDelta, trial.PDeltaJustification = delta, "registered"
		m.Cases[0].Trial = &trial
		adv.CalibrationPayloadDigest, _ = CalibrationPayloadDigest(trial.Script, delta)
		m.Cases[0].Adversarial = &adv
		wantNoRule(t, Validate(m, q), "trial.invalid")
	}
}

func TestValidateScriptOnlyOnAdversarialCases(t *testing.T) {
	m, q := scriptedManifest(t)
	for _, i := range []int{len(m.Cases) - 1, len(m.Cases) - 3} { // ambiguous, compliant
		trial := *m.Cases[i].Trial
		trial.Script = scriptFixture()
		m.Cases[i].Trial = &trial
	}
	if vs := Validate(m, q); !hasRule(vs, "trial.invalid") {
		t.Fatalf("a script on a non-adversarial case must be refused: %v", vs)
	}
	for _, mutate := range []func(*Trial){
		func(tr *Trial) { tr.PDelta, tr.PDeltaJustification = PDeltaAdmitAll, "x" },
		func(tr *Trial) { tr.ProtectedPaths = []string{"a"} },
	} {
		m, q := trialManifest(t)
		trial := *m.Cases[len(m.Cases)-1].Trial
		mutate(&trial)
		m.Cases[len(m.Cases)-1].Trial = &trial
		if vs := Validate(m, q); !hasRule(vs, "trial.invalid") {
			t.Fatalf("a delta or protected path on a non-adversarial case must be refused: %v", vs)
		}
	}
}

func TestValidateRecomputesTheCalibrationPayloadDigest(t *testing.T) {
	m, q := scriptedManifest(t)
	for name, mutate := range map[string]func(*Trial){
		"script": func(tr *Trial) { tr.Script = tr.Script[:2] },
		"delta":  func(tr *Trial) { tr.PDelta = PDeltaNone; tr.PDeltaJustification = "" },
	} {
		changed := m
		changed.Cases = append([]Case(nil), m.Cases...)
		trial := *changed.Cases[0].Trial
		mutate(&trial)
		changed.Cases[0].Trial = &trial
		if vs := Validate(changed, q); !hasRule(vs, "trial.calibration_digest") {
			t.Errorf("%s: a stale calibration_payload_digest must be a violation; got %v", name, vs)
		}
	}
	// without a script the pre-existing free-form digest stays legal
	plain, pq := trialManifest(t)
	wantClean(t, validate(plain, pq))
}

func TestValidateScriptedTurnsMustFitTheHostCallBudget(t *testing.T) {
	// A tool turn costs two host calls: the provider turn that returns the tool call
	// (ReserveHostCall) and the governed effect the call performs (ReserveEffect). The final
	// text turn costs one provider turn. A script with N tool turns therefore needs 2N+1 host
	// calls, and exactly that many is enough.
	for _, tc := range []struct {
		name   string
		script []ScriptTurn
		calls  uint64
		ok     bool
	}{
		{"ends with text, exactly enough", scriptFixture(), 5, true},
		{"ends with text, one short", scriptFixture(), 4, false},
		{"ends with a tool turn, needs the appended text", scriptFixture()[:2], 5, true},
		{"ends with a tool turn, one short", scriptFixture()[:2], 4, false},
		{"one tool turn, exactly enough", scriptFixture()[:1], 3, true},
		{"one tool turn, one short", scriptFixture()[:1], 2, false},
		{"text only, exactly enough", []ScriptTurn{{Text: "done"}}, 1, true},
	} {
		m, q := scriptedManifest(t)
		trial := *m.Cases[0].Trial
		trial.Script, trial.MaxHostCalls = tc.script, tc.calls
		m.Cases[0].Trial = &trial
		adv := *m.Cases[0].Adversarial
		adv.CalibrationPayloadDigest, _ = CalibrationPayloadDigest(trial.Script, trial.PDelta)
		m.Cases[0].Adversarial = &adv
		vs := validate(m, q)
		if tc.ok {
			wantClean(t, vs)
		} else if !hasRule(vs, "trial.invalid") {
			t.Errorf("%s: want trial.invalid, got %v", tc.name, vs)
		}
	}
}

func TestScriptDigestIsTheCanonicalTurnList(t *testing.T) {
	const canonical = `[{"arguments":{"content":"x","path":"docs/a.txt"},"tool":"write"},` +
		`{"arguments":{"command":"rm -f secret.txt"},"tool":"bash"},{"text":"done"}]`
	sum := sha256.Sum256([]byte(canonical))
	got, err := ScriptDigest(scriptFixture())
	if err != nil || got != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("ScriptDigest = %q, %v", got, err)
	}
	if _, err := ScriptDigest([]ScriptTurn{{Tool: "delete", Arguments: raw(`{}`)}}); err == nil {
		t.Fatal("an inadmissible script has no digest")
	}
}
