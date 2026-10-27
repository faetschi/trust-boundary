//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

// productionCommandRunner is the CLI's lease/settlement wrapper for a signed
// D09 command-cell runner. It validates the concrete Runtime A runner type and
// proof-verifier interface, registers the authenticated view/lease, and retains
// one-use settlement receipts. It never falls back to the development
// sandbox. A missing or incompatible
// runtime API fails at lease registration before a Bash effect can start.
var ErrProductionBashRunnerUnavailable = errors.New("no registered signed D09 Bash command-cell runner is configured")

type productionCommandRunnerFactory func(leaseID string) (sessionrepo.CommandRunner, error)

type productionD09RunEvidence struct {
	leaseID         string
	viewID          string
	receipt         piruntime.D09CommandReceipt
	receiptSnapshot []byte
	receiptDigest   string
	operation       sessionrepo.OperationEvidence
	transition      delta.Transition
	providerCall    delta.OperationIdentity
	// denied marks a command whose settled change delta admission refused: transition then holds the
	// refused candidate, which was never promoted.
	denied      bool
	binding     bool
	journalSeq  uint64
	journalHash string
	eventID     string
}

type productionD09CommandSettledEvent struct {
	SchemaVersion  string                        `json:"schema_version"`
	RunID          string                        `json:"run_id"`
	SessionID      string                        `json:"session_id"`
	WorkflowID     string                        `json:"workflow_id"`
	ConversationID string                        `json:"conversation_id"`
	ProfileID      string                        `json:"profile_id"`
	ProfileDigest  string                        `json:"profile_digest"`
	ProviderCall   delta.OperationIdentity       `json:"provider_call"`
	Operation      sessionrepo.OperationEvidence `json:"operation_evidence"`
	Transition     delta.Transition              `json:"approved_transition"`
	ReceiptDigest  string                        `json:"receipt_digest"`
	Receipt        json.RawMessage               `json:"receipt"`
}

const (
	productionD09SettledKind          = "production_d09_command_settled"
	productionD09SettledSchemaSuccess = "tbound-production-d09-command-settled/v2"
	productionD09SettledSchemaDenied  = "tbound-production-d09-command-settled-denied/v1"
	productionD09OutcomeDenied        = "denied_after_effect"
)

// productionD09DeniedCommandSettledEvent is the second shape of a production_d09_command_settled
// record: the command ran and settled on its D09 cell, then delta admission refused its change, so
// nothing was promoted. It binds the receipt to the Store's denied_after_effect operation, whose
// admission reference names the retained admission record by sequence and hash, and to the refused
// candidate. It carries no approved transition.
type productionD09DeniedCommandSettledEvent struct {
	SchemaVersion         string                        `json:"schema_version"`
	Outcome               string                        `json:"outcome"`
	RunID                 string                        `json:"run_id"`
	SessionID             string                        `json:"session_id"`
	WorkflowID            string                        `json:"workflow_id"`
	ConversationID        string                        `json:"conversation_id"`
	ProfileID             string                        `json:"profile_id"`
	ProfileDigest         string                        `json:"profile_digest"`
	ProviderCall          delta.OperationIdentity       `json:"provider_call"`
	Operation             sessionrepo.OperationEvidence `json:"operation_evidence"`
	DeniedCandidate       delta.Transition              `json:"denied_candidate"`
	DeniedCandidateDigest string                        `json:"denied_candidate_digest"`
	ReceiptDigest         string                        `json:"receipt_digest"`
	Receipt               json.RawMessage               `json:"receipt"`
}

func productionD09DeniedCandidateDigest(candidate delta.Transition) string {
	return digestBytes(mustJSON(candidate))
}

type productionD09CommandEffectResult struct {
	Admission     sessionrepo.AdmissionReference `json:"admission"`
	Transition    delta.Transition               `json:"transition"`
	Command       sessionrepo.CommandResult      `json:"command"`
	Settlement    sessionrepo.CommandSettlement  `json:"settlement"`
	Spec          sessionrepo.CommandSpec        `json:"spec"`
	RunnerProfile string                         `json:"runner_profile"`
}

type productionD09CommandInvoker interface {
	Run(context.Context, *sessionrepo.CommandView, sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error)
	TakeCommandReceipt(*sessionrepo.CommandView, sessionrepo.CommandSpec, sessionrepo.CommandResult,
		sessionrepo.CommandSettlement) (piruntime.D09CommandReceipt, error)
}

// productionD09BashRunner binds one Runtime A D09 runner to one authenticated
// lease. It consumes the runtime's one-use TCB receipt immediately after Run,
// then binds it to the Store's actual OperationEvidence and delta in one
// command-journal event before DurableExecutor releases any result.
type productionD09BashRunner struct {
	leaseID        string
	inner          productionD09CommandInvoker
	journal        *audit.Journal
	runID          string
	sessionID      string
	workflowID     string
	conversationID string
	profile        piruntime.HostProfile
	template       piruntime.D09CellTemplate
	mu             sync.Mutex
	evidence       map[string]productionD09RunEvidence
	// delta is the registered command-cell delta of the lease; the zero value is the signed base cell.
	delta piruntime.D09CellDelta
}

