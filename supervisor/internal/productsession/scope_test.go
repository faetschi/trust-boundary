//go:build linux

package productsession

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"tbound/supervisor/internal/audit"
)

func notesScope() *ProductScope {
	return &ProductScope{Writable: []string{"notes"}, ReadOnly: []string{"notes/frozen"}, Protected: []string{"notes/secret.txt"}}
}

func withAlphaScope(scope *ProductScope) func(*Registration) {
	return func(r *Registration) { r.Projects[0].Scope = scope }
}

func createSession(t *testing.T, c *Catalog, project, key string, readOnly bool) Session {
	t.Helper()
	s, replayed, err := c.CreateSession(uid1, CreateSessionRequest{ProjectID: project, ProviderProfile: "p1", PublicationMode: "review",
		Budget: bigBudget, RequestKey: key, ReadOnly: readOnly}, b0)
	if err != nil || replayed {
		t.Fatalf("create session %s: %v replayed=%v", key, err, replayed)
	}
	return s
}

// The v2 registration and existing request digests omit absent scope and read-only fields.
func TestScopeFieldsLeaveExistingDigestsUnchanged(t *testing.T) {
	request := CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "auto", Budget: Budget{10, 1000, 60}, RequestKey: "create-1"}
	if got := signedRegistration(t, nil).Digest; got != "sha256:347f1c10cc494b1871fe314e0620b08f66576d004d067195fb96786962ef8bb4" {
		t.Errorf("registration digest without a scope moved: %s", got)
	}
	if got := payloadDigest(opCreateSession, request); got != "sha256:1a17fc1aa5ed68b8248b26a919c2eaf19414420143dce6c77d56fdfdee5b715c" {
		t.Errorf("create_session payload digest without read_only moved: %s", got)
	}
	readOnly := request
	readOnly.ReadOnly = true
	if payloadDigest(opCreateSession, readOnly) == payloadDigest(opCreateSession, request) {
		t.Error("the create_session payload digest does not cover read_only")
	}
	seen := map[string]string{signedRegistration(t, nil).Digest: "no scope"}
	for name, scope := range map[string]*ProductScope{
		"empty scope":  {},
		"notes scope":  notesScope(),
		"read-only a":  {ReadOnly: []string{"a"}},
		"protected a":  {Protected: []string{"a"}},
		"writable a":   {Writable: []string{"a"}},
		"writable a b": {Writable: []string{"a", "b"}},
	} {
		digest := signedRegistration(t, withAlphaScope(scope)).Digest
		if other, dup := seen[digest]; dup {
			t.Errorf("%s shares a registration digest with %s", name, other)
		}
		seen[digest] = name
	}
}

func TestRegistrationScopeValidation(t *testing.T) {
	_, priv := fixtureKey()
	many := make([]string, 129)
	for i := range many {
		many[i] = fmt.Sprintf("d%03d", i)
	}
	for name, scope := range map[string]*ProductScope{
		"unsorted writable":          {Writable: []string{"b", "a"}},
		"unsorted read-only":         {ReadOnly: []string{"b", "a"}},
		"unsorted protected":         {Protected: []string{"b", "a"}},
		"duplicate entry":            {Writable: []string{"a", "a"}},
		"in writable and read-only":  {Writable: []string{"a"}, ReadOnly: []string{"a"}},
		"in writable and protected":  {Writable: []string{"a"}, Protected: []string{"a"}},
		"in read-only and protected": {ReadOnly: []string{"a"}, Protected: []string{"a"}},
		"absolute path":              {Writable: []string{"/etc"}},
		"dot-dot path":               {Protected: []string{"a/../b"}},
		"trailing slash":             {ReadOnly: []string{"a/"}},
		"empty path":                 {Writable: []string{""}},
		"more than 128 entries":      {Writable: many},
		"unsorted no_delete":         {Writable: []string{"a", "b"}, NoDelete: []string{"b", "a"}},
		"non-canonical no_delete":    {Writable: []string{"a"}, NoDelete: []string{"a/"}},
		"more than 128 no_delete":    {NoDelete: many},
	} {
		r := fixtureUnsigned()
		r.Projects[0].Scope = scope
		if _, _, err := SignRegistration(r, priv); !errors.Is(err, ErrRegistration) {
			t.Errorf("%s: err=%v, want ErrRegistration", name, err)
		}
	}
	for name, scope := range map[string]*ProductScope{
		"no scope":          nil,
		"empty scope":       {},
		"notes scope":       notesScope(),
		"nested lists":      {Writable: []string{"src"}, ReadOnly: []string{"src/vendor"}, Protected: []string{"src/vendor/key"}},
		"no_delete overlay": {Writable: []string{"src"}, NoDelete: []string{"src"}},
	} {
		reg := signedRegistration(t, withAlphaScope(scope))
		got := reg.Projects[0].Scope
		if (scope == nil) != (got == nil) || scope != nil && !sameScope(scope, got) {
			t.Errorf("%s: round trip changed the scope: %+v -> %+v", name, scope, got)
		}
	}
}

