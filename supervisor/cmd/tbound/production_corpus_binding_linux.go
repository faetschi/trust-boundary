//go:build linux

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"tbound/supervisor/internal/adaptivesource"
	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

// The provider modes of a corpus workflow, as the receipt reports them.
const (
	providerModeModel    = "model"
	providerModeScripted = "scripted"
)

// ErrScriptedRoleNotRecoverable: a workflow of a scripted role is never resumed. Its recovery pins
// would have to carry the script cursor, and a replay that restarts at turn n is not the frozen
// stimulus. It is refused; the interrupted run stays unknown.
var ErrScriptedRoleNotRecoverable = errors.New("a scripted replay workflow cannot be recovered")

// scriptedCorpusRole reports whether the provider of role is a script. Only the admitted role decides
// this: no request member, config member or environment variable does.
func scriptedCorpusRole(role string) bool {
	return staticCorpusRole(role) || adaptiveCorpusRole(role)
}

// staticCorpusRole reports whether role replays the frozen script of its case.
func staticCorpusRole(role string) bool {
	return role == corpusRoleStaticReplay || role == corpusRolePStaticReplay
}

// adaptiveCorpusRole reports whether role is driven by the attacker machine over the case's pivot table:
// the C and the P half run the identical machine, pivots and budget.
func adaptiveCorpusRole(role string) bool {
	return role == corpusRoleAdaptiveReplay || role == corpusRolePAdaptiveReplay
}

// pCorpusRole reports whether role is a role of the P half of a pair.
func pCorpusRole(role string) bool {
	return role == corpusRolePStaticReplay || role == corpusRolePAdaptiveReplay
}

// bCorpusRole reports whether role is a role of Configuration B. Only the admitted role selects the
// B composition: it keeps the real model and the production proxy tool path and relaxes delta admission to
// admit-all/v1 (no protected-path denial, no bash output-scope denial). The production gate profile
// already allows every registered tool, so B differs from C in admission only; the gate keeps its
// correlation and argument checks, which are the adapter and transport controls B is meant to contain.
func bCorpusRole(role string) bool {
	return role == corpusRoleBCompliant || role == corpusRoleBAmbiguous
}

// corpusAdmissionRole returns the role of a corpus admission id, <case_id>/<role>/<attempt>, for the
// case the catalog admitted.
func corpusAdmissionRole(admissionID, caseID string) (string, bool) {
	role, _, ok := corpusAdmissionParts(admissionID, caseID)
	return role, ok
}

// corpusAdmissionParts returns the role and the attempt (1 or 2) of a corpus admission id.
func corpusAdmissionParts(admissionID, caseID string) (string, int, bool) {
	attemptAt := strings.LastIndex(admissionID, "/")
	if attemptAt < 0 || caseID == "" {
		return "", 0, false
	}
	attempt, err := strconv.Atoi(admissionID[attemptAt+1:])
	if err != nil || (attempt != 1 && attempt != 2) || strconv.Itoa(attempt) != admissionID[attemptAt+1:] {
		return "", 0, false
	}
	roleAt := strings.LastIndex(admissionID[:attemptAt], "/")
	if roleAt <= 0 || admissionID[:roleAt] != caseID {
		return "", 0, false
	}
	role := admissionID[roleAt+1 : attemptAt]
	return role, attempt, validCorpusRole(role)
}

// corpusBinding is everything the service derives for one corpus workflow from its admitted role and
// the pinned manifest: where the provider turns come from, the registered delta, and the admission
// rule. Nothing in it comes from a client.
type corpusBinding struct {
	hintProvider       providerbridge.HintProvider
	hintChooser        string
	hintManifestDigest string
	caseID             string
	role               string
	// delta is the registered targeted configuration delta of the run: the case's p_delta for a P role,
	// admit-all/v1 for a B role (its admission rule), none for every C role.
	delta string
	// scope is the delta-admission rule of the Store: the case's protected paths for the control
	// package, promote-all only for a P role whose case registers admit-all/v1.
	scope  sessionrepo.AdmissionScope
	script []evalcorpus.ScriptTurn
	// adaptive is the attacker-driven source of an adaptive replay role. It is shared by the provider factory
	// and the receipt, which reads its terminal state. attackerDigest and pivotsDigest identify the pinned
	// artifact and the case's pivot table.
	adaptive       *adaptivesource.AdaptiveSource
	attackerDigest string
	pivotsDigest   string
	// predecessor is the earlier attempt of the case and role whose attacker budget this attempt continues
	// (production_corpus_adaptive_budget_linux.go); nil for attempt 1.
	predecessor *productionAttackerPredecessor
}

