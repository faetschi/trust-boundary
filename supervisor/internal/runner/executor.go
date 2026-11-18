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

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/wsoracle"
)

// Executor runs one step in two phases, and the phase decides what an error
// costs. The runner, not the executor, controls which one an attempt is in.
//
//   - Prepare is the only place an attempt can be voided. It must have no visible
//     effect: no write to the workspace, no request to a service, no guest boot,
//     nothing an oracle could observe. It checks that the step can be run (wiring,
//     admission, registration) and may refuse. Any error from Prepare (ErrNotWired,
//     ErrSetupRefused or another) voids the begin: no unit is spent, the step stays
//     without a terminal record and the run stops with that error.
//   - Execute does the work. The runner writes and fsyncs a started journal entry
//     before it calls Execute, so every dispatched attempt is spent by default and
//     the executor does not have to cooperate. Whatever Execute returns, including
//     ErrSetupRefused or ErrNotWired with an empty Outcome, the unit is spent:
//     an error with a non-zero Outcome is journaled as a partial outcome with the
//     error, so observations already established are kept; an error without an
//     Outcome is a setup failure; an interruption leaves the step abandoned. A
//     crash after the started entry is closed as abandoned on resume and never
//     run again, except for a step the runner may resume (resumable): Execute is
//     then called once more with Step.Resumed set, and must collect the result of
//     the attempt already sent, never start a second one.
//
// An executor must honor ctx and must not retry a failed step on its own except
// where Metrics.Retries records it.
//
// Trust boundary. Executors are TBound's own measurement instrumentation, trusted
// evidence producers. The adversary is the agent inside the cell, not the executor.
// The runner therefore checks the internal consistency of what an executor returns
// (hash chains, interval binding, report versions, report references, anchors), not
// its authenticity: an executor that fabricates a consistent outcome is not detected.
// The one cross-check against a second source is the publication anchor
// (PublicationFree.AnchorHead and AnchorCount), which must come from the governed
// service's own receipt for the interval, not from the executor's own journal copy.
type Executor interface {
	Prepare(ctx context.Context, s Step) error
	Execute(ctx context.Context, s Step) (Outcome, error)
}

// Executors routes a step to the executor of its configuration. A nil entry is
// an unwired configuration.
type Executors struct {
	A, B, C, P, V Executor
	CDH           map[Configuration]Executor
}

// Prepare implements Executor. An unwired configuration refuses with ErrNotWired.
func (e Executors) Prepare(ctx context.Context, s Step) error {
	x := e.of(s.Configuration)
	if x == nil {
		return fmt.Errorf("%w: configuration %s", ErrNotWired, s.Configuration)
	}
	return x.Prepare(ctx, s)
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
	if c.IsCDH() {
		return e.CDH[c]
	}
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

	// The roles of the P half of a pair: Configuration P under its registered delta, through the same
	// service and the same stimulus as the C half.
	GovRolePStaticReplay   = "p-static-replay"
	GovRolePAdaptiveReplay = "p-adaptive-replay"

	// The roles of Configuration B: the compliant and ambiguous cases on the real model through the same
	// containment and proxy tool path, with an allow-all gate and admit-all/v1 admission.
	GovRoleBCompliant = "b-compliant"
	GovRoleBAmbiguous = "b-ambiguous"
)

// GovernedRoles lists the closed set, in the order of the service's enum.
func GovernedRoles() []string {
	return []string{GovRoleTrajectory, GovRoleStaticReplay, GovRoleAdaptiveReplay,
		GovRoleVariance1, GovRoleVariance2, GovRoleCompliant, GovRoleAmbiguous, GovRolePStaticReplay, GovRolePAdaptiveReplay,
		GovRoleBCompliant, GovRoleBAmbiguous}
}

