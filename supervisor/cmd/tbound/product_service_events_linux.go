//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

// Bounds of the observation projection.
const (
	productEventMaxTextBytes = 16 << 10
	productEventMaxPaths     = 256
	// productEventPathsBudget bounds the worst-case encoded size of one review event's paths. Each byte of a
	// path may be escaped as \u00XX, so a long path with `<` can take six bytes per byte.
	productEventPathsBudget = 256 << 10
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
// turn_cancel_requested, then turn_terminal. Resolution and cancellation are withheld until finality,
// because their position relative to concurrent provider work and review mutations is not recorded.
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
	var captureRefusalReason string
	events := []productprotocol.Event{{Kind: productprotocol.EventTurnAdmitted, TurnID: turn.ID,
		Text: boundText(turn.Prompt, productEventMaxTextBytes)}}
	if turn.Link != nil {
		events = append(events, productprotocol.Event{Kind: productprotocol.EventTurnLinked, TurnID: turn.ID})
		trace, err := p.env.providerTrace(turn)
		if err != nil {
			return nil, fmt.Errorf("read the provider journal of %s: %w", turn.ID, err)
		}
		if len(trace.Records) > 0 && trace.Records[len(trace.Records)-1].Event.Kind == providerbridge.CaptureRefusalEvent {
			var refusal struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(trace.Records[len(trace.Records)-1].Event.Data, &refusal); err != nil {
				return nil, err
			}
			captureRefusalReason = refusal.Reason
		}
		provider, err := projectProviderEvents(turn.ID, trace.Records)
		if err != nil {
			return nil, fmt.Errorf("project the provider journal of %s: %w", turn.ID, err)
		}
		events = append(events, provider...)
	}
	if turn.CandidateDigest != "" && turn.OperatorResolution == nil {
		paths, err := p.candidatePaths(turn)
		if err != nil {
			return nil, err
		}
		events = append(events, reviewEvent(turn, paths))
	}
	if final && turn.Resolution != "" {
		events = append(events, productprotocol.Event{Kind: productprotocol.EventTurnResolution, TurnID: turn.ID,
			CandidateDigest: turn.CandidateDigest, Reason: turn.Resolution})
	}
	if final && turn.CancelRequested {
		events = append(events, productprotocol.Event{Kind: productprotocol.EventTurnCancelRequested, TurnID: turn.ID})
	}
	if terminal, ok := terminalEvent(turn); ok {
		if terminal.Outcome == productsession.OutcomeFailed && captureRefusalReason != "" {
			terminal.Reason = fmt.Sprintf("the provider returned a response TBound cannot use (%s); the turn was not applied", captureRefusalReason)
		}
		events = append(events, terminal)
	}
	if final {
		p.mu.Lock()
		p.settled[turn.ID] = events
		p.mu.Unlock()
	}
	return events, nil
}

// reviewEvent is the awaiting-review event of a candidate. Text carries the full count; Paths is bounded.
func reviewEvent(turn productsession.Turn, paths []string) productprotocol.Event {
	return productprotocol.Event{Kind: productprotocol.EventTurnAwaitingReview, TurnID: turn.ID,
		CandidateDigest: turn.CandidateDigest, Paths: boundPaths(paths), Text: fmt.Sprintf("%d changed paths", len(paths))}
}

// boundPaths keeps the leading paths, at most productEventMaxPaths, whose worst-case encoded size fits
// productEventPathsBudget. The caller sorts the paths, so the kept set is deterministic.
func boundPaths(paths []string) []string {
	size := 0
	for i, p := range paths {
		size += 6*len(p) + 3 // each byte escaped as \u00XX, then the quotes and the comma
		if i == productEventMaxPaths || size > productEventPathsBudget {
			return paths[:i]
		}
	}
	return paths
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

// candidatePaths projects net changes from the successful Store effects committed by the receipt.
// It never opens a writer or reconstructs a mutable Store. The sealed candidate never changes.
func (p *productEventProjector) candidatePaths(turn productsession.Turn) ([]string, error) {
	p.mu.Lock()
	cached, ok := p.paths[turn.ID]
	p.mu.Unlock()
	if ok {
		return cached, nil
	}
	trace, err := readProductJournalTrace(turn.Link.StoreJournalPath)
	if err != nil {
		return nil, err
	}
	receipt, err := decodeProductTurnReceipt(turn.Receipt)
	if err != nil {
		return nil, err
	}
	if uint64(len(trace.Records)) != receipt.StoreJournalCount || auditHeadHash(trace.Records) != receipt.StoreJournalHead {
		return nil, errProductTurnEvidence
	}
	changes := map[string]delta.ObjectChange{}
	for _, effect := range trace.Effects {
		if !strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			continue
		}
		if effect.Unresolved || effect.Outcome != "success" {
			return nil, errProductTurnEvidence
		}
		var intent struct {
			Request struct{ Tool string } `json:"request"`
		}
		if err := json.Unmarshal(effect.Intent, &intent); err != nil {
			return nil, err
		}
		switch intent.Request.Tool {
		case "write", "edit", "delete", "bash":
		default:
			continue
		}
		var result struct {
			Transition delta.Transition         `json:"transition"`
			Admission  struct{ Outcome string } `json:"admission"`
		}
		if err := json.Unmarshal(effect.Result, &result); err != nil {
			return nil, err
		}
		if result.Admission.Outcome == "deny" {
			continue
		}
		if result.Admission.Outcome != "allow" {
			return nil, errProductTurnEvidence
		}
		for _, change := range result.Transition.Changes {
			if previous, ok := changes[change.Path]; ok {
				change.Before = previous.Before
			}
			changes[change.Path] = change
		}
	}
	paths := make([]string, 0, len(changes))
	for path, change := range changes {
		if change.Before != change.After {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
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
