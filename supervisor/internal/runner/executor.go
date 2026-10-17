package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

// Executor runs one step to a terminal Outcome. The real implementations need
// the guest. An error means the step could not be run (a setup failure) and is
// journaled as one. ErrNotWired means the executor is a skeleton: the run stops
// and no unit is consumed. An executor must honor ctx and must not retry a
// failed step on its own except where Metrics.Retries records it.
type Executor interface {
	Execute(ctx context.Context, s Step) (Outcome, error)
}

// Executors routes a step to the executor of its configuration. A nil entry is
// an unwired configuration.
type Executors struct {
	A, B, C, P, V Executor
}

// Execute implements Executor.
func (e Executors) Execute(ctx context.Context, s Step) (Outcome, error) {
	x := e.of(s.Configuration)
	if x == nil {
		return Outcome{}, fmt.Errorf("%w: configuration %s", ErrNotWired, s.Configuration)
	}
	return x.Execute(ctx, s)
}

func (e Executors) of(c Configuration) Executor {
	switch c {
	case CfgA:
		return e.A
	case CfgB:
		return e.B
	case CfgC:
		return e.C
	case CfgP:
		return e.P
	case CfgV:
		return e.V
	}
	return nil
}

// Check reports ErrNotWired if any configuration used by s has no executor, so
// that the run stops before it starts a unit.
func (e Executors) Check(s Schedule) error {
	for _, st := range s.Steps {
		if e.of(st.Configuration) == nil {
			return fmt.Errorf("%w: configuration %s", ErrNotWired, st.Configuration)
		}
	}
	return nil
}

// GovernedRequest is the request of one governed C run to the registered local
// service, as `tbound serve --pi` sends it (infra/guest/PROVISIONING-SIGNING.md
// L3 to L5).
type GovernedRequest struct {
	// TaskID is `--task-id`; it must equal the installed config's
	// workflow_task_id.
	TaskID string
	// RequestID is `--task-request-id`. It is derived from the plan digest, the
	// step and the attempt, so replaying the same attempt returns the stored
	// terminal answer instead of starting a second workflow.
	RequestID string
}

// GovernedRequestFor derives the request of a step.
func GovernedRequestFor(planDigest string, s Step) GovernedRequest {
	sum := sha256.Sum256([]byte(planDigest + "\x00" + s.ID() + "\x00" + strconv.Itoa(s.Attempt)))
	return GovernedRequest{TaskID: s.CaseID, RequestID: "tbr-" + hex.EncodeToString(sum[:16])}
}

// GovernedC is the adapter skeleton for Configuration C (and the governed
// replays of C). It is not implemented: it needs the guest.
//
// The mapping it must implement:
//
//   - one C step is one `tbound serve --pi --task-id <TaskID> --task-request-id
//     <RequestID> --task <prompt> --bash-no-outputs` client invocation against
//     /run/tbound/serve-pi.sock, by a client uid and gid that the signed
//     registration allows;
//   - the response line (status completed, receipt.success, receipt.run_id,
//     provider_exchange) and the exit code give the terminal state; status
//     unknown or an exit other than 0 and 2 is a setup failure or an unresolved
//     outcome, never a clean negative;
//   - the oracle findings come from the host-side wsoracle and effectoracle
//     reports taken at the E04 observation points, mapped with FindingFromWS
//     and FindingFromEffect, plus the NET and PROC observers when they exist;
//   - E01 identity comes from the receipt and the measured profile, tokens and
//     cost from the provider journal, and the audit files under
//     /var/lib/tbound/audit from the run id.
//
// Open point: the service takes a one-use admission per workflow (L4), so each
// C step needs its own admission id, config digest and registration before it
// can start.
type GovernedC struct{}

// Execute returns ErrNotWired.
func (GovernedC) Execute(context.Context, Step) (Outcome, error) {
	return Outcome{}, ErrNotWired
}

// FakeExecutor is a deterministic in-memory executor for tests. By default it
// returns a clean, complete outcome for every step: every required oracle
// reports none, except that P steps observe the registered effect and manifest
// it. Hook may change the outcome or fail the step.
type FakeExecutor struct {
	Cfg  Config
	Hook func(s Step, o *Outcome) error

	mu    sync.Mutex
	calls []Step
}

// NewFakeExecutor returns a FakeExecutor for cfg.
func NewFakeExecutor(cfg Config) *FakeExecutor { return &FakeExecutor{Cfg: cfg} }

// Calls returns the steps executed so far, in order.
func (f *FakeExecutor) Calls() []Step {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Step(nil), f.calls...)
}

// Execute implements Executor.
func (f *FakeExecutor) Execute(_ context.Context, s Step) (Outcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
	o := defaultOutcome(f.Cfg, s)
	if f.Hook != nil {
		if err := f.Hook(s, &o); err != nil {
			return Outcome{}, err
		}
	}
	return o, nil
}

// defaultOutcome is the clean, complete outcome of a step.
func defaultOutcome(cfg Config, s Step) Outcome {
	o := Outcome{
		E01:          E01Identity{Digest: "fake-e01:" + s.ID(), Verified: true},
		Channel:      "workspace-write",
		Oracles:      map[string]Finding{},
		Route:        RouteCoverage{Complete: true, Digest: "fake-route"},
		EvidenceLoss: evalscore.LossNone,
		Metrics:      Metrics{LatencyMS: 10 + s.Index%7, GateLatencyMS: 1, InTokens: 100, OutTokens: 20, CostMicroUSD: 100, ToolCalls: 1},
	}
	ids := cfg.EVQObligations
	if len(ids) == 0 {
		ids = []string{"EVQ-1"}
	}
	for _, id := range ids {
		o.EVQ = append(o.EVQ, Obligation{ID: id, Passed: true})
	}
	if s.Kind != evalcorpus.KindAdversarial {
		o.Task = TaskPass
		return o
	}
	for _, k := range cfg.required(s.ThreatClass) {
		o.Oracles[k] = FindingNone
	}
	if s.Configuration == CfgP {
		o.Oracles[OracleWS] = FindingObserved
		attempts := 1
		if s.Role == RoleAdaptiveReplay {
			attempts = 3
			o.Attacker = &AttackerTerminal{Stop: "manifested", Attempts: 3, Iterations: 3, StateDigest: "fake"}
		}
		o.Calibration = &CalibrationEvidence{
			WitnessID: s.WitnessID, WitnessPredicateID: s.Predicate,
			Evidence: []string{"obs:" + s.ID()}, Attempts: attempts,
		}
		return o
	}
	o.ToolAttempt = true
	o.MatchedDelivery = s.Role == RoleStaticReplay || s.Role == RoleAdaptiveReplay
	return o
}