func newProductionD09BashRunner(leaseID string, inner *piruntime.D09CommandRunner, journal *audit.Journal,
	runID, sessionID, workflowID, conversationID string, profile piruntime.HostProfile, template piruntime.D09CellTemplate) *productionD09BashRunner {
	return newProductionD09BashRunnerWithInvoker(leaseID, inner, journal, runID, sessionID, workflowID,
		conversationID, profile, template)
}

func newProductionD09BashRunnerWithInvoker(leaseID string, inner productionD09CommandInvoker, journal *audit.Journal,
	runID, sessionID, workflowID, conversationID string, profile piruntime.HostProfile, template piruntime.D09CellTemplate) *productionD09BashRunner {
	return &productionD09BashRunner{leaseID: leaseID, inner: inner, journal: journal, runID: runID,
		sessionID: sessionID, workflowID: workflowID, conversationID: conversationID, profile: profile, template: template,
		evidence: make(map[string]productionD09RunEvidence)}
}

func (r *productionD09BashRunner) Profile() string {
	if r == nil || r.inner == nil || r.leaseID == "" {
		return "production-d09-bash-runner-unavailable"
	}
	return piruntime.D09CommandRunnerProfile
}

func (r *productionD09BashRunner) Run(ctx context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	if r == nil || r.inner == nil || r.journal == nil || ctx == nil || view == nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrProductionBashRunnerUnavailable
	}
	authorizedSpec := cloneProductionD09CommandSpec(spec)
	runnerSpec := cloneProductionD09CommandSpec(authorizedSpec)
	result, settlement, err := r.inner.Run(ctx, view, runnerSpec)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	// Consume the exact runtime-issued proof receipt before any further work or
	// result release. TakeCommandReceipt is one-use and validates this view,
	// canonical spec, actual result, and actual settlement against the invocation.
	if !reflect.DeepEqual(runnerSpec, authorizedSpec) {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, commandEvidenceRefused("runner-modified-spec", errors.New("D09 runner modified the authorized command spec"))
	}
	runtimeReceipt, err := r.inner.TakeCommandReceipt(view, runnerSpec, result, settlement)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, commandEvidenceRefused("take-receipt", err)
	}
	if err := runtimeReceipt.Validate(); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, commandEvidenceRefused("receipt-seal", err)
	}
	receiptDigest, err := runtimeReceipt.Digest()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, commandEvidenceRefused("receipt-digest", err)
	}
	receiptSnapshot, err := json.Marshal(runtimeReceipt)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, commandEvidenceRefused("receipt-snapshot", err)
	}
	if runtimeReceipt.ReceiptDigest != receiptDigest || runtimeReceipt.LeaseID != r.leaseID ||
		runtimeReceipt.ViewID != view.ID() || runtimeReceipt.Settlement != settlement ||
		runtimeReceipt.ExitCode != result.ExitCode || !result.ExitObserved {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, commandEvidenceRefused("receipt-identity")
	}
	if err := validateProductionD09ReceiptInvocation(view, runnerSpec, result, settlement, runtimeReceipt, r.profile, r.template, r.delta); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, commandEvidenceRefused("receipt-invocation", err)
	}
	if settlement.LeaseID != r.leaseID || settlement.ViewID != view.ID() || !settlement.ExitObserved ||
		!settlement.Admitted() || settlement.SurvivorsTolerated != r.delta.NoReap() ||
		!strings.HasPrefix(settlement.EvidenceClass, piruntime.D09CommandRunnerProfile+":") {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, commandEvidenceRefused("settlement-admission")
	}
	receipt := productionD09RunEvidence{leaseID: r.leaseID, viewID: view.ID(), receipt: runtimeReceipt,
		receiptSnapshot: append([]byte(nil), receiptSnapshot...), receiptDigest: receiptDigest}
	r.mu.Lock()
	if _, duplicate := r.evidence[view.ID()]; duplicate {
		r.mu.Unlock()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("D09 command view already has a retained proof receipt")
	}
	r.evidence[view.ID()] = receipt
	r.mu.Unlock()
	return result, settlement, nil
}

