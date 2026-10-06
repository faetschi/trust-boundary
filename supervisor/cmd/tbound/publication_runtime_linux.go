//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/workflow"
)

// PublicationRuntime composes the existing DurableExecutor with the shared,
// presentation-independent workflow finalizer. A host-owned TBound runtime
// installs it around its existing Supervisor; it does not replace Pi with a
// new chat client or add a model-visible workspace_commit tool.
type PublicationRuntime struct {
	finalizer *workflow.Finalizer
	durable   *DurableExecutor
	mu        sync.Mutex
	started   bool
}

type admissionLease struct {
	release func()
}

type admittingBroker struct {
	inner             Broker
	admission         *workflow.Admission
	currentGeneration func() string
	mu                sync.Mutex
	leases            map[string]admissionLease
}

type admittedExecutor struct {
	inner  Executor
	broker *admittingBroker
}

type admissionDecisionRecorder struct {
	inner  DecisionRecorder
	broker *admittingBroker
}

// NewPublicationRuntime binds publication to the actual DurableExecutor's
// Store and synchronized current tip. Trust, quiescence, publication/recovery
// repository openers, shared lifecycle journal, and readiness callbacks remain
// mandatory inputs in FinalizerOptions; there are no flags that synthesize
// containment or production admission.
func NewPublicationRuntime(executor *DurableExecutor, options workflow.FinalizerOptions) (*PublicationRuntime, error) {
	if executor == nil || executor.store == nil || options.Store != nil && options.Store != executor.store {
		return nil, errors.New("publication runtime requires the registered session's DurableExecutor and matching Store")
	}
	options.Store = executor.store
	options.CurrentTip = executor.Tip
	finalizer, err := workflow.NewFinalizer(options)
	if err != nil {
		return nil, err
	}
	return &PublicationRuntime{finalizer: finalizer, durable: executor}, nil
}

// Serve runs the existing broker/correlation/gate/IPC Supervisor with a
// proposal-admission lease spanning correlation through durable execution.
// Caller-supplied executors and missing durable decision recorders are refused
// so this seam cannot silently bypass the session repository.
func (r *PublicationRuntime) Serve(ctx context.Context, supervisor *Supervisor) error {
	if r == nil || r.finalizer == nil || r.durable == nil || supervisor == nil || supervisor.Broker == nil ||
		supervisor.Policy.Digest() == "" || supervisor.Decisions == nil || supervisor.Executor != nil || ctx == nil {
		return errors.New("publication runtime requires a configured Supervisor, durable decision recorder, and no bypass executor")
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("publication runtime session has already started")
	}
	r.started = true
	r.mu.Unlock()
	if err := r.finalizer.RecoverBeforeAdmission(ctx); err != nil {
		return fmt.Errorf("reconcile registered live-root publications before proposal admission: %w", err)
	}
	if err := r.finalizer.Admission().Activate(); err != nil {
		return err
	}
	sessionLease, err := r.finalizer.Admission().Acquire(ctx)
	if err != nil {
		return err
	}
	defer sessionLease()
	broker := &admittingBroker{inner: supervisor.Broker, admission: r.finalizer.Admission(), currentGeneration: func() string {
		if tip := r.durable.Tip(); tip != nil {
			return tip.ID()
		}
		return ""
	}, leases: make(map[string]admissionLease)}
	supervisor.Broker = broker
	supervisor.Decisions = &admissionDecisionRecorder{inner: supervisor.Decisions, broker: broker}
	supervisor.Executor = &admittedExecutor{inner: r.durable, broker: broker}
	serveErr := supervisor.Serve(ctx)
	broker.releaseAll()
	return serveErr
}

// Finalize closes proposal admission, waits for every admitted proposal's
// durable executor call, and publishes the verified current session tip.
func (r *PublicationRuntime) Finalize(ctx context.Context) (publication.Result, error) {
	if r == nil || r.finalizer == nil {
		return publication.Result{}, errors.New("publication runtime is not configured")
	}
	return r.finalizer.Finalize(ctx)
}

func (r *PublicationRuntime) Close() error {
	if r == nil || r.finalizer == nil {
		return nil
	}
	return r.finalizer.Close()
}

// Run provides the reusable lifecycle boundary for a host-owned session: serve
// through the ordinary Supervisor, stop admission, then finalize. It does not
// choose a provider, launch Pi, or weaken the host's trusted runtime profile.
func (r *PublicationRuntime) Run(ctx context.Context, supervisor *Supervisor) (publication.Result, error) {
	serveErr := r.Serve(ctx, supervisor)
	result, finalizeErr := r.Finalize(ctx)
	return result, errors.Join(serveErr, finalizeErr)
}

func (b *admittingBroker) Correlate(ctx context.Context, proposal protocol.Proposal) (correlation.Decision, error) {
	if b == nil || b.inner == nil || b.admission == nil {
		return correlation.Decision{}, errors.New("workflow proposal admission is unavailable")
	}
	release, err := b.admission.Acquire(ctx)
	if err != nil {
		return correlation.Decision{}, err
	}
	decision, err := b.inner.Correlate(ctx, proposal)
	if err != nil {
		release()
		return correlation.Decision{}, err
	}
	if decision.Accepted && (decision.Proposal == nil || b.currentGeneration == nil ||
		decision.Proposal.Generation == "" || decision.Proposal.Generation != b.currentGeneration()) {
		release()
		return correlation.Decision{}, errors.New("captured provider generation is not the current DurableExecutor tip")
	}
	key := proposal.ToolCallID
	if key == "" {
		release()
		return correlation.Decision{}, errors.New("proposal admission requires the broker-bound tool-call ID")
	}
	b.mu.Lock()
	if _, duplicate := b.leases[key]; duplicate {
		b.mu.Unlock()
		release()
		return correlation.Decision{}, errors.New("duplicate in-flight tool-call admission")
	}
	b.leases[key] = admissionLease{release: release}
	b.mu.Unlock()
	return decision, nil
}

func (b *admittingBroker) take(callID string) (admissionLease, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	lease, ok := b.leases[callID]
	if ok {
		delete(b.leases, callID)
	}
	return lease, ok
}

func (b *admittingBroker) releaseCall(callID string) {
	lease, ok := b.take(callID)
	if ok {
		lease.release()
	}
}

func (b *admittingBroker) releaseAll() {
	b.mu.Lock()
	leases := b.leases
	b.leases = make(map[string]admissionLease)
	b.mu.Unlock()
	for _, lease := range leases {
		lease.release()
	}
}

func (r *admissionDecisionRecorder) RecordDecision(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) error {
	if r == nil || r.inner == nil || r.broker == nil {
		return errors.New("publication runtime requires a durable gate decision recorder")
	}
	if err := r.inner.RecordDecision(ctx, proposal, decision); err != nil {
		r.broker.releaseCall(proposal.ToolCallID)
		return err
	}
	if decision.Verdict != gate.Allow {
		r.broker.releaseCall(proposal.ToolCallID)
	}
	return nil
}

func (e *admittedExecutor) Execute(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) (output json.RawMessage, err error) {
	if e == nil || e.inner == nil || e.broker == nil {
		return nil, errors.New("registered durable executor is unavailable")
	}
	lease, ok := e.broker.take(proposal.ToolCallID)
	if !ok {
		return nil, errors.New("executor call has no active workflow admission lease")
	}
	defer lease.release()
	return e.inner.Execute(ctx, proposal, decision)
}
