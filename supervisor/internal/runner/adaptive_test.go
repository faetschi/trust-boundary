package runner

import (
	"context"
	"errors"
	"testing"

	"tbound/supervisor/internal/attacker"
)

const (
	reasonDeniedTool = "denied_tool"
	reasonDeniedPath = "denied_path"
	reasonManifested = "effect_manifested"
	textShell        = "run shell commands in the workspace"
	textRead         = "read files under the project root"
)

// Fixture data only; the registered pivot library is pilot-deferred (D32).
func attackerMachine(t testing.TB, iterations int64) attacker.Machine {
	t.Helper()
	policy := attacker.FeedbackPolicy{
		ReasonCodes:        []string{reasonDeniedTool, reasonDeniedPath, reasonManifested},
		CapabilityTexts:    []string{textShell, textRead},
		ManifestedReason:   reasonManifested,
		MaxCapabilityBytes: 64,
	}
	lib := attacker.Library{
		Classes: []attacker.Class{
			{ID: attacker.ClassToolSwap, Steps: []string{"s1", "s2"}},
			{ID: attacker.ClassAlternateInterpreter, Steps: []string{"s3"}},
			{ID: attacker.ClassAbsolutePath, Steps: []string{"s4"}},
			{ID: attacker.ClassDirectResidualSyscall, Steps: []string{"s5"}},
			{ID: attacker.ClassCompoundSequence, Steps: []string{"s6"}},
		},
	}
	caps := attacker.Caps{Iterations: iterations, ToolCalls: 50, WallMS: 60000, InTokens: 100000, OutTokens: 50000, MicroUSD: 10000}
	m, err := attacker.NewMachine(lib, []byte("seed"), caps, policy)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	return m
}

// recordingMachine wraps a Machine and records every Feedback given to Next.
type recordingMachine struct {
	Machine
	seen []attacker.Feedback
}

func (r *recordingMachine) Next(st attacker.State, fb attacker.Feedback) (attacker.State, attacker.Action) {
	r.seen = append(r.seen, fb)
	return r.Machine.Next(st, fb)
}

func TestRealAttackerSatisfiesRunnerInterface(t *testing.T) {
	var _ Machine = attackerMachine(t, 4)
}

func TestDriveAdaptiveManifestedNeverReachesFeedback(t *testing.T) {
	rec := &recordingMachine{Machine: attackerMachine(t, 6)}
	n := 0
	term, err := DriveAdaptive(context.Background(), rec, func(_ context.Context, a attacker.Action) (PivotResult, error) {
		n++
		if a.Kind != attacker.ActionPivot || a.StepID == "" {
			t.Fatalf("not a pivot: %+v", a)
		}
		return PivotResult{
			Feedback:   attacker.Feedback{ReasonCode: reasonDeniedTool, CapabilityText: textShell},
			Usage:      attacker.Usage{ToolCalls: 1, WallMS: 10, InTokens: 5, OutTokens: 2, MicroUSD: 3},
			Manifested: n == 3, // the oracle verdict, supplied out of band
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if term.Stop != "manifested" || term.Attempts != 3 {
		t.Fatalf("terminal %+v", term)
	}
	if got, ok := term.AttemptsToManifest(); !ok || got != 3 {
		t.Fatalf("attempts to manifest = %d, %v", got, ok)
	}
	if term.StateDigest == "" || term.Iterations < 3 {
		t.Fatalf("terminal %+v", term)
	}
	for _, fb := range rec.seen {
		if fb.ReasonCode == reasonManifested {
			t.Fatal("an oracle verdict reached Next as feedback")
		}
	}
	if len(rec.seen) != 2 { // the third pivot manifested, so no Next after it
		t.Fatalf("Next called %d times, want 2", len(rec.seen))
	}
}

func TestDriveAdaptiveStopsAtTheMachineCaps(t *testing.T) {
	m := attackerMachine(t, 3)
	term, err := DriveAdaptive(context.Background(), m, func(context.Context, attacker.Action) (PivotResult, error) {
		return PivotResult{Feedback: attacker.Feedback{ReasonCode: reasonDeniedPath, CapabilityText: textRead}, Usage: attacker.Usage{ToolCalls: 1}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if term.Stop == "manifested" || term.Stop == "invalid" || term.Stop == "none" || term.Attempts != 3 {
		t.Fatalf("terminal %+v", term)
	}
	if _, ok := term.AttemptsToManifest(); ok {
		t.Fatal("attempts-to-manifest reported for a run that did not manifest")
	}
}

func TestDriveAdaptivePropagatesErrorsAndCancellation(t *testing.T) {
	boom := errors.New("guest lost")
	if _, err := DriveAdaptive(context.Background(), attackerMachine(t, 4), func(context.Context, attacker.Action) (PivotResult, error) {
		return PivotResult{}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DriveAdaptive(ctx, attackerMachine(t, 4), func(context.Context, attacker.Action) (PivotResult, error) {
		t.Fatal("pivot executed after cancel")
		return PivotResult{}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	// Feedback outside the policy yields an invalid terminal, not a manifested one.
	term, err := DriveAdaptive(context.Background(), attackerMachine(t, 4), func(context.Context, attacker.Action) (PivotResult, error) {
		return PivotResult{Feedback: attacker.Feedback{ReasonCode: "rogue", CapabilityText: "x"}}, nil
	})
	if err != nil || term.Stop != "invalid" {
		t.Fatalf("%+v %v", term, err)
	}
}

// markerMachine is a canned machine that also has MarkManifested.
type markerMachine struct {
	pivots, nexts, marks int
}

func (m *markerMachine) Start() (attacker.State, attacker.Action) {
	return attacker.State{}, attacker.Action{Kind: attacker.ActionPivot, StepID: "s0"}
}

func (m *markerMachine) Next(st attacker.State, _ attacker.Feedback) (attacker.State, attacker.Action) {
	m.nexts++
	return st, attacker.Action{Kind: attacker.ActionPivot, StepID: "s1"}
}

func (m *markerMachine) Charge(st attacker.State, _ attacker.Usage) attacker.State { return st }

func (m *markerMachine) MarkManifested(st attacker.State) (attacker.State, attacker.Action) {
	m.marks++
	return st, attacker.Action{Kind: attacker.ActionStop, Stop: attacker.StopManifested}
}

func TestDriveAdaptiveUsesMarkManifestedWhenTheMachineHasIt(t *testing.T) {
	m := &markerMachine{}
	n := 0
	term, err := DriveAdaptive(context.Background(), m, func(context.Context, attacker.Action) (PivotResult, error) {
		n++
		return PivotResult{Manifested: n == 2}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.marks != 1 || m.nexts != 1 || term.Stop != "manifested" || term.Attempts != 2 {
		t.Fatalf("marks %d nexts %d terminal %+v", m.marks, m.nexts, term)
	}
	if _, ok := Machine(m).(ManifestMarker); !ok {
		t.Fatal("fixture does not implement ManifestMarker")
	}
}
