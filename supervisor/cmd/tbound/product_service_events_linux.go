//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

// Bounds of the observation projection.
const (
	productEventMaxTextBytes = 16 << 10
	productEventMaxPaths     = 256
	// productObserveBudget keeps one observe response well inside productprotocol.MaxResponseBytes.
	productObserveBudget = 768 << 10
	// productViewPromptBytes bounds the prompt a session snapshot repeats; the full prompt is the
	// text of the turn's turn_admitted event.
	productViewPromptBytes = 1 << 10
)

// productEventProjector projects the deterministic event list of a session from durable records
// only: the catalog's turn records, and each linked turn's provider journal. It keeps no state that
// the records do not determine, except a cache of settled turns, whose records can no longer change.
//
// The list only ever grows at its end. A turn contributes, in this order: turn_admitted, turn_linked,
// the provider events in journal order, turn_awaiting_review, turn_resolution_requested,
// turn_cancel_requested, then turn_terminal. A cancel request of a turn that is still running is
// withheld until the turn leaves that state, because its position among the provider events that are
// still being written is not recorded anywhere.
type productEventProjector struct {
	env *productTurnEnv

	mu      sync.Mutex
	settled map[string][]productprotocol.Event // events of turns whose records are final
	paths   map[string][]string                // changed paths of a turn's candidate, by turn ID
}

func newProductEventProjector(env *productTurnEnv) *productEventProjector {
	return &productEventProjector{env: env, settled: map[string][]productprotocol.Event{}, paths: map[string][]string{}}
}

// session returns every event of the session in order, with Seq set to its index.
func (p *productEventProjector) session(sess productsession.Session) ([]productprotocol.Event, error) {
	var all []productprotocol.Event
	for _, id := range sess.Turns {
		turn, ok := p.env.Catalog.Turn(id)
		if !ok {
			return nil, fmt.Errorf("session %s names a turn that is not in the catalog", sess.ID)
		}
		events, err := p.turn(turn)
		if err != nil {
			return nil, err
		}
		all = append(all, events...)
	}
	for i := range all {
		all[i].Seq = uint64(i)
	}
	return all, nil
}

func (p *productEventProjector) turn(turn productsession.Turn) ([]productprotocol.Event, error) {
	final := turn.State == productsession.StateTerminal || turn.State == productsession.StateAbandoned ||
		turn.State == productsession.StateUnknown
	p.mu.Lock()
	cached, ok := p.settled[turn.ID]
	p.mu.Unlock()
	if ok {
		return cached, nil
	}
	events := []productprotocol.Event{{Kind: productprotocol.EventTurnAdmitted, TurnID: turn.ID,
		Text: boundText(turn.Prompt, productEventMaxTextBytes)}}
	if turn.Link != nil {
		events = append(events, productprotocol.Event{Kind: productprotocol.EventTurnLinked, TurnID: turn.ID})
		trace, err := p.env.providerTrace(turn)
		if err != nil {
			return nil, fmt.Errorf("read the provider journal of %s: %w", turn.ID, err)
		}
		provider, err := projectProviderEvents(turn.ID, trace.Records)
		if err != nil {
			return nil, fmt.Errorf("project the provider journal of %s: %w", turn.ID, err)
		}
		events = append(events, provider...)
	}
	complete := true
	if turn.CandidateDigest != "" {
		paths, err := p.candidatePaths(turn)
		complete = err == nil
		review := productprotocol.Event{Kind: productprotocol.EventTurnAwaitingReview, TurnID: turn.ID,
			CandidateDigest: turn.CandidateDigest, Paths: paths[:min(len(paths), productEventMaxPaths)]}
		if len(paths) != 0 {
			review.Text = fmt.Sprintf("%d changed paths", len(paths))
		}
		events = append(events, review)
	}
	if turn.Resolution != "" {
		events = append(events, productprotocol.Event{Kind: productprotocol.EventTurnResolution, TurnID: turn.ID,
			CandidateDigest: turn.CandidateDigest, Reason: turn.Resolution})
	}
	if turn.CancelRequested && turn.State != productsession.StateLinked {
		events = append(events, productprotocol.Event{Kind: productprotocol.EventTurnCancelRequested, TurnID: turn.ID})
	}
	if terminal, ok := terminalEvent(turn); ok {
		events = append(events, terminal)
	}
	if final && complete {
		p.mu.Lock()
		p.settled[turn.ID] = events
		p.mu.Unlock()
	}
	return events, nil
}

