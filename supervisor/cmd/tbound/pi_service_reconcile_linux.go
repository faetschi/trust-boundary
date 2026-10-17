//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrPiServiceUnreconciled reports that the workflow behind an unknown request is not proven
// quiescent, so the request stays quarantined and its barrier on later requests stays.
var ErrPiServiceUnreconciled = errors.New("the workflow behind the unknown Pi service request is not reconciled")

// piServiceAbandonReconciler proves that the workflow behind one unknown request is quiescent
// and returns the evidence digests the proof rests on. It is read-side apart from the owner
// drain verifier's own teardown receipts. A non-nil error means something is unreconciled, and
// its text lists what.
type piServiceAbandonReconciler interface {
	Reconcile(ctx context.Context, state piServiceRequestState) (piServiceReconciliation, error)
}

// piServiceReconciliation is the evidence of one reconciliation. Every field is derived from
// host state, never from the operator, and every digest is stable once the workflow is
// quiescent: the later arrival of unrelated requests or workflows does not change it.
type piServiceReconciliation struct {
	// WorkflowID is the reconciled workflow. NoWorkflow instead states that the catalog never
	// reserved one for this admission, so nothing could have run.
	WorkflowID string `json:"workflow_id,omitempty"`
	NoWorkflow bool   `json:"no_workflow,omitempty"`
	// OwnerNeverBound states that no owner checkpoint or scope was ever bound, so there is no
	// owner epoch to drain. OwnerCheckpoint is then a digest of that fact.
	OwnerNeverBound bool `json:"owner_never_bound,omitempty"`
	// OwnerCheckpoint is the digest of the latest settled owner checkpoint, whose pins carry the
	// effect, host-call, pending-call and provider dispositions.
	OwnerCheckpoint string `json:"owner_checkpoint_digest"`
	// OwnerDrainProofs are the drain-proof digests, one per owner epoch, from the owner drain
	// verifier (NeverBound receipts for never-exposed owners). A proof digest also commits to
	// the boot id it was observed on, so OwnerDrainBootID is recorded beside them: across a
	// reboot the proofs are not compared again, only re-proven.
	OwnerDrainProofs []string `json:"owner_drain_proofs"`
	OwnerDrainBootID string   `json:"owner_drain_boot_id,omitempty"`
	// Publication is the digest of the workflow's records in the publication journal, all of
	// which are resolved.
	Publication string `json:"publication_digest"`
	// ProviderRoute is "research" (Provider digests the workflow's USD ledger units) or
	// "engineering" (Provider digests the workflow's provider-bridge journals).
	ProviderRoute string `json:"provider_route"`
	Provider      string `json:"provider_digest"`
}

func (r piServiceReconciliation) digest() (string, error) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}

func (r piServiceReconciliation) valid() bool {
	if !validDigestText(r.OwnerCheckpoint) || !validDigestText(r.Publication) || !validDigestText(r.Provider) ||
		r.ProviderRoute != "research" && r.ProviderRoute != "engineering" {
		return false
	}
	if r.NoWorkflow {
		return r.WorkflowID == "" && !r.OwnerNeverBound && len(r.OwnerDrainProofs) == 0 && r.OwnerDrainBootID == ""
	}
	if !validIdentityText(r.WorkflowID) {
		return false
	}
	if r.OwnerNeverBound {
		return len(r.OwnerDrainProofs) == 0 && r.OwnerDrainBootID == ""
	}
	if len(r.OwnerDrainProofs) == 0 || r.OwnerDrainBootID == "" {
		return false
	}
	for _, proof := range r.OwnerDrainProofs {
		if !validDigestText(proof) {
			return false
		}
	}
	return true
}

// stillHolds reports whether the evidence recorded at abandonment (r) is what a fresh
// reconciliation (current) finds. Everything must be equal; the one exception is the drain-proof
// digests across a reboot, which differ by construction and are then required to be re-proven
// for the same number of owner epochs.
func (r piServiceReconciliation) stillHolds(current piServiceReconciliation) bool {
	if !r.valid() || !current.valid() {
		return false
	}
	if len(r.OwnerDrainProofs) != len(current.OwnerDrainProofs) {
		return false
	}
	if r.OwnerDrainBootID == current.OwnerDrainBootID {
		for index := range r.OwnerDrainProofs {
			if r.OwnerDrainProofs[index] != current.OwnerDrainProofs[index] {
				return false
			}
		}
	}
	r.OwnerDrainProofs, r.OwnerDrainBootID = nil, ""
	current.OwnerDrainProofs, current.OwnerDrainBootID = nil, ""
	return r.WorkflowID == current.WorkflowID && r.NoWorkflow == current.NoWorkflow &&
		r.OwnerNeverBound == current.OwnerNeverBound && r.OwnerCheckpoint == current.OwnerCheckpoint &&
		r.Publication == current.Publication && r.ProviderRoute == current.ProviderRoute && r.Provider == current.Provider
}