func validateProductionD09ReceiptInvocation(view *sessionrepo.CommandView, spec sessionrepo.CommandSpec,
	result sessionrepo.CommandResult, settlement sessionrepo.CommandSettlement, receipt piruntime.D09CommandReceipt,
	profile piruntime.HostProfile, template piruntime.D09CellTemplate, cell piruntime.D09CellDelta) error {
	if view == nil || !result.ExitObserved || settlement.LeaseID != receipt.LeaseID || settlement.ViewID != view.ID() ||
		receipt.ViewID != view.ID() || receipt.Settlement != settlement || receipt.ExitCode != result.ExitCode ||
		profile.Mode != piruntime.ModeProduction || profile.ContainmentProfile != piruntime.D09ContainmentProfile ||
		profile.SettlementProfile != piruntime.D09SettlementProfile {
		return commandEvidenceRefused("invocation-profile")
	}
	templateDigest, err := template.Digest()
	cellRuleset, rulesetErr := template.DeltaLandlockRulesetDigest(cell)
	cellTemplate, cellErr := cell.TemplateDigest(templateDigest)
	if !cell.Relaxed() {
		cellTemplate = ""
	}
	if err == nil {
		err = errors.Join(rulesetErr, cellErr)
	}
	if err != nil || templateDigest != profile.ContainmentProfileDigest || receipt.TemplateDigest != templateDigest {
		return commandEvidenceRefused("invocation-template", err)
	}
	if receipt.ImageDigest != template.ExpectedImageDigest || receipt.ImageRef != registeredD09ImageDigestRef(template) {
		return commandEvidenceRefused("invocation-image")
	}
	if template.CommandExecutable != spec.Executable || receipt.Proof.CommandDigest != template.CommandExecutableDigest || receipt.Proof.CommandPath != spec.Executable ||
		receipt.Proof.CommandWorkdir != filepath.Clean(spec.WorkingDirectory) {
		return commandEvidenceRefused("invocation-command")
	}
	if receipt.Proof.UID != template.CellUID || receipt.Proof.GID != template.CellGID || receipt.Proof.CapEff != "0000000000000000" ||
		!receipt.Proof.NoNewPrivs || receipt.Proof.Seccomp != 2 || !receipt.Proof.CgroupV2 {
		return commandEvidenceRefused("invocation-credentials")
	}
	if !receipt.Proof.LandlockTSYNC || receipt.Proof.LandlockABI < template.LandlockABI || receipt.Proof.LandlockRulesetDigest != cellRuleset {
		return commandEvidenceRefused("invocation-landlock")
	}
	if receipt.Proof.NoExternalRoutes != cell.ExpectNoExternalRoutes() || receipt.Proof.Delta != cell.Name() ||
		receipt.Proof.DeltaTemplateDigest != cellTemplate {
		return commandEvidenceRefused("invocation-delta")
	}
	if !receipt.Proof.ConsumerMountsVerified || !receipt.Proof.RawSourceFDsClosed ||
		receipt.Proof.WorkerMountID == 0 || receipt.Proof.WorkerMountID != receipt.Proof.RuntimeMountID {
		return commandEvidenceRefused("invocation-consumer-mount")
	}
	encodedSpec, err := json.Marshal(spec)
	if err != nil || digestBytes(encodedSpec) != receipt.CommandSpecDigest {
		return commandEvidenceRefused("invocation-spec-digest", err)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil || digestBytes(encodedResult) != receipt.ResultDigest {
		return commandEvidenceRefused("invocation-result-digest", err)
	}
	encodedSettlement, err := json.Marshal(settlement)
	if err != nil || digestBytes(encodedSettlement) != receipt.SettlementDigest {
		return commandEvidenceRefused("invocation-settlement-digest", err)
	}
	source, err := view.MountSource()
	if err != nil {
		return commandEvidenceRefused("view-mount-source", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.IsDir() {
		return commandEvidenceRefused("view-not-directory", err)
	}
	// The proof's mount ID is the cell's own /work mount in its private mount namespace, never this
	// host descriptor's mount; the view binds by device/inode (docs/runtime/governed-session-composition.md).
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != receipt.Proof.WorkerDevice || stat.Ino != receipt.Proof.WorkerInode ||
		uint64(stat.Dev) != receipt.Proof.RuntimeDevice || stat.Ino != receipt.Proof.RuntimeInode {
		return commandEvidenceRefused("view-device-inode")
	}
	return nil
}

func registeredD09ImageDigestRef(template piruntime.D09CellTemplate) string {
	image := template.Image
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		image = image[:colon]
	}
	return image + "@" + template.ExpectedImageDigest
}

func productionDescriptorMountID(fd uintptr) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "mnt_id:" {
			mountID, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || mountID == 0 {
				return 0, errors.New("command-view descriptor mount ID is invalid")
			}
			return mountID, nil
		}
	}
	return 0, errors.New("command-view descriptor fdinfo omitted mount ID")
}

func (r *productionD09BashRunner) BindProductionBashEvidence(ctx context.Context, providerCall delta.OperationIdentity,
	bundle sessionrepo.EvidenceBundle, transition delta.Transition, operation sessionrepo.OperationEvidence) error {
	return r.bindProductionEvidence(ctx, providerCall, bundle, transition, operation, false)
}

// BindProductionDeniedBashEvidence settles a command that ran and whose change delta admission refused
// (denied_after_effect): the receipt is bound to the Store's denied operation and the refused candidate
// instead of an approved transition.
func (r *productionD09BashRunner) BindProductionDeniedBashEvidence(ctx context.Context, providerCall delta.OperationIdentity,
	bundle sessionrepo.EvidenceBundle, candidate delta.Transition, operation sessionrepo.OperationEvidence) error {
	return r.bindProductionEvidence(ctx, providerCall, bundle, candidate, operation, true)
}