// bindingFor derives the binding of role for caseID, or reports that this build cannot serve it. The options
// configure the adaptive source of an adaptive role (the budget a retry continues).
func (c *productionCorpus) bindingFor(caseID, role string, options ...adaptivesource.AdaptiveOption) (corpusBinding, error) {
	if !c.roleServable(caseID, role) {
		return corpusBinding{}, fmt.Errorf("%w: role %q is not served for case %q", ErrPiServiceCorpus, role, caseID)
	}
	trial := c.trials[caseID]
	binding := corpusBinding{caseID: caseID, role: role, delta: evalcorpus.PDeltaNone,
		scope: sessionrepo.AdmissionScope{ProtectedPaths: append([]string(nil), trial.ProtectedPaths...)}}
	if model, _, ok := corpusadmit.CDHRoleParts(role); ok {
		binding.hintChooser = model
		binding.hintProvider = c.hintProviders[binding.hintChooser]
		binding.hintManifestDigest, _ = c.cdh.Manifest.Digest()
	}
	if pCorpusRole(role) {
		binding.delta = trial.EffectivePDelta()
		if binding.delta == evalcorpus.PDeltaAdmitAll {
			binding.scope = sessionrepo.AdmissionScope{Rule: sessionrepo.AdmissionScopeRulePromoteAll}
		}
	}
	if bCorpusRole(role) {
		binding.delta = evalcorpus.PDeltaAdmitAll
		binding.scope = sessionrepo.AdmissionScope{Rule: sessionrepo.AdmissionScopeRulePromoteAll}
	}
	if staticCorpusRole(role) {
		binding.script = append([]evalcorpus.ScriptTurn(nil), trial.Script...)
	}
	if adaptiveCorpusRole(role) {
		pivots := make(map[string]providerbridge.ScriptTurn, len(trial.Pivots))
		for id, turn := range trial.Pivots {
			pivots[id] = providerbridge.ScriptTurn{Tool: turn.Tool, Arguments: append([]byte(nil), turn.Arguments...), Text: turn.Text}
		}
		source, err := adaptivesource.NewAdaptiveSource(*c.attacker, pivots, options...)
		if err != nil {
			return corpusBinding{}, fmt.Errorf("%w: adaptive source of case %q: %w", ErrPiServiceCorpus, caseID, err)
		}
		digest, err := evalcorpus.PivotsDigest(trial.Pivots)
		if err != nil {
			return corpusBinding{}, fmt.Errorf("%w: pivots of case %q: %w", ErrPiServiceCorpus, caseID, err)
		}
		binding.adaptive, binding.attackerDigest, binding.pivotsDigest = source, c.attackerDigest, digest
	}
	return binding, binding.scope.Validate()
}

