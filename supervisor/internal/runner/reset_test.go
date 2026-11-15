package runner

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// orderLog is shared by a fake trial transport and a fake inner executor so that a test
// can see the order of the guest calls.
type orderLog struct {
	mu     sync.Mutex
	events []string
}

func (l *orderLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *orderLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type trialTransport struct {
	log  *orderLog
	exit func(argv []string) int
	err  error
	argv [][]string
	// stderr replaces the default refusal text of a non-zero exit.
	stderr []byte
}

func (t *trialTransport) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	t.log.add(strings.Join(argv, " "))
	t.argv = append(t.argv, append([]string(nil), argv...))
	if t.err != nil {
		return nil, nil, 0, t.err
	}
	code := 0
	if t.exit != nil {
		code = t.exit(argv)
	}
	if code != 0 && t.stderr != nil {
		return nil, t.stderr, code, nil
	}
	if code != 0 {
		return nil, []byte("fixture digest mismatch"), code, nil
	}
	return []byte(`{"schema":"x"}` + "\n"), nil, 0, nil
}

type loggedInner struct {
	log     *orderLog
	prepErr error
	execErr error
}

func (l *loggedInner) Prepare(context.Context, Step) error {
	l.log.add("inner.prepare")
	return l.prepErr
}
func (l *loggedInner) Execute(_ context.Context, s Step) (Outcome, error) {
	l.log.add("inner.execute")
	return defaultOutcome(Config{}, s), l.execErr
}

func resetFixture() (*orderLog, *trialTransport, *loggedInner, ResetExecutor) {
	log := &orderLog{}
	tr := &trialTransport{log: log}
	in := &loggedInner{log: log}
	return log, tr, in, ResetExecutor{Inner: in, Transport: tr}
}

func TestResetPrepareChecksAfterTheInnerPrepare(t *testing.T) {
	log, tr, _, r := resetFixture()
	st := advStep(CfgC, RoleTrajectory)
	if err := r.Prepare(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	want := []string{"inner.prepare", "reset adv-p --check"}
	if got := log.get(); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if !reflect.DeepEqual(tr.argv, [][]string{{"reset", "adv-p", "--check"}}) {
		t.Fatalf("argv %v", tr.argv)
	}
}

func TestResetPrepareFailuresVoidAndSendNothingMore(t *testing.T) {
	// The inner refusal comes first and the guest is not asked.
	log, _, in, r := resetFixture()
	in.prepErr = ErrNotWired
	if err := r.Prepare(context.Background(), advStep(CfgC, RoleTrajectory)); !errors.Is(err, ErrNotWired) {
		t.Fatalf("inner refusal: %v", err)
	}
	if got := log.get(); !slices.Equal(got, []string{"inner.prepare"}) {
		t.Fatalf("a refused inner Prepare must not reach the guest: %v", got)
	}
	// A failed check is a refusal of the side-effect-free phase.
	_, tr, _, r := resetFixture()
	tr.exit = func([]string) int { return 3 }
	if err := r.Prepare(context.Background(), advStep(CfgC, RoleTrajectory)); !errors.Is(err, ErrSetupRefused) || !strings.Contains(err.Error(), "exit 3") {
		t.Fatalf("check exit 3: %v", err)
	}
	_, tr, _, r = resetFixture()
	tr.err = errors.New("ssh down")
	if err := r.Prepare(context.Background(), advStep(CfgC, RoleTrajectory)); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("transport error: %v", err)
	}
	r.Transport = nil
	if err := r.Prepare(context.Background(), advStep(CfgC, RoleTrajectory)); !errors.Is(err, ErrNotWired) {
		t.Fatalf("no transport: %v", err)
	}
}

func TestResetExecuteResetsBeforeTheInnerExecute(t *testing.T) {
	log, tr, _, r := resetFixture()
	out, err := r.Execute(context.Background(), advStep(CfgC, RoleStaticReplay))
	if err != nil || !hasOutcome(out) {
		t.Fatalf("%+v %v", out, err)
	}
	if got := log.get(); !slices.Equal(got, []string{"reset adv-p", "inner.execute"}) {
		t.Fatalf("order %v", got)
	}
	if !reflect.DeepEqual(tr.argv, [][]string{{"reset", "adv-p"}}) {
		t.Fatalf("argv %v (the execute reset has no --check)", tr.argv)
	}
}