// governedRoleOf maps a step to its governed role, or "" if the step has none. Configuration P has
// the two replay roles only; Configuration B has the
// compliant and ambiguous roles only; every other configuration other than C has none.
func governedRoleOf(s Step) string {
	if s.Configuration == CfgP {
		switch s.Role {
		case RoleStaticReplay:
			return GovRolePStaticReplay
		case RoleAdaptiveReplay:
			return GovRolePAdaptiveReplay
		}
		return ""
	}
	if s.Configuration == CfgB {
		switch s.Role {
		case RoleCompliant:
			return GovRoleBCompliant
		case RoleAmbiguous:
			return GovRoleBAmbiguous
		}
		return ""
	}
	if s.Configuration != CfgC {
		return ""
	}
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
	_, _, cdh := corpusadmit.CDHRoleParts(r.Role)
	if !slices.Contains(GovernedRoles(), r.Role) && !cdh {
		return nil, fmt.Errorf("runner: governed request role %q is not a governed role", r.Role)
	}
	if r.Attempt != 1 && r.Attempt != 2 {
		return nil, fmt.Errorf("runner: governed request attempt %d is not 1 or 2", r.Attempt)
	}
	return []string{"serve", "--pi", "--task-id", r.TaskID, "--role", r.Role, "--attempt", strconv.Itoa(r.Attempt),
		"--task-request-id", r.RequestID, "--bash-no-outputs"}, nil
}

// FakeExecutor is a deterministic in-memory executor for tests. By default it
// returns a clean, complete outcome for every step: every required oracle
// reports none, except that P steps observe the registered effect and manifest
// it. PrepareHook models a refusal in the side-effect-free phase (the attempt is
// voided); Hook may change the outcome or fail the step in Execute, where every
// error spends the unit.
type FakeExecutor struct {
	Cfg Config
	// PrepareHook, if set, runs in Prepare; an error voids the attempt.
	PrepareHook func(s Step) error
	Hook        func(s Step, o *Outcome) error

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

// Prepare implements Executor. It records nothing and has no visible effect.
func (f *FakeExecutor) Prepare(_ context.Context, s Step) error {
	if f.PrepareHook != nil {
		return f.PrepareHook(s)
	}
	return nil
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
		Interval:     IntervalRef(s),
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
	for _, k := range cfg.requiredFor(s.ThreatClass, cParts(s.Predicate, s.Effects)...) {
		o.Oracles[k] = FindingNone
	}
	o.OracleVersion = wsoracle.OracleVersion
	if s.Configuration == CfgP {
		for _, p := range pParts(s.Predicate, s.Effects, s.EffectJoin) {
			if k, ok := observerFor(p, s.ThreatClass); ok {
				o.Oracles[k] = FindingObserved
			}
		}
		attempts := 1
		if s.Role == RoleAdaptiveReplay {
			attempts = 3
			o.Attacker = &AttackerTerminal{Stop: "manifested", Attempts: 3, Iterations: 3, StateDigest: "fake"}
		}
		o.Calibration = &CalibrationEvidence{
			WitnessID: s.WitnessID, WitnessPredicateID: s.Predicate,
			Evidence: []string{"obs:" + s.ID()}, Attempts: attempts,
		}
		fakeWSRefs(&o, s)
		return o
	}
	o.ToolAttempt = true
	o.MatchedDelivery = s.Role == RoleStaticReplay || s.Role == RoleAdaptiveReplay
	fakeWSRefs(&o, s)
	return o
}

// fakeWSRefs gives every finding of a default outcome a report reference of its own,
// as a wired executor gets from RecordE04, RecordWS, netoracle.RecordNET01/02 and
// procoracle.Apply, each of the version its key requires (oracleVersionFor). The
// digests are synthetic. As with the real recorders, the aggregate OracleReport
// belongs to the WS findings only.
func fakeWSRefs(o *Outcome, s Step) {
	o.OracleRefs = map[string]OracleRef{}
	for _, k := range slices.Concat(oracleKeys, observerKeys) {
		if o.Oracles[k] == "" {
			continue
		}
		v, _ := oracleVersionFor(k)
		sum := sha256.Sum256([]byte("fake-oracle-report:" + v + ":" + IntervalRef(s)))
		d := hex.EncodeToString(sum[:])
		if slices.Contains(oracleKeys, k) {
			o.OracleReport = d
		}
		o.OracleRefs[k] = OracleRef{Version: v, Report: d}
	}
}
