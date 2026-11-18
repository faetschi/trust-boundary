package runner

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"tbound/supervisor/internal/evalcorpus"
)

// fakeGuest is the governed service and the trial command of one guest. Like the real
// service, it keeps one terminal per request id and answers a request id it has
// settled with that stored line instead of running a second workflow. dieAfterRun
// makes the host process die after the guest settled the request: the answer never
// reaches the runner.
type fakeGuest struct {
	mu          sync.Mutex
	stored      map[string][]byte
	workflows   int
	resets      []string
	dieAfterRun bool
	running     int // answers busy this many more times for a request id that has no terminal yet
}

type guestClient struct{ g *fakeGuest }
type guestTrial struct{ g *fakeGuest }

func (c guestClient) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	g := c.g
	g.mu.Lock()
	id := argv[slices.Index(argv, "--task-request-id")+1]
	line, settled := g.stored[id]
	if !settled && g.running > 0 {
		g.running--
		g.mu.Unlock()
		return busyLine(id), nil, 2, nil
	}
	if !settled {
		g.workflows++
		receipt := map[string]any{"schema_version": governedReceiptSchema, "run_id": "production-run-guest", "success": true,
			"tool_sequence": []string{"read"}}
		raw, _ := json.Marshal(map[string]any{"schema_version": governedResponseSchema, "request_id": id, "status": "completed", "receipt": receipt})
		line = append(raw, '\n')
		g.stored[id] = line
	}
	die := g.dieAfterRun
	g.dieAfterRun = false
	g.mu.Unlock()
	if die {
		panic("host process died")
	}
	return line, nil, 0, nil
}

func busyLine(id string) []byte {
	raw, _ := json.Marshal(map[string]any{"schema_version": governedResponseSchema, "request_id": id, "status": "busy", "error_code": "one-task-at-a-time"})
	return append(raw, '\n')
}

func (t guestTrial) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	t.g.mu.Lock()
	defer t.g.mu.Unlock()
	if argv[0] == "reset" && !slices.Contains(argv, "--check") {
		t.g.resets = append(t.g.resets, argv[1])
	}
	return []byte("{}\n"), nil, 0, nil
}

func firstStepOf(t *testing.T, s Schedule, cfg Configuration, kind evalcorpus.Kind) Step {
	t.Helper()
	for _, st := range s.Steps {
		if st.Configuration == cfg && st.Kind == kind {
			return st
		}
	}
	t.Fatalf("no %s %s step", cfg, kind)
	return Step{}
}

// governedStack routes every C step through the reset wrapper and GovernedC to the fake guest,
// and every other configuration to FakeExecutor.
func governedStack(s Schedule, g *fakeGuest, dispatchDir string) Executors {
	fake := NewFakeExecutor(s.Config)
	c := ResetExecutor{Inner: GovernedC{PlanDigest: s.Digest, Transport: guestClient{g}, ResendPause: time.Millisecond, ResendWindow: time.Second},
		Transport: guestTrial{g}, DispatchDir: dispatchDir}
	return Executors{A: fake, B: fake, C: c, P: fake, V: fake}
}

// runUntilDeath runs s until the guest's dieAfterRun fires, as a host whose process ends mid-unit.
func runUntilDeath(t *testing.T, s Schedule, dir string, e Executor) {
	t.Helper()
	j := openJournal(t, dir, s)
	func() {
		defer func() {
			_ = j.Close()
			if recover() == nil {
				t.Fatal("the simulated host death did not happen")
			}
		}()
		_, _ = mustRunner(t, s, j, e, Options{}).Run(context.Background())
	}()
}

func TestResumedCompliantStepTakesTheGuestsStoredResult(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	st := firstStepOf(t, s, CfgC, evalcorpus.KindCompliant)
	g := &fakeGuest{stored: map[string][]byte{}}
	stack := governedStack(s, g, dir+"/dispatched")
	// The guest settles the request of st and the host dies before the answer arrives.
	stack.C = dieOn{Executor: stack.C, target: st, g: g}
	runUntilDeath(t, s, dir, stack)

	j := openJournal(t, dir, s) // the next process
	defer j.Close()
	rep, err := mustRunner(t, s, j, governedStack(s, g, dir+"/dispatched"), Options{}).Run(context.Background())
	if err != nil || !rep.Finished || rep.Abandoned != 0 || rep.UnitsUsed != 89 {
		t.Fatalf("report %+v %v", rep, err)
	}
	term, ok := j.Final(st.ID())
	if !ok || term.Kind != EntryDone || term.Outcome == nil || term.Outcome.Partial || term.SetupFailure != "" {
		t.Fatalf("terminal of the interrupted step = %+v, want done with the stored outcome", term)
	}
	req := GovernedRequestFor(s.Digest, Step{CaseID: st.CaseID, Role: st.Role, Configuration: st.Configuration, Repeat: st.Repeat, Attempt: 1})
	if term.Outcome.E01.Digest != digestOfLine(g.stored[req.RequestID][:len(g.stored[req.RequestID])-1]) {
		t.Fatalf("E01 %s is not the digest of the guest's stored terminal", term.Outcome.E01.Digest)
	}
	if !strings.Contains(term.Reason, "resumed") {
		t.Fatalf("reason %q does not record the resume", term.Reason)
	}
	// One workflow and one reset per C step: the resume neither reran the request nor reset
	// the tree the run left behind.
	cSteps := 0
	for _, x := range s.Steps {
		if x.Configuration == CfgC {
			cSteps++
		}
	}
	if g.workflows != cSteps || len(g.resets) != cSteps {
		t.Fatalf("guest ran %d workflows and %d resets for %d C steps", g.workflows, len(g.resets), cSteps)
	}
}

