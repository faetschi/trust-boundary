//go:build linux

package main

import (
	"testing"

	"tbound/supervisor/internal/workflowrecovery"
)

// A resumed generation's settled Bash records carry the session and run the binder minted for it. The command
// history finds them only when the transcript pin of that generation maps to the same identities.
func TestRecoveryCommandSessionForPinNamesMintedGenerationIdentities(t *testing.T) {
	entry := piServiceWorkflowEntry{WorkflowID: "wf", SessionID: "wf-session-g1", RunID: "wf-run-g1", ConversationID: "wf-conversation-g1"}
	cases := []struct {
		name, conversationID, wantSession, wantRun string
	}{
		{"entry conversation keeps its own identities", "wf-conversation-g1", "wf-session-g1", "wf-run-g1"},
		{"generation 2 names the minted session and run", "wf-conversation-g2", "wf-session-g2", "wf-session-g2"},
		{"generation 3 names the minted session and run", "wf-conversation-g3", "wf-session-g3", "wf-session-g3"},
		{"unknown conversation matches no record", "wf-other-conversation", "", ""},
		{"zero-padded generation matches no record", "wf-conversation-g02", "", ""},
	}
	for _, tc := range cases {
		gotSession, gotRun := recoveryCommandSessionForPin(entry, workflowrecovery.TranscriptPin{ConversationID: tc.conversationID})
		if gotSession != tc.wantSession || gotRun != tc.wantRun {
			t.Errorf("%s: session %q run %q, want session %q run %q", tc.name, gotSession, gotRun, tc.wantSession, tc.wantRun)
		}
	}
	// A workflow id that itself contains the conversation marker still names its own generations.
	marked := piServiceWorkflowEntry{WorkflowID: "wf-conversation-g2", SessionID: "s", RunID: "r", ConversationID: "c"}
	session, run := recoveryCommandSessionForPin(marked, workflowrecovery.TranscriptPin{ConversationID: "wf-conversation-g2-conversation-g3"})
	if session != "wf-conversation-g2-session-g3" || run != session {
		t.Errorf("marked workflow id: session %q run %q, want wf-conversation-g2-session-g3 twice", session, run)
	}
}
