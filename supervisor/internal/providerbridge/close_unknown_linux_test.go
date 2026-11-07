//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

// releasedExecutor blocks inside Execute until release is closed, then returns its configured outcome.
type releasedExecutor struct {
	started chan struct{}
	release chan struct{}
	output  json.RawMessage
	err     error
}

func (e *releasedExecutor) Execute(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error) {
	close(e.started)
	<-e.release
	return append(json.RawMessage(nil), e.output...), e.err
}

type executingFixture struct {
	cfg      Config
	path     string
	journal  *audit.Journal
	c        *Conversation
	executor *releasedExecutor
	run      func(context.Context) (json.RawMessage, error)
}

// newExecutingFixture drives one ALLOWed call up to the executor, which blocks until release.
func newExecutingFixture(t *testing.T, tool string, arguments json.RawMessage, output json.RawMessage, execErr error) *executingFixture {
	t.Helper()
	cfg := recoveryTestConfig()
	path := filepath.Join(t.TempDir(), "provider.audit")
	journal := openRecoveryTestJournal(t, path)
	doer := &fixtureDoer{responses: []fixtureResponse{{responseID: "response-1", callID: "call-1", tool: tool, arguments: arguments}}}
	c, err := newWithDoer(cfg, ApprovedModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AdmitPrompt("task-close", "synthetic close test"); err != nil {
		t.Fatal(err)
	}
	turn, err := c.NextTurn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalFor(turn)
	matched, err := c.Correlate(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Evaluate(proposal, matched, recoveryTestPolicy(t, true))
	if err := c.RecordDecision(context.Background(), proposal, decision); err != nil {
		t.Fatal(err)
	}
	inner := &releasedExecutor{started: make(chan struct{}), release: make(chan struct{}), output: output, err: execErr}
	wrapped, err := NewExecutor(c, inner)
	if err != nil {
		t.Fatal(err)
	}
	return &executingFixture{cfg: cfg, path: path, journal: journal, c: c, executor: inner,
		run: func(ctx context.Context) (json.RawMessage, error) { return wrapped.Execute(ctx, proposal, decision) }}
}

type unknownEvidence struct {
	Outcome           string `json:"outcome"`
	ExecutorOutput    []byte `json:"executor_output"`
	CommandNotStarted bool   `json:"command_not_started"`
	Failure           string `json:"failure"`
}

// dispositions reopens the journal file and returns the kinds of the call's disposition records and the UNKNOWN evidence.
func (f *executingFixture) dispositions(t *testing.T) (results int, unknowns []unknownEvidence) {
	t.Helper()
	if err := f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := audit.Open(f.path)
	if err != nil {
		t.Fatalf("reopen provider journal: %v", err)
	}
	defer reopened.Close()
	trace, err := reopened.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "provider_tool_result", "provider_admission_denied":
			results++
		case "provider_tool_result_unknown":
			var evidence unknownEvidence
			if err := json.Unmarshal(record.Event.Data, &evidence); err != nil {
				t.Fatal(err)
			}
			unknowns = append(unknowns, evidence)
			if err := verifyReplayEventID(record, f.cfg.ConversationID, "unknown-result", uint64(record.Sequence)); err != nil {
				t.Fatalf("UNKNOWN record has an unexpected event ID %q: %v", record.Event.ID, err)
			}
		}
	}
	// The existing replay reader recognizes the record as an unresolved UNKNOWN result, not as an unsupported kind.
	if len(unknowns) != 1 {
		return results, unknowns
	}
	_, err = ReplayTranscript(reopened, RecoveryPins{Config: f.cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, true)}, &replayVerifierFixture{})
	if !errors.Is(err, ErrTranscriptReplay) || !strings.Contains(err.Error(), "unresolved UNKNOWN") {
		t.Fatalf("replay of the journal = %v, want the unresolved-UNKNOWN refusal", err)
	}
	return results, unknowns
}

