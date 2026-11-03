//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"slices"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

// productPublicationTrust is the live-root trust of a supervisor-loop turn. The loop runs inside this
// process, with no Pi worker. Command cells must settle before publication.
// Every transition and origin is verified by the turn's own authority. The trust never claims what the
// supervisor-loop worker cannot show:
//
//   - The host checks OS permissions on each managed root's ancestry before publication. Root and
//     the service account remain trusted writers; a racing writer with either identity is not excluded.
//   - Bash authority comes only from the exact terminal lease and authenticated D09 receipts.
//
// It replaces neither the research production trust, which needs a Pi worker, its D09 launch proof and a
// managed live-root permit, nor the test trust, which accepts every admission.
type productPublicationTrust struct {
	env       *productTurnEnv
	authority *SessionOperationAuthority
	// xattr re-verifies the calibrated filesystem, mount and scanner identity and returns the attestation.
	xattr func() (workspace.XattrVisibilityAttestation, error)
}

var errProductTrust = errors.New("product publication trust refused")

func (t *productPublicationTrust) CheckAdmission(ctx context.Context) error {
	if t == nil || t.env == nil || t.authority == nil || t.xattr == nil || ctx == nil {
		return errProductTrust
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a := t.authority
	if a.runner != nil {
		runner, ok := a.runner.(*productD09TurnRunner)
		if !ok || runner == nil || runner.productionCommandRunner == nil || runner.cleanup == nil || runner.Profile() != piruntime.D09CommandRunnerProfile {
			return errProductTrust
		}
	}
	if a.workflowLedger == nil || a.store == nil || a.baseline == nil ||
		!slices.Contains(t.env.Registration.ProviderProfiles, a.providerProfileID) {
		return errProductTrust
	}
	return a.requirePreparedSource()
}

func (t *productPublicationTrust) CheckPublicationPrerequisites(ctx context.Context, r workflow.EvidenceRequest) error {
	if err := t.CheckAdmission(ctx); err != nil {
		return err
	}
	a := t.authority
	if r.Workflow != a.workflowID || r.OwnerEpoch != 1 || r.Bundle.PolicyDigest != a.storePolicyDigest ||
		r.Bundle.MetadataPolicyDigest != a.metadataPolicyDigest {
		return errProductTrust
	}
	for _, operation := range r.Bundle.Operations {
		if operation.Tool != "bash" {
			continue
		}
		if !deniedAfterEffect(operation) {
			transition, err := productionTransitionForOperation(r.Bundle, operation)
			if err != nil {
				return err
			}
			if err := a.VerifyOrigin(r.Bundle, transition); err != nil {
				return err
			}
		}
		// A scope-denied effect has no approved delta, but still needs its settled receipt.
		if err := verifyProductionBashEvidence(ctx, a.runner, operation); err != nil {
			return err
		}
	}
	if a.runner != nil && a.runner.(*productD09TurnRunner).Pending() != 0 {
		return errors.Join(errProductTrust, piruntime.ErrUnknownSettlement)
	}
	return nil
}

func (t *productPublicationTrust) ResolveTerminalLeaseOrigin(ctx context.Context, bundle sessionrepo.EvidenceBundle, terminal delta.Transition) (delta.OperationIdentity, error) {
	if err := t.CheckAdmission(ctx); err != nil {
		return delta.OperationIdentity{}, err
	}
	return t.authority.ResolveTerminalLeaseOrigin(ctx, bundle, terminal)
}

func (t *productPublicationTrust) AttestQuiescent(ctx context.Context, live *os.File, baseline, tip *sessionrepo.Generation) error {
	if err := t.CheckAdmission(ctx); err != nil {
		return err
	}
	a := t.authority
	if live == nil || baseline == nil || baseline != a.baseline || tip == nil || tip.ID() == baseline.ID() {
		return errProductTrust
	}
	a.mu.Lock()
	conversation := a.conversation
	a.mu.Unlock()
	// driveProductTurn closes the conversation before any finalization, including a review's.
	if !conversation.Closed() {
		return errors.Join(errProductTrust, errors.New("the supervisor loop's conversation is still open"))
	}
	bundle, err := a.store.EvidenceBundle()
	if err != nil {
		return err
	}
	if err := t.CheckPublicationPrerequisites(ctx, workflow.EvidenceRequest{Workflow: a.workflowID, OwnerEpoch: 1, Bundle: bundle}); err != nil {
		return err
	}
	checkpoint, _ := a.workflowLedger.Snapshot()
	if err := productTurnChildResolved(bundle, checkpoint.Pins); err != nil {
		return err
	}
	attestation, err := t.xattr()
	if err != nil {
		return err
	}
	if attestation != t.env.Xattr {
		return errors.Join(errProductTrust, errors.New("the xattr calibration changed since start"))
	}
	snapshot, err := workspace.Scan(live, t.env.projectWorkspace())
	if err != nil {
		return err
	}
	if snapshot.TreeDigest != baseline.TreeDigest() {
		return errors.Join(errProductTrust, errors.New("the managed project changed before publication"))
	}
	return nil
}

func (t *productPublicationTrust) VerifyTransition(ctx context.Context, decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	if t == nil || t.authority == nil || ctx == nil {
		return errProductTrust
	}
	return t.authority.VerifyTransition(decision, binding)
}

func (t *productPublicationTrust) VerifyOrigin(ctx context.Context, bundle sessionrepo.EvidenceBundle, transition delta.Transition) error {
	if t == nil || t.authority == nil || ctx == nil {
		return errProductTrust
	}
	return t.authority.VerifyOrigin(bundle, transition)
}
