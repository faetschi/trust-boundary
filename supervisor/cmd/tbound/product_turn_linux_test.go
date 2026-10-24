//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	productDeniedCallID = "product-call-write"
	productFinalAnswer  = "The task file says: vertical fixture."
)

// retainedProductPredecessor is what a fresh process has of a finished turn: retained paths, host
// pins, the receipt and the boundary the next turn imports.
type retainedProductPredecessor struct {
	paths    productTurnPaths
	options  sessionrepo.Options
	pins     GovernedReplayPins
	receipt  productTurnReceipt
	boundary providerbridge.TurnBoundary
}

// runProductPredecessorTurn completes one governed turn (an ALLOWed read with its result, a DENIED
// write whose result carries the static hint, a final answer with no tool call), records what a fresh
// process needs, and closes every fixture handle. It runs without a workflow ledger because a
// ledger-backed conversation cannot yet checkpoint a released DENY: workflowrecovery retires an
// available captured call only when its conversation ends.
func runProductPredecessorTurn(t *testing.T) retainedProductPredecessor {
	t.Helper()
	f := prepareWorkflowVertical(t, workflowVerticalOptions{withoutLedger: true, rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectDeny},
	}})
	f.startConversation(t, func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		return providerbridge.NewDevelopmentOfflineConversation(config, journal, []providerbridge.DevelopmentProviderTurn{
			{ResponseID: "product-response-read", CallID: "product-call-read", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
			{ResponseID: "product-response-write", CallID: productDeniedCallID, Tool: "write",
				Arguments: json.RawMessage(`{"path":"task.txt","content":"changed\n"}`)},
			{ResponseID: "product-response-final", Text: productFinalAnswer},
		})
	})
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []gate.Verdict{gate.Allow, gate.Deny} {
		turn, err := f.conversation.NextTurn(f.ctx)
		if err != nil || turn.ToolCall == nil {
			t.Fatalf("provider tool turn: turn=%+v err=%v", turn, err)
		}
		proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
			Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
		matched, err := f.conversation.Correlate(f.ctx, proposal)
		if err != nil || !matched.Accepted {
			t.Fatalf("correlate %s: %+v %v", proposal.ToolCallID, matched, err)
		}
		decision := gate.Evaluate(proposal, matched, f.policy)
		if decision.Verdict != want {
			t.Fatalf("gate verdict for %s = %+v, want %s", proposal.ToolCallID, decision, want)
		}
		if err := f.conversation.RecordDecision(f.ctx, proposal, decision); err != nil {
			t.Fatal(err)
		}
		if want == gate.Allow {
			if _, err := executor.Execute(f.ctx, proposal, decision); err != nil {
				t.Fatalf("execute the allowed read: %v", err)
			}
		}
	}
	if turn, err := f.conversation.NextTurn(f.ctx); err != nil || turn.ToolCall != nil || turn.AssistantText != productFinalAnswer {
		t.Fatalf("final provider answer: turn=%+v err=%v", turn, err)
	}

	bundle, err := f.store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	providerTrace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	storeTrace, err := f.storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	config := f.config
	route := config.Route.Resolved()
	retained := retainedProductPredecessor{
		paths: productTurnPaths{StoreRoot: f.storePath, StoreJournalPath: f.storeJournalPath, ProviderJournalPath: f.providerJournalPath},
		options: sessionrepo.Options{PolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
			Limits: workspace.DefaultLimits(), XattrVisibility: workspace.XattrVisibilityAttestation{
				ProfileDigest: governedReplayXattrPolicy, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}},
		pins: GovernedReplayPins{
			Provider: providerbridge.RecoveryPins{Config: config, Model: route.Model, Policy: f.policy},
			Profile: broker.Profile{ID: config.ProfileID, Model: route.Model, Route: config.Route,
				Messages:     providerbridge.InitialPromptMessages(route, config.SystemPrompt, config.DeveloperPrompt),
				ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
				ResponseIDIssuer: governedResponseIssuer, Generation: config.InitialGenerationID},
			Baseline: bundle.Baseline, StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
		},
		receipt: productTurnReceipt{SchemaVersion: productTurnReceiptSchema,
			SessionID: "psess-" + strings.Repeat("ab", 16), Ordinal: 1, WorkflowID: config.WorkflowID, RunID: "vertical-run",
			ConversationID: config.ConversationID, WorkerKind: productsession.WorkerHeadlessFixture, WorkerInstance: "fixture-1",
			ProviderJournalHead: auditHeadHash(providerTrace.Records), ProviderJournalCount: uint64(len(providerTrace.Records)),
			StoreJournalHead: auditHeadHash(storeTrace.Records), StoreJournalCount: uint64(len(storeTrace.Records)),
			BaselineDigest:       "sha256:" + bareStoreTreeDigest(bundle.Baseline.TreeDigest),
			ResultBaselineDigest: "sha256:" + bareStoreTreeDigest(bundle.Baseline.TreeDigest),
			Outcome:              productsession.OutcomeNoDiff, Settlement: productsession.SettlementHeadlessClosed, Usage: productsession.Budget{HostCalls: 4, Tokens: 3072, DurationSeconds: 1},
			ToolSequence: []string{"read", "write"},
			Denials:      []productTurnDenial{{ToolCallID: productDeniedCallID, Tool: "write", ReasonCode: "policy_rule_deny"}}},
	}
	retained.receipt.TurnID = retained.receipt.SessionID + "/turn-1"
	encoded, err := encodeProductTurnReceipt(retained.receipt)
	if err != nil {
		t.Fatal(err)
	}
	retained.boundary = providerbridge.TurnBoundary{PredecessorReceiptDigest: productsession.ReceiptDigest(encoded),
		Outcome: productsession.OutcomeNoDiff, ProjectBaselineDigest: bundle.Baseline.TreeDigest}
	f.close()
	return retained
}

