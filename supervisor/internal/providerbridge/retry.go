package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"tbound/supervisor/internal/broker"
)

// Pre-effect provider retry.
//
// The upstream behind the OpenCode Go route is intermittently unavailable and says so before any
// model output exists: HTTP 502/503/504, or 422/503 with "Endpoint is unavailable". Such an
// attempt did no work and returned no stream byte, so it can be repeated without any effect
// having happened. Each repeat is its own reserved host call (it is charged against the workflow
// host-call and token ceilings exactly like a first attempt), and the failed attempt is closed
// durably by a provider_exchange_retryable_failure record that binds that reservation, so the
// journal never has two open reservations and replay can prove every attempt.
//
//   - at most MaxProviderRetries extra attempts per turn, then the existing failure path
//     (provider_exchange_unknown) applies to the last attempt;
//   - never after a 200 or any streamed/completion byte, never for other 4xx, never for a
//     transport error (the request may have been processed), never on the research route
//     (its monetary spend accounting has its own per-request reservation);
//   - a crash after the retry record but before the host call is settled in the workflow
//     ledger leaves a reservation the ledger still calls reserved: that is UNKNOWN, exactly as
//     for a crash between provider_exchange and its settlement. A crash after the settlement
//     leaves the post-failure checkpoint (awaiting provider exchange, terminal host call, no
//     captured call), which the ledger admits a fresh reservation from and which restart treats as
//     any other idle point before an exchange.

// MaxProviderRetries is the number of extra attempts a turn may make after a retryable failure.
const MaxProviderRetries = 2

const (
	exchangeRetryKind     = "provider_exchange_retryable_failure"
	exchangeRetryID       = "exchange-retry"
	retryFailureBodyLimit = 16 << 10
)

// RetryableProviderFailure reports whether an HTTP response that is not a completion may be
// retried: 502, 503 and 504, or 422 whose body says the endpoint is unavailable. A body that
// carries stream or completion content is never retryable.
func RetryableProviderFailure(status int, body []byte) bool {
	if len(body) > retryFailureBodyLimit || bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:")) ||
		bytes.Contains(body, []byte(`"choices"`)) {
		return false
	}
	switch status {
	case 502, 503, 504:
		return true
	case 422:
		return bytes.Contains(body, []byte("Endpoint is unavailable"))
	}
	return false
}

func retryableExchange(records []broker.ExchangeRecord) bool {
	if len(records) != 1 || !records[0].HasResponse() {
		return false
	}
	return RetryableProviderFailure(records[0].StatusCode(), records[0].ResponseBody())
}

type exchangeRetryEvidence struct {
	WorkflowID string          `json:"workflow_id"`
	Turn       uint64          `json:"turn"`
	Attempt    uint64          `json:"attempt"`
	Request    requestEvidence `json:"request"`
	StatusCode int             `json:"status_code"`
	Response   []byte          `json:"response"`
	HostCall   HostCallBinding `json:"host_call"`
}

// recordRetryableFailureLocked closes a failed attempt: it journals the retryable failure bound
// to the attempt's reservation and then settles that reservation, so the next attempt can reserve.
func (c *Conversation) recordRetryableFailureLocked(ctx context.Context, records []broker.ExchangeRecord,
	binding HostCallBinding, attempt uint64) error {
	if len(records) != 1 {
		return errors.New("retryable provider failure without exactly one recorded attempt")
	}
	evidence := exchangeRetryEvidence{WorkflowID: c.cfg.WorkflowID, Turn: c.turns + 1, Attempt: attempt,
		Request: requestEvidenceFromBroker(records[0].Request()), StatusCode: records[0].StatusCode(),
		Response: records[0].ResponseBody(), HostCall: binding}
	data, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("encode retryable provider failure: %w", err)
	}
	record, err := c.appendLocked(exchangeRetryKind, exchangeRetryID, data)
	if err != nil {
		return fmt.Errorf("persist retryable provider failure; response withheld: %w", err)
	}
	if err := c.cfg.CompleteHostCall(ctx, HostCallCompletion{Binding: binding, OutcomeDigest: record.Hash}); err != nil {
		return fmt.Errorf("settle failed provider attempt reservation before the next attempt: %w", err)
	}
	return nil
}
