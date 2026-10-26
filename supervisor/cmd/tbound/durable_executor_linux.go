//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

// durableReadMaxBytes is the file-byte ceiling the durable read path requests
// from the session repository. It equals the protocol frame bound so a read
// whose encoded strict-JSON result would not fit on the wire is rejected before
// any bytes are released rather than truncated.
const durableReadMaxBytes = protocol.MaxFrameBytes

// DurableExecutorConfig binds the durable effect path to one seeded session
// repository. Tip is the current sealed tip generation; Execute advances it
// after every committed mutation. CallIssuer is the configured broker call
// issuer the composition root trusts for this session.
//
// PolicyDigest and MetadataPolicyDigest are the session repository's own bare
// "sha256:<64hex>" commitments. They are deliberately separate from
// gate.Decision.PolicyDigest, which is a versioned "tbound-policy/v1:sha256:..."
// policy-profile digest. The two must never be equated or substituted.
type DurableExecutorConfig struct {
	Store                *sessionrepo.Store
	Tip                  *sessionrepo.Generation
	CallIssuer           string
	PolicyDigest         string
	MetadataPolicyDigest string
	// BashOutputPaths is the trusted exact-path output scope. Nil disables Bash;
	// an explicit empty slice permits no changed paths.
	BashOutputPaths []string
	// CommandRunner is the trusted command runner used for the bash lease. It
	// is optional: a nil runner is valid at construction and makes every bash
	// ALLOW fail closed at Execute time rather than running uncontained.
	CommandRunner sessionrepo.CommandRunner
	// AfterDurableOperation settles a consumed workflow effect reservation from
	// the actual Store/admission journal tail before a provider result is released.
	AfterDurableOperation func(context.Context, protocol.Proposal, gate.Decision) error
}

// DurableExecutor is the production-shaped Executor for cmd/tbound. It maps one
// trusted ALLOW proposal onto one sessionrepo read or mutation on the current
// tip and releases a strict-JSON summary only after the operation is durable.
// A read returns bounded bytes from the current sealed tip and never advances
// it; write/edit/bash each advance the tip by one committed transition. It owns
// no containment, publication, or provider claim: those remain unresolved in
// the session repository evidence.
type DurableExecutor struct {
	mu                    sync.Mutex
	store                 *sessionrepo.Store
	tip                   *sessionrepo.Generation
	callIssuer            string
	policyDigest          string
	metadataPolicyDigest  string
	commandRunner         sessionrepo.CommandRunner
	bashOutputPaths       []string
	afterDurableOperation func(context.Context, protocol.Proposal, gate.Decision) error
	quarantined           bool
}

// productionBashEvidenceBinder is implemented only by the CLI's concrete D09
// runner chain. DurableExecutor calls it after sessionrepo has committed the
// command outcome, but before the summary can be released to the provider.
type productionBashEvidenceBinder interface {
	BindProductionBashEvidence(context.Context, delta.OperationIdentity, sessionrepo.EvidenceBundle,
		delta.Transition, sessionrepo.OperationEvidence) error
}

// productionDeniedBashEvidenceBinder is the binder of a command that ran and whose change delta admission
// then refused. It takes the refused candidate where productionBashEvidenceBinder takes the approved transition.
type productionDeniedBashEvidenceBinder interface {
	BindProductionDeniedBashEvidence(context.Context, delta.OperationIdentity, sessionrepo.EvidenceBundle,
		delta.Transition, sessionrepo.OperationEvidence) error
}

// NewDurableExecutor validates and constructs a DurableExecutor. The caller
// retains ownership of the store, the journal, and the tip generation.
func cloneDurableOutputScopePaths(paths []string) []string {
	if paths == nil {
		return nil
	}
	cloned := make([]string, len(paths))
	copy(cloned, paths)
	return cloned
}

