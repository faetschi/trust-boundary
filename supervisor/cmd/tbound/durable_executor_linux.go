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
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/sessionrepo"
)

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
}

// DurableExecutor is the production-shaped Executor for cmd/tbound. It maps one
// trusted ALLOW proposal onto one sessionrepo mutation on the current tip and
// releases a strict-JSON summary only after the effect is durable. It owns no
// containment, publication, or provider claim: those remain unresolved in the
// session repository evidence.
type DurableExecutor struct {
	mu                   sync.Mutex
	store                *sessionrepo.Store
	tip                  *sessionrepo.Generation
	callIssuer           string
	policyDigest         string
	metadataPolicyDigest string
}

// NewDurableExecutor validates and constructs a DurableExecutor. The caller
// retains ownership of the store, the journal, and the tip generation.
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
	return &DurableExecutor{
		store: config.Store, tip: config.Tip, callIssuer: config.CallIssuer,
		policyDigest: config.PolicyDigest, metadataPolicyDigest: config.MetadataPolicyDigest,
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

// Execute maps one trusted ALLOW proposal to a sessionrepo Write or Edit on the
// current tip. It never fabricates authority: the argument digest, effect ID,
// generation, transition, and settlement evidence all come from the store's
// durable path.
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
	if e.tip == nil {
		return nil, errors.New("durable executor has no current tip generation")
	}

	var mutation sessionrepo.MutationResult
	switch proposal.Tool {
	case "write":
		path, content, err := durableWriteArguments(proposal.Arguments)
		if err != nil {
			return nil, err
		}
		mutation, err = e.store.Write(ctx, e.tip, operation, policyDecision, path, content)
		if err != nil {
			return nil, fmt.Errorf("durable write: %w", err)
		}
	case "edit":
		path, edits, err := durableEditArguments(proposal.Arguments)
		if err != nil {
			return nil, err
		}
		mutation, err = e.store.Edit(ctx, e.tip, operation, policyDecision, path, edits)
		if err != nil {
			return nil, fmt.Errorf("durable edit: %w", err)
		}
	default:
		return nil, fmt.Errorf("durable executor does not implement tool %q", proposal.Tool)
	}
	if mutation.Generation == nil || mutation.Transition.ID == "" {
		return nil, errors.New("durable mutation returned no committed generation or transition")
	}
	e.tip = mutation.Generation
	return durableMutationSummary(proposal.Tool, mutation)
}

// durableMutationSummary encodes a strict-JSON view of a committed mutation. It
// deliberately contains no containment, publication, or provider claim.
type durableMutationSummaryPayload struct {
	Tool       string                   `json:"tool"`
	Generation durableGenerationSummary `json:"generation"`
	Transition durableTransitionSummary `json:"transition"`
	EffectID   string                   `json:"effect_id"`
	Outcome    string                   `json:"outcome"`
}

type durableGenerationSummary struct {
	ID         string `json:"id"`
	TreeDigest string `json:"tree_digest"`
}

type durableTransitionSummary struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
}

func durableMutationSummary(tool string, mutation sessionrepo.MutationResult) (json.RawMessage, error) {
	payload := durableMutationSummaryPayload{
		Tool: tool,
		Generation: durableGenerationSummary{
			ID: mutation.Generation.ID(), TreeDigest: mutation.Generation.TreeDigest(),
		},
		Transition: durableTransitionSummary{ID: mutation.Transition.ID, Sequence: mutation.Transition.Sequence},
		EffectID:   mutation.EffectID,
		Outcome:    mutation.Outcome,
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

// durableOperationIdentity derives the stable operation identity for one
// proposal call from the trusted decision tuple plus the Pi call ID.
func durableOperationIdentity(proposal protocol.Proposal, decision gate.Decision, callIssuer string) (delta.OperationIdentity, error) {
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