// bindingForEntry is the binding of a catalog entry, or nil for an entry that is not a corpus admission.
func (c *piServiceWorkflowCatalog) bindingForEntry(entry piServiceWorkflowEntry) (*corpusBinding, error) {
	if entry.CorpusManifestDigest == "" {
		return nil, nil
	}
	if c == nil || c.corpus == nil || c.corpus.digest != entry.CorpusManifestDigest {
		return nil, ErrPiWorkflowCatalog
	}
	role, attempt, ok := corpusAdmissionParts(entry.HostAdmissionID, entry.TaskID)
	if !ok {
		return nil, ErrPiWorkflowCatalog
	}
	// An adaptive retry continues the cumulative attacker budget of the attempt before it, read from the
	// service's own catalog.
	var options []adaptivesource.AdaptiveOption
	var predecessor *productionAttackerPredecessor
	if adaptiveCorpusRole(role) && attempt > 1 {
		var err error
		if predecessor, err = c.attackerPredecessor(entry.TaskID, role, attempt); err != nil {
			return nil, err
		}
		options = append(options, adaptivesource.WithPriorUsage(predecessor.CumulativeUsage))
	}
	binding, err := c.corpus.bindingFor(entry.TaskID, role, options...)
	if err != nil {
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	binding.predecessor = predecessor
	return &binding, nil
}

func (b corpusBinding) scripted() bool { return scriptedCorpusRole(b.role) }

func (b corpusBinding) providerMode() string {
	if b.scripted() {
		return providerModeScripted
	}
	return providerModeModel
}

// scriptDigest is the digest of the frozen script a static replay run replays, empty for every other run
// (an adaptive replay reports pivots_digest and attacker_digest instead).
func (b corpusBinding) scriptDigest() string {
	if !staticCorpusRole(b.role) {
		return ""
	}
	digest, err := evalcorpus.ScriptDigest(b.script)
	if err != nil {
		return ""
	}
	return digest
}

// admissionPolicyDigest commits to the admission rule of the run under the Store's pinned policies.
func (b corpusBinding) admissionPolicyDigest(policyDigest, metadataPolicyDigest string) (string, error) {
	return sessionrepo.AdmissionPolicyDigest(policyDigest, metadataPolicyDigest, b.scope)
}

// scriptedFactory is the conversation factory of a scripted role: the real Conversation over a static
// source holding the manifest's frozen script. It is nil for a model role, which keeps the route's
// factory.
func (b corpusBinding) scriptedFactory() func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error) {
	if !b.scripted() {
		return nil
	}
	if b.adaptive != nil {
		source := b.adaptive
		return func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
			return providerbridge.NewScriptedConversation(config, journal, source)
		}
	}
	turns := make([]providerbridge.ScriptTurn, len(b.script))
	for i, turn := range b.script {
		turns[i] = providerbridge.ScriptTurn{Tool: turn.Tool, Arguments: append([]byte(nil), turn.Arguments...), Text: turn.Text}
	}
	return func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		return providerbridge.NewScriptedConversation(config, journal, providerbridge.NewStaticScript(turns))
	}
}

// scriptedRecoveryRefusal refuses to resume a workflow of a scripted role.
func scriptedRecoveryRefusal(admission *productionWorkflowAdmission) error {
	if admission == nil || admission.Catalog == nil {
		return nil
	}
	binding, err := admission.Catalog.bindingForEntry(admission.Entry)
	if err != nil {
		return err
	}
	if binding != nil && binding.scripted() {
		return fmt.Errorf("%w: case %q role %q", ErrScriptedRoleNotRecoverable, binding.caseID, binding.role)
	}
	return nil
}

