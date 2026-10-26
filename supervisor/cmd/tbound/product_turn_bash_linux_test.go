//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
)

// productDevCommandRunner is the development command runner with the retention the D09 runner has: each
// settlement the Store verified is appended to the turn's command journal, bound to its view and lease.
// It claims no containment. It stands in for the cell runner of a qualified host.
type productDevCommandRunner struct {
	devFixtureRunner
	journal *audit.Journal
}

func (r productDevCommandRunner) VerifySettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if err := r.devFixtureRunner.VerifySettlement(settlement, viewID, leaseID); err != nil {
		return err
	}
	data, err := json.Marshal(settlement)
	if err != nil {
		return err
	}
	_, err = r.journal.Append(audit.Event{Kind: "dev_command_settled", ID: viewID + ":" + leaseID, Data: data})
	return err
}

// productDevCommandHistory verifies a retained development command journal read-only: every Bash
// effect of the Store must carry exactly the settlement the journal retained for its view and lease,
// and the journal holds no settlement without a Store Bash operation.
type productDevCommandHistory struct {
	journal *audit.Journal
	initial audit.Trace
	byLease map[string]sessionrepo.CommandSettlement
}

func openProductDevCommandHistory(journal *audit.Journal, _ productsession.TurnLink) (governedRecoveryCommandVerifier, error) {
	trace, err := journal.Trace()
	if err != nil {
		return nil, err
	}
	h := &productDevCommandHistory{journal: journal, initial: trace, byLease: map[string]sessionrepo.CommandSettlement{}}
	for _, record := range trace.Records {
		var settlement sessionrepo.CommandSettlement
		if record.Event.Kind != "dev_command_settled" || json.Unmarshal(record.Event.Data, &settlement) != nil ||
			record.Event.ID != settlement.ViewID+":"+settlement.LeaseID {
			return nil, errors.New("the retained command journal holds a record that is not a command settlement")
		}
		if _, duplicate := h.byLease[record.Event.ID]; duplicate {
			return nil, errors.New("the retained command journal repeats a command settlement")
		}
		h.byLease[record.Event.ID] = settlement
	}
	return h, nil
}

func (h *productDevCommandHistory) VerifySettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if retained, ok := h.byLease[viewID+":"+leaseID]; !ok || retained != settlement {
		return errors.New("the command settlement was not retained for this view and lease")
	}
	return nil
}

func (h *productDevCommandHistory) VerifyStoreBundle(bundle sessionrepo.EvidenceBundle) error {
	bash := 0
	for _, operation := range bundle.Operations {
		if operation.Tool != "bash" {
			continue
		}
		bash++
		if _, ok := h.byLease[operation.ViewID+":"+operation.Operation.LeaseID]; !ok {
			return errors.New("a Store Bash operation has no retained command settlement")
		}
	}
	if bash != len(h.byLease) {
		return errors.New("the retained command journal holds a settlement without a Store Bash operation")
	}
	return nil
}

func (h *productDevCommandHistory) VerifyGovernedVerifier(*governedStoreReplayVerifier) error {
	return nil
}

func (h *productDevCommandHistory) VerifyUnleasedProviderCallRetirement(providerbridge.ProviderCallRetirementBinding) error {
	return errors.New("the development command history cannot retire provider calls")
}

func (h *productDevCommandHistory) VerifyIdleProviderLineageRetirement(string, string, uint64, uint64, uint64) error {
	return errors.New("the development command history cannot retire provider lineage")
}

func (h *productDevCommandHistory) VerifyUnchanged() error {
	trace, err := h.journal.Trace()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(h.initial, trace) {
		return errors.New("the retained command journal changed during verification")
	}
	return nil
}

func seedBashProject(t *testing.T) func(string) {
	seed := seedScopedProject(t)
	return func(projectDir string) {
		seed(projectDir)
		writeProductDriverFile(t, filepath.Join(projectDir, "notes"), "old.txt", "stale\n")
	}
}

