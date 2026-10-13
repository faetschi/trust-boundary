package webchat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func validSnapshot() Snapshot {
	now := time.Now().UTC()
	return Snapshot{
		SchemaVersion: ContractVersion,
		Epoch:         "epoch-1",
		Cursor:        "epoch-1:0",
		Source:        SourceFixture,
		Verification:  VerificationUnverifiedInput,
		GeneratedAt:   now,
		Session:       SessionHeader{SessionID: "session-1", Status: SessionPending},
		Panels: Panels{
			Publication:   PublicationPanel{State: PublicationNotAttempted},
			Recovery:      RecoveryPanel{State: RecoveryNotRequired},
			Qualification: QualificationPanel{G1: false, H1: "unexecuted", Containment: "not-established", ClaimBearing: false},
		},
	}
}

func TestUnavailableStoreIsExplicitAndEmpty(t *testing.T) {
	store, err := NewUnavailableStore()
	if err != nil {
		t.Fatalf("NewUnavailableStore: %v", err)
	}
	snapshot := store.Snapshot()
	if snapshot.Source != SourceUnavailable || snapshot.Verification != VerificationUnavailable {
		t.Fatalf("unavailable store source=%q verification=%q", snapshot.Source, snapshot.Verification)
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("unavailable store fabricated %d items", len(snapshot.Items))
	}
	if snapshot.Panels.Qualification.G1 || snapshot.Panels.Qualification.ClaimBearing {
		t.Fatal("unavailable store must not claim qualification")
	}
	if snapshot.Panels.Publication.State != PublicationUnavailable || snapshot.Panels.Recovery.State != RecoveryUnavailable {
		t.Fatal("unavailable store must not claim publication or recovery")
	}
	if len(snapshot.Streams) == 0 || snapshot.Streams[0].Available {
		t.Fatal("unavailable store must expose an unavailable stream, not an available one")
	}
	if len(snapshot.Warnings) == 0 {
		t.Fatal("unavailable store must explain the absence of a source")
	}
}

func TestFixtureStoreDowngradesToUnverifiedDiagnostic(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	snapshot := store.Snapshot()
	if snapshot.Source != SourceFixture || snapshot.Verification != VerificationUnverifiedInput {
		t.Fatalf("fixture store source=%q verification=%q", snapshot.Source, snapshot.Verification)
	}
	if snapshot.Panels.Provenance.Available {
		t.Fatal("a fixture document must not claim verified provenance")
	}
	if snapshot.Panels.Qualification.G1 || snapshot.Panels.Qualification.ClaimBearing {
		t.Fatal("a fixture must not claim qualification")
	}
	sawSuccess := false
	for _, item := range snapshot.Items {
		if item.Authority != AuthorityDiagnostic {
			t.Fatalf("item %q authority=%q, want diagnostic", item.ID, item.Authority)
		}
		if item.Verification != VerificationUnverifiedInput {
			t.Fatalf("item %q verification=%q, want unverified-input", item.ID, item.Verification)
		}
		if item.State == ResultSucceeded {
			sawSuccess = true
		}
	}
	if !sawSuccess {
		t.Fatal("fixture should exercise a success state so the client must downgrade it")
	}
}

func TestValidateSnapshotRefusesAuthoritativeAuthority(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Items = []Item{{
		SchemaVersion: ContractVersion,
		ID:            "msg-1",
		Type:          ItemMessage,
		OccurredAt:    time.Now().UTC(),
		Authority:     AuthorityAuthoritative,
		Verification:  VerificationUnverifiedInput,
		Role:          RoleUser,
		Text:          "hello",
	}}
	if err := ValidateSnapshot(snapshot); err == nil {
		t.Fatal("authoritative authority must be refused at the presentation boundary")
	}
}

