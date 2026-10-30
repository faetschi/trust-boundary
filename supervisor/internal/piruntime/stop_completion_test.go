package piruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

type secondDoneObservedContext struct {
	context.Context
	calls   int
	waiting chan struct{}
}

func (c *secondDoneObservedContext) Done() <-chan struct{} {
	// Stop and its completion wait evaluate Done on the same goroutine.
	c.calls++
	if c.calls == 2 {
		close(c.waiting)
	}
	return c.Context.Done()
}

func processWithCompletedKillError(settler DescendantSettler, killErr error) *Process {
	done := make(chan struct{})
	close(done)
	return &Process{
		cmd:     &exec.Cmd{Process: &os.Process{}},
		settler: settler, completion: make(chan struct{}),
		stopStarted: true, stopDone: done, stopErr: killErr,
		settlement: Settlement{State: SettlementRunning},
	}
}

func completeSyntheticProcessExit(p *Process) {
	p.waitMu.Lock()
	p.waitResult = waitResult{}
	close(p.completion)
	p.waitMu.Unlock()
}

func TestStopWaitsForExitEvidenceAfterKillError(t *testing.T) {
	for _, observed := range []bool{true, false} {
		t.Run(map[bool]string{true: "delayed exit", false: "deadline then retry"}[observed], func(t *testing.T) {
			killErr := errors.New("TerminateProcess: Access is denied")
			settler := &countingSettler{}
			p := processWithCompletedKillError(settler, killErr)
			base, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ctx := &secondDoneObservedContext{Context: base, waiting: make(chan struct{})}
			type outcome struct {
				settlement Settlement
				err        error
			}
			result := make(chan outcome, 1)
			go func() { s, err := p.Stop(ctx); result <- outcome{s, err} }()
			select {
			case <-ctx.waiting:
			case got := <-result:
				t.Fatalf("returned before bounded exit wait: %+v", got)
			case <-base.Done():
				t.Fatal("never entered bounded exit wait")
			}
			if observed {
				completeSyntheticProcessExit(p)
			}
			got := <-result
			if !observed {
				if !errors.Is(got.err, killErr) || !errors.Is(got.err, context.DeadlineExceeded) ||
					got.settlement.State != SettlementUnknown || got.settlement.ExitObserved || got.settlement.DescendantsSettled || settler.Calls() != 0 {
					t.Fatalf("unobserved exit did not fail closed: %+v", got)
				}
				completeSyntheticProcessExit(p)
				got.settlement, got.err = p.Stop(context.Background())
			}
			if got.err != nil || got.settlement.State != SettlementStopped || !got.settlement.ExitObserved || !got.settlement.DescendantsSettled || settler.Calls() != 1 {
				t.Fatalf("observed exit did not settle with descendant proof: %+v calls=%d", got, settler.Calls())
			}
		})
	}
}
