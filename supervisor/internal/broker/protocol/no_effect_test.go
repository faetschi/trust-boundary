package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The refusal bytes are the model-visible contract, so the test spells them out instead of reading the code's copy.
const wantNoEffectRefusal = `{"code":"invalid_arguments","message":"The tool arguments are not valid. Check the tool schema and send a corrected call."}`

func TestNoEffectResultStatusRoundTripsForEveryRegisteredCall(t *testing.T) {
	for _, tool := range []string{"read", "write", "edit", "bash"} {
		result := allowedBashResult()
		result.Tool, result.Status, result.Output = tool, ResultStatusNoEffectRefused, json.RawMessage(wantNoEffectRefusal)
		encoded, err := MarshalResult(result)
		if err != nil {
			t.Fatalf("%s: the fixed refusal was refused: %v", tool, err)
		}
		decoded, err := DecodeResult(encoded)
		if err != nil || decoded.Status != ResultStatusNoEffectRefused || decoded.Verdict != "ALLOW" || string(decoded.Output) != wantNoEffectRefusal {
			t.Fatalf("%s: decoded = %+v, %v", tool, decoded, err)
		}
	}
}

func TestNoEffectResultStatusIsRefusedWhereItHasNoMeaning(t *testing.T) {
	refusal := json.RawMessage(wantNoEffectRefusal)
	denied := allowedBashResult()
	denied.Verdict, denied.ReasonCode, denied.Status, denied.Output = "DENY", "policy_rule_deny", ResultStatusNoEffectRefused, refusal
	unregistered := allowedBashResult()
	unregistered.Tool, unregistered.Status, unregistered.Output = "grep", ResultStatusNoEffectRefused, refusal
	changed := allowedBashResult()
	changed.Status = ResultStatusNoEffectRefused
	changed.Output = json.RawMessage(`{"code":"invalid_arguments","message":"The path is outside the workspace."}`)
	extra := allowedBashResult()
	extra.Status = ResultStatusNoEffectRefused
	extra.Output = json.RawMessage(`{"code":"invalid_arguments","message":"The tool arguments are not valid. Check the tool schema and send a corrected call.","path":"secret.txt"}`)
	missing := allowedBashResult()
	missing.Status = ResultStatusNoEffectRefused
	for name, result := range map[string]Result{"deny": denied, "unregistered tool": unregistered, "changed output": changed, "extra output member": extra, "missing output": missing} {
		if _, err := MarshalResult(result); err == nil {
			t.Errorf("%s: a no-effect status was accepted", name)
		}
	}
}

func TestNoEffectResultBuildsTheFixedRefusalFromAnAllowedBaseOnly(t *testing.T) {
	base := allowedBashResult()
	result, err := NoEffectResult(base)
	if err != nil {
		t.Fatalf("an ALLOWed base was refused: %v", err)
	}
	if result.Status != ResultStatusNoEffectRefused || string(result.Output) != wantNoEffectRefusal || len(result.Output) != 123 || len(result.Output) > 256 {
		t.Fatalf("status %q, output %q (%d bytes)", result.Status, result.Output, len(result.Output))
	}
	if result.Verdict != base.Verdict || result.ReasonCode != base.ReasonCode || result.PolicyDigest != base.PolicyDigest ||
		result.ToolCallID != base.ToolCallID || result.Sequence != base.Sequence ||
		result.CanonicalArgumentsDigest != base.CanonicalArgumentsDigest || *result.ResponseID != *base.ResponseID {
		t.Fatalf("the refusal changed the gate's verdict or identity: %+v", result)
	}
	denied := base
	denied.Verdict, denied.ReasonCode = "DENY", "policy_rule_deny"
	if _, err := NoEffectResult(denied); err == nil {
		t.Fatal("a DENY base became a no-effect refusal")
	}
}

func TestNoEffectErrorIsFoundThroughWrapsAndKeepsItsCauseOutOfItsText(t *testing.T) {
	cause := errors.New("write content: member name /private/a.txt is not allowed")
	wrapped := fmt.Errorf("durable write: %w", &NoEffectError{Tool: "write", Err: cause})
	var marker *NoEffectError
	if !errors.As(wrapped, &marker) || marker.Tool != "write" {
		t.Fatalf("the marker was not found through a wrap: %v", wrapped)
	}
	if !errors.Is(wrapped, cause) {
		t.Fatal("the local cause is not kept in the unwrap chain")
	}
	if strings.Contains(marker.Error(), cause.Error()) {
		t.Fatalf("the marker text repeats the cause: %q", marker.Error())
	}
}