func TestPerTypeFieldSmugglingRefused(t *testing.T) {
	cases := map[string]Item{
		"message carrying a verdict": {
			SchemaVersion: ContractVersion, ID: "i1", Type: ItemMessage, OccurredAt: time.Now().UTC(),
			Authority: AuthorityDiagnostic, Role: RoleUser, Text: "hi", Verdict: VerdictAllow,
		},
		"decision without a reason code": {
			SchemaVersion: ContractVersion, ID: "i2", Type: ItemDecision, OccurredAt: time.Now().UTC(),
			Authority: AuthorityDiagnostic, Tool: ToolRead, ToolCallID: "call-1", Verdict: VerdictAllow,
		},
		"result without a state": {
			SchemaVersion: ContractVersion, ID: "i3", Type: ItemResult, OccurredAt: time.Now().UTC(),
			Authority: AuthorityDiagnostic, Tool: ToolRead, ToolCallID: "call-1",
		},
		"activity carrying transcript text": {
			SchemaVersion: ContractVersion, ID: "i4", Type: ItemActivity, OccurredAt: time.Now().UTC(),
			Authority: AuthorityDiagnostic, Kind: "proposal", Tool: ToolRead, Summary: "read", State: ResultPending, Text: "leak",
		},
		"unregistered tool": {
			SchemaVersion: ContractVersion, ID: "i5", Type: ItemDecision, OccurredAt: time.Now().UTC(),
			Authority: AuthorityDiagnostic, Tool: Tool("mcp"), ToolCallID: "call-1", Verdict: VerdictDeny, ReasonCode: "policy_default_deny",
		},
		"unsafe reason code": {
			SchemaVersion: ContractVersion, ID: "i6", Type: ItemDecision, OccurredAt: time.Now().UTC(),
			Authority: AuthorityDiagnostic, Tool: ToolRead, ToolCallID: "call-1", Verdict: VerdictDeny, ReasonCode: "../../etc/passwd",
		},
		"oversized text": {
			SchemaVersion: ContractVersion, ID: "i7", Type: ItemMessage, OccurredAt: time.Now().UTC(),
			Authority: AuthorityDiagnostic, Role: RoleUser, Text: strings.Repeat("x", MaxTextBytes+1),
		},
	}
	for name, item := range cases {
		item.Verification = VerificationUnverifiedInput
		snapshot := validSnapshot()
		snapshot.Items = []Item{item}
		if err := ValidateSnapshot(snapshot); err == nil {
			t.Errorf("%s: expected refusal, got acceptance", name)
		}
	}
}

func TestDocumentRefusals(t *testing.T) {
	base := `{"schema_version":"tbound-webchat-document/v1","snapshot":{"schema_version":"tbound-webchat-observation/v1","epoch":"e1","cursor":"e1:0","source":"fixture","verification_scope":"unverified-input","generated_at":"2026-10-13T10:00:00Z","session":{"status":"pending","turn_count":0},"panels":{"governance":{},"provenance":{},"publication":{"state":"not-attempted"},"recovery":{"state":"not-required"},"qualification":{"g1":false,"h1":"unexecuted","containment":"not-established","claim_bearing":false}},"streams":[],"items":[]}}`
	if _, err := decodeDocument([]byte(base), SourceFixture); err != nil {
		t.Fatalf("baseline document must be accepted: %v", err)
	}

	cases := map[string]string{
		"unknown field":         strings.Replace(base, `"streams":[]`, `"raw_journal":"secret","streams":[]`, 1),
		"containment claim":     strings.Replace(base, `"containment":"not-established"`, `"containment":"established"`, 1),
		"research protocol":     strings.Replace(base, `"h1":"unexecuted"`, `"h1":"executed"`, 1),
		"source mismatch":       strings.Replace(base, `"source":"fixture"`, `"source":"replay"`, 1),
		"live from document":    strings.Replace(base, `"source":"fixture"`, `"source":"live"`, 1),
		"trailing json":         base + `{}`,
		"unexpected item field": strings.Replace(base, `"items":[]`, `"items":[{"schema_version":"tbound-webchat-observation/v1","id":"i1","type":"message","occurred_at":"2026-10-13T10:00:00Z","authority":"diagnostic","role":"user","text":"hi","command":"rm -rf /"}]`, 1),
	}
	for name, raw := range cases {
		if _, err := decodeDocument([]byte(raw), SourceFixture); err == nil {
			t.Errorf("%s: expected refusal, got acceptance", name)
		}
	}

	// A forged authoritative item is downgraded, not trusted.
	forged := strings.Replace(base, `"items":[]`, `"items":[{"schema_version":"tbound-webchat-observation/v1","id":"i1","type":"result","occurred_at":"2026-10-13T10:00:00Z","authority":"authoritative","verification_scope":"structural-audit-only","tool":"read","tool_call_id":"call-1","state":"succeeded"}]`, 1)
	snapshot, err := decodeDocument([]byte(forged), SourceFixture)
	if err != nil {
		t.Fatalf("forged authoritative item should be downgraded, not refused: %v", err)
	}
	if snapshot.Items[0].Authority != AuthorityDiagnostic || snapshot.Items[0].Verification != VerificationUnverifiedInput {
		t.Fatalf("forged item was not downgraded: authority=%q verification=%q", snapshot.Items[0].Authority, snapshot.Items[0].Verification)
	}
}

