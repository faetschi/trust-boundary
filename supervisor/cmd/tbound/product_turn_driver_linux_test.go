//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

const (
	productDriverProject  = "project-alpha"
	productDriverProfile  = "provider-profile-1"
	productDriverPromptA  = "Write notes.txt with what you learned."
	productDriverPromptB  = "Read notes.txt back to me."
	productDriverAnswerA  = "I wrote notes.txt."
	productDriverAnswerB  = "notes.txt says: notes from turn one."
	productDriverNotes    = "notes from turn one\n"
	productDriverEditCall = "t1-call-edit"
	productDriverReadCall = "t2-call-read"
)

// productDriverScript is the scripted provider of one turn: it answers its turns in order, records
// every request history, and runs check on the first request.
type productDriverScript struct {
	mu        sync.Mutex
	turns     []providerbridge.ScriptTurn
	check     func([]providerbridge.ScriptMessage) error
	histories [][]providerbridge.ScriptMessage
	// onRequest runs before the response to request n (from 1), inside the running turn.
	onRequest func(n int)
}

func (s *productDriverScript) Next(history []providerbridge.ScriptMessage) (providerbridge.ScriptTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.histories = append(s.histories, append([]providerbridge.ScriptMessage(nil), history...))
	if s.onRequest != nil {
		s.onRequest(len(s.histories))
	}
	if len(s.histories) == 1 && s.check != nil {
		if err := s.check(history); err != nil {
			return providerbridge.ScriptTurn{}, err
		}
	}
	if len(s.histories) > len(s.turns) {
		return providerbridge.ScriptTurn{}, providerbridge.ErrScriptExhausted
	}
	return s.turns[len(s.histories)-1], nil
}

func (s *productDriverScript) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.histories)
}

// productDriverTrust is the test publication trust: transitions and the terminal origin are verified by
// the turn's real authority; admission, quiescence and production prerequisites are fixtures.
type productDriverTrust struct{ authority *SessionOperationAuthority }

func (productDriverTrust) CheckAdmission(context.Context) error { return nil }
func (productDriverTrust) CheckPublicationPrerequisites(context.Context, workflow.EvidenceRequest) error {
	return nil
}
func (productDriverTrust) AttestQuiescent(_ context.Context, live *os.File, baseline, tip *sessionrepo.Generation) error {
	if live == nil || baseline == nil || tip == nil || tip.ID() == baseline.ID() {
		return errors.New("no changed tip to attest")
	}
	return nil
}
func (t productDriverTrust) VerifyTransition(_ context.Context, decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	return t.authority.VerifyTransition(decision, binding)
}
func (t productDriverTrust) VerifyOrigin(_ context.Context, bundle sessionrepo.EvidenceBundle, transition delta.Transition) error {
	return t.authority.VerifyOrigin(bundle, transition)
}
func (t productDriverTrust) ResolveTerminalLeaseOrigin(ctx context.Context, bundle sessionrepo.EvidenceBundle, terminal delta.Transition) (delta.OperationIdentity, error) {
	return t.authority.ResolveTerminalLeaseOrigin(ctx, bundle, terminal)
}

type productDriverFixture struct {
	env        *productTurnEnv
	root       string
	principal  productsession.Principal
	projectDir string
	session    productsession.Session
	script     *productDriverScript
}

// newProductDriverFixture registers one project whose policy allows read and write and denies edit,
// opens a real catalog, and creates a session in mode over a project holding task.txt.
func newProductDriverFixture(t *testing.T, mode string) *productDriverFixture {
	t.Helper()
	return newScopedProductDriverFixture(t, mode, nil, false, nil)
}

// newScopedProductDriverFixture is newProductDriverFixture for a project registered with scope (nil
// registers none). seed adds files to the project before the first measurement, and readOnly creates
// the session read-only.
func newScopedProductDriverFixture(t *testing.T, mode string, scope *productsession.ProductScope, readOnly bool,
	seed func(projectDir string)) *productDriverFixture {
	t.Helper()
	return newBashProductDriverFixture(t, mode, scope, readOnly, nil, seed)
}

