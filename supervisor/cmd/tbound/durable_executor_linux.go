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
	"math"
	"path"
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
	"tbound/supervisor/internal/runend"
	"tbound/supervisor/internal/sessionrepo"
)

// durableReadMaxBytes is the file-byte ceiling the durable read path requests
// from the session repository. It equals the protocol frame bound so a read
// whose encoded strict-JSON result would not fit on the wire is rejected before
// any bytes are released rather than truncated.
const durableReadMaxBytes = protocol.MaxFrameBytes

// durableCommandOutputMaxBytes is the largest per-stream bound a configured executor accepts. Two streams
// of it, at JSON's worst case of six bytes per control character, still fit one protocol frame.
const durableCommandOutputMaxBytes = 64 << 10

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
	// CommandOutputBytes, when positive, selects the product Bash stream projection: valid
	// UTF-8, at most this many bytes, the tail when it is cut, and a truncated flag per stream. Zero keeps
	// the thesis summary, which releases 4 KiB tails with the raw byte counts.
	CommandOutputBytes int
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
	commandOutputBytes    int
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
	if config.CommandOutputBytes < 0 || config.CommandOutputBytes > durableCommandOutputMaxBytes {
		return nil, errors.New("durable executor command output bound is outside its permitted range")
	}
	return &DurableExecutor{
		store: config.Store, tip: config.Tip, callIssuer: config.CallIssuer,
		policyDigest: config.PolicyDigest, metadataPolicyDigest: config.MetadataPolicyDigest,
		commandRunner: config.CommandRunner, commandOutputBytes: config.CommandOutputBytes,
		bashOutputPaths:       cloneDurableOutputScopePaths(config.BashOutputPaths),
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
		// and returns a bounded summary without advancing the tip. The Store
		// reads and commits the whole file; the requested line window is
		// applied only to the released bytes.
		path, window, err := durableReadArguments(proposal.Arguments)
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
		return durableReadSummary(path, window, durableGenerationSummary{ID: e.tip.ID(), TreeDigest: e.tip.TreeDigest()}, content)
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
	return durableMutationSummary(proposal.Tool, mutation, e.commandRunner, e.commandOutputBytes)
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
// descriptive profile and the released stream projections; the containment status
// is a fixed, honest "not-established" because the session repository never proves containment.
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
// containment status is always "not-established". Stdout and Stderr are the
// released projection of the settled streams (durableCommandStreamProjection).
type durableCommandSummary struct {
	ExitCode                 int     `json:"exit_code"`
	ExitObserved             bool    `json:"exit_observed"`
	CommandContainmentStatus string  `json:"command_containment_status"`
	CommandRunnerProfile     string  `json:"command_runner_profile"`
	Stdout                   *string `json:"stdout,omitempty"`
	StdoutBytes              *int    `json:"stdout_bytes,omitempty"`
	StdoutTruncated          *bool   `json:"stdout_truncated,omitempty"`
	Stderr                   *string `json:"stderr,omitempty"`
	StderrBytes              *int    `json:"stderr_bytes,omitempty"`
	StderrTruncated          *bool   `json:"stderr_truncated,omitempty"`
}

// durableCommandStreamReleaseBytes bounds the released tail of each settled stream. The Store keeps the whole stream up to its 1 MiB capture bound.
// Under the signed pilot ceiling of 17 host calls, the bash streams contribute at most 17 x 2 x 4096 x 7 = 974,848 request
// bytes, counting 7 bytes per escaped control byte. A 4 KiB call envelope per result and a 64 KiB prompt allowance bring the
// modeled total to 1,110,016 bytes, 26% of the 4 MiB cap. This is the worst-case bash-stream contribution, not a guarantee that
// a whole request fits. Assistant text, original arguments, large reads, and write or edit arguments are not bounded by it.
const durableCommandStreamReleaseBytes = 4 << 10

// durableCommandStreamProjection releases the tail of one settled stream as text, as Pi's bash tool does: it keeps the
// end of the output and marks truncation. A tail cut that starts inside a rune drops the partial rune, and invalid
// UTF-8 becomes U+FFFD, so the text is a pure function of the raw bytes. rawBytes is the stream's full length.
func durableCommandStreamProjection(stream []byte) (text string, rawBytes int, truncated bool) {
	kept := stream
	if len(kept) > durableCommandStreamReleaseBytes {
		kept = kept[len(kept)-durableCommandStreamReleaseBytes:]
		for len(kept) > 0 && !utf8.RuneStart(kept[0]) {
			kept = kept[1:]
		}
		truncated = true
	}
	return strings.ToValidUTF8(string(kept), "�"), len(stream), truncated
}

// durableBashCommandSummary builds the model-facing view of one settled command. Governed replay calls it too, so
// the executor and the verifier release the same bytes from the same retained command result.
func durableBashCommandSummary(command sessionrepo.CommandResult, runnerProfile string) durableCommandSummary {
	stdout, stdoutBytes, stdoutTruncated := durableCommandStreamProjection(command.Stdout)
	stderr, stderrBytes, stderrTruncated := durableCommandStreamProjection(command.Stderr)
	return durableCommandSummary{
		ExitCode: command.ExitCode, ExitObserved: command.ExitObserved,
		CommandContainmentStatus: "not-established", CommandRunnerProfile: runnerProfile,
		Stdout: &stdout, StdoutBytes: &stdoutBytes, StdoutTruncated: &stdoutTruncated,
		Stderr: &stderr, StderrBytes: &stderrBytes, StderrTruncated: &stderrTruncated,
	}
}

// newDurableCommandSummary is the one encoder of a settled command's summary. The executor builds the
// summary the provider receives with it, and the governed replay rebuilds the expected summary from the
// Store's retained command result with it, so the two cannot disagree.
func newDurableCommandSummary(command sessionrepo.CommandResult, profile string, outputBytes int) *durableCommandSummary {
	if outputBytes == 0 {
		summary := durableBashCommandSummary(command, profile)
		return &summary
	}
	summary := &durableCommandSummary{
		ExitCode: command.ExitCode, ExitObserved: command.ExitObserved,
		CommandContainmentStatus: "not-established", CommandRunnerProfile: profile,
	}
	if outputBytes > 0 {
		stdout, stdoutCut := boundedCommandText(command.Stdout, outputBytes)
		stderr, stderrCut := boundedCommandText(command.Stderr, outputBytes)
		summary.Stdout, summary.StdoutTruncated = &stdout, &stdoutCut
		summary.Stderr, summary.StderrTruncated = &stderr, &stderrCut
	}
	return summary
}

// boundedCommandText returns data as valid UTF-8 of at most limit bytes. Invalid byte runs become U+FFFD
// first, so the bound holds for the text the provider sees. A longer text keeps its tail, because the end
// of a build or test run holds the failure, and the cut never splits a rune.
func boundedCommandText(data []byte, limit int) (string, bool) {
	text := strings.ToValidUTF8(string(data), string(utf8.RuneError))
	if len(text) <= limit {
		return text, false
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:], true
}

func durableMutationSummary(tool string, mutation sessionrepo.MutationResult, runner sessionrepo.CommandRunner, outputBytes int) (json.RawMessage, error) {
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
		payload.Command = newDurableCommandSummary(*mutation.Command, durableCommandRunnerProfile(runner), outputBytes)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode durable mutation summary: %w", err)
	}
	if len(encoded) > protocol.MaxFrameBytes {
		return nil, fmt.Errorf("%w: durable mutation summary exceeds the protocol bound; result withheld", runend.ErrCeiling)
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

// durableWorkspaceRoots are the absolute prefixes under which the model may name a workspace file. Pi reports the
// workspace as its working directory, which the Pi cell mounts at PiWorkerCellDefaultWorkingDirectory. The command
// cell mounts the same workspace at PiWorkerCellWorkdir, where bash runs.
var durableWorkspaceRoots = []string{piruntime.PiWorkerCellDefaultWorkingDirectory, piruntime.PiWorkerCellWorkdir}

// durableWorkspaceRelativePath maps a path the model names to the Store-relative path it denotes. A relative path
// resolves inside the workspace. An absolute path counts only under a workspace root at a slash boundary. Lexical
// cleaning drops "." and empty segments and resolves ".." in place, so "build/../task.txt" names "task.txt". The
// Store holds no symbolic links, so cleaning names the file the spelling named. A path that would leave the
// workspace is refused here, before the Store or admission sees it; its callers return that refusal as a no-effect one.
func durableWorkspaceRelativePath(modelPath string) (string, error) {
	relative := modelPath
	if strings.HasPrefix(modelPath, "/") {
		root, found := "", false
		for _, candidate := range durableWorkspaceRoots {
			if modelPath == candidate || strings.HasPrefix(modelPath, candidate+"/") {
				root, found = candidate, true
				break
			}
		}
		if !found {
			return "", errors.New("path is outside the workspace")
		}
		relative = strings.TrimLeft(modelPath[len(root):], "/")
	}
	cleaned := path.Clean(relative)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path is outside the workspace")
	}
	return cleaned, nil
}

func durableWriteArguments(raw json.RawMessage) (string, []byte, error) {
	if err := durableRequireExactMembers(raw, "path", "content"); err != nil {
		return "", nil, &protocol.NoEffectError{Tool: "write", Err: fmt.Errorf("write arguments: %w", err)}
	}
	var arguments durableWriteArgumentsPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&arguments); err != nil {
		return "", nil, &protocol.NoEffectError{Tool: "write", Err: fmt.Errorf("write arguments: %w", err)}
	}
	if err := durableNoTrailingJSON(decoder); err != nil {
		return "", nil, &protocol.NoEffectError{Tool: "write", Err: fmt.Errorf("write arguments: %w", err)}
	}
	if arguments.Path == "" {
		return "", nil, &protocol.NoEffectError{Tool: "write", Err: errors.New("write arguments require a path")}
	}
	storePath, err := durableWorkspaceRelativePath(arguments.Path)
	if err != nil {
		return "", nil, &protocol.NoEffectError{Tool: "write", Err: err}
	}
	return storePath, []byte(arguments.Content), nil
}