func TestStoreDeduplicatesAndBroadcasts(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	before := store.Snapshot()
	existing := before.Items[0]

	if err := store.AppendItem(existing); err != nil {
		t.Fatalf("duplicate append: %v", err)
	}
	if after := store.Snapshot(); len(after.Items) != len(before.Items) {
		t.Fatalf("duplicate append grew the store: %d -> %d", len(before.Items), len(after.Items))
	}

	subscription, gap := store.SubscribeAfter(before.Cursor)
	if gap != nil {
		t.Fatalf("unexpected gap: %+v", gap)
	}
	defer subscription.Close()

	added := Item{
		SchemaVersion: ContractVersion,
		ID:            "msg-new",
		Type:          ItemMessage,
		TurnID:        "turn-01",
		OccurredAt:    time.Now().UTC(),
		Authority:     AuthorityDiagnostic,
		Verification:  VerificationUnverifiedInput,
		Role:          RoleAssistant,
		Text:          "new projection element",
	}
	if err := store.AppendItem(added); err != nil {
		t.Fatalf("append: %v", err)
	}
	select {
	case event := <-subscription.Events:
		if event.Item == nil || event.Item.ID != "msg-new" {
			t.Fatalf("unexpected event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber did not receive the appended item")
	}
}

func TestAppendItemRequiresExplicitVerificationScope(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	// An item with no verification scope must fail closed: defaulting it here
	// would let a later reader treat an empty scope as verified.
	item := Item{
		SchemaVersion: ContractVersion,
		ID:            "msg-no-scope",
		Type:          ItemMessage,
		OccurredAt:    time.Now().UTC(),
		Authority:     AuthorityUnknown,
		Role:          RoleAssistant,
		Text:          "no scope",
	}
	if err := store.AppendItem(item); err == nil {
		t.Fatal("an item without an explicit verification scope must be refused")
	}
}

func TestValidateSnapshotRefusesObservedAuthorityOutsideLive(t *testing.T) {
	// `observed` means "projected from the supervisor's durable transition". A
	// fixture or replay observation must never carry it.
	snapshot := validSnapshot()
	snapshot.Items = []Item{{
		SchemaVersion: ContractVersion,
		ID:            "res-observed",
		Type:          ItemResult,
		OccurredAt:    time.Now().UTC(),
		Authority:     AuthorityObserved,
		Verification:  VerificationStructuralAudit,
		Tool:          ToolRead,
		ToolCallID:    "call-1",
		State:         ResultSucceeded,
	}}
	if err := ValidateSnapshot(snapshot); err == nil {
		t.Fatal("a non-live observation must not claim observed authority")
	}
}

func TestLiveObservedSnapshotValidates(t *testing.T) {
	// The live seam: an observed item under a structural-audit scope is the only
	// thing the client will render as confirmed success.
	now := time.Now().UTC()
	snapshot := Snapshot{
		SchemaVersion: ContractVersion,
		Epoch:         "live-epoch-01",
		Cursor:        "live-epoch-01:1",
		Source:        SourceLive,
		Verification:  VerificationStructuralAudit,
		GeneratedAt:   now,
		Session:       SessionHeader{SessionID: "session-live-1", Status: SessionRunning},
		Panels: Panels{
			Publication:   PublicationPanel{State: PublicationPending},
			Recovery:      RecoveryPanel{State: RecoveryNotRequired},
			Qualification: QualificationPanel{G1: false, H1: "unexecuted", Containment: "not-established", ClaimBearing: false},
		},
		Items: []Item{{
			SchemaVersion: ContractVersion,
			ID:            "res-live",
			Type:          ItemResult,
			OccurredAt:    now,
			Authority:     AuthorityObserved,
			Verification:  VerificationStructuralAudit,
			Tool:          ToolRead,
			ToolCallID:    "call-live-1",
			State:         ResultSucceeded,
		}},
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("live observed snapshot must validate: %v", err)
	}
	if _, err := NewStore(snapshot, DefaultItemCapacity); err != nil {
		t.Fatalf("live observed snapshot must be storable: %v", err)
	}
}

func TestEpochAndSequenceAreValidated(t *testing.T) {
	// The epoch is interpolated into the SSE id line, so control characters and
	// separators must be refused.
	bad := validSnapshot()
	bad.Epoch = "epoch\r:id"
	bad.Cursor = "epoch\r:id:0"
	if err := ValidateSnapshot(bad); err == nil {
		t.Fatal("an epoch containing control characters or a colon must be refused")
	}

	// A caller-provided sequence that is not contiguous would be silently
	// renumbered by the store, erasing an input discontinuity.
	gapped := validSnapshot()
	gapped.Items = []Item{
		{SchemaVersion: ContractVersion, Sequence: 3, ID: "i1", Type: ItemMessage, OccurredAt: time.Now().UTC(), Authority: AuthorityDiagnostic, Verification: VerificationUnverifiedInput, Role: RoleUser, Text: "a"},
		{SchemaVersion: ContractVersion, Sequence: 4, ID: "i2", Type: ItemMessage, OccurredAt: time.Now().UTC(), Authority: AuthorityDiagnostic, Verification: VerificationUnverifiedInput, Role: RoleAssistant, Text: "b"},
	}
	if err := ValidateSnapshot(gapped); err == nil {
		t.Fatal("a non-contiguous input sequence must be refused rather than renumbered")
	}
}

func TestCursorSequenceAndActivityRules(t *testing.T) {
	// A cursor that disagrees with the provided item sequence would let the store
	// normalize away a discontinuity, so it is refused.
	mismatched := validSnapshot()
	mismatched.Items = []Item{
		{SchemaVersion: ContractVersion, Sequence: 1, ID: "i1", Type: ItemMessage, OccurredAt: time.Now().UTC(), Authority: AuthorityDiagnostic, Verification: VerificationUnverifiedInput, Role: RoleUser, Text: "a"},
	}
	mismatched.Cursor = mismatched.Epoch + ":9"
	if err := ValidateSnapshot(mismatched); err == nil {
		t.Fatal("a cursor that does not match the item sequence must be refused")
	}

	// A tool activity must be correlatable, so a filter can always retain it
	// beside its decision and result.
	uncorrelated := validSnapshot()
	uncorrelated.Items = []Item{
		{SchemaVersion: ContractVersion, ID: "a1", Type: ItemActivity, OccurredAt: time.Now().UTC(), Authority: AuthorityDiagnostic, Verification: VerificationUnverifiedInput, Kind: "proposal", Tool: ToolRead, Summary: "read", State: ResultPending},
	}
	if err := ValidateSnapshot(uncorrelated); err == nil {
		t.Fatal("an activity without a tool call id must be refused")
	}

	// A session id must be an identifier, so a relative path cannot masquerade as
	// one.
	pathLike := validSnapshot()
	pathLike.Session.SessionID = "home/alice/.ssh/id_rsa"
	if err := ValidateSnapshot(pathLike); err == nil {
		t.Fatal("a path-like session id must be refused")
	}

	// Control characters are refused in display text.
	control := validSnapshot()
	control.Session.Title = "esc\x1b[31m"
	if err := ValidateSnapshot(control); err == nil {
		t.Fatal("display text containing control characters must be refused")
	}
}

func TestSubscriptionPanelsAreIsolated(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	subscription, gap := store.SubscribeAfter("")
	if gap != nil {
		t.Fatalf("unexpected gap: %+v", gap)
	}
	defer subscription.Close()
	before := store.Snapshot().Panels.Governance.AllowCount
	// A subscriber must not be able to mutate retained store state.
	panels := subscription.Panels
	panels.Governance.AllowCount = 999
	after := store.Snapshot().Panels.Governance.AllowCount
	if after != before {
		t.Fatalf("mutating a subscription's panels changed store state: %d -> %d", before, after)
	}
}

func TestSubscriberLimitWithholdsReplay(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	var held []*Subscription
	defer func() {
		for _, subscription := range held {
			subscription.Close()
		}
	}()
	for index := 0; index < MaxSubscribers; index += 1 {
		subscription, gap := store.SubscribeAfter("")
		if gap != nil {
			t.Fatalf("subscription %d refused early: %+v", index, gap)
		}
		held = append(held, subscription)
	}
	over, gap := store.SubscribeAfter("")
	if gap == nil || gap.Reason != "subscriber_limit" {
		t.Fatalf("expected a subscriber_limit gap, got %+v", gap)
	}
	if len(over.Initial) != 0 {
		t.Fatalf("an over-limit subscriber must receive no replay, got %d items", len(over.Initial))
	}
	over.Close()
}

func TestSubscribeAfterGaps(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	snapshot := store.Snapshot()

	cases := map[string]struct {
		cursor     string
		wantReason string
	}{
		"ahead":         {snapshot.Epoch + ":9999", "cursor_ahead"},
		"other epoch":   {"other-epoch:1", "source_epoch_changed"},
		"invalid":       {"not-a-cursor", "invalid_cursor"},
		"history evict": {snapshot.Epoch + ":0", ""},
	}
	for name, tc := range cases {
		subscription, gap := store.SubscribeAfter(tc.cursor)
		if tc.wantReason == "" {
			if name == "history evict" {
				// sequence 0 is retained, so no gap is expected here.
				if gap != nil {
					t.Fatalf("%s: unexpected gap %+v", name, gap)
				}
			}
			subscription.Close()
			continue
		}
		if gap == nil || gap.Reason != tc.wantReason {
			t.Errorf("%s: gap=%+v, want reason %q", name, gap, tc.wantReason)
		}
		subscription.Close()
	}

	// A live subscription must return items strictly after the cursor.
	subscription, gap := store.SubscribeAfter(snapshot.Epoch + ":5")
	if gap != nil {
		t.Fatalf("unexpected gap for retained cursor: %+v", gap)
	}
	defer subscription.Close()
	if len(subscription.Initial) == 0 {
		t.Fatal("expected replayed items after the cursor")
	}
	for _, item := range subscription.Initial {
		if item.Sequence <= 5 {
			t.Fatalf("replayed item %q has sequence %d <= cursor", item.ID, item.Sequence)
		}
	}
}

type orderWriter struct {
	header http.Header
	steps  []string
}

func (w *orderWriter) Header() http.Header { return w.header }
func (w *orderWriter) WriteHeader(int)     {}
func (w *orderWriter) Write(p []byte) (int, error) {
	w.steps = append(w.steps, "write")
	return len(p), nil
}
func (w *orderWriter) Flush() { w.steps = append(w.steps, "flush") }
func (w *orderWriter) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		w.steps = append(w.steps, "clear")
	} else {
		w.steps = append(w.steps, "set")
	}
	return nil
}

