//go:build linux

package main

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

// durableCaseVariantCase pairs a plain argument object, which must be accepted, with the same object plus a member
// whose name repeats a schema name, or differs from one by case or Unicode folding, written with or without JSON
// escapes. Go's decoder matches member names case-insensitively, so without an exact-name check the later member
// silently replaces the earlier one. The protocol capture refuses the variant, and each durable decoder must refuse
// it before any effect.
type durableCaseVariantCase struct {
	name, tool, plain, variant string
}

var durableCaseVariantCases = []durableCaseVariantCase{
	{"bash command", "bash", `{"command":"true"}`, `{"command":"true","Command":"printf unexpected"}`},
	{"read path", "read", `{"path":"task.txt"}`, `{"path":"task.txt","Path":"build/result.txt"}`},
	{"write content", "write", `{"path":"out.txt","content":"expected\n"}`,
		`{"path":"out.txt","content":"expected\n","Content":"unexpected\n"}`},
	{"edit replacements", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}],"Edits":[{"oldText":"baseline","newText":"unexpected"}]}`},
	{"edit nested new text", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited","NewText":"unexpected"}]}`},
	{"edit nested old text", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","OldText":"baseline","newText":"edited"}]}`},
	{"bash exact duplicate", "bash", `{"command":"true"}`, `{"command":"true","command":"printf unexpected"}`},
	{"bash escaped duplicate", "bash", `{"command":"true"}`, `{"command":"true","command":"printf unexpected"}`},
	{"bash escaped case variant", "bash", `{"command":"true"}`, `{"command":"true","Command":"printf unexpected"}`},
	{"read exact duplicate", "read", `{"path":"task.txt"}`, `{"path":"task.txt","path":"build/result.txt"}`},
	{"read escaped duplicate", "read", `{"path":"task.txt"}`, `{"path":"task.txt","path":"build/result.txt"}`},
	{"read escaped case variant", "read", `{"path":"task.txt"}`, `{"path":"task.txt","Path":"build/result.txt"}`},
	{"read folded offset", "read", `{"path":"task.txt"}`, `{"path":"task.txt","offſet":2}`},
	{"write exact duplicate", "write", `{"path":"out.txt","content":"expected\n"}`,
		`{"path":"out.txt","content":"expected\n","content":"unexpected\n"}`},
	{"write escaped duplicate", "write", `{"path":"out.txt","content":"expected\n"}`,
		`{"path":"out.txt","content":"expected\n","content":"unexpected\n"}`},
	{"write escaped case variant", "write", `{"path":"out.txt","content":"expected\n"}`,
		`{"path":"out.txt","content":"expected\n","Content":"unexpected\n"}`},
	{"edit exact duplicate", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}],"edits":[{"oldText":"baseline","newText":"unexpected"}]}`},
	{"edit escaped duplicate", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}],"edits":[{"oldText":"baseline","newText":"unexpected"}]}`},
	{"edit escaped case variant", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}],"Edits":[{"oldText":"baseline","newText":"unexpected"}]}`},
	{"edit folded name", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}],"editſ":[{"oldText":"baseline","newText":"unexpected"}]}`},
	{"edit nested exact duplicate", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","oldText":"unexpected","newText":"edited"}]}`},
	{"edit nested escaped duplicate", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","oldText":"unexpected","newText":"edited"}]}`},
	{"edit nested escaped case variant", "edit", `{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`,
		`{"path":"task.txt","edits":[{"oldText":"baseline","OldText":"unexpected","newText":"edited"}]}`},
}

// caseVariantDecision is a synthetic trusted ALLOW decision for one call of the given tool.
func caseVariantDecision(callID, tool string) gate.Decision {
	return gate.Decision{
		Verdict: gate.Allow, ReasonCode: "policy_rule_allow",
		PolicyDigest: "tbound-policy/v1:sha256:" + strings.Repeat("4", 64),
		ToolCallID:   callID, Tool: tool, Sequence: 1,
		ResponseID: &correlation.Identifier{Issuer: durableResponseIssuer, Opaque: "response-" + callID},
	}
}

// durableDecoderError runs the durable argument decoder for tool over arguments and returns its error.
func durableDecoderError(t *testing.T, tool, arguments string) error {
	t.Helper()
	raw := json.RawMessage(arguments)
	switch tool {
	case "bash":
		_, err := durableBashArguments(raw)
		return err
	case "read":
		_, _, err := durableReadArguments(raw)
		return err
	case "write":
		_, _, err := durableWriteArguments(raw)
		return err
	case "edit":
		_, _, err := durableEditArguments(raw)
		return err
	}
	t.Fatalf("no durable decoder for tool %q", tool)
	return nil
}

// TestDurableDecodersRefuseCaseVariantMembers proves each durable argument decoder, for bash, read, write and edit
// including a nested edit member, refuses a member whose name differs from a schema name only by case. The plain
// control must still decode, and the protocol capture must refuse the variant too.
func TestDurableDecodersRefuseCaseVariantMembers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable decoders are built for Linux only")
	}
	for _, tc := range durableCaseVariantCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := protocol.CanonicalArguments(tc.tool, json.RawMessage(tc.variant)); err == nil {
				t.Fatalf("the protocol capture accepted the case-variant %s object %s", tc.tool, tc.variant)
			}
			if err := durableDecoderError(t, tc.tool, tc.plain); err != nil {
				t.Fatalf("the durable %s decoder refused the plain control %s: %v", tc.tool, tc.plain, err)
			}
			if err := durableDecoderError(t, tc.tool, tc.variant); err == nil {
				t.Fatalf("the durable %s decoder accepted the case-variant member %s", tc.tool, tc.variant)
			}
		})
	}
}

