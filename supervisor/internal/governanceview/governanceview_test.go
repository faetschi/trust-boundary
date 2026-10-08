package governanceview

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/webview"
)

func testSnapshot(mode Mode) Snapshot {
	epoch := "test-epoch"
	return Snapshot{
		SchemaVersion: ContractVersion,
		Epoch:         epoch,
		Cursor:        formatCursor(epoch, 0),
		Mode:          mode,
		GeneratedAt:   time.Now().UTC().Add(-time.Minute),
		Session:       SessionHeader{Status: StateUnknown},
		Preflight:     []PreflightCheck{{Name: "admission", State: PreflightUnavailable}},
		Sources:       []Source{{ID: "source-1", Label: "sanitized source", Mode: mode, Health: SourceComplete, Available: mode != ModeUnavailable}},
		Publication:   PublicationView{State: PublicationUnavailable},
		Recovery:      RecoveryView{State: RecoveryUnavailable},
		Generation:    GenerationView{MetadataState: "unavailable", ContentsState: "unavailable", DiffState: "not-comparable"},
		ProjectTests:  []ProjectTestOutput{{Name: "Pi project tests", State: StateUnknown}},
	}
}

func testRecord(mode Mode, id string, sequence uint64) Record {
	return Record{
		SchemaVersion: ContractVersion,
		Sequence:      sequence,
		ID:            id,
		SourceID:      "source-1",
		Mode:          mode,
		Verification:  defaultVerificationScope(mode),
		Namespace:     NamespaceProposal,
		Kind:          "proposal",
		Status:        StatePending,
		Authority:     AuthorityDiagnostic,
		OccurredAt:    time.Now().UTC().Add(-time.Minute),
		Summary:       "hostile text is retained as display text only",
	}
}

func TestContractRejectsFixtureAuthorityAndUnknownFields(t *testing.T) {
	snapshot := testSnapshot(ModeFixture)
	record := testRecord(ModeFixture, "fixture-record", 1)
	record.Authority = AuthorityAuthoritative
	snapshot.Records = []Record{record}
	snapshot.Cursor = formatCursor(snapshot.Epoch, 1)
	if err := ValidateSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "authoritative") {
		t.Fatalf("expected fixture authority rejection, got %v", err)
	}

	snapshot.Records[0].Authority = AuthorityDiagnostic
	document := Document{SchemaVersion: DocumentVersion, Snapshot: snapshot}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "\\fixture.json"
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFixture(path)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if loaded.Mode != ModeFixture || len(loaded.Records) != 1 {
		t.Fatalf("unexpected loaded fixture: %#v", loaded)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":"tbound-governance-document/v1","snapshot":{},"extra":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFixture(path); err == nil {
		t.Fatal("unknown document field was accepted")
	}
}

func TestUnavailableSnapshotIsExplicitAndEmpty(t *testing.T) {
	snapshot := NewUnavailableSnapshot()
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Mode != ModeUnavailable || len(snapshot.Records) != 0 || snapshot.Sources[0].Available {
		t.Fatalf("unavailable snapshot fabricated production data: %#v", snapshot)
	}
}

func TestPublicWebviewAdapterStaysDiagnosticAndNamespaced(t *testing.T) {
	record, _, ok := FromWebviewRecordForRegistration(webview.Record{
		ID:         7,
		Source:     "registered-transcript",
		Types:      []string{"proposal"},
		ReceivedAt: time.Now().UTC().Add(-time.Minute),
		Record:     []byte(`{"proposal_id":"proposal-7","status":"pending"}`),
	}, SourceRegistration{Token: "registered-transcript", ID: "source-transcript", Label: "registered transcript"})
	if !ok {
		t.Fatal("public webview record was not mapped")
	}
	if record.Mode != ModeLive || record.Authority != AuthorityDiagnostic || record.Namespace != NamespaceProposal || record.Links.ProposalID != "proposal-7" {
		t.Fatalf("unexpected structural mapping: %#v", record)
	}
}

func TestPlainAuditTraceStaysStructuralDiagnostic(t *testing.T) {
	snapshot, err := FromVerifiedAuditTrace(audit.Trace{Records: []audit.Record{{
		Version: 1, Sequence: 4, Event: audit.Event{Kind: "effect_outcome", ID: "effect-4", Outcome: "unknown"}, Hash: strings.Repeat("a", 64),
	}}}, Source{ID: "audit-source", Label: "verified audit", Mode: ModeReplay, Health: SourceComplete})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != 1 || snapshot.Records[0].Authority != AuthorityDiagnostic || snapshot.Records[0].Verification != VerificationStructuralAudit || snapshot.Records[0].Status != StateUnknown || snapshot.Records[0].Links.EffectID != "effect-4" {
		t.Fatalf("unexpected verified audit projection: %#v", snapshot.Records)
	}
}

