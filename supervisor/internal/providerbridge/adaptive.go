package providerbridge

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

// The stops of an adaptive replay, as the receipt names them. The attacker machine's own stop reasons map
// onto them: budget_exhausted is "cap", library_exhausted is "exhausted", invalid is "invalid". A pivot the
// case has no turn for also ends the run as "exhausted". "success_pivot" is the first pivot whose Result
// is not a denial: whether it manifested the effect is the oracle's verdict, never the source's.
const (
	AdaptiveStopSuccessPivot = "success_pivot"
	AdaptiveStopCap          = "cap"
	AdaptiveStopExhausted    = "exhausted"
	AdaptiveStopInvalid      = "invalid"
	// AdaptiveStopOpen means the conversation ended before the source decided to stop (a host budget, a
	// deadline or a failure ended the run first).
	AdaptiveStopOpen = "open"
)

// AdaptiveFinalText is the assistant text that ends an adaptive replay.
const AdaptiveFinalText = "Adaptive replay complete."

// AdaptiveTerminal is what an adaptive replay reports at its end.
type AdaptiveTerminal struct {
	Stop         string
	PivotsIssued int
	// StateDigest is the attacker machine's state-history digest, lower-case hex, empty before the first
	// pivot decision.
	StateDigest string
}

// AdaptiveSource is the ScriptSource of the adaptive replay roles (C and P run the identical machine). It
// holds the attacker Machine built from the pinned artifact and the case's pivot table. Its only input
// from the run is the verdict and reason code of the last tool Result in the request history:
//
//   - a DENY Result is projected through attacker.NewFeedback(policy, reason, PublicCapabilityText(reason))
//     and fed to Machine.Next, which selects the next library step; the case's pivot table turns that step
//     into one concrete tool call;
//   - any other verdict ends the run at the first non-denied pivot (AdaptiveStopSuccessPivot);
//   - tool output, the arguments, ids, timing and every other field of the Result or the history never
//     reach the machine.
//
// The machine starts from its initial state (Start), not from a static prefix: the specification has each
// iteration consume the initial state or the previous projected feedback, and the static witness is the
// separate static replay. Usage is charged as one tool call per pivot only; wall time, tokens and money
// would make the state digest depend on the clock, and the host budgets bound them instead.
type AdaptiveSource struct {
	mu      sync.Mutex
	machine attacker.Machine
	policy  attacker.FeedbackPolicy
	pivots  map[string]ScriptTurn

	state   attacker.State
	started bool
	issued  int
	stop    string // empty while the replay is active
	done    bool   // the final text turn was served

	last      ScriptTurn
	lastTools int
	haveLast  bool
}

// NewAdaptiveSource builds the source from a validated attacker artifact and the case's pivots. Every
// pivot must be a tool call; the source owns copies.
func NewAdaptiveSource(spec attacker.Spec, pivots map[string]ScriptTurn) (*AdaptiveSource, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	machine, err := spec.Machine()
	if err != nil {
		return nil, err
	}
	if len(pivots) == 0 {
		return nil, errors.New("adaptive replay requires at least one pivot")
	}
	copied := make(map[string]ScriptTurn, len(pivots))
	for id, turn := range pivots {
		if turn.Tool == "" || len(turn.Arguments) == 0 || turn.Text != "" {
			return nil, fmt.Errorf("adaptive pivot %q is not a tool call", id)
		}
		turn.ResponseID, turn.CallID = "", ""
		turn.Arguments = append(json.RawMessage(nil), turn.Arguments...)
		copied[id] = turn
	}
	return &AdaptiveSource{machine: machine, policy: spec.Policy(), pivots: copied}, nil
}

// Terminal reports the replay's stop, pivot count and state digest. It is final once the source served its
// text turn; before that the stop is AdaptiveStopOpen.
func (s *AdaptiveSource) Terminal() AdaptiveTerminal {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := AdaptiveTerminal{Stop: s.stop, PivotsIssued: s.issued}
	if t.Stop == "" {
		t.Stop = AdaptiveStopOpen
	}
	if s.started {
		digest := s.state.Digest()
		t.StateDigest = hex.EncodeToString(digest[:])
	}
	return t
}

func adaptiveCallID(n int) string { return fmt.Sprintf("adaptive-call-%d", n) }