// replayRetainedPredecessor reopens the retained turn as a fresh process does and replays its history.
func replayRetainedPredecessor(r retainedProductPredecessor) (providerbridge.PredecessorContext, GovernedTranscriptProjection, error) {
	turn, err := openRetainedProductTurn(r.paths, r.options)
	if err != nil {
		return providerbridge.PredecessorContext{}, GovernedTranscriptProjection{}, err
	}
	predecessor, projection, err := ReplayGovernedPredecessorContext(turn.ProviderJournal, turn.StoreJournal, turn.Store, r.pins, r.boundary)
	return predecessor, projection, errors.Join(err, turn.Close())
}

// productRecordingSource answers with one final text turn and keeps every request history it saw.
type productRecordingSource struct {
	mu        sync.Mutex
	histories [][]providerbridge.ScriptMessage
}

func (s *productRecordingSource) Next(history []providerbridge.ScriptMessage) (providerbridge.ScriptTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.histories = append(s.histories, append([]providerbridge.ScriptMessage(nil), history...))
	if len(s.histories) > 1 {
		return providerbridge.ScriptTurn{}, providerbridge.ErrScriptExhausted
	}
	return providerbridge.ScriptTurn{ResponseID: "product-next-response-final", Text: "Second turn answer."}, nil
}

type productWireMessage struct {
	Role       string  `json:"role"`
	Content    *string `json:"content"`
	ToolCallID string  `json:"tool_call_id"`
}

// firstProviderRequestMessages decodes the exact first request body journaled for a conversation.
func firstProviderRequestMessages(t *testing.T, journal *audit.Journal) []productWireMessage {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_exchange" {
			continue
		}
		var exchange struct {
			Request struct {
				Body []byte `json:"body"`
			} `json:"request"`
		}
		var body struct {
			Messages []productWireMessage `json:"messages"`
		}
		if err := json.Unmarshal(record.Event.Data, &exchange); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(exchange.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		return body.Messages
	}
	t.Fatal("provider journal has no exchange")
	return nil
}