func TestPlainAuditOutcomeWithoutIntentDoesNotBecomeCompleted(t *testing.T) {
	snapshot, err := FromVerifiedAuditTrace(audit.Trace{Records: []audit.Record{{
		Version: 1, Sequence: 1, Event: audit.Event{Kind: "effect_outcome", ID: "forged-effect", Outcome: "success"}, Hash: strings.Repeat("b", 64),
	}}}, Source{ID: "audit-source", Label: "structural audit input", Mode: ModeReplay, Health: SourceComplete})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Records[0].Status != StateUnknown || snapshot.Records[0].Authority != AuthorityDiagnostic {
		t.Fatalf("outcome without intent was overclaimed: %#v", snapshot.Records[0])
	}
}

func TestPlainAuditEffectProjectionRequiresOnePriorCompatibleIntent(t *testing.T) {
	makeRecord := func(sequence uint64, kind, id, outcome string) audit.Record {
		return audit.Record{Version: 1, Sequence: sequence, Event: audit.Event{Kind: kind, ID: id, Outcome: outcome}}
	}
	project := func(records ...audit.Record) Snapshot {
		t.Helper()
		snapshot, err := FromVerifiedAuditTrace(audit.Trace{Records: records}, Source{ID: "audit-source", Label: "structural audit input", Mode: ModeReplay, Health: SourceComplete})
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	status := func(snapshot Snapshot, sequence uint64) State {
		t.Helper()
		wantID := fmt.Sprintf("audit-%d", sequence)
		for _, record := range snapshot.Records {
			if record.ID == wantID {
				if record.Authority != AuthorityDiagnostic || record.Verification != VerificationStructuralAudit {
					t.Fatalf("audit projection overclaimed authority: %#v", record)
				}
				return record.Status
			}
		}
		t.Fatalf("projected audit record %q not retained", wantID)
		return StateUnknown
	}

	t.Run("outcome before intent", func(t *testing.T) {
		snapshot := project(makeRecord(1, "effect_outcome", "effect-ordered", "success"), makeRecord(2, "effect_intent", "effect-ordered", ""))
		if got := status(snapshot, 1); got != StateUnknown {
			t.Fatalf("outcome preceding intent status=%q", got)
		}
		if got := status(snapshot, 2); got != StateUnknown {
			t.Fatalf("intent following outcome status=%q", got)
		}
	})

	t.Run("unique prior intent", func(t *testing.T) {
		snapshot := project(makeRecord(4, "effect_intent", "effect-correct", ""), makeRecord(5, "effect_outcome", "effect-correct", "success"))
		if got := status(snapshot, 4); got != StatePending {
			t.Fatalf("intent status=%q", got)
		}
		if got := status(snapshot, 5); got != StateCompleted {
			t.Fatalf("outcome status=%q", got)
		}
	})

	t.Run("duplicate intent poisons tuple", func(t *testing.T) {
		snapshot := project(makeRecord(1, "effect_intent", "effect-duplicate", ""), makeRecord(2, "effect_intent", "effect-duplicate", ""), makeRecord(3, "effect_outcome", "effect-duplicate", "success"))
		for _, sequence := range []uint64{1, 2, 3} {
			if got := status(snapshot, sequence); got != StateUnknown {
				t.Fatalf("duplicate lineage sequence %d status=%q", sequence, got)
			}
		}
	})

	t.Run("conflicting outcome id does not settle another intent", func(t *testing.T) {
		snapshot := project(makeRecord(1, "effect_intent", "effect-left", ""), makeRecord(2, "effect_outcome", "effect-right", "success"))
		if got := status(snapshot, 1); got != StatePending {
			t.Fatalf("unmatched intent status=%q", got)
		}
		if got := status(snapshot, 2); got != StateUnknown {
			t.Fatalf("mismatched effect ID outcome status=%q", got)
		}
	})

	t.Run("duplicate outcomes downgrade both", func(t *testing.T) {
		snapshot := project(makeRecord(1, "effect_intent", "effect-double-outcome", ""), makeRecord(2, "effect_outcome", "effect-double-outcome", "success"), makeRecord(3, "effect_outcome", "effect-double-outcome", "failed"))
		for _, sequence := range []uint64{2, 3} {
			if got := status(snapshot, sequence); got != StateUnknown {
				t.Fatalf("duplicate outcome sequence %d status=%q", sequence, got)
			}
		}
	})

	t.Run("non-success outcome with data is incompatible", func(t *testing.T) {
		records := []audit.Record{
			makeRecord(1, "effect_intent", "effect-invalid-data", ""),
			makeRecord(2, "effect_outcome", "effect-invalid-data", "failed"),
		}
		records[1].Event.Data = []byte("must not be released")
		snapshot := project(records...)
		if got := status(snapshot, 2); got != StateUnknown {
			t.Fatalf("invalid non-success outcome status=%q", got)
		}
	})
}

func TestAuditIntentOutsideVisibleBoundStillRequiresOrderedFullTrace(t *testing.T) {
	records := make([]audit.Record, 0, MaxRecords+1)
	records = append(records, audit.Record{Version: 1, Sequence: 1, Event: audit.Event{Kind: "effect_intent", ID: "effect-before-window"}})
	for sequence := uint64(2); sequence <= MaxRecords; sequence++ {
		records = append(records, audit.Record{Version: 1, Sequence: sequence, Event: audit.Event{Kind: "note", ID: fmt.Sprintf("note-%d", sequence)}})
	}
	records = append(records, audit.Record{Version: 1, Sequence: MaxRecords + 1, Event: audit.Event{Kind: "effect_outcome", ID: "effect-before-window", Outcome: "success"}})
	snapshot, err := FromVerifiedAuditTrace(audit.Trace{Records: records}, Source{ID: "audit-source", Label: "structural audit input", Mode: ModeReplay, Health: SourceComplete})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != MaxRecords {
		t.Fatalf("record projection count=%d want=%d", len(snapshot.Records), MaxRecords)
	}
	for _, record := range snapshot.Records {
		if record.ID == fmt.Sprintf("audit-%d", MaxRecords+1) {
			if record.Status != StateCompleted || record.Authority != AuthorityDiagnostic || record.Verification != VerificationStructuralAudit {
				t.Fatalf("outcome with prior out-of-window intent was misprojected: %#v", record)
			}
			return
		}
	}
	t.Fatal("bounded projection omitted the final outcome")
}

func TestUntrustedDocumentDowngradesAuthorityClaims(t *testing.T) {
	snapshot := testSnapshot(ModeReplay)
	record := testRecord(ModeReplay, "wire-authority", 1)
	record.Authority = AuthorityAuthoritative
	record.Verification = VerificationStructuralAudit
	snapshot.Records = []Record{record}
	snapshot.Actions = []ActionOutput{{Name: "wire-action", State: StateCompleted, Authority: AuthorityAuthoritative, Mode: ModeReplay, Verification: VerificationStructuralAudit}}
	snapshot.Cursor = formatCursor(snapshot.Epoch, 1)
	document := Document{SchemaVersion: DocumentVersion, Snapshot: snapshot}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "\\replay.json"
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReplay(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Records[0].Authority != AuthorityDiagnostic || loaded.Records[0].Verification != VerificationUnverifiedInput || loaded.Actions[0].Authority != AuthorityDiagnostic || loaded.Actions[0].Verification != VerificationUnverifiedInput {
		t.Fatalf("untrusted document claim was not downgraded: %#v %#v", loaded.Records[0], loaded.Actions[0])
	}
}

func TestFixtureScopeRejectsPublishedAndReadyClaims(t *testing.T) {
	snapshot := testSnapshot(ModeFixture)
	snapshot.Sources[0].Mode = ModeFixture
	snapshot.Warnings = []string{"fixture-only diagnostic input"}
	snapshot.Publication.State = PublicationPublished
	if err := ValidateSnapshot(snapshot); err == nil {
		t.Fatal("fixture publication claim was accepted")
	}
	snapshot.Publication.State = PublicationUnavailable
	snapshot.Recovery.State = RecoveryReady
	if err := ValidateSnapshot(snapshot); err == nil {
		t.Fatal("fixture recovery claim was accepted")
	}
}

func TestReplayAndActionAuthorityClaimsAreRejected(t *testing.T) {
	snapshot := testSnapshot(ModeReplay)
	snapshot.Records = []Record{testRecord(ModeReplay, "forged-replay", 1)}
	snapshot.Records[0].Authority = AuthorityAuthoritative
	snapshot.Cursor = formatCursor(snapshot.Epoch, 1)
	if err := ValidateSnapshot(snapshot); err == nil {
		t.Fatal("forged replay record authority was accepted")
	}
	snapshot.Records[0].Authority = AuthorityDiagnostic
	snapshot.Actions = []ActionOutput{{Name: "forged-action", State: StateCompleted, Authority: AuthorityAuthoritative, Mode: ModeReplay}}
	if err := ValidateSnapshot(snapshot); err == nil {
		t.Fatal("forged action authority was accepted")
	}
}

func TestRegisteredSourceHealthUsesOnlySafeIdentityAndPublicMarkers(t *testing.T) {
	registrations := []SourceRegistration{
		{Token: "registered-audit-1", ID: "audit-source-1", Label: "registered audit"},
		{Token: "registered-audit-2", ID: "audit-source-2", Label: "registered transcript"},
	}
	snapshot := FromWebviewRecordsWithRegistrations([]webview.Record{
		{ID: 1, Source: "registered-audit-1", SourceEpoch: "epoch-1", Types: []string{"audit"}, ReceivedAt: time.Now().UTC().Add(-time.Minute)},
		{ID: 2, Source: "registered-audit-1", SourceEpoch: "epoch-2", Types: []string{"source_gap"}, ReceivedAt: time.Now().UTC().Add(-time.Minute)},
		{ID: 3, Source: `C:\private\secret.json`, Types: []string{"proposal"}, ReceivedAt: time.Now().UTC().Add(-time.Minute)},
	}, registrations)
	if len(snapshot.Sources) != 3 {
		t.Fatalf("unexpected source count: %#v", snapshot.Sources)
	}
	var seenGap, seenUnavailable, seenUnknown bool
	for _, source := range snapshot.Sources {
		switch source.ID {
		case "audit-source-1":
			seenGap = source.Health == SourceGap && source.Epoch == "epoch-2" && source.Continuity == "gap"
		case "audit-source-2":
			seenUnavailable = !source.Available && source.Health == SourceUnavailable
		case "webview-unregistered":
			seenUnknown = !source.Available && source.Health == SourceUnavailable
		}
		if strings.Contains(source.Label, "secret") || strings.Contains(source.Label, "private") {
			t.Fatalf("source path leaked into label: %#v", source)
		}
	}
	if !seenGap || !seenUnavailable || !seenUnknown {
		t.Fatalf("source health did not remain honest: %#v", snapshot.Sources)
	}
	for _, record := range snapshot.Records {
		if strings.Contains(record.Summary, "secret") || (record.SourceID == "webview-unregistered" && record.Status != StateUnknown) {
			t.Fatalf("unregistered source was not unknown/sanitized: %#v", record)
		}
	}
}

func TestInvalidSourceLabelsCannotBecomeBrowserIdentities(t *testing.T) {
	snapshot := FromWebviewRecordsWithRegistrations([]webview.Record{{ID: 1, Source: "unsafe", Types: []string{"proposal"}, ReceivedAt: time.Now().UTC().Add(-time.Minute)}}, []SourceRegistration{{Token: "unsafe", ID: "registered-source", Label: `C:\secret\input.json`}})
	if len(snapshot.Records) != 1 || snapshot.Records[0].SourceID != "webview-unregistered" || snapshot.Records[0].Status != StateUnknown {
		t.Fatalf("unsafe source label was treated as registered: %#v", snapshot)
	}
}

func TestEmbeddedClientStartsWithSnapshotThenCursorSSEAndReloadsGaps(t *testing.T) {
	pageBytes, err := page.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	pageText := string(pageBytes)
	loadIndex := strings.Index(pageText, "function loadSnapshot()")
	if loadIndex < 0 {
		t.Fatal("client snapshot loader missing")
	}
	fetchIndex := strings.Index(pageText[loadIndex:], `fetch("/api/v1/snapshot"`)
	connectIndex := strings.Index(pageText[loadIndex:], "rememberSnapshot(snapshot); render(snapshot); connect();")
	if loadIndex < 0 || fetchIndex < 0 || connectIndex < 0 || fetchIndex > connectIndex {
		t.Fatal("client does not establish snapshot-before-cursor SSE ordering")
	}
	for _, required := range []string{"live stream replay gap reported", "observation cursor gap detected", "eventEpoch !== state.epoch", "textContent"} {
		if !strings.Contains(pageText, required) {
			t.Fatalf("client regression marker missing: %q", required)
		}
	}
}

func TestStoreDeduplicatesAndReportsEviction(t *testing.T) {
	snapshot := testSnapshot(ModeReplay)
	store, err := NewStore(snapshot, 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 3; index++ {
		record := testRecord(ModeReplay, "record-"+string(rune('0'+index)), uint64(index))
		if err := store.Append(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Append(testRecord(ModeReplay, "record-3", 99)); err != nil {
		t.Fatal(err)
	}
	current := store.Snapshot()
	if len(current.Records) != 2 || current.Records[0].Sequence != 2 || current.Records[1].Sequence != 3 {
		t.Fatalf("unexpected retained records: %#v", current.Records)
	}
	_, gap := store.SubscribeAfter(current.Epoch + ":0")
	if gap == nil || gap.Reason != "history_evicted" {
		t.Fatalf("expected history gap, got %#v", gap)
	}
}

func TestStoreClosesSlowSubscriber(t *testing.T) {
	snapshot := testSnapshot(ModeReplay)
	store, err := NewStore(snapshot, 16)
	if err != nil {
		t.Fatal(err)
	}
	subscription, gap := store.SubscribeAfter("")
	if gap != nil {
		t.Fatal(gap)
	}
	defer subscription.Close()
	for index := 0; index < SubscriberQueueSize+1; index++ {
		if err := store.Append(testRecord(ModeReplay, "slow-"+string(rune('a'+index)), uint64(index))); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < SubscriberQueueSize; index++ {
		if _, open := <-subscription.Records; !open {
			t.Fatal("slow subscriber closed before queue drained")
		}
	}
	if _, open := <-subscription.Records; open {
		t.Fatal("slow subscriber was not closed")
	}
}

func TestHandlerIsGETOnlyAndHostOriginRestricted(t *testing.T) {
	store, err := NewStore(NewUnavailableSnapshot(), DefaultRecordCapacity)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, HandlerOptions{AllowedHost: "127.0.0.1:8790", AllowedOrigin: "http://127.0.0.1:8790"})
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8790/api/v1/snapshot", nil)
	request.Host = "127.0.0.1:8790"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("snapshot request not secured: %d %#v", response.Code, response.Header())
	}
	post := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8790/api/v1/snapshot", nil)
	post.Host = "127.0.0.1:8790"
	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("mutation method was accepted: %d", postResponse.Code)
	}
	foreign := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8790/api/v1/snapshot", nil)
	foreign.Host = "127.0.0.1:8790"
	foreign.Header.Set("Origin", "https://evil.example")
	foreignResponse := httptest.NewRecorder()
	handler.ServeHTTP(foreignResponse, foreign)
	if foreignResponse.Code != http.StatusForbidden {
		t.Fatalf("foreign origin was accepted: %d", foreignResponse.Code)
	}
}

func TestHandlerSSEAndJSONUseSecurityHeadersAndCursorReplay(t *testing.T) {
	snapshot := testSnapshot(ModeReplay)
	snapshot.Records = []Record{testRecord(ModeReplay, "sse-record", 1)}
	snapshot.Cursor = formatCursor(snapshot.Epoch, 1)
	store, err := NewStore(snapshot, DefaultRecordCapacity)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store, HandlerOptions{}))
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" || response.Header.Get("Content-Security-Policy") == "" {
		response.Body.Close()
		t.Fatalf("snapshot security headers missing: %#v", response.Header)
	}
	response.Body.Close()
	sse, err := http.Get(server.URL + "/api/v1/events?cursor=" + snapshot.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer sse.Body.Close()
	if sse.Header.Get("Cache-Control") != "no-store, no-cache, no-transform" || sse.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("SSE security headers missing: %#v", sse.Header)
	}
	line, err := bufio.NewReader(sse.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, ": ready") {
		t.Fatalf("SSE did not establish: %q %v", line, err)
	}
}

