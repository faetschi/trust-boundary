package providerbridge

import (
	"encoding/json"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

func bashOutput(exitCode int, observed bool) json.RawMessage {
	return mutationOutput("bash", "g1", "tree-g1", "t1", 1, "effect-1", &commandSummary{ExitCode: exitCode, ExitObserved: observed,
		CommandContainmentStatus: "established", CommandRunnerProfile: "d09"})
}

func allowDecisionFor(proposal protocol.Proposal) gate.Decision {
	return gate.Decision{Verdict: gate.Allow, ReasonCode: "policy_rule_allow", ToolCallID: proposal.ToolCallID, Tool: proposal.Tool,
		Sequence: 1, PolicyDigest: "sha256:" + strings.Repeat("0", 64)}
}

// A settled bash command with a nonzero exit stays ALLOW (the gate decided it) but the trusted Result producer
// projects the failure into the stable public status; the output never decides it for the source.
func TestResultForProjectsAFailedCommandIntoTheStatus(t *testing.T) {
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: "c1", Tool: "bash", Arguments: json.RawMessage(`{"command":"false"}`)}
	cases := map[string]struct {
		tool   string
		output json.RawMessage
		want   string
	}{
		"nonzero exit":        {"bash", bashOutput(3, true), protocol.ResultStatusCommandFailed},
		"exit not observed":   {"bash", bashOutput(0, false), protocol.ResultStatusCommandFailed},
		"zero exit":           {"bash", bashOutput(0, true), ""},
		"write":               {"write", mutationOutput("write", "g1", "tree-g1", "t1", 1, "e1", nil), ""},
		"unparsable output":   {"bash", json.RawMessage(`not json`), ""},
		"bash without result": {"bash", nil, ""},
	}
	for name, tc := range cases {
		p := proposal
		p.Tool = tc.tool
		if got := resultFor(p, allowDecisionFor(p), tc.output).Status; got != tc.want {
			t.Errorf("%s: status %q, want %q", name, got, tc.want)
		}
	}
	denied := admissionDeniedResult(proposal, allowDecisionFor(proposal), "scope_protected_path")
	if denied.Status != "" || denied.Verdict != string(gate.Deny) {
		t.Fatalf("a denial carries no status: %+v", denied)
	}
}

// A write or edit that the tool reports as failed never becomes a Result, so it can never read as a success
// pivot: the executor output is refused (the execution is withheld as UNKNOWN and the run ends open).
func TestAFailedWriteOrEditNeverBecomesAResult(t *testing.T) {
	for _, tool := range []string{"write", "edit", "bash"} {
		var command *commandSummary
		if tool == "bash" {
			command = &commandSummary{ExitCode: 0, ExitObserved: true, CommandContainmentStatus: "established", CommandRunnerProfile: "d09"}
		}
		output := mutationOutput(tool, "g1", "tree-g1", "t1", 1, "e1", command)
		if _, _, _, _, _, err := validateExecutorOutput(tool, output, "g0", "tree-g0"); err != nil {
			t.Fatalf("%s: a settled mutation was refused: %v", tool, err)
		}
		var object map[string]any
		if err := json.Unmarshal(output, &object); err != nil {
			t.Fatal(err)
		}
		object["outcome"] = "failure"
		failed, _ := json.Marshal(object)
		if _, _, _, _, _, err := validateExecutorOutput(tool, failed, "g0", "tree-g0"); err == nil {
			t.Errorf("%s: a mutation whose outcome is a failure was accepted as a Result", tool)
		}
	}
}