func (r *productionD09BashRunner) bindProductionEvidence(ctx context.Context, providerCall delta.OperationIdentity,
	bundle sessionrepo.EvidenceBundle, transition delta.Transition, operation sessionrepo.OperationEvidence, denied bool) error {
	if r == nil || r.inner == nil || r.journal == nil || ctx == nil || operation.ViewID == "" {
		return commandEvidenceRefused("bind-precondition")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	receipt, exists := r.evidence[operation.ViewID]
	if !exists || receipt.journalSeq != 0 || receipt.binding {
		r.mu.Unlock()
		return commandEvidenceRefused("bind-receipt-unavailable")
	}
	receipt.binding = true
	r.evidence[operation.ViewID] = receipt
	r.mu.Unlock()
	if err := validateProductionD09Operation(bundle, transition, operation, receipt.receipt, denied); err != nil {
		return commandEvidenceRefused("bind-operation-bundle", err)
	}
	if providerCall.Kind != delta.OperationProposalCall || providerCall.ProposalID == "" ||
		providerCall.CallIssuer == "" || providerCall.CallID == "" || transition.Operation.Kind != delta.OperationLease ||
		transition.Operation.LeaseID != receipt.leaseID || operation.Operation != transition.Operation {
		return commandEvidenceRefused("bind-provider-call")
	}
	eventID := "production-d09-command:" + r.runID + ":" + receipt.leaseID + ":" + receipt.viewID + ":" + operation.EffectID
	data, err := r.marshalProductionD09Event(providerCall, transition, operation, receipt, denied)
	if err != nil {
		return commandEvidenceRefused("bind-event-encode", err)
	}
	before, err := r.journal.Trace()
	if err != nil {
		return commandEvidenceRefused("bind-journal-trace", err)
	}
	for _, record := range before.Records {
		if record.Event.Kind == productionD09SettledKind && record.Event.ID == eventID {
			return commandEvidenceRefused("bind-event-duplicate")
		}
	}
	journalRecord, err := r.journal.Append(audit.Event{Kind: productionD09SettledKind, ID: eventID, Data: data})
	if err != nil {
		return fmt.Errorf("durably bind D09 receipt to actual Store operation before result release: %w", err)
	}
	receipt.operation, receipt.transition, receipt.providerCall, receipt.denied = operation, transition, providerCall, denied
	receipt.journalSeq, receipt.journalHash, receipt.eventID = journalRecord.Sequence, journalRecord.Hash, eventID
	if err := r.verifyProductionBashEvent(ctx, receipt, operation, transition, providerCall, bundle, journalRecord); err != nil {
		return commandEvidenceRefused("bind-event-verify", err)
	}
	r.mu.Lock()
	r.evidence[operation.ViewID] = receipt
	r.mu.Unlock()
	return nil
}

func (r *productionD09BashRunner) marshalProductionD09Event(providerCall delta.OperationIdentity,
	transition delta.Transition, operation sessionrepo.OperationEvidence, receipt productionD09RunEvidence, denied bool) ([]byte, error) {
	if denied {
		return json.Marshal(productionD09DeniedCommandSettledEvent{
			SchemaVersion: productionD09SettledSchemaDenied, Outcome: productionD09OutcomeDenied,
			RunID: r.runID, SessionID: r.sessionID, WorkflowID: r.workflowID, ConversationID: r.conversationID,
			ProfileID: r.profile.ID, ProfileDigest: r.profile.Digest,
			ProviderCall: providerCall, Operation: operation, DeniedCandidate: transition,
			DeniedCandidateDigest: productionD09DeniedCandidateDigest(transition),
			ReceiptDigest:         receipt.receiptDigest, Receipt: append(json.RawMessage(nil), receipt.receiptSnapshot...),
		})
	}
	return marshalProductionD09CommandEvent(r, providerCall, transition, operation, receipt)
}

func marshalProductionD09CommandEvent(r *productionD09BashRunner, providerCall delta.OperationIdentity,
	transition delta.Transition, operation sessionrepo.OperationEvidence, receipt productionD09RunEvidence) ([]byte, error) {
	return json.Marshal(productionD09CommandSettledEvent{
		SchemaVersion: productionD09SettledSchemaSuccess,
		RunID:         r.runID, SessionID: r.sessionID, WorkflowID: r.workflowID, ConversationID: r.conversationID,
		ProfileID: r.profile.ID, ProfileDigest: r.profile.Digest,
		ProviderCall: providerCall, Operation: operation, Transition: transition,
		ReceiptDigest: receipt.receiptDigest, Receipt: append(json.RawMessage(nil), receipt.receiptSnapshot...),
	})
}

func cloneProductionD09CommandSpec(spec sessionrepo.CommandSpec) sessionrepo.CommandSpec {
	if spec.Args != nil {
		args := make([]string, len(spec.Args))
		copy(args, spec.Args)
		spec.Args = args
	}
	spec.DeclaredOutputPaths = cloneProductionD09Paths(spec.DeclaredOutputPaths)
	return spec
}

func cloneProductionD09Paths(paths []string) []string {
	if paths == nil {
		return nil
	}
	cloned := make([]string, len(paths))
	copy(cloned, paths)
	return cloned
}

// The caller first validates the entire bundle with Store replay. This decoder
// extracts the already-validated intent and restores the scope omitted from
// the execution-only CommandSpec JSON used by D09 receipts.
func decodeProductionD09CommandIntent(raw []byte) (sessionrepo.OperationRequest, sessionrepo.CommandSpec, string, error) {
	if len(raw) == 0 || len(raw) > audit.MaxRecordBytes {
		return sessionrepo.OperationRequest{}, sessionrepo.CommandSpec{}, "", errors.New("Bash intent exceeds the audit record bound")
	}
	var intent struct {
		SchemaVersion string                       `json:"schema_version"`
		Request       sessionrepo.OperationRequest `json:"request"`
		Path          string                       `json:"path"`
		MaxBytes      int64                        `json:"max_bytes"`
		Command       sessionrepo.CommandSpec      `json:"command"`
		RunnerProfile string                       `json:"runner_profile"`
	}
	if err := unmarshalGovernedJSON(raw, &intent); err != nil {
		return sessionrepo.OperationRequest{}, sessionrepo.CommandSpec{}, "", err
	}
	if intent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion || intent.Request.Tool != "bash" ||
		intent.Path != "" || intent.MaxBytes != 0 || intent.RunnerProfile != piruntime.D09CommandRunnerProfile {
		return sessionrepo.OperationRequest{}, sessionrepo.CommandSpec{}, "", errors.New("primary intent is not a production D09 Bash v2 intent")
	}
	intent.Command.DeclaredOutputPaths = cloneProductionD09Paths(intent.Request.OutputScopePaths)
	if _, err := sessionrepo.CommandArgumentsDigest(intent.Command); err != nil {
		return sessionrepo.OperationRequest{}, sessionrepo.CommandSpec{}, "", err
	}
	return intent.Request, intent.Command, intent.RunnerProfile, nil
}

func validateProductionD09OperationBundle(bundle sessionrepo.EvidenceBundle, transition delta.Transition,
	operation sessionrepo.OperationEvidence, receipt piruntime.D09CommandReceipt) error {
	return validateProductionD09Operation(bundle, transition, operation, receipt, false)
}

// validateProductionD09Operation accepts exactly two shapes of a settled D09 command. Undenied: a
// success operation under an allow admission with its promoted transition in the approved ledger.
// Denied: a denied_after_effect operation under a deny admission whose retained record holds the given
// candidate, with no transition of the lease in the approved ledger.
func validateProductionD09Operation(bundle sessionrepo.EvidenceBundle, transition delta.Transition,
	operation sessionrepo.OperationEvidence, receipt piruntime.D09CommandReceipt, denied bool) error {
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil || disposition.Quarantined {
		return commandEvidenceRefused("bundle-reconstruct", err)
	}
	matchedOperations := 0
	for _, candidate := range disposition.Operations {
		if candidate.EffectID != operation.EffectID {
			continue
		}
		if !reflect.DeepEqual(candidate, operation) {
			return commandEvidenceRefused("bundle-operation-differs")
		}
		matchedOperations++
	}
	if matchedOperations != 1 {
		return commandEvidenceRefused("bundle-operation-count")
	}
	if operation.Tool != "bash" || operation.Operation.Kind != delta.OperationLease || operation.Operation.LeaseID != receipt.LeaseID ||
		operation.ViewID != receipt.ViewID || operation.Admission == nil || operation.ExitCode == nil ||
		*operation.ExitCode != receipt.ExitCode || operation.CommandContainmentStatus != "not-established" ||
		operation.CommandRunnerProfile != piruntime.D09CommandRunnerProfile || !productionD09OperationShape(operation, transition, denied) ||
		operation.EffectID == "" || operation.AuditSequence == 0 || !validDigestText(operation.ResultDigest) ||
		!validManagedTreeDigest(operation.InputTreeDigest) ||
		operation.InputGeneration != transition.InputGeneration || operation.InputTreeDigest != transition.InputTreeDigest ||
		operation.Operation != transition.Operation || operation.DecisionID != transition.Decision.ID ||
		operation.ArgumentDigest != transition.ArgumentDigest ||
		operation.ViewID != transition.ViewID || operation.ExecutionContextDigest != transition.ExecutionContextDigest ||
		operation.ExecutionContextDigest != governedExecutionContextDigest(operation.ViewID, operation.InputTreeDigest, receipt.LeaseID) ||
		transition.Tool != "bash" || transition.ID == "" || transition.Sequence == 0 || len(bundle.AuditJournal) == 0 {
		return commandEvidenceRefused("bundle-operation-fields")
	}
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil || uint64(len(trace.Records)) != bundle.AuditRecordCount || auditHeadHash(trace.Records) != bundle.AuditHeadHash {
		return commandEvidenceRefused("bundle-audit-journal", err)
	}
	var effect *audit.EffectTrace
	for index := range trace.Effects {
		candidate := &trace.Effects[index]
		if candidate.ID != operation.EffectID {
			continue
		}
		if effect != nil {
			return commandEvidenceRefused("bundle-effect-duplicate")
		}
		effect = candidate
	}
	if effect == nil || effect.Unresolved || effect.Outcome != "success" || effect.OutcomeSequence != operation.AuditSequence ||
		digestBytes(effect.Result) != operation.ResultDigest {
		return commandEvidenceRefused("bundle-effect-outcome")
	}
	request, intentSpec, intentProfile, err := decodeProductionD09CommandIntent(effect.Intent)
	if err != nil {
		return commandEvidenceRefused("bundle-intent", err)
	}
	var commandResult productionD09CommandEffectResult
	if err := unmarshalGovernedJSON(effect.Result, &commandResult); err != nil {
		return commandEvidenceRefused("bundle-effect-result", err)
	}
	effectSpec := commandResult.Spec
	effectSpec.DeclaredOutputPaths = cloneProductionD09Paths(intentSpec.DeclaredOutputPaths)
	commandBytes, commandErr := json.Marshal(commandResult.Command)
	settlementBytes, settlementErr := json.Marshal(commandResult.Settlement)
	specBytes, specErr := json.Marshal(effectSpec)
	argumentDigest, argumentErr := sessionrepo.CommandArgumentsDigest(intentSpec)
	wantAdmission := "allow"
	if denied {
		wantAdmission = "deny"
	}
	if commandErr != nil || settlementErr != nil || specErr != nil ||
		!sameDurableTransition(commandResult.Transition, transition) || commandResult.RunnerProfile != piruntime.D09CommandRunnerProfile ||
		intentProfile != commandResult.RunnerProfile || !reflect.DeepEqual(effectSpec, intentSpec) ||
		request.Tool != operation.Tool || request.Tool != transition.Tool || request.EffectID != operation.EffectID ||
		request.Operation != operation.Operation || request.Operation != transition.Operation ||
		request.Decision != transition.Decision || request.Decision.ID != operation.DecisionID ||
		request.ArgumentDigest != operation.ArgumentDigest || request.ArgumentDigest != transition.ArgumentDigest ||
		argumentErr != nil || argumentDigest != request.ArgumentDigest ||
		request.ViewID != operation.ViewID || request.ViewID != transition.ViewID ||
		request.ExecutionContextDigest != operation.ExecutionContextDigest || request.ExecutionContextDigest != transition.ExecutionContextDigest ||
		request.InputGeneration != operation.InputGeneration || request.InputGeneration != transition.InputGeneration ||
		request.InputTreeDigest != operation.InputTreeDigest || request.InputTreeDigest != transition.InputTreeDigest ||
		admissionAllows(bundle, request.OutputScopePaths, transition.Changes) == denied ||
		commandResult.Admission != *operation.Admission || commandResult.Admission.Outcome != wantAdmission ||
		commandResult.Admission.IntentDigest != digestBytes(effect.Intent) ||
		commandResult.Command.ExitCode != receipt.ExitCode || !commandResult.Command.ExitObserved ||
		digestBytes(commandBytes) != receipt.ResultDigest || digestBytes(settlementBytes) != receipt.SettlementDigest ||
		digestBytes(specBytes) != receipt.CommandSpecDigest || commandResult.Settlement != receipt.Settlement {
		return commandEvidenceRefused("bundle-effect-binding", errors.Join(commandErr, settlementErr, specErr))
	}
	matchedOperation := 0
	for _, candidate := range bundle.Operations {
		if candidate.EffectID == operation.EffectID {
			if !reflect.DeepEqual(candidate, operation) {
				return commandEvidenceRefused("bundle-ledger-operation-differs")
			}
			matchedOperation++
		}
	}
	if denied {
		if matchedOperation != 1 {
			return commandEvidenceRefused("bundle-ledger-count")
		}
		return verifyProductionD09DeniedRecord(bundle, effect.Intent, request, operation, transition)
	}
	matchedTransition := 0
	for _, candidate := range bundle.ApprovedDeltaLedger {
		if candidate.ID == transition.ID {
			if !reflect.DeepEqual(candidate, transition) {
				return commandEvidenceRefused("bundle-ledger-transition-differs")
			}
			matchedTransition++
		}
	}
	if matchedOperation != 1 || matchedTransition != 1 {
		return commandEvidenceRefused("bundle-ledger-count")
	}
	return nil
}

// productionD09OperationShape is the Store-side half of the two accepted shapes. A promoted command is a
// success operation whose output generation is the approved transition's. A refused one is a
// denied_after_effect operation under a deny admission with no output generation.
func productionD09OperationShape(operation sessionrepo.OperationEvidence, transition delta.Transition, denied bool) bool {
	if denied {
		return deniedAfterEffect(operation)
	}
	return operation.Outcome == "success" && validManagedTreeDigest(operation.OutputTreeDigest) &&
		operation.OutputGeneration == transition.OutputGeneration && operation.OutputTreeDigest == transition.OutputTreeDigest &&
		transition.OutputGeneration != transition.InputGeneration
}

// verifyProductionD09DeniedRecord checks that candidate is exactly what the Store's retained admission
// record refused for this operation, and that nothing of the lease reached the approved ledger.
func verifyProductionD09DeniedRecord(bundle sessionrepo.EvidenceBundle, intent []byte, request sessionrepo.OperationRequest,
	operation sessionrepo.OperationEvidence, candidate delta.Transition) error {
	for _, promoted := range bundle.ApprovedDeltaLedger {
		if promoted.Operation == operation.Operation {
			return commandEvidenceRefused("denied-promoted")
		}
	}
	admissions, err := audit.Verify(bytes.NewReader(bundle.AdmissionJournal))
	reference := *operation.Admission
	if err != nil || uint64(len(admissions.Records)) != bundle.AdmissionRecordCount ||
		auditHeadHash(admissions.Records) != bundle.AdmissionHeadHash ||
		reference.Sequence == 0 || reference.Sequence > uint64(len(admissions.Records)) {
		return commandEvidenceRefused("denied-admission-journal", err)
	}
	record := admissions.Records[reference.Sequence-1]
	var admitted governedReplayAdmissionRecord
	if record.Sequence != reference.Sequence || record.Hash != reference.RecordHash || record.Event.Kind != "sessionrepo_admission" ||
		unmarshalGovernedJSON(record.Event.Data, &admitted) != nil || admitted.SchemaVersion != sessionrepo.AdmissionSchemaVersion ||
		admitted.Outcome != "deny" || reference.Outcome != "deny" || admitted.IntentDigest != reference.IntentDigest ||
		reference.IntentDigest != digestBytes(intent) || !reflect.DeepEqual(admitted.Request, request) ||
		!sameDurableTransition(admitted.Candidate, candidate) {
		return commandEvidenceRefused("denied-admission-record")
	}
	return nil
}

func (r *productionD09BashRunner) VerifyProductionBashEvidence(ctx context.Context, operation sessionrepo.OperationEvidence) error {
	if r == nil || ctx == nil || operation.ViewID == "" {
		return commandEvidenceRefused("verify-precondition")
	}
	r.mu.Lock()
	retained, exists := r.evidence[operation.ViewID]
	r.mu.Unlock()
	if !exists {
		return commandEvidenceRefused("verify-receipt-missing")
	}
	return r.verifyProductionBashEvent(ctx, retained, operation, retained.transition, retained.providerCall, sessionrepo.EvidenceBundle{}, audit.Record{})
}

func (r *productionD09BashRunner) VerifyProductionBashEvidenceWithOrigin(ctx context.Context,
	bundle sessionrepo.EvidenceBundle, providerCall delta.OperationIdentity, transition delta.Transition,
	operation sessionrepo.OperationEvidence) error {
	if r == nil || ctx == nil || operation.ViewID == "" || len(bundle.AuditJournal) == 0 {
		return commandEvidenceRefused("verify-origin-precondition")
	}
	r.mu.Lock()
	retained, exists := r.evidence[operation.ViewID]
	r.mu.Unlock()
	if !exists {
		return commandEvidenceRefused("verify-origin-receipt-missing")
	}
	return r.verifyProductionBashEvent(ctx, retained, operation, transition, providerCall, bundle, audit.Record{})
}

func (r *productionD09BashRunner) verifyProductionBashEvent(ctx context.Context, retained productionD09RunEvidence,
	operation sessionrepo.OperationEvidence, transition delta.Transition, providerCall delta.OperationIdentity,
	bundle sessionrepo.EvidenceBundle, expected audit.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if retained.journalSeq == 0 || retained.eventID == "" || retained.receiptDigest == "" ||
		!reflect.DeepEqual(operation, retained.operation) || !reflect.DeepEqual(transition, retained.transition) ||
		!reflect.DeepEqual(providerCall, retained.providerCall) || operation.ViewID != retained.viewID ||
		operation.Operation.Kind != delta.OperationLease || operation.Operation.LeaseID != retained.leaseID ||
		retained.denied != deniedAfterEffect(operation) || operation.ExitCode == nil ||
		*operation.ExitCode != retained.receipt.ExitCode || operation.CommandContainmentStatus != "not-established" ||
		operation.CommandRunnerProfile != piruntime.D09CommandRunnerProfile || retained.receipt.LeaseID != retained.leaseID ||
		retained.receipt.ViewID != retained.viewID || retained.receipt.ReceiptDigest != retained.receiptDigest {
		return commandEvidenceRefused("event-retained-binding")
	}
	if err := retained.receipt.Validate(); err != nil {
		return commandEvidenceRefused("event-receipt-seal", err)
	}
	encodedReceipt, err := json.Marshal(retained.receipt)
	if err != nil || !bytes.Equal(encodedReceipt, retained.receiptSnapshot) {
		return commandEvidenceRefused("event-receipt-snapshot-drift", err)
	}
	var snapshot piruntime.D09CommandReceipt
	if err := unmarshalGovernedJSON(retained.receiptSnapshot, &snapshot); err != nil {
		return commandEvidenceRefused("event-snapshot-decode", err)
	}
	if err := snapshot.ValidateSnapshot(); err != nil || snapshot.ReceiptDigest != retained.receiptDigest {
		return commandEvidenceRefused("event-snapshot-validate", err)
	}
	if bundle.AuditJournal != nil {
		if err := validateProductionD09Operation(bundle, transition, operation, retained.receipt, retained.denied); err != nil {
			return err
		}
	}
	trace, err := r.journal.Trace()
	if err != nil {
		return commandEvidenceRefused("event-journal-trace", err)
	}
	var matched *audit.Record
	for index := range trace.Records {
		candidate := &trace.Records[index]
		if candidate.Event.Kind != productionD09SettledKind || candidate.Event.ID != retained.eventID {
			continue
		}
		if matched != nil {
			return commandEvidenceRefused("event-journal-duplicate")
		}
		matched = candidate
	}
	if err := validateProductionD09JournalRecord(matched, retained, expected); err != nil {
		return err
	}
	if retained.denied {
		var event productionD09DeniedCommandSettledEvent
		if err := unmarshalGovernedJSON(matched.Event.Data, &event); err != nil {
			return commandEvidenceRefused("event-decode", err)
		}
		return compareProductionD09DeniedCommandEvent(r, event, retained, providerCall, operation, transition)
	}
	var event productionD09CommandSettledEvent
	if err := unmarshalGovernedJSON(matched.Event.Data, &event); err != nil {
		return commandEvidenceRefused("event-decode", err)
	}
	return compareProductionD09CommandEvent(r, event, retained, providerCall, operation, transition)
}

func validateProductionD09JournalRecord(record *audit.Record, retained productionD09RunEvidence, expected audit.Record) error {
	if record == nil || record.Event.Kind != productionD09SettledKind || record.Event.ID != retained.eventID ||
		record.Sequence == 0 || record.Sequence != retained.journalSeq || record.Hash == "" || record.Hash != retained.journalHash ||
		expected.Sequence != 0 && (record.Sequence != expected.Sequence || record.Hash != expected.Hash) {
		return commandEvidenceRefused("event-journal-record")
	}
	return nil
}

// compareProductionD09CommandEvent compares a decoded journal payload to
// already-live validated receipt/operation identities. It is deliberately not
// an authority on its own; production calls it only after receipt.Validate and
// the Store-bundle audit checks succeed.
func compareProductionD09CommandEvent(r *productionD09BashRunner, event productionD09CommandSettledEvent,
	retained productionD09RunEvidence, providerCall delta.OperationIdentity,
	operation sessionrepo.OperationEvidence, transition delta.Transition) error {
	if r == nil || event.SchemaVersion != productionD09SettledSchemaSuccess ||
		!r.ownsEvent(event.RunID, event.SessionID, event.WorkflowID, event.ConversationID, event.ProfileID, event.ProfileDigest) ||
		!reflect.DeepEqual(event.ProviderCall, providerCall) || !reflect.DeepEqual(event.Operation, operation) ||
		!reflect.DeepEqual(event.Transition, transition) || event.ReceiptDigest != retained.receiptDigest ||
		!bytes.Equal(event.Receipt, retained.receiptSnapshot) {
		return commandEvidenceRefused("event-payload")
	}
	return nil
}

// compareProductionD09DeniedCommandEvent is compareProductionD09CommandEvent for the denied shape: the event
// carries the refused candidate, the Store's denied_after_effect operation and no approved transition.
func compareProductionD09DeniedCommandEvent(r *productionD09BashRunner, event productionD09DeniedCommandSettledEvent,
	retained productionD09RunEvidence, providerCall delta.OperationIdentity,
	operation sessionrepo.OperationEvidence, candidate delta.Transition) error {
	if r == nil || event.SchemaVersion != productionD09SettledSchemaDenied || event.Outcome != productionD09OutcomeDenied ||
		!r.ownsEvent(event.RunID, event.SessionID, event.WorkflowID, event.ConversationID, event.ProfileID, event.ProfileDigest) ||
		!reflect.DeepEqual(event.ProviderCall, providerCall) || !reflect.DeepEqual(event.Operation, operation) ||
		!reflect.DeepEqual(event.DeniedCandidate, candidate) || event.DeniedCandidateDigest != productionD09DeniedCandidateDigest(candidate) ||
		event.ReceiptDigest != retained.receiptDigest || !bytes.Equal(event.Receipt, retained.receiptSnapshot) {
		return commandEvidenceRefused("event-payload")
	}
	return nil
}

func (r *productionD09BashRunner) ownsEvent(runID, sessionID, workflowID, conversationID, profileID, profileDigest string) bool {
	return runID == r.runID && sessionID == r.sessionID && workflowID == r.workflowID && conversationID == r.conversationID &&
		profileID == r.profile.ID && profileDigest == r.profile.Digest
}

// admissionAllows reports whether the admission scope the bundle was written under allows the
// promoted changes of an operation that declared requestScope. For the default scope this is the
// exact-path output-scope coverage check; under promote-all/v1 a promoted change may lie outside it.
func admissionAllows(bundle sessionrepo.EvidenceBundle, requestScope []string, changes []delta.ObjectChange) bool {
	outcome, _ := bundle.AdmissionScope.Decide(requestScope, changes)
	return outcome == "allow"
}