// newBashProductDriverFixture is newScopedProductDriverFixture for a project whose policy also allows
// Bash (bashPaths non-nil), registered with those exact output paths, which a product scope ignores.
func newBashProductDriverFixture(t *testing.T, mode string, scope *productsession.ProductScope, readOnly bool, bashPaths []string,
	seed func(projectDir string)) *productDriverFixture {
	t.Helper()
	root := privateGovernedTestRoot(t)
	projectDir, stateRoot := filepath.Join(root, "project"), filepath.Join(root, "state")
	for _, dir := range []string{projectDir, stateRoot} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeProductDriverFile(t, projectDir, "task.txt", "turn fixture\n")
	if seed != nil {
		seed(projectDir)
	}
	// Publication applies only to a managed root holding the effect oracle's fence directory.
	fence := filepath.Join(projectDir, "lost+found")
	if err := os.Mkdir(fence, 0o755); err != nil || os.Chmod(fence, 0o755) != nil {
		t.Fatal(err)
	}
	f := openBashProductDriverFixture(t, root, scope, bashPaths)
	session, replayed, err := f.env.Catalog.CreateSession(f.principal, productsession.CreateSessionRequest{ProjectID: productDriverProject,
		ProviderProfile: productDriverProfile, PublicationMode: mode, Budget: f.env.Registration.Limits.Session, RequestKey: "create-1",
		ReadOnly: readOnly}, f.measure(t))
	if err != nil || replayed {
		t.Fatalf("create session: replayed=%v err=%v", replayed, err)
	}
	f.session = session
	return f
}

// openProductDriverFixture opens the registration, catalog and env over the project and state
// directories under root, as a fresh process does after a restart.
func openProductDriverFixture(t *testing.T, root string) *productDriverFixture {
	t.Helper()
	return openProductDriverFixtureWithScope(t, root, nil)
}

func openProductDriverFixtureWithScope(t *testing.T, root string, scope *productsession.ProductScope) *productDriverFixture {
	return openBashProductDriverFixture(t, root, scope, nil)
}

