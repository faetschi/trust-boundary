package evalscore

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Synthetic-only JSON fixtures. They check the loader, not thesis data.

func TestLoadRoundTripsSyntheticInput(t *testing.T) {
	want := cleanInput()
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("round trip changed the input")
	}
	if _, err := Summarize(got); err != nil {
		t.Fatalf("Summarize after Load: %v", err)
	}
}

func TestLoadRejectsMalformedInput(t *testing.T) {
	raw, err := json.Marshal(cleanInput())
	if err != nil {
		t.Fatal(err)
	}
	base := string(raw)
	replace := func(old, new string) string {
		if !strings.Contains(base, old) {
			t.Fatalf("fixture lacks %q; the mutation would be a no-op", old)
		}
		return strings.Replace(base, old, new, 1)
	}

	tests := []struct {
		name  string
		input string
	}{
		{"wrong schema", replace(`"schema":"evalscore-trials/v1"`, `"schema":"evalscore-trials/v0"`)},
		{"missing c_effect", replace(`"c_effect":"none",`, ``)},
		{"null identity", replace(`"identity_verified":true`, `"identity_verified":null`)},
		{"unknown field", replace(`"c_effect"`, `"c_efect"`)},
		{"case-variant field", replace(`"case_id"`, `"Case_Id"`)},
		{"duplicate field", replace(`"channel":"tool_call"`, `"channel":"tool_call","channel":"compound"`)},
		{"boolean as string", replace(`"p_static_manifested":true`, `"p_static_manifested":"true"`)},
		{"invalid evidence loss", replace(`"evidence_loss":"none"`, `"evidence_loss":"maybe"`)},
		{"attempt zero", replace(`"attempt":1`, `"attempt":0`)},
		{"fractional attempt", replace(`"attempt":1`, `"attempt":1.5`)},
		{"missing top-level array", replace(`"compliant":`, `"complaint":`)},
		{"trailing data", base + " {}"},
		{"invalid UTF-8 identifier", replace(`"case_id":"adv-000"`, "\"case_id\":\"adv-\xff\"")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(strings.NewReader(tt.input)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestLoadRejectsOversizedInput(t *testing.T) {
	if _, err := Load(strings.NewReader(strings.Repeat(" ", MaxInputBytes+1))); err == nil {
		t.Fatal("want error for input above MaxInputBytes")
	}
}

func TestLoadRejectsNonObjectInput(t *testing.T) {
	for _, s := range []string{``, `[]`, `null`, `"text"`} {
		if _, err := Load(strings.NewReader(s)); err == nil {
			t.Errorf("Load(%q): want error", s)
		}
	}
}
