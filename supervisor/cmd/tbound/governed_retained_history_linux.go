//go:build linux

package main

import (
	"bytes"
	"errors"
	"reflect"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

// governedRetainedHistory accepts only the exact prefix revalidated against the spent
// recovery receipt and full provider/Store/command replay. It never authorizes an operation.
type governedRetainedHistory struct {
	transitions   []delta.Transition
	journalPrefix []byte
}

func newGovernedRetainedHistory(bundle sessionrepo.EvidenceBundle, pins workflowrecovery.Pins,
	historical *governedHistoricalStoreVerifier) (*governedRetainedHistory, error) {
	if bundle.AuditRecordCount != pins.StoreJournalCount || bundle.AuditHeadHash != pins.StoreJournalHead ||
		bundle.AdmissionRecordCount != pins.AdmissionJournalCount || bundle.AdmissionHeadHash != pins.AdmissionJournalHead ||
		len(bundle.Generations) == 0 {
		return nil, ErrGovernedSessionEvidence
	}
	tip := bundle.Generations[len(bundle.Generations)-1]
	if tip.ID != pins.StoreTipGenerationID || bareStoreTreeDigest(tip.TreeDigest) != pins.StoreTipTreeDigest {
		return nil, ErrGovernedSessionEvidence
	}
	if err := historical.VerifyBundle(bundle); err != nil {
		return nil, err
	}
	// Reconstruction binds the entire transition list to the exact journal, including its length.
	if _, err := sessionrepo.Reconstruct(bundle); err != nil {
		return nil, err
	}
	history := &governedRetainedHistory{journalPrefix: bytes.Clone(bundle.AuditJournal),
		transitions: append([]delta.Transition(nil), bundle.ApprovedDeltaLedger...)}
	for i := range history.transitions {
		history.transitions[i].Changes = append([]delta.ObjectChange(nil), history.transitions[i].Changes...)
	}
	return history, nil
}

func governedTransitionBinding(t delta.Transition) delta.TransitionBinding {
	return delta.TransitionBinding{ID: t.ID, Sequence: t.Sequence, Tool: t.Tool, ArgumentDigest: t.ArgumentDigest,
		ViewID: t.ViewID, ExecutionContextDigest: t.ExecutionContextDigest,
		InputGeneration: t.InputGeneration, OutputGeneration: t.OutputGeneration,
		InputTreeDigest: t.InputTreeDigest, OutputTreeDigest: t.OutputTreeDigest,
		Operation: t.Operation, Changes: t.Changes}
}

func (h *governedRetainedHistory) contains(sequence uint64) bool {
	return h != nil && sequence > 0 && sequence <= uint64(len(h.transitions))
}

func (h *governedRetainedHistory) verifyDecision(decision delta.PolicyDecision, binding delta.TransitionBinding,
	live delta.DecisionVerifier) error {
	if h.contains(binding.Sequence) {
		return h.verifyRetained(decision, binding)
	}
	return live(decision, binding)
}

// verifyRetained accepts a frozen sequence only when its decision and every binding field are exactly the retained ones.
func (h *governedRetainedHistory) verifyRetained(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	if !h.contains(binding.Sequence) {
		return ErrGovernedSessionEvidence
	}
	retained := h.transitions[binding.Sequence-1]
	if decision != retained.Decision || !reflect.DeepEqual(binding, governedTransitionBinding(retained)) {
		return ErrGovernedSessionEvidence
	}
	return nil
}

func (h *governedRetainedHistory) verifyOrigin(bundle sessionrepo.EvidenceBundle, transition delta.Transition,
	journal *audit.Journal) error {
	if !h.contains(transition.Sequence) || !reflect.DeepEqual(transition, h.transitions[transition.Sequence-1]) ||
		!bytes.HasPrefix(bundle.AuditJournal, h.journalPrefix) || len(bundle.ApprovedDeltaLedger) < len(h.transitions) ||
		!reflect.DeepEqual(bundle.ApprovedDeltaLedger[:len(h.transitions)], h.transitions) {
		return ErrGovernedSessionEvidence
	}
	trace, err := journal.Trace()
	if err != nil || !traceMatchesBundle(trace, bundle.AuditJournal, bundle.AuditRecordCount, bundle.AuditHeadHash) {
		return errors.Join(ErrGovernedSessionEvidence, err)
	}
	_, err = sessionrepo.Reconstruct(bundle)
	return err
}