func terminalEvent(turn productsession.Turn) (productprotocol.Event, bool) {
	event := productprotocol.Event{Kind: productprotocol.EventTurnTerminal, TurnID: turn.ID}
	switch turn.State {
	case productsession.StateTerminal:
		event.Outcome, event.Reason = turn.Outcome, turn.TerminalReason
		if event.Reason == "" {
			event.Reason = string(turn.RefusalCode)
		}
	case productsession.StateAbandoned:
		event.Outcome = string(productsession.StateAbandoned)
	case productsession.StateUnknown:
		event.Outcome, event.Reason = string(productsession.StateUnknown), turn.UnknownReason
	default:
		return event, false
	}
	return event, true
}

// candidatePaths are the changed paths of a review turn's candidate: from the Store this process
// holds for the review, else from the retained Store. The result of a turn never changes, so it is cached.
func (p *productEventProjector) candidatePaths(turn productsession.Turn) ([]string, error) {
	p.mu.Lock()
	cached, ok := p.paths[turn.ID]
	p.mu.Unlock()
	if ok {
		return cached, nil
	}
	store, held := p.env.pendingReviewStore(turn.ID)
	if !held {
		link, err := productTurnPathsFromLink(*turn.Link)
		if err != nil {
			return nil, err
		}
		retained, err := openRetainedProductTurn(link, p.env.storeOptions(turn))
		if err != nil {
			return nil, err
		}
		defer retained.Close()
		store = retained.Store
	}
	_, _, changes, err := storeCandidateChanges(store)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(changes))
	for i, change := range changes {
		paths[i] = change.path
	}
	p.mu.Lock()
	p.paths[turn.ID] = paths
	p.mu.Unlock()
	return paths, nil
}

// projectProviderEvents turns the records of one turn's provider journal into events. Only records
// of completed exchanges, gate decisions and released results contribute; request records, and with
// them every raw request body, are never read.
func projectProviderEvents(turnID string, records []audit.Record) ([]productprotocol.Event, error) {
	var events []productprotocol.Event
	for _, record := range records {
		switch record.Event.Kind {
		case "provider_exchange":
			var exchange struct {
				Response []byte `json:"response"`
			}
			if err := json.Unmarshal(record.Event.Data, &exchange); err != nil {
				return nil, fmt.Errorf("record %d: %w", record.Sequence, err)
			}
			captured, err := openrouter.CaptureSSE(bytes.NewReader(exchange.Response))
			if err != nil {
				return nil, fmt.Errorf("record %d: %w", record.Sequence, err)
			}
			if text := captured.AssistantText(); text != "" {
				events = append(events, productprotocol.Event{Kind: productprotocol.EventAssistantMessage, TurnID: turnID,
					Text: boundText(text, productEventMaxTextBytes)})
			}
		case "provider_gate_decision":
			var decision gate.Decision
			if err := json.Unmarshal(record.Event.Data, &decision); err != nil {
				return nil, fmt.Errorf("record %d: %w", record.Sequence, err)
			}
			event := productprotocol.Event{Kind: productprotocol.EventToolCall, TurnID: turnID, Tool: decision.Tool,
				ToolCallID: decision.ToolCallID, Verdict: string(decision.Verdict), ReasonCode: decision.ReasonCode}
			if decision.Verdict == gate.Deny {
				event.Hint = staticDenialHint(decision.ReasonCode)
			}
			events = append(events, event)
		case "provider_tool_result", "provider_admission_denied":
			var evidence struct {
				ToolCallID string `json:"tool_call_id"`
				Tool       string `json:"tool"`
				Result     []byte `json:"result"`
			}
			var result protocol.Result
			if err := json.Unmarshal(record.Event.Data, &evidence); err != nil || json.Unmarshal(evidence.Result, &result) != nil {
				return nil, fmt.Errorf("record %d is not a tool result", record.Sequence)
			}
			event := productprotocol.Event{Kind: productprotocol.EventToolResult, TurnID: turnID, Tool: evidence.Tool,
				ToolCallID: evidence.ToolCallID, Verdict: result.Verdict, ReasonCode: result.ReasonCode, Text: summarizeToolResult(result)}
			if record.Event.Kind == "provider_admission_denied" {
				event.Hint = staticDenialHint(result.ReasonCode)
			}
			events = append(events, event)
		}
	}
	return events, nil
}

// staticDenialHint is the advisory hint text of a denial reason, from the table providerbridge owns.
func staticDenialHint(reason string) string { return providerbridge.HintCandidates(reason)["0"] }

// summarizeToolResult is a fixed-format summary. It never carries tool output or arguments.
func summarizeToolResult(result protocol.Result) string {
	switch {
	case result.Verdict == string(gate.Deny):
		return "denied"
	case result.Status == protocol.ResultStatusCommandFailed:
		return "command failed"
	case len(result.Output) != 0:
		return fmt.Sprintf("ok, %d bytes of output", len(result.Output))
	}
	return "ok"
}

// boundText cuts s to at most max bytes at a rune boundary and marks the cut.
func boundText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], "\n") + "\n[truncated]"
}
