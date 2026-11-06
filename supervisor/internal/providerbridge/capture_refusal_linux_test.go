//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
)

const rejectedCaptureBody = `data: {"id":"parallel-response","model":"minimax/minimax-m2.5","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{},{}]},"finish_reason":null}]}

data: [DONE]

`

func TestCaptureRefusalProductGateAndReplay(t *testing.T) {
	for _, product := range []bool{false, true} {
		t.Run(map[bool]string{false: "research", true: "product"}[product], func(t *testing.T) {
			cfg := recoveryTestConfig()
			cfg.ProductCaptureRefusals = product
			hooks := &retryHooks{}
			hooks.apply(&cfg)
			journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "capture.audit"))
			doer := &fixtureDoer{responses: []fixtureResponse{{failStatus: 200, failBody: rejectedCaptureBody}}}
			conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
			if err != nil {
				t.Fatal(err)
			}
			if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
				t.Fatal(err)
			}
			_, err = conversation.NextTurn(context.Background())
			if err == nil || !strings.Contains(err.Error(), "multiple or malformed tool calls") {
				t.Fatalf("refusal: %v", err)
			}
			trace, err := journal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			last := trace.Records[len(trace.Records)-1]
			pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}
			if !product {
				if last.Event.Kind != "provider_exchange_unknown" || len(hooks.completions) != 0 {
					t.Fatalf("research behavior changed: %s %+v", last.Event.Kind, hooks.completions)
				}
				var evidence struct {
					Response []byte `json:"response"`
					Failure  string `json:"failure"`
				}
				if err := json.Unmarshal(last.Event.Data, &evidence); err != nil {
					t.Fatal(err)
				}
				if string(evidence.Response) != rejectedCaptureBody || evidence.Failure != "capture OpenRouter response: multiple or malformed tool calls" {
					t.Fatalf("legacy refusal evidence changed: %+v", evidence)
				}
				if _, err := ReplayTranscript(journal, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
					t.Fatalf("research UNKNOWN replay: %v", err)
				}
				return
			}
			if last.Event.Kind != CaptureRefusalEvent || len(hooks.completions) != 1 || hooks.completions[0].OutcomeDigest != last.Hash {
				t.Fatalf("completion lacks durable refusal: %s %+v", last.Event.Kind, hooks.completions)
			}
			projection, err := ReplayTranscript(journal, pins, nil)
			if err != nil || projection.Disposition != DispositionRetired || projection.HostCall == nil || *projection.HostCall != hooks.reservations[0] || projection.HostCallOutcomeDigest != last.Hash || projection.HostCallRetryableFailure || projection.CapturedCallCount != 0 {
				t.Fatalf("replay: %+v %v", projection, err)
			}
			pins.Config.ProductCaptureRefusals = false
			if _, err := ReplayTranscript(journal, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
				t.Fatalf("research accepted product evidence: %v", err)
			}
			pins.Config.ProductCaptureRefusals = true
			for _, mutation := range []string{"reason", "digest", "valid response", "host call", "request", "turn", "status", "event ID", "before reservation", "after refusal"} {
				t.Run(mutation, func(t *testing.T) {
					forged := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "forged.audit"))
					events := make([]audit.Event, len(trace.Records))
					for i, record := range trace.Records {
						events[i] = record.Event
					}
					var evidence captureRefusalEvidence
					if err := json.Unmarshal(last.Event.Data, &evidence); err != nil {
						t.Fatal(err)
					}
					switch mutation {
					case "reason":
						evidence.Reason = "other"
					case "digest":
						evidence.ResponseDigest = digestBytes([]byte("other"))
					case "valid response":
						evidence.Response = []byte(`data: {"id":"valid","model":"minimax/minimax-m2.5","created":1,"choices":[{"index":0,"delta":{"content":"valid"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n")
						evidence.ResponseDigest = digestBytes(evidence.Response)
						if reason := captureRefusalReason(evidence.Response); reason != "" {
							t.Fatalf("usable response fixture was rejected: %s", reason)
						}
					case "host call":
						evidence.HostCall.CallID = "other-call"
					case "request":
						evidence.Request.Body = []byte(`{}`)
					case "turn":
						evidence.Turn++
					case "status":
						evidence.StatusCode = 503
					case "event ID":
						events[len(events)-1].ID += "-forged"
					case "before reservation":
						events[1], events[2] = events[2], events[1]
					case "after refusal":
						events = append(events, events[0])
					}
					if mutation != "before reservation" {
						encoded, err := json.Marshal(evidence)
						if err != nil {
							t.Fatal(err)
						}
						events[len(trace.Records)-1].Data = encoded
					}
					for _, event := range events {
						if _, err := forged.Append(event); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := ReplayTranscript(forged, pins, nil); !errors.Is(err, ErrTranscriptReplay) {
						t.Fatalf("forged %s accepted: %v", mutation, err)
					}
				})
			}
		})
	}
}

func TestCaptureRefusalDoesNotCompleteBeforeDurableRecord(t *testing.T) {
	cfg := recoveryTestConfig()
	cfg.ProductCaptureRefusals = true
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	journal := &memoryJournal{failKind: CaptureRefusalEvent}
	conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{{failStatus: 200, failBody: rejectedCaptureBody}}}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err == nil || !strings.Contains(err.Error(), "journal sync failure") {
		t.Fatalf("failed sync: %v", err)
	}
	if len(hooks.completions) != 0 {
		t.Fatal("host call completed before refusal sync")
	}
}

func TestProductCaptureRefusalRequiresCompleteTransport(t *testing.T) {
	cfg := recoveryTestConfig()
	cfg.ProductCaptureRefusals = true
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "incomplete.audit"))
	conversation, err := newWithDoer(cfg, ApprovedModel, doerFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Request: r, Header: make(http.Header), Body: &failingBody{data: []byte(rejectedCaptureBody), err: io.ErrUnexpectedEOF}}, nil
	}), journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err == nil {
		t.Fatal("incomplete response succeeded")
	}
	kinds := journalKinds(t, journal)
	if kinds[len(kinds)-1] != "provider_exchange_unknown" || len(hooks.completions) != 0 {
		t.Fatalf("incomplete transport retired: %v", kinds)
	}
}

func TestProductCaptureRefusalReasonIsDeterministic(t *testing.T) {
	body := strings.Replace(rejectedCaptureBody, `"created":1`, `"extra_a":true,"extra_b":true,"created":1`, 1)
	cfg := recoveryTestConfig()
	cfg.ProductCaptureRefusals = true
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "unsupported.audit"))
	c, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{{failStatus: 200, failBody: body}}}, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AdmitPrompt("task", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextTurn(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid provider response") {
		t.Fatalf("normalized refusal: %v", err)
	}
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}
	for i := 0; i < 100; i++ {
		if _, err := ReplayTranscript(journal, pins, nil); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
}

// A journal written before f53fd29f holds a refusal whose body the live parser now accepts.
func TestProductCaptureRefusalReplaysLegacyNullRefusal(t *testing.T) {
	cfg := recoveryTestConfig()
	cfg.ProductCaptureRefusals = true
	hooks := &retryHooks{}
	hooks.apply(&cfg)
	source := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "source.audit"))
	c, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{{failStatus: 200, failBody: rejectedCaptureBody}}}, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AdmitPrompt("task", "prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextTurn(context.Background()); err == nil {
		t.Fatal("no refusal")
	}
	trace, err := source.Trace()
	if err != nil {
		t.Fatal(err)
	}
	last := trace.Records[len(trace.Records)-1]
	pins := RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true), RequireHostCallReservations: true}
	forge := func(t *testing.T, edit func(*captureRefusalEvidence)) error {
		var evidence captureRefusalEvidence
		if err := json.Unmarshal(last.Event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		evidence.Response = []byte(legacyNullRefusalBody)
		evidence.ResponseDigest = digestBytes(evidence.Response)
		evidence.Reason = "multiple or malformed tool calls"
		edit(&evidence)
		encoded, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		forged := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "forged.audit"))
		for i, record := range trace.Records {
			event := record.Event
			if i == len(trace.Records)-1 {
				event.Data = encoded
			}
			if _, err := forged.Append(event); err != nil {
				t.Fatal(err)
			}
		}
		_, err = ReplayTranscript(forged, pins, nil)
		return err
	}
	if err := forge(t, func(*captureRefusalEvidence) {}); err != nil {
		t.Fatalf("legacy refusal rejected: %v", err)
	}
	if err := forge(t, func(e *captureRefusalEvidence) { e.Reason = "invalid provider response" }); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("changed reason accepted: %v", err)
	}
	if err := forge(t, func(e *captureRefusalEvidence) {
		e.Response = []byte(strings.Replace(legacyNullRefusalBody, `"created":1`, `"created":1,"extra_a":true`, 1))
		e.ResponseDigest = digestBytes(e.Response)
	}); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("body now failing for another reason accepted: %v", err)
	}
	if err := forge(t, func(e *captureRefusalEvidence) { e.ResponseDigest = digestBytes([]byte("other")) }); !errors.Is(err, ErrTranscriptReplay) {
		t.Fatalf("changed digest accepted: %v", err)
	}
}