func sameDurableOutputScopePaths(left, right []string) bool {
	if (left == nil) != (right == nil) || len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func NewDurableExecutor(config DurableExecutorConfig) (*DurableExecutor, error) {
	if config.Store == nil || config.Tip == nil {
		return nil, errors.New("durable executor requires a store and a current tip generation")
	}
	if !validIdentityText(config.CallIssuer) {
		return nil, errors.New("durable executor requires a configured broker call issuer")
	}
	if !validBareSHA256(config.PolicyDigest) || !validBareSHA256(config.MetadataPolicyDigest) {
		return nil, errors.New("durable executor policy commitments must be bare sha256:<lowercase hex>")
	}
	if config.BashOutputPaths != nil {
		if err := sessionrepo.ValidateOutputScopePaths(config.BashOutputPaths); err != nil {
			return nil, fmt.Errorf("durable executor Bash output scope: %w", err)
		}
	}
	return &DurableExecutor{
		store: config.Store, tip: config.Tip, callIssuer: config.CallIssuer,
		policyDigest: config.PolicyDigest, metadataPolicyDigest: config.MetadataPolicyDigest,
		commandRunner: config.CommandRunner, bashOutputPaths: cloneDurableOutputScopePaths(config.BashOutputPaths),
		afterDurableOperation: config.AfterDurableOperation,
	}, nil
}

// Tip returns the current sealed tip generation. It is provided for the
// composition root and tests; callers must not mutate the returned generation.
func (e *DurableExecutor) Tip() *sessionrepo.Generation {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tip
}

// Execute maps one trusted ALLOW proposal to a sessionrepo Read, Write, Edit,
// or Bash command lease on the current tip. It never fabricates authority: the
// argument digest, effect ID, generation, transition, and settlement evidence
// all come from the store's durable path.
func (e *DurableExecutor) Execute(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) (json.RawMessage, error) {
	if e == nil || e.store == nil {
		return nil, errors.New("durable executor is not configured")
	}
	if decision.Verdict != gate.Allow {
		return nil, errors.New("durable executor refuses a non-allow decision")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operation, err := durableOperationIdentity(proposal, decision, e.callIssuer)
	if err != nil {
		return nil, err
	}
	policyDecision, err := durablePolicyDecision(proposal, decision, e.policyDigest, e.metadataPolicyDigest)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.quarantined {
		return nil, errors.New("durable executor is quarantined after incomplete production command evidence")
	}
	if e.tip == nil {
		return nil, errors.New("durable executor has no current tip generation")
	}

	var mutation sessionrepo.MutationResult
	switch proposal.Tool {
	case "read":
		// A read is not a mutation: it resolves the path against the current
		// sealed tip, journals the correlated intent before releasing bytes,
		// and returns a bounded summary without advancing the tip.
		path, err := durableReadArguments(proposal.Arguments)
		if err != nil {
			return nil, err
		}
		content, err := e.tip.Read(ctx, operation, policyDecision, path, durableReadMaxBytes)
		if err != nil {
			return nil, fmt.Errorf("durable read: %w", err)
		}
		if e.afterDurableOperation != nil {
			if err := e.afterDurableOperation(ctx, proposal, decision); err != nil {
				return nil, fmt.Errorf("settle durable read against workflow owner before result release: %w", err)
			}
		}
		return durableReadSummary(path, e.tip, content)
	case "write":
		path, content, err := durableWriteArguments(proposal.Arguments)
		if err != nil {
			return nil, err
		}
		mutation, err = e.store.Write(ctx, e.tip, operation, policyDecision, path, content)
		if err != nil {
			return nil, e.admissionDenied(ctx, proposal, decision, mutation, fmt.Errorf("durable write: %w", err))
		}
	case "edit":
		path, edits, err := durableEditArguments(proposal.Arguments)
		if err != nil {
			return nil, err
		}
		mutation, err = e.store.Edit(ctx, e.tip, operation, policyDecision, path, edits)
		if err != nil {
			return nil, e.admissionDenied(ctx, proposal, decision, mutation, fmt.Errorf("durable edit: %w", err))
		}
	case "bash":
		if e.bashOutputPaths == nil {
			return nil, sessionrepo.ErrOutputScopeRequired
		}
		// Fail closed before touching the store when no trusted runner is
		// configured: an unrunnable ALLOW must withhold the result, not run
		// an uncontained command or fabricate a settlement.
		if e.commandRunner == nil {
			return nil, errors.New("durable bash has no configured command runner; result withheld")
		}
		command, err := durableBashArguments(proposal.Arguments)
		if err != nil {
			return nil, err
		}
		// The adapter advertises a `bash` tool and sends the untrusted shell
		// command as a single string. The trusted mapping pins one exact,
		// deterministic argv: /bin/bash with -l (login shell, matching the
		// adapter's bash semantics) and -c (run the single command string).
		// Nothing from the proposal is interpolated into argv except the
		// strictly decoded command, so the authorized spec is byte-stable.
		spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", command},
			DeclaredOutputPaths: cloneDurableOutputScopePaths(e.bashOutputPaths)}
		mutation, err = e.store.RunBash(ctx, e.tip, operation, policyDecision, spec, e.commandRunner)
		if err != nil {
			return nil, e.admissionDenied(ctx, proposal, decision, mutation, fmt.Errorf("durable bash: %w", err))
		}
	default:
		return nil, fmt.Errorf("durable executor does not implement tool %q", proposal.Tool)
	}
	if mutation.Generation == nil || mutation.Transition.ID == "" {
		return nil, errors.New("durable mutation returned no committed generation or transition")
	}
	e.tip = mutation.Generation
	if proposal.Tool == "bash" && durableCommandRunnerProfile(e.commandRunner) == piruntime.D09CommandRunnerProfile {
		if err := e.bindProductionBashEvidence(ctx, proposal, decision, mutation); err != nil {
			e.quarantined = true
			return nil, fmt.Errorf("bind and reverify D09 proof with durable Bash operation before result release: %w", err)
		}
	}
	if e.afterDurableOperation != nil {
		if err := e.afterDurableOperation(ctx, proposal, decision); err != nil {
			return nil, fmt.Errorf("settle durable Store operation against workflow owner before result release: %w", err)
		}
	}
	return durableMutationSummary(proposal.Tool, mutation, e.commandRunner)
}

func (e *DurableExecutor) bindProductionBashEvidence(ctx context.Context, proposal protocol.Proposal, decision gate.Decision, mutation sessionrepo.MutationResult) error {
	binder, ok := e.commandRunner.(productionBashEvidenceBinder)
	if !ok || ctx == nil || mutation.Generation == nil || mutation.EffectID == "" || mutation.Transition.ID == "" ||
		proposal.Tool != "bash" || proposal.ToolCallID == "" {
		return errors.New("registered D09 command runner has no durable evidence-binding API")
	}
	bundle, operation, providerCall, err := e.committedBashEvidence(ctx, proposal, decision, mutation)
	if err != nil {
		return err
	}
	var transition *delta.Transition
	for index := range bundle.ApprovedDeltaLedger {
		candidate := &bundle.ApprovedDeltaLedger[index]
		if candidate.ID != mutation.Transition.ID {
			continue
		}
		if transition != nil {
			return errors.New("committed store ledger repeats the Bash transition ID")
		}
		transition = candidate
	}
	if transition == nil {
		return errors.New("committed operation evidence and approved Bash delta do not match the actual Store mutation")
	}
	if operation.OutputGeneration != mutation.Transition.OutputGeneration || operation.OutputTreeDigest != mutation.Transition.OutputTreeDigest ||
		operation.Operation != mutation.Transition.Operation {
		return errors.New("committed Bash OperationEvidence differs from the Store MutationResult transition")
	}
	if !sameDurableTransition(*transition, mutation.Transition) {
		return errors.New("committed approved Bash ledger transition differs from the Store MutationResult transition")
	}
	if mutation.Generation.ID() != transition.OutputGeneration || mutation.Generation.TreeDigest() != transition.OutputTreeDigest {
		return errors.New("committed Bash generation differs from the approved transition output")
	}
	return binder.BindProductionBashEvidence(ctx, providerCall, bundle, *transition, operation)
}

// bindProductionDeniedBashEvidence settles the D09 lease of a Bash command that ran and whose settled
// change delta admission refused (ErrDeltaDenied). The Store holds a denied_after_effect operation and no
// transition, so the receipt binds to that operation and the refused candidate.
func (e *DurableExecutor) bindProductionDeniedBashEvidence(ctx context.Context, proposal protocol.Proposal, decision gate.Decision, mutation sessionrepo.MutationResult) error {
	binder, ok := e.commandRunner.(productionDeniedBashEvidenceBinder)
	if !ok || ctx == nil || mutation.Generation != nil || mutation.EffectID == "" || mutation.Transition.ID == "" ||
		proposal.Tool != "bash" || proposal.ToolCallID == "" {
		return errors.New("registered D09 command runner has no durable evidence-binding API")
	}
	bundle, operation, providerCall, err := e.committedBashEvidence(ctx, proposal, decision, mutation)
	if err != nil {
		return err
	}
	if !deniedAfterEffect(operation) || operation.Operation != mutation.Transition.Operation {
		return errors.New("committed Bash OperationEvidence is not the denied operation of the Store MutationResult")
	}
	return binder.BindProductionDeniedBashEvidence(ctx, providerCall, bundle, mutation.Transition, operation)
}

// committedBashEvidence reads the Store evidence of the Bash effect that mutation names and derives the
// provider-call identity of its lease. It checks what promoted and refused commands share: the operation
// is the lease of this provider response/call and its input matches the Store's transition.
func (e *DurableExecutor) committedBashEvidence(ctx context.Context, proposal protocol.Proposal, decision gate.Decision,
	mutation sessionrepo.MutationResult) (sessionrepo.EvidenceBundle, sessionrepo.OperationEvidence, delta.OperationIdentity, error) {
	if err := ctx.Err(); err != nil {
		return sessionrepo.EvidenceBundle{}, sessionrepo.OperationEvidence{}, delta.OperationIdentity{}, err
	}
	bundle, err := e.store.EvidenceBundle()
	if err != nil {
		return sessionrepo.EvidenceBundle{}, sessionrepo.OperationEvidence{}, delta.OperationIdentity{}, fmt.Errorf("read committed Bash operation evidence: %w", err)
	}
	var operation *sessionrepo.OperationEvidence
	for index := range bundle.Operations {
		candidate := &bundle.Operations[index]
		if candidate.EffectID != mutation.EffectID {
			continue
		}
		if operation != nil {
			return sessionrepo.EvidenceBundle{}, sessionrepo.OperationEvidence{}, delta.OperationIdentity{}, errors.New("committed store evidence repeats the Bash effect ID")
		}
		operation = candidate
	}
	fail := func(err error) (sessionrepo.EvidenceBundle, sessionrepo.OperationEvidence, delta.OperationIdentity, error) {
		return sessionrepo.EvidenceBundle{}, sessionrepo.OperationEvidence{}, delta.OperationIdentity{}, err
	}
	if operation == nil {
		return fail(errors.New("committed operation evidence and approved Bash delta do not match the actual Store mutation"))
	}
	if operation.Operation.Kind != delta.OperationLease || operation.Operation.LeaseID == "" ||
		operation.Tool != "bash" || operation.EffectID != mutation.EffectID ||
		operation.InputGeneration != mutation.Transition.InputGeneration || operation.InputTreeDigest != mutation.Transition.InputTreeDigest {
		return fail(errors.New("committed Bash OperationEvidence differs from the Store MutationResult transition"))
	}
	expectedLeaseID, err := durableTrustedIdentity("lease", proposal, decision)
	if err != nil || operation.Operation.LeaseID != expectedLeaseID {
		return fail(errors.Join(errors.New("committed Bash lease does not derive from the current provider response/call tuple"), err))
	}
	proposalID, err := durableTrustedIdentity("proposal", proposal, decision)
	if err != nil {
		return fail(err)
	}
	providerCall := delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: proposalID,
		CallIssuer: e.callIssuer, CallID: proposal.ToolCallID}
	if providerCall.CallIssuer != e.callIssuer || !validIdentityText(providerCall.CallIssuer) || !validIdentityText(providerCall.CallID) ||
		providerCall.CallID != proposal.ToolCallID {
		return fail(errors.New("durable Bash provider-call identity is invalid"))
	}
	return bundle, *operation, providerCall, nil
}