func TestCloseDuringExecutionRecordsExactlyOneUnknown(t *testing.T) {
	write := json.RawMessage(`{"path":"x","content":"y"}`)
	for _, tc := range []struct {
		name       string
		tool       string
		args       json.RawMessage
		output     json.RawMessage
		err        error
		cancel     bool
		wantOutput bool
		notStarted bool
	}{
		{name: "mutation output", tool: "write", args: write, wantOutput: true,
			output: mutationOutput("write", "g1", recoveryTestTree("tree-1"), "transition-1", 1, "effect-1", nil)},
		{name: "executor error", tool: "write", args: write, err: errors.New("executor exploded")},
		{name: "bash command never started", tool: "bash", args: json.RawMessage(`{"command":"true"}`),
			err: fmt.Errorf("run: %w", protocol.ErrCommandNotStarted), notStarted: true},
		{name: "canceled", tool: "write", args: write, cancel: true, wantOutput: true,
			output: mutationOutput("write", "g1", recoveryTestTree("tree-1"), "transition-1", 1, "effect-1", nil)},
		{name: "admission denial", tool: "write", args: write,
			err: fmt.Errorf("durable write: %w", protectedWriteDenial("effect-denied"))},
		{name: "no-effect refusal", tool: "write", args: write,
			err: &protocol.NoEffectError{Tool: "write", Err: errors.New("refused")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecutingFixture(t, tc.tool, tc.args, tc.output, tc.err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				output json.RawMessage
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				output, err := f.run(ctx)
				done <- outcome{output, err}
			}()
			<-f.executor.started
			f.c.Close()
			f.c.Close()
			if !f.c.Closed() {
				t.Fatal("Close did not mark the conversation closed")
			}
			if _, err := f.c.NextTurn(context.Background()); !errors.Is(err, ErrClosed) {
				t.Fatalf("closed conversation accepted new work: %v", err)
			}
			if tc.cancel {
				cancel()
			}
			close(f.executor.release)
			got := <-done
			if got.output != nil || !errors.Is(got.err, ErrResultWithheld) {
				t.Fatalf("Execute after Close = (%s, %v), want the output withheld", got.output, got.err)
			}
			results, unknowns := f.dispositions(t)
			if results != 0 || len(unknowns) != 1 {
				t.Fatalf("dispositions: %d results, %d UNKNOWN records; want exactly one UNKNOWN", results, len(unknowns))
			}
			unknown := unknowns[0]
			if unknown.Outcome != "UNKNOWN" || (len(unknown.ExecutorOutput) != 0) != tc.wantOutput || unknown.CommandNotStarted != tc.notStarted {
				t.Fatalf("UNKNOWN evidence = %+v, want output=%v command_not_started=%v", unknown, tc.wantOutput, tc.notStarted)
			}
		})
	}
}

// A completion racing Close ends in exactly one disposition: the released result, or the UNKNOWN with the output withheld.
func TestCompletionRacingCloseRecordsExactlyOneDisposition(t *testing.T) {
	sawResult, sawUnknown := 0, 0
	for i := 0; i < 60; i++ {
		f := newExecutingFixture(t, "write", json.RawMessage(`{"path":"x","content":"y"}`),
			mutationOutput("write", "g1", recoveryTestTree("tree-1"), "transition-1", 1, "effect-1", nil), nil)
		var output json.RawMessage
		var execErr error
		done := make(chan struct{})
		go func() { output, execErr = f.run(context.Background()); close(done) }()
		<-f.executor.started
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); f.c.Close() }()
		go func() { defer wg.Done(); close(f.executor.release) }()
		wg.Wait()
		<-done
		f.journal.Close()
		reopened, err := audit.Open(f.path)
		if err != nil {
			t.Fatal(err)
		}
		trace, err := reopened.Trace()
		reopened.Close()
		if err != nil {
			t.Fatal(err)
		}
		results, unknowns := 0, 0
		for _, record := range trace.Records {
			switch record.Event.Kind {
			case "provider_tool_result":
				results++
			case "provider_tool_result_unknown":
				unknowns++
			}
		}
		switch {
		case results == 1 && unknowns == 0 && execErr == nil && len(output) != 0:
			sawResult++
		case results == 0 && unknowns == 1 && errors.Is(execErr, ErrResultWithheld) && output == nil:
			sawUnknown++
		default:
			t.Fatalf("iteration %d: %d results, %d UNKNOWN, err=%v, output=%q; want exactly one disposition", i, results, unknowns, execErr, output)
		}
	}
	t.Logf("completion won %d times, Close won %d times", sawResult, sawUnknown)
}
