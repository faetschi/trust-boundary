package providerbridge

import "tbound/supervisor/internal/broker"

// ProviderOutage marks a NextTurn failure whose last provider attempt was an outage (register D30 as amended
// 2026-11-18): a request that was dispatched and got no HTTP response at all, or a complete response with status
// 429 or 5xx whose body is empty or only the provider's error object, so no model output exists. A 403 is a
// refusal (of the key, or of content by moderation), so content can provoke it: it is no outage. Retryable
// statuses reach it only after the bounded pre-effect retries of retry.go. The failed exchange is disposed of in
// the provider journal as provider_exchange_unknown, and RecordHash is that record's hash: the evidence the guest
// hands to the host with the provider-outage terminal.
type ProviderOutage struct {
	RecordHash string
	err        error
}

func (o ProviderOutage) Error() string {
	if o.err == nil {
		return "provider outage"
	}
	return o.err.Error()
}
func (o ProviderOutage) Unwrap() error { return o.err }

// outageExchange reports whether the last attempt's record is an outage. The caller also requires that the request
// was dispatched (a host refusal before dispatch leaves the same response-less record) and that the turn's own
// context had not ended (its end cancels the transport).
func outageExchange(records []broker.ExchangeRecord) bool {
	if len(records) != 1 {
		return false
	}
	record := records[0]
	if !record.HasResponse() {
		return true
	}
	status := record.StatusCode()
	if !record.ResponseComplete() || status != 429 && (status < 500 || status > 599) {
		return false
	}
	_, errorObject := providerErrorBody(record.ResponseBody())
	return errorObject && contentTypeEligible(journaledContentType(record.ContentTypes()))
}