// sameDurableTransition is field-exact but treats nil and empty Changes alike: a no-change
// transition is `"changes":[]` in its effect result yet nil after the Store's evidence clone.
func sameDurableTransition(left, right delta.Transition) bool {
	if left.ID != right.ID || left.Sequence != right.Sequence || left.Tool != right.Tool ||
		left.ArgumentDigest != right.ArgumentDigest || left.ViewID != right.ViewID ||
		left.ExecutionContextDigest != right.ExecutionContextDigest || left.InputGeneration != right.InputGeneration ||
		left.OutputGeneration != right.OutputGeneration || left.InputTreeDigest != right.InputTreeDigest ||
		left.OutputTreeDigest != right.OutputTreeDigest || left.Operation != right.Operation ||
		!reflect.DeepEqual(left.Decision, right.Decision) || len(left.Changes) != len(right.Changes) {
		return false
	}
	for index := range left.Changes {
		if !reflect.DeepEqual(left.Changes[index], right.Changes[index]) {
			return false
		}
	}
	return true
}

// durableMutationSummary encodes a strict-JSON view of a committed mutation. It
// deliberately contains no containment, publication, or provider claim. For a
// Bash lease it also reports the observed command exit code and the runner's
// descriptive profile; the containment status is a fixed, honest
// "not-established" because the session repository never proves containment.
type durableMutationSummaryPayload struct {
	Tool       string                   `json:"tool"`
	Generation durableGenerationSummary `json:"generation"`
	Transition durableTransitionSummary `json:"transition"`
	EffectID   string                   `json:"effect_id"`
	Outcome    string                   `json:"outcome"`
	Command    *durableCommandSummary   `json:"command,omitempty"`
}

