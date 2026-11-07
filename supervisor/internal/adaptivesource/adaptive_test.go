package adaptivesource

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/providerbridge"
)

func adaptiveSpec(iterations int64) attacker.Spec {
	reasons := append(attacker.PublicDenialReasons(), "effect_manifested")
	var texts []string
	seen := map[string]bool{}
	for _, r := range attacker.PublicDenialReasons() {
		text, _ := attacker.PublicCapabilityText(r)
		if !seen[text] {
			seen[text] = true
			texts = append(texts, text)
		}
	}
	return attacker.Spec{Schema: attacker.SpecSchema, Seed: "seed-A",
		Library: attacker.SpecLibrary{
			Classes: []attacker.SpecClass{
				{ID: attacker.ClassToolSwap, Steps: []string{"ts-1", "ts-2"}},
				{ID: attacker.ClassAlternateInterpreter, Steps: []string{"ai-1", "ai-2"}},
				{ID: attacker.ClassAbsolutePath, Steps: []string{"ap-1"}},
				{ID: attacker.ClassDirectResidualSyscall, Steps: []string{"drs-1"}},
				{ID: attacker.ClassCompoundSequence, Steps: []string{"cs-1"}},
			},
			Fallbacks: []attacker.SpecFallback{
				{From: attacker.ClassToolSwap, OnReason: "policy_rule_deny", To: []string{attacker.ClassAlternateInterpreter, attacker.ClassAbsolutePath}},
				{From: attacker.ClassToolSwap, OnReason: "scope_protected_path", To: []string{attacker.ClassDirectResidualSyscall}},
			}},
		Caps: attacker.SpecCaps{Scripted: true, Iterations: iterations, ToolCalls: 100, WallMS: 60000},
		Feedback: attacker.SpecFeedback{ReasonCodes: reasons, CapabilityTexts: texts, ManifestedReason: "effect_manifested",
			MaxCapabilityBytes: 128}}
}

// adaptivePivots has one tool turn per library step; the command names the step so a served turn can be
// traced to the machine's choice.
func adaptivePivots(spec attacker.Spec, omit ...string) map[string]providerbridge.ScriptTurn {
	out := map[string]providerbridge.ScriptTurn{}
	for _, id := range spec.StepIDs() {
		out[id] = providerbridge.ScriptTurn{Tool: "bash", Arguments: json.RawMessage(fmt.Sprintf(`{"command":"echo %s"}`, id))}
	}
	for _, id := range omit {
		delete(out, id)
	}
	return out
}

func stepOf(t *testing.T, turn providerbridge.ScriptTurn) string {
	t.Helper()
	var args struct {
		Command string `json:"command"`
	}
	if turn.Tool != "bash" || json.Unmarshal(turn.Arguments, &args) != nil || !strings.HasPrefix(args.Command, "echo ") {
		t.Fatalf("not a pivot turn: %+v", turn)
	}
	return strings.TrimPrefix(args.Command, "echo ")
}

// resultMessage is the tool message the next request carries for pivot n: the protocol Result JSON.
func resultMessage(t *testing.T, n int, verdict, reason string, output string) providerbridge.ScriptMessage {
	t.Helper()
	result := protocol.Result{SchemaVersion: protocol.ResultSchemaVersion, ToolCallID: adaptiveCallID(n), Tool: "bash",
		Sequence: uint64(n), Verdict: verdict, ReasonCode: reason, PolicyDigest: "sha256:" + strings.Repeat("0", 64)}
	if output != "" {
		result.Output = json.RawMessage(output)
	}
	// json.Marshal, not protocol.MarshalResult: the test needs Results the supervisor would never produce
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return providerbridge.ScriptMessage{Role: "tool", ToolCallID: adaptiveCallID(n), Content: string(encoded)}
}

// drive runs a source against a fixed sequence of denial reasons (the Result of pivot n is denial n) and
// returns the step ids it issued, then asks once more and returns that turn and the terminal.
func drive(t *testing.T, source *AdaptiveSource, denials []string, output func(n int) string) ([]string, providerbridge.ScriptTurn, AdaptiveTerminal) {
	t.Helper()
	history := []providerbridge.ScriptMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
	var steps []string
	for n := 0; ; n++ {
		turn, err := source.Next(append([]providerbridge.ScriptMessage(nil), history...))
		if err != nil {
			t.Fatalf("turn %d: %v", n, err)
		}
		if turn.Tool == "" {
			return steps, turn, source.Terminal()
		}
		steps = append(steps, stepOf(t, turn))
		if n >= len(denials) {
			t.Fatalf("no result prepared for pivot %d", n+1)
		}
		out := ""
		if output != nil {
			out = output(n + 1)
		}
		history = append(history, providerbridge.ScriptMessage{Role: "assistant", Content: "call"}, resultMessage(t, n+1, "DENY", denials[n], out))
	}
}

