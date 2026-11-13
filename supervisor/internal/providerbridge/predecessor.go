package providerbridge

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
)

// ErrContextExhausted reports a predecessor prefix that does not fit the transcript bounds of a new
// conversation. Nothing is truncated or summarized.
var ErrContextExhausted = errors.New("predecessor conversation context exceeds the transcript bounds")

const (
	importedPrefixSchema = "tbound-provider-imported-prefix/v1"
	historyImportedKind  = "provider_history_imported"
	historyImportedID    = "history-imported"
)

// PredecessorContext is the verified history a new conversation starts from. It grants no execution
// authority: its tool calls are never registered for correlation, and its response and tool-call IDs
// are reserved. The zero value means no predecessor. Only this package constructs a non-zero value.
type PredecessorContext struct{ sealed *predecessorContext }

type predecessorContext struct {
	receiptDigest       string
	messages            []importedMessage
	reservedResponseIDs []string
	reservedToolCallIDs []string
	digest              string
}

// importedMessage is the exact journal and digest form of one history message. Arguments stay a string
// so their bytes round-trip unchanged.
type importedMessage struct {
	Role       openrouter.Role    `json:"role"`
	Content    *string            `json:"content"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	ToolCalls  []importedToolCall `json:"tool_calls,omitempty"`
	Reasoning  string             `json:"reasoning,omitempty"`
}

type importedToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type historyImportedEvidence struct {
	WorkflowID               string            `json:"workflow_id"`
	ConversationID           string            `json:"conversation_id"`
	ImportedPrefixDigest     string            `json:"imported_prefix_digest"`
	PredecessorReceiptDigest string            `json:"predecessor_receipt_digest"`
	Messages                 []importedMessage `json:"messages"`
	ReservedResponseIDs      []string          `json:"reserved_response_ids"`
	ReservedToolCallIDs      []string          `json:"reserved_tool_call_ids"`
}

// Digest is the ImportedPrefixDigest, "sha256:" and 64 lowercase hex. It is empty for the zero value.
func (p PredecessorContext) Digest() string {
	if p.sealed == nil {
		return ""
	}
	return p.sealed.digest
}

// PredecessorReceiptDigest is the receipt digest of the immediate predecessor turn.
func (p PredecessorContext) PredecessorReceiptDigest() string {
	if p.sealed == nil {
		return ""
	}
	return p.sealed.receiptDigest
}

func (p PredecessorContext) IsZero() bool { return p.sealed == nil }

// TurnBoundary is the host record of how the predecessor turn ended. It becomes one user message built
// only from these validated values.
type TurnBoundary struct {
	PredecessorReceiptDigest string
	Outcome                  string
	ProjectBaselineDigest    string
}

var boundaryClauses = map[string]string{
	"published": "its file changes were applied to the project.",
	"no_diff":   "it left the project files unchanged.",
	"discarded": "its file changes were discarded and are not in the project.",
	"failed":    "it failed; none of its file changes are in the project.",
	"cancelled": "it was cancelled; none of its file changes are in the project.",
}

func (b TurnBoundary) message() (importedMessage, error) {
	clause, ok := boundaryClauses[b.Outcome]
	if !ok || !validPrefixedDigest(b.PredecessorReceiptDigest) || !validReplayTreeDigest(b.ProjectBaselineDigest) {
		return importedMessage{}, errors.New("turn boundary has an unknown outcome or an invalid digest")
	}
	text := `[TBound host record] The previous user turn ended with outcome "` + b.Outcome + `": ` + clause +
		" Current project baseline: " + b.ProjectBaselineDigest +
		". Tool results above show the workspace as it was when each tool ran."
	return importedMessage{Role: openrouter.User, Content: &text}, nil
}

// ReplayPredecessorContext replays a completed predecessor provider journal with the same strict replay
// as ReplayTranscript and returns its history after the system and developer prompts, followed by the
// boundary message. The transcript must end at awaiting_host_prompt after at least one exchange. It also
// returns the replay's projection, the one ReplayTranscript returns, for callers that verify it further.
func ReplayPredecessorContext(journal *audit.Journal, pins RecoveryPins, verifier StoreReplayVerifier,
	boundary TurnBoundary) (PredecessorContext, TranscriptProjection, error) {
	boundaryMessage, err := boundary.message()
	if err != nil {
		return PredecessorContext{}, TranscriptProjection{}, err
	}
	var replayed replayedHistory
	projection, err := replayTranscript(journal, pins, verifier, &replayed)
	if err != nil {
		return PredecessorContext{}, TranscriptProjection{}, err
	}
	if projection.Disposition != DispositionAwaitingHostPrompt || projection.ProviderSequence == 0 {
		return PredecessorContext{}, TranscriptProjection{}, replayError("predecessor transcript did not end with a final provider answer")
	}
	messages := append(toImportedMessages(replayed.messages), boundaryMessage)
	predecessor := newPredecessorContext(boundary.PredecessorReceiptDigest, messages, setKeys(replayed.responses), setKeys(replayed.calls))
	return predecessor, projection, nil
}

// ExtendPredecessorContext adds the boundary of a predecessor that is not context-eligible to previous.
// A zero previous yields a context of only the boundary message.
func ExtendPredecessorContext(previous PredecessorContext, boundary TurnBoundary) (PredecessorContext, error) {
	boundaryMessage, err := boundary.message()
	if err != nil {
		return PredecessorContext{}, err
	}
	var messages []importedMessage
	var responses, calls []string
	if previous.sealed != nil {
		messages = append(messages, previous.sealed.messages...)
		responses, calls = previous.sealed.reservedResponseIDs, previous.sealed.reservedToolCallIDs
	}
	return newPredecessorContext(boundary.PredecessorReceiptDigest, append(messages, boundaryMessage), responses, calls), nil
}

func newPredecessorContext(receiptDigest string, messages []importedMessage, responses, calls []string) PredecessorContext {
	sealed := &predecessorContext{
		receiptDigest: receiptDigest, messages: messages,
		reservedResponseIDs: sortedCopy(responses), reservedToolCallIDs: sortedCopy(calls),
	}
	sealed.digest = importedPrefixDigest(sealed.receiptDigest, sealed.messages, sealed.reservedResponseIDs, sealed.reservedToolCallIDs)
	return PredecessorContext{sealed: sealed}
}

func importedPrefixDigest(receiptDigest string, messages []importedMessage, responses, calls []string) string {
	encoded, err := json.Marshal(struct {
		SchemaVersion            string            `json:"schema_version"`
		PredecessorReceiptDigest string            `json:"predecessor_receipt_digest"`
		Messages                 []importedMessage `json:"messages"`
		ReservedResponseIDs      []string          `json:"reserved_response_ids"`
		ReservedToolCallIDs      []string          `json:"reserved_tool_call_ids"`
	}{importedPrefixSchema, receiptDigest, messages, responses, calls})
	if err != nil {
		return ""
	}
	return digestBytes(encoded)
}

func (p *predecessorContext) evidence(workflowID, conversationID string) historyImportedEvidence {
	return historyImportedEvidence{
		WorkflowID: workflowID, ConversationID: conversationID, ImportedPrefixDigest: p.digest,
		PredecessorReceiptDigest: p.receiptDigest, Messages: p.messages,
		ReservedResponseIDs: p.reservedResponseIDs, ReservedToolCallIDs: p.reservedToolCallIDs,
	}
}

// validateImportedHistory checks a journaled import record against its pin and returns the messages
// it seeds. Every tool call it carries must be reserved.
func validateImportedHistory(evidence historyImportedEvidence, cfg Config, pin string) ([]openrouter.Message, error) {
	if evidence.WorkflowID != cfg.WorkflowID || evidence.ConversationID != cfg.ConversationID ||
		evidence.ImportedPrefixDigest != pin || !validPrefixedDigest(evidence.PredecessorReceiptDigest) ||
		importedPrefixDigest(evidence.PredecessorReceiptDigest, evidence.Messages, evidence.ReservedResponseIDs,
			evidence.ReservedToolCallIDs) != pin ||
		!strictlySortedIDs(evidence.ReservedResponseIDs) || !strictlySortedIDs(evidence.ReservedToolCallIDs) ||
		len(evidence.Messages) == 0 {
		return nil, errors.New("imported provider history does not match its pinned digest or conversation")
	}
	reserved := make(map[string]struct{}, len(evidence.ReservedToolCallIDs))
	for _, id := range evidence.ReservedToolCallIDs {
		reserved[id] = struct{}{}
	}
	for _, message := range evidence.Messages {
		if message.Role != openrouter.User && message.Role != openrouter.Assistant && message.Role != openrouter.Tool {
			return nil, errors.New("imported provider history carries a system or developer message")
		}
		for _, call := range message.ToolCalls {
			if _, ok := reserved[call.ID]; !ok {
				return nil, errors.New("imported provider history carries an unreserved tool call")
			}
		}
	}
	return fromImportedMessages(evidence.Messages), nil
}

func toImportedMessages(messages []openrouter.Message) []importedMessage {
	out := make([]importedMessage, len(messages))
	for i, message := range messages {
		out[i] = importedMessage{Role: message.Role, ToolCallID: message.ToolCallID, Reasoning: message.Reasoning}
		if message.Content != nil {
			content := *message.Content
			out[i].Content = &content
		}
		for _, call := range message.ToolCalls {
			out[i].ToolCalls = append(out[i].ToolCalls, importedToolCall{ID: call.ID, Name: call.Name, Arguments: string(call.RawArguments)})
		}
	}
	return out
}

func fromImportedMessages(messages []importedMessage) []openrouter.Message {
	out := make([]openrouter.Message, len(messages))
	for i, message := range messages {
		out[i] = openrouter.Message{Role: message.Role, ToolCallID: message.ToolCallID, Reasoning: message.Reasoning}
		if message.Content != nil {
			content := *message.Content
			out[i].Content = &content
		}
		for _, call := range message.ToolCalls {
			out[i].ToolCalls = append(out[i].ToolCalls, openrouter.PriorToolCall{ID: call.ID, Name: call.Name, RawArguments: json.RawMessage(call.Arguments)})
		}
	}
	return out
}

func validPrefixedDigest(value string) bool {
	rest, ok := strings.CutPrefix(value, "sha256:")
	return ok && validLowerHexDigest(rest)
}

func strictlySortedIDs(ids []string) bool {
	for i, id := range ids {
		if !validText(id, MaxIdentityBytes) || i > 0 && ids[i-1] >= id {
			return false
		}
	}
	return ids != nil
}

func sortedCopy(ids []string) []string {
	out := append([]string{}, ids...)
	sort.Strings(out)
	return out
}

func setKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}