// durableEscapedExactCases writes exact schema names with JSON escapes. Each decodes to a schema name, so the protocol
// capture and every durable decoder must accept the object.
var durableEscapedExactCases = []struct{ name, tool, arguments string }{
	{"bash command", "bash", `{"command":"true"}`},
	{"read path", "read", `{"path":"task.txt"}`},
	{"write path and content", "write", `{"path":"out.txt","content":"expected\n"}`},
	{"edit path, edits and nested members", "edit",
		`{"path":"task.txt","edits":[{"oldText":"baseline","newText":"edited"}]}`},
}

// TestDurableDecodersAcceptEscapedExactMemberNames is the positive control for the refusals above: a name written with
// escapes that equals a schema name is the same member, so it must still decode.
func TestDurableDecodersAcceptEscapedExactMemberNames(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable decoders are built for Linux only")
	}
	for _, tc := range durableEscapedExactCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := protocol.CanonicalArguments(tc.tool, json.RawMessage(tc.arguments)); err != nil {
				t.Fatalf("the protocol capture refused the escaped exact %s object %s: %v", tc.tool, tc.arguments, err)
			}
			if err := durableDecoderError(t, tc.tool, tc.arguments); err != nil {
				t.Fatalf("the durable %s decoder refused the escaped exact object %s: %v", tc.tool, tc.arguments, err)
			}
		})
	}
}

// TestDurableExecutorRunsEscapedExactBashAndReadMembers proves that an escaped exact bash command reaches the runner
// as "true", and that an escaped exact read path reads the baseline file.
func TestDurableExecutorRunsEscapedExactBashAndReadMembers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	output, _, calls, argv, err := executeDurableBashOrReadVariant(t, "bash", `{"command":"true"}`)
	if err != nil || len(output) == 0 || calls != 1 || len(argv) != 2 || argv[0] != "-lc" || argv[1] != "true" {
		t.Fatalf("escaped bash command did not run as true: output=%q runner=%d argv=%q err=%v", output, calls, argv, err)
	}
	output, _, _, _, err = executeDurableBashOrReadVariant(t, "read", `{"path":"task.txt"}`)
	if err != nil || !strings.Contains(string(output), "baseline") {
		t.Fatalf("escaped read path did not read task.txt: output=%q err=%v", output, err)
	}
}

// executeDurableBashOrReadVariant runs one argument object through a fresh durable executor and reports the released
// output, the tip after the call, how many times the bash runner ran, the argv it received, and the error.
func executeDurableBashOrReadVariant(t *testing.T, tool, arguments string) (json.RawMessage, string, int, []string, error) {
	t.Helper()
	if tool == "bash" {
		store, _, _, g0 := durableBashFixture(t)
		proposal, decision := durableBashProposal(arguments)
		leaseID, err := durableTrustedIdentity("lease", proposal, decision)
		if err != nil {
			t.Fatal(err)
		}
		runner := durableConsistentBashRunner(leaseID)
		executor, err := NewDurableExecutor(DurableExecutorConfig{
			Store: store, Tip: g0, CallIssuer: durableCallIssuer,
			PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
			CommandRunner: runner, BashOutputPaths: []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		output, err := executor.Execute(context.Background(), proposal, decision)
		return output, executor.Tip().ID(), runner.calls, runner.lastSpec.Args, err
	}
	store, _, _, g0 := e05Store(t, e05TaskBaseline)
	executor, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: durableCallIssuer,
		PolicyDigest: durablePolicyDigest, MetadataPolicyDigest: durableMetadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := executor.Execute(context.Background(), e05Proposal("call-case-read", "read", json.RawMessage(arguments)),
		caseVariantDecision("call-case-read", "read"))
	return output, executor.Tip().ID(), 0, nil, err
}

// TestDurableExecutorRefusesCaseVariantBashAndReadBeforeRunning proves that for bash and read the executor releases
// nothing and runs nothing for a case-variant member. The plain control executes first, so the refusal is the
// decoder's and not a broken harness.
func TestDurableExecutorRefusesCaseVariantBashAndReadBeforeRunning(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("durable session-repository integration runs only on Linux")
	}
	for _, tc := range durableCaseVariantCases {
		if tc.tool != "bash" && tc.tool != "read" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			control, _, _, _, err := executeDurableBashOrReadVariant(t, tc.tool, tc.plain)
			if err != nil || len(control) == 0 {
				t.Fatalf("plain %s control did not execute: output=%q err=%v", tc.tool, control, err)
			}
			output, tip, runnerCalls, _, err := executeDurableBashOrReadVariant(t, tc.tool, tc.variant)
			if err == nil || len(output) != 0 || tip != "g0" || runnerCalls != 0 {
				t.Fatalf("durable %s executed the case-variant member %s: output=%q err=%v tip=%s runner=%d",
					tc.tool, tc.variant, output, err, tip, runnerCalls)
			}
		})
	}
}
