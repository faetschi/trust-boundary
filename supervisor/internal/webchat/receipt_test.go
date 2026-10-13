package webchat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var receiptTestTime = time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)

func projectExample(t *testing.T) Snapshot {
	t.Helper()
	snapshot, err := ProjectReceipt(exampleReceipt, receiptTestTime)
	if err != nil {
		t.Fatalf("ProjectReceipt(example): %v", err)
	}
	return snapshot
}

func TestProjectDevelopmentReceiptIsHonest(t *testing.T) {
	snapshot := projectExample(t)

	if snapshot.Source != SourceReplay || snapshot.Verification != VerificationUnverifiedInput {
		t.Fatalf("receipt projection source=%q verification=%q", snapshot.Source, snapshot.Verification)
	}
	// Settlement is UNKNOWN, so the session is not presented as completed.
	if snapshot.Session.Status != SessionUnknown {
		t.Fatalf("session status=%q, want unknown for an UNKNOWN settlement", snapshot.Session.Status)
	}
	if snapshot.Session.TurnCount != 1 {
		t.Fatalf("turn count=%d, want 1", snapshot.Session.TurnCount)
	}
	if snapshot.Panels.Publication.State != PublicationNotAttempted {
		t.Fatalf("publication=%q, want not-attempted", snapshot.Panels.Publication.State)
	}
	if snapshot.Panels.Recovery.State != RecoveryUnknown {
		t.Fatalf("recovery=%q, want unknown", snapshot.Panels.Recovery.State)
	}
	if snapshot.Panels.Provenance.Available {
		t.Fatal("an imported receipt must not claim verified provenance")
	}
	if snapshot.Panels.Qualification.G1 || snapshot.Panels.Qualification.ClaimBearing {
		t.Fatal("an imported receipt must not claim qualification")
	}

	// Every projected element is diagnostic unverified input, so the client
	// renders any success as "reported", never as confirmed success.
	for _, item := range snapshot.Items {
		if item.Authority != AuthorityDiagnostic || item.Verification != VerificationUnverifiedInput {
			t.Fatalf("item %q authority=%q verification=%q", item.ID, item.Authority, item.Verification)
		}
	}

	// 1 lifecycle + 4 activities + 4 results + 1 settlement line.
	if len(snapshot.Items) != 10 {
		t.Fatalf("projected %d items, want 10", len(snapshot.Items))
	}

	results := map[string]ResultState{}
	for _, item := range snapshot.Items {
		if item.Type == ItemResult {
			results[item.ToolCallID] = item.State
		}
	}
	if results["example-call-bash-g1"] != ResultSucceeded {
		t.Fatalf("bash result=%q, want succeeded", results["example-call-bash-g1"])
	}
	if results["example-call-edit-g0"] != ResultSucceeded {
		t.Fatalf("edit result=%q, want succeeded", results["example-call-edit-g0"])
	}
	// A read has no retained terminal outcome and must stay UNKNOWN.
	if results["example-call-read-g0"] != ResultUnknown || results["example-call-read-g2"] != ResultUnknown {
		t.Fatalf("read results=%q/%q, want unknown", results["example-call-read-g0"], results["example-call-read-g2"])
	}

	joined := strings.Join(snapshot.Warnings, " | ")
	if !strings.Contains(joined, "2 tool call(s) have no terminal outcome") {
		t.Fatalf("warnings did not surface the missing outcomes: %s", joined)
	}
	if !strings.Contains(joined, "not a qualification result") {
		t.Fatalf("warnings did not state the claim boundary: %s", joined)
	}

	gapSeen := false
	for _, stream := range snapshot.Streams {
		if stream.ID == "per-tool-outcome" && stream.Health == StreamGap {
			gapSeen = true
		}
		if stream.ID == "transcript" && stream.Available {
			t.Fatal("the transcript stream must be unavailable for a receipt import")
		}
	}
	if !gapSeen {
		t.Fatal("the per-tool-outcome stream should report a continuity gap")
	}
}

func TestProjectReceiptRefusals(t *testing.T) {
	base := string(exampleReceipt)
	if _, err := ProjectReceipt([]byte(base), receiptTestTime); err != nil {
		t.Fatalf("baseline example receipt must be accepted: %v", err)
	}

	cases := map[string]string{
		"claim bearing":     strings.Replace(base, `"claim_bearing": false`, `"claim_bearing": true`, 1),
		"containment claim": strings.Replace(base, `"containment": "not-established"`, `"containment": "established"`, 1),
		"unknown mode":      strings.Replace(base, `"mode": "dev-fixture"`, `"mode": "whatever"`, 1),
		"unregistered tool": strings.Replace(base, `"tool": "read"`, `"tool": "mcp"`, 1),
		"trailing json":     base + `{}`,
	}
	for name, raw := range cases {
		if _, err := ProjectReceipt([]byte(raw), receiptTestTime); err == nil {
			t.Errorf("%s: expected refusal, got acceptance", name)
		}
	}

	production := `{"schema_version":"tbound-production-pi-receipt/v1","mode":"production","run_id":"run-1","claim_bearing":false,"settlement":"SUCCEEDED","success":true}`
	if _, err := ProjectReceipt([]byte(production), receiptTestTime); err != nil {
		t.Fatalf("valid production receipt refused: %v", err)
	}
	wrongSchema := strings.Replace(production, "tbound-production-pi-receipt/v1", "tbound-other/v1", 1)
	if _, err := ProjectReceipt([]byte(wrongSchema), receiptTestTime); err == nil {
		t.Fatal("an unrecognized production receipt schema must be refused")
	}
}