func bashCall(id, command string) providerbridge.ScriptTurn {
	arguments, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		panic(err)
	}
	return providerbridge.ScriptTurn{ResponseID: "response-" + id, CallID: "call-" + id, Tool: "bash", Arguments: arguments}
}

// runBashPredecessor runs turn 1 of an auto-publish session whose Bash call creates notes/a.txt.
func runBashPredecessor(t *testing.T) (*productDriverFixture, productsession.Turn) {
	t.Helper()
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("create", `printf 'inside\n' > notes/a.txt`),
		scopeDriverFinal("t1", scopeDriverAnswer)}}
	_, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
	if err != nil || turn.Outcome != productsession.OutcomePublished {
		t.Fatalf("turn 1: %+v %v", turn, err)
	}
	return f, turn
}

// A Bash candidate is admitted by the product scope alone: creates, deletes and renames inside the
// writable subtree are published, and the same operations outside it, across a protected path, or into
// it from outside are denied with their stable reason codes and hints. The next turn reopens the Bash
// turn as its predecessor and carries its results.
func TestProductTurnBashIsAdmittedByTheProductScopeAndReopensAsPredecessor(t *testing.T) {
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
	f.env.MaxWorkerSteps = 12
	baseline := f.measure(t)
	calls := []struct{ id, command, reason string }{
		{"create-in", `mkdir -p notes/sub && printf 'inside\n' > notes/sub/a.txt`, ""},
		{"delete-in", `rm notes/old.txt`, ""},
		{"rename-in", `mv notes/sub/a.txt notes/b.txt`, ""},
		{"create-out", `printf 'new\n' > outside.txt`, "scope_path_not_covered"},
		{"modify-out", `printf 'changed\n' > other.txt`, "scope_path_not_covered"},
		{"delete-out", `rm other.txt`, "scope_path_not_covered"},
		{"mkdir-out", `mkdir outdir && printf 'x\n' > outdir/c.txt`, "scope_path_not_covered"},
		{"rename-out-in", `mv other.txt notes/other.txt`, "scope_path_not_covered"},
		{"rename-protected", `mv notes/secret.txt notes/moved.txt`, "scope_protected_path"},
	}
	script := &productDriverScript{}
	var wantDenials []productTurnDenial
	for _, call := range calls {
		script.turns = append(script.turns, bashCall(call.id, call.command))
		if call.reason != "" {
			wantDenials = append(wantDenials, productTurnDenial{ToolCallID: "call-" + call.id, Tool: "bash", ReasonCode: call.reason})
		}
	}
	script.turns = append(script.turns, scopeDriverFinal("t1", scopeDriverAnswer))
	receipt, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	notes := filepath.Join(f.projectDir, "notes")
	for _, want := range []struct{ dir, name, content string }{
		{notes, "b.txt", "inside\n"}, {f.projectDir, "other.txt", "outside\n"}, {notes, "secret.txt", "private\n"},
		{notes, "a.txt", "<open " + filepath.Join(notes, "a.txt") + ": no such file or directory>"},
		{notes, "old.txt", "<open " + filepath.Join(notes, "old.txt") + ": no such file or directory>"},
		{notes, "moved.txt", "<open " + filepath.Join(notes, "moved.txt") + ": no such file or directory>"},
		{f.projectDir, "outside.txt", "<open " + filepath.Join(f.projectDir, "outside.txt") + ": no such file or directory>"},
		{notes, "other.txt", "<open " + filepath.Join(notes, "other.txt") + ": no such file or directory>"},
	} {
		if got := fileContent(t, want.dir, want.name); got != want.content {
			t.Errorf("%s/%s = %q, want %q", want.dir, want.name, got, want.content)
		}
	}
	if _, err := os.Stat(filepath.Join(f.projectDir, "outdir")); !os.IsNotExist(err) {
		t.Errorf("a directory created outside the writable scope reached the project: %v", err)
	}
	if turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomePublished ||
		turn.ResultBaselineDigest == baseline || turn.ResultBaselineDigest != f.measure(t) || receipt.PublicationTransactionID == "" ||
		fmt.Sprint(receipt.ToolSequence) != "["+strings.TrimSpace(strings.Repeat("bash ", len(calls)))+"]" ||
		fmt.Sprint(receipt.Denials) != fmt.Sprint(wantDenials) {
		t.Fatalf("turn 1 settlement: turn=%+v receipt=%+v", turn, receipt)
	}
	if receipt.CommandJournalCount != uint64(len(calls)) || !validBareWorkflowDigest(receipt.CommandJournalHead) {
		t.Fatalf("the receipt does not commit one retained settlement per Bash command: %+v", receipt)
	}
	if session := f.currentSession(t); session.Blocked || session.Reserved != (productsession.Budget{}) {
		t.Fatalf("a denied Bash candidate must not block the session: %+v", session)
	}
	messages := lastProviderRequestMessages(t, turn.Link.ProviderJournalPath)
	for _, call := range calls {
		denied := toolMessage(t, messages, "call-"+call.id)
		if call.reason == "" {
			if strings.Contains(denied, "DENY") || strings.Contains(denied, "hint:") {
				t.Errorf("%s: the admitted command was reported as denied: %q", call.id, denied)
			}
			continue
		}
		hint := providerbridge.HintCandidates(call.reason)["0"]
		if hint == "" || !strings.Contains(denied, `"DENY"`) || !strings.Contains(denied, call.reason) || !strings.HasSuffix(denied, "\nhint: "+hint) {
			t.Errorf("%s: the provider did not get the denial with its hint: %q", call.id, denied)
		}
	}

	script2 := &productDriverScript{
		turns: []providerbridge.ScriptTurn{bashCall("grep", `grep -q inside notes/b.txt`), scopeDriverFinal("t2", "It says inside.")},
		check: func(history []providerbridge.ScriptMessage) error {
			for _, id := range []string{"call-create-in", "call-rename-protected"} {
				if messageIndex(history, 0, func(m providerbridge.ScriptMessage) bool { return m.Role == "tool" && m.ToolCallID == id }) < 0 {
					return fmt.Errorf("the Bash result %s is missing from the imported history", id)
				}
			}
			return nil
		},
	}
	receipt2, turn2, err := f.run(t, f.submit(t, "turn-2", "Read notes/b.txt back to me."), script2)
	if err != nil || turn2.Outcome != productsession.OutcomeNoDiff || receipt2.PredecessorReceiptDigest != turn.ReceiptDigest ||
		receipt2.CommandJournalCount != 1 || script2.requests() != 2 {
		t.Fatalf("turn 2 over the Bash predecessor: %+v %+v %v", turn2, receipt2, err)
	}
	if got := toolMessage(t, lastProviderRequestMessages(t, turn2.Link.ProviderJournalPath), "call-grep"); !strings.Contains(got, `"exit_code":0`) {
		t.Errorf("the read-only command did not run and succeed in turn 2: %q", got)
	}
}