func TestBoundedWriteFlushesBeforeClearingDeadline(t *testing.T) {
	writer := &orderWriter{header: http.Header{}}
	if err := boundedWrite(writer, func() error {
		_, err := writer.Write([]byte("payload"))
		return err
	}); err != nil {
		t.Fatalf("boundedWrite: %v", err)
	}
	want := []string{"set", "write", "flush", "clear"}
	if len(writer.steps) != len(want) {
		t.Fatalf("steps=%v, want %v", writer.steps, want)
	}
	for index, step := range want {
		if writer.steps[index] != step {
			t.Fatalf("steps=%v, want %v (flush must precede clearing the deadline)", writer.steps, want)
		}
	}
}

func TestHandlerRejectsMethodsOriginsAndBodies(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	server := httptest.NewServer(NewHandler(store, HandlerOptions{AllowedHost: "", AllowedOrigin: ""}))
	defer server.Close()

	post, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d, want 405", response.StatusCode)
	}
	response.Body.Close()

	forged, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	forged.Header.Set("Origin", "http://evil.example")
	response, err = http.DefaultClient.Do(forged)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d, want 403", response.StatusCode)
	}
	response.Body.Close()

	body, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/snapshot", bytes.NewReader([]byte(strings.Repeat("x", 1024))))
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET with body status=%d, want 400", response.StatusCode)
	}
	response.Body.Close()

	// The read-only surface exposes no control route.
	control, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/sessions/session-1/turns", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(control)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusAccepted {
		t.Fatalf("a control route appears to exist: status=%d", response.StatusCode)
	}
	response.Body.Close()
}