// dieOn arms the guest's death for the target step only.
type dieOn struct {
	Executor
	target Step
	g      *fakeGuest
}

func (d dieOn) Execute(ctx context.Context, s Step) (Outcome, error) {
	if s.ID() == d.target.ID() {
		d.g.mu.Lock()
		d.g.dieAfterRun = true
		d.g.mu.Unlock()
	}
	return d.Executor.Execute(ctx, s)
}

func TestResumedAdversarialStepStaysAbandoned(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	dir := t.TempDir()
	st := firstStepOf(t, s, CfgC, evalcorpus.KindAdversarial)
	g := &fakeGuest{stored: map[string][]byte{}}
	stack := governedStack(s, g, dir+"/dispatched")
	stack.C = dieOn{Executor: stack.C, target: st, g: g}
	runUntilDeath(t, s, dir, stack)

	j := openJournal(t, dir, s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, governedStack(s, g, dir+"/dispatched"), Options{}).Run(context.Background())
	if err != nil || rep.Abandoned != 1 || !rep.Finished {
		t.Fatalf("report %+v %v", rep, err)
	}
	if term, _ := j.Final(st.ID()); term.Kind != EntryAbandoned || term.Outcome != nil {
		t.Fatalf("terminal %+v, want abandoned without an outcome: its observers died with the process", term)
	}
}

// A process that died before its reset ended sent no request: the next process resets and
// sends it as a new one.
func TestResumedStepWithoutADispatchRecordIsResetAndSent(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	st := firstStepOf(t, s, CfgC, evalcorpus.KindCompliant)
	g := &fakeGuest{stored: map[string][]byte{}}
	run := st
	run.Resumed = true
	o, err := governedStack(s, g, t.TempDir()).C.Execute(context.Background(), run)
	if err != nil || o.Partial || g.workflows != 1 || len(g.resets) != 1 {
		t.Fatalf("outcome %+v err %v workflows %d resets %d", o, err, g.workflows, len(g.resets))
	}
	if _, err := (ResetExecutor{Inner: NewFakeExecutor(s.Config), Transport: guestTrial{g}}).Execute(context.Background(), run); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("a resume without a dispatch record directory was not refused: %v", err)
	}
}

func TestGovernedCSendsTheSameRequestAgainWhenTheAnswerIsLost(t *testing.T) {
	f := newGovernedFixture(t)
	line := f.respond("completed", "", f.receipt(true), 0)
	lost := &lossyTransport{inner: f.tr, lose: 2}
	f.g.Transport, f.g.ResendPause, f.g.ResendWindow = lost, time.Millisecond, time.Second
	o, err := f.g.Execute(context.Background(), f.step)
	if err != nil || o.Partial || o.E01.Digest != digestOfLine(line) {
		t.Fatalf("outcome %+v err %v", o, err)
	}
	want, _ := f.req.Argv()
	if len(lost.calls) != 3 || !slices.EqualFunc(lost.calls, [][]string{want, want, want}, slices.Equal) {
		t.Fatalf("calls %q, want the same request three times", lost.calls)
	}
}

func TestGovernedCWaitsForABusyServiceOnlyOnAResend(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	st := firstStepOf(t, s, CfgC, evalcorpus.KindCompliant)
	g := &fakeGuest{stored: map[string][]byte{}, running: 2}
	gc := GovernedC{PlanDigest: s.Digest, Transport: guestClient{g}, ResendPause: time.Millisecond, ResendWindow: time.Second}
	run := st
	run.Resumed = true
	if o, err := gc.Execute(context.Background(), run); err != nil || o.Partial || g.workflows != 1 {
		t.Fatalf("resumed: outcome %+v err %v workflows %d", o, err, g.workflows)
	}
	g = &fakeGuest{stored: map[string][]byte{}, running: 1}
	gc.Transport = guestClient{g}
	if _, err := gc.Execute(context.Background(), st); !errors.Is(err, ErrSetupRefused) || g.workflows != 0 {
		t.Fatalf("first send to a busy service: err %v workflows %d, want a setup failure and no workflow", err, g.workflows)
	}
}

// lossyTransport loses the first lose answers, as an ssh connection that drops: exit 255 and nothing on stdout.
type lossyTransport struct {
	inner Transport
	lose  int
	calls [][]string
}

func (l *lossyTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	l.calls = append(l.calls, slices.Clone(argv))
	if l.lose > 0 {
		l.lose--
		return nil, []byte("Connection reset by peer\n"), 255, nil
	}
	return l.inner.Run(ctx, argv)
}
