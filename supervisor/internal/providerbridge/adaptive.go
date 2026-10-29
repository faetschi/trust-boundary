package providerbridge

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

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

// AdaptiveUsage is the spend of an adaptive replay against the cumulative caps of the attacker artifact:
// pivots issued (Iterations), tool calls and wall-clock milliseconds. Tokens and money are never spent by a
// scripted source.
type AdaptiveUsage struct {
	Iterations int64 `json:"iterations"`
	ToolCalls  int64 `json:"tool_calls"`
	WallMS     int64 `json:"wall_ms"`
}

// Add returns the sum of two usages.
func (u AdaptiveUsage) Add(other AdaptiveUsage) AdaptiveUsage {
	return AdaptiveUsage{Iterations: u.Iterations + other.Iterations, ToolCalls: u.ToolCalls + other.ToolCalls, WallMS: u.WallMS + other.WallMS}
}

// ErrAdaptiveBudgetExhausted means the usage already spent by the earlier attempts of the case leaves no
// positive budget in one of the capped dimensions: there is nothing left to run.
var ErrAdaptiveBudgetExhausted = errors.New("the cumulative adaptive budget of the case is exhausted")

// AdaptiveOption configures an AdaptiveSource.
type AdaptiveOption func(*AdaptiveSource)

// WithPriorUsage makes the source the continuation of earlier attempts of the same case and role: the
// machine is built with the artifact caps minus prior (cumulative budgets across retries, D32), and the
// source reports the cumulative usage. The machine itself restarts from its initial state: the retry
// reruns against a reset fixture, so the earlier attempt's tried steps do not carry over; only the budget does.
func WithPriorUsage(prior AdaptiveUsage) AdaptiveOption {
	return func(s *AdaptiveSource) { s.prior = prior }
}

// WithClock replaces the wall clock that measures the run (tests only).
func WithClock(now func() time.Time) AdaptiveOption {
	return func(s *AdaptiveSource) { s.now = now }
}

// AdaptiveTerminal is what an adaptive replay reports at its end.
type AdaptiveTerminal struct {
	Stop         string
	PivotsIssued int
	// StateDigest is the attacker machine's state-history digest, lower-case hex, empty before the first
	// pivot decision.
	StateDigest string
	// Usage is what this attempt spent; Cumulative adds the prior attempts' usage. Wall time runs from the
	// first provider request to the end of the replay (to now while it is open).
	Usage      AdaptiveUsage
	Cumulative AdaptiveUsage
}

// AdaptiveSource is the ScriptSource of the adaptive replay roles (C and P run the identical machine). It
// holds the attacker Machine built from the pinned artifact and the case's pivot table. Its only input
// from the run is the verdict and reason code of the last tool Result in the request history:
//
//   - a DENY Result is projected through attacker.NewFeedback(policy, reason, PublicCapabilityText(reason))
//     and fed to Machine.Next, which selects the next library step; the case's pivot table turns that step
//     into one concrete tool call;
//   - an ALLOW Result whose Status is command_failed (the trusted Result producer saw the settled command
//     fail) is projected the same way under the public reason command_failed: the pivot ran but did not
//     execute, so the replay goes on; an ALLOW Result whose Status is no_effect_refused is projected the
//     same way under the public reason invalid_arguments;
//   - any other ALLOW ends the run at the first non-denied, non-failed pivot (AdaptiveStopSuccessPivot);
//   - tool output, the arguments, ids, timing and every other field of the Result or the history never
//     reach the machine.
//
// The machine starts from its initial state (Start), not from a static prefix: the specification has each
// iteration consume the initial state or the previous projected feedback, and the static witness is the
// separate static replay. Usage is charged to the machine as one tool call per pivot only; wall time would
// make the state digest depend on the clock, so the source measures it itself: it stops the replay as
// "cap" before it issues a pivot once the cumulative wall time reaches the artifact's wall cap, and it
// reports the wall time in the terminal. Tokens and money are never spent by a scripted source.
// Retries (WithPriorUsage) share one cumulative budget: the machine gets the caps minus the usage of the
// earlier attempts.
type AdaptiveSource struct {
	mu      sync.Mutex
	machine attacker.Machine
	policy  attacker.FeedbackPolicy
	pivots  map[string]ScriptTurn

	prior   AdaptiveUsage
	wallCap int64 // the artifact's cumulative wall cap, milliseconds
	now     func() time.Time

	state   attacker.State
	started bool
	issued  int
	stop    string // empty while the replay is active
	done    bool   // the final text turn was served

	startedAt time.Time
	endedAt   time.Time

	last      ScriptTurn
	lastTools int
	haveLast  bool
}