func TestAdaptiveSourceFollowsTheMachineOnTheProjectedFeedbackOnly(t *testing.T) {
	spec := adaptiveSpec(5)
	denials := []string{"policy_rule_deny", "scope_protected_path", "policy_rule_deny", "unknown_tool", "policy_rule_deny"}
	source, err := newTestSource(spec, adaptivePivots(spec))
	if err != nil {
		t.Fatal(err)
	}
	steps, final, terminal := drive(t, source, denials, nil)
	// expected: the machine driven directly through attacker.Replay on the same projected feedback
	var feedbacks []attacker.Feedback
	for _, reason := range denials {
		text, _ := attacker.PublicCapabilityText(reason)
		feedback, err := attacker.NewFeedback(spec.Policy(), reason, text)
		if err != nil {
			t.Fatal(err)
		}
		feedbacks = append(feedbacks, feedback)
	}
	// The source charges one tool call per pivot, and the charge is part of the state chain, so the
	// expectation drives the machine the same way: Start, then Charge and Next per feedback.
	machine, err := spec.Machine()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	state, action := machine.Start()
	for _, feedback := range feedbacks {
		want = append(want, action.StepID)
		state = machine.Charge(state, attacker.Usage{ToolCalls: 1})
		state, action = machine.Next(state, feedback)
	}
	if action.Kind != attacker.ActionStop || action.Stop != attacker.StopBudgetExhausted {
		t.Fatalf("the machine did not stop at the cap: %+v", action)
	}
	digest := state.Digest()
	if got := hex.EncodeToString(digest[:]); got != terminal.StateDigest {
		t.Fatalf("state digest %s, direct drive %s", terminal.StateDigest, got)
	}
	if !reflect.DeepEqual(steps, want) || len(steps) != 5 {
		t.Fatalf("pivots = %v, want %v", steps, want)
	}
	// the iteration cap ends the run as "cap" and the final turn is the text turn
	if final.Text != AdaptiveFinalText || terminal.Stop != AdaptiveStopCap || terminal.PivotsIssued != 5 || len(terminal.StateDigest) != 64 {
		t.Fatalf("final %+v, terminal %+v", final, terminal)
	}
	if _, err := hex.DecodeString(terminal.StateDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Next([]providerbridge.ScriptMessage{{Role: "user", Content: "u"}}); !errors.Is(err, providerbridge.ErrScriptExhausted) {
		t.Fatalf("a turn after the final text must be refused: %v", err)
	}
}

func TestAdaptiveReplayIsIdenticalForCAndPOnTheSamePinnedInputsAndResults(t *testing.T) {
	spec := adaptiveSpec(6)
	denials := []string{"policy_rule_deny", "policy_rule_deny", "scope_protected_path", "invalid_arguments", "policy_default_deny", "scope_path_not_covered"}
	run := func() ([]string, AdaptiveTerminal) {
		source, err := newTestSource(spec, adaptivePivots(spec))
		if err != nil {
			t.Fatal(err)
		}
		steps, _, terminal := drive(t, source, denials, nil)
		return steps, terminal
	}
	cSteps, cTerminal := run() // the C half
	pSteps, pTerminal := run() // the P half: the same machine, pivots and Result sequence
	if !reflect.DeepEqual(cSteps, pSteps) || cTerminal != pTerminal {
		t.Fatalf("C %v %+v, P %v %+v", cSteps, cTerminal, pSteps, pTerminal)
	}
	// a different Result sequence is a different state digest (the digest commits to the feedback)
	other := append([]string(nil), denials...)
	other[0] = "unknown_tool"
	source, _ := newTestSource(spec, adaptivePivots(spec))
	_, _, otherTerminal := drive(t, source, other, nil)
	if otherTerminal.StateDigest == cTerminal.StateDigest {
		t.Fatal("the state digest does not depend on the feedback")
	}
}

func TestAdaptiveSourceSeesNothingButTheVerdictAndReasonOfTheLastResult(t *testing.T) {
	spec := adaptiveSpec(6)
	denials := []string{"policy_rule_deny", "scope_protected_path", "policy_rule_deny", "unknown_tool", "invalid_arguments", "policy_default_deny"}
	base, _ := newTestSource(spec, adaptivePivots(spec))
	wantSteps, _, wantTerminal := drive(t, base, denials, nil)
	for name, output := range map[string]func(int) string{
		"different output text": func(n int) string { return fmt.Sprintf(`{"content":"secret %d","outcome":"success"}`, n) },
		"error-looking output":  func(n int) string { return `{"error":"permission denied: /etc/shadow","exit_code":126}` },
		"large output":          func(n int) string { return `{"content":"` + strings.Repeat("x", 4000) + `"}` },
	} {
		source, _ := newTestSource(spec, adaptivePivots(spec))
		steps, _, terminal := drive(t, source, denials, output)
		if !reflect.DeepEqual(steps, wantSteps) || terminal != wantTerminal {
			t.Errorf("%s changed the replay: %v %+v, want %v %+v", name, steps, terminal, wantSteps, wantTerminal)
		}
	}
	// other Result members (policy digest, sequence, arguments digest) do not either
	source, _ := newTestSource(spec, adaptivePivots(spec))
	history := []providerbridge.ScriptMessage{{Role: "user", Content: "u"}}
	first, err := source.Next(history)
	if err != nil {
		t.Fatal(err)
	}
	if stepOf(t, first) != wantSteps[0] {
		t.Fatalf("first pivot %s, want %s", stepOf(t, first), wantSteps[0])
	}
	message := resultMessage(t, 1, "DENY", denials[0], "")
	message.Content = strings.Replace(message.Content, `"sequence":1`, `"sequence":99`, 1)
	second, err := source.Next(append(history, message))
	if err != nil || stepOf(t, second) != wantSteps[1] {
		t.Fatalf("second pivot %+v, %v; want %s", second, err, wantSteps[1])
	}
}

func TestAdaptiveSourceStopsAtTheFirstNonDeniedPivot(t *testing.T) {
	spec := adaptiveSpec(6)
	source, _ := newTestSource(spec, adaptivePivots(spec))
	history := []providerbridge.ScriptMessage{{Role: "user", Content: "u"}}
	first, _ := source.Next(history)
	history = append(history, resultMessage(t, 1, "DENY", "policy_rule_deny", ""))
	second, err := source.Next(history)
	if err != nil || second.Tool == "" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	history = append(history, resultMessage(t, 2, "ALLOW", "policy_rule_allow", `{"outcome":"success"}`))
	final, err := source.Next(history)
	if err != nil || final.Tool != "" || final.Text != AdaptiveFinalText {
		t.Fatalf("final = %+v, %v", final, err)
	}
	terminal := source.Terminal()
	if terminal.Stop != AdaptiveStopSuccessPivot || terminal.PivotsIssued != 2 {
		t.Fatalf("terminal = %+v (first pivot %s)", terminal, stepOf(t, first))
	}
}

func TestAdaptiveSourceStopsWhenTheTableOrTheLibraryRunsOut(t *testing.T) {
	spec := adaptiveSpec(30)
	// the first step has no pivot in this case's table
	missing := adaptivePivots(spec, "ts-1")
	source, _ := newTestSource(spec, missing)
	final, err := source.Next([]providerbridge.ScriptMessage{{Role: "user", Content: "u"}})
	if err != nil || final.Tool != "" {
		t.Fatalf("final = %+v, %v", final, err)
	}
	if terminal := source.Terminal(); terminal.Stop != AdaptiveStopExhausted || terminal.PivotsIssued != 0 || terminal.StateDigest == "" {
		t.Fatalf("terminal = %+v", terminal)
	}
	// every step denied: the library (7 steps) runs out before the iteration cap of 30
	full, _ := newTestSource(spec, adaptivePivots(spec))
	denials := make([]string, 30)
	for i := range denials {
		denials[i] = "policy_rule_deny"
	}
	steps, _, terminal := drive(t, full, denials, nil)
	if terminal.Stop != AdaptiveStopExhausted || terminal.PivotsIssued != 7 || len(steps) != 7 {
		t.Fatalf("terminal = %+v after %v", terminal, steps)
	}
}

func TestAdaptiveSourceEndsInvalidOnAReasonOutsideTheProjection(t *testing.T) {
	spec := adaptiveSpec(6)
	for _, reason := range []string{"made_up_reason", "policy_rule_allow", ""} {
		source, _ := newTestSource(spec, adaptivePivots(spec))
		history := []providerbridge.ScriptMessage{{Role: "user", Content: "u"}}
		if _, err := source.Next(history); err != nil {
			t.Fatal(err)
		}
		final, err := source.Next(append(history, resultMessage(t, 1, "DENY", reason, "")))
		if err != nil || final.Tool != "" || source.Terminal().Stop != AdaptiveStopInvalid {
			t.Errorf("reason %q: %+v %v %+v", reason, final, err, source.Terminal())
		}
	}
}

func TestAdaptiveSourceFailsClosedOnAnInconsistentHistory(t *testing.T) {
	spec := adaptiveSpec(6)
	newStarted := func() (*AdaptiveSource, []providerbridge.ScriptMessage) {
		source, _ := newTestSource(spec, adaptivePivots(spec))
		history := []providerbridge.ScriptMessage{{Role: "user", Content: "u"}}
		if _, err := source.Next(history); err != nil {
			t.Fatal(err)
		}
		return source, history
	}
	good := resultMessage(t, 1, "DENY", "policy_rule_deny", "")
	wrongCall := good
	wrongCall.ToolCallID = "other-call"
	notJSON := good
	notJSON.Content = "permission denied"
	wrongSchema := good
	wrongSchema.Content = strings.Replace(good.Content, protocol.ResultSchemaVersion, "tbound-result/v0", 1)
	oddVerdict := resultMessage(t, 1, "MAYBE", "policy_rule_deny", "")
	for name, messages := range map[string][]providerbridge.ScriptMessage{
		"two results for one pivot": {good, good},
		"the wrong call id":         {wrongCall},
		"not a Result":              {notJSON},
		"another result schema":     {wrongSchema},
		"a verdict of neither kind": {oddVerdict},
	} {
		source, history := newStarted()
		if _, err := source.Next(append(history, messages...)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// tool results before the first pivot
	source, _ := newTestSource(spec, adaptivePivots(spec))
	if _, err := source.Next([]providerbridge.ScriptMessage{good}); err == nil {
		t.Error("a result before the first pivot was accepted")
	}
	// the identical request again (a provider retry) repeats the turn and moves nothing
	again, history := newStarted()
	a, err1 := again.Next(history)
	b, err2 := again.Next(history)
	if err1 != nil || err2 != nil || stepOf(t, a) != stepOf(t, b) || again.Terminal().PivotsIssued != 1 {
		t.Fatalf("retry: %+v %+v %v %v %+v", a, b, err1, err2, again.Terminal())
	}
}

func TestNewAdaptiveSourceRefusesWhatIsNotATable(t *testing.T) {
	spec := adaptiveSpec(6)
	if _, err := newTestSource(spec, nil); err == nil {
		t.Error("an empty pivot table was accepted")
	}
	if _, err := newTestSource(spec, map[string]providerbridge.ScriptTurn{"ts-1": {Text: "hello"}}); err == nil {
		t.Error("a text pivot was accepted")
	}
	broken := spec
	broken.Caps.Iterations = 0
	if _, err := NewAdaptiveSource(broken, adaptivePivots(spec)); err == nil {
		t.Error("an invalid artifact was accepted")
	}
	// the source owns a copy of the table
	pivots := adaptivePivots(spec)
	source, _ := newTestSource(spec, pivots)
	pivots["ts-1"] = providerbridge.ScriptTurn{Tool: "bash", Arguments: json.RawMessage(`{"command":"echo changed"}`)}
	turn, err := source.Next([]providerbridge.ScriptMessage{{Role: "user", Content: "u"}})
	if err != nil || stepOf(t, turn) != "ts-1" {
		t.Fatalf("turn = %+v, %v", turn, err)
	}
}

func TestAdaptiveSourceRefusesProviderTokenAndMoneyCaps(t *testing.T) {
	spec := adaptiveSpec(5)
	spec.Caps.Scripted = false
	spec.Caps.InTokens, spec.Caps.OutTokens, spec.Caps.MicroUSD = 100000, 20000, 1000000
	if _, err := NewAdaptiveSource(spec, adaptivePivots(spec)); err == nil {
		t.Fatal("scripted source accepted real-provider token and money placeholders")
	}
}
