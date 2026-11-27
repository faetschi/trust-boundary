//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/adaptivesource"
	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/workflowrecovery"
)

// budgetEntry is a valid reserved catalog entry of an adaptive attempt.
func budgetEntry(corpus *productionCorpus, caseID, role string, attempt int, hashChar string) piServiceWorkflowEntry {
	return piServiceWorkflowEntry{SchemaVersion: workflowCatalogSchema, HostAdmissionID: corpusAdmissionID(caseID, role, attempt),
		AdmissionRecordHash: strings.Repeat(hashChar, 64), AdmissionSequence: 1, PeerUID: 1000, PeerGID: 1000, PeerPID: 1,
		RequestID: "req-" + hashChar, TaskID: caseID, PayloadDigest: corpusTestDigest("payload"),
		ServiceConfigDigest: corpusTestDigest("config"), ServiceRegistrationID: corpusTestDigest("registration"),
		CorpusManifestDigest: corpus.digest, CorpusQuotaProfile: corpus.quotaProfile,
		WorkflowID: "wf-" + hashChar, RunID: "run-" + hashChar, SessionID: "run-" + hashChar, ConversationID: "conv-" + hashChar,
		OwnerDBPath: "/var/lib/budget/owner-" + hashChar + ".sqlite", StoreRootPath: "/var/lib/budget/store-" + hashChar,
		AdminRootPath: "/var/lib/budget/admin-" + hashChar, StoreJournalPath: "/var/lib/budget/s-" + hashChar + ".jsonl",
		ProviderJournalPath: "/var/lib/budget/p-" + hashChar + ".jsonl", CommandJournalPath: "/var/lib/budget/c-" + hashChar + ".jsonl",
		TaskPrompt: "prompt", TaskDigest: corpusTestDigest("task"), PromptDigest: corpusTestDigest("prompt"),
		Limits:     workflowrecovery.Limits{Deadline: time.Now().UTC().Add(time.Hour), MaxHostCalls: 4, MaxTokens: 100},
		ReservedAt: time.Now().UTC()}
}

func budgetUsage(iterations, wallMS int64) *piServiceAttackerUsage {
	u := adaptivesource.AdaptiveUsage{Iterations: iterations, ToolCalls: iterations, WallMS: wallMS}
	return &piServiceAttackerUsage{Stop: adaptivesource.AdaptiveStopOpen, StateDigest: strings.Repeat("a", 64), Usage: u, Cumulative: u}
}

// driveBinding answers every pivot of the binding's source with a policy denial until it stops and returns
// the number of pivots issued.
func driveBinding(t *testing.T, binding *corpusBinding) int {
	t.Helper()
	history := []providerbridge.ScriptMessage{{Role: "user", Content: "u"}}
	for n := 1; n <= 20; n++ {
		turn, err := binding.adaptive.Next(append([]providerbridge.ScriptMessage(nil), history...))
		if err != nil {
			t.Fatal(err)
		}
		if turn.Tool == "" {
			return n - 1
		}
		history = append(history, adaptiveResult(t, n, "DENY", "policy_rule_deny"))
	}
	t.Fatal("the source never stopped")
	return 0
}

