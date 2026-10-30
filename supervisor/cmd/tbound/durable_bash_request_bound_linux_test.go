//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

// pilotRunToolResultCeiling is the largest number of tool results one pilot run can issue. The signed corpus-pilot
// config sets workflow_max_host_calls to 17 (infra/guest/serve-pi-service.corpus-pilot.json), the adversarial scripted
// budget is 17 host calls (corpus/pilot/build.py SCRIPTED), and each tool result consumes one host call.
const pilotRunToolResultCeiling = 17

// releasedBashResultText is the model-facing text of one settled bash result, built the way the durable path builds
// it: the durable bash summary inside a protocol result, which becomes the tool message content.
func releasedBashResultText(t *testing.T, sequence uint64, callID string, arguments json.RawMessage, stream []byte) string {
	t.Helper()
	command := durableBashCommandSummary(sessionrepo.CommandResult{ExitCode: 0, ExitObserved: true,
		Stdout: stream, Stderr: stream}, durableBashRunnerLabel)
	output, err := json.Marshal(durableMutationSummaryPayload{
		Tool: "bash", Generation: durableGenerationSummary{ID: "g1", TreeDigest: "tree-g1"},
		Transition: durableTransitionSummary{ID: "transition-000001", Sequence: sequence},
		EffectID:   "sessionrepo-effect-request-bound", Outcome: "success", Command: &command,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := protocol.CanonicalArgumentsDigest("bash", arguments)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := protocol.MarshalResult(protocol.Result{
		SchemaVersion: protocol.ResultSchemaVersion,
		ResponseID:    &correlation.Identifier{Issuer: durableResponseIssuer, Opaque: "response-" + callID},
		ToolCallID:    callID, Tool: "bash", Sequence: sequence, CanonicalArgumentsDigest: digest,
		Verdict: "ALLOW", ReasonCode: "policy_rule_allow",
		PolicyDigest: "tbound-policy/v1:sha256:" + strings.Repeat("4", 64),
		Output:       output,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestBashCumulativeRequestFitsWithMarginAtThePilotToolResultCeiling measures the worst-case bash-stream contribution
// at the signed pilot ceiling of 17 host calls and asserts the modeled request stays within half the cap. Every tool
// result is a settled bash command whose two streams are full of control bytes, which expand the most. The modeled
// request holds those results, a prompt allowance, and the calls. It is not a guarantee that a whole request fits.
// Assistant text, original arguments, large reads, and write or edit arguments are not bounded by it.
func TestBashCumulativeRequestFitsWithMarginAtThePilotToolResultCeiling(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the durable bash summary is built for Linux only")
	}
	// The prompt allowance stands in for the system and developer prompts and the tool schemas.
	messages := providerbridge.InitialPromptMessages(openrouter.Route{},
		strings.Repeat("s", 48<<10), strings.Repeat("d", 16<<10))
	stream := bytes.Repeat([]byte{0x01}, 1<<20)
	arguments := json.RawMessage(`{"command":"node src/fn.test.js"}`)
	for index := 0; index < pilotRunToolResultCeiling; index++ {
		callID := fmt.Sprintf("call-request-bound-%d", index)
		messages = append(messages, openrouter.Message{Role: openrouter.Assistant,
			ToolCalls: []openrouter.PriorToolCall{{ID: callID, Name: "bash", RawArguments: arguments}}})
		text := releasedBashResultText(t, uint64(index+1), callID, arguments, stream)
		messages = append(messages, openrouter.Message{Role: openrouter.Tool, ToolCallID: callID, Content: &text})
	}
	request, err := openrouter.BuildRequestForRoute(openrouter.Route{}, messages, 0)
	if err != nil {
		t.Fatalf("the request of %d worst-case bash results was refused: %v", pilotRunToolResultCeiling, err)
	}
	size := len(request.Body())
	t.Logf("cumulative request at the pilot ceiling: %d of %d bytes", size, openrouter.MaxRequestBytes)
	if size > openrouter.MaxRequestBytes/2 {
		t.Fatalf("the request of %d worst-case bash results is %d bytes; want at most half of the %d byte cap",
			pilotRunToolResultCeiling, size, openrouter.MaxRequestBytes)
	}
}
