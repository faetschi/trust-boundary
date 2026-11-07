// Package broker composes a registered OpenRouter profile, an injected HTTP
// transport, and the strict provider-call correlation protocol. It has no
// default transport: callers must explicitly inject the trusted Doer.
package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
)

const (
	maxProfileIDBytes = 128
	// MaxDurableExchangeBytes reserves the raw request and response bytes that
	// can be nested into one bounded provider audit event. The bridge serializer
	// applies two base64 layers to these byte slices.
	MaxDurableExchangeBytes = 3 << 20
	// MaxRecordedBytes bounds exact process-local request/response retention.
	MaxRecordedBytes     = 16 << 20
	maxRecordedExchanges = protocol.MaxCapturesPerStream
)

var declaredToolManifest = [...]string{"read", "write", "edit", "bash"}

// DeclaredToolManifest returns the fixed, ordered Pi tool surface. A profile
// must declare this exact manifest; it cannot add, remove, or reorder tools.
func DeclaredToolManifest() []string {
	return append([]string(nil), declaredToolManifest[:]...)
}

// Profile is trusted broker configuration, never populated from Pi IPC. The
// endpoint and request schemas remain fixed by openrouter.BuildRequest.
type Profile struct {
	ID             string
	ConversationID string
	Model          string
	// Route selects the wire profile. The zero Route is the fixed engineering
	// (OpenCode Go) route; a research route must name the same Model.
	Route            openrouter.Route
	Messages         []openrouter.Message
	ToolManifest     []string
	ToolCallIssuer   string
	ResponseIDIssuer string
	Generation       string
}

// HTTPDoer is the only provider transport seam. The implementation must not
// follow redirects or mutate the request. This package supplies no HTTP client
// and performs no network access unless a caller injects a Doer.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// RequestRecord is an immutable in-memory snapshot of the request presented to
// the injected transport. Its accessors return defensive copies.
type RequestRecord struct {
	profileID     string
	method        string
	url           string
	host          string
	contentLength int64
	header        http.Header
	body          []byte
}

func (r RequestRecord) ProfileID() string    { return r.profileID }
func (r RequestRecord) Method() string       { return r.method }
func (r RequestRecord) URL() string          { return r.url }
func (r RequestRecord) Host() string         { return r.host }
func (r RequestRecord) ContentLength() int64 { return r.contentLength }
func (r RequestRecord) Header() http.Header  { return r.header.Clone() }
func (r RequestRecord) Body() []byte         { return append([]byte(nil), r.body...) }

