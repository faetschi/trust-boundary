package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/correlation"
)

const testIssuer = "fixture/provider-tool-call/v1"
const testResponseIssuer = "fixture/provider-response/v1"

type requestFixture struct {
	SchemaVersion string          `json:"schema_version"`
	ToolCallID    string          `json:"tool_call_id"`
	Tool          string          `json:"tool"`
	Arguments     json.RawMessage `json:"arguments"`
}

func TestPiProposalsMatchTrustedFourToolCaptureSequence(t *testing.T) {
	stream := newTestStream(t)
	calls := []struct {
		id, tool, args, generation string
		sequence                   uint64
	}{
		{"call-1", "read", `{"path":"README.md","offset":1}`, "g0", 1},
		{"call-2", "edit", `{"path":"README.md","edits":[{"oldText":"draft","newText":"final"}]}`, "g1", 2},
		{"call-3", "bash", `{"command":"true","timeout":5}`, "g2", 3},
		{"call-4", "read", `{"path":"README.md"}`, "g2", 4},
	}
	for _, call := range calls {
		decision := stream.Capture(testCapture(t, call.id, call.tool, call.args, call.sequence, call.generation))
		if !decision.Accepted || decision.StreamClosed || decision.CaptureOrdinal != call.sequence {
			t.Fatalf("capture %s: got %+v", call.id, decision)
		}
	}
	for index, call := range calls {
		decision := stream.Propose(encodeRequest(t, call.id, call.tool, call.args))
		if !decision.Accepted || decision.ReasonCode != correlation.ReasonMatched || decision.StreamClosed || decision.CaptureOrdinal != uint64(index+1) {
			t.Fatalf("proposal %s: got %+v", call.id, decision)
		}
		if decision.Proposal == nil || decision.Proposal.Sequence != call.sequence || decision.Proposal.Generation != call.generation {
			t.Fatalf("proposal did not inherit trusted broker context: %+v", decision.Proposal)
		}
	}
}

func TestProposalCannotForgeBrokerMetadata(t *testing.T) {
	stream := newTestStream(t)
	args := `{"path":"README.md"}`
	stream.Capture(testCapture(t, "call-1", "read", args, 1, "g0"))
	raw := []byte(`{"schema_version":"tbound-proposal/v1","tool_call_id":"call-1","tool":"read","arguments":{"path":"README.md"},"response_id":{"issuer":"fake","opaque":"response-1"},"issuer":"fake","canonical_arguments_digest":"` + strings.Repeat("0", 64) + `","sequence":99,"generation":"g999"}`)
	decision := stream.Propose(raw)
	if decision.Accepted || decision.ReasonCode != correlation.ReasonMalformedProposal || !decision.StreamClosed {
		t.Fatalf("forged broker metadata should fail closed as an unknown field: %+v", decision)
	}
	if next := stream.Propose(encodeRequest(t, "call-1", "read", args)); next.ReasonCode != correlation.ReasonSessionClosed || !next.StreamClosed {
		t.Fatalf("stream remained usable after forged metadata: %+v", next)
	}
}

func TestArgumentMismatchFailsClosed(t *testing.T) {
	stream := newTestStream(t)
	stream.Capture(testCapture(t, "call-1", "write", `{"path":"a","content":"expected"}`, 1, "g0"))
	decision := stream.Propose(encodeRequest(t, "call-1", "write", `{"path":"a","content":"changed"}`))
	if decision.Accepted || decision.ReasonCode != correlation.ReasonArgumentDigestMismatch || !decision.StreamClosed {
		t.Fatalf("argument mismatch should fail closed: %+v", decision)
	}
}

func TestUnknownCallFailsClosed(t *testing.T) {
	stream := newTestStream(t)
	decision := stream.Propose(encodeRequest(t, "not-captured", "read", `{"path":"a"}`))
	if decision.Accepted || decision.ReasonCode != correlation.ReasonUnknownCall || !decision.StreamClosed {
		t.Fatalf("unknown call should fail closed: %+v", decision)
	}
}