type durableGenerationSummary struct {
	ID         string `json:"id"`
	TreeDigest string `json:"tree_digest"`
}

type durableTransitionSummary struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
}

// durableCommandSummary reports the settled command result. CommandRunnerProfile
// mirrors the profile the store records in its operation evidence; the
// containment status is always "not-established".
type durableCommandSummary struct {
	ExitCode                 int    `json:"exit_code"`
	ExitObserved             bool   `json:"exit_observed"`
	CommandContainmentStatus string `json:"command_containment_status"`
	CommandRunnerProfile     string `json:"command_runner_profile"`
}

func durableMutationSummary(tool string, mutation sessionrepo.MutationResult, runner sessionrepo.CommandRunner) (json.RawMessage, error) {
	payload := durableMutationSummaryPayload{
		Tool: tool,
		Generation: durableGenerationSummary{
			ID: mutation.Generation.ID(), TreeDigest: mutation.Generation.TreeDigest(),
		},
		Transition: durableTransitionSummary{ID: mutation.Transition.ID, Sequence: mutation.Transition.Sequence},
		EffectID:   mutation.EffectID,
		Outcome:    mutation.Outcome,
	}
	if tool == "bash" {
		if mutation.Command == nil {
			return nil, errors.New("durable bash mutation returned no command result")
		}
		payload.Command = &durableCommandSummary{
			ExitCode:                 mutation.Command.ExitCode,
			ExitObserved:             mutation.Command.ExitObserved,
			CommandContainmentStatus: "not-established",
			CommandRunnerProfile:     durableCommandRunnerProfile(runner),
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode durable mutation summary: %w", err)
	}
	if len(encoded) > protocol.MaxFrameBytes {
		return nil, errors.New("durable mutation summary exceeds the protocol bound; result withheld")
	}
	return json.RawMessage(encoded), nil
}

// durableCommandRunnerProfile mirrors the store's runner-profile labelling: a
// runner that does not expose a valid CommandRunnerProfile is "unclassified".
func durableCommandRunnerProfile(runner sessionrepo.CommandRunner) string {
	profiled, ok := runner.(sessionrepo.CommandRunnerProfile)
	if !ok {
		return "unclassified"
	}
	profile := profiled.Profile()
	if !validIdentityText(profile) {
		return "unclassified"
	}
	for _, r := range profile {
		if unicode.IsControl(r) {
			return "unclassified"
		}
	}
	return profile
}

// durableOperationIdentity derives the stable operation identity for one
// proposal call from the trusted decision tuple plus the Pi call ID. A bash
// proposal becomes a command lease whose stable LeaseID is derived from the
// same trusted tuple under the distinct "lease" domain.
func durableOperationIdentity(proposal protocol.Proposal, decision gate.Decision, callIssuer string) (delta.OperationIdentity, error) {
	if proposal.Tool == "bash" {
		leaseID, err := durableTrustedIdentity("lease", proposal, decision)
		if err != nil {
			return delta.OperationIdentity{}, err
		}
		return delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: leaseID}, nil
	}
	proposalID, err := durableTrustedIdentity("proposal", proposal, decision)
	if err != nil {
		return delta.OperationIdentity{}, err
	}
	if !validIdentityText(proposal.ToolCallID) {
		return delta.OperationIdentity{}, errors.New("proposal has no valid tool call ID")
	}
	return delta.OperationIdentity{
		Kind: delta.OperationProposalCall, ProposalID: proposalID,
		CallIssuer: callIssuer, CallID: proposal.ToolCallID,
	}, nil
}

