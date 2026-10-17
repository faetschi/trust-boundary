package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
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

// The closed set of governed roles a corpus-mode service admits. Each names one planned
// governed C trial of a case, and the service checks it against the manifest case kind.
// Ambiguous cases also run C in the plan, so their role is admitted too.
const (
	GovRoleTrajectory     = "trajectory"      // adversarial
	GovRoleStaticReplay   = "static-replay"   // adversarial
	GovRoleAdaptiveReplay = "adaptive-replay" // adversarial
	GovRoleVariance1      = "variance-1"      // adversarial, variance subset
	GovRoleVariance2      = "variance-2"      // adversarial, variance subset
	GovRoleCompliant      = "compliant"       // compliant
	GovRoleAmbiguous      = "ambiguous"       // ambiguous
)

// GovernedRoles lists the closed set, in the order of the service's enum.
func GovernedRoles() []string {
	return []string{GovRoleTrajectory, GovRoleStaticReplay, GovRoleAdaptiveReplay,
		GovRoleVariance1, GovRoleVariance2, GovRoleCompliant, GovRoleAmbiguous}
}

// governedRoleOf maps a step to its governed role, or "" if the step has none.
func governedRoleOf(s Step) string {
	switch s.Role {
	case RoleTrajectory:
		return GovRoleTrajectory
	case RoleStaticReplay:
		return GovRoleStaticReplay
	case RoleAdaptiveReplay:
		return GovRoleAdaptiveReplay
	case RoleCompliant:
		return GovRoleCompliant
	case RoleAmbiguous:
		return GovRoleAmbiguous
	case RoleVarianceRepeat:
		switch s.Repeat {
		case 1:
			return GovRoleVariance1
		case 2:
			return GovRoleVariance2
		}
	}
	return ""
}

// GovernedRequest is the corpus-mode request of one governed C run to the registered
// local service, as `tbound serve --pi` sends it. The service admits trials from its
// owner-signed, digest-pinned corpus manifest: it takes the prompt and the budgets of
// the case from the manifest, so the request names the case, the role and the attempt
// and never carries task text. The admission id is `<case_id>/<role>/<attempt>` and is
// single-use: each planned governed trial has exactly one.
type GovernedRequest struct {
	// TaskID is `--task-id`; in corpus mode it is the case_id.
	TaskID string
	// Role is `--role`, one of GovernedRoles.
	Role string
	// Attempt is `--attempt`: 1, or 2 for the one outage rerun. The service enforces
	// 1 or 2 at admission; the runner and evalscore enforce the outage evidence.
	Attempt int
	// RequestID is `--task-request-id`. It is derived from the plan digest, the
	// step and the attempt, so replaying the same attempt returns the stored
	// terminal answer instead of starting a second workflow.
	RequestID string
}

// GovernedRequestFor derives the request of a step.
func GovernedRequestFor(planDigest string, s Step) GovernedRequest {
	sum := sha256.Sum256([]byte(planDigest + "\x00" + s.ID() + "\x00" + strconv.Itoa(s.Attempt)))
	return GovernedRequest{TaskID: s.CaseID, Role: governedRoleOf(s), Attempt: s.Attempt, RequestID: "tbr-" + hex.EncodeToString(sum[:16])}
}

// Argv is the argument vector of the client invocation, after the tbound binary. It
// has no `--task`: a corpus-mode service refuses client task text.
func (r GovernedRequest) Argv() ([]string, error) {
	if r.TaskID == "" || r.RequestID == "" {
		return nil, errors.New("runner: governed request lacks a case id or request id")
	}
	if !slices.Contains(GovernedRoles(), r.Role) {
		return nil, fmt.Errorf("runner: governed request role %q is not a governed role", r.Role)
	}
	if r.Attempt != 1 && r.Attempt != 2 {
		return nil, fmt.Errorf("runner: governed request attempt %d is not 1 or 2", r.Attempt)
	}
	return []string{"serve", "--pi", "--task-id", r.TaskID, "--role", r.Role, "--attempt", strconv.Itoa(r.Attempt),
		"--task-request-id", r.RequestID, "--bash-no-outputs"}, nil
}

// GovernedC is the adapter for Configuration C (and the governed replays of C). Its
// request mapping is wired; the transport to the guest is not, so Execute returns
// ErrNotWired.
//
// The mapping:
//
//   - one C step is one `tbound serve --pi --task-id <case_id> --role <role> --attempt
//     <1|2> --task-request-id <RequestID> --bash-no-outputs` client invocation against
//     /run/tbound/serve-pi.sock (Request and GovernedRequest.Argv), by a client uid and
//     gid that the signed registration allows;
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
type GovernedC struct {
	// PlanDigest is Schedule.Digest of the plan being run.
	PlanDigest string
}

// Request maps a C step to its corpus-mode request.
func (g GovernedC) Request(s Step) (GovernedRequest, error) {
	if s.Configuration != CfgC {
		return GovernedRequest{}, fmt.Errorf("runner: step %s is configuration %s, not C", s.ID(), s.Configuration)
	}
	request := GovernedRequestFor(g.PlanDigest, s)
	if _, err := request.Argv(); err != nil {
		return GovernedRequest{}, err
	}
	return request, nil
}

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