// openBashProductDriverFixture is openProductDriverFixtureWithScope for a policy that also allows Bash
// when bashPaths is non-nil. The env then builds the development command runner and its history.
func openBashProductDriverFixture(t *testing.T, root string, scope *productsession.ProductScope, bashPaths []string) *productDriverFixture {
	t.Helper()
	projectDir, stateRoot := filepath.Join(root, "project"), filepath.Join(root, "state")
	rules := []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow}, {Tool: "edit", Effect: gate.EffectDeny},
	}
	registeredBash := []string{}
	if bashPaths != nil {
		rules, registeredBash = append(rules, gate.Rule{Tool: "bash", Effect: gate.EffectAllow}), bashPaths
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	projectRoot, err := os.Open(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := recoveryDirectoryIdentityDigest(projectRoot)
	_ = projectRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	f := &productDriverFixture{root: root, principal: productsession.Principal(os.Getuid()), projectDir: projectDir}
	unsigned := productsession.Registration{SchemaVersion: productsession.RegistrationSchema, RegistrationID: "reg-driver",
		ServiceIdentity: "tbound-product-test", AllowedPrincipals: []productsession.Principal{f.principal},
		Projects: []productsession.Project{{ProjectID: productDriverProject, RootIdentityDigest: sha256DigestFromBarePin(identity),
			PolicyDigest: sha256DigestFromBarePin(barePolicyDigest(policy.Digest())), BashOutputPaths: registeredBash,
			PublicationModes: []string{productsession.PublicationReview, productsession.PublicationAuto}, Scope: scope}},
		ProviderProfiles: []string{productDriverProfile},
		Limits: productsession.Limits{Turn: productsession.Budget{HostCalls: 20, Tokens: 20 * 1024, DurationSeconds: 600},
			Session: productsession.Budget{HostCalls: 60, Tokens: 60 * 1024, DurationSeconds: 1800}, MaxTurnsPerSession: 10,
			ReviewTimeoutSeconds: 600},
		GitDigest: digestBytes([]byte("git")), HostIdentityDigest: digestBytes([]byte("host")), RuntimeDigest: digestBytes([]byte("runtime")), ImageDigest: digestBytes([]byte("image")),
		ExtensionDigest: digestBytes([]byte("extension"))}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	data, signature, err := productsession.SignRegistration(unsigned, key)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := productsession.VerifyRegistration(data, signature, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := productsession.Open(filepath.Join(stateRoot, "catalog.jsonl"), registration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	f.env = &productTurnEnv{Catalog: catalog, Registration: registration, StateRoot: stateRoot,
		ProjectRoots:      map[string]string{productDriverProject: projectDir},
		GatePolicies:      map[string]gate.Policy{productDriverProject: policy},
		StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
		Xattr:        workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted},
		SystemPrompt: governedReplaySystem, DeveloperPrompt: governedReplayDeveloper,
		PinGit: func() (privategit.PinnedGit, sessionlaunch.GitProfile, error) {
			git, profile := pinGovernedTestGit(t)
			return git, profile, nil
		},
		NewConversation: func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
			return providerbridge.NewScriptedConversation(config, journal, f.script)
		},
		PublicationTrust: func(authority *SessionOperationAuthority) workflow.Trust {
			return productDriverTrust{authority: authority}
		},
		MaxWorkerSteps: 8,
	}
	if bashPaths != nil {
		f.env.NewCommandRunner = func(c productTurnCommandContext) (productTurnCommandRunner, error) {
			return productDevCommandRunner{devFixtureRunner: newDevelopmentFixtureCommandRunner(), journal: c.Journal}, nil
		}
		f.env.OpenCommandHistory = openProductDevCommandHistory
	}
	t.Cleanup(func() { _ = f.env.Close() })
	return f
}

func writeProductDriverFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *productDriverFixture) measure(t *testing.T) string {
	t.Helper()
	baseline, err := f.env.MeasureProjectBaseline(productDriverProject)
	if err != nil {
		t.Fatal(err)
	}
	return baseline
}