// Next implements ScriptSource.
func (s *AdaptiveSource) Next(history []ScriptMessage) (ScriptTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tools []ScriptMessage
	for _, message := range history {
		if message.Role == "tool" {
			tools = append(tools, message)
		}
	}
	// The same request again (a provider retry) is answered with the same turn and moves nothing.
	if s.haveLast && len(tools) == s.lastTools {
		return s.copyLast(), nil
	}
	if s.done {
		return ScriptTurn{}, ErrScriptExhausted
	}
	if !s.started {
		if len(tools) != 0 {
			return ScriptTurn{}, errors.New("adaptive replay: history holds tool results before the first pivot")
		}
		s.started = true
		var action attacker.Action
		s.state, action = s.machine.Start()
		return s.serve(action, len(tools))
	}
	if len(tools) != s.issued {
		return ScriptTurn{}, fmt.Errorf("adaptive replay: history holds %d tool results for %d pivots", len(tools), s.issued)
	}
	result, err := lastResult(tools[len(tools)-1], adaptiveCallID(s.issued))
	if err != nil {
		return ScriptTurn{}, err
	}
	s.state = s.machine.Charge(s.state, attacker.Usage{ToolCalls: 1})
	switch result.Verdict {
	case string(gate.Allow):
		return s.finish(AdaptiveStopSuccessPivot, len(tools)), nil
	case string(gate.Deny):
	default:
		return ScriptTurn{}, errors.New("adaptive replay: the tool Result has no ALLOW or DENY verdict")
	}
	text, known := attacker.PublicCapabilityText(result.ReasonCode)
	if !known {
		return s.finish(AdaptiveStopInvalid, len(tools)), nil
	}
	feedback, err := attacker.NewFeedback(s.policy, result.ReasonCode, text)
	if err != nil {
		return s.finish(AdaptiveStopInvalid, len(tools)), nil
	}
	var action attacker.Action
	s.state, action = s.machine.Next(s.state, feedback)
	return s.serve(action, len(tools))
}

// serve turns a machine action into the next provider turn.
func (s *AdaptiveSource) serve(action attacker.Action, tools int) (ScriptTurn, error) {
	switch action.Kind {
	case attacker.ActionPivot:
		turn, ok := s.pivots[action.StepID]
		if !ok {
			return s.finish(AdaptiveStopExhausted, tools), nil
		}
		s.issued++
		turn.Arguments = append(json.RawMessage(nil), turn.Arguments...)
		turn.CallID = adaptiveCallID(s.issued)
		turn.ResponseID = fmt.Sprintf("adaptive-response-%d", s.issued)
		s.remember(turn, tools)
		return s.copyLast(), nil
	case attacker.ActionStop:
		switch action.Stop {
		case attacker.StopBudgetExhausted:
			return s.finish(AdaptiveStopCap, tools), nil
		case attacker.StopLibraryExhausted:
			return s.finish(AdaptiveStopExhausted, tools), nil
		}
	}
	return s.finish(AdaptiveStopInvalid, tools), nil
}

func (s *AdaptiveSource) finish(stop string, tools int) ScriptTurn {
	s.stop, s.done = stop, true
	s.remember(ScriptTurn{ResponseID: fmt.Sprintf("adaptive-response-%d", s.issued+1), Text: AdaptiveFinalText}, tools)
	return s.copyLast()
}

func (s *AdaptiveSource) remember(turn ScriptTurn, tools int) {
	s.last, s.lastTools, s.haveLast = turn, tools, true
}

func (s *AdaptiveSource) copyLast() ScriptTurn {
	turn := s.last
	turn.Arguments = append(json.RawMessage(nil), s.last.Arguments...)
	return turn
}

// toolResult is the part of protocol.Result the source reads: the verdict and the reason code. The
// output, the arguments digest and the policy digest are not decoded.
type toolResult struct {
	SchemaVersion string `json:"schema_version"`
	ToolCallID    string `json:"tool_call_id"`
	Verdict       string `json:"verdict"`
	ReasonCode    string `json:"reason_code"`
}

func lastResult(message ScriptMessage, wantCallID string) (toolResult, error) {
	var result toolResult
	if message.ToolCallID != wantCallID {
		return result, fmt.Errorf("adaptive replay: the last tool message answers %q, not the last pivot %q", message.ToolCallID, wantCallID)
	}
	if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
		return result, fmt.Errorf("adaptive replay: the last tool message is not a protocol Result: %w", err)
	}
	if result.SchemaVersion != protocol.ResultSchemaVersion || result.ToolCallID != wantCallID {
		return result, errors.New("adaptive replay: the last tool message is not the Result of the last pivot")
	}
	return result, nil
}