func TestProductTurnBashWithNoWorkspaceChangesReopensAsPredecessor(t *testing.T) {
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
	baseline := f.measure(t)
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{
		bashCall("tests", `printf 'tests passed\n'`), scopeDriverFinal("t1", "The tests passed.")}}
	receipt, turn, err := f.run(t, f.submit(t, "turn-1", "Run the tests."), script)
	if err != nil || turn.Outcome != productsession.OutcomeNoDiff || turn.ResultBaselineDigest != baseline ||
		f.measure(t) != baseline || receipt.CommandJournalCount != 1 || script.requests() != 2 {
		t.Fatalf("turn 1 with no workspace changes: turn=%+v receipt=%+v err=%v", turn, receipt, err)
	}
	result := toolMessage(t, lastProviderRequestMessages(t, turn.Link.ProviderJournalPath), "call-tests")
	if !strings.Contains(result, `"exit_code":0`) || !strings.Contains(result, `tests passed\n`) {
		t.Fatalf("the no-change command did not return its output: %q", result)
	}

	script2 := &productDriverScript{
		turns: []providerbridge.ScriptTurn{scopeDriverFinal("t2", "The tests passed.")},
		check: func(history []providerbridge.ScriptMessage) error {
			at := messageIndex(history, 0, func(m providerbridge.ScriptMessage) bool { return m.Role == "tool" && m.ToolCallID == "call-tests" })
			if at < 0 || history[at].Content != result {
				return errors.New("the imported history lost the no-change command result")
			}
			return nil
		},
	}
	receipt2, turn2, err := f.run(t, f.submit(t, "turn-2", "What was the test result?"), script2)
	if err != nil || turn2.Outcome != productsession.OutcomeNoDiff || script2.requests() != 1 ||
		receipt2.PredecessorReceiptDigest != turn.ReceiptDigest || f.measure(t) != baseline {
		t.Fatalf("turn 2 over a no-change Bash predecessor: turn=%+v receipt=%+v err=%v", turn2, receipt2, err)
	}
}

