//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

type productionCommandLease struct {
	leaseID string
	runner  sessionrepo.CommandRunner
	running bool
}

type productionCommandRunner struct {
	mu              sync.Mutex
	factory         productionCommandRunnerFactory
	leases          map[string]productionCommandLease
	receipts        map[string]sessionrepo.CommandSettlement
	settledReceipts map[string]sessionrepo.CommandSettlement
	evidenceRunners map[string]sessionrepo.CommandRunner
	settled         int
}

// newProductionCommandRunner accepts only a per-lease factory. RegisterLease
// constructs a fresh signed D09CommandRunner for the exact authenticated lease;
// a missing factory or a profile-only/legacy runner fails before command launch.
func newProductionCommandRunner(factory productionCommandRunnerFactory) *productionCommandRunner {
	return &productionCommandRunner{
		factory: factory, leases: make(map[string]productionCommandLease),
		receipts:        make(map[string]sessionrepo.CommandSettlement),
		settledReceipts: make(map[string]sessionrepo.CommandSettlement),
		evidenceRunners: make(map[string]sessionrepo.CommandRunner),
	}
}

func (r *productionCommandRunner) Profile() string {
	if r == nil || r.factory == nil {
		return "production-d09-bash-runner-unavailable"
	}
	return piruntime.D09CommandRunnerProfile
}

func (r *productionCommandRunner) RegisterLease(viewID, leaseID string) error {
	if r == nil || r.factory == nil {
		return ErrProductionBashRunnerUnavailable
	}
	if viewID == "" || leaseID == "" {
		return errors.New("production command runner requires a view and lease identity")
	}
	r.mu.Lock()
	if _, duplicate := r.leases[viewID]; duplicate {
		r.mu.Unlock()
		return errors.New("production command runner already registered this view")
	}
	r.mu.Unlock()
	signed, err := r.factory(leaseID)
	if err != nil {
		return errors.Join(ErrProductionBashRunnerUnavailable, err)
	}
	if signed == nil {
		return ErrProductionBashRunnerUnavailable
	}
	value := reflect.ValueOf(signed)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return ErrProductionBashRunnerUnavailable
	}
	profile, hasProfile := signed.(sessionrepo.CommandRunnerProfile)
	_, hasVerifier := signed.(productionBashEvidenceVerifier)
	if signed == nil || !hasProfile || !hasVerifier || profile.Profile() != piruntime.D09CommandRunnerProfile {
		return ErrProductionBashRunnerUnavailable
	}
	if _, exact := signed.(*productionD09BashRunner); !exact {
		return ErrProductionBashRunnerUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, duplicate := r.leases[viewID]; duplicate {
		return errors.New("production command runner raced a duplicate view registration")
	}
	r.leases[viewID] = productionCommandLease{leaseID: leaseID, runner: signed}
	return nil
}

func (r *productionCommandRunner) Run(ctx context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	if r == nil || r.factory == nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrProductionBashRunnerUnavailable
	}
	if view == nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("production command runner requires a command view")
	}
	r.mu.Lock()
	lease, exists := r.leases[view.ID()]
	if !exists || lease.running {
		r.mu.Unlock()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("production command runner has no idle registered lease for the view")
	}
	lease.running = true
	r.leases[view.ID()] = lease
	r.mu.Unlock()
	result, settlement, err := lease.runner.Run(ctx, view, spec)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	r.mu.Lock()
	if _, duplicate := r.receipts[view.ID()]; duplicate {
		r.mu.Unlock()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("production command runner produced a duplicate settlement")
	}
	r.receipts[view.ID()] = settlement
	r.evidenceRunners[view.ID()] = lease.runner
	r.mu.Unlock()
	return result, settlement, nil
}

// Settlements returns the number of one-use command-cell settlements observed.
func (r *productionCommandRunner) Settlements() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settled
}

// Pending returns the number of registered leases that do not yet have a
// verified one-use settlement plus any retained-but-unverified receipts. A
// non-zero value means command writers are not proven stopped.
func (r *productionCommandRunner) Pending() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.leases) + len(r.receipts) + len(r.settledReceipts)
}