// BindCorpusBinding applies the binding of an admitted corpus workflow to the authority: the Store's
// admission scope and, for a scripted role, the scripted conversation factory in place of the route's.
// It is called once, before the Store is created and before any provider conversation. The role of
// the admission is the only input.
func (a *SessionOperationAuthority) BindCorpusBinding(binding corpusBinding) error {
	if a == nil {
		return ErrGovernedSessionEvidence
	}
	if err := binding.scope.Validate(); err != nil {
		return fmt.Errorf("corpus admission scope: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store != nil || a.conversation != nil {
		return errors.New("corpus binding must be applied before the Store and the provider conversation exist")
	}
	a.admissionScope = binding.scope
	a.hintProvider = binding.hintProvider
	if factory := binding.scriptedFactory(); factory != nil {
		a.providerFactory = factory
	}
	return nil
}

// productionProposalFact is the gate verdict of one tool proposal in the receipt.
type productionProposalFact struct {
	Tool       string `json:"tool"`
	Verdict    string `json:"verdict"`
	ReasonCode string `json:"reason_code"`
	Status     string `json:"status,omitempty"`
}

// maxReceiptProposals bounds the proposal list so that the receipt stays within its size bound.
const maxReceiptProposals = 64

// receiptProposals lists the gate decisions of the provider journal in order. A decision that cannot
// be decoded fails the list: an attempt must not be dropped silently. An ALLOWed proposal whose settled
// change delta admission refused is listed as DENY with the admission reason code: the agent received a
// deny Result for it. Settled command_failed status is retained separately from the gate verdict.
func receiptProposals(trace audit.Trace) ([]productionProposalFact, error) {
	admissionDenials := make(map[string]string)
	statuses := make(map[string]string)
	for _, record := range trace.Records {
		if record.Event.Kind == "provider_tool_result" {
			var evidence providerResultEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &evidence); err != nil {
				return nil, fmt.Errorf("decode provider result %d for the receipt: %w", record.Sequence, err)
			}
			result, err := protocol.DecodeResult(evidence.Result)
			if err != nil {
				return nil, fmt.Errorf("decode protocol result %d for the receipt: %w", record.Sequence, err)
			}
			statuses[evidence.ToolCallID] = string(result.Status)
		}
		if record.Event.Kind != "provider_admission_denied" {
			continue
		}
		var denial providerAdmissionDeniedEvidence
		if err := unmarshalGovernedJSON(record.Event.Data, &denial); err != nil || denial.ToolCallID == "" {
			return nil, fmt.Errorf("decode provider admission denial %d for the receipt: %w", record.Sequence, err)
		}
		admissionDenials[denial.ToolCallID] = denial.ReasonCode
	}
	var facts []productionProposalFact
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_gate_decision" {
			continue
		}
		var decision gate.Decision
		if err := unmarshalGovernedJSON(record.Event.Data, &decision); err != nil {
			return nil, fmt.Errorf("decode provider gate decision %d for the receipt: %w", record.Sequence, err)
		}
		if len(facts) == maxReceiptProposals {
			return nil, fmt.Errorf("the run has more than %d proposals; the receipt cannot list them", maxReceiptProposals)
		}
		fact := productionProposalFact{Tool: decision.Tool, Verdict: string(decision.Verdict), ReasonCode: decision.ReasonCode}
		fact.Status = statuses[decision.ToolCallID]
		if reason, denied := admissionDenials[decision.ToolCallID]; denied && decision.Verdict == gate.Allow {
			fact.Verdict, fact.ReasonCode = string(gate.Deny), reason
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// applyCorpusReceipt fills the receipt members that describe the provider, the delta and the admission
// rule of the run. A model conversation without a corpus binding reports provider_mode "model" only.
func applyCorpusReceipt(receipt *productionPiReceipt, binding *corpusBinding, providerExchange bool,
	policyDigest, metadataPolicyDigest string, providerTrace audit.Trace, traceErr error) error {
	if binding != nil {
		receipt.ProviderMode, receipt.ScriptDigest, receipt.Delta = binding.providerMode(), binding.scriptDigest(), binding.delta
		digest, err := binding.admissionPolicyDigest(policyDigest, metadataPolicyDigest)
		if err != nil {
			return fmt.Errorf("admission policy digest for the receipt: %w", err)
		}
		receipt.AdmissionPolicyDigest = digest
		if binding.adaptive != nil {
			terminal := binding.adaptive.Terminal()
			receipt.PivotsIssued, receipt.AttackerStop, receipt.AttackerStateDigest = terminal.PivotsIssued, terminal.Stop, terminal.StateDigest
			receipt.AttackerDigest, receipt.PivotsDigest = binding.attackerDigest, binding.pivotsDigest
			applyAttackerUsage(receipt, binding, terminal)
		}
	} else if providerExchange {
		receipt.ProviderMode = providerModeModel
	}
	if traceErr != nil {
		return nil // already reported by the caller
	}
	proposals, err := receiptProposals(providerTrace)
	receipt.Proposals = proposals
	if err == nil {
		err = cdhReceipt(receipt, binding, providerTrace)
	}
	return err
}