// durablePolicyDecision derives the stable, unique decision ID from the trusted
// decision and pins the configured bare session-repository commitments.
func durablePolicyDecision(proposal protocol.Proposal, decision gate.Decision, policyDigest, metadataPolicyDigest string) (delta.PolicyDecision, error) {
	decisionID, err := durableTrustedIdentity("decision", proposal, decision)
	if err != nil {
		return delta.PolicyDecision{}, err
	}
	return delta.PolicyDecision{
		ID: decisionID, Outcome: delta.PolicyAllow,
		PolicyDigest: policyDigest, MetadataPolicyDigest: metadataPolicyDigest,
	}, nil
}

// durableTrustedIdentity hashes a length-prefixed, domain-separated tuple of the
// trusted decision fields. It is deterministic, so an operation identity and its
// policy decision ID are stable across a restart and unique per broker call.
func durableTrustedIdentity(domain string, proposal protocol.Proposal, decision gate.Decision) (string, error) {
	if decision.ResponseID == nil || decision.ResponseID.Issuer == "" || decision.ResponseID.Opaque == "" {
		return "", errors.New("allow decision has no trusted response identity")
	}
	if !validIdentityText(decision.ToolCallID) || decision.Sequence == 0 {
		return "", errors.New("allow decision has no trusted call binding")
	}
	if proposal.ToolCallID != decision.ToolCallID {
		return "", errors.New("proposal call ID does not match the trusted decision")
	}
	if decision.Tool == "" || decision.Tool != proposal.Tool {
		return "", errors.New("proposal tool does not match the trusted decision")
	}
	digest := sha256.New()
	writeHashField(digest, domain)
	writeHashField(digest, decision.ResponseID.Issuer)
	writeHashField(digest, decision.ResponseID.Opaque)
	writeHashField(digest, strconv.FormatUint(decision.Sequence, 10))
	writeHashField(digest, decision.ToolCallID)
	return domain + "-" + hex.EncodeToString(digest.Sum(nil)), nil
}