// Retained command settlements are verified when the next turn reopens a Bash turn. A command journal that
// differs from the receipt's commitment, or a service that cannot verify settlements, refuses the next
// turn before any provider work.
func TestProductTurnBashRefusesAPredecessorWithTamperedCommandEvidence(t *testing.T) {
	for name, tamper := range map[string]func(t *testing.T, f *productDriverFixture, turn productsession.Turn){
		"empty command journal": func(t *testing.T, _ *productDriverFixture, turn productsession.Turn) {
			if err := os.WriteFile(turn.Link.CommandJournalPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"extra settlement record": func(t *testing.T, _ *productDriverFixture, turn productsession.Turn) {
			journal, err := audit.Open(turn.Link.CommandJournalPath)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(sessionrepo.CommandSettlement{LeaseID: "lease-forged", ViewID: "view-forged"})
			if _, err := journal.Append(audit.Event{Kind: "dev_command_settled", ID: "view-forged:lease-forged", Data: data}); err != nil {
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
		},
		"corrupted command journal": func(t *testing.T, _ *productDriverFixture, turn productsession.Turn) {
			data, err := os.ReadFile(turn.Link.CommandJournalPath)
			if err != nil {
				t.Fatal(err)
			}
			data[len(data)/2] ^= 0x01
			if err := os.WriteFile(turn.Link.CommandJournalPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"no command history verifier": func(_ *testing.T, f *productDriverFixture, _ productsession.Turn) {
			f.env.OpenCommandHistory = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, turn1 := runBashPredecessor(t)
			if err := verifyReceiptCommitsCommands(turn1); err != nil {
				t.Fatal(err)
			}
			tamper(t, f, turn1)
			script := &productDriverScript{turns: []providerbridge.ScriptTurn{scopeDriverFinal("t2", "Done.")}}
			_, turn2, err := f.run(t, f.submit(t, "turn-2", "Read notes/a.txt back to me."), script)
			var refusal *productTurnRefusal
			if !errors.Is(err, productsession.ErrCode(productsession.CodePredecessorEvidenceInvalid)) || !errors.As(err, &refusal) {
				t.Fatalf("tampered command evidence: %v", err)
			}
			if script.requests() != 0 || turn2.Link != nil || turn2.State != productsession.StateTerminal ||
				turn2.Outcome != productsession.OutcomeFailed || turn2.Refund != turn2.Reservation {
				t.Fatalf("refused turn: requests=%d turn=%+v", script.requests(), turn2)
			}
		})
	}
}

func verifyReceiptCommitsCommands(turn productsession.Turn) error {
	receipt, err := decodeProductTurnReceipt(turn.Receipt)
	if err != nil {
		return err
	}
	if receipt.CommandJournalCount != 1 {
		return fmt.Errorf("receipt commits %d command settlements, want 1", receipt.CommandJournalCount)
	}
	data, err := os.ReadFile(turn.Link.CommandJournalPath)
	if err != nil || !bytes.Contains(data, []byte("dev_command_settled")) {
		return fmt.Errorf("the command journal retains no settlement: %v", err)
	}
	return nil
}

// A read-only session runs a read-only command but cannot change the project by Bash, with or without a
// registered scope.
func TestProductTurnBashInAReadOnlySessionChangesNothing(t *testing.T) {
	for name, scope := range map[string]*productsession.ProductScope{
		"project with a scope":    productScopeNotes(),
		"project without a scope": nil,
	} {
		t.Run(name, func(t *testing.T) {
			f := newBashProductDriverFixture(t, productsession.PublicationAuto, scope, true, []string{}, seedBashProject(t))
			baseline := f.measure(t)
			script := &productDriverScript{turns: []providerbridge.ScriptTurn{
				bashCall("write", `printf 'changed\n' > notes/a.txt`), bashCall("grep", `grep -q "turn fixture" task.txt`), scopeDriverFinal("ro", "Read it.")}}
			receipt, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
			if err != nil {
				t.Fatalf("turn: %v", err)
			}
			wantDenials := []productTurnDenial{{ToolCallID: "call-write", Tool: "bash", ReasonCode: "scope_path_not_covered"}}
			if turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomeNoDiff ||
				turn.ResultBaselineDigest != baseline || f.measure(t) != baseline || receipt.PublicationTransactionID != "" ||
				fmt.Sprint(receipt.Denials) != fmt.Sprint(wantDenials) || receipt.CommandJournalCount != 2 {
				t.Fatalf("a read-only Bash turn changed something or settled wrongly: turn=%+v receipt=%+v", turn, receipt)
			}
			messages := lastProviderRequestMessages(t, turn.Link.ProviderJournalPath)
			if got := toolMessage(t, messages, "call-grep"); !strings.Contains(got, `"exit_code":0`) || strings.Contains(got, "DENY") {
				t.Errorf("the read-only command did not run: %q", got)
			}
			if got := toolMessage(t, messages, "call-write"); !strings.Contains(got, "scope_path_not_covered") {
				t.Errorf("the writing command was not denied: %q", got)
			}
		})
	}
}

// A project without a product scope keeps the exact-path rule for Bash: the registered paths are the
// admitted candidate, and a retained Store reopens under the same declared scope.
func TestProductTurnBashKeepsTheExactPathRuleWithoutAProductScope(t *testing.T) {
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, nil, false, []string{"out.txt"}, nil)
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{
		bashCall("out", `printf 'ok\n' > out.txt`), bashCall("task", `printf 'no\n' > task.txt`), scopeDriverFinal("t1", "Done.")}}
	receipt, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
	wantDenials := []productTurnDenial{{ToolCallID: "call-task", Tool: "bash", ReasonCode: "scope_path_not_covered"}}
	if err != nil || turn.Outcome != productsession.OutcomePublished || fmt.Sprint(receipt.Denials) != fmt.Sprint(wantDenials) ||
		fileContent(t, f.projectDir, "out.txt") != "ok\n" || fileContent(t, f.projectDir, "task.txt") != "turn fixture\n" {
		t.Fatalf("exact-path Bash turn: %+v %+v %v", turn, receipt, err)
	}
	script2 := &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("grep", `grep -q ok out.txt`), scopeDriverFinal("t2", "ok")}}
	if _, turn2, err := f.run(t, f.submit(t, "turn-2", "Read out.txt."), script2); err != nil || turn2.Outcome != productsession.OutcomeNoDiff {
		t.Fatalf("turn 2 over the exact-path Bash predecessor: %+v %v", turn2, err)
	}
}

