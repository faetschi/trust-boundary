//go:build linux

package providerbridge

import (
	"encoding/json"
	"errors"
	"sync"

	"tbound/supervisor/internal/audit"
)

// DevelopmentProviderTurn is one scripted, offline provider response used only
// by the explicit, non-claim-bearing development launch route. It carries no
// credential, endpoint selection, model choice, or request material: the owner
// Conversation fixes the approved model and the registered request shape. The
// caller may only script the bounded response projection.
type DevelopmentProviderTurn struct {
	ResponseID string          `json:"response_id"`
	CallID     string          `json:"call_id,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Text       string          `json:"text,omitempty"`
	// FailStatus and FailBody inject a provider HTTP failure (see ScriptTurn).
	FailStatus int    `json:"fail_status,omitempty"`
	FailBody   string `json:"fail_body,omitempty"`
}

// NewDevelopmentOfflineConversation constructs a Conversation whose provider
// transport is the caller-supplied scripted offline turns. It performs no
// network access and loads no credentials. This is an explicit, Linux-only
// development seam, compiled only on Linux and reachable only from the explicit
// `serve --pi --dev-fixture` route: it must never be reachable from the
// production serve --pi route or any other composition root, and a Conversation
// built here carries no provider-authenticity, G1, or containment claim. Its
// receipt remains claim_bearing=false and provider_exchange=false. Production
// callers must use NewFromCredentials, or NewScriptedConversation
// where a corpus trial's admitted role selects a scripted source.
//
// It is NewScriptedConversation over a fixed list of turns that must carry their own IDs and that
// fails once the list is used up.
func NewDevelopmentOfflineConversation(cfg Config, journal *audit.Journal, turns []DevelopmentProviderTurn) (*Conversation, error) {
	if journal == nil {
		return nil, errors.New("development offline conversation requires the protected durable audit journal")
	}
	if len(turns) == 0 {
		return nil, errors.New("development offline conversation requires at least one scripted provider turn")
	}
	copied := make([]ScriptTurn, len(turns))
	for index, turn := range turns {
		if turn.ResponseID == "" && turn.FailStatus == 0 {
			return nil, errors.New("development offline turn requires a response ID")
		}
		copied[index] = ScriptTurn{ResponseID: turn.ResponseID, CallID: turn.CallID, Tool: turn.Tool,
			Arguments: append(json.RawMessage(nil), turn.Arguments...), Text: turn.Text,
			FailStatus: turn.FailStatus, FailBody: turn.FailBody}
	}
	return NewScriptedConversation(cfg, journal, &developmentSource{turns: copied})
}

// developmentSource serves a fixed list of turns once.
type developmentSource struct {
	mu    sync.Mutex
	turns []ScriptTurn
	next  int
}

func (s *developmentSource) Next([]ScriptMessage) (ScriptTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.turns) {
		return ScriptTurn{}, errors.New("development offline provider response script is exhausted")
	}
	turn := s.turns[s.next]
	s.next++
	return turn, nil
}
