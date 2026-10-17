package evalcorpus

import (
	"bytes"
	"strings"
	"testing"
)

func trialFixture(label string) *Trial {
	return &Trial{Prompt: "synthetic host-approved prompt " + label, MaxHostCalls: 8, MaxTokens: 20000, DurationSeconds: 600}
}

func trialManifest(t *testing.T) (Manifest, Quotas) {
	t.Helper()
	q := smallQuotas()
	m := allocated(t, synthManifest(q, synthSeed), q)
	for i := range m.Cases {
		m.Cases[i].Trial = trialFixture(m.Cases[i].CaseID)
	}
	return m, q
}

func TestTrialIsPartOfTheCanonicalFormAndTheDigest(t *testing.T) {
	m, q := trialManifest(t)
	wantClean(t, Validate(m, q))
	canonical, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"trial":{"duration_seconds":600,"max_host_calls":8,"max_tokens":20000,"prompt":"synthetic host-approved prompt`} {
		if !bytes.Contains(canonical, []byte(want)) {
			t.Fatalf("canonical form lacks %q", want)
		}
	}
	base, err := Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Trial){
		"prompt":   func(tr *Trial) { tr.Prompt += " changed" },
		"calls":    func(tr *Trial) { tr.MaxHostCalls++ },
		"tokens":   func(tr *Trial) { tr.MaxTokens++ },
		"duration": func(tr *Trial) { tr.DurationSeconds++ },
	} {
		changed := m
		changed.Cases = append([]Case(nil), m.Cases...)
		trial := *changed.Cases[0].Trial
		mutate(&trial)
		changed.Cases[0].Trial = &trial
		got, err := Digest(changed)
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Errorf("digest does not cover the trial %s", name)
		}
	}
}

func TestManifestWithoutTrialKeepsItsDigest(t *testing.T) {
	q := smallQuotas()
	m := allocated(t, synthManifest(q, synthSeed), q)
	canonical, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte(`"trial"`)) {
		t.Fatal("a case without a trial must not emit a trial member")
	}
	wantClean(t, Validate(m, q))
}

func TestLoadRoundTripsTrialAndRejectsNonIntegerBudgets(t *testing.T) {
	m, _ := trialManifest(t)
	canonical, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(bytes.NewReader(canonical))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := Digest(loaded)
	want, _ := Digest(m)
	if err != nil || got != want {
		t.Fatalf("digest after Load = %q, %v; want %q", got, err, want)
	}
	for name, replacement := range map[string]string{
		"fraction":      `"max_tokens":20000.5`,
		"exponent":      `"max_tokens":2e4`,
		"negative":      `"max_tokens":-1`,
		"string":        `"max_tokens":"20000"`,
		"null":          `"max_tokens":null`,
		"fraction-zero": `"max_tokens":20000.0`,
	} {
		bad := strings.Replace(string(canonical), `"max_tokens":20000`, replacement, 1)
		if bad == string(canonical) {
			t.Fatalf("%s: fixture not replaced", name)
		}
		if _, err := Load(strings.NewReader(bad)); err == nil {
			t.Errorf("%s: Load accepted a non-integer or non-number budget", name)
		}
	}
	missing := strings.Replace(string(canonical), `"max_tokens":20000,`, ``, 1)
	if _, err := Load(strings.NewReader(missing)); err == nil {
		t.Error("Load accepted a trial without max_tokens")
	}
}

func TestValidateTrialRules(t *testing.T) {
	for name, mutate := range map[string]func(*Trial){
		"empty prompt":      func(tr *Trial) { tr.Prompt = "" },
		"blank prompt":      func(tr *Trial) { tr.Prompt = " \n\t" },
		"nul in prompt":     func(tr *Trial) { tr.Prompt = "a\x00b" },
		"oversize prompt":   func(tr *Trial) { tr.Prompt = strings.Repeat("p", MaxTrialPromptBytes+1) },
		"zero host calls":   func(tr *Trial) { tr.MaxHostCalls = 0 },
		"zero tokens":       func(tr *Trial) { tr.MaxTokens = 0 },
		"zero duration":     func(tr *Trial) { tr.DurationSeconds = 0 },
		"unsafe host calls": func(tr *Trial) { tr.MaxHostCalls = MaxTrialNumber + 1 },
		"unsafe tokens":     func(tr *Trial) { tr.MaxTokens = MaxTrialNumber + 1 },
		"unsafe duration":   func(tr *Trial) { tr.DurationSeconds = MaxTrialNumber + 1 },
	} {
		m, q := trialManifest(t)
		trial := *m.Cases[1].Trial
		mutate(&trial)
		m.Cases[1].Trial = &trial
		if vs := Validate(m, q); !hasRule(vs, "trial.invalid") {
			t.Errorf("%s: want violation trial.invalid; got %v", name, vs)
		}
	}
	m, q := trialManifest(t)
	trial := *m.Cases[0].Trial
	trial.MaxTokens = MaxTrialNumber
	m.Cases[0].Trial = &trial
	wantNoRule(t, Validate(m, q), "trial.invalid")
	if _, err := Digest(m); err != nil {
		t.Fatalf("the largest safe integer must canonicalize: %v", err)
	}
}

func TestCanonicalJSONRefusesAnUnsafeInteger(t *testing.T) {
	m, _ := trialManifest(t)
	trial := *m.Cases[0].Trial
	trial.MaxTokens = MaxTrialNumber + 1
	m.Cases[0].Trial = &trial
	if _, err := Digest(m); err == nil {
		t.Fatal("an integer above 2^53-1 has no interoperable canonical form and must be refused")
	}
}

func hasRule(vs []Violation, rule string) bool {
	for _, v := range vs {
		if v.Rule == rule {
			return true
		}
	}
	return false
}
