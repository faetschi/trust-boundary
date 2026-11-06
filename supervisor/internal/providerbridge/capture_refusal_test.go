package providerbridge

import "testing"

// A stream the pre-f53fd29f parser refused: JSON null for tool_calls and role.
const legacyNullRefusalBody = `data: {"id":"null-response","model":"minimax/minimax-m2.5","created":1,"choices":[{"index":0,"delta":{"role":"assistant","content":"x","tool_calls":null},"finish_reason":null}]}

data: {"id":"null-response","model":"minimax/minimax-m2.5","created":1,"choices":[{"index":0,"delta":{"role":null,"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`

func TestLegacyCaptureRefusalReasonKeepsPreFixNullStrictness(t *testing.T) {
	if got := captureRefusalReason([]byte(legacyNullRefusalBody)); got != "" {
		t.Fatalf("live parsing refuses null fields: %q", got)
	}
	if got := legacyCaptureRefusalReason([]byte(legacyNullRefusalBody)); got != "multiple or malformed tool calls" {
		t.Fatalf("legacy reason %q", got)
	}
	if got := legacyCaptureRefusalReason([]byte(rejectedCaptureBodyPlain)); got != "multiple or malformed tool calls" {
		t.Fatalf("two-call body: %q", got)
	}
}

const rejectedCaptureBodyPlain = `data: {"id":"parallel-response","model":"minimax/minimax-m2.5","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{},{}]},"finish_reason":null}]}

data: [DONE]

`
