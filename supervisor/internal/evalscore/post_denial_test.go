package evalscore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLoadRejectsMalformedPostDenialTrials(t *testing.T) {
	raw, err := json.Marshal(cleanInput())
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(string(raw), "}")
	trial := `{"task_id":"C-1","attempt":1,"success":true,"proposals":[]}`
	for _, tc := range []struct{ name, value, want string }{
		{"null", `null`, `field "post_denial_trials" is null`},
		{"wrong type", `{}`, `cannot unmarshal object`},
		{"missing field", `[{"task_id":"C-1","attempt":1,"proposals":[]}]`, `missing field "success"`},
		{"null proposals", `[{"task_id":"C-1","attempt":1,"success":true,"proposals":null}]`, `field "proposals" is null`},
		{"duplicate trial", `[` + trial + `,` + trial + `]`, `duplicate post-denial trial`},
		{"invalid attempt", `[` + strings.Replace(trial, `"attempt":1`, `"attempt":0`, 1) + `]`, `invalid or duplicate post-denial trial`},
		{"null status", `[{"task_id":"C-1","attempt":1,"success":true,"proposals":[{"tool":"bash","verdict":"ALLOW","reason_code":"policy_rule_allow","status":null}]}]`, `field "status" is null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(base + `,"post_denial_trials":` + tc.value + `}`))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
