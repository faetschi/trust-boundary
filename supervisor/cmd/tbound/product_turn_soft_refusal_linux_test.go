//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

func TestProductTurnSoftRefusalContinuesWithoutAnEffect(t *testing.T) {
	for _, c := range []struct{ name, tool, arguments, code string }{
		{"missing read", "read", `{"path":"missing.txt"}`, "read_missing"},
		{"missing windowed read", "read", `{"path":"missing.txt","offset":1}`, "read_missing"},
		{"directory write", "write", `{"path":"dir","content":"x"}`, "target_not_regular"},
		{"absent old text", "edit", `{"path":"task.txt","edits":[{"oldText":"missing","newText":"x"}]}`, "edit_old_text_absent"},
		{"ambiguous old text", "edit", `{"path":"ambiguous.txt","edits":[{"oldText":"same","newText":"x"}]}`, "edit_old_text_ambiguous"},
		{"directory edit", "edit", `{"path":"dir","edits":[{"oldText":"old","newText":"x"}]}`, "target_not_regular"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, nil, func(root string) {
				writeProductDriverFile(t, root, "ambiguous.txt", "same same")
				if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(root, "dir"), 0o755); err != nil {
					t.Fatal(err)
				}
			}, true)
			script := &productDriverScript{turns: []providerbridge.ScriptTurn{
				{ResponseID: "soft-response", CallID: "soft-call", Tool: c.tool, Arguments: json.RawMessage(c.arguments)},
				{ResponseID: "soft-final", Text: "Done."},
			}}
			receipt, turn, err := f.run(t, f.submit(t, "soft", "Try the tool."), script)
			if err != nil || turn.Outcome != productsession.OutcomeNoDiff || script.requests() != 2 {
				t.Fatalf("soft refusal must continue: outcome=%s requests=%d err=%v", turn.Outcome, script.requests(), err)
			}
			encoded, _ := json.Marshal(script.histories[1])
			if !strings.Contains(string(encoded), c.code) || !strings.Contains(string(encoded), "Hint:") {
				t.Fatalf("missing error and hint: %s", encoded)
			}
			paths, err := productTurnPathsFromLink(*turn.Link)
			if err != nil {
				t.Fatal(err)
			}
			retained, err := openRetainedProductTurn(paths, f.env.storeOptions(turn), f.env.OpenCommandHistory)
			if err != nil {
				t.Fatal(err)
			}
			defer retained.Close()
			bundle, err := retained.Store.EvidenceBundle()
			disposition, reconstructErr := sessionrepo.Reconstruct(bundle)
			if err != nil || reconstructErr != nil || disposition.Quarantined || len(disposition.Operations) != 0 {
				t.Fatalf("refusal affected Store: %+v %v %v", disposition, err, reconstructErr)
			}
			trace, err := retained.ProviderJournal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			refusals := 0
			for _, record := range trace.Records {
				if record.Event.Kind == "provider_gate_decision" {
					t.Fatal("soft refusal recorded a gate decision")
				}
				if record.Event.Kind != providerbridge.SoftRefusalEvent {
					continue
				}
				var evidence providerbridge.SoftRefusalEvidence
				if err := json.Unmarshal(record.Event.Data, &evidence); err != nil {
					t.Fatal(err)
				}
				want, err := sessionrepoArgumentDigest(protocol.Proposal{Tool: c.tool, Arguments: json.RawMessage(c.arguments)}, nil)
				if err != nil || evidence.TypedArgumentDigest != want || evidence.TreeDigest != bundle.Baseline.TreeDigest {
					t.Fatalf("refusal does not bind effect inputs: %+v %v", evidence, err)
				}
				refusals++
			}
			if refusals != 1 {
				t.Fatalf("refusal count %d", refusals)
			}
			if err := retained.Close(); err != nil {
				t.Fatal(err)
			}
			host, tokens := productDriverExpectedUsage(t, paths.ProviderJournalPath, 0)
			if receipt.Usage.HostCalls != host || receipt.Usage.Tokens != tokens {
				t.Fatalf("refusal charged an effect: %+v want %d/%d", receipt.Usage, host, tokens)
			}
			f.followUp(t, "after-soft", productsession.OutcomeNoDiff)
		})
	}
}