// DigestRequestRecord commits to the exact fixed-profile request metadata and
// bytes presented to the injected transport. The digest is lowercase hex with
// no prefix, matching workflowrecovery's canonical digest format.
func DigestRequestRecord(r RequestRecord) string {
	encoded, err := json.Marshal(struct {
		ProfileID     string      `json:"profile_id"`
		Method        string      `json:"method"`
		URL           string      `json:"url"`
		Host          string      `json:"host"`
		ContentLength int64       `json:"content_length"`
		Headers       http.Header `json:"headers"`
		Body          []byte      `json:"body"`
	}{r.profileID, r.method, r.url, r.host, r.contentLength, r.header.Clone(), append([]byte(nil), r.body...)})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte("tbound-provider-request/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

// ExchangeRecord retains the exact request and, when received, the bounded raw
// response bytes. Records are process-local and are not durable audit evidence.
type ExchangeRecord struct {
	request      RequestRecord
	statusCode   int
	responseBody []byte
	responseSet  bool
	failure      string
	retryAfter   string
	contentTypes []string
	// responseComplete is true only when the endpoint was validated, the body was non-nil and
	// read to its end within the bound, and the body closed cleanly. An empty body still counts: it is
	// a complete (empty) HTTP response, which the 200 path refuses for its own reason.
	responseComplete bool
}

func (r ExchangeRecord) Request() RequestRecord { return cloneRequestRecord(r.request) }
func (r ExchangeRecord) StatusCode() int        { return r.statusCode }
func (r ExchangeRecord) HasResponse() bool      { return r.responseSet }
func (r ExchangeRecord) ResponseBody() []byte   { return append([]byte(nil), r.responseBody...) }
func (r ExchangeRecord) Failure() string        { return r.failure }

// RetryAfter is the bounded Retry-After header value of the response, as sent.
func (r ExchangeRecord) RetryAfter() string { return r.retryAfter }

// ContentTypes is every Content-Type header value of the response, in order, each cut at 256
// bytes (a longer value is ineligible whatever its tail says) and at most 9 values (more than one is
// ambiguous whatever the rest say).
func (r ExchangeRecord) ContentTypes() []string { return append([]string(nil), r.contentTypes...) }

// ResponseComplete reports that the response was received whole: endpoint and method validated, a
// body read to its end within the byte bound, and a clean close. A read failure (for example an
// unexpected EOF), a nil body or a transport that changed the endpoint leaves it false.
func (r ExchangeRecord) ResponseComplete() bool { return r.responseSet && r.responseComplete }

// Broker serializes provider exchanges and proposal correlation for one
// registered profile/session. A valid provider tool call is admitted to the
// protocol stream once, then consumed at most once by Correlate.
type Broker struct {
	mu       sync.Mutex
	profile  Profile
	doer     HTTPDoer
	stream   *protocol.Stream
	sequence uint64
	recorded uint64
	closed   bool
	records  []ExchangeRecord
}

// New registers one immutable provider profile and its explicitly injected
// transport. The profile is validated and copied before it can be used.
func New(profile Profile, doer HTTPDoer) (*Broker, error) {
	if doer == nil {
		return nil, errors.New("an injected HTTP Doer is required")
	}
	if !validText(profile.ID, maxProfileIDBytes) ||
		(profile.ConversationID != "" && !validText(profile.ConversationID, 256)) ||
		!validText(profile.Generation, 128) {
		return nil, errors.New("invalid registered profile identity or generation")
	}
	if profile.ConversationID == "" {
		// Low-level broker users that have no conversation owner get a stable
		// profile-scoped session. providerbridge always supplies Config.ConversationID.
		profile.ConversationID = profile.ID
	}
	if !slices.Equal(profile.ToolManifest, declaredToolManifest[:]) {
		return nil, errors.New("registered profile must declare exactly read, write, edit, bash in order")
	}
	stream, err := protocol.NewStream(profile.ToolCallIssuer, profile.ResponseIDIssuer)
	if err != nil {
		return nil, fmt.Errorf("register provider identifier namespaces: %w", err)
	}
	profile.Messages = cloneMessages(profile.Messages)
	profile.ToolManifest = DeclaredToolManifest()
	route := profile.Route.Resolved()
	if route.Model != profile.Model {
		return nil, errors.New("registered profile model differs from its provider route")
	}
	if _, err := openrouter.BuildRequestForRoute(route, profile.Messages, 0); err != nil {
		return nil, fmt.Errorf("invalid registered provider profile: %w", err)
	}
	return &Broker{profile: profile, doer: doer, stream: stream}, nil
}

// Exchange builds a request only from the registered profile, records its
// exact method/URL/headers/body before transport, and returns the bounded parsed
// provider response. A captured tool call is registered for a later Pi
// proposal; a transport or provider-protocol failure closes this broker stream.
func (b *Broker) Exchange(ctx context.Context) (openrouter.CapturedResponse, error) {
	return b.ExchangeWithCompletionLimit(ctx, 0, nil)
}

// ExchangeWithCompletionLimit uses the exact host-selected completion cap in
// the outbound request. beforeDispatch runs after the request is fixed and
// recorded, but before the transport can see it.
func (b *Broker) ExchangeWithCompletionLimit(ctx context.Context, completionLimit uint64,
	beforeDispatch func(RequestRecord) error) (openrouter.CapturedResponse, error) {
	if b == nil {
		return openrouter.CapturedResponse{}, errors.New("nil provider broker")
	}
	if ctx == nil {
		return openrouter.CapturedResponse{}, errors.New("exchange context is required")
	}
	if err := ctx.Err(); err != nil {
		return openrouter.CapturedResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return openrouter.CapturedResponse{}, errors.New("provider broker stream is closed")
	}
	if err := ctx.Err(); err != nil {
		return openrouter.CapturedResponse{}, err
	}

	built, err := openrouter.BuildRequestForRoute(b.profile.Route, b.profile.Messages, completionLimit)
	if err != nil {
		return openrouter.CapturedResponse{}, b.failLocked(-1, fmt.Errorf("build registered provider request: %w", err))
	}
	body := built.Body()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, built.RequestURL(), bytes.NewReader(body))
	if err != nil {
		return openrouter.CapturedResponse{}, b.failLocked(-1, fmt.Errorf("construct registered provider request: %w", err))
	}
	// The route fixes the header set: the research route sends no vendor session
	// header (see openrouter.Route.RequestHeaders).
	request.Header = b.profile.Route.RequestHeaders(b.profile.ConversationID)
	requestBytes := uint64(len(body))
	if requestBytes >= uint64(MaxDurableExchangeBytes) {
		_ = request.Body.Close()
		return openrouter.CapturedResponse{}, b.failLocked(-1, errors.New("provider request leaves no durable response budget"))
	}
	responseLimit := min(uint64(MaxDurableExchangeBytes)-requestBytes, uint64(openrouter.MaxResponseBytes))
	remaining := uint64(MaxRecordedBytes) - b.recorded
	if len(b.records) >= maxRecordedExchanges || requestBytes > remaining ||
		responseLimit+1 > remaining-requestBytes {
		_ = request.Body.Close()
		return openrouter.CapturedResponse{}, b.failLocked(-1, errors.New("provider exchange record budget exhausted"))
	}
	requestRecord := RequestRecord{
		profileID: b.profile.ID, method: request.Method, url: request.URL.String(), host: request.Host,
		contentLength: request.ContentLength, header: request.Header.Clone(), body: append([]byte(nil), body...),
	}
	recordIndex := len(b.records)
	b.records = append(b.records, ExchangeRecord{request: requestRecord})
	b.recorded += requestBytes
	if beforeDispatch != nil {
		if err := beforeDispatch(cloneRequestRecord(requestRecord)); err != nil {
			return openrouter.CapturedResponse{}, b.failLocked(recordIndex, fmt.Errorf("host refused provider dispatch: %w", err))
		}
	}

	response, doErr := b.doer.Do(request)
	_ = request.Body.Close()
	if doErr != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, fmt.Errorf("provider transport failed: %w", doErr))
	}
	if response == nil {
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, errors.New("OpenRouter transport returned a nil response"))
	}
	record := &b.records[recordIndex]
	record.statusCode = response.StatusCode
	if value := response.Header.Get("Retry-After"); len(value) <= 32 {
		record.retryAfter = value
	}
	for index, value := range response.Header.Values("Content-Type") {
		if index == 9 {
			break
		}
		if len(value) > 256 {
			value = value[:256]
		}
		record.contentTypes = append(record.contentTypes, value)
	}
	record.responseSet = true
	if response.Request != nil && response.Request.URL != nil &&
		(response.Request.Method != http.MethodPost || response.Request.URL.String() != b.profile.Route.Resolved().Endpoint) {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, errors.New("provider transport changed the registered endpoint or method"))
	}
	if response.Body == nil {
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, errors.New("OpenRouter response has no body"))
	}
	if err := ctx.Err(); err != nil {
		_ = response.Body.Close()
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, fmt.Errorf("OpenRouter exchange canceled: %w", err))
	}

	responseBody, readErr := readBoundedResponse(response.Body, int(responseLimit))
	closeErr := response.Body.Close()
	record.responseBody = append([]byte(nil), responseBody...)
	b.recorded += uint64(len(responseBody))
	record.responseComplete = (readErr == nil || errors.Is(readErr, errEmptyResponse)) && closeErr == nil
	if readErr != nil {
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, fmt.Errorf("read provider response: %w", readErr))
	}
	if closeErr != nil {
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, fmt.Errorf("close OpenRouter response: %w", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, fmt.Errorf("OpenRouter returned HTTP status %d", response.StatusCode))
	}

	captured, err := openrouter.CaptureSSE(bytes.NewReader(responseBody))
	if err != nil {
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, fmt.Errorf("capture OpenRouter response: %w", err))
	}
	if !b.profile.Route.AcceptsResponseModel(captured.Model()) {
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, errors.New("provider response model differs from the registered profile"))
	}
	if b.sequence == ^uint64(0) {
		return openrouter.CapturedResponse{}, b.failLocked(recordIndex, errors.New("provider response sequence exhausted"))
	}
	b.sequence++
	if _, hasCall := captured.ToolCall(); hasCall {
		trusted, err := captured.TrustedCapture(
			b.profile.ToolCallIssuer, b.profile.ResponseIDIssuer, b.profile.Generation, b.sequence,
		)
		if err != nil {
			return openrouter.CapturedResponse{}, b.failLocked(recordIndex, fmt.Errorf("map trusted provider capture: %w", err))
		}
		decision := b.stream.Capture(trusted)
		if !decision.Accepted || decision.StreamClosed {
			return openrouter.CapturedResponse{}, b.failLocked(recordIndex, fmt.Errorf("register provider tool call: %s", decision.ReasonCode))
		}
	}
	return captured, nil
}