// retainedTurnFiles maps every retained file and directory of a turn to its bytes.
func retainedTurnFiles(t *testing.T, paths productTurnPaths) map[string]string {
	t.Helper()
	files := make(map[string]string)
	walk := func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			files[path] = "directory"
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	}
	for _, root := range []string{paths.StoreRoot, paths.StoreJournalPath, paths.ProviderJournalPath} {
		if err := filepath.WalkDir(root, walk); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestProductTurnPredecessorFromRetainedEvidenceReachesTheNextTurn(t *testing.T) {
	retained := runProductPredecessorTurn(t)
	retainedFiles := retainedTurnFiles(t, retained.paths)

	turn, err := openRetainedProductTurn(retained.paths, retained.options)
	if err != nil {
		t.Fatalf("reopen the retained turn: %v", err)
	}
	if err := verifyProductTurnReceiptJournals(retained.receipt, turn.ProviderJournal, turn.StoreJournal); err != nil {
		t.Fatalf("receipt does not match the retained journals: %v", err)
	}
	for name, mutate := range map[string]func(*productTurnReceipt){
		"provider count": func(r *productTurnReceipt) { r.ProviderJournalCount-- },
		"provider head":  func(r *productTurnReceipt) { r.ProviderJournalHead = strings.Repeat("0", 64) },
		"Store count":    func(r *productTurnReceipt) { r.StoreJournalCount++ },
		"Store head":     func(r *productTurnReceipt) { r.StoreJournalHead = r.ProviderJournalHead },
	} {
		changed := retained.receipt
		mutate(&changed)
		if err := verifyProductTurnReceiptJournals(changed, turn.ProviderJournal, turn.StoreJournal); !errors.Is(err, errProductTurnEvidence) {
			t.Errorf("receipt with a different %s was accepted: %v", name, err)
		}
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}

	predecessor, projection, err := replayRetainedPredecessor(retained)
	if err != nil {
		t.Fatalf("replay the retained predecessor: %v", err)
	}
	if projection.VerificationScope != "store_crosslinked" || projection.Provider.VerificationScope != providerbridge.ProjectionScopeStoreCrosslink ||
		projection.Provider.Disposition != providerbridge.DispositionAwaitingHostPrompt || projection.Provider.DecisionCount != 2 ||
		projection.Provider.ResultCount != 2 || projection.StoreOperationCount != 1 || !VerifyGovernedTranscriptProjectionCommitment(projection) {
		t.Fatalf("predecessor replay is not the Store-crosslinked completed turn: %+v", projection)
	}
	if !validDigestText(predecessor.Digest()) || predecessor.PredecessorReceiptDigest() != retained.boundary.PredecessorReceiptDigest {
		t.Fatalf("predecessor digest=%q receipt=%q", predecessor.Digest(), predecessor.PredecessorReceiptDigest())
	}
	again, _, err := replayRetainedPredecessor(retained)
	if err != nil || again.Digest() != predecessor.Digest() {
		t.Fatalf("a second reopen did not replay the same prefix: %q %v", again.Digest(), err)
	}
	if after := retainedTurnFiles(t, retained.paths); !reflect.DeepEqual(after, retainedFiles) {
		t.Fatal("reopening and replaying the retained turn changed its files")
	}

	source := &productRecordingSource{}
	next := prepareWorkflowVertical(t, workflowVerticalOptions{predecessor: predecessor})
	next.startConversation(t, func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		return providerbridge.NewScriptedConversation(config, journal, source)
	})
	if turn, err := next.conversation.NextTurn(next.ctx); err != nil || turn.ToolCall != nil || turn.AssistantText != "Second turn answer." {
		t.Fatalf("second turn answer: turn=%+v err=%v", turn, err)
	}

	wantBoundary := `[TBound host record] The previous user turn ended with outcome "no_diff": it left the project files unchanged. Current project baseline: ` +
		retained.boundary.ProjectBaselineDigest + `. Tool results above show the workspace as it was when each tool ran.`
	if len(source.histories) != 1 {
		t.Fatalf("script source saw %d requests, want 1", len(source.histories))
	}
	seen := source.histories[0]
	if len(seen) < 3 || seen[len(seen)-2].Role != "user" || seen[len(seen)-2].Content != wantBoundary ||
		seen[len(seen)-1].Role != "user" || seen[len(seen)-1].Content != workflowVerticalPrompt {
		t.Fatalf("first request does not end with the boundary and the new prompt: %+v", seen)
	}
	hint := providerbridge.HintCandidates("policy_rule_deny")["0"]
	if hint == "" {
		t.Fatal("no static hint for policy_rule_deny")
	}
	var deniedResult string
	for _, message := range firstProviderRequestMessages(t, next.providerJournal) {
		if message.Role == "tool" && message.ToolCallID == productDeniedCallID && message.Content != nil {
			deniedResult = *message.Content
		}
	}
	if !strings.Contains(deniedResult, `"DENY"`) || !strings.HasSuffix(deniedResult, "\nhint: "+hint) {
		t.Fatalf("imported denied result does not carry its hint: %q", deniedResult)
	}

	nextBundle, err := next.store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	pins := GovernedReplayPins{Provider: providerbridge.RecoveryPins{Config: next.config, Model: retained.pins.Provider.Model,
		Policy: next.policy, RequireHostCallReservations: true, ImportedPrefixDigest: predecessor.Digest()},
		Profile: retained.pins.Profile, Baseline: nextBundle.Baseline,
		StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy}
	pins.Profile.Generation = next.config.InitialGenerationID
	if _, err := ReplayGovernedTranscript(next.providerJournal, next.storeJournal, next.store, pins); err != nil {
		t.Fatalf("second turn does not replay under its imported prefix pin: %v", err)
	}
	pins.Provider.ImportedPrefixDigest = ""
	if _, err := ReplayGovernedTranscript(next.providerJournal, next.storeJournal, next.store, pins); err == nil {
		t.Fatal("second turn replayed without its imported prefix pin")
	}
}