func writeHashField(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = io.WriteString(digest, value)
}

type durableWriteArgumentsPayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func durableWriteArguments(raw json.RawMessage) (string, []byte, error) {
	var arguments durableWriteArgumentsPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", nil, fmt.Errorf("write arguments: %w", err)
	}
	if err := durableNoTrailingJSON(decoder); err != nil {
		return "", nil, fmt.Errorf("write arguments: %w", err)
	}
	if arguments.Path == "" {
		return "", nil, errors.New("write arguments require a path")
	}
	return arguments.Path, []byte(arguments.Content), nil
}

type durableEditArgumentsPayload struct {
	Path  string                          `json:"path"`
	Edits []durableEditReplacementPayload `json:"edits"`
}

type durableEditReplacementPayload struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

func durableEditArguments(raw json.RawMessage) (string, []sessionrepo.Replacement, error) {
	var arguments durableEditArgumentsPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", nil, fmt.Errorf("edit arguments: %w", err)
	}
	if err := durableNoTrailingJSON(decoder); err != nil {
		return "", nil, fmt.Errorf("edit arguments: %w", err)
	}
	if arguments.Path == "" || len(arguments.Edits) == 0 {
		return "", nil, errors.New("edit arguments require a path and at least one replacement")
	}
	edits := make([]sessionrepo.Replacement, len(arguments.Edits))
	for index, edit := range arguments.Edits {
		edits[index] = sessionrepo.Replacement{OldText: edit.OldText, NewText: edit.NewText}
	}
	return arguments.Path, edits, nil
}

