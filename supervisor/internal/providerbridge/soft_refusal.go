package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
)

const SoftRefusalEvent = "provider_proven_unexecuted"

// SoftRefusalEvidence is a product precondition result, never an ALLOW or gate DENY.
type SoftRefusalEvidence struct {
	WorkflowID          string `json:"workflow_id"`
	ConversationID      string `json:"conversation_id"`
	ToolCallID          string `json:"tool_call_id"`
	Tool                string `json:"tool"`
	ArgumentsDigest     string `json:"arguments_digest"`
	TypedArgumentDigest string `json:"typed_argument_digest"`
	Generation          string `json:"generation"`
	TreeDigest          string `json:"tree_digest"`
	Code                string `json:"code"`
	Hint                string `json:"hint"`
	Result              string `json:"result"`
}

type SoftRefusalBinding struct {
	Evidence SoftRefusalEvidence
	Proposal protocol.Proposal
	Record   audit.Record
}

// Product replay must supply a concrete verifier that recomputes the precondition from the Store.
type SoftRefusalVerifier interface {
	VerifySoftRefusal(SoftRefusalBinding) error
}

func SoftRefusalText(code, hint string) string {
	return fmt.Sprintf("Error: %s. No tool effect occurred.\nHint: %s", code, hint)
}

func SoftRefusalRetirementDigest(record audit.Record) (string, error) {
	var evidence SoftRefusalEvidence
	if record.Event.Kind != SoftRefusalEvent || unmarshalCanonicalEvent(record.Event.Data, &evidence) != nil || evidence.ToolCallID == "" || evidence.Result != SoftRefusalText(evidence.Code, evidence.Hint) {
		return "", ErrTranscriptReplay
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	return digestDomain("tbound-proven-unexecuted-call-retirement/v1", data)[len("sha256:"):], nil
}

// RecordSoftRefusal consumes only the already correlated product call. The caller supplies Store
// preflight evidence; AdvanceProgress verifies it again and retires the capture before release.
func (c *Conversation) RecordSoftRefusal(ctx context.Context, proposal protocol.Proposal, typedDigest, code, hint string, boundary func(string)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cfg.ProductSoftRefusals || c.closed || c.state != stateAwaitingDecision || c.current == nil || !sameProposal(proposal, c.current.proposal) {
		return c.failLocked(ErrWrongState)
	}
	if err := ctx.Err(); err != nil {
		return c.failLocked(err)
	}
	if !strings.HasPrefix(typedDigest, "sha256:") || !validLowerHexDigest(strings.TrimPrefix(typedDigest, "sha256:")) || !validText(code, MaxIdentityBytes) || !validText(hint, 1024) || c.cfg.AdvanceProgress == nil {
		return c.failLocked(errors.New("soft refusal lacks preflight evidence or retirement authority"))
	}
	evidence := SoftRefusalEvidence{WorkflowID: c.cfg.WorkflowID, ConversationID: c.cfg.ConversationID, ToolCallID: proposal.ToolCallID, Tool: proposal.Tool, ArgumentsDigest: c.current.call.CanonicalArgumentsDigest, TypedArgumentDigest: typedDigest, Generation: c.generation, TreeDigest: c.treeDigest, Code: code, Hint: hint, Result: SoftRefusalText(code, hint)}
	data, err := json.Marshal(evidence)
	if err != nil {
		return c.failLocked(err)
	}
	if boundary != nil {
		boundary("before-soft-refusal-record")
	}
	if _, err = c.appendLocked(SoftRefusalEvent, "soft-refusal", data); err != nil {
		return c.failLocked(err)
	}
	if boundary != nil {
		boundary("after-soft-refusal-record")
	}
	if err = c.appendHistoryLocked(openrouter.Message{Role: openrouter.Tool, ToolCallID: proposal.ToolCallID, Content: &evidence.Result}); err != nil {
		return c.failLocked(err)
	}
	c.current = nil
	c.state = stateReadyToExchange
	if err = c.cfg.AdvanceProgress(ctx); err != nil {
		return c.failLocked(err)
	}
	if boundary != nil {
		boundary("after-soft-refusal-retirement")
	}
	return nil
}