// NewAdaptiveSource builds the source from a validated attacker artifact and the case's pivots. Every
// pivot must be a tool call; the source owns copies. With WithPriorUsage the machine's caps are the
// artifact's minus the prior usage, and a prior usage that leaves no positive cap is
// ErrAdaptiveBudgetExhausted.
func NewAdaptiveSource(spec attacker.Spec, pivots map[string]ScriptTurn, options ...AdaptiveOption) (*AdaptiveSource, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if !spec.Caps.Scripted {
		return nil, errors.New("adaptive replay requires scripted caps with zero token and money dimensions")
	}
	source := &AdaptiveSource{policy: spec.Policy(), wallCap: spec.Caps.WallMS, now: time.Now}
	for _, option := range options {
		option(source)
	}
	caps := spec.CapsValue()
	if source.prior.Iterations < 0 || source.prior.ToolCalls < 0 || source.prior.WallMS < 0 {
		return nil, errors.New("adaptive replay: the prior usage is negative")
	}
	caps.Iterations -= source.prior.Iterations
	caps.ToolCalls -= source.prior.ToolCalls
	caps.WallMS -= source.prior.WallMS
	if caps.Iterations <= 0 || caps.ToolCalls <= 0 || caps.WallMS <= 0 {
		return nil, ErrAdaptiveBudgetExhausted
	}
	machine, err := attacker.NewMachine(spec.LibraryValue(), []byte(spec.Seed), caps, spec.Policy())
	if err != nil {
		return nil, err
	}
	source.machine = machine
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
	source.pivots = copied
	return source, nil
}

// wallMS is the wall time of the replay so far in whole milliseconds: from the first provider request to the
// end of the replay, or to now while it is open. It is zero before the first request.
func (s *AdaptiveSource) wallMS() int64 {
	if !s.started {
		return 0
	}
	end := s.endedAt
	if end.IsZero() {
		end = s.now()
	}
	if elapsed := end.Sub(s.startedAt).Milliseconds(); elapsed > 0 {
		return elapsed
	}
	return 0
}

// Terminal reports the replay's stop, pivot count, state digest and usage. It is final once the source served
// its text turn; before that the stop is AdaptiveStopOpen and the wall time runs to now.
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
	t.Usage = AdaptiveUsage{Iterations: int64(s.issued), ToolCalls: int64(s.issued), WallMS: s.wallMS()}
	t.Cumulative = s.prior.Add(t.Usage)
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
		s.started, s.startedAt = true, s.now()
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
	// The reason the machine is shown: the denial's own reason code, or the public reason of an ALLOWed
	// call that failed or was refused before any effect. An ALLOW without a status is the success pivot.
	reason := result.ReasonCode
	switch result.Verdict {
	case string(gate.Allow):
		switch result.Status {
		case "":
			return s.finish(AdaptiveStopSuccessPivot, len(tools)), nil
		case protocol.ResultStatusCommandFailed:
			reason = attacker.ReasonCommandFailed
		case protocol.ResultStatusNoEffectRefused:
			reason = attacker.ReasonInvalidArguments
		default:
			return s.finish(AdaptiveStopInvalid, len(tools)), nil
		}
	case string(gate.Deny):
		if result.Status != "" {
			return s.finish(AdaptiveStopInvalid, len(tools)), nil
		}
	default:
		return ScriptTurn{}, errors.New("adaptive replay: the tool Result has no ALLOW or DENY verdict")
	}
	if s.wallMS()+s.prior.WallMS >= s.wallCap {
		return s.finish(AdaptiveStopCap, len(tools)), nil
	}
	text, known := attacker.PublicCapabilityText(reason)
	if !known {
		return s.finish(AdaptiveStopInvalid, len(tools)), nil
	}
	feedback, err := attacker.NewFeedback(s.policy, reason, text)
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
	if s.started && s.endedAt.IsZero() {
		s.endedAt = s.now()
	}
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

// toolResult is the part of protocol.Result the source reads: the verdict, the reason code and the status. The
// output, the arguments digest and the policy digest are not decoded.
type toolResult struct {
	SchemaVersion string `json:"schema_version"`
	ToolCallID    string `json:"tool_call_id"`
	Verdict       string `json:"verdict"`
	ReasonCode    string `json:"reason_code"`
	Status        string `json:"status"`
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
