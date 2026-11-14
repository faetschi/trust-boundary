//go:build linux

package main

import (
	"encoding/json"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
)

// usageSSE is a completed text reply in the wire shape of the F23 M3 provider audits (OpenCode Go):
// the usage object rides on the finishing chunk. An empty usage writes no usage member, as the
// scripted source does.
func usageSSE(usage string) []byte {
	tail := ""
	if usage != "" {
		tail = `,"usage":` + usage
	}
	return []byte(`data: {"id":"gw-1","object":"chat.completion.chunk","created":1794163296,"model":"deepseek-v4.1-flash","choices":[{"index":0,"finish_reason":null,"delta":{"role":"assistant","content":"ok"}}]}` + "\n\n" +
		`data: {"id":"gw-1","object":"chat.completion.chunk","created":1794163296,"model":"deepseek-v4.1-flash","choices":[{"index":0,"finish_reason":"stop","delta":{}}]` + tail + "}\n\n" +
		"data: [DONE]\n\n")
}

func usageRecord(t *testing.T, seq uint64, kind string, response []byte) audit.Record {
	t.Helper()
	data, err := json.Marshal(providerExchangeEvidence{WorkflowID: "wf", Turn: seq, StatusCode: 200, Response: response})
	if err != nil {
		t.Fatal(err)
	}
	return audit.Record{Sequence: seq, Event: audit.Event{Kind: kind, Data: data}}
}

// The runner reads in_tokens, out_tokens and provider_retries from this receipt (internal/runner
// governedReceipt). F23 M3 finding 7: the receipt carried no usage, so every governed unit
// journaled 0 tokens although the provider audit held the usage.
func TestReceiptUsageSumsSettledExchangesOfTheProviderAudit(t *testing.T) {
	m3 := `{"prompt_tokens":832,"completion_tokens":40,"total_tokens":872,"prompt_tokens_details":{"cached_tokens":0},` +
		`"completion_tokens_details":{"reasoning_tokens":7},"cost":0,"opencode":{"billing_source":"go"}}`
	trace := audit.Trace{Records: []audit.Record{
		usageRecord(t, 1, "provider_exchange", usageSSE(m3)),
		// A refused or resampled attempt (decision 13 / D54): counted as a retry, not in the tokens.
		usageRecord(t, 2, "provider_exchange_retryable_failure", usageSSE(`{"prompt_tokens":5000,"completion_tokens":500}`)),
		usageRecord(t, 3, "provider_exchange", usageSSE(`{"prompt_tokens":1100,"completion_tokens":60}`)),
		usageRecord(t, 4, "provider_exchange", usageSSE("")), // scripted: no usage object
		usageRecord(t, 5, "provider_exchange_unknown", usageSSE(`{"prompt_tokens":9000,"completion_tokens":900}`)),
	}}
	var receipt productionPiReceipt
	if err := applyProviderLineage(&receipt, trace, openrouter.Route{}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		InTokens        int `json:"in_tokens"`
		OutTokens       int `json:"out_tokens"`
		ProviderRetries int `json:"provider_retries"`
		ProviderTurns   int `json:"provider_turns"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.InTokens != 832+1100 || wire.OutTokens != 40+60 || wire.ProviderRetries != 1 || wire.ProviderTurns != 3 {
		t.Fatalf("receipt usage = %+v, want in %d out %d retries 1 turns 3", wire, 832+1100, 40+60)
	}
}
