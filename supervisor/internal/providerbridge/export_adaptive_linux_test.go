//go:build linux

package providerbridge

import (
	"encoding/json"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
)

// Exports for the external adaptive-source conversation tests: the adaptive source lives in
// internal/adaptivesource, which imports this package, so these tests cannot be in package providerbridge.
type CommandSummary = commandSummary

func MutationOutput(tool, generation, treeDigest, transition string, sequence uint64, effect string, command *CommandSummary) json.RawMessage {
	return mutationOutput(tool, generation, treeDigest, transition, sequence, effect, command)
}

func ReadOutput(generation, treeDigest, content string) json.RawMessage {
	return readOutput(generation, treeDigest, content)
}

func ProposalFor(turn Turn) protocol.Proposal { return proposalFor(turn) }

type TestJournal struct{ memoryJournal }

func (j *TestJournal) Snapshot() []audit.Event { return j.snapshot() }

func NewScriptedForTest(cfg Config, journal *TestJournal, source ScriptSource) (*Conversation, error) {
	return newScripted(cfg, &journal.memoryJournal, source)
}

func NewDenyingExecutor(deny map[int]*protocol.AdmissionDeniedError, outputs []json.RawMessage) Executor {
	return &denyingExecutor{deny: deny, outputs: outputs}
}