func TestRegistrationScopeIsStrictJSON(t *testing.T) {
	pub, priv := fixtureKey()
	r := fixtureUnsigned()
	r.Projects[0].Scope = notesScope()
	data, sig, err := SignRegistration(r, priv)
	if err != nil {
		t.Fatal(err)
	}
	for name, tampered := range map[string][]byte{
		"unknown scope field":   bytes.Replace(data, []byte(`"writable":["notes"]`), []byte(`"writable":["notes"],"writeable":["x"]`), 1),
		"duplicate scope field": bytes.Replace(data, []byte(`"writable":["notes"]`), []byte(`"writable":["notes"],"writable":["notes"]`), 1),
		"widened scope":         bytes.Replace(data, []byte(`"writable":["notes"]`), []byte(`"writable":["notes","src"]`), 1),
	} {
		if bytes.Equal(tampered, data) {
			t.Fatalf("%s: the fixture did not change", name)
		}
		if _, err := VerifyRegistration(tampered, sig, pub); !errors.Is(err, ErrRegistration) {
			t.Errorf("%s: err=%v, want ErrRegistration", name, err)
		}
	}
}

func TestReadOnlySessionRoundTripsThroughReplay(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	reg := signedRegistration(t, withAlphaScope(notesScope()))
	c := openAt(t, path, reg)

	type want struct {
		project  string
		readOnly bool
		scope    *ProductScope
	}
	cases := []want{
		{"alpha", false, notesScope()},
		{"alpha", true, &ProductScope{ReadOnly: []string{"notes/frozen"}, Protected: []string{"notes/secret.txt"}}},
		{"beta", false, nil},
		{"beta", true, &ProductScope{}},
	}
	var sessions []Session
	var turns []Turn
	for i, w := range cases {
		s := createSession(t, c, w.project, fmt.Sprintf("s%d", i), w.readOnly)
		if s.ReadOnly != w.readOnly {
			t.Fatalf("case %d: session read_only = %v", i, s.ReadOnly)
		}
		turn := mustSubmit(t, c, uid1, s.ID, 0, fmt.Sprintf("k%d", i), "p")
		if (turn.Scope == nil) != (w.scope == nil) || w.scope != nil && !sameScope(turn.Scope, w.scope) {
			t.Fatalf("case %d (%s, read_only=%v): turn scope %+v, want %+v", i, w.project, w.readOnly, turn.Scope, w.scope)
		}
		if turn.Scope != nil && len(turn.Scope.AdmissionScope().Writable) != 0 && w.readOnly {
			t.Fatalf("case %d: a read-only session kept a writable path: %+v", i, turn.Scope)
		}
		if err := c.AbandonUnlinkedTurn(turn.ID); err != nil {
			t.Fatal(err)
		}
		sessions, turns = append(sessions, s), append(turns, turn)
	}
	// The scope a caller gets is a copy.
	copied, _ := c.Turn(turns[0].ID)
	copied.Scope.Writable[0] = "tampered"
	if again, _ := c.Turn(turns[0].ID); again.Scope.Writable[0] != "notes" {
		t.Fatal("a returned turn aliases the catalog's scope")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openAt(t, path, reg)
	if !reflect.DeepEqual(c.st.sessions, reopened.st.sessions) || !reflect.DeepEqual(c.st.turns, reopened.st.turns) {
		t.Fatal("reopened state differs from the state that wrote the journal")
	}
	for i, s := range sessions {
		got, err := reopened.Session(uid1, s.ID)
		if err != nil || got.ReadOnly != cases[i].readOnly {
			t.Errorf("case %d: reopened session read_only = %v, %v", i, got.ReadOnly, err)
		}
		turn, _ := reopened.Turn(turns[i].ID)
		if (turn.Scope == nil) != (cases[i].scope == nil) || cases[i].scope != nil && !sameScope(turn.Scope, cases[i].scope) {
			t.Errorf("case %d: reopened turn scope %+v, want %+v", i, turn.Scope, cases[i].scope)
		}
	}
}

func TestCreateSessionReadOnlyIsPartOfTheRequestKey(t *testing.T) {
	c, _ := newCatalog(t)
	first := createSession(t, c, "alpha", "same-key", true)
	again, replayed, err := c.CreateSession(uid1, CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "review",
		Budget: bigBudget, RequestKey: "same-key", ReadOnly: true}, b0)
	if err != nil || !replayed || again.ID != first.ID || !again.ReadOnly {
		t.Fatalf("an identical read-only request must replay: %+v %v %v", again, replayed, err)
	}
	_, _, err = c.CreateSession(uid1, CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1", PublicationMode: "review",
		Budget: bigBudget, RequestKey: "same-key"}, b0)
	wantCode(t, err, CodeRequestConflict)
}