func TestProductTurnPredecessorRefusesAModifiedRetainedJournal(t *testing.T) {
	for name, path := range map[string]func(productTurnPaths) string{
		"Store journal":    func(p productTurnPaths) string { return p.StoreJournalPath },
		"provider journal": func(p productTurnPaths) string { return p.ProviderJournalPath },
	} {
		t.Run(name, func(t *testing.T) {
			retained := runProductPredecessorTurn(t)
			if _, _, err := replayRetainedPredecessor(retained); err != nil {
				t.Fatalf("untouched retained turn did not replay: %v", err)
			}
			target := path(retained.paths)
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			data[len(data)/2] ^= 0x01
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := replayRetainedPredecessor(retained); err == nil {
				t.Fatalf("a flipped byte in the retained %s still replayed", name)
			}
		})
	}
}

func TestProductProviderConversationRequiresTheCheckpointedPredecessor(t *testing.T) {
	predecessor := func(label string) providerbridge.PredecessorContext {
		context, err := providerbridge.ExtendPredecessorContext(providerbridge.PredecessorContext{}, providerbridge.TurnBoundary{
			PredecessorReceiptDigest: digestBytes([]byte("receipt " + label)), Outcome: productsession.OutcomeNoDiff,
			ProjectBaselineDigest: delta.TreeDigestProfile + bareWorkflowDigest(digestBytes([]byte("tree")))})
		if err != nil {
			t.Fatal(err)
		}
		return context
	}
	for name, c := range map[string]struct {
		pinned, configured providerbridge.PredecessorContext
	}{
		"pinned but not configured": {pinned: predecessor("a")},
		"configured but not pinned": {configured: predecessor("a")},
		"configured differs":        {pinned: predecessor("a"), configured: predecessor("b")},
	} {
		t.Run(name, func(t *testing.T) {
			f := prepareWorkflowVertical(t, workflowVerticalOptions{predecessor: c.pinned})
			config := f.config
			config.Predecessor = c.configured
			_, err := f.authority.newProviderConversation(config, func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error) {
				t.Fatal("a conversation was constructed against disagreeing predecessor pins")
				return nil, nil
			})
			if err == nil {
				t.Fatal("newProviderConversation accepted disagreeing predecessor pins")
			}
		})
	}
}

func TestProductTurnReceiptIsStrict(t *testing.T) {
	receipt := productTurnReceipt{SchemaVersion: productTurnReceiptSchema, SessionID: "psess-1", TurnID: "psess-1/turn-2",
		Ordinal: 2, WorkflowID: "w", RunID: "r", ConversationID: "c", WorkerKind: productsession.WorkerHeadlessFixture, WorkerInstance: "i",
		ProviderJournalHead: strings.Repeat("a", 64), ProviderJournalCount: 3, StoreJournalHead: strings.Repeat("b", 64), StoreJournalCount: 4,
		ImportedPrefixDigest: digestBytes([]byte("prefix")), PredecessorReceiptDigest: digestBytes([]byte("receipt")),
		BaselineDigest: digestBytes([]byte("base")), ResultBaselineDigest: digestBytes([]byte("base")),
		Outcome: productsession.OutcomeNoDiff, Settlement: productsession.SettlementHeadlessClosed, ToolSequence: []string{}, Denials: []productTurnDenial{}}
	encoded, err := encodeProductTurnReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeProductTurnReceipt(encoded)
	if err != nil || decoded.TurnID != receipt.TurnID || decoded.ImportedPrefixDigest != receipt.ImportedPrefixDigest {
		t.Fatalf("receipt did not round-trip: %+v %v", decoded, err)
	}
	for name, raw := range map[string][]byte{
		"unknown field":  append(encoded[:len(encoded)-1:len(encoded)-1], []byte(`,"extra":1}`)...),
		"trailing data":  append(append([]byte(nil), encoded...), []byte(` {}`)...),
		"duplicate key":  append([]byte(`{"ordinal":2,`), encoded[1:]...),
		"over the bound": append(append([]byte(nil), encoded...), make([]byte, productsession.MaxReceiptBytes)...),
	} {
		if _, err := decodeProductTurnReceipt(raw); err == nil {
			t.Errorf("receipt with %s was accepted", name)
		}
	}
	for name, mutate := range map[string]func(*productTurnReceipt){
		"other worker":         func(r *productTurnReceipt) { r.WorkerKind = "pi-process" },
		"turn ID mismatch":     func(r *productTurnReceipt) { r.TurnID = "psess-1/turn-3" },
		"half predecessor":     func(r *productTurnReceipt) { r.PredecessorReceiptDigest = "" },
		"no journal head":      func(r *productTurnReceipt) { r.StoreJournalHead = "" },
		"unknown outcome":      func(r *productTurnReceipt) { r.Outcome = "unknown" },
		"missing denials list": func(r *productTurnReceipt) { r.Denials = nil },
	} {
		changed := receipt
		mutate(&changed)
		if _, err := encodeProductTurnReceipt(changed); err == nil {
			t.Errorf("receipt with %s was encoded", name)
		}
	}
}