func TestResetFailureIsASetupErrorAndTheInnerNeverRuns(t *testing.T) {
	log, tr, _, r := resetFixture()
	tr.exit = func(argv []string) int {
		if !slices.Contains(argv, "--check") {
			return 1
		}
		return 0
	}
	out, err := r.Execute(context.Background(), advStep(CfgC, RoleTrajectory))
	if !errors.Is(err, ErrSetupRefused) || hasOutcome(out) {
		t.Fatalf("a reset failure is a setup failure with no outcome: %+v %v", out, err)
	}
	if got := log.get(); !slices.Equal(got, []string{"reset adv-p"}) {
		t.Fatalf("the inner executor must not run after a failed reset: %v", got)
	}
	// An inner outcome and error pass through unchanged.
	_, _, in, r := resetFixture()
	in.execErr = errors.New("inner failed")
	if out, err := r.Execute(context.Background(), advStep(CfgC, RoleTrajectory)); err == nil || !hasOutcome(out) {
		t.Fatalf("inner partial outcome lost: %+v %v", out, err)
	}
}

func TestResetFailureSpendsTheUnitThroughTheRunner(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	log := &orderLog{}
	tr := &trialTransport{log: log, exit: func(argv []string) int {
		if !slices.Contains(argv, "--check") && argv[1] == s.Steps[0].CaseID {
			return 1
		}
		return 0
	}}
	fake := NewFakeExecutor(s.Config)
	j, rep := runAll(t, s, t.TempDir(), ResetExecutor{Inner: fake, Transport: tr})
	defer j.Close()
	if rep.SetupFailures == 0 || rep.UnitsUsed != rep.Executed {
		t.Fatalf("a failed reset must spend its unit as a setup failure: %+v", rep)
	}
	for _, c := range fake.Calls() {
		if c.Index == 0 {
			t.Fatal("step 0 reached the inner executor after its reset failed")
		}
	}
	for _, e := range j.Entries() {
		if e.Kind == EntryDone && e.Index == 0 && !strings.Contains(e.SetupFailure, "reset") {
			t.Fatalf("setup failure %q does not name the reset", e.SetupFailure)
		}
	}
}

// The guest refuses `reset --check` while an acceptance or V cell container is left over (procoracle
// stated limit F1). The refusal is a Prepare refusal: the begin is voided, no unit is spent, no request is
// sent and the run stops with an error and a journal reason that name the container.
func TestLeftoverContainerRefusalStopsTheRunBeforeAnyRequest(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	const name = "tbound-v-0123456789abcdef"
	tr := &trialTransport{log: &orderLog{}, exit: func(argv []string) int {
		if slices.Contains(argv, "--check") {
			return 2
		}
		return 0
	}, stderr: []byte("tbound trial: refusing: leftover service container(s) " + name +
		": an acceptance or V cell outside the cells cgroup is scored as a cell escape (procoracle F1); stop and report, its removal is owner-supervised\n" +
		"usage: tbound trial reset <case_id> [--check]   (root only; one JSON document on stdout)\n")}
	fake := NewFakeExecutor(s.Config)
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	rep, err := mustRunner(t, s, j, ResetExecutor{Inner: fake, Transport: tr}, Options{}).Run(context.Background())
	if !errors.Is(err, ErrSetupRefused) || !strings.Contains(err.Error(), name) {
		t.Fatalf("run error %v; want a setup refusal naming %s", err, name)
	}
	if rep.UnitsUsed != 0 || len(fake.Calls()) != 0 || len(tr.argv) != 1 {
		t.Fatalf("units %d, executed %v, guest calls %v; want nothing spent, run or reset", rep.UnitsUsed, fake.Calls(), tr.argv)
	}
	entries := j.Entries()
	if last := entries[len(entries)-1]; last.Kind != EntryVoid || !strings.Contains(last.Reason, name) {
		t.Fatalf("last journal entry %+v; want a void naming %s", last, name)
	}
}

func TestDefaultTrialCommandAndLauncherValidation(t *testing.T) {
	if got := DefaultTrialCommand(); !slices.Equal(got, []string{EvidenceBinaryPath, "trial"}) {
		t.Fatalf("%v", got)
	}
	ssh := SSHTransport{SSHPath: "ssh", IdentityFile: "k", User: "root", Host: "10.0.0.2", HostKeyAlias: "guest", KnownHostsFile: "kh"}
	if err := (ResetExecutor{Inner: &loggedInner{log: &orderLog{}}, Transport: ssh}).Prepare(context.Background(), advStep(CfgC, RoleTrajectory)); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("the client launcher must not serve as the trial channel: %v", err)
	}
	ssh.ClientCommand = DefaultTrialCommand()
	if err := (ResetExecutor{Transport: ssh}).Validate(); err != nil {
		t.Fatalf("trial launcher: %v", err)
	}
}
