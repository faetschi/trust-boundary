package evalscore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Synthetic-only JSON fixtures. They check the loader, not thesis data.

func TestLoadRoundTripsSyntheticInput(t *testing.T) {
	want := cleanInput()
	want.Registered = nil // not part of the trials schema, so Load never sets it
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

// TestLoadRejectsMalformedInput checks each rejection by its specific error text,
// so a mutation that rejects for the wrong reason fails the test.
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
		want  string // substring of the error
	}{
		{"wrong schema", replace(`"schema":"evalscore-trials/v1"`, `"schema":"evalscore-trials/v0"`),
			`schema "evalscore-trials/v0", want "evalscore-trials/v1"`},
		{"missing c_effect", replace(`"c_effect":"none",`, ``), `missing field "c_effect"`},
		{"null identity", replace(`"identity_verified":true`, `"identity_verified":null`), `field "identity_verified" is null`},
		{"unknown field", replace(`"c_effect"`, `"c_efect"`), `unknown field "c_efect"`},
		{"case-variant field", replace(`"case_id"`, `"Case_Id"`), `unknown field "Case_Id"`},
		{"duplicate field", replace(`"channel":"tool_call"`, `"channel":"tool_call","channel":"compound"`), `duplicate field "channel"`},
		{"boolean as string", replace(`"p_static_manifested":true`, `"p_static_manifested":"true"`), `p_static_manifested`},
		{"attempt zero", replace(`"attempt":1`, `"attempt":0`), `attempt 0, want >= 1`},
		{"fractional attempt", replace(`"attempt":1`, `"attempt":1.5`), `attempt`},
		{"missing top-level array", replace(`"compliant":`, `"complaint":`), `unknown field "complaint"`},
		{"trailing data", base + " {}", `trailing data after JSON object`},
		{"invalid UTF-8 identifier", replace(`"case_id":"adv-000"`, "\"case_id\":\"adv-\xff\""), `not valid UTF-8`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(tt.input))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestLoadAcceptsUnregisteredEnumForReporting: Load checks JSON shape only. A
// well-formed record with an unregistered enum value loads, and Summarize reports
// it as an invalid record.
func TestLoadAcceptsUnregisteredEnumForReporting(t *testing.T) {
	raw, err := json.Marshal(cleanInput())
	if err != nil {
		t.Fatal(err)
	}
	in, err := Load(strings.NewReader(strings.Replace(string(raw), `"evidence_loss":"none"`, `"evidence_loss":"maybe"`, 1)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s, err := Summarize(in)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(s.InvalidRecords) != 1 {
		t.Fatalf("invalid records = %+v, want one", s.InvalidRecords)
	}
}

// TestLoadRejectsOversizedInput uses valid JSON whose only defect is its size, so
// the size check is the only thing that can reject it. Without that check, Load
// would accept the input.
func TestLoadRejectsOversizedInput(t *testing.T) {
	raw := `{` + strings.Repeat(" ", MaxInputBytes) +
		`"schema":"evalscore-trials/v1","evq_passed":true,"adversarial":[],"compliant":[]}`
	_, err := Load(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("trials exceed %d bytes", MaxInputBytes)) {
		t.Fatalf("Load oversized input: err = %v; want the size error", err)
	}
}

func TestLoadRejectsNonObjectInput(t *testing.T) {
	for _, s := range []string{``, `[]`, `null`, `"text"`} {
		if _, err := Load(strings.NewReader(s)); err == nil {
			t.Errorf("Load(%q): want error", s)
		}
	}
}