func TestProjectProductionReceiptShape(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	raw := `{
      "schema_version": "tbound-production-pi-receipt/v1",
      "mode": "production",
      "run_id": "run-2026-10-13-01",
      "profile_id": "pi-cli-0.87.1",
      "profile_hash": "` + digest + `",
      "containment_profile": "d09-profile",
      "containment_profile_digest": "` + digest + `",
      "frozen_source_digest": "` + digest + `",
      "provider_profile": "broker-template-v1",
      "provider_exchange": true,
      "claim_bearing": false,
      "worker_ready": true,
      "task_admitted": true,
      "turn_completed": true,
      "proposal_count": 2,
      "provider_turns": 3,
      "settlement": "SUCCEEDED",
      "publication_status": "SUCCEEDED",
      "publication_transaction_id": "publication-transaction-0001",
      "disposition": "publication-succeeded",
      "success": true,
      "tcb_proof_digest": "` + digest + `",
      "tcb_proof_journal_sequence": 42
    }`
	snapshot, err := ProjectReceipt([]byte(raw), receiptTestTime)
	if err != nil {
		t.Fatalf("ProjectReceipt(production): %v", err)
	}
	if snapshot.Source != SourceReplay || snapshot.Verification != VerificationUnverifiedInput {
		t.Fatal("a production receipt import is still an unverified replay observation")
	}
	if snapshot.Session.Status != SessionCompleted {
		t.Fatalf("session status=%q, want completed for an explicit SUCCEEDED receipt", snapshot.Session.Status)
	}
	if snapshot.Panels.Publication.State != PublicationPublished || snapshot.Panels.Publication.TransactionID == "" {
		t.Fatalf("publication=%q transaction=%q", snapshot.Panels.Publication.State, snapshot.Panels.Publication.TransactionID)
	}
	if snapshot.Panels.Recovery.State != RecoveryNotRequired {
		t.Fatalf("recovery=%q, want not-required", snapshot.Panels.Recovery.State)
	}
	if snapshot.Panels.Provenance.ProfileID != "pi-cli-0.87.1" || snapshot.Panels.Provenance.TCBProofSequence != 42 {
		t.Fatalf("provenance not projected: %+v", snapshot.Panels.Provenance)
	}
	if snapshot.Panels.Provenance.Available {
		t.Fatal("provenance must remain unverified")
	}
	if snapshot.Panels.Qualification.G1 || snapshot.Panels.Qualification.ClaimBearing {
		t.Fatal("a production receipt import must not change the claim boundary")
	}
	// A provider exchange is reported but must not be presented as qualification.
	found := false
	for _, warning := range snapshot.Warnings {
		if strings.Contains(warning, "provider exchange") {
			found = true
		}
	}
	if !found {
		t.Fatal("a reported provider exchange should be surfaced with a caveat")
	}
}

func TestProjectNativeFixtureReceiptShape(t *testing.T) {
	raw := `{"mode":"native-fixture","claim_bearing":false,"provider_exchange":false,"containment":"not-established","publication_status":"SUCCEEDED"}`
	snapshot, err := ProjectReceipt([]byte(raw), receiptTestTime)
	if err != nil {
		t.Fatalf("ProjectReceipt(native-fixture): %v", err)
	}
	if snapshot.Panels.Publication.State != PublicationPublished {
		t.Fatalf("publication=%q, want published (reported)", snapshot.Panels.Publication.State)
	}
	if snapshot.Panels.Qualification.ClaimBearing || snapshot.Panels.Qualification.G1 {
		t.Fatal("native fixture receipt must not claim qualification")
	}
	if snapshot.Session.TurnCount != 0 {
		t.Fatalf("turn count=%d, want 0 for a receipt with no turn fields", snapshot.Session.TurnCount)
	}
}

func TestReceiptDemoStoreServesReplayProjection(t *testing.T) {
	store, err := NewReceiptDemoStore()
	if err != nil {
		t.Fatalf("NewReceiptDemoStore: %v", err)
	}
	snapshot := store.Snapshot()
	if snapshot.Source != SourceReplay {
		t.Fatalf("source=%q, want replay", snapshot.Source)
	}
	if len(snapshot.Items) == 0 {
		t.Fatal("receipt demo projection has no items")
	}
	// The served projection must be a valid bounded observation.
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("served snapshot invalid: %v", err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "command_runner_profile") {
		t.Fatal("the projection must not expose the raw command runner profile")
	}
}