// Attempt 2 of an adaptive role must not get a fresh budget: the service reads the usage that attempt 1
// persisted in its durable catalog and starts the machine with the remainder; the receipt binds the
// cumulative usage and the predecessor.
func TestAdaptiveRetryStartsWithTheBudgetAttemptOneLeft(t *testing.T) {
	corpus, manifest, _ := pivotedCorpus(t, nil) // attacker caps: 3 pivots, 3 tool calls, 60000 ms
	caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	for _, role := range []string{corpusRoleAdaptiveReplay, corpusRolePAdaptiveReplay} {
		first := budgetEntry(corpus, caseID, role, 1, "1")
		first.AttackerUsage = budgetUsage(2, 1500)
		catalog := &piServiceWorkflowCatalog{corpus: corpus, entries: map[string]piServiceWorkflowEntry{first.AdmissionRecordHash: first},
			byHostAdmissionID: map[string]string{first.HostAdmissionID: first.AdmissionRecordHash}}
		// attempt 1 runs against the full cap, with no predecessor
		fresh, err := catalog.bindingForEntry(budgetEntry(corpus, caseID, role, 1, "9"))
		if err != nil || fresh.predecessor != nil || driveBinding(t, fresh) != 3 {
			t.Fatalf("%s attempt 1: %+v %v", role, fresh, err)
		}
		// attempt 2 gets the one pivot attempt 1 left
		retry, err := catalog.bindingForEntry(budgetEntry(corpus, caseID, role, 2, "2"))
		if err != nil || retry.predecessor == nil {
			t.Fatalf("%s attempt 2: %+v %v", role, retry, err)
		}
		if got := driveBinding(t, retry); got != 1 {
			t.Fatalf("%s attempt 2 issued %d pivots, want the 1 remaining", role, got)
		}
		var receipt productionPiReceipt
		if err := applyCorpusReceipt(&receipt, retry, true, corpusTestDigest("sp"), corpusTestDigest("mp"), audit.Trace{}, nil); err != nil {
			t.Fatal(err)
		}
		if receipt.AttackerUsage == nil || receipt.AttackerUsage.Iterations != 1 || receipt.AttackerCumulativeUsage == nil ||
			receipt.AttackerCumulativeUsage.Iterations != 3 || receipt.AttackerCumulativeUsage.ToolCalls != 3 ||
			receipt.AttackerCumulativeUsage.WallMS < 1500 || receipt.AttackerStop != adaptivesource.AdaptiveStopCap {
			t.Fatalf("%s receipt = %+v", role, receipt)
		}
		if p := receipt.AttackerPredecessor; p == nil || p.AdmissionID != first.HostAdmissionID || p.StateDigest != first.AttackerUsage.StateDigest ||
			p.CumulativeUsage != first.AttackerUsage.Cumulative {
			t.Fatalf("%s predecessor = %+v", role, receipt.AttackerPredecessor)
		}
		encoded, _ := json.Marshal(receipt)
		for _, member := range []string{`"attacker_usage":`, `"attacker_cumulative_usage":`, `"attacker_predecessor":`} {
			if !strings.Contains(string(encoded), member) {
				t.Errorf("%s receipt JSON lacks %s: %s", role, member, encoded)
			}
		}
		// the runner reads the same members
		var facts runner.ReceiptFacts
		if err := json.Unmarshal(encoded, &facts); err != nil || facts.AttackerUsage == nil || facts.AttackerUsage.Iterations != 1 ||
			facts.AttackerCumulativeUsage == nil || facts.AttackerCumulativeUsage.Iterations != 3 || facts.AttackerPredecessor == nil ||
			facts.AttackerPredecessor.AdmissionID != first.HostAdmissionID || facts.AttackerPredecessor.CumulativeUsage.Iterations != 2 {
			t.Errorf("%s: runner facts = %+v, %v", role, facts, err)
		}
		// attempt 1's receipt carries its own usage as the cumulative one and no predecessor
		var firstReceipt productionPiReceipt
		if err := applyCorpusReceipt(&firstReceipt, fresh, true, corpusTestDigest("sp"), corpusTestDigest("mp"), audit.Trace{}, nil); err != nil {
			t.Fatal(err)
		}
		if firstReceipt.AttackerPredecessor != nil || firstReceipt.AttackerCumulativeUsage == nil ||
			firstReceipt.AttackerCumulativeUsage.Iterations != 3 || *firstReceipt.AttackerUsage != *firstReceipt.AttackerCumulativeUsage {
			t.Fatalf("%s attempt 1 receipt = %+v", role, firstReceipt)
		}
	}
}