// durableEditArgumentsPayload keeps each replacement raw, so every edit object gets the same exact-member check
// as the top level before it is decoded.
type durableEditArgumentsPayload struct {
	Path  string            `json:"path"`
	Edits []json.RawMessage `json:"edits"`
}

type durableEditReplacementPayload struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

func durableEditArguments(raw json.RawMessage) (string, []sessionrepo.Replacement, error) {
	if err := durableRequireExactMembers(raw, "path", "edits"); err != nil {
		return "", nil, &protocol.NoEffectError{Tool: "edit", Err: fmt.Errorf("edit arguments: %w", err)}
	}
	var arguments durableEditArgumentsPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&arguments); err != nil {
		return "", nil, &protocol.NoEffectError{Tool: "edit", Err: fmt.Errorf("edit arguments: %w", err)}
	}
	if err := durableNoTrailingJSON(decoder); err != nil {
		return "", nil, &protocol.NoEffectError{Tool: "edit", Err: fmt.Errorf("edit arguments: %w", err)}
	}
	if arguments.Path == "" || len(arguments.Edits) == 0 {
		return "", nil, &protocol.NoEffectError{Tool: "edit", Err: errors.New("edit arguments require a path and at least one replacement")}
	}
	storePath, err := durableWorkspaceRelativePath(arguments.Path)
	if err != nil {
		return "", nil, &protocol.NoEffectError{Tool: "edit", Err: err}
	}
	edits := make([]sessionrepo.Replacement, len(arguments.Edits))
	for index, rawEdit := range arguments.Edits {
		if err := durableRequireExactMembers(rawEdit, "oldText", "newText"); err != nil {
			return "", nil, &protocol.NoEffectError{Tool: "edit", Err: fmt.Errorf("edit arguments: edits[%d]: %w", index, err)}
		}
		var edit durableEditReplacementPayload
		if err := json.Unmarshal(rawEdit, &edit); err != nil {
			return "", nil, &protocol.NoEffectError{Tool: "edit", Err: fmt.Errorf("edit arguments: edits[%d]: %w", index, err)}
		}
		edits[index] = sessionrepo.Replacement{OldText: edit.OldText, NewText: edit.NewText}
	}
	return storePath, edits, nil
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

