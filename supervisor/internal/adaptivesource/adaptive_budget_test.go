package adaptivesource

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"tbound/supervisor/internal/attacker"
	"tbound/supervisor/internal/providerbridge"
)

// fakeClock advances only when a test says so, so a source's wall time is exact and its state digest never
// depends on the machine running the test.
type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time { return c.at }
func (c *fakeClock) advance(ms int64) {
	c.at = c.at.Add(time.Duration(ms) * time.Millisecond)
}

func newFakeClock() *fakeClock { return &fakeClock{at: time.Unix(1_800_000_000, 0)} }

// newTestSource is NewAdaptiveSource on a frozen clock unless the test passes its own.
func newTestSource(spec attacker.Spec, pivots map[string]providerbridge.ScriptTurn, options ...AdaptiveOption) (*AdaptiveSource, error) {
	frozen := newFakeClock()
	return NewAdaptiveSource(spec, pivots, append([]AdaptiveOption{WithClock(frozen.now)}, options...)...)
}

// driveDenied issues pivots until the source stops, answering each with a policy denial after the given
// wall time, and returns the steps issued.
func driveDenied(t *testing.T, source *AdaptiveSource, clock *fakeClock, windowMS int64) []string {
	t.Helper()
	history := []providerbridge.ScriptMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
	var steps []string
	for n := 1; n <= 40; n++ {
		turn, err := source.Next(append([]providerbridge.ScriptMessage(nil), history...))
		if err != nil {
			t.Fatalf("turn %d: %v", n, err)
		}
		if turn.Tool == "" {
			return steps
		}
		steps = append(steps, stepOf(t, turn))
		clock.advance(windowMS)
		history = append(history, providerbridge.ScriptMessage{Role: "assistant", Content: "call"}, resultMessage(t, n, "DENY", "policy_rule_deny", ""))
	}
	t.Fatal("the source never stopped")
	return nil
}

// Retries share one budget: the machine of attempt 2 gets the artifact caps minus what attempt 1 spent, so
// the two attempts together never issue more pivots than the cap, and the usage reports are cumulative.
func TestAdaptiveSourceOfARetryStartsWithTheRemainingBudget(t *testing.T) {
	spec := adaptiveSpec(5)
	clock := newFakeClock()
	first, err := NewAdaptiveSource(spec, adaptivePivots(spec), WithClock(clock.now))
	if err != nil {
		t.Fatal(err)
	}
	// attempt 1 is cut off (an outage) after three pivots: its terminal is open
	history := []providerbridge.ScriptMessage{{Role: "user", Content: "u"}}
	for n := 1; n <= 3; n++ {
		if _, err := first.Next(append([]providerbridge.ScriptMessage(nil), history...)); err != nil {
			t.Fatal(err)
		}
		clock.advance(100)
		history = append(history, providerbridge.ScriptMessage{Role: "assistant", Content: "call"}, resultMessage(t, n, "DENY", "policy_rule_deny", ""))
	}
	if _, err := first.Next(history); err != nil {
		t.Fatal(err)
	}
	spent := first.Terminal()
	if spent.Stop != AdaptiveStopOpen || spent.Usage.Iterations != 4 || spent.Usage.ToolCalls != 4 || spent.Usage.WallMS != 300 ||
		spent.Cumulative != spent.Usage {
		t.Fatalf("attempt 1 terminal = %+v", spent)
	}
	// attempt 2 gets 5 - 4 = 1 pivot, whatever happens
	clock2 := newFakeClock()
	second, err := NewAdaptiveSource(spec, adaptivePivots(spec), WithClock(clock2.now), WithPriorUsage(spent.Usage))
	if err != nil {
		t.Fatal(err)
	}
	steps := driveDenied(t, second, clock2, 50)
	terminal := second.Terminal()
	if len(steps) != 1 || terminal.Stop != AdaptiveStopCap || terminal.PivotsIssued != 1 {
		t.Fatalf("attempt 2 issued %v, terminal %+v; want exactly the 1 remaining pivot and a cap stop", steps, terminal)
	}
	want := AdaptiveUsage{Iterations: 5, ToolCalls: 5, WallMS: 350}
	if terminal.Usage.Iterations != 1 || terminal.Cumulative != want {
		t.Fatalf("usage %+v cumulative %+v, want cumulative %+v", terminal.Usage, terminal.Cumulative, want)
	}
	// the machine restarts from its initial state: the retry's first step is the fresh run's first step
	fresh, _ := newTestSource(spec, adaptivePivots(spec))
	freshSteps := driveDenied(t, fresh, newFakeClock(), 0)
	if steps[0] != freshSteps[0] {
		t.Fatalf("the retry starts at %s, the first attempt at %s", steps[0], freshSteps[0])
	}
}

