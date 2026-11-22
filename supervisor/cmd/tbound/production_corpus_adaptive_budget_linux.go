//go:build linux

package main

import (
	"errors"
	"fmt"
	"strings"

	"tbound/supervisor/internal/adaptivesource"
	"tbound/supervisor/internal/evalcorpus"
)

// The adaptive budget is cumulative across the attempts of one case and role (D32 item 6: the caps hold
// across retries and recovery generations). Attempt 1 runs against the full caps of the pinned attacker
// artifact. Attempt 2 (the rerun of an evidenced external outage) runs against the caps minus what attempt 1
// spent, so the original and the rerun together never exceed the pinned caps.
//
// Retry semantics, decided from the thesis text. The thesis fixes the budget as cumulative and the pre-state
// as matched; it does not carry the machine's tried-step set into a retry. The retry also runs on a reset
// fixture (`tbound trial reset`), where the earlier attempt's progress no longer exists. So the budget
// continues and the machine restarts from its initial state: attempt 2 replays the library from its first
// step, over the remaining iterations, tool calls and wall time. The receipt of attempt 2 binds the
// predecessor (its admission id and state digest) and the cumulative usage, and the runner checks that the
// two attempts' usage adds up and stays within the caps.
//
// The service keeps the spend of a finished attempt in its durable catalog (pi_workflow_attacker_usage), so
// attempt 2 reads it from the service's own record and never from the request. An attempt 2 whose
// predecessor left no record (it never ran, or it was cut off before it could record) is refused: the spend
// is unknown, and granting a fresh budget would be exactly the reset this prevents.

// piServiceAttackerUsage is the durable record of the attacker spend of one finished adaptive attempt.
type piServiceAttackerUsage struct {
	Stop        string                       `json:"stop"`
	StateDigest string                       `json:"state_digest"`
	Usage       adaptivesource.AdaptiveUsage `json:"usage"`
	Cumulative  adaptivesource.AdaptiveUsage `json:"cumulative"`
	// Predecessor is the admission id of the attempt this one continued the budget of, empty for attempt 1.
	Predecessor string `json:"predecessor,omitempty"`
}

// productionAttackerPredecessor is the receipt's reference to the attempt whose budget the run continued:
// its admission id, its attacker state digest and its cumulative usage.
type productionAttackerPredecessor struct {
	AdmissionID     string                       `json:"admission_id"`
	StateDigest     string                       `json:"state_digest"`
	CumulativeUsage adaptivesource.AdaptiveUsage `json:"cumulative_usage"`
}

func validAdaptiveUsage(u adaptivesource.AdaptiveUsage) bool {
	return u.Iterations >= 0 && u.ToolCalls >= 0 && u.WallMS >= 0 && u.InTokens >= 0 && u.OutTokens >= 0
}

func validAttackerUsage(u piServiceAttackerUsage) bool {
	return validAdaptiveUsage(u.Usage) && validAdaptiveUsage(u.Cumulative) && len(u.Stop) <= 32 && len(u.StateDigest) <= 64 &&
		len(u.Predecessor) <= productionRequestMaxBytes
}

// RecordAttackerUsage persists the attacker spend of the adaptive attempt admitted under admissionHash. An
// identical second record is a no-op; a different one, a record for an unknown admission or for an admission
// that is not an adaptive role is refused.
func (c *piServiceWorkflowCatalog) RecordAttackerUsage(admissionHash string, usage piServiceAttackerUsage) error {
	if c == nil || !validBareWorkflowDigest(admissionHash) || !validAttackerUsage(usage) {
		return ErrPiWorkflowCatalog
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[admissionHash]
	if !ok || !adaptiveAdmission(entry) {
		return ErrPiWorkflowCatalog
	}
	if entry.AttackerUsage != nil {
		if *entry.AttackerUsage == usage {
			return nil
		}
		return ErrPiWorkflowCatalog
	}
	event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash, AttackerUsage: &usage}
	if err := c.appendLocked("pi_workflow_attacker_usage", workflowCatalogEventID("attacker-usage", admissionHash), event); err != nil {
		return err
	}
	entry.AttackerUsage = &usage
	c.entries[admissionHash] = entry
	return nil
}

// adaptiveAdmission reports whether the catalog entry is the admission of an adaptive replay role.
func adaptiveAdmission(entry piServiceWorkflowEntry) bool {
	role, ok := corpusAdmissionRole(entry.HostAdmissionID, entry.TaskID)
	return ok && adaptiveCorpusRole(role)
}