func TestSnapshotAndSSEHeadersAndCursorReplay(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	server := httptest.NewServer(NewHandler(store, HandlerOptions{}))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	csp := response.Header.Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP must not allow inline script/style: %q", csp)
	}
	if response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff header")
	}
	var snapshot Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	response.Body.Close()
	if len(snapshot.Items) == 0 {
		t.Fatal("fixture snapshot has no items")
	}

	// A cursor ahead of the store must produce an explicit gap event.
	gapResponse, err := http.Get(server.URL + "/api/v1/events?cursor=" + snapshot.Epoch + "%3A9999")
	if err != nil {
		t.Fatal(err)
	}
	defer gapResponse.Body.Close()
	lines := readSSELines(t, gapResponse, 4)
	if !strings.Contains(lines[0], ": ready") {
		t.Fatalf("first SSE line=%q, want ready comment", lines[0])
	}
	if !strings.Contains(strings.Join(lines, "\n"), "event: gap") {
		t.Fatalf("no gap event was streamed: %q", strings.Join(lines, "\n"))
	}
}

func TestSSEReplaysItemsAfterCursor(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	server := httptest.NewServer(NewHandler(store, HandlerOptions{}))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/events?cursor=" + store.Snapshot().Epoch + "%3A17")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := readSSELines(t, response, 4)
	if !strings.Contains(lines[0], ": ready") {
		t.Fatalf("first SSE line=%q, want ready comment", lines[0])
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "event: item") {
		t.Fatalf("no item event was streamed: %q", joined)
	}
	last := store.Snapshot().Items[len(store.Snapshot().Items)-1]
	if !strings.Contains(joined, last.ID) {
		t.Fatalf("last item %q was not replayed: %q", last.ID, joined)
	}
}

// readSSELines reads count lines from the response body with a bounded wait.
func readSSELines(t *testing.T, response *http.Response, count int) []string {
	t.Helper()
	type result struct {
		lines []string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		lines := make([]string, 0, count)
		for len(lines) < count && scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			lines = append(lines, line)
		}
		done <- result{lines: lines, err: scanner.Err()}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read SSE: %v", got.err)
		}
		if len(got.lines) < count {
			t.Fatalf("read %d SSE lines, want %d", len(got.lines), count)
		}
		return got.lines
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out reading %d SSE lines", count)
		return nil
	}
}