// A project whose policy allows Bash cannot launch a turn on a service with no command runner.
func TestProductTurnBashRefusesToLaunchWithoutACommandRunner(t *testing.T) {
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
	f.env.NewCommandRunner = nil
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{scopeDriverFinal("t1", "Done.")}}
	_, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
	if !errors.Is(err, errProductBashRunnerUnavailable) || script.requests() != 0 || turn.State != productsession.StateAbandoned ||
		turn.Link != nil || f.currentSession(t).Blocked {
		t.Fatalf("no command runner: turn=%+v err=%v", turn, err)
	}
}

// TestProductTurnBashCrashChild runs only as the fresh process the restart test starts. It drives a Bash
// turn to the point where the command has settled and exits there without any cleanup.
func TestProductTurnBashCrashChild(t *testing.T) {
	root := os.Getenv(productCrashRootEnv)
	if root == "" {
		t.Skip("fresh-process crash helper")
	}
	f := openBashProductDriverFixture(t, root, productScopeNotes(), []string{})
	f.session.ID = os.Getenv(productCrashSessionEnv)
	f.env.crashAt = func(at string) {
		if at == "after-effect" {
			os.Exit(productCrashExit)
		}
	}
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{bashCall("create", `printf 'inside\n' > notes/a.txt`),
		scopeDriverFinal("t1", scopeDriverAnswer)}}
	_, _, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
	t.Fatalf("the child reached no crash point: %v", err)
}

