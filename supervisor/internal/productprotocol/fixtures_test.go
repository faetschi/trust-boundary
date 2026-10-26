package productprotocol

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The optional web chat module keeps its own copy of the wire types and checks it
// against JSON fixtures that this test owns. A change to the protocol changes the
// fixtures, this test fails until they are regenerated, and the module's parity
// test fails until its copy follows. Regenerate with:
//
//	go test ./internal/productprotocol -run Fixtures -update
var updateFixtures = flag.Bool("update", false, "rewrite the web chat protocol fixtures")

// fixtureDir holds the canonical messages. The web chat module reads them from here; the core never names the module.
func fixtureDir() string { return "testdata" }

const (
	fixtureSession = "psess-0123456789abcdef0123456789abcdef"
	fixtureTurn1   = fixtureSession + "/turn-1"
	fixtureTurn2   = fixtureSession + "/turn-2"
	fixtureDigest  = "sha256:3b1c0f5e9a7d2c4b6e8f0a1b2c3d4e5f60718293a4b5c6d7e8f9012a3b4c5d6e"
)

func fixtureBudget() Budget { return Budget{HostCalls: 200, Tokens: 100000, DurationSeconds: 900} }

func okResponse(r Response) Response { r.Version, r.Status = Version, StatusOK; return r }

// fixtureExamples are the canonical messages: every request kind, and the responses
// a client sees, with every field of every wire type set in at least one of them.
func fixtureExamples() map[string]any {
	turns := []TurnView{
		{ID: fixtureTurn1, Ordinal: 1, State: "terminal", Prompt: "fix the failing test", Outcome: "published",
			Resolution: "publish", CandidateDigest: fixtureDigest, ReviewTimeoutSeconds: 600},
		{ID: fixtureTurn2, Ordinal: 2, State: "terminal", Prompt: "also update the docs", Outcome: "failed",
			TerminalReason: "provider_failed", CancelRequested: true},
	}
	session := SessionView{ID: fixtureSession, ProjectID: "demo-project", ProviderProfile: "openrouter-default",
		PublicationMode: "review", ReadOnly: true, Revision: 7, Blocked: true,
		Budget: fixtureBudget(), Consumed: Budget{HostCalls: 12, Tokens: 4096, DurationSeconds: 61},
		Reserved: Budget{HostCalls: 1, Tokens: 512, DurationSeconds: 30}, TotalTurns: 70, TurnsTruncated: true, Turns: turns}
	listed := session
	listed.Turns = nil
	events := []Event{
		{Seq: 0, Kind: EventTurnAdmitted, TurnID: fixtureTurn1, Text: "make the comparison return a<b"},
		{Seq: 1, Kind: EventTurnLinked, TurnID: fixtureTurn1},
		{Seq: 2, Kind: EventAssistantMessage, TurnID: fixtureTurn1, Text: "I will read the file first."},
		{Seq: 3, Kind: EventToolCall, TurnID: fixtureTurn1, Tool: "bash", ToolCallID: "call_1", Verdict: "DENY",
			ReasonCode: "policy_rule_deny", Hint: "Use the read tool for files in the project."},
		{Seq: 4, Kind: EventToolResult, TurnID: fixtureTurn1, Tool: "bash", ToolCallID: "call_1", Verdict: "DENY",
			ReasonCode: "policy_rule_deny", Hint: "Use the read tool for files in the project.", Text: "denied"},
		{Seq: 5, Kind: EventTurnCancelRequested, TurnID: fixtureTurn1},
		{Seq: 6, Kind: EventTurnAwaitingReview, TurnID: fixtureTurn1, Text: "2 changed paths", CandidateDigest: fixtureDigest,
			Paths: []string{"src/compare.go", "src/compare_test.go"}},
		{Seq: 7, Kind: EventTurnResolution, TurnID: fixtureTurn1, CandidateDigest: fixtureDigest, Reason: "publish"},
		{Seq: 8, Kind: EventTurnTerminal, TurnID: fixtureTurn1, Outcome: "published", Reason: "user"},
	}
	return map[string]any{
		"request-create_session": Request{Version: Version, Op: OpCreateSession, CreateSession: &CreateSession{
			ProjectID: "demo-project", ProviderProfile: "openrouter-default", PublicationMode: "review",
			Budget: fixtureBudget(), ReadOnly: true, RequestKey: "ck-00112233445566778899aabbccddeeff"}},
		"request-submit_turn": Request{Version: Version, Op: OpSubmitTurn, SubmitTurn: &SubmitTurn{SessionID: fixtureSession,
			ExpectedRevision: 7, RequestKey: "ck-ffeeddccbbaa99887766554433221100", Prompt: "fix the failing test",
			ExpectedBaselineDigest: fixtureDigest}},
		"request-get_session":   Request{Version: Version, Op: OpGetSession, GetSession: &SessionRef{SessionID: fixtureSession}},
		"request-list_sessions": Request{Version: Version, Op: OpListSessions},
		"request-observe": Request{Version: Version, Op: OpObserve,
			Observe: &Observe{SessionID: fixtureSession, Cursor: 9, WaitMillis: MaxObserveWaitMillis}},
		"request-get_candidate": Request{Version: Version, Op: OpGetCandidate, GetCandidate: &TurnRef{SessionID: fixtureSession, TurnID: fixtureTurn1}},
		"request-cancel_turn": Request{Version: Version, Op: OpCancelTurn, CancelTurn: &CancelTurn{SessionID: fixtureSession,
			TurnID: fixtureTurn1, ExpectedRevision: 7, RequestKey: "ck-0123456789abcdef0123456789abcdef"}},
		"request-resolve_turn": Request{Version: Version, Op: OpResolveTurn, ResolveTurn: &ResolveTurn{SessionID: fixtureSession,
			TurnID: fixtureTurn1, ExpectedRevision: 7, RequestKey: "ck-fedcba9876543210fedcba9876543210",
			CandidateDigest: fixtureDigest, Decision: "publish"}},
		"response-session":    okResponse(Response{Session: &session}),
		"response-sessions":   okResponse(Response{Sessions: []SessionView{listed}}),
		"response-turn":       okResponse(Response{Turn: &turns[0], Replayed: true}),
		"response-events":     okResponse(Response{Events: events, NextCursor: 9}),
		"response-events-gap": okResponse(Response{Events: events[8:], NextCursor: 9, Gap: true}),
		"response-candidate": okResponse(Response{Candidate: &CandidateView{TurnID: fixtureTurn1, CandidateDigest: fixtureDigest,
			Changes: []CandidateChange{
				{Path: "src/compare.go", Op: "modify", Diff: "--- a/src/compare.go\n+++ b/src/compare.go\n@@ -1 +1 @@\n-return a > b\n+return a < b\n", Truncated: true},
				{Path: "src/new.go", Op: "create"},
			}}}),
		"response-error": Response{Version: Version, Status: StatusError,
			Error: &Error{Code: "STALE_REVISION", Detail: "the session is at revision 8"}},
	}
}