func TestAdaptiveSourceRefusesARetryWithNoBudgetLeft(t *testing.T) {
	spec := adaptiveSpec(5)
	for name, prior := range map[string]AdaptiveUsage{
		"iterations": {Iterations: 5, ToolCalls: 1, WallMS: 1},
		"tool calls": {Iterations: 1, ToolCalls: 100, WallMS: 1},
		"wall":       {Iterations: 1, ToolCalls: 1, WallMS: 60000},
	} {
		if _, err := newTestSource(spec, adaptivePivots(spec), WithPriorUsage(prior)); !errors.Is(err, ErrAdaptiveBudgetExhausted) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := newTestSource(spec, adaptivePivots(spec), WithPriorUsage(AdaptiveUsage{Iterations: -1})); err == nil {
		t.Error("a negative prior usage was accepted")
	}
}

// The artifact's wall cap is enforced: once the cumulative wall time reaches it the source issues no further
// pivot and stops as "cap". The cap counts the earlier attempts' wall time.
func TestAdaptiveSourceEnforcesTheWallCap(t *testing.T) {
	spec := adaptiveSpec(6)
	spec.Caps.WallMS = 1000
	clock := newFakeClock()
	source, err := NewAdaptiveSource(spec, adaptivePivots(spec), WithClock(clock.now))
	if err != nil {
		t.Fatal(err)
	}
	steps := driveDenied(t, source, clock, 400) // 400 ms per pivot: the cap is reached after the third result
	terminal := source.Terminal()
	if len(steps) != 3 || terminal.Stop != AdaptiveStopCap || terminal.Usage.WallMS != 1200 {
		t.Fatalf("issued %v, terminal %+v; want 3 pivots and a cap stop at 1200 ms", steps, terminal)
	}
	// with 800 ms already spent by an earlier attempt, one 400 ms pivot exhausts the cap
	clock2 := newFakeClock()
	retry, err := NewAdaptiveSource(spec, adaptivePivots(spec), WithClock(clock2.now), WithPriorUsage(AdaptiveUsage{Iterations: 2, ToolCalls: 2, WallMS: 800}))
	if err != nil {
		t.Fatal(err)
	}
	steps = driveDenied(t, retry, clock2, 400)
	if len(steps) != 1 || retry.Terminal().Stop != AdaptiveStopCap || retry.Terminal().Cumulative.WallMS != 1200 {
		t.Fatalf("retry issued %v, terminal %+v", steps, retry.Terminal())
	}
}

// Wall time is measured by the source but never charged to the machine: the state digest is a function of
// the feedback sequence alone, so a verifier can replay it, and it does not depend on how slow a run was.
func TestAdaptiveStateDigestDoesNotDependOnTheClock(t *testing.T) {
	spec := adaptiveSpec(4)
	digest := func(windowMS int64) (string, []string) {
		clock := newFakeClock()
		source, _ := NewAdaptiveSource(spec, adaptivePivots(spec), WithClock(clock.now))
		steps := driveDenied(t, source, clock, windowMS)
		return source.Terminal().StateDigest, steps
	}
	fast, fastSteps := digest(1)
	slow, slowSteps := digest(200)
	if fast != slow || !reflect.DeepEqual(fastSteps, slowSteps) {
		t.Fatalf("the clock changed the run: %s %v vs %s %v", fast, fastSteps, slow, slowSteps)
	}
}
