package webchat

import (
	"encoding/json"
	"fmt"
	"testing"
)

// wireObject decodes a snapshot as the gateway writes it. A client reads an absent count as a missing key and a stated
// zero as 0, so the count assertions read the JSON rather than the Go value.
func wireObject(t *testing.T, snapshot Snapshot) map[string]any {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return object
}

// wireSession returns the session header as written.
func wireSession(t *testing.T, snapshot Snapshot) map[string]any {
	t.Helper()
	session, ok := wireObject(t, snapshot)["session"].(map[string]any)
	if !ok {
		t.Fatal("snapshot writes no session object")
	}
	return session
}

// wireGovernance returns the governance panel as written.
func wireGovernance(t *testing.T, snapshot Snapshot) map[string]any {
	t.Helper()
	panels, ok := wireObject(t, snapshot)["panels"].(map[string]any)
	if !ok {
		t.Fatal("snapshot writes no panels object")
	}
	governance, ok := panels["governance"].(map[string]any)
	if !ok {
		t.Fatal("snapshot writes no governance object")
	}
	return governance
}

// countField names one count the gateway can write: the object that carries it and its key.
type countField struct {
	object string
	key    string
}

var countFields = []countField{
	{"session", "turn_count"},
	{"governance", "allow_count"},
	{"governance", "deny_count"},
	{"governance", "unknown_count"},
}

// wireCount reads one count as the gateway writes it: its value, and whether the key is present at all.
func wireCount(t *testing.T, snapshot Snapshot, field countField) (any, bool) {
	t.Helper()
	object := wireSession(t, snapshot)
	if field.object == "governance" {
		object = wireGovernance(t, snapshot)
	}
	value, present := object[field.key]
	return value, present
}

// countRows is every JSON value a count can take in a document. An absent count is not stated, a zero is stated,
// and a negative or fractional count is not a count, so the document is refused.
var countRows = []struct {
	name   string
	value  string
	accept bool
	want   string // the wire value; empty means the key is not written
}{
	{"absent", "", true, ""},
	{"zero", "0", true, "0"},
	{"positive", "3", true, "3"},
	{"negative", "-1", false, ""},
	{"fraction", "1.5", false, ""},
}

// countDocument is an otherwise valid fixture document whose one count takes the given JSON value. An empty value omits
// the key.
func countDocument(t *testing.T, field countField, value string) []byte {
	t.Helper()
	session := map[string]json.RawMessage{"status": json.RawMessage(`"pending"`)}
	governance := map[string]json.RawMessage{}
	if value != "" {
		target := session
		if field.object == "governance" {
			target = governance
		}
		target[field.key] = json.RawMessage(value)
	}
	document := map[string]any{
		"schema_version": "tbound-webchat-document/v1",
		"snapshot": map[string]any{
			"schema_version":     "tbound-webchat-observation/v1",
			"epoch":              "e1",
			"cursor":             "e1:0",
			"source":             "fixture",
			"verification_scope": "unverified-input",
			"generated_at":       "2026-10-13T10:00:00Z",
			"session":            session,
			"panels": map[string]any{
				"governance":    governance,
				"provenance":    map[string]any{},
				"publication":   map[string]any{"state": "not-attempted"},
				"recovery":      map[string]any{"state": "not-required"},
				"qualification": map[string]any{"g1": false, "h1": "unexecuted", "containment": "not-established", "claim_bearing": false},
			},
			"streams": []any{},
			"items":   []any{},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return raw
}

// A document states each count on its own. An absent count is written as no key, never as zero; a stated zero stays 0;
// a negative or fractional count is refused.
func TestDocumentCountsAreAbsentOrStated(t *testing.T) {
	for _, field := range countFields {
		for _, row := range countRows {
			t.Run(field.key+"/"+row.name, func(t *testing.T) {
				snapshot, err := decodeDocument(countDocument(t, field, row.value), SourceFixture)
				if !row.accept {
					if err == nil {
						t.Fatalf("%s=%s was accepted, want refusal", field.key, row.value)
					}
					return
				}
				if err != nil {
					t.Fatalf("%s=%s refused: %v", field.key, row.value, err)
				}
				store, err := NewStore(snapshot, DefaultItemCapacity)
				if err != nil {
					t.Fatalf("NewStore: %v", err)
				}
				got, present := wireCount(t, store.Snapshot(), field)
				if row.want == "" {
					if present {
						t.Fatalf("%s is written as %v for an absent count, want no key", field.key, got)
					}
					return
				}
				if !present || fmt.Sprint(got) != row.want {
					t.Fatalf("%s is written as %v (present=%v), want %s", field.key, got, present, row.want)
				}
			})
		}
	}
}

// A receipt records no per-tool verdicts and no session turn count. provider_turns counts provider exchanges, not turns,
// and turn_completed is a flag, so no count is stated, with or without turn fields.
func TestReceiptStatesNoCountItDoesNotRecord(t *testing.T) {
	cases := map[string]string{
		"example receipt": string(exampleReceipt),
		"production receipt with turns": `{"schema_version":"tbound-production-pi-receipt/v1","mode":"production","run_id":"run-turns-1",` +
			`"claim_bearing":false,"task_admitted":true,"turn_completed":true,"provider_turns":3,"proposal_count":2,` +
			`"settlement":"SUCCEEDED","success":true}`,
	}
	for name, raw := range cases {
		snapshot, err := ProjectReceipt([]byte(raw), receiptTestTime)
		if err != nil {
			t.Fatalf("%s: ProjectReceipt: %v", name, err)
		}
		if _, present := wireSession(t, snapshot)["turn_count"]; present {
			t.Fatalf("%s: turn_count is written; a receipt states no session turn count", name)
		}
		for _, key := range []string{"allow_count", "deny_count", "unknown_count"} {
			if _, present := wireGovernance(t, snapshot)[key]; present {
				t.Fatalf("%s: %s is written; a receipt records no per-tool verdicts", name, key)
			}
		}
	}
}

// The unavailable store has no session projection, so it states no count.
func TestUnavailableStateStatesNoCount(t *testing.T) {
	store, err := NewUnavailableStore()
	if err != nil {
		t.Fatalf("NewUnavailableStore: %v", err)
	}
	for _, field := range countFields {
		if got, present := wireCount(t, store.Snapshot(), field); present {
			t.Fatalf("%s is written as %v for an unavailable state, want no key", field.key, got)
		}
	}
}

// The demo fixture states every count, including a real zero, so each is written.
func TestFixtureStatesEveryCount(t *testing.T) {
	store, err := NewFixtureStore()
	if err != nil {
		t.Fatalf("NewFixtureStore: %v", err)
	}
	want := map[string]string{"turn_count": "1", "allow_count": "4", "deny_count": "1", "unknown_count": "0"}
	for _, field := range countFields {
		got, present := wireCount(t, store.Snapshot(), field)
		if !present || fmt.Sprint(got) != want[field.key] {
			t.Fatalf("%s is written as %v (present=%v), want %s", field.key, got, present, want[field.key])
		}
	}
}