// A linked Bash turn whose process died is reconciled from its retained evidence: failed when the Store
// and its command settlements verify, and unknown, blocking the session, when they cannot be verified.
// The next turn reopens a reconciled Bash turn as its predecessor.
func TestProductTurnBashRestartReconcilesALinkedTurn(t *testing.T) {
	for _, c := range []struct {
		name    string
		history bool
	}{{"verified command settlements", true}, {"no command history verifier", false}} {
		t.Run(c.name, func(t *testing.T) {
			created := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
			baseline := created.measure(t)
			if err := created.env.Catalog.Close(); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestProductTurnBashCrashChild$", "-test.count=1")
			child.Env = append(os.Environ(), productCrashRootEnv+"="+created.root, productCrashSessionEnv+"="+created.session.ID)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != productCrashExit {
				t.Fatalf("child did not crash after the command settled: %v\n%s", err, output)
			}
			f := openBashProductDriverFixture(t, created.root, productScopeNotes(), []string{})
			f.session = created.session
			if !c.history {
				f.env.OpenCommandHistory = nil
			}
			reconcileErr := f.env.ReconcileAfterRestart(context.Background())
			turn, session := f.turn(t, f.session.ID+"/turn-1"), f.currentSession(t)
			if f.measure(t) != baseline {
				t.Fatal("the project changed")
			}
			if !c.history {
				if reconcileErr == nil || turn.State != productsession.StateUnknown || !session.Blocked {
					t.Fatalf("unverifiable Bash turn: turn=%+v session=%+v err=%v", turn, session, reconcileErr)
				}
				return
			}
			receipt, decodeErr := decodeProductTurnReceipt(turn.Receipt)
			if reconcileErr != nil || decodeErr != nil || turn.State != productsession.StateTerminal || turn.Outcome != productsession.OutcomeFailed ||
				turn.Settlement != productsession.SettlementReconciledAfterRestart || session.Blocked || receipt.CommandJournalCount != 1 {
				t.Fatalf("verified Bash turn: turn=%+v session=%+v receipt=%+v errs=%v %v", turn, session, receipt, reconcileErr, decodeErr)
			}
			f.followUp(t, "turn-2", productsession.OutcomeFailed)
		})
	}
}

// A publication trust that cannot resolve the origin of a Bash lease would leave the finished turn
// unpublishable, so the turn is refused before any work.
func TestProductTurnBashRefusesAPublicationTrustWithoutALeaseOriginResolver(t *testing.T) {
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
	f.env.PublicationTrust = func(authority *SessionOperationAuthority) workflow.Trust {
		return struct{ workflow.Trust }{productDriverTrust{authority: authority}}
	}
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{scopeDriverFinal("t1", "Done.")}}
	_, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
	if err == nil || script.requests() != 0 || turn.State != productsession.StateAbandoned || turn.Link != nil {
		t.Fatalf("trust without a lease origin resolver: turn=%+v err=%v", turn, err)
	}
}

// bashResultOf decodes the Bash result the provider received for a call: the settled command summary,
// the public status, and the text after the result JSON, which holds the hint.
func bashResultOf(t *testing.T, message string) (command durableCommandSummary, status, suffix string) {
	t.Helper()
	body, suffix, _ := strings.Cut(message, "\nhint: ")
	var result struct {
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
	}
	var summary durableMutationSummaryPayload
	if err := json.Unmarshal([]byte(body), &result); err != nil || json.Unmarshal(result.Output, &summary) != nil || summary.Command == nil {
		t.Fatalf("the provider got no settled Bash result: %q (%v)", message, err)
	}
	return *summary.Command, result.Status, suffix
}