// durableBashArguments strictly decodes the adapter bash arguments. Member names must be exact and unique, and
// trailing JSON is rejected, so the trusted supervisor never runs a command that differs from what the adapter signed.
func durableBashArguments(raw json.RawMessage) (string, error) {
	if err := durableRequireExactMembers(raw, "command", "timeout"); err != nil {
		return "", &protocol.NoEffectError{Tool: "bash", Err: fmt.Errorf("bash arguments: %w", err)}
	}
	var arguments durableBashArgumentsPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&arguments); err != nil {
		return "", &protocol.NoEffectError{Tool: "bash", Err: fmt.Errorf("bash arguments: %w", err)}
	}
	if err := durableNoTrailingJSON(decoder); err != nil {
		return "", &protocol.NoEffectError{Tool: "bash", Err: fmt.Errorf("bash arguments: %w", err)}
	}
	if arguments.Command == "" {
		return "", &protocol.NoEffectError{Tool: "bash", Err: errors.New("bash arguments require a command")}
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
// strict-JSON result would exceed the protocol's frame bound. It is the one
// place that applies the requested line window: content is the whole file the
// Store returned, and Size, ContentDigest and Content describe only the
// released window. The executor and the replay verifier both call it, so a
// replay reconstructs exactly the bytes the executor released.
func durableReadSummary(path string, window durableReadWindow, generation durableGenerationSummary, content []byte) (json.RawMessage, error) {
	released := window.apply(content)
	payload := durableReadSummaryPayload{
		Tool: "read", Path: path,
		Generation:    generation,
		Size:          len(released),
		ContentDigest: durableContentDigest(released),
		Content:       string(released),
		Outcome:       "success",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode durable read summary: %w", err)
	}
	if len(encoded) > protocol.MaxFrameBytes {
		return nil, fmt.Errorf("%w: durable read result exceeds the protocol bound; result withheld", runend.ErrCeiling)
	}
	return json.RawMessage(encoded), nil
}

// durableReadArgumentsPayload mirrors the adapter's read tool schema: a
// required path and optional numeric offset/limit. The Store always reads the
// whole bounded file; offset/limit only select the lines released to the model
// (see durableReadWindow).
type durableReadArgumentsPayload struct {
	Path   string   `json:"path"`
	Offset *float64 `json:"offset,omitempty"`
	Limit  *float64 `json:"limit,omitempty"`
}

// durableReadWindow is the line window of a read: offset is the 1-indexed
// first line and limit the maximum number of lines. Zero means absent, which is
// unambiguous because a request carrying 0 is refused before a window exists.
type durableReadWindow struct{ offset, limit int }

// durableReadLineCount validates one offset/limit: absent (0) or a positive
// integer. Stock Pi also tolerates 0, negative and fractional values; the
// durable path refuses them as malformed arguments rather than guess what the
// model meant.
func durableReadLineCount(name string, value *float64) (int, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 1 || *value != math.Trunc(*value) {
		return 0, &protocol.NoEffectError{Tool: "read", Err: fmt.Errorf("read %s must be a positive integer", name)}
	}
	// A file is bounded by durableReadMaxBytes, so any larger count reads to its end.
	return int(min(*value, math.MaxInt32)), nil
}

// apply returns the released bytes of content under the window. It mirrors
// stock Pi's read tool (pi-coding-agent 0.87.1, dist/core/tools/read.js): the
// file is split on LF only, so a trailing newline yields a final empty line and
// CRLF keeps its CR; the window is lines[offset-1 : offset-1+limit] joined with
// LF, so it carries a trailing newline only when it reaches the final empty
// line. Unlike Pi, an offset past the last line is an empty success rather than
// an error, so a model that overshoots does not lose its trial. Pi's own 2000
// line / 50KB truncation and its continuation notices (including the one Pi adds
// to an ordinary limited read) are not applied: the file is already bounded and
// the released bytes stay pure file content. Configurations B and C both read
// through this path, so these differences from stock Pi are the same in both.
func (window durableReadWindow) apply(content []byte) []byte {
	if window == (durableReadWindow{}) {
		return content
	}
	lines := bytes.Split(content, []byte("\n"))
	start := max(window.offset-1, 0)
	if start >= len(lines) {
		return []byte{}
	}
	end := len(lines)
	if window.limit > 0 {
		end = min(start+window.limit, end)
	}
	return bytes.Join(lines[start:end], []byte("\n"))
}

// durableReadArguments strictly decodes the adapter read arguments. Member names must be exact and unique, and
// trailing JSON is rejected, so the trusted supervisor never reads a path that differs from what the adapter signed.
func durableReadArguments(raw json.RawMessage) (string, durableReadWindow, error) {
	if err := durableRequireExactMembers(raw, "path", "offset", "limit"); err != nil {
		return "", durableReadWindow{}, &protocol.NoEffectError{Tool: "read", Err: fmt.Errorf("read arguments: %w", err)}
	}
	var arguments durableReadArgumentsPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&arguments); err != nil {
		return "", durableReadWindow{}, &protocol.NoEffectError{Tool: "read", Err: fmt.Errorf("read arguments: %w", err)}
	}
	if err := durableNoTrailingJSON(decoder); err != nil {
		return "", durableReadWindow{}, &protocol.NoEffectError{Tool: "read", Err: fmt.Errorf("read arguments: %w", err)}
	}
	if arguments.Path == "" {
		return "", durableReadWindow{}, &protocol.NoEffectError{Tool: "read", Err: errors.New("read arguments require a path")}
	}
	offset, err := durableReadLineCount("offset", arguments.Offset)
	if err != nil {
		return "", durableReadWindow{}, fmt.Errorf("read arguments: %w", err)
	}
	limit, err := durableReadLineCount("limit", arguments.Limit)
	if err != nil {
		return "", durableReadWindow{}, fmt.Errorf("read arguments: %w", err)
	}
	storePath, err := durableWorkspaceRelativePath(arguments.Path)
	if err != nil {
		return "", durableReadWindow{}, &protocol.NoEffectError{Tool: "read", Err: fmt.Errorf("read arguments: %w", err)}
	}
	return storePath, durableReadWindow{offset: offset, limit: limit}, nil
}

// durableContentDigest is the bare sha256 commitment the session repository
// uses for released result bytes.
func durableContentDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// durableRequireExactMembers refuses an argument object unless it is a JSON object whose member names are exactly
// the given schema names and each appears once. Go's decoder matches member names case-insensitively, so a case
// variant would otherwise replace a schema member, and a repeated name would silently keep its last value.
func durableRequireExactMembers(raw json.RawMessage, schemaNames ...string) error {
	allowed := make(map[string]bool, len(schemaNames))
	for _, name := range schemaNames {
		allowed[name] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if opening != json.Delim('{') {
		return errors.New("tool arguments must be a JSON object")
	}
	seen := make(map[string]bool, len(schemaNames))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, isName := token.(string)
		if !isName || !allowed[name] || seen[name] {
			return fmt.Errorf("tool argument member %v is not a unique schema name", token)
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
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