// Replay recomputes the request digest from the record, so a record cannot flip read_only without
// the digest the client's request produced.
func TestOpenRefusesASessionRecordWhoseReadOnlyFlagDiffersFromItsDigest(t *testing.T) {
	for name, tc := range map[string]struct{ recorded, digested bool }{
		"read-only record, writable digest": {true, false},
		"writable record, read-only digest": {false, true},
	} {
		path := filepath.Join(privateDir(t), "catalog.jsonl")
		reg := signedRegistration(t, nil)
		c, err := Open(path, reg)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		j, err := audit.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		event := sessionCreatedRecord{header: hdr, SessionID: "psess-" + fmt.Sprintf("%032x", 1), Principal: uid1, ProjectID: "alpha",
			ProviderProfile: "p1", PublicationMode: "review", Budget: bigBudget, ReadOnly: tc.recorded, BaselineDigest: b0, RequestKey: "k",
			PayloadDigest: payloadDigest(opCreateSession, CreateSessionRequest{ProjectID: "alpha", ProviderProfile: "p1",
				PublicationMode: "review", Budget: bigBudget, RequestKey: "k", ReadOnly: tc.digested})}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.Append(audit.Event{Kind: kindSessionCreated, ID: "evidence", Data: data}); err != nil {
			t.Fatalf("%s: append: %v", name, err)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, reg); !errors.Is(err, ErrCode(CodeCatalogCorrupt)) {
			t.Errorf("%s: Open err=%v, want CATALOG_CORRUPT", name, err)
		}
	}
}

// admittedRecord is the record SubmitTurn writes for the first turn of s, with scope as the pin.
func admittedRecord(c *Catalog, s Session, scope *ProductScope) turnAdmittedRecord {
	rp, _ := c.st.reg.project(s.ProjectID)
	const key, prompt = "k", "p"
	return turnAdmittedRecord{header: hdr, TurnID: s.ID + "/turn-1", SessionID: s.ID, Ordinal: 1, Principal: uid1, RequestKey: key,
		PayloadDigest: payloadDigest(opSubmitTurn, SubmitTurnRequest{SessionID: s.ID, RequestKey: key, Prompt: prompt}),
		Prompt:        prompt, PromptDigest: sha256Digest([]byte(prompt)), ProjectID: s.ProjectID, PolicyDigest: rp.PolicyDigest,
		BashOutputPaths: rp.BashOutputPaths, PublicationMode: s.PublicationMode, BaselineDigest: b0,
		Reservation: c.st.reservation(c.st.sessions[s.ID]), SessionRevision: 1, Scope: scope}
}