// The model sees what its Bash command printed: stdout and stderr as bounded valid UTF-8, the tail of a long
// stream with its truncated flag, and a failed command with its output and the command_failed hint. The next
// turn reopens the Bash turn as a predecessor, so replay rebuilds the same output from the Store.
func TestProductTurnBashShowsTheModelItsCommandOutput(t *testing.T) {
	f := newBashProductDriverFixture(t, productsession.PublicationAuto, productScopeNotes(), false, []string{}, seedBashProject(t))
	f.env.MaxWorkerSteps = 8
	script := &productDriverScript{turns: []providerbridge.ScriptTurn{
		bashCall("fail", `printf 'out\n'; printf 'FAIL: boom\n' >&2; printf 'ran\n' > notes/run.log; exit 3`),
		bashCall("long", `i=0; while [ $i -lt 3000 ]; do printf 'line %04d\n' $i; i=$((i+1)); done; printf 'ran\n' > notes/long.log`),
		bashCall("binary", `printf 'a\377b\n'; printf 'ran\n' > notes/binary.log`),
		scopeDriverFinal("t1", scopeDriverAnswer)}}
	_, turn, err := f.run(t, f.submit(t, "turn-1", scopeDriverPrompt), script)
	if err != nil || turn.Outcome != productsession.OutcomePublished {
		t.Fatalf("turn 1: %+v %v", turn, err)
	}
	messages := lastProviderRequestMessages(t, turn.Link.ProviderJournalPath)

	failed, status, hint := bashResultOf(t, toolMessage(t, messages, "call-fail"))
	if failed.ExitCode != 3 || failed.Stdout == nil || *failed.Stdout != "out\n" || *failed.StdoutTruncated ||
		*failed.Stderr != "FAIL: boom\n" || *failed.StderrTruncated || status != "command_failed" ||
		hint != providerbridge.HintCandidates("command_failed")["0"] {
		t.Errorf("the failed command did not reach the model with its output and hint: %+v status=%q hint=%q", failed, status, hint)
	}

	long, status, _ := bashResultOf(t, toolMessage(t, messages, "call-long"))
	if status != "" || long.Stdout == nil || !*long.StdoutTruncated || len(*long.Stdout) > productCommandOutputBytes ||
		len(*long.Stdout) < productCommandOutputBytes-16 || !strings.HasSuffix(*long.Stdout, "line 2999\n") ||
		strings.Contains(*long.Stdout, "line 0000") || *long.Stderr != "" || *long.StderrTruncated {
		t.Errorf("a long stdout must keep its bounded tail with the flag: truncated=%v len=%d", *long.StdoutTruncated, len(*long.Stdout))
	}

	binary, _, _ := bashResultOf(t, toolMessage(t, messages, "call-binary"))
	if binary.Stdout == nil || *binary.Stdout != "a\xef\xbf\xbdb\n" || !utf8.ValidString(*binary.Stdout) {
		t.Errorf("invalid UTF-8 must be replaced: %q", *binary.Stdout)
	}

	script2 := &productDriverScript{
		turns: []providerbridge.ScriptTurn{scopeDriverFinal("t2", "Done.")},
		check: func(history []providerbridge.ScriptMessage) error {
			at := messageIndex(history, 0, func(m providerbridge.ScriptMessage) bool { return m.Role == "tool" && m.ToolCallID == "call-fail" })
			if at < 0 || !strings.Contains(history[at].Content, "FAIL: boom") {
				return errors.New("the imported history lost the command output of turn 1")
			}
			return nil
		},
	}
	if _, turn2, err := f.run(t, f.submit(t, "turn-2", "What failed?"), script2); err != nil || turn2.Outcome != productsession.OutcomeNoDiff ||
		script2.requests() != 1 {
		t.Fatalf("turn 2 over a predecessor whose Bash output is replayed: %+v %v", turn2, err)
	}
}