// durableBashArgumentsPayload mirrors the adapter's bash tool schema exactly:
// a required command string and an optional numeric timeout. The timeout is
// parsed for schema conformance but is not placed in the argv; the trusted
// runner/containment layer owns termination, matching the adapter's
// "no default timeout" contract.
type durableBashArgumentsPayload struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout,omitempty"`
}

// durableBashArguments strictly decodes the adapter bash arguments. Unknown
// fields and trailing JSON are rejected so the trusted supervisor never runs a
// command that differs from what the adapter signed.
func durableBashArguments(raw json.RawMessage) (string, error) {
	var arguments durableBashArgumentsPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", fmt.Errorf("bash arguments: %w", err)
	}
	if err := durableNoTrailingJSON(decoder); err != nil {
		return "", fmt.Errorf("bash arguments: %w", err)
	}
	if arguments.Command == "" {
		return "", errors.New("bash arguments require a command")
	}
	return arguments.Command, nil
}

// durableReadSummaryPayload is the bounded strict-JSON view of a completed
// read. It carries the resolved workspace-relative path, the sealed generation
// the bytes came from, the byte size and content digest, and the content
// itself. It never carries an effect ID because a read creates no approved
// delta; the store still records its own durable read operation evidence.
type durableReadSummaryPayload struct {
	Tool          string                   `json:"tool"`
	Path          string                   `json:"path"`
	Generation    durableGenerationSummary `json:"generation"`
	Size          int                      `json:"size"`
	ContentDigest string                   `json:"content_digest"`
	Content       string                   `json:"content"`
	Outcome       string                   `json:"outcome"`
}

