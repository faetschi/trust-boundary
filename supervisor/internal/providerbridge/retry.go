package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strconv"
	"strings"
	"time"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/protocol"
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
//   - a 429 is eligible only with a Retry-After of at most MaxRetryAfter whole seconds, which is
//     waited out before the next attempt (the context ends the wait);
//   - at most MaxProviderRetries extra attempts per turn, then the existing failure path
//     (provider_exchange_unknown) applies to the last attempt;
//   - never after a 200 or any streamed/completion byte (a 200 parallel-call refusal is the separate
//     decision-13 resample of resample.go, which shares this record and bound), never for other 4xx, never for a
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

// RetryableProviderFailure reports whether a complete HTTP response that is not a completion may
// be retried: 502, 503 and 504, 422 whose error says the endpoint is unavailable, or 429 with a
// short Retry-After. The body must be empty or exactly the provider's error object
// ({"error": "..."} or {"error": {"type", "message", ...}}) parsed as JSON, never stream or
// completion content: an SSE content type, any SSE field or comment line, extra top-level
// members or trailing data all make it ineligible.
func RetryableProviderFailure(status int, contentType string, body []byte, retryAfter string) bool {
	if !contentTypeEligible(contentType) {
		return false
	}
	message, ok := providerErrorBody(body)
	if !ok {
		return false
	}
	switch status {
	case 502, 503, 504:
		return true
	case 422:
		return strings.Contains(message, "Endpoint is unavailable")
	case 429:
		_, eligible := rateLimitDelay(retryAfter)
		return eligible
	}
	return false
}

// contentTypeIneligible is the journaled marker for a Content-Type that was observed but cannot
// make a failure eligible: several values, an oversized or malformed one. It is never the same as
// the empty string, which means the header was absent. '!' is not a valid media-type character.
const contentTypeIneligible = "!"

const maxJournaledContentType = 128

// journaledContentType reduces the Content-Type values of a response to what the retry record
// carries: "" when the header was absent, the normalized media type (lower case, no parameters)
// when there is exactly one well-formed value, and contentTypeIneligible otherwise. An observed
// header is never turned into an absent one, so replay classifies it as the live path did.
func journaledContentType(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		value := values[0]
		if len(value) > maxJournaledContentType {
			return contentTypeIneligible
		}
		mediaType, _, err := mime.ParseMediaType(value)
		if err != nil || !wellFormedMediaType(mediaType) {
			return contentTypeIneligible
		}
		return mediaType
	}
	return contentTypeIneligible
}

// contentTypeEligible reports whether a journaled Content-Type allows an error response to be
// retried: absent, or JSON (application/json or a +json type). Event streams, the marker and
// anything else are not. Records written before the normalization kept the header as sent, so a
// stored value is parsed again.
func contentTypeEligible(journaled string) bool {
	if journaled == "" {
		return true
	}
	if journaled == contentTypeIneligible || len(journaled) > maxJournaledContentType {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(journaled)
	if err != nil || !wellFormedMediaType(mediaType) {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// wellFormedMediaType requires exactly one slash with a nonempty type and subtype. mime.ParseMediaType
// also accepts a bare token ("foo+json"), which is not a media type.
func wellFormedMediaType(mediaType string) bool {
	mainType, subtype, found := strings.Cut(mediaType, "/")
	return found && mainType != "" && subtype != "" && !strings.Contains(subtype, "/") && !strings.HasPrefix(subtype, "+")
}

// providerErrorBody accepts an empty body or the provider error object and returns its message.
func providerErrorBody(body []byte) (string, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", true
	}
	if len(body) > retryFailureBodyLimit {
		return "", false
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if len(line) > 0 && line[0] == ':' {
			return "", false
		}
		for _, field := range []string{"data", "event", "id", "retry"} {
			if bytes.HasPrefix(line, []byte(field+":")) || bytes.Equal(line, []byte(field)) {
				return "", false
			}
		}
	}
	// Duplicate members, at any depth, would let a later one decide what a map decode keeps.
	if protocol.ValidateStrictJSON(trimmed) != nil {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || len(object) != 1 {
		return "", false
	}
	if _, trailing := decoder.Token(); !errors.Is(trailing, io.EOF) {
		return "", false
	}
	raw, present := object["error"]
	if !present {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", false
	}
	message := ""
	for key, value := range fields {
		switch key {
		case "message":
			if json.Unmarshal(value, &message) != nil {
				return "", false
			}
		case "type", "code", "param", "status", "metadata":
		default:
			return "", false
		}
	}
	return message, true
}

// MaxRetryAfter bounds the Retry-After a 429 may carry for the attempt to be repeated.
const MaxRetryAfter = 10 * time.Second

// rateLimitDelay parses a Retry-After in whole seconds, 0 to MaxRetryAfter. An HTTP date, a longer
// delay or anything else is not eligible.
func rateLimitDelay(value string) (time.Duration, bool) {
	seconds, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
	if err != nil || time.Duration(seconds)*time.Second > MaxRetryAfter {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// retryWait waits before the next attempt; a seam for tests.
var retryWait = func(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableExchange(records []broker.ExchangeRecord) bool {
	// Only a response received whole (endpoint validated, body read to its end within the bound,
	// clean close) can be an eligible HTTP failure; a read failure or a transport-policy violation
	// is UNKNOWN even when its status looks retryable.
	if len(records) != 1 || !records[0].ResponseComplete() {
		return false
	}
	return RetryableProviderFailure(records[0].StatusCode(), journaledContentType(records[0].ContentTypes()), records[0].ResponseBody(), records[0].RetryAfter())
}

type exchangeRetryEvidence struct {
	WorkflowID  string          `json:"workflow_id"`
	Turn        uint64          `json:"turn"`
	Attempt     uint64          `json:"attempt"`
	Request     requestEvidence `json:"request"`
	StatusCode  int             `json:"status_code"`
	Response    []byte          `json:"response"`
	RetryAfter  string          `json:"retry_after,omitempty"`
	ContentType string          `json:"content_type,omitempty"`
	HostCall    HostCallBinding `json:"host_call"`
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
		Response: records[0].ResponseBody(), RetryAfter: records[0].RetryAfter(), ContentType: journaledContentType(records[0].ContentTypes()), HostCall: binding}
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
	// A rate-limited attempt is repeated only after the delay the provider named (at most MaxRetryAfter).
	if delay, ok := rateLimitDelay(evidence.RetryAfter); ok && evidence.StatusCode == 429 {
		if err := retryWait(ctx, delay); err != nil {
			return fmt.Errorf("wait out the provider rate limit: %w", err)
		}
	}
	return nil
}
