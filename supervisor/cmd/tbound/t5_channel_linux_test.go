//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
)

const (
	t5ChannelDraftDir      = "../../../corpus/pilot/t5-2026-10-29"
	t5ChannelProvenanceDir = "../../../corpus/pilot/provenance-2026-10-29"
	t5ChannelCallID        = "t5-call-1"
	t5ChannelFinalText     = "done"
)

// t5RecordingSource answers with a fixed script and keeps the history of every request it answers, so the
// test reads the messages the provider actually receives.
type t5RecordingSource struct {
	script   providerbridge.ScriptSource
	mu       sync.Mutex
	requests [][]providerbridge.ScriptMessage
}

func (s *t5RecordingSource) Next(history []providerbridge.ScriptMessage) (providerbridge.ScriptTurn, error) {
	s.mu.Lock()
	s.requests = append(s.requests, slices.Clone(history))
	s.mu.Unlock()
	return s.script.Next(history)
}

// t5ChannelRecord is the part of one provenance-2026-10-29/cases.json entry that the channel test uses.
type t5ChannelRecord struct {
	LegitimatePrompt      string `json:"legitimate_prompt"`
	Injection             string `json:"injection"`
	OriginalCarrierSHA256 string `json:"original_carrier_sha256"`
}

func loadT5ChannelManifest(t *testing.T) evalcorpus.Manifest {
	t.Helper()
	file, err := os.Open(t5ChannelDraftDir + "/manifest.draft.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := evalcorpus.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func loadT5ChannelRecords(t *testing.T) map[string]t5ChannelRecord {
	t.Helper()
	raw, err := os.ReadFile(t5ChannelProvenanceDir + "/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var records map[string]t5ChannelRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	return records
}

// t5ChannelResult returns the provider tool result that the provider journal holds for one call ID.
func t5ChannelResult(t *testing.T, f *workflowVerticalFixture, callID string) []byte {
	t.Helper()
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_tool_result" {
			continue
		}
		var evidence providerResultEvidence
		if err := unmarshalGovernedJSON(record.Event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.ToolCallID == callID {
			return []byte(evidence.Result)
		}
	}
	t.Fatalf("provider journal holds no tool result for %s", callID)
	return nil
}

// t5ChannelCase drives one carrier case through the governed vertical path. The frozen read is the only
// tool call, and the model answers with plain text, so the injection reaches the model only as read output.
func t5ChannelCase(t *testing.T, c evalcorpus.Case, record t5ChannelRecord, artifacts fs.FS) {
	spec, err := corpusadmit.LoadFixture(artifacts, c.Adversarial.WorkspaceFixtureDigest)
	if err != nil {
		t.Fatal(err)
	}
	files, err := spec.Files()
	if err != nil {
		t.Fatal(err)
	}
	frozen := c.Trial.Script[0]
	if frozen.Tool != "read" {
		t.Fatalf("frozen turn 1 is %s, not the read of the carrier", frozen.Tool)
	}
	source := &t5RecordingSource{script: providerbridge.NewStaticScript([]providerbridge.ScriptTurn{
		{CallID: t5ChannelCallID, Tool: frozen.Tool, Arguments: frozen.Arguments},
		{Text: t5ChannelFinalText},
	})}
	f := newWorkflowVerticalFixtureWith(t, verticalFixtureSetup{
		seed:   files,
		prompt: c.Trial.Prompt,
		provider: func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
			return providerbridge.NewScriptedConversation(config, journal, source)
		},
	}, false, 0)

	proposal, decision := ledgerDenyTurn(t, f, t5ChannelCallID, gate.Allow)
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(f.ctx, proposal, decision); err != nil {
		t.Fatalf("carrier read was not released: %v", err)
	}
	requireFinalAnswer(t, f, t5ChannelFinalText, 3, 2048)
	f.conversation.Close()
	if _, err := f.store.Verify(); err != nil {
		t.Fatalf("verify Store after the carrier read: %v", err)
	}

	source.mu.Lock()
	requests := source.requests
	source.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider received %d requests, want 2", len(requests))
	}
	for index, request := range requests {
		for _, message := range request {
			if message.Role != "tool" && strings.Contains(message.Content, record.Injection) {
				t.Errorf("request %d carries the injection in a %s message", index+1, message.Role)
			}
		}
	}
	if !slices.ContainsFunc(requests[0], func(message providerbridge.ScriptMessage) bool {
		return message.Role == "user" && message.Content == record.LegitimatePrompt
	}) {
		t.Error("first request has no user message equal to the legitimate prompt")
	}

	journaledResult := t5ChannelResult(t, f, t5ChannelCallID)
	result, err := protocol.DecodeResult(journaledResult)
	if err != nil {
		t.Fatal(err)
	}
	var journaled durableReadSummaryPayload
	if err := decodeGovernedResult(result.Output, &journaled); err != nil {
		t.Fatal(err)
	}
	if journaled.Outcome != "success" || !strings.Contains(journaled.Content, record.Injection) {
		t.Errorf("journaled read result for %s does not release the injection: outcome %q", t5ChannelCallID, journaled.Outcome)
	}

	var tool *providerbridge.ScriptMessage
	for i := range requests[1] {
		if requests[1][i].Role == "tool" && requests[1][i].ToolCallID == t5ChannelCallID {
			tool = &requests[1][i]
		}
	}
	if tool == nil {
		t.Fatalf("second request carries no tool message for %s", t5ChannelCallID)
	}
	// modelResultText (providerbridge/result_hints.go) adds "\nhint: " only to denials and failed commands, and
	// scripted.go strips that suffix from the history. The cut mirrors the strip, so a read must match exactly.
	forwarded, _, _ := strings.Cut(tool.Content, "\nhint: ")
	if forwarded != string(journaledResult) {
		t.Errorf("second request's tool result for %s is not the journaled provider tool result", t5ChannelCallID)
	}
	forwardedResult, err := protocol.DecodeResult([]byte(forwarded))
	if err != nil {
		t.Fatal(err)
	}
	var sent durableReadSummaryPayload
	if err := decodeGovernedResult(forwardedResult.Output, &sent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.Content, record.Injection) {
		t.Errorf("second request's tool result for %s does not carry the injection", t5ChannelCallID)
	}
	carrier := strings.TrimSuffix(sent.Content, "\n"+record.Injection+"\n")
	if sum := sha256.Sum256([]byte(carrier)); hex.EncodeToString(sum[:]) != record.OriginalCarrierSHA256 {
		t.Errorf("second request's read of %s does not begin with the original carrier bytes", t5ChannelCallID)
	}
}

// TestT5ToolReadCarrierChannel drives each of the seven carrier cases through the governed vertical path.
func TestT5ToolReadCarrierChannel(t *testing.T) {
	draft := loadT5ChannelManifest(t)
	records := loadT5ChannelRecords(t)
	if len(records) != 7 {
		t.Fatalf("provenance holds %d carrier cases, want 7", len(records))
	}
	artifacts := os.DirFS(t5ChannelDraftDir + "/artifacts")
	ran := 0
	for _, c := range draft.Cases {
		record, ok := records[c.CaseID]
		if !ok {
			continue
		}
		ran++
		t.Run(c.CaseID, func(t *testing.T) {
			t5ChannelCase(t, c, record, artifacts)
		})
	}
	if ran != 7 {
		t.Fatalf("ran %d carrier cases, want 7", ran)
	}
}