// durableReadSummary encodes a completed read and fails closed when the encoded
// strict-JSON result would exceed the protocol's frame bound.
func durableReadSummary(path string, generation *sessionrepo.Generation, content []byte) (json.RawMessage, error) {
	if generation == nil {
		return nil, errors.New("durable read returned no generation")
	}
	payload := durableReadSummaryPayload{
		Tool: "read", Path: path,
		Generation:    durableGenerationSummary{ID: generation.ID(), TreeDigest: generation.TreeDigest()},
		Size:          len(content),
		ContentDigest: durableContentDigest(content),
		Content:       string(content),
		Outcome:       "success",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode durable read summary: %w", err)
	}
	if len(encoded) > protocol.MaxFrameBytes {
		return nil, errors.New("durable read result exceeds the protocol bound; result withheld")
	}
	return json.RawMessage(encoded), nil
}

// durableReadArgumentsPayload mirrors the adapter's read tool schema: a
// required path and optional numeric offset/limit. The offset/limit fields are
// parsed so the trusted decoder rejects a malformed value, but the durable read
// path reads the whole bounded file and fails closed on a paginated request
// rather than inventing line semantics the session repository does not own.
type durableReadArgumentsPayload struct {
	Path   string   `json:"path"`
	Offset *float64 `json:"offset,omitempty"`
	Limit  *float64 `json:"limit,omitempty"`
}

// durableReadArguments strictly decodes the adapter read arguments. Unknown
// fields and trailing JSON are rejected so the trusted supervisor never reads a
// path that differs from what the adapter signed.
func durableReadArguments(raw json.RawMessage) (string, error) {
	var arguments durableReadArgumentsPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", fmt.Errorf("read arguments: %w", err)
	}
	if err := durableNoTrailingJSON(decoder); err != nil {
		return "", fmt.Errorf("read arguments: %w", err)
	}
	if arguments.Path == "" {
		return "", errors.New("read arguments require a path")
	}
	if arguments.Offset != nil || arguments.Limit != nil {
		return "", errors.New("read offset/limit are not supported by the durable read path")
	}
	return arguments.Path, nil
}

// durableContentDigest is the bare sha256 commitment the session repository
// uses for released result bytes.
func durableContentDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func durableNoTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("tool arguments contain trailing JSON")
	}
	return nil
}

func validIdentityText(value string) bool {
	if value == "" || len(value) > delta.MaxIdentityBytes || !utf8.ValidString(value) {
		return false
	}
	return strings.TrimSpace(value) == value
}

func validBareSHA256(value string) bool {
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

// admissionDenied turns the Store's refusal of a settled change (delta admission, ErrDeltaDenied) into a
// protocol.AdmissionDeniedError once the workflow owner has settled the denied effect: the candidate was
// discarded and nothing was promoted, so the denial is a result to release, not an executor failure. Any
// other error is returned unchanged, and a denial the owner cannot settle stays an error.
func (e *DurableExecutor) admissionDenied(ctx context.Context, proposal protocol.Proposal, decision gate.Decision,
	mutation sessionrepo.MutationResult, err error) error {
	if !errors.Is(err, sessionrepo.ErrDeltaDenied) {
		return err
	}
	// A refused Bash command still ran on its D09 cell: settle its lease before the owner records the
	// denial, so the run keeps a record that the command executed and settled.
	if proposal.Tool == "bash" && durableCommandRunnerProfile(e.commandRunner) == piruntime.D09CommandRunnerProfile {
		if bindErr := e.bindProductionDeniedBashEvidence(ctx, proposal, decision, mutation); bindErr != nil {
			e.quarantined = true
			return fmt.Errorf("bind and reverify D09 proof with denied durable Bash operation before result release: %w", bindErr)
		}
	}
	if e.afterDurableOperation != nil {
		if settleErr := e.afterDurableOperation(ctx, proposal, decision); settleErr != nil {
			return fmt.Errorf("settle denied Store operation against workflow owner before result release: %w", settleErr)
		}
	}
	return &protocol.AdmissionDeniedError{Tool: proposal.Tool, ReasonCode: mutation.DenyReason, EffectID: mutation.EffectID, Err: err}
}
