//go:build linux

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"tbound/supervisor/internal/audit"
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
	return role == corpusRoleStaticReplay || role == corpusRolePStaticReplay
}

// pCorpusRole reports whether role is a role of the P half of a pair.
func pCorpusRole(role string) bool {
	return role == corpusRolePStaticReplay || role == corpusRolePAdaptiveReplay
}

// corpusAdmissionRole returns the role of a corpus admission id, <case_id>/<role>/<attempt>, for the
// case the catalog admitted.
func corpusAdmissionRole(admissionID, caseID string) (string, bool) {
	attemptAt := strings.LastIndex(admissionID, "/")
	if attemptAt < 0 || caseID == "" {
		return "", false
	}
	if attempt, err := strconv.Atoi(admissionID[attemptAt+1:]); err != nil || (attempt != 1 && attempt != 2) ||
		strconv.Itoa(attempt) != admissionID[attemptAt+1:] {
		return "", false
	}
	roleAt := strings.LastIndex(admissionID[:attemptAt], "/")
	if roleAt <= 0 || admissionID[:roleAt] != caseID {
		return "", false
	}
	role := admissionID[roleAt+1 : attemptAt]
	return role, validCorpusRole(role)
}

// corpusBinding is everything the service derives for one corpus workflow from its admitted role and
// the pinned manifest: where the provider turns come from, the registered delta, and the admission
// rule. Nothing in it comes from a client.
type corpusBinding struct {
	caseID string
	role   string
	// delta is the registered targeted configuration delta of the run: the case's p_delta for a P role,
	// none for every C role.
	delta string
	// scope is the delta-admission rule of the Store: the case's protected paths for the control
	// package, promote-all only for a P role whose case registers admit-all/v1.
	scope  sessionrepo.AdmissionScope
	script []evalcorpus.ScriptTurn
}

// bindingFor derives the binding of role for caseID, or reports that this build cannot serve it.
func (c *productionCorpus) bindingFor(caseID, role string) (corpusBinding, error) {
	if !c.roleServable(caseID, role) {
		return corpusBinding{}, fmt.Errorf("%w: role %q is not served for case %q", ErrPiServiceCorpus, role, caseID)
	}
	trial := c.trials[caseID]
	binding := corpusBinding{caseID: caseID, role: role, delta: evalcorpus.PDeltaNone,
		scope: sessionrepo.AdmissionScope{ProtectedPaths: append([]string(nil), trial.ProtectedPaths...)}}
	if pCorpusRole(role) {
		binding.delta = trial.EffectivePDelta()
		if binding.delta == evalcorpus.PDeltaAdmitAll {
			binding.scope = sessionrepo.AdmissionScope{Rule: sessionrepo.AdmissionScopeRulePromoteAll}
		}
	}
	if scriptedCorpusRole(role) {
		binding.script = append([]evalcorpus.ScriptTurn(nil), trial.Script...)
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
	role, ok := corpusAdmissionRole(entry.HostAdmissionID, entry.TaskID)
	if !ok {
		return nil, ErrPiWorkflowCatalog
	}
	binding, err := c.corpus.bindingFor(entry.TaskID, role)
	if err != nil {
		return nil, errors.Join(ErrPiWorkflowCatalog, err)
	}
	return &binding, nil
}

func (b corpusBinding) scripted() bool { return scriptedCorpusRole(b.role) }

func (b corpusBinding) providerMode() string {
	if b.scripted() {
		return providerModeScripted
	}
	return providerModeModel
}

// scriptDigest is the digest of the frozen script a scripted run replays, empty for a model run.
func (b corpusBinding) scriptDigest() string {
	if !b.scripted() {
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
}

// maxReceiptProposals bounds the proposal list so that the receipt stays within its size bound.
const maxReceiptProposals = 64

// receiptProposals lists the gate decisions of the provider journal in order. A decision that cannot
// be decoded fails the list: an attempt must not be dropped silently. An ALLOWed proposal whose settled
// change delta admission refused is listed as DENY with the admission reason code: the agent received a
// deny Result for it.
func receiptProposals(trace audit.Trace) ([]productionProposalFact, error) {
	admissionDenials := make(map[string]string)
	for _, record := range trace.Records {
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
	} else if providerExchange {
		receipt.ProviderMode = providerModeModel
	}
	if traceErr != nil {
		return nil // already reported by the caller
	}
	proposals, err := receiptProposals(providerTrace)
	receipt.Proposals = proposals
	return err
}
