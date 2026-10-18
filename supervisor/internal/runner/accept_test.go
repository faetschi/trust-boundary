package runner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
)

// scriptedTransport answers every call from reply and records the argument vectors.
type scriptedTransport struct {
	mu    sync.Mutex
	reply func(argv []string) (stdout, stderr []byte, exit int, err error)
	argv  [][]string
}

func (t *scriptedTransport) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	t.mu.Lock()
	t.argv = append(t.argv, append([]string(nil), argv...))
	t.mu.Unlock()
	return t.reply(argv)
}

func (t *scriptedTransport) calls() [][]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([][]string(nil), t.argv...)
}

// fixedInner returns a fixed outcome and error and counts its calls.
type fixedInner struct {
	out      Outcome
	err      error
	prepErr  error
	prepared int
	executed int
}

func (s *fixedInner) Prepare(context.Context, Step) error { s.prepared++; return s.prepErr }
func (s *fixedInner) Execute(context.Context, Step) (Outcome, error) {
	s.executed++
	return s.out, s.err
}

func acceptJSON(t *testing.T, caseID string, passed bool) []byte {
	t.Helper()
	doc := taskaccept.AcceptDoc{Schema: taskaccept.ResultSchema, CaseID: caseID, SpecDigest: "sha256:" + strings.Repeat("a", 64), Passed: passed,
		Results: []taskaccept.Result{{Op: "contains", Path: "f", Passed: passed, Detail: "d"}, {Op: "unchanged_except", Path: ".", Passed: true, Detail: "d"}}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func compliantStep(cfg Configuration) Step {
	return Step{Index: 3, CaseID: "cmp-1", Kind: evalcorpus.KindCompliant, Configuration: cfg, Role: RoleCompliant, Attempt: 1}
}

func TestAcceptExecutorSetsTheTaskFromTheGuestResult(t *testing.T) {
	for _, cfg := range []Configuration{CfgB, CfgC} {
		for _, passed := range []bool{true, false} {
			tr := &scriptedTransport{reply: func([]string) ([]byte, []byte, int, error) { return acceptJSON(t, "cmp-1", passed), nil, 0, nil }}
			inner := &fixedInner{out: Outcome{E01: E01Identity{Digest: "d"}, Task: TaskPass}} // an inner claim is never trusted
			got, err := AcceptExecutor{Inner: inner, Transport: tr}.Execute(context.Background(), compliantStep(cfg))
			want := TaskFail
			if passed {
				want = TaskPass
			}
			if err != nil || got.Task != want || got.E01.Digest != "d" {
				t.Fatalf("%s passed=%v: task %q err %v", cfg, passed, got.Task, err)
			}
			if calls := tr.calls(); !reflect.DeepEqual(calls, [][]string{{"accept", "cmp-1", "--tree", "live"}}) {
				t.Fatalf("calls = %v", calls)
			}
		}
	}
}

func TestAcceptExecutorNeverPassesWhenTheAcceptCallFails(t *testing.T) {
	pass := acceptJSON(t, "cmp-1", true)
	for name, reply := range map[string]func([]string) ([]byte, []byte, int, error){
		"transport error": func([]string) ([]byte, []byte, int, error) { return pass, nil, 0, errors.New("ssh: connection reset") },
		"exit non-zero":   func([]string) ([]byte, []byte, int, error) { return pass, []byte("refused"), 2, nil },
		"empty output":    func([]string) ([]byte, []byte, int, error) { return nil, nil, 0, nil },
		"not json":        func([]string) ([]byte, []byte, int, error) { return []byte("ok\n"), nil, 0, nil },
		"other schema": func([]string) ([]byte, []byte, int, error) {
			return []byte(strings.Replace(string(pass), taskaccept.ResultSchema, "x/v1", 1)), nil, 0, nil
		},
		"other case": func([]string) ([]byte, []byte, int, error) { return acceptJSON(t, "cmp-2", true), nil, 0, nil },
		"unknown member": func([]string) ([]byte, []byte, int, error) {
			return []byte(strings.Replace(string(pass), `"passed":true}`, `"passed":true,"extra":1}`, 1)), nil, 0, nil
		},
		"trailing data": func([]string) ([]byte, []byte, int, error) {
			return append(append([]byte(nil), pass...), []byte(`{"passed":true}`)...), nil, 0, nil
		},
		"no results": func([]string) ([]byte, []byte, int, error) {
			return []byte(`{"schema":"` + taskaccept.ResultSchema + `","case_id":"cmp-1","spec_digest":"x","results":[],"passed":true}`), nil, 0, nil
		},
		"passed with a failed check": func([]string) ([]byte, []byte, int, error) {
			return []byte(`{"schema":"` + taskaccept.ResultSchema + `","case_id":"cmp-1","spec_digest":"x","results":[{"op":"absent","path":"f","passed":false,"detail":"d"}],"passed":true}`), nil, 0, nil
		},
	} {
		inner := &fixedInner{out: Outcome{E01: E01Identity{Digest: "d"}}}
		got, err := AcceptExecutor{Inner: inner, Transport: &scriptedTransport{reply: reply}}.Execute(context.Background(), compliantStep(CfgC))
		if err != nil || got.Task != TaskLost {
			t.Errorf("%s: task %q err %v, want %q (never pass)", name, got.Task, err, TaskLost)
		}
	}
}

func TestAcceptExecutorLeavesOtherStepsAndFailedRunsAlone(t *testing.T) {
	tr := &scriptedTransport{reply: func([]string) ([]byte, []byte, int, error) { return acceptJSON(t, "cmp-1", true), nil, 0, nil }}
	for name, s := range map[string]Step{
		"V":         compliantStep(CfgV),
		"P":         compliantStep(CfgP),
		"ambiguous": {CaseID: "cmp-1", Kind: evalcorpus.KindAmbiguous, Configuration: CfgC, Role: RoleAmbiguous, Attempt: 1},
		"adversary": {CaseID: "cmp-1", Kind: evalcorpus.KindAdversarial, Configuration: CfgC, Role: RoleTrajectory, Attempt: 1},
	} {
		got, err := AcceptExecutor{Inner: &fixedInner{}, Transport: tr}.Execute(context.Background(), s)
		if err != nil || got.Task != "" {
			t.Errorf("%s: task %q err %v", name, got.Task, err)
		}
	}
	if len(tr.calls()) != 0 {
		t.Fatalf("accept was called for steps without an acceptance artifact: %v", tr.calls())
	}
	// A run that failed keeps its error and partial outcome and is not judged.
	boom := errors.New("governed run completed without success")
	inner := &fixedInner{out: Outcome{E01: E01Identity{Digest: "d"}}, err: boom}
	got, err := AcceptExecutor{Inner: inner, Transport: tr}.Execute(context.Background(), compliantStep(CfgC))
	if !errors.Is(err, boom) || got.Task != "" || len(tr.calls()) != 0 {
		t.Fatalf("task %q err %v calls %v", got.Task, err, tr.calls())
	}
}

func TestAcceptExecutorReportsACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &scriptedTransport{reply: func([]string) ([]byte, []byte, int, error) { cancel(); return nil, nil, 0, ctx.Err() }}
	got, err := AcceptExecutor{Inner: &fixedInner{out: Outcome{E01: E01Identity{Digest: "d"}}}, Transport: tr}.Execute(ctx, compliantStep(CfgC))
	if !errors.Is(err, context.Canceled) || got.Task == TaskPass {
		t.Fatalf("task %q err %v", got.Task, err)
	}
}

func TestAcceptExecutorPrepare(t *testing.T) {
	ok := &scriptedTransport{reply: func([]string) ([]byte, []byte, int, error) { return nil, nil, 0, nil }}
	inner := &fixedInner{}
	if err := (AcceptExecutor{Inner: inner, Transport: ok}).Prepare(context.Background(), compliantStep(CfgC)); err != nil || inner.prepared != 1 || len(ok.calls()) != 0 {
		t.Fatalf("Prepare = %v, prepared %d, calls %v", err, inner.prepared, ok.calls())
	}
	inner = &fixedInner{prepErr: ErrSetupRefused}
	if err := (AcceptExecutor{Inner: inner, Transport: ok}).Prepare(context.Background(), compliantStep(CfgC)); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("inner refusal lost: %v", err)
	}
	if err := (AcceptExecutor{Inner: &fixedInner{}}).Prepare(context.Background(), compliantStep(CfgC)); !errors.Is(err, ErrNotWired) {
		t.Fatalf("no transport: %v", err)
	}
	if err := (AcceptExecutor{Transport: ok}).Prepare(context.Background(), compliantStep(CfgC)); !errors.Is(err, ErrNotWired) {
		t.Fatalf("no inner: %v", err)
	}
	if _, err := (AcceptExecutor{Transport: ok}).Execute(context.Background(), compliantStep(CfgC)); !errors.Is(err, ErrNotWired) {
		t.Fatalf("no inner (Execute): %v", err)
	}
	// The transport must be the trial transport.
	bad := SSHTransport{SSHPath: "ssh", IdentityFile: "k", User: "root", Host: "h", HostKeyAlias: "a", KnownHostsFile: "/kh"}
	if err := (AcceptExecutor{Inner: &fixedInner{}, Transport: bad}).Prepare(context.Background(), compliantStep(CfgC)); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("a client-command transport was accepted: %v", err)
	}
	good := bad
	good.ClientCommand = DefaultTrialCommand()
	if err := (AcceptExecutor{Inner: &fixedInner{}, Transport: good}).Prepare(context.Background(), compliantStep(CfgC)); err != nil {
		t.Fatalf("trial transport refused: %v", err)
	}
}

func TestAcceptExecutorBehindTheResetWrapper(t *testing.T) {
	// The stack the host builds: reset first, then the governed run, then accept.
	log := &orderLog{}
	trial := &trialTransport{log: log}
	trial2 := &scriptedTransport{reply: func(argv []string) ([]byte, []byte, int, error) {
		log.add(strings.Join(argv, " "))
		return acceptJSON(t, "cmp-1", true), nil, 0, nil
	}}
	inner := &loggedInner{log: log}
	stack := AcceptExecutor{Inner: ResetExecutor{Inner: inner, Transport: trial}, Transport: trial2}
	got, err := stack.Execute(context.Background(), compliantStep(CfgC))
	if err != nil || got.Task != TaskPass {
		t.Fatalf("task %q err %v", got.Task, err)
	}
	events := log.get()
	if len(events) < 3 || events[0] != "reset cmp-1" || events[len(events)-1] != "accept cmp-1 --tree live" {
		t.Fatalf("order = %v", events)
	}
}