func TestReplayAndOutOfOrderFailClosed(t *testing.T) {
	t.Run("replay", func(t *testing.T) {
		stream := newTestStream(t)
		for i, id := range []string{"call-1", "call-2"} {
			stream.Capture(testCapture(t, id, "read", `{"path":"a"}`, uint64(i+1), "g0"))
		}
		first := stream.Propose(encodeRequest(t, "call-1", "read", `{"path":"a"}`))
		if !first.Accepted {
			t.Fatalf("first call was not accepted: %+v", first)
		}
		second := stream.Propose(encodeRequest(t, "call-1", "read", `{"path":"a"}`))
		if second.Accepted || second.ReasonCode != correlation.ReasonReplay || !second.StreamClosed {
			t.Fatalf("replay should fail closed: %+v", second)
		}
	})
	t.Run("out-of-order", func(t *testing.T) {
		stream := newTestStream(t)
		for i, id := range []string{"call-1", "call-2"} {
			stream.Capture(testCapture(t, id, "read", `{"path":"a"}`, uint64(i+1), "g0"))
		}
		decision := stream.Propose(encodeRequest(t, "call-2", "read", `{"path":"a"}`))
		if decision.Accepted || decision.ReasonCode != correlation.ReasonOutOfOrder || !decision.StreamClosed {
			t.Fatalf("out-of-order call should fail closed: %+v", decision)
		}
	})
}

func TestCaptureSequenceGenerationAndDigestValidation(t *testing.T) {
	t.Run("non-increasing sequence", func(t *testing.T) {
		stream := newTestStream(t)
		stream.Capture(testCapture(t, "call-1", "read", `{"path":"a"}`, 2, "g0"))
		decision := stream.Capture(testCapture(t, "call-2", "read", `{"path":"b"}`, 2, "g1"))
		if decision.Accepted || decision.ReasonCode != correlation.ReasonNonIncreasingSequence || !decision.StreamClosed {
			t.Fatalf("non-increasing sequence should fail closed: %+v", decision)
		}
	})
	t.Run("blank generation", func(t *testing.T) {
		stream := newTestStream(t)
		decision := stream.Capture(testCapture(t, "call-1", "read", `{"path":"a"}`, 1, " "))
		if decision.Accepted || decision.ReasonCode != correlation.ReasonMalformedCapture || !decision.StreamClosed {
			t.Fatalf("blank generation should fail closed: %+v", decision)
		}
	})
	t.Run("wrong digest profile", func(t *testing.T) {
		stream := newTestStream(t)
		capture := testCapture(t, "call-1", "read", `{"path":"a"}`, 1, "g0")
		capture.CanonicalizationProfile = "other-profile/v1"
		decision := stream.Capture(capture)
		if decision.Accepted || decision.ReasonCode != correlation.ReasonMalformedCapture || !decision.StreamClosed {
			t.Fatalf("wrong digest profile should fail closed: %+v", decision)
		}
	})
	t.Run("wrong issuer", func(t *testing.T) {
		stream := newTestStream(t)
		capture := testCapture(t, "call-1", "read", `{"path":"a"}`, 1, "g0")
		capture.ToolCallID.Issuer = "unregistered/provider/v1"
		decision := stream.Capture(capture)
		if decision.Accepted || decision.ReasonCode != correlation.ReasonMalformedCapture || !decision.StreamClosed {
			t.Fatalf("wrong issuer should fail closed: %+v", decision)
		}
	})
	t.Run("wrong response issuer", func(t *testing.T) {
		stream := newTestStream(t)
		capture := testCapture(t, "call-1", "read", `{"path":"a"}`, 1, "g0")
		capture.ResponseID.Issuer = "unregistered/response/v1"
		decision := stream.Capture(capture)
		if decision.Accepted || decision.ReasonCode != correlation.ReasonMalformedCapture || !decision.StreamClosed {
			t.Fatalf("wrong response issuer should fail closed: %+v", decision)
		}
	})
	t.Run("duplicate call ID", func(t *testing.T) {
		stream := newTestStream(t)
		first := testCapture(t, "call-1", "read", `{"path":"a"}`, 1, "g0")
		stream.Capture(first)
		second := testCapture(t, "call-1", "read", `{"path":"b"}`, 2, "g1")
		decision := stream.Capture(second)
		if decision.Accepted || decision.ReasonCode != correlation.ReasonDuplicateCallID || !decision.StreamClosed {
			t.Fatalf("duplicate call ID should fail closed: %+v", decision)
		}
	})
}

