package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
)

const CaptureRefusalEvent = "provider_capture_refused"
const captureRefusalID = "capture-refused"

// The raw response stays in the host journal, as for provider_exchange_unknown;
// none of its text or calls enter the conversation or reach the worker.
type captureRefusalEvidence struct {
	WorkflowID     string          `json:"workflow_id"`
	Turn           uint64          `json:"turn"`
	Request        requestEvidence `json:"request"`
	StatusCode     int             `json:"status_code"`
	Response       []byte          `json:"response"`
	ResponseDigest string          `json:"response_digest"`
	Reason         string          `json:"reason"`
	HostCall       HostCallBinding `json:"host_call"`
}

func captureRefusalReason(response []byte) string {
	if _, err := openrouter.CaptureSSE(bytes.NewReader(response)); err != nil {
		// Unsupported-field errors can depend on Go map iteration; never persist that text.
		if err.Error() == "multiple or malformed tool calls" {
			return "multiple or malformed tool calls"
		}
		return "invalid provider response"
	}
	return ""
}

func (c *Conversation) recordCaptureRefusalLocked(ctx context.Context, exchange broker.ExchangeRecord, binding HostCallBinding, reason string) error {
	response := exchange.ResponseBody()
	data, err := json.Marshal(captureRefusalEvidence{WorkflowID: c.cfg.WorkflowID, Turn: c.turns + 1,
		Request: requestEvidenceFromBroker(exchange.Request()), StatusCode: exchange.StatusCode(),
		Response: response, ResponseDigest: digestBytes(response), Reason: reason, HostCall: binding})
	if err != nil {
		return err
	}
	record, err := c.appendLocked(CaptureRefusalEvent, captureRefusalID, data)
	if err != nil {
		return fmt.Errorf("persist provider capture refusal: %w", err)
	}
	if err := c.cfg.CompleteHostCall(ctx, HostCallCompletion{Binding: binding, OutcomeDigest: record.Hash}); err != nil {
		return fmt.Errorf("settle refused provider exchange reservation: %w", err)
	}
	return nil
}