// The turn's scope is a pin like its policy digest: a record that carries another scope than the
// registration and the session produce is refused, so a turn cannot run under a wider scope.
func TestAdmittedTurnScopeMustMatchTheRegistrationAndTheSession(t *testing.T) {
	for name, tc := range map[string]struct {
		readOnly bool
		scope    *ProductScope
		ok       bool
	}{
		"the project's scope":                   {false, notesScope(), true},
		"no scope although the project has one": {false, nil, false},
		"a wider writable list":                 {false, &ProductScope{Writable: []string{"notes", "src"}, ReadOnly: []string{"notes/frozen"}, Protected: []string{"notes/secret.txt"}}, false},
		"a dropped protected path":              {false, &ProductScope{Writable: []string{"notes"}, ReadOnly: []string{"notes/frozen"}}, false},
		"read-only session with writable paths": {true, notesScope(), false},
		"read-only session emptied":             {true, &ProductScope{ReadOnly: []string{"notes/frozen"}, Protected: []string{"notes/secret.txt"}}, true},
		"a scope that is not valid":             {false, &ProductScope{Writable: []string{"b", "a"}}, false},
	} {
		c := openAt(t, filepath.Join(privateDir(t), "catalog.jsonl"), signedRegistration(t, withAlphaScope(notesScope())))
		s := createSession(t, c, "alpha", "s", tc.readOnly)
		err := c.commit(kindTurnAdmitted, s.ID+"/turn-1", admittedRecord(c, s, tc.scope))
		if tc.ok && err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !tc.ok && !errors.Is(err, ErrCode(CodeInvalidRequest)) {
			t.Errorf("%s: err=%v, want INVALID_REQUEST", name, err)
		}
	}
}

// A new registration may change a project's scope once nothing is unresolved. Earlier turns keep the
// scope they ran under, because their retained evidence only verifies under it.
func TestTurnKeepsItsScopeAcrossARegistrationChange(t *testing.T) {
	path := filepath.Join(privateDir(t), "catalog.jsonl")
	regA := signedRegistration(t, withAlphaScope(notesScope()))
	regB := signedRegistration(t, withAlphaScope(&ProductScope{Writable: []string{"src"}}))
	c := openAt(t, path, regA)
	s := createSession(t, c, "alpha", "s", false)
	first := mustSubmit(t, c, uid1, s.ID, 0, "k1", "p")
	if err := c.AbandonUnlinkedTurn(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c = openAt(t, path, regB)
	again, _ := c.Turn(first.ID)
	if !sameScope(again.Scope, notesScope()) {
		t.Fatalf("the earlier turn lost its scope: %+v", again.Scope)
	}
	second := mustSubmit(t, c, uid1, s.ID, 2, "k2", "p")
	if !sameScope(second.Scope, &ProductScope{Writable: []string{"src"}}) {
		t.Fatalf("the new turn must run under the new registration's scope: %+v", second.Scope)
	}
}

func TestNoDeleteIsPartOfTheScopeIdentity(t *testing.T) {
	plain := &ProductScope{Writable: []string{"src"}}
	guarded := &ProductScope{Writable: []string{"src"}, NoDelete: []string{"src"}}
	if sameScope(plain, guarded) {
		t.Error("a scope with no_delete equals one without")
	}
	if got := guarded.clone(); !sameScope(guarded, got) || &got.NoDelete[0] == &guarded.NoDelete[0] {
		t.Errorf("clone must copy no_delete: %+v", got)
	}
	if got := guarded.AdmissionScope().NoDelete; len(got) != 1 || got[0] != "src" {
		t.Errorf("AdmissionScope drops no_delete: %+v", got)
	}
	if signedRegistration(t, withAlphaScope(plain)).Digest == signedRegistration(t, withAlphaScope(guarded)).Digest {
		t.Error("no_delete must change the registration digest")
	}
}