func encodeFixture(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

// TestFixturesMatchTheCommittedFiles compares each canonical message with the file
// the web chat module reads, byte for byte.
func TestFixturesMatchTheCommittedFiles(t *testing.T) {
	dir := fixtureDir()
	examples := fixtureExamples()
	if *updateFixtures {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		stale, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, path := range stale {
			if _, keep := examples[strings.TrimSuffix(filepath.Base(path), ".json")]; !keep {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for name, value := range examples {
		want := encodeFixture(t, value)
		path := filepath.Join(dir, name+".json")
		if *updateFixtures {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v (regenerate with -update)", name, err)
		} else if !bytes.Equal(got, want) {
			t.Errorf("%s.json differs from the protocol's canonical message (regenerate with -update and update the web chat module's copy)", name)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if _, known := examples[strings.TrimSuffix(filepath.Base(path), ".json")]; !known {
			t.Errorf("%s is not a canonical message any more (regenerate with -update)", filepath.Base(path))
		}
	}
}

// TestFixturesAreValidProtocol runs each canonical message through the real framing
// and strict decoder, and each request through Validate.
func TestFixturesAreValidProtocol(t *testing.T) {
	for name, value := range fixtureExamples() {
		var frame bytes.Buffer
		switch message := value.(type) {
		case Request:
			if err := message.Validate(); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			var got Request
			if err := WriteFrame(&frame, message, MaxRequestBytes); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if err := ReadFrame(&frame, MaxRequestBytes, &got); err != nil || !reflect.DeepEqual(got, message) {
				t.Errorf("%s: framing round trip: %v", name, err)
			}
		case Response:
			var got Response
			if err := WriteFrame(&frame, message, MaxResponseBytes); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if err := ReadFrame(&frame, MaxResponseBytes, &got); err != nil || !reflect.DeepEqual(got, message) {
				t.Errorf("%s: framing round trip: %v", name, err)
			}
		default:
			t.Fatalf("%s: unexpected example type %T", name, value)
		}
	}
}

// TestFixturesSetEveryWireField fails when a wire type gains a field that no
// canonical message sets, because a field no fixture carries cannot be checked
// against the web chat module's copy.
func TestFixturesSetEveryWireField(t *testing.T) {
	set := map[string]bool{}
	for _, value := range fixtureExamples() {
		markSetFields(reflect.ValueOf(value), set)
	}
	want := map[string]bool{}
	collectFields(reflect.TypeOf(Request{}), want)
	collectFields(reflect.TypeOf(Response{}), want)
	var missing []string
	for field := range want {
		if !set[field] {
			missing = append(missing, field)
		}
	}
	slices.Sort(missing)
	if len(missing) != 0 {
		t.Errorf("no canonical message sets %v; add them to fixtureExamples", missing)
	}
}

func markSetFields(v reflect.Value, set map[string]bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			markSetFields(v.Elem(), set)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			markSetFields(v.Index(i), set)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !v.Field(i).IsZero() {
				set[v.Type().Name()+"."+v.Type().Field(i).Name] = true
			}
			markSetFields(v.Field(i), set)
		}
	}
}

func collectFields(t reflect.Type, want map[string]bool) {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice:
		collectFields(t.Elem(), want)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			want[t.Name()+"."+t.Field(i).Name] = true
			collectFields(t.Field(i).Type, want)
		}
	}
}