func TestSSESubscriberBoundClosesAdmissionWithGap(t *testing.T) {
	store, err := NewStore(NewUnavailableSnapshot(), DefaultRecordCapacity)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store, HandlerOptions{}))
	defer server.Close()
	responses := make([]*http.Response, 0, MaxSubscribers+1)
	defer func() {
		for _, response := range responses {
			response.Body.Close()
		}
	}()
	for index := 0; index < MaxSubscribers+1; index++ {
		response, requestErr := http.Get(server.URL + "/api/v1/events")
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		responses = append(responses, response)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("SSE subscriber %d status %d", index, response.StatusCode)
		}
		line, readErr := bufio.NewReader(response.Body).ReadString('\n')
		if readErr != nil || !strings.Contains(line, ": ready") {
			t.Fatalf("SSE subscriber %d did not establish: %q %v", index, line, readErr)
		}
	}
	if len(responses) != MaxSubscribers+1 {
		t.Fatal("subscriber bound did not return the bounded overflow connection")
	}
}

func TestDocumentLoaderRefusesSymlink(t *testing.T) {
	directory := t.TempDir()
	target := directory + "\\target.json"
	link := directory + "\\link.json"
	if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := LoadReplay(link); err == nil {
		t.Fatal("symlink observation document was accepted")
	}
}