func (f *productDriverFixture) currentSession(t *testing.T) productsession.Session {
	t.Helper()
	session, err := f.env.Catalog.Session(f.principal, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func (f *productDriverFixture) submit(t *testing.T, key, prompt string) productsession.Admission {
	t.Helper()
	admission, err := f.env.Catalog.SubmitTurn(f.principal, productsession.SubmitTurnRequest{SessionID: f.session.ID,
		ExpectedRevision: f.currentSession(t).Revision, RequestKey: key, Prompt: prompt}, f.measure(t))
	if err != nil || admission.Replayed {
		t.Fatalf("submit %s: replayed=%v err=%v", key, admission.Replayed, err)
	}
	return admission
}

// run runs one admitted turn against script and returns its receipt and the turn's catalog state.
func (f *productDriverFixture) run(t *testing.T, admission productsession.Admission, script *productDriverScript) (productTurnReceipt, productsession.Turn, error) {
	t.Helper()
	f.script = script
	receipt, err := f.env.RunTurn(context.Background(), admission)
	turn, ok := f.env.Catalog.Turn(admission.Turn.ID)
	if !ok {
		t.Fatalf("turn %s vanished from the catalog", admission.Turn.ID)
	}
	return receipt, turn, err
}

func productDriverTurnOne(withDenial bool) *productDriverScript {
	turns := []providerbridge.ScriptTurn{{ResponseID: "t1-response-write", CallID: "t1-call-write", Tool: "write",
		Arguments: json.RawMessage(`{"path":"notes.txt","content":"notes from turn one\n"}`)}}
	if withDenial {
		turns = append(turns, providerbridge.ScriptTurn{ResponseID: "t1-response-edit", CallID: productDriverEditCall, Tool: "edit",
			Arguments: json.RawMessage(`{"path":"task.txt","edits":[{"oldText":"turn fixture\n","newText":"changed\n"}]}`)})
	}
	return &productDriverScript{turns: append(turns, providerbridge.ScriptTurn{ResponseID: "t1-response-final", Text: productDriverAnswerA})}
}

// productDriverExpectedUsage is the ledger charge model: every provider exchange is one host call and
// one 1024-token reservation, and every executed tool is one host call.
func productDriverExpectedUsage(t *testing.T, providerJournalPath string, executed int) (int64, int64) {
	t.Helper()
	journal, err := openExistingRecoveryJournal(providerJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	exchanges := 0
	for _, record := range trace.Records {
		if record.Event.Kind == "provider_exchange" {
			exchanges++
		}
	}
	return int64(exchanges + executed), int64(exchanges) * 1024
}

func messageIndex(history []providerbridge.ScriptMessage, from int, match func(providerbridge.ScriptMessage) bool) int {
	for index := from; index < len(history); index++ {
		if match(history[index]) {
			return index
		}
	}
	return -1
}

// runProductDriverTwoTurns runs turn 1 (write notes.txt, optionally a denied edit, final answer) and
// turn 2 (reads notes.txt) in an auto-publish session, each from retained evidence on disk.
func runProductDriverTwoTurns(t *testing.T, withDenial bool) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	baselineBefore := f.measure(t)
	first := f.submit(t, "turn-1", productDriverPromptA)
	again, err := f.env.Catalog.SubmitTurn(f.principal, productsession.SubmitTurnRequest{SessionID: f.session.ID,
		ExpectedRevision: 0, RequestKey: "turn-1", Prompt: productDriverPromptA}, baselineBefore)
	if err != nil || !again.Replayed || again.Turn.ID != first.Turn.ID || len(f.currentSession(t).Turns) != 1 {
		t.Fatalf("duplicate submit did not replay the admission: %+v %v", again, err)
	}
	if _, err := f.env.Catalog.SubmitTurn(f.principal, productsession.SubmitTurnRequest{SessionID: f.session.ID,
		ExpectedRevision: 0, RequestKey: "turn-1", Prompt: productDriverPromptB}, baselineBefore); !errors.Is(err, productsession.ErrCode(productsession.CodeRequestConflict)) {
		t.Fatalf("same key with another prompt: %v", err)
	}

	receipt1, turn1, err := f.run(t, first, productDriverTurnOne(withDenial))
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	notes, err := os.ReadFile(filepath.Join(f.projectDir, "notes.txt"))
	if err != nil || string(notes) != productDriverNotes {
		t.Fatalf("turn 1 did not publish notes.txt: %q %v", notes, err)
	}
	wantTools, wantDenials := []string{"write"}, []productTurnDenial{}
	if withDenial {
		wantTools = append(wantTools, "edit")
		wantDenials = append(wantDenials, productTurnDenial{ToolCallID: productDriverEditCall, Tool: "edit", ReasonCode: "policy_rule_deny"})
	}
	if turn1.State != productsession.StateTerminal || turn1.Outcome != productsession.OutcomePublished || !turn1.ContextEligible ||
		turn1.ResultBaselineDigest == baselineBefore || turn1.ResultBaselineDigest != f.measure(t) ||
		receipt1.PublicationTransactionID == "" || receipt1.ImportedPrefixDigest != "" || receipt1.PredecessorReceiptDigest != "" ||
		fmt.Sprint(receipt1.ToolSequence) != fmt.Sprint(wantTools) || fmt.Sprint(receipt1.Denials) != fmt.Sprint(wantDenials) {
		t.Fatalf("turn 1 settlement: turn=%+v receipt=%+v", turn1, receipt1)
	}
	hostCalls, tokens := productDriverExpectedUsage(t, turn1.Link.ProviderJournalPath, 1)
	if receipt1.Usage != turn1.Usage || receipt1.Usage.HostCalls != hostCalls || receipt1.Usage.Tokens != tokens {
		t.Fatalf("turn 1 usage %+v, catalog %+v, ledger model host calls %d tokens %d", receipt1.Usage, turn1.Usage, hostCalls, tokens)
	}

	if _, err := f.env.Catalog.SubmitTurn(f.principal, productsession.SubmitTurnRequest{SessionID: f.session.ID,
		ExpectedRevision: f.currentSession(t).Revision - 1, RequestKey: "turn-2-stale", Prompt: productDriverPromptB}, f.measure(t)); !errors.Is(err, productsession.ErrCode(productsession.CodeStaleRevision)) {
		t.Fatalf("stale revision: %v", err)
	}
	second := f.submit(t, "turn-2", productDriverPromptB)
	hint := providerbridge.HintCandidates("policy_rule_deny")["0"]
	wantBoundary := `[TBound host record] The previous user turn ended with outcome "published": its file changes were applied to the project. Current project baseline: ` +
		delta.TreeDigestProfile + bareWorkflowDigest(turn1.ResultBaselineDigest) + `. Tool results above show the workspace as it was when each tool ran.`
	script2 := &productDriverScript{
		turns: []providerbridge.ScriptTurn{
			{ResponseID: "t2-response-read", CallID: productDriverReadCall, Tool: "read", Arguments: json.RawMessage(`{"path":"notes.txt"}`)},
			{ResponseID: "t2-response-final", Text: productDriverAnswerB},
		},
		check: func(history []providerbridge.ScriptMessage) error {
			at := messageIndex(history, 0, func(m providerbridge.ScriptMessage) bool {
				return m.Role == "user" && m.Content == productDriverPromptA
			})
			if withDenial {
				at = messageIndex(history, at+1, func(m providerbridge.ScriptMessage) bool {
					return m.Role == "tool" && m.ToolCallID == productDriverEditCall
				})
			}
			for _, want := range []providerbridge.ScriptMessage{{Role: "assistant", Content: productDriverAnswerA},
				{Role: "user", Content: wantBoundary}, {Role: "user", Content: productDriverPromptB}} {
				if at < 0 {
					break
				}
				at = messageIndex(history, at+1, func(m providerbridge.ScriptMessage) bool { return m.Role == want.Role && m.Content == want.Content })
			}
			if at != len(history)-1 {
				return fmt.Errorf("first request of turn 2 lacks the ordered predecessor history: %+v", history)
			}
			return nil
		},
	}
	receipt2, turn2, err := f.run(t, second, script2)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if read := script2.histories[1]; read[len(read)-1].ToolCallID != productDriverReadCall ||
		!strings.Contains(read[len(read)-1].Content, strings.TrimSuffix(productDriverNotes, "\n")) {
		t.Fatalf("turn 2 did not read turn 1's notes: %+v", read[len(read)-1])
	}
	if withDenial {
		// The scripted view strips a validated hint; the provider request itself must carry it.
		journal, err := audit.Open(turn2.Link.ProviderJournalPath)
		if err != nil {
			t.Fatal(err)
		}
		var denied string
		for _, message := range firstProviderRequestMessages(t, journal) {
			if message.Role == "tool" && message.ToolCallID == productDriverEditCall && message.Content != nil {
				denied = *message.Content
			}
		}
		_ = journal.Close()
		if hint == "" || !strings.Contains(denied, `"DENY"`) || !strings.HasSuffix(denied, "\nhint: "+hint) {
			t.Fatalf("turn 2 request does not carry turn 1's denial with its hint: %q", denied)
		}
	}
	if turn2.Outcome != productsession.OutcomeNoDiff || turn2.ResultBaselineDigest != turn1.ResultBaselineDigest ||
		receipt2.PredecessorReceiptDigest != turn1.ReceiptDigest || !validDigestText(receipt2.ImportedPrefixDigest) ||
		receipt2.ImportedPrefixDigest != turn2.Link.ImportedPrefixDigest || receipt2.PublicationTransactionID != "" {
		t.Fatalf("turn 2 settlement: turn=%+v receipt=%+v", turn2, receipt2)
	}
	for name, pair := range map[string][2]string{
		"workflow":        {receipt1.WorkflowID, receipt2.WorkflowID},
		"run":             {receipt1.RunID, receipt2.RunID},
		"conversation":    {receipt1.ConversationID, receipt2.ConversationID},
		"runtime session": {turn1.Link.SessionID, turn2.Link.SessionID},
		"worker instance": {receipt1.WorkerInstance, receipt2.WorkerInstance},
	} {
		if pair[0] == "" || pair[0] == pair[1] {
			t.Errorf("turns share their %s identity %q", name, pair[0])
		}
	}
	hostCalls, tokens = productDriverExpectedUsage(t, turn2.Link.ProviderJournalPath, 1)
	if receipt2.Usage != turn2.Usage || receipt2.Usage.HostCalls != hostCalls || receipt2.Usage.Tokens != tokens {
		t.Fatalf("turn 2 usage %+v, catalog %+v, ledger model host calls %d tokens %d", receipt2.Usage, turn2.Usage, hostCalls, tokens)
	}
	session := f.currentSession(t)
	consumed := productsession.Budget{HostCalls: receipt1.Usage.HostCalls + receipt2.Usage.HostCalls,
		Tokens: receipt1.Usage.Tokens + receipt2.Usage.Tokens, DurationSeconds: receipt1.Usage.DurationSeconds + receipt2.Usage.DurationSeconds}
	if session.Consumed != consumed || session.Reserved != (productsession.Budget{}) || session.Blocked {
		t.Fatalf("session budget after two turns: consumed=%+v reserved=%+v blocked=%v, want consumed %+v",
			session.Consumed, session.Reserved, session.Blocked, consumed)
	}
}

// A ledger-backed DENY currently fails at RecordDecision ("advance workflow checkpoint after denied
// provider result"); the workflowrecovery fix makes this pass.
func TestProductTurnDriverCarriesADenialAndHintIntoTheNextTurn(t *testing.T) {
	runProductDriverTwoTurns(t, true)
}

func TestProductTurnDriverRunsTwoTurnsFromRetainedEvidence(t *testing.T) {
	runProductDriverTwoTurns(t, false)
}

func TestProductTurnDriverRefusesTamperedPredecessorEvidence(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	_, turn1, err := f.run(t, f.submit(t, "turn-1", productDriverPromptA), productDriverTurnOne(false))
	if err != nil || turn1.Outcome != productsession.OutcomePublished {
		t.Fatalf("turn 1: %+v %v", turn1, err)
	}
	data, err := os.ReadFile(turn1.Link.ProviderJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0x01
	if err := os.WriteFile(turn1.Link.ProviderJournalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	second := f.submit(t, "turn-2", productDriverPromptB)
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: "t2-response-final", Text: productDriverAnswerB}}}
	_, turn2, err := f.run(t, second, script)
	var refusal *productTurnRefusal
	if !errors.Is(err, productsession.ErrCode(productsession.CodePredecessorEvidenceInvalid)) || !errors.As(err, &refusal) {
		t.Fatalf("tampered predecessor journal: %v", err)
	}
	if script.requests() != 0 || turn2.Link != nil || turn2.State != productsession.StateTerminal ||
		turn2.Outcome != productsession.OutcomeFailed || turn2.Refund != turn2.Reservation || !f.currentSession(t).Blocked {
		t.Fatalf("refused turn: requests=%d turn=%+v", script.requests(), turn2)
	}
}

func TestProductTurnDriverRefusesAStaleBaseline(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	admission := f.submit(t, "turn-1", productDriverPromptA)
	writeProductDriverFile(t, f.projectDir, "task.txt", "changed outside the turn\n")
	script := productDriverTurnOne(false)
	_, turn, err := f.run(t, admission, script)
	if !errors.Is(err, productsession.ErrCode(productsession.CodeStaleBaseline)) || script.requests() != 0 ||
		turn.Link != nil || turn.Outcome != productsession.OutcomeFailed || turn.Refund != turn.Reservation {
		t.Fatalf("stale baseline: turn=%+v err=%v", turn, err)
	}
	if _, err := f.env.Catalog.SubmitTurn(f.principal, productsession.SubmitTurnRequest{SessionID: f.session.ID,
		ExpectedRevision: f.currentSession(t).Revision, RequestKey: "turn-2", Prompt: productDriverPromptB}, f.measure(t)); !errors.Is(err, productsession.ErrCode(productsession.CodeProjectBlocked)) {
		t.Fatalf("submit after a stale baseline: %v", err)
	}
}

// A tool call that leaves the Store with an unresolved effect makes the turn unknown: its reservation
// stays consumed and the session and project stay blocked.
func TestProductTurnDriverRecordsUnresolvedChildWorkAsUnknown(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	admission := f.submit(t, "turn-1", productDriverPromptA)
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{
		{ResponseID: "u-response-read", CallID: "u-call-read", Tool: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)},
		{ResponseID: "u-response-final", Text: productDriverAnswerA},
	}}
	_, turn, err := f.run(t, admission, script)
	session := f.currentSession(t)
	if err == nil || turn.State != productsession.StateUnknown || turn.Refund != (productsession.Budget{}) ||
		!session.Blocked || session.Consumed != turn.Reservation {
		t.Fatalf("unresolved child work: turn=%+v session=%+v err=%v", turn, session, err)
	}
	if _, err := f.env.Catalog.SubmitTurn(f.principal, productsession.SubmitTurnRequest{SessionID: f.session.ID,
		ExpectedRevision: session.Revision, RequestKey: "turn-2", Prompt: productDriverPromptB}, f.measure(t)); !errors.Is(err, productsession.ErrCode(productsession.CodePredecessorUnresolved)) {
		t.Fatalf("submit after an unknown turn: %v", err)
	}
}

