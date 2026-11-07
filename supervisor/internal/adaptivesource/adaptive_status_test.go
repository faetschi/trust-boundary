package adaptivesource

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/providerbridge"
)

func statusMessage(t *testing.T, n int, status string, output string) providerbridge.ScriptMessage {
	t.Helper()
	result := protocol.Result{SchemaVersion: protocol.ResultSchemaVersion, ToolCallID: adaptiveCallID(n), Tool: "bash",
		Sequence: uint64(n), Verdict: "ALLOW", ReasonCode: "policy_rule_allow", Status: status, PolicyDigest: "sha256:" + strings.Repeat("0", 64)}
	if output != "" {
		result.Output = json.RawMessage(output)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return providerbridge.ScriptMessage{Role: "tool", ToolCallID: adaptiveCallID(n), Content: string(encoded)}
}

// An ALLOW Result whose command failed is not a success pivot: the source feeds the machine the public
// command_failed reason with its generic capability text and continues; the first ALLOW without a failure ends
// the replay. The output never reaches the machine, so two runs with the same statuses are the same run.
func TestAdaptiveSourceContinuesOnAFailedCommandAndStopsOnTheFirstSuccess(t *testing.T) {
	spec := adaptiveSpec(6)
	run := func(outputs func(n int) string) ([]string, AdaptiveTerminal) {
		source, err := newTestSource(spec, adaptivePivots(spec))
		if err != nil {
			t.Fatal(err)
		}
		history := []providerbridge.ScriptMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
		var steps []string
		for n := 1; n <= 3; n++ {
			turn, err := source.Next(append([]providerbridge.ScriptMessage(nil), history...))
			if err != nil || turn.Tool == "" {
				t.Fatalf("pivot %d: %+v %v", n, turn, err)
			}
			steps = append(steps, stepOf(t, turn))
			status := protocol.ResultStatusCommandFailed
			if n == 3 {
				status = ""
			}
			history = append(history, providerbridge.ScriptMessage{Role: "assistant", Content: "call"}, statusMessage(t, n, status, outputs(n)))
		}
		final, err := source.Next(append([]providerbridge.ScriptMessage(nil), history...))
		if err != nil || final.Text != AdaptiveFinalText {
			t.Fatalf("final = %+v %v", final, err)
		}
		return steps, source.Terminal()
	}
	steps, terminal := run(func(int) string { return `{"command":{"exit_code":2}}` })
	if terminal.Stop != AdaptiveStopSuccessPivot || terminal.PivotsIssued != 3 {
		t.Fatalf("terminal = %+v after %v", terminal, steps)
	}
	// the expected steps are the machine driven directly on command_failed feedback
	text, known := attacker.PublicCapabilityText(attacker.ReasonCommandFailed)
	if !known {
		t.Fatal("command_failed has no public capability text")
	}
	feedback, err := attacker.NewFeedback(spec.Policy(), attacker.ReasonCommandFailed, text)
	if err != nil {
		t.Fatal(err)
	}
	machine, _ := spec.Machine()
	state, action := machine.Start()
	var want []string
	for i := 0; i < 2; i++ {
		want = append(want, action.StepID)
		state = machine.Charge(state, attacker.Usage{ToolCalls: 1})
		state, action = machine.Next(state, feedback)
	}
	want = append(want, action.StepID)
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("steps %v, machine %v", steps, want)
	}
	// other output, same statuses: the same run
	steps2, terminal2 := run(func(n int) string { return fmt.Sprintf(`{"content":"secret %d"}`, n) })
	if strings.Join(steps2, ",") != strings.Join(steps, ",") || terminal2.StateDigest != terminal.StateDigest {
		t.Fatalf("output changed the run: %v %+v vs %v %+v", steps2, terminal2, steps, terminal)
	}
	// The released stream projection is output too: a stdout that names the secret changes nothing.
	steps3, terminal3 := run(func(n int) string {
		return fmt.Sprintf(`{"command":{"exit_code":2,"exit_observed":true,"stdout":"secret %d\n","stdout_bytes":9,"stdout_truncated":false,"stderr":"","stderr_bytes":0,"stderr_truncated":false}}`, n)
	})
	if strings.Join(steps3, ",") != strings.Join(steps, ",") || terminal3.StateDigest != terminal.StateDigest {
		t.Fatalf("released stdout changed the run: %v %+v vs %v %+v", steps3, terminal3, steps, terminal)
	}
}

func TestAdaptiveSourceRefusesAStatusItDoesNotKnow(t *testing.T) {
	spec := adaptiveSpec(3)
	source, _ := newTestSource(spec, adaptivePivots(spec))
	history := []providerbridge.ScriptMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
	if _, err := source.Next(history); err != nil {
		t.Fatal(err)
	}
	history = append(history, providerbridge.ScriptMessage{Role: "assistant", Content: "call"}, statusMessage(t, 1, "weird_failure", ""))
	turn, err := source.Next(history)
	if err != nil || turn.Text != AdaptiveFinalText || source.Terminal().Stop != AdaptiveStopInvalid {
		t.Fatalf("an unknown status must end the replay as invalid: %+v %v %+v", turn, err, source.Terminal())
	}
}

// A refused call is not an executed pivot either: the machine is shown the public invalid_arguments reason and
// pivots on. Only the first ALLOW without a status ends the replay as the success pivot.
func TestAdaptiveSourceFeedsANoEffectRefusalAsInvalidArgumentsAndContinues(t *testing.T) {
	spec := adaptiveSpec(6)
	source, err := newTestSource(spec, adaptivePivots(spec))
	if err != nil {
		t.Fatal(err)
	}
	refusal := `{"code":"invalid_arguments","message":"The tool arguments are not valid. Check the tool schema and send a corrected call."}`
	history := []providerbridge.ScriptMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
	var steps []string
	for n := 1; n <= 3; n++ {
		turn, err := source.Next(append([]providerbridge.ScriptMessage(nil), history...))
		if err != nil || turn.Tool == "" {
			t.Fatalf("pivot %d after a refusal: %+v %v, terminal %+v", n, turn, err, source.Terminal())
		}
		steps = append(steps, stepOf(t, turn))
		status, output := protocol.ResultStatusNoEffectRefused, refusal
		if n == 3 {
			status, output = "", ""
		}
		history = append(history, providerbridge.ScriptMessage{Role: "assistant", Content: "call"}, statusMessage(t, n, status, output))
	}
	final, err := source.Next(append([]providerbridge.ScriptMessage(nil), history...))
	if err != nil || final.Text != AdaptiveFinalText {
		t.Fatalf("final = %+v %v", final, err)
	}
	if terminal := source.Terminal(); terminal.Stop != AdaptiveStopSuccessPivot || terminal.PivotsIssued != 3 {
		t.Fatalf("terminal = %+v", terminal)
	}
	text, known := attacker.PublicCapabilityText("invalid_arguments")
	if !known {
		t.Fatal("invalid_arguments has no public capability text")
	}
	feedback, err := attacker.NewFeedback(spec.Policy(), "invalid_arguments", text)
	if err != nil {
		t.Fatal(err)
	}
	machine, _ := spec.Machine()
	state, action := machine.Start()
	var want []string
	for i := 0; i < 2; i++ {
		want = append(want, action.StepID)
		state = machine.Charge(state, attacker.Usage{ToolCalls: 1})
		state, action = machine.Next(state, feedback)
	}
	want = append(want, action.StepID)
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("steps %v, machine %v", steps, want)
	}
}
