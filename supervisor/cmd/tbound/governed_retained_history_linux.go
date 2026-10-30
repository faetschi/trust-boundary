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
	// bash is the historical Bash evidence frozen with the prefix. It is nil when the history has no command journal.
	bash *frozenBashHistory
}

func newGovernedRetainedHistory(bundle sessionrepo.EvidenceBundle, pins workflowrecovery.Pins,
	historical *governedHistoricalStoreVerifier, commands governedRecoveryCommandVerifier) (*governedRetainedHistory, error) {
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
	if commands != nil {
		production, ok := commands.(*productionHistoricalBashHistory)
		if !ok || production == nil {
			return nil, ErrGovernedSessionEvidence
		}
		frozen, err := production.freezeBash()
		if err != nil {
			return nil, err
		}
		history.bash = frozen
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

// frozenBashHistory is the historical Bash evidence of an activated history: each verified command bound to its Store
// EffectID, the origin of each promoted one, and the command journal prefix it was read from. Only an exact match
// reaches it, so a fresh effect or a fresh transition cannot borrow it.
type frozenBashHistory struct {
	commands map[string]frozenBashCommand
	prefixes []frozenJournalPrefix
}

type frozenBashCommand struct {
	event  productionHistoricalCommandEvent
	denied bool
	origin delta.OperationIdentity
}

type frozenJournalPrefix struct {
	journal *audit.Journal
	records []audit.Record
}

// verifyOperation checks a Bash operation whose EffectID is frozen against that exact evidence. It reports handled
// false for any other effect, which the live runner verifies.
func (f *frozenBashHistory) verifyOperation(operation sessionrepo.OperationEvidence) (bool, error) {
	if f == nil {
		return false, nil
	}
	command, frozen := f.commands[operation.EffectID]
	if !frozen {
		return false, nil
	}
	if err := f.verifyPrefixes(); err != nil {
		return true, err
	}
	if command.denied != deniedAfterEffect(operation) || !reflect.DeepEqual(command.event.Operation, operation) {
		return true, commandEvidenceRefused("frozen-operation-mismatch")
	}
	return true, nil
}

// leaseOrigin returns the origin frozen for the one promoted Bash transition that equals transition.
func (f *frozenBashHistory) leaseOrigin(transition delta.Transition) (delta.OperationIdentity, error) {
	if f == nil {
		return delta.OperationIdentity{}, ErrGovernedSessionEvidence
	}
	if err := f.verifyPrefixes(); err != nil {
		return delta.OperationIdentity{}, err
	}
	for _, command := range f.commands {
		if !command.denied && reflect.DeepEqual(command.event.Transition, transition) {
			return command.origin, nil
		}
	}
	return delta.OperationIdentity{}, ErrGovernedSessionEvidence
}

// verifyPrefixes checks that each command journal still begins with the records the frozen evidence was read from.
// Fresh Bash progress extends a journal, so only the frozen prefix is compared.
func (f *frozenBashHistory) verifyPrefixes() error {
	for _, prefix := range f.prefixes {
		trace, err := prefix.journal.Trace()
		if err != nil || len(trace.Records) < len(prefix.records) ||
			!reflect.DeepEqual(trace.Records[:len(prefix.records)], prefix.records) {
			return errors.Join(commandEvidenceRefused("frozen-prefix"), err)
		}
	}
	return nil
}