// An external writer that changes the project while a turn runs makes the turn unknown, whether the
// turn would have published or found nothing to publish.
func TestProductTurnDriverRecordsAnExternalEditAsUnknown(t *testing.T) {
	for name, script := range map[string]*productDriverScript{
		"no diff": {turns: []providerbridge.ScriptTurn{
			{ResponseID: "x-response-read", CallID: "x-call-read", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
			{ResponseID: "x-response-final", Text: productDriverAnswerA},
		}},
		"published": productDriverTurnOne(false),
	} {
		t.Run(name, func(t *testing.T) {
			f := newProductDriverFixture(t, productsession.PublicationAuto)
			admission := f.submit(t, "turn-1", productDriverPromptA)
			script.onRequest = func(n int) {
				if n == 2 {
					writeProductDriverFile(t, f.projectDir, "outside.txt", "written by another process\n")
				}
			}
			_, turn, err := f.run(t, admission, script)
			if err == nil || turn.State != productsession.StateUnknown || !f.currentSession(t).Blocked {
				t.Fatalf("external edit during the turn: turn=%+v err=%v", turn, err)
			}
		})
	}
}

// An external edit after a successful publication is never adopted as the committed baseline.
func TestProductTurnDriverRecordsAnEditAfterPublicationAsUnknown(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	admission := f.submit(t, "turn-1", productDriverPromptA)
	f.env.publicationFaults = func(point string, _ int) error {
		if point == "after-terminal-checkpoint" {
			writeProductDriverFile(t, f.projectDir, "outside.txt", "written after publication\n")
		}
		return nil
	}
	_, turn, err := f.run(t, admission, productDriverTurnOne(false))
	session := f.currentSession(t)
	if err == nil || turn.State != productsession.StateUnknown || turn.Refund != (productsession.Budget{}) || !session.Blocked {
		t.Fatalf("edit after publication: turn=%+v session=%+v err=%v", turn, session, err)
	}
	if _, err := os.Stat(filepath.Join(f.projectDir, "notes.txt")); err != nil {
		t.Fatalf("the publication itself did not apply: %v", err)
	}
}

func TestProductTurnDriverHoldsAChangedReviewTurn(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationReview)
	baseline := f.measure(t)
	receipt, turn, err := f.run(t, f.submit(t, "turn-1", productDriverPromptA), productDriverTurnOne(false))
	if err != nil || turn.State != productsession.StateAwaitingReview || !validDigestText(turn.CandidateDigest) ||
		receipt.Outcome != "awaiting_review" || receipt.ResultBaselineDigest != baseline || f.measure(t) != baseline {
		t.Fatalf("review turn: turn=%+v receipt=%+v err=%v", turn, receipt, err)
	}
	if _, err := os.Stat(filepath.Join(f.projectDir, "notes.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a review turn changed the project: %v", err)
	}
	if _, err := f.env.Catalog.SubmitTurn(f.principal, productsession.SubmitTurnRequest{SessionID: f.session.ID,
		ExpectedRevision: f.currentSession(t).Revision, RequestKey: "turn-2", Prompt: productDriverPromptB}, baseline); !errors.Is(err, productsession.ErrCode(productsession.CodePendingReview)) {
		t.Fatalf("submit while a turn awaits review: %v", err)
	}
}