func TestMalformedRequestsFailClosed(t *testing.T) {
	base := `{"schema_version":"tbound-proposal/v1","tool_call_id":"call-1","tool":"read","arguments":{"path":"a"}}`
	cases := map[string]string{
		"duplicate top-level key":   strings.Replace(base, `"tool":"read"`, `"tool":"read","tool":"read"`, 1),
		"duplicate argument key":    `{"schema_version":"tbound-proposal/v1","tool_call_id":"call-1","tool":"read","arguments":{"path":"a","path":"b"}}`,
		"trailing value":            base + ` {}`,
		"extra proposal field":      strings.TrimSuffix(base, "}") + `,"sequence":1}`,
		"case variant extra key":    strings.TrimSuffix(base, "}") + `,"Tool":"read"}`,
		"case variant required key": strings.Replace(base, `"tool":"read"`, `"Tool":"read"`, 1),
		"unknown tool":              strings.Replace(base, `"read"`, `"powershell"`, 1),
		"unknown argument":          strings.Replace(base, `"path":"a"`, `"path":"a","extra":true`, 1),
		"negative zero":             `{"schema_version":"tbound-proposal/v1","tool_call_id":"call-1","tool":"read","arguments":{"path":"a","offset":-0}}`,
		"surrounding ID whitespace": `{"schema_version":"tbound-proposal/v1","tool_call_id":" call-1 ","tool":"read","arguments":{"path":"a"}}`,
		"surrogate escape":          `{"schema_version":"tbound-proposal/v1","tool_call_id":"call-1","tool":"write","arguments":{"path":"a","content":"\ud800"}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			stream := newTestStream(t)
			decision := stream.Propose([]byte(raw))
			want := correlation.ReasonMalformedProposal
			if name == "unknown tool" {
				want = correlation.ReasonUnregisteredTool
			}
			if decision.Accepted || decision.ReasonCode != want || !decision.StreamClosed {
				t.Fatalf("malformed request should fail closed with %s: %+v", want, decision)
			}
		})
	}
	t.Run("oversized frame", func(t *testing.T) {
		stream := newTestStream(t)
		decision := stream.Propose([]byte(strings.Repeat(" ", MaxFrameBytes+1)))
		if decision.Accepted || decision.ReasonCode != correlation.ReasonMalformedProposal || !decision.StreamClosed {
			t.Fatalf("oversized frame should fail closed: %+v", decision)
		}
	})
	t.Run("excessive nesting", func(t *testing.T) {
		stream := newTestStream(t)
		depth := maxJSONDepth + 1
		nested := strings.Repeat("{\"x\":", depth) + `null` + strings.Repeat("}", depth)
		raw := `{"schema_version":"tbound-proposal/v1","tool_call_id":"call-1","tool":"read","arguments":` + nested + `}`
		decision := stream.Propose([]byte(raw))
		if decision.Accepted || decision.ReasonCode != correlation.ReasonMalformedProposal || !decision.StreamClosed {
			t.Fatalf("excessive nesting should fail closed: %+v", decision)
		}
	})
}

func TestCanonicalDigestProfile(t *testing.T) {
	left, err := CanonicalArgumentsDigest("read", json.RawMessage(`{"path":"a","offset":1,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalArgumentsDigest("read", json.RawMessage(`{"limit":2,"path":"a","offset":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("reordered arguments have different RFC 8785 digests: %s != %s", left, right)
	}
	if !validDigest(left) {
		t.Fatalf("digest does not include profile and lowercase SHA-256: %q", left)
	}
}

func TestRFC8785CanonicalJSONVectors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "RFC 8785 primitive and number sample",
			raw:  `{"numbers":[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001],"string":"\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/","literals":[null,true,false]}`,
			want: `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`,
		},
		{
			name: "RFC 8785 UTF-16 property ordering sample",
			raw:  `{"\u20ac":"Euro Sign","\r":"Carriage Return","\ufb33":"Hebrew Letter Dalet With Dagesh","1":"One","\ud83d\ude00":"Emoji: Grinning Face","\u0080":"Control","\u00f6":"Latin Small Letter O With Diaeresis"}`,
			want: "{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"\u00f6\":\"Latin Small Letter O With Diaeresis\",\"€\":\"Euro Sign\",\"😀\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalizeJSON([]byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("RFC 8785 bytes mismatch:\n got: %q\nwant: %q", got, test.want)
			}
		})
	}
}

func TestCanonicalArgumentsBytesAndPiNumberSemantics(t *testing.T) {
	tests := []struct {
		tool string
		raw  string
		want string
	}{
		{"write", `{"path":"x","content":"<>&\u2028\u2029"}`, "{\"content\":\"<>&\u2028\u2029\",\"path\":\"x\"}"},
		{"read", `{"path":"x","offset":1.5,"limit":1e1}`, `{"limit":10,"offset":1.5,"path":"x"}`},
		{"read", `{"path":"x","offset":9007199254740992}`, `{"offset":9007199254740992,"path":"x"}`},
		{"bash", `{"command":"true","timeout":0.25}`, `{"command":"true","timeout":0.25}`},
		{"bash", `{"command":"true","timeout":2147483.647}`, `{"command":"true","timeout":2147483.647}`},
	}
	for _, test := range tests {
		got, err := CanonicalArguments(test.tool, json.RawMessage(test.raw))
		if err != nil {
			t.Fatalf("%s arguments %s: %v", test.tool, test.raw, err)
		}
		if string(got) != test.want {
			t.Fatalf("%s canonical bytes mismatch: got %q, want %q", test.tool, got, test.want)
		}
	}
	for _, raw := range []string{
		`{"command":"true","timeout":0}`,
		`{"command":"true","timeout":-1}`,
		`{"command":"true","timeout":2147483.648}`,
	} {
		if _, err := CanonicalArguments("bash", json.RawMessage(raw)); err == nil {
			t.Errorf("Pi-invalid bash timeout was accepted: %s", raw)
		}
	}
}

func TestRFC8785HTMLSeparatorsAndNegativeZeroPolicy(t *testing.T) {
	got, err := canonicalizeJSON([]byte(`{"text":"<>&\u2028\u2029"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"text\":\"<>&\u2028\u2029\"}"
	if string(got) != want {
		t.Fatalf("HTML or separator escaping differs from JCS: got %q, want %q", got, want)
	}
	for _, raw := range []string{"-0", "-0.0", "-0e0", "{\"n\":-0}"} {
		if _, err := canonicalizeJSON([]byte(raw)); err == nil {
			t.Errorf("negative zero was accepted despite verified RFC 8785 erratum 7920: %s", raw)
		}
	}
	if got, err := canonicalizeJSON([]byte("0")); err != nil || string(got) != "0" {
		t.Fatalf("positive zero should remain valid, got %q, %v", got, err)
	}
}

func TestCaptureAndProposalCanonicalizeTheirOwnRawArguments(t *testing.T) {
	captureRaw := `{"path":"<file>","content":"A\u0062<&\u2028\u2029"}`
	proposalRaw := `{"content":"Ab<&\u2028\u2029","path":"<file>"}`
	want := "{\"content\":\"Ab<&\u2028\u2029\",\"path\":\"<file>\"}"
	for _, raw := range []string{captureRaw, proposalRaw} {
		got, err := CanonicalArguments("write", json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("raw argument bytes canonicalized to %q, want %q", got, want)
		}
	}
	stream := newTestStream(t)
	if decision := stream.Capture(testCapture(t, "call-1", "write", captureRaw, 1, "g0")); !decision.Accepted {
		t.Fatalf("capture failed: %+v", decision)
	}
	decision := stream.Propose(encodeRequest(t, "call-1", "write", proposalRaw))
	if !decision.Accepted || decision.ReasonCode != correlation.ReasonMatched {
		t.Fatalf("semantically equal raw capture/proposal forms did not match: %+v", decision)
	}
}

func TestCaptureRequiresRawArgumentsAndChecksAssertedDigest(t *testing.T) {
	t.Run("digest only", func(t *testing.T) {
		stream := newTestStream(t)
		capture := testCapture(t, "call-1", "read", `{"path":"a"}`, 1, "g0")
		capture.RawArguments = nil
		decision := stream.Capture(capture)
		if decision.Accepted || decision.ReasonCode != correlation.ReasonMalformedCapture || !decision.StreamClosed {
			t.Fatalf("digest-only capture should fail closed: %+v", decision)
		}
	})
	t.Run("asserted digest mismatch", func(t *testing.T) {
		stream := newTestStream(t)
		capture := testCapture(t, "call-1", "read", `{"path":"a"}`, 1, "g0")
		capture.RawArguments = json.RawMessage(`{"path":"different"}`)
		decision := stream.Capture(capture)
		if decision.Accepted || decision.ReasonCode != correlation.ReasonMalformedCapture || !decision.StreamClosed {
			t.Fatalf("capture digest mismatch should fail closed: %+v", decision)
		}
	})
}

func TestPerStreamCountBudgets(t *testing.T) {
	args := `{"path":"a"}`
	stream := newTestStream(t)
	for index := 0; index < MaxCapturesPerStream; index++ {
		id := fmt.Sprintf("call-%d", index+1)
		if decision := stream.Capture(testCapture(t, id, "read", args, uint64(index+1), "g0")); !decision.Accepted {
			t.Fatalf("capture %d failed: %+v", index+1, decision)
		}
	}
	if decision := stream.Capture(testCapture(t, "call-over", "read", args, MaxCapturesPerStream+1, "g0")); decision.ReasonCode != ReasonResourceLimit || !decision.StreamClosed {
		t.Fatalf("capture budget should fail closed: %+v", decision)
	}

	stream = newTestStream(t)
	for index := 0; index < MaxProposalsPerStream; index++ {
		id := fmt.Sprintf("call-%d", index+1)
		if decision := stream.Capture(testCapture(t, id, "read", args, uint64(index+1), "g0")); !decision.Accepted {
			t.Fatalf("capture %d failed: %+v", index+1, decision)
		}
	}
	for index := 0; index < MaxProposalsPerStream; index++ {
		id := fmt.Sprintf("call-%d", index+1)
		if decision := stream.Propose(encodeRequest(t, id, "read", args)); !decision.Accepted {
			t.Fatalf("proposal %d failed: %+v", index+1, decision)
		}
	}
	if decision := stream.Propose(encodeRequest(t, "call-over", "read", args)); decision.ReasonCode != ReasonResourceLimit || !decision.StreamClosed {
		t.Fatalf("proposal count budget should fail closed: %+v", decision)
	}
}

func TestPerStreamProposalByteBudget(t *testing.T) {
	stream := newTestStream(t)
	content := strings.Repeat("x", MaxFrameBytes/2-1024)
	args := `{"path":"a","content":"` + content + `"}`
	for index := 0; index < 32; index++ {
		id := fmt.Sprintf("call-%d", index+1)
		if decision := stream.Capture(testCapture(t, id, "write", args, uint64(index+1), "g0")); !decision.Accepted {
			t.Fatalf("capture %d failed: %+v", index+1, decision)
		}
		if decision := stream.Propose(encodeRequest(t, id, "write", args)); !decision.Accepted {
			t.Fatalf("proposal %d failed: %+v", index+1, decision)
		}
	}
	id := "call-33"
	if decision := stream.Capture(testCapture(t, id, "write", args, 33, "g0")); !decision.Accepted {
		t.Fatalf("final capture failed: %+v", decision)
	}
	if decision := stream.Propose(encodeRequest(t, id, "write", args)); decision.ReasonCode != ReasonResourceLimit || !decision.StreamClosed {
		t.Fatalf("proposal byte budget should fail closed: %+v", decision)
	}
}

func newTestStream(t *testing.T) *Stream {
	t.Helper()
	stream, err := NewStream(testIssuer, testResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func testCapture(t *testing.T, id, tool, args string, sequence uint64, generation string) TrustedCapture {
	t.Helper()
	digest, err := CanonicalArgumentsDigest(tool, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return TrustedCapture{
		ResponseID:               correlation.Identifier{Issuer: testResponseIssuer, Opaque: fmt.Sprintf("response-%d", sequence)},
		ToolCallID:               correlation.Identifier{Issuer: testIssuer, Opaque: id},
		ToolName:                 tool,
		RawArguments:             json.RawMessage(args),
		CanonicalizationProfile:  CanonicalizationProfile,
		CanonicalArgumentsDigest: digest,
		Sequence:                 sequence,
		Generation:               generation,
	}
}

func encodeRequest(t *testing.T, id, tool, args string) []byte {
	t.Helper()
	encoded, err := json.Marshal(requestFixture{
		SchemaVersion: ProposalSchemaVersion,
		ToolCallID:    id,
		Tool:          tool,
		Arguments:     json.RawMessage(args),
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