// attackerPredecessor returns the record of the attempt before attempt, of the same case and role, for an
// adaptive attempt after the first. The previous attempt must exist in the catalog and have recorded its
// spend.
func (c *piServiceWorkflowCatalog) attackerPredecessor(caseID, role string, attempt int) (*productionAttackerPredecessor, error) {
	if c == nil || attempt < 2 {
		return nil, ErrPiWorkflowCatalog
	}
	id := corpusAdmissionID(caseID, role, attempt-1)
	c.mu.Lock()
	defer c.mu.Unlock()
	hash, ok := c.byHostAdmissionID[id]
	if !ok {
		return nil, fmt.Errorf("%w: attempt %d of %s/%s has no attempt %d to continue the adaptive budget of", ErrPiWorkflowCatalog, attempt, caseID, role, attempt-1)
	}
	entry := c.entries[hash]
	if entry.AttackerUsage == nil {
		return nil, fmt.Errorf("%w: %s recorded no attacker usage, so the remaining adaptive budget is unknown", ErrPiWorkflowCatalog, id)
	}
	return &productionAttackerPredecessor{AdmissionID: id, StateDigest: entry.AttackerUsage.StateDigest,
		CumulativeUsage: entry.AttackerUsage.Cumulative}, nil
}

// attemptDurationSeconds is the run deadline of an admission: the trial's duration, and for an adaptive
// role at most the wall time the artifact's cumulative cap leaves, in whole seconds. An adaptive attempt with
// no pivot, tool call, whole second of wall budget (or, unscripted, token) left is not admitted.
func (c *piServiceWorkflowCatalog) attemptDurationSeconds(trial evalcorpus.Trial, request piServiceRequest) (uint64, bool) {
	if !adaptiveCorpusRole(request.Role) {
		return trial.DurationSeconds, true
	}
	if c == nil || c.corpus == nil || c.corpus.attacker == nil {
		return 0, false
	}
	var prior adaptivesource.AdaptiveUsage
	if request.Attempt > 1 {
		predecessor, err := c.attackerPredecessor(request.TaskID, request.Role, request.Attempt)
		if err != nil {
			return 0, false
		}
		prior = predecessor.CumulativeUsage
	}
	caps := c.corpus.attacker.Caps
	wall := caps.WallMS - prior.WallMS
	if caps.Iterations-prior.Iterations <= 0 || caps.ToolCalls-prior.ToolCalls <= 0 || wall < 1000 ||
		(!caps.Scripted && (caps.InTokens-prior.InTokens <= 0 || caps.OutTokens-prior.OutTokens <= 0)) {
		return 0, false
	}
	return min(uint64(wall/1000), trial.DurationSeconds), true
}

// applyAttackerUsage fills the receipt's usage members from the source's terminal and the binding's
// predecessor.
func applyAttackerUsage(receipt *productionPiReceipt, binding *corpusBinding, terminal adaptivesource.AdaptiveTerminal) {
	usage, cumulative := terminal.Usage, terminal.Cumulative
	receipt.AttackerUsage, receipt.AttackerCumulativeUsage = &usage, &cumulative
	receipt.AttackerPredecessor = binding.predecessor
}

// recordAttackerUsage persists the spend of the adaptive attempt of binding in the catalog of the admission, so
// that a following attempt reads it. The session calls it before it releases the receipt, and, deferred, on every
// other way out of the session: an attempt that fails still spent its budget, and a rerun of it must not get a
// fresh one. The first record wins; a later, different one is refused and ignored by the deferred call. A run
// that is not an adaptive replay records nothing.
func recordAttackerUsage(admission *productionWorkflowAdmission, binding *corpusBinding) error {
	if admission == nil || admission.Catalog == nil || binding == nil || binding.adaptive == nil {
		return nil
	}
	terminal := binding.adaptive.Terminal()
	record := piServiceAttackerUsage{Stop: terminal.Stop, StateDigest: terminal.StateDigest, Usage: terminal.Usage, Cumulative: terminal.Cumulative}
	if binding.predecessor != nil {
		record.Predecessor = binding.predecessor.AdmissionID
	}
	if err := admission.Catalog.RecordAttackerUsage(admission.Entry.AdmissionRecordHash, record); err != nil {
		return errors.Join(fmt.Errorf("record the attacker usage of %s", strings.TrimSpace(admission.Entry.HostAdmissionID)), err)
	}
	return nil
}