func TestAdaptiveRetryWithoutARecordedPredecessorIsRefused(t *testing.T) {
	corpus, manifest, _ := pivotedCorpus(t, nil)
	caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	role := corpusRolePAdaptiveReplay
	newCatalog := func(entries ...piServiceWorkflowEntry) *piServiceWorkflowCatalog {
		catalog := &piServiceWorkflowCatalog{corpus: corpus, entries: map[string]piServiceWorkflowEntry{}, byHostAdmissionID: map[string]string{}}
		for _, e := range entries {
			catalog.entries[e.AdmissionRecordHash] = e
			catalog.byHostAdmissionID[e.HostAdmissionID] = e.AdmissionRecordHash
		}
		return catalog
	}
	retry := budgetEntry(corpus, caseID, role, 2, "2")
	unrecorded := budgetEntry(corpus, caseID, role, 1, "1") // attempt 1 ran but its usage was never persisted
	exhausted := budgetEntry(corpus, caseID, role, 1, "1")
	exhausted.AttackerUsage = budgetUsage(3, 10)
	otherRole := budgetEntry(corpus, caseID, corpusRoleAdaptiveReplay, 1, "3") // the C half's usage is not P's
	otherRole.AttackerUsage = budgetUsage(1, 10)
	for name, catalog := range map[string]*piServiceWorkflowCatalog{
		"no attempt 1":            newCatalog(),
		"attempt 1 left no usage": newCatalog(unrecorded),
		"budget used up":          newCatalog(exhausted),
		"only the other half ran": newCatalog(otherRole),
	} {
		if _, err := catalog.bindingForEntry(retry); !errors.Is(err, ErrPiWorkflowCatalog) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// The adaptive run deadline is the trial's duration: the attacker enforces its own wall cap, so a cap of a few
// seconds never becomes the run's deadline. An attempt with no pivot or whole second of wall budget left is not admitted.
func TestAdaptiveEnvelopeDurationIsTheTrialDurationWhileWallBudgetIsLeft(t *testing.T) {
	corpus, manifest, config := pivotedCorpus(t, nil)
	caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	role := corpusRoleAdaptiveReplay
	first := budgetEntry(corpus, caseID, role, 1, "1")
	first.AttackerUsage = budgetUsage(1, 45_300)
	catalog := &piServiceWorkflowCatalog{corpus: corpus, entries: map[string]piServiceWorkflowEntry{first.AdmissionRecordHash: first},
		byHostAdmissionID: map[string]string{first.HostAdmissionID: first.AdmissionRecordHash}}
	duration := func(role string, attempt int) (uint64, bool) {
		envelope, ok := catalog.envelopeFor(config, corpusRequest("req", caseID, role, attempt))
		return envelope.DurationSeconds, ok
	}
	for _, tc := range []struct {
		role    string
		attempt int
	}{{role, 1}, {role, 2}, {corpusRolePAdaptiveReplay, 1}, {corpusRoleStaticReplay, 1}} {
		if d, ok := duration(tc.role, tc.attempt); !ok || d != 600 {
			t.Errorf("%s attempt %d deadline = %d, %v; want the trial's 600 s", tc.role, tc.attempt, d, ok)
		}
	}
	corpus.attacker.Caps.WallMS = 4000
	if d, ok := catalog.attemptDurationSeconds(evalcorpus.Trial{DurationSeconds: 1200}, corpusRequest("req", caseID, role, 1)); !ok || d != 1200 {
		t.Errorf("deadline under a 4000 ms wall cap = %d, %v; want the trial's 1200 s", d, ok)
	}
	corpus.attacker.Caps.WallMS = 60_000
	first.AttackerUsage = budgetUsage(1, 59_500)
	catalog.entries[first.AdmissionRecordHash] = first
	if _, ok := duration(role, 2); ok {
		t.Error("an attempt with less than a second of wall budget left was admitted")
	}
	first.AttackerUsage = budgetUsage(3, 100)
	catalog.entries[first.AdmissionRecordHash] = first
	if _, ok := duration(role, 2); ok {
		t.Error("an attempt with no pivot left was admitted")
	}
}

// The usage of a finished adaptive attempt is durable: it survives a reload of the catalog journal, a second
// record is refused unless identical, and only an adaptive admission can carry one.
func TestAttackerUsageIsDurableInTheCatalog(t *testing.T) {
	corpus, manifest, _ := pivotedCorpus(t, nil)
	caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaAdmitAll)
	path := filepath.Join(privateGovernedTestRoot(t), "workflow-catalog.jsonl")
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	adaptive := budgetEntry(corpus, caseID, corpusRoleAdaptiveReplay, 1, "1")
	static := budgetEntry(corpus, caseID, corpusRoleStaticReplay, 1, "2")
	catalog := &piServiceWorkflowCatalog{journal: journal, corpus: corpus, entries: map[string]piServiceWorkflowEntry{},
		byHostAdmissionID: map[string]string{}, ownerScopes: map[string]piServiceWorkflowOwnerScope{}}
	for _, e := range []piServiceWorkflowEntry{adaptive, static} {
		event := piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: e.AdmissionRecordHash, Entry: e}
		if err := catalog.appendLocked("pi_workflow_admission_reserved", workflowCatalogEventID("reserved", e.AdmissionRecordHash), event); err != nil {
			t.Fatal(err)
		}
		catalog.entries[e.AdmissionRecordHash] = e
		catalog.byHostAdmissionID[e.HostAdmissionID] = e.AdmissionRecordHash
	}
	usage := *budgetUsage(2, 1500)
	if err := catalog.RecordAttackerUsage(adaptive.AdmissionRecordHash, usage); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := catalog.RecordAttackerUsage(adaptive.AdmissionRecordHash, usage); err != nil {
		t.Fatalf("an identical record is idempotent: %v", err)
	}
	other := usage
	other.Cumulative.Iterations++
	for name, err := range map[string]error{
		"a different second record": catalog.RecordAttackerUsage(adaptive.AdmissionRecordHash, other),
		"a static admission":        catalog.RecordAttackerUsage(static.AdmissionRecordHash, usage),
		"an unknown admission":      catalog.RecordAttackerUsage(strings.Repeat("f", 64), usage),
		"negative usage":            catalog.RecordAttackerUsage(adaptive.AdmissionRecordHash, piServiceAttackerUsage{Cumulative: adaptivesource.AdaptiveUsage{WallMS: -1}}),
	} {
		if !errors.Is(err, ErrPiWorkflowCatalog) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadPiServiceWorkflowCatalog(reopened)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer reloaded.Close()
	got := reloaded.entries[adaptive.AdmissionRecordHash].AttackerUsage
	if got == nil || *got != usage || reloaded.entries[static.AdmissionRecordHash].AttackerUsage != nil {
		t.Fatalf("reloaded usage = %+v", got)
	}
}

// The spend of an attempt is recorded from the source itself, whether or not the session got as far as a receipt:
// the next attempt then continues from it, and an attempt that recorded nothing leaves no rerun.
func TestRecordedAttackerUsageIsWhatTheNextAttemptContinuesFrom(t *testing.T) {
	corpus, manifest, _ := pivotedCorpus(t, nil)
	caseID := corpusCaseWithDelta(t, manifest, evalcorpus.PDeltaNone)
	role := corpusRolePAdaptiveReplay
	journal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "workflow-catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	first := budgetEntry(corpus, caseID, role, 1, "1")
	catalog := &piServiceWorkflowCatalog{journal: journal, corpus: corpus, entries: map[string]piServiceWorkflowEntry{first.AdmissionRecordHash: first},
		byHostAdmissionID: map[string]string{first.HostAdmissionID: first.AdmissionRecordHash}, ownerScopes: map[string]piServiceWorkflowOwnerScope{}}
	binding, err := catalog.bindingForEntry(first)
	if err != nil {
		t.Fatal(err)
	}
	admission := &productionWorkflowAdmission{Catalog: catalog, Entry: first}
	// attempt 1 issues its first pivot and the session dies
	if turn, err := binding.adaptive.Next([]providerbridge.ScriptMessage{{Role: "user", Content: "u"}}); err != nil || turn.Tool == "" {
		t.Fatalf("first turn = %+v, %v", turn, err)
	}
	if _, err := catalog.bindingForEntry(budgetEntry(corpus, caseID, role, 2, "2")); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("a rerun of an attempt that recorded nothing was served: %v", err)
	}
	if err := recordAttackerUsage(admission, binding); err != nil {
		t.Fatal(err)
	}
	retry, err := catalog.bindingForEntry(budgetEntry(corpus, caseID, role, 2, "2"))
	if err != nil || retry.predecessor == nil || retry.predecessor.CumulativeUsage.Iterations != 1 || retry.predecessor.AdmissionID != first.HostAdmissionID {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	// the first record wins: a later call, made after the source moved on, changes nothing and is an error
	if _, err := binding.adaptive.Next([]providerbridge.ScriptMessage{{Role: "user", Content: "u"}, adaptiveResult(t, 1, "DENY", "policy_rule_deny")}); err != nil {
		t.Fatal(err)
	}
	if err := recordAttackerUsage(admission, binding); err == nil {
		t.Error("a second, different record was accepted")
	}
	if recordAttackerUsage(nil, binding) != nil || recordAttackerUsage(admission, nil) != nil {
		t.Error("nothing to record must be a no-op")
	}
}