// Correlate strictly decodes the actual Pi proposal shape and delegates the
// issuer, response ID, tool-call ID, exact tool name, digest, order, and one-use
// checks to protocol.Stream and its existing correlation core.
func (b *Broker) Correlate(ctx context.Context, proposal protocol.Proposal) (correlation.Decision, error) {
	if b == nil {
		return correlation.Decision{}, errors.New("nil provider broker")
	}
	if ctx == nil {
		return correlation.Decision{}, errors.New("correlation context is required")
	}
	if err := ctx.Err(); err != nil {
		return correlation.Decision{}, err
	}
	encoded, err := protocol.MarshalProposal(proposal)
	if err != nil {
		return correlation.Decision{}, fmt.Errorf("encode bounded Pi proposal: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return correlation.Decision{}, err
	}
	if b.closed {
		return closedDecision(), nil
	}
	decision := b.stream.Propose(encoded)
	if decision.StreamClosed {
		b.closed = true
	}
	return decision, nil
}

// Records returns defensive copies of all provider request/result records.
// They are in-memory diagnostics only and do not replace durable audit.
func (b *Broker) Records() []ExchangeRecord {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	records := make([]ExchangeRecord, len(b.records))
	for index, record := range b.records {
		records[index] = cloneExchangeRecord(record)
	}
	return records
}

func cloneMessages(messages []openrouter.Message) []openrouter.Message {
	cloned := make([]openrouter.Message, len(messages))
	for index, message := range messages {
		cloned[index] = message
		if message.Content != nil {
			content := *message.Content
			cloned[index].Content = &content
		}
		if message.ToolCalls != nil {
			cloned[index].ToolCalls = make([]openrouter.PriorToolCall, len(message.ToolCalls))
			for callIndex, call := range message.ToolCalls {
				cloned[index].ToolCalls[callIndex] = call
				cloned[index].ToolCalls[callIndex].RawArguments = append([]byte(nil), call.RawArguments...)
			}
		}
	}
	return cloned
}

func readBoundedResponse(reader io.Reader, limit int) ([]byte, error) {
	if limit <= 0 || limit > openrouter.MaxResponseBytes {
		return nil, errors.New("provider response byte budget is invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return raw, err
	}
	if len(raw) == 0 {
		return raw, errEmptyResponse
	}
	if len(raw) > limit {
		return raw, errors.New("provider response byte length out of bounds")
	}
	return raw, nil
}

var errEmptyResponse = errors.New("provider response byte length out of bounds: empty")

func (b *Broker) failLocked(recordIndex int, err error) error {
	b.closed = true
	if recordIndex >= 0 && recordIndex < len(b.records) {
		b.records[recordIndex].failure = err.Error()
	}
	return err
}

func closedDecision() correlation.Decision {
	return correlation.Decision{
		Phase: correlation.PhaseProposal, ReasonCode: correlation.ReasonSessionClosed, StreamClosed: true,
	}
}

func cloneRequestRecord(record RequestRecord) RequestRecord {
	copy := record
	copy.header = record.header.Clone()
	copy.body = append([]byte(nil), record.body...)
	return copy
}

func cloneExchangeRecord(record ExchangeRecord) ExchangeRecord {
	copy := record
	copy.request = cloneRequestRecord(record.request)
	copy.responseBody = append([]byte(nil), record.responseBody...)
	return copy
}

func validText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