// An imported tool call carries no authority: a proposal with turn 1's tool-call ID does not
// correlate in turn 2's composed conversation.
func TestProductTurnDriverImportedCallDoesNotCorrelate(t *testing.T) {
	f := newProductDriverFixture(t, productsession.PublicationAuto)
	if _, turn1, err := f.run(t, f.submit(t, "turn-1", productDriverPromptA), productDriverTurnOne(false)); err != nil ||
		turn1.Outcome != productsession.OutcomePublished {
		t.Fatalf("turn 1: %+v %v", turn1, err)
	}
	turn2 := f.submit(t, "turn-2", productDriverPromptB).Turn
	ctx := context.Background()
	predecessor, err := f.env.predecessorContext(turn2, productDriverProfile)
	if err != nil {
		t.Fatal(err)
	}
	w, err := f.env.composeProductTurnWorkflow(ctx, turn2, productDriverProfile, predecessor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.close() })
	f.script = &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: "t2-response-read", CallID: productDriverReadCall,
		Tool: "read", Arguments: json.RawMessage(`{"path":"notes.txt"}`)}}}
	conversation, err := w.authority.newProviderConversation(w.config, f.env.NewConversation)
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	if err := w.authority.AdmitPrompt(conversation, w.durable, w.taskID, turn2.Prompt); err != nil {
		t.Fatal(err)
	}
	if next, err := conversation.NextTurn(ctx); err != nil || next.ToolCall == nil || next.ToolCall.ID != productDriverReadCall {
		t.Fatalf("turn 2 tool call: %+v %v", next, err)
	}
	matched, err := conversation.Correlate(ctx, protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion,
		ToolCallID: "t1-call-write", Tool: "write", Arguments: json.RawMessage(`{"path":"notes.txt","content":"notes from turn one\n"}`)})
	if err != nil || matched.Accepted {
		t.Fatalf("turn 1's imported tool call correlated in turn 2: %+v %v", matched, err)
	}
}
