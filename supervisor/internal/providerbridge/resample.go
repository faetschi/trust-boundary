package providerbridge

import (
	"bytes"
	"errors"
	"net/http"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
)

// Bounded broker resample (thesis decision 13, register entry D54).
//
// A provider may stream a second tool call although the request sets parallel_tool_calls=false. The
// capture refuses that stream (openrouter.ErrParallelToolCall) and still does. When the refusal is
// exactly that class, on a complete HTTP 200 response, the turn may send the same request bytes again:
//
//   - the refused attempt is closed like a retryable failure (retry.go): a
//     provider_exchange_retryable_failure record carries its exact request and the whole refused stream,
//     binds its host-call reservation, and settles that reservation before the next attempt reserves;
//   - every attempt is its own reserved host call, charged against the workflow host-call and token
//     ceilings; on the research route it is also its own USD reservation (SpendReservation.Attempt),
//     and the refused attempt's reservation is marked uncertain, so its full worst case stays booked;
//   - the resample must present the same request digest as the refused attempt. A host that would select
//     another completion cap, or any refused reservation, stops the turn before dispatch;
//   - at most MaxParallelCallResamples per turn, a bound shared with the pre-effect retries of retry.go;
//     after it the last refused attempt takes the existing failure path (provider_exchange_unknown, the
//     request ends UNKNOWN and H1 scores CONSERVATIVE_FAILURE);
//   - never in a product turn (Config.ProductCaptureRefusals), which keeps its durable capture refusal.

// MaxParallelCallResamples is decision 13's resample bound (register D54). It is the per-turn bound of
// extra attempts, MaxProviderRetries, which pre-effect retries and resamples share.
const MaxParallelCallResamples = MaxProviderRetries

var errResampleRequestChanged = errors.New("decision 13 resample: the request differs from the refused attempt")

// ParallelToolCallResample reports whether a journaled attempt is the decision 13 class: an HTTP 200
// response whose capture on route is refused for a parallel tool call.
func ParallelToolCallResample(status int, response []byte, route openrouter.Route) bool {
	if status != http.StatusOK {
		return false
	}
	_, err := openrouter.CaptureSSEForRoute(bytes.NewReader(response), route)
	return errors.Is(err, openrouter.ErrParallelToolCall)
}

// resampleableExchange is ParallelToolCallResample for the one attempt the broker retained, which must
// have been received whole.
func resampleableExchange(records []broker.ExchangeRecord, route openrouter.Route) bool {
	return len(records) == 1 && records[0].ResponseComplete() &&
		ParallelToolCallResample(records[0].StatusCode(), records[0].ResponseBody(), route)
}