func (r *productionCommandRunner) VerifySettlement(candidate sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if r == nil {
		return errors.New("production command runner is not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	actual, exists := r.receipts[viewID]
	lease, leaseExists := r.leases[viewID]
	if !exists || !leaseExists || lease.leaseID != leaseID || actual.LeaseID != leaseID || actual.ViewID != viewID ||
		!actual.ExitObserved || !actual.ProcessScopeEmpty || !actual.WritersStopped || !actual.MountDetached ||
		!strings.HasPrefix(actual.EvidenceClass, piruntime.D09CommandRunnerProfile+":") || !settlementMatches(actual, candidate) {
		return errors.New("production command settlement does not match the retained one-use receipt")
	}
	delete(r.receipts, viewID)
	r.settledReceipts[viewID] = actual
	return nil
}

func (r *productionCommandRunner) BindProductionBashEvidence(ctx context.Context, providerCall delta.OperationIdentity,
	bundle sessionrepo.EvidenceBundle, transition delta.Transition, operation sessionrepo.OperationEvidence) error {
	if r == nil || ctx == nil || operation.ViewID == "" || operation.Operation.Kind != delta.OperationLease {
		return ErrProductionCommandEvidence
	}
	r.mu.Lock()
	lease, leaseExists := r.leases[operation.ViewID]
	settlement, settlementExists := r.settledReceipts[operation.ViewID]
	signed := r.evidenceRunners[operation.ViewID]
	if !leaseExists || !settlementExists || signed == nil || lease.leaseID != operation.Operation.LeaseID ||
		settlement.LeaseID != lease.leaseID || settlement.ViewID != operation.ViewID {
		r.mu.Unlock()
		return ErrProductionCommandEvidence
	}
	r.mu.Unlock()
	binder, ok := signed.(productionBashEvidenceBinder)
	if !ok {
		return ErrProductionCommandEvidence
	}
	if err := binder.BindProductionBashEvidence(ctx, providerCall, bundle, transition, operation); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	currentLease, leaseStillExists := r.leases[operation.ViewID]
	currentSettlement, settlementStillExists := r.settledReceipts[operation.ViewID]
	if !leaseStillExists || !settlementStillExists || currentLease.leaseID != lease.leaseID ||
		!settlementMatches(currentSettlement, settlement) || r.evidenceRunners[operation.ViewID] != signed {
		return ErrProductionCommandEvidence
	}
	delete(r.leases, operation.ViewID)
	delete(r.settledReceipts, operation.ViewID)
	r.settled++
	return nil
}

// VerifyProductionBashEvidence re-verifies a completed D09 per-command proof
// against the exact OperationEvidence produced after the Store's durable result.
// The schema's generic containment marker remains not-established; only the
// registered signed D09 runner's captured proof can authenticate the command.
func (r *productionCommandRunner) VerifyProductionBashEvidence(ctx context.Context, operation sessionrepo.OperationEvidence) error {
	if r == nil || ctx == nil || operation.Tool != "bash" || operation.Operation.Kind != delta.OperationLease ||
		operation.Operation.LeaseID == "" || operation.ViewID == "" || operation.ExitCode == nil ||
		operation.CommandContainmentStatus != "not-established" ||
		operation.CommandRunnerProfile != piruntime.D09CommandRunnerProfile {
		return ErrProductionCommandEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	signed := r.evidenceRunners[operation.ViewID]
	r.mu.Unlock()
	if signed == nil {
		return ErrProductionCommandEvidence
	}
	verifier, ok := signed.(productionBashEvidenceVerifier)
	if !ok {
		return ErrProductionCommandEvidence
	}
	if err := verifier.VerifyProductionBashEvidence(ctx, operation); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	return nil
}

func (r *productionCommandRunner) VerifyProductionBashEvidenceWithOrigin(ctx context.Context,
	bundle sessionrepo.EvidenceBundle, providerCall delta.OperationIdentity, transition delta.Transition,
	operation sessionrepo.OperationEvidence) error {
	if err := r.VerifyProductionBashEvidence(ctx, operation); err != nil {
		return err
	}
	r.mu.Lock()
	signed := r.evidenceRunners[operation.ViewID]
	r.mu.Unlock()
	verifier, ok := signed.(interface {
		VerifyProductionBashEvidenceWithOrigin(context.Context, sessionrepo.EvidenceBundle, delta.OperationIdentity,
			delta.Transition, sessionrepo.OperationEvidence) error
	})
	if !ok {
		return ErrProductionCommandEvidence
	}
	if err := verifier.VerifyProductionBashEvidenceWithOrigin(ctx, bundle, providerCall, transition, operation); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	return nil
}

func settlementMatches(actual, candidate sessionrepo.CommandSettlement) bool {
	return actual.LeaseID == candidate.LeaseID && actual.ViewID == candidate.ViewID &&
		actual.ExitObserved == candidate.ExitObserved && actual.ExitCode == candidate.ExitCode &&
		actual.ProcessScopeEmpty == candidate.ProcessScopeEmpty && actual.WritersStopped == candidate.WritersStopped &&
		actual.MountDetached == candidate.MountDetached && actual.EvidenceClass == candidate.EvidenceClass
}

func validDigestText(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := value[len(prefix):]
	if encoded != strings.ToLower(encoded) {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}