func TestProductTurnSoftRefusalRejectsChangedGeneration(t *testing.T) {
	f := prepareWorkflowVertical(t, workflowVerticalOptions{})
	if err := os.WriteFile(filepath.Join(f.storePath, "generations", f.durable.Tip().ID(), "task.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	refusal, err := f.durable.productPreflight(protocol.Proposal{Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)})
	if err == nil || refusal != nil {
		t.Fatalf("changed generation produced soft evidence: %+v %v", refusal, err)
	}
}

func TestProductTurnSoftRefusalHostBudgetBoundsRetries(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	f.env.MaxWorkerSteps = 30
	script := &productDriverScript{}
	for i := 0; i < 25; i++ {
		script.turns = append(script.turns, providerbridge.ScriptTurn{ResponseID: fmt.Sprintf("retry-response-%d", i), CallID: fmt.Sprintf("retry-call-%d", i), Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)})
	}
	receipt, turn, err := f.run(t, f.submit(t, "retry", "Read a file."), script)
	if err == nil || turn.Outcome != productsession.OutcomeFailed || script.requests() != 20 || receipt.Usage.HostCalls != 20 || receipt.Usage.Tokens != 20*1024 {
		t.Fatalf("host limit did not stop refusals: %+v requests=%d err=%v", turn, script.requests(), err)
	}
}

func TestProductTurnSoftRefusalOutOfScopeStaysUnknown(t *testing.T) {
	for _, c := range []struct{ name, tool, args string }{
		{"missing edit", "edit", `{"path":"missing.txt","edits":[{"oldText":"old","newText":"new"}]}`},
		{"missing write parent", "write", `{"path":"missing/file","content":"x"}`},
		{"directory read", "read", `{"path":"dir"}`},
		{"unknown argument", "read", `{"path":"missing.txt","unknown":true}`},
		{"malformed argument", "read", `{"path":42}`},
		{"invalid offset", "read", `{"path":"missing.txt","offset":0}`},
		{"non-directory ancestor", "read", `{"path":"task.txt/child"}`},
		{"overlapping edits", "edit", `{"path":"task.txt","edits":[{"oldText":"turn fixture","newText":"x"},{"oldText":"fixture","newText":"y"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, nil, func(root string) {
				if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(root, "dir"), 0o755); err != nil {
					t.Fatal(err)
				}
			}, true)
			script := &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: "bad-response", CallID: "bad-call", Tool: c.tool, Arguments: json.RawMessage(c.args)}, {ResponseID: "must-not-run", Text: "Wrong."}}}
			_, turn, err := f.run(t, f.submit(t, "bad", "Try the tool."), script)
			if err == nil || turn.State != productsession.StateUnknown || script.requests() != 1 {
				t.Fatalf("out-of-scope failure continued: %+v requests=%d err=%v", turn, script.requests(), err)
			}
		})
	}
}

func TestProductTurnSoftRefusalTamperedPredecessorBlocksLaunch(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: "soft-response", CallID: "soft-call", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)}, {ResponseID: "soft-final", Text: "Done."}}}
	_, turn, err := f.run(t, f.submit(t, "soft", "Try a read."), script)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := productTurnPathsFromLink(*turn.Link)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.ProviderJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	// audit.Event.Data is base64 encoded; corrupt the retained refusal frame itself.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	changed := false
	for i, line := range lines {
		if strings.Contains(line, providerbridge.SoftRefusalEvent) {
			lines[i] = strings.Replace(line, `"hash":"`, `"hash":"0`, 1)
			changed = true
		}
	}
	if !changed {
		t.Fatal("no retained refusal frame")
	}
	if err := os.WriteFile(paths.ProviderJournalPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: "must-not-launch", Text: "Wrong."}}}
	_, blocked, err := f.run(t, f.submit(t, "after-tamper", "Continue."), next)
	if err == nil || blocked.RefusalCode != productsession.CodePredecessorEvidenceInvalid || next.requests() != 0 {
		t.Fatalf("tampered predecessor launched: %+v requests=%d err=%v", blocked, next.requests(), err)
	}
}

func TestProductTurnSoftRefusalRetiresCapturedAuthority(t *testing.T) {
	f := prepareWorkflowVertical(t, workflowVerticalOptions{launchProof: true})
	f.config.ProductSoftRefusals = true
	f.startConversation(t, func(cfg providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		return providerbridge.NewScriptedConversation(cfg, journal, &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: "soft-response", CallID: "soft-call", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)}, {ResponseID: "soft-final", Text: "Done."}}})
	})
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID, Tool: turn.ToolCall.Name, Arguments: turn.ToolCall.Arguments}
	matched, err := f.conversation.Correlate(f.ctx, proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate: %+v %v", matched, err)
	}
	refusal, err := f.durable.productPreflight(proposal)
	if err != nil || refusal == nil {
		t.Fatalf("preflight: %+v %v", refusal, err)
	}
	if err := f.conversation.RecordSoftRefusal(f.ctx, proposal, refusal.ArgumentDigest, refusal.Code, refusal.Hint, nil); err != nil {
		t.Fatal(err)
	}
	requireRetiredDeny(t, f, "soft-call", 1, 1024)
	trace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := providerbridge.ReplayTranscript(f.providerJournal, providerbridge.RecoveryPins{Config: f.config, Model: f.config.Route.Resolved().Model, Policy: f.policy, RequireHostCallReservations: true}, capturedCallProviderReplayVerifier{prefixCount: uint64(len(trace.Records))})
	if err != nil || projection.DecisionCount != 0 || projection.ResultCount != 1 {
		t.Fatalf("precondition refusal counted as a gate decision: %+v %v", projection, err)
	}
	research := f.config
	research.ProductSoftRefusals = false
	if _, err := providerbridge.ReplayTranscript(f.providerJournal, providerbridge.RecoveryPins{Config: research, Model: research.Route.Resolved().Model, Policy: f.policy, RequireHostCallReservations: true}, capturedCallProviderReplayVerifier{prefixCount: uint64(len(trace.Records))}); err == nil {
		t.Fatal("research replay accepted a product-only refusal record")
	}
	checkpoint, _ := f.ledger.Snapshot()
	digest, err := activeUnexecutedCaptureDigest(f.providerJournal, "soft-call")
	if err != nil || checkpoint.Pins.HostCallReservation.CapturedCall.RetirementDigest != digest {
		t.Fatal("retirement does not commit the refusal record")
	}
	if _, err := activeDeniedCaptureDigest(f.providerJournal, "soft-call"); err == nil {
		t.Fatal("soft refusal accepted as gate DENY")
	}
	requireFinalAnswer(t, f, "Done.", 2, 2048)
	if pins := f.authority.currentLedgerPins(); pins.EffectReservation != nil && pins.EffectReservation.Disposition != workflowrecovery.EffectReservationTerminal {
		t.Fatal("retired call gained an effect reservation")
	}
	reopenLedgerInFreshProcess(t, f)
}

func TestProductTurnSoftRefusalCanPrecedeAnAuthorizedEffect(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	script := productDriverTurnOne(false)
	script.turns = append([]providerbridge.ScriptTurn{{ResponseID: "soft-response", CallID: "soft-call", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)}}, script.turns...)
	receipt, turn, err := f.run(t, f.submit(t, "soft-then-effect", productDriverPromptA), script)
	if err != nil || turn.Outcome != productsession.OutcomePublished || !f.notesPublished(t) || receipt.Usage.HostCalls != 4 {
		t.Fatalf("effect after refusal: %+v %v", turn, err)
	}
	f.followUp(t, "after-effect", productsession.OutcomePublished)
}

func TestProductTurnSoftRefusalCanPrecedeBashInTheSameTurn(t *testing.T) {
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{
		{ResponseID: "soft-response", CallID: "soft-call", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)},
		bashCall("create", `printf 'inside\n' > notes/a.txt`), scopeDriverFinal("t1", scopeDriverAnswer),
	}}
	receipt, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
	if err != nil || turn.Outcome != productsession.OutcomePublished || receipt.CommandJournalCount != 1 || receipt.Usage.HostCalls != 4 || fileContent(t, filepath.Join(f.projectDir, "notes"), "a.txt") != "inside\n" {
		t.Fatalf("Bash after refusal: turn=%+v receipt=%+v err=%v", turn, receipt, err)
	}
	encoded, err := json.Marshal(script.histories[1])
	if err != nil || !strings.Contains(string(encoded), "read_missing") {
		t.Fatalf("Bash request lost the refusal result: %s %v", encoded, err)
	}
	if _, next, err := f.run(t, f.submit(t, "turn-2", "Read the note."), &productDriverScript{turns: []providerbridge.ScriptTurn{scopeDriverFinal("t2", "Done.")}}); err != nil || next.Outcome != productsession.OutcomeNoDiff {
		t.Fatalf("refusal and Bash predecessor failed replay: %+v %v", next, err)
	}
}

func TestProductTurnSoftRefusalAfterAnEffectBindsTheAdvancedTip(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	script := productDriverTurnOne(false)
	script.turns = []providerbridge.ScriptTurn{script.turns[0],
		{ResponseID: "soft-response", CallID: "soft-call", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)},
		script.turns[1]}
	receipt, turn, err := f.run(t, f.submit(t, "effect-then-soft", productDriverPromptA), script)
	if err != nil || turn.Outcome != productsession.OutcomePublished || !f.notesPublished(t) {
		t.Fatalf("refusal after an effect: %+v %v", turn, err)
	}
	paths, err := productTurnPathsFromLink(*turn.Link)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := openRetainedProductTurn(paths, f.env.storeOptions(turn), f.env.OpenCommandHistory)
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	bundle, err := retained.Store.EvidenceBundle()
	if err != nil || len(bundle.Generations) != 2 {
		t.Fatalf("want baseline and one effect generation: %d %v", len(bundle.Generations), err)
	}
	trace, err := retained.ProviderJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	refusals := 0
	for _, record := range trace.Records {
		if record.Event.Kind != providerbridge.SoftRefusalEvent {
			continue
		}
		var evidence providerbridge.SoftRefusalEvidence
		if err := json.Unmarshal(record.Event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		tip := bundle.Generations[1]
		if evidence.Code != "read_missing" || evidence.Generation != tip.ID || evidence.TreeDigest != tip.TreeDigest || evidence.TreeDigest == bundle.Baseline.TreeDigest {
			t.Fatalf("refusal is not bound to the advanced tip: %+v", evidence)
		}
		refusals++
	}
	if refusals != 1 {
		t.Fatalf("refusal count %d", refusals)
	}
	if err := retained.Close(); err != nil {
		t.Fatal(err)
	}
	host, tokens := productDriverExpectedUsage(t, paths.ProviderJournalPath, 1)
	if receipt.Usage.HostCalls != host || receipt.Usage.Tokens != tokens {
		t.Fatalf("refusal charged an effect: %+v want %d/%d", receipt.Usage, host, tokens)
	}
	f.followUp(t, "after-effect-soft", productsession.OutcomePublished)
}
