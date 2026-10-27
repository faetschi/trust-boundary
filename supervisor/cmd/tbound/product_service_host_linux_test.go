//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productprovider"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

const productHostTestKey = "offline-product-test-key"

// newProductHostFixture is a product service fixture whose host inputs are installed files: a signed
// registration over one project, its gate policy, a digest-pinned copy of Git, a signed xattr
// calibration and a provider credential. The scanner is described offline; every other input is real.
func newProductHostFixture(t *testing.T, bash bool) *productServiceFixture {
	t.Helper()
	f := newProductServiceFixture(t)
	rules := []gate.Rule{{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow}, {Tool: "edit", Effect: gate.EffectAllow}}
	if bash {
		rules = append(rules, gate.Rule{Tool: "bash", Effect: gate.EffectAllow})
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	policyBytes, err := json.Marshal(policy.Profile())
	if err != nil {
		t.Fatal(err)
	}
	reg := filepath.Join(f.root, "reg")
	policyFile := filepath.Join(reg, "policy.json")
	if err := os.WriteFile(policyFile, policyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	f.cfg.GatePolicyPaths = map[string]string{productDriverProject: policyFile}

	registration, err := productsession.LoadRegistration(f.cfg.RegistrationPath, f.cfg.RegistrationSignaturePath, f.cfg.RegistrationPublicKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	registration.ProviderProfiles = []string{productprovider.OpenCodeGo}
	registration.Projects[0].PolicyDigest = sha256DigestFromBarePin(barePolicyDigest(policy.Digest()))
	registration.Projects[0].Scope = &productsession.ProductScope{Writable: []string{"notes.txt"}, Protected: []string{"protected.txt"}}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signed, signature, err := productsession.SignRegistration(registration, key)
	if err != nil {
		t.Fatal(err)
	}
	for file, content := range map[string][]byte{f.cfg.RegistrationPath: signed,
		f.cfg.RegistrationSignaturePath: []byte(base64.StdEncoding.EncodeToString(signature) + "\n")} {
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeProductDriverFile(t, f.projectDir, "protected.txt", "private project data\n")

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("installed Git is unavailable: %v", err)
	}
	gitBinary, err := os.ReadFile(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.GitPath, f.cfg.GitDigest = filepath.Join(f.root, "git"), digestBytes(gitBinary)
	if err := os.WriteFile(f.cfg.GitPath, gitBinary, 0o700); err != nil {
		t.Fatal(err)
	}

	credentials := filepath.Join(f.root, "credentials")
	if err := os.Mkdir(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentials, productprovider.OpenCodeGo+".key"), []byte(productHostTestKey), 0o600); err != nil {
		t.Fatal(err)
	}

	scanner := piruntime.D09XattrVisibilityMeasurements{FilesystemMagic: 0xef53, MountOptionsDigest: digestBytes([]byte("offline-mount")),
		ScanUID: 1000, ScanGID: 1000, ScanPrivilegeProfile: piruntime.D09XattrScanPrivilegeProfile,
		EffectiveCapabilities: "0000000000000000", NoNewPrivs: true, InitialUserNamespace: true}
	calibration := piruntime.D09XattrEnumerationCalibration{Authority: piruntime.D09XattrBoundedAuthority,
		FilesystemMagic: scanner.FilesystemMagic, MountOptionsDigest: scanner.MountOptionsDigest, ScanUID: scanner.ScanUID,
		ScanGID: scanner.ScanGID, ScanPrivilegeProfile: scanner.ScanPrivilegeProfile,
		SourceAccessProfile: piruntime.D09XattrManagedRWSourceProfile, EnumerationProfile: piruntime.D09XattrEnumerationProfile,
		VisibilityProfileDigest: piruntime.DigestD09XattrVisibilityProfile(scanner.FilesystemMagic, scanner.MountOptionsDigest,
			scanner.ScanUID, scanner.ScanGID, scanner.ScanPrivilegeProfile),
		ProbeCorpusDigest: digestBytes([]byte("offline-probe")), ProbeObservationDigest: digestBytes([]byte("offline-observation")),
		ProbeEntryCount: 1, ProbeXattrNameCount: 1}
	encoded, calibrationSignature, _, err := piruntime.SignD09XattrEnumerationCalibration(calibration, key)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.XattrCalibrationPath = filepath.Join(reg, "xattr.json")
	for file, content := range map[string][]byte{f.cfg.XattrCalibrationPath: encoded,
		f.cfg.XattrCalibrationPath + ".sig": []byte(base64.StdEncoding.EncodeToString(calibrationSignature) + "\n")} {
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original := productHostMeasureXattr
	productHostMeasureXattr = func(*os.File) (piruntime.D09XattrVisibilityMeasurements, error) { return scanner, nil }
	t.Cleanup(func() { productHostMeasureXattr = original })
	return f
}

// productFakeProvider is an OpenAI-compatible chat-completions server that answers its script in order
// and records every request body.
type productFakeProvider struct {
	mu     sync.Mutex
	script []providerbridge.ScriptTurn
	bodies [][]byte
	target *url.URL
	client *http.Transport
}

func newProductFakeProvider(t *testing.T, script []providerbridge.ScriptTurn) *productFakeProvider {
	t.Helper()
	p := &productFakeProvider{script: script, client: http.DefaultTransport.(*http.Transport).Clone()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+productHostTestKey {
			t.Error("the installed credential did not reach the real transport")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		p.mu.Lock()
		n := len(p.bodies)
		p.bodies = append(p.bodies, body)
		p.mu.Unlock()
		if n >= len(p.script) {
			t.Error("unexpected extra provider request")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		turn := p.script[n]
		delta, finish := map[string]any{"role": "assistant", "content": turn.Text}, "stop"
		if turn.Tool != "" {
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": turn.CallID, "type": "function",
				"function": map[string]any{"name": turn.Tool, "arguments": string(turn.Arguments)}}}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta map[string]any, finish any, usage any) {
			encoded, _ := json.Marshal(map[string]any{"id": turn.ResponseID, "model": openrouter.ApprovedModel, "created": 1,
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": usage})
			fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		chunk(delta, nil, nil)
		chunk(map[string]any{}, finish, map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(func() { server.Close(); p.client.CloseIdleConnections() })
	p.target, _ = url.Parse(server.URL)
	return p
}

// RoundTrip serves the one fixed endpoint from the loopback server. Any other URL fails before a dial.
func (p *productFakeProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != openrouter.Endpoint {
		return nil, fmt.Errorf("unexpected provider URL %s", r.URL)
	}
	routed := r.Clone(r.Context())
	routed.URL = &url.URL{Scheme: p.target.Scheme, Host: p.target.Host, Path: r.URL.Path}
	routed.Host = p.target.Host
	response, err := p.client.RoundTrip(routed)
	if response != nil {
		response.Request = r // the broker checks that the response answers the registered endpoint
	}
	return response, err
}

func (p *productFakeProvider) requests() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.bodies...)
}

func (p *productFakeProvider) messages(t *testing.T, request int) []productWireMessage {
	t.Helper()
	var body struct {
		Messages []productWireMessage `json:"messages"`
	}
	if err := json.Unmarshal(p.requests()[request], &body); err != nil {
		t.Fatal(err)
	}
	return body.Messages
}

// routeProductProvider points the real conversation factory at the temporary credential directory and
// at the fake provider. Everything else of the factory runs unchanged.
func routeProductProvider(t *testing.T, f *productServiceFixture, provider *productFakeProvider) {
	t.Helper()
	original := productHostFactory
	productHostFactory = func(id, _ string) (func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error), error) {
		factory, err := productprovider.ConversationFactoryForTest(id, filepath.Join(f.root, "credentials", id+".key"), f.root)
		if err != nil {
			return nil, err
		}
		return func(c providerbridge.Config, j *audit.Journal) (*providerbridge.Conversation, error) {
			c.HTTPTransport = provider
			return factory(c, j)
		}, nil
	}
	t.Cleanup(func() { productHostFactory = original })
}

// startProductHost starts the service through the real host profile, with the fake provider behind the
// real conversation factory. The principal comes from SO_PEERCRED, not the fixture's seam.
func startProductHost(t *testing.T, script []providerbridge.ScriptTurn) (*productServiceFixture, *productService, *productFakeProvider, *bytes.Buffer) {
	t.Helper()
	f := newProductHostFixture(t, false)
	provider := newProductFakeProvider(t, script)
	routeProductProvider(t, f, provider)
	opts := f.options()
	opts.Principal = nil
	opts.Host = newProductServiceHostProfile(f.cfg)
	logs := &bytes.Buffer{}
	opts.Log = logs
	svc := f.start(opts)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("service log:\n%s", logs.String())
		}
	})
	return f, svc, provider, logs
}

func TestProductServiceHostRunsTwoGovernedTurnsThroughTheRealFactory(t *testing.T) {
	f, svc, provider, logs := startProductHost(t, []providerbridge.ScriptTurn{
		{ResponseID: "host-1-write", CallID: "host-call-1", Tool: "write", Arguments: json.RawMessage(`{"path":"notes.txt","content":"first turn\n"}`)},
		{ResponseID: "host-1-denied", CallID: "host-call-2", Tool: "write", Arguments: json.RawMessage(`{"path":"protected.txt","content":"forbidden\n"}`)},
		{ResponseID: "host-1-final", Text: "I wrote the notes and respected the denial."},
		{ResponseID: "host-2-read", CallID: "host-call-3", Tool: "read", Arguments: json.RawMessage(`{"path":"notes.txt"}`)},
		{ResponseID: "host-2-edit", CallID: "host-call-4", Tool: "edit", Arguments: json.RawMessage(`{"path":"notes.txt","edits":[{"oldText":"first turn\n","newText":"second turn\n"}]}`)},
		{ResponseID: "host-2-final", Text: "I read the published notes and changed them."},
	})
	if svc.env.NewCommandRunner != nil || svc.env.SystemPrompt != productSystemPrompt ||
		svc.env.MaxWorkerSteps != int(svc.env.Registration.Limits.Turn.HostCalls) {
		t.Fatal("the host profile left a fixture or default input in place")
	}
	if _, err := svc.env.NewConversation(providerbridge.Config{ProfileID: "unregistered"}, nil); err == nil {
		t.Fatal("a provider profile outside the registration reached the factory")
	}

	session := *f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &productprotocol.CreateSession{
		ProjectID: productDriverProject, PublicationMode: productsession.PublicationReview, RequestKey: "host-create"}}).Session
	if session.ProviderProfile != productprovider.OpenCodeGo {
		t.Fatalf("the session did not default to the registered profile: %+v", session)
	}
	publish := func(turn productprotocol.TurnView, cursor uint64) ([]productprotocol.Event, uint64) {
		events, next := f.observeUntil(f.principalA, session.ID, cursor, kindIs(productprotocol.EventTurnAwaitingReview))
		current := f.getSession(f.principalA, session.ID)
		resolved := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpResolveTurn, ResolveTurn: &productprotocol.ResolveTurn{
			SessionID: session.ID, TurnID: turn.ID, ExpectedRevision: current.Revision, RequestKey: fmt.Sprintf("host-publish-%d", turn.Ordinal),
			CandidateDigest: events[len(events)-1].CandidateDigest, Decision: productsession.DecisionPublish}})
		if resolved.Turn.Outcome != productsession.OutcomePublished {
			t.Fatalf("publication of turn %d: %+v\nlogs: %s", turn.Ordinal, resolved.Turn, logs.String())
		}
		terminal, last := f.observeUntil(f.principalA, session.ID, next, kindIs(productprotocol.EventTurnTerminal))
		return append(events, terminal...), last
	}

	first := f.mustCall(f.principalA, f.submitRequest(session, "host-turn-1", "Write notes.txt, then try the protected file."))
	events, cursor := publish(*first.Turn, 0)
	if got := fileContent(t, f.projectDir, "notes.txt"); got != "first turn\n" {
		t.Fatalf("turn 1 published %q", got)
	}
	if got := fileContent(t, f.projectDir, "protected.txt"); got != "private project data\n" {
		t.Fatalf("the protected path changed: %q", got)
	}
	hint := providerbridge.HintCandidates("scope_protected_path")["0"]
	denied := toolMessage(t, provider.messages(t, 2), "host-call-2")
	if hint == "" || !strings.Contains(denied, `"DENY"`) || !strings.Contains(denied, "scope_protected_path") || !strings.HasSuffix(denied, "\nhint: "+hint) {
		t.Fatalf("the provider did not get the protected-path denial with its hint: %q", denied)
	}
	if !slices.ContainsFunc(events, func(e productprotocol.Event) bool {
		return e.Kind == productprotocol.EventToolResult && e.ToolCallID == "host-call-2" && e.Verdict == "DENY" && e.ReasonCode == "scope_protected_path" && e.Hint == hint
	}) {
		t.Fatalf("the client was not shown the denial and its hint: %v", eventKinds(events))
	}

	session = f.getSession(f.principalA, session.ID)
	second := f.mustCall(f.principalA, f.submitRequest(session, "host-turn-2", "Read the published notes and change first to second."))
	publish(*second.Turn, cursor)
	if got := fileContent(t, f.projectDir, "notes.txt"); got != "second turn\n" {
		t.Fatalf("turn 2 published %q", got)
	}

	requests := provider.requests()
	if len(requests) != 6 {
		t.Fatalf("the provider saw %d requests, want 6", len(requests))
	}
	// Request 4 is the first of turn 2: it carries turn 1's verified history as context.
	prior := provider.messages(t, 3)
	for _, want := range []struct{ role, text string }{{"user", "Write notes.txt, then try the protected file."},
		{"assistant", "I wrote the notes and respected the denial."}, {"user", `outcome "published"`}, {"user", "Read the published notes and change first to second."}} {
		if !slices.ContainsFunc(prior, func(m productWireMessage) bool {
			return m.Role == want.role && m.Content != nil && strings.Contains(*m.Content, want.text)
		}) {
			t.Errorf("turn 2's first request lacks the %s message %q", want.role, want.text)
		}
	}
	if got := toolMessage(t, prior, "host-call-2"); got != denied {
		t.Errorf("turn 2 sees the denial as %q, not as turn 1 released it: %q", got, denied)
	}
	if got := toolMessage(t, provider.messages(t, 4), "host-call-3"); !strings.Contains(got, "first turn") {
		t.Errorf("turn 2's read did not return turn 1's published file: %q", got)
	}

	for _, id := range []string{first.Turn.ID, second.Turn.ID} {
		turn, _ := svc.catalog.Turn(id)
		receipt, err := productsession.DecodeReceipt(turn.Receipt)
		if err != nil || receipt.WorkerKind != productsession.WorkerSupervisorLoop || receipt.Settlement != productsession.SettlementSupervisorLoopClosed ||
			receipt.PublicationTransactionID == "" {
			t.Fatalf("receipt of %s: %+v %v", id, receipt, err)
		}
	}
	if strings.Contains(logs.String(), productHostTestKey) {
		t.Fatal("the credential reached the service log")
	}
	for _, body := range requests {
		if bytes.Contains(body, []byte(productHostTestKey)) {
			t.Fatal("the credential reached a provider request body")
		}
	}
}

// The publication trust checks the live root against the turn's baseline itself: an edit from outside
// the service, made while the review is pending, is never published over.
func TestProductServiceHostTrustRefusesPublicationOverAnExternalEdit(t *testing.T) {
	f, _, _, logs := startProductHost(t, []providerbridge.ScriptTurn{
		{ResponseID: "ext-write", CallID: "ext-call-1", Tool: "write", Arguments: json.RawMessage(`{"path":"notes.txt","content":"candidate\n"}`)},
		{ResponseID: "ext-final", Text: "Written."},
	})
	session := *f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &productprotocol.CreateSession{
		ProjectID: productDriverProject, PublicationMode: productsession.PublicationReview, RequestKey: "ext-create"}}).Session
	turn := *f.mustCall(f.principalA, f.submitRequest(session, "ext-turn", "Write the notes.")).Turn
	events, _ := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnAwaitingReview))
	writeHostFile(t, filepath.Join(f.projectDir, "task.txt"), "edited outside the service\n")
	current := f.getSession(f.principalA, session.ID)
	resolved := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpResolveTurn, ResolveTurn: &productprotocol.ResolveTurn{
		SessionID: session.ID, TurnID: turn.ID, ExpectedRevision: current.Revision, RequestKey: "ext-publish",
		CandidateDigest: events[len(events)-1].CandidateDigest, Decision: productsession.DecisionPublish}})
	if resolved.Turn.Outcome == productsession.OutcomePublished {
		t.Fatalf("the candidate was published over an external edit: %+v", resolved.Turn)
	}
	if f.notesPublished("notes.txt") || fileContent(t, f.projectDir, "task.txt") != "edited outside the service\n" {
		t.Fatal("the publication changed the project despite the refusal")
	}
	if !strings.Contains(logs.String(), "the managed project changed before publication") {
		t.Fatalf("the trust did not refuse the publication itself:\n%s", logs.String())
	}
}

func TestProductServiceHostRefusesAPolicyThatAllowsBashAtStartup(t *testing.T) {
	f := newProductHostFixture(t, true)
	opts := f.options()
	opts.Host = newProductServiceHostProfile(f.cfg)
	_, err := startProductService(f.cfg, opts)
	if err == nil || !strings.Contains(err.Error(), "allows bash") || !strings.Contains(err.Error(), "no contained command runner") {
		t.Fatalf("a bash policy started: %v", err)
	}
	if _, err := os.Stat(f.socketPath); !os.IsNotExist(err) {
		t.Fatal("a refused service opened its socket")
	}
	// Once a contained runner exists, the same policy is accepted. The fixture's runner stands in for it here.
	opts.Host = func(env *productTurnEnv) error {
		env.NewCommandRunner = func(productTurnCommandContext) (productTurnCommandRunner, error) {
			return nil, errProductBashRunnerUnavailable
		}
		return newProductServiceHostProfile(f.cfg)(env)
	}
	svc, err := startProductService(f.cfg, opts)
	if err != nil {
		t.Fatalf("a bash policy with a command runner was refused: %v", err)
	}
	_ = svc.Shutdown()
}

func TestProductServiceHostRefusesUnverifiedInputsBeforeListening(t *testing.T) {
	for name, scenario := range map[string]struct {
		want   string
		mutate func(f *productServiceFixture)
	}{
		"policy differs from the registration digest": {"differs from its registration", func(f *productServiceFixture) {
			writeHostFile(t, f.cfg.GatePolicyPaths[productDriverProject], `{"version":"tbound-policy/v1","rules":[]}`)
		}},
		"policy group-writable": {"protected bounded regular file", func(f *productServiceFixture) {
			if err := os.Chmod(f.cfg.GatePolicyPaths[productDriverProject], 0o660); err != nil {
				t.Fatal(err)
			}
		}},
		"policy a symlink": {"read the gate policy", func(f *productServiceFixture) {
			link := f.cfg.GatePolicyPaths[productDriverProject] + ".link"
			if err := os.Symlink(f.cfg.GatePolicyPaths[productDriverProject], link); err != nil {
				t.Fatal(err)
			}
			f.cfg.GatePolicyPaths[productDriverProject] = link
		}},
		"git bytes differ from the pinned digest": {"pin approved Git executable", func(f *productServiceFixture) {
			f.cfg.GitDigest = digestBytes([]byte("another git"))
		}},
		"git writable by others": {"pin approved Git executable", func(f *productServiceFixture) {
			if err := os.Chmod(f.cfg.GitPath, 0o777); err != nil {
				t.Fatal(err)
			}
		}},
		"calibration signature does not verify": {"verify the xattr calibration", func(f *productServiceFixture) {
			writeHostFile(t, f.cfg.XattrCalibrationPath+".sig", base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)))
		}},
		"calibration bytes differ from the signed ones": {"verify the xattr calibration", func(f *productServiceFixture) {
			writeHostFile(t, f.cfg.XattrCalibrationPath, strings.Replace(readHostFile(t, f.cfg.XattrCalibrationPath), `"probe_entry_count":1`, `"probe_entry_count":2`, 1))
		}},
		"scanner differs from the calibration": {"differs from the signed xattr calibration", func(*productServiceFixture) {
			productHostMeasureXattr = func(*os.File) (piruntime.D09XattrVisibilityMeasurements, error) {
				return piruntime.D09XattrVisibilityMeasurements{}, nil
			}
		}},
		"scanner measured read-only": {"differs from the signed xattr calibration", func(*productServiceFixture) {
			measured, _ := productHostMeasureXattr(nil)
			measured.ReadOnly = true
			productHostMeasureXattr = func(*os.File) (piruntime.D09XattrVisibilityMeasurements, error) { return measured, nil }
		}},
		"no policy path": {"needs git_path", func(f *productServiceFixture) { f.cfg.GatePolicyPaths = nil }},
		"no git":         {"needs git_path", func(f *productServiceFixture) { f.cfg.GitPath = "" }},
		"no calibration": {"needs git_path", func(f *productServiceFixture) { f.cfg.XattrCalibrationPath = "" }},
	} {
		t.Run(name, func(t *testing.T) {
			f := newProductHostFixture(t, false)
			scenario.mutate(f)
			opts := f.options()
			opts.Host = newProductServiceHostProfile(f.cfg)
			svc, err := startProductService(f.cfg, opts)
			if err == nil {
				_ = svc.Shutdown()
				t.Fatal("a host with an unverified input started")
			}
			if !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("refused for the wrong reason, want %q: %v", scenario.want, err)
			}
			if _, statErr := os.Stat(f.socketPath); !os.IsNotExist(statErr) {
				t.Fatal("a refused service opened its socket")
			}
		})
	}
}

func writeHostFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readHostFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestProductServiceHostEnforcesTheCredentialRules(t *testing.T) {
	f := newProductHostFixture(t, false)
	routeProductProvider(t, f, newProductFakeProvider(t, nil))
	opts := f.options()
	opts.Host = newProductServiceHostProfile(f.cfg)
	svc := f.start(opts)
	key := filepath.Join(f.root, "credentials", productprovider.OpenCodeGo+".key")
	config := providerbridge.Config{ProfileID: productprovider.OpenCodeGo}
	for name, prepare := range map[string]func(){
		"group-readable key": func() { _ = os.Chmod(key, 0o640) },
		"absent key":         func() { _ = os.Remove(key) },
	} {
		prepare()
		if _, err := svc.env.NewConversation(config, nil); err == nil || strings.Contains(err.Error(), productHostTestKey) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestProductPublicationTrustRefusesAnUnboundAuthority(t *testing.T) {
	for name, trust := range map[string]*productPublicationTrust{"nil": nil, "empty": {}} {
		if err := trust.CheckAdmission(context.Background()); err == nil {
			t.Errorf("%s trust admitted", name)
		}
		if err := trust.VerifyTransition(context.Background(), delta.PolicyDecision{}, delta.TransitionBinding{}); err == nil {
			t.Errorf("%s trust verified a transition", name)
		}
	}
}

// The offline test seams are fenced at run time by testing.Testing. This also pins that no other
// non-test source names them, so no configuration or service path can reach them.
func TestOfflineProviderSeamsAreNamedOnlyByTheirOwnPackages(t *testing.T) {
	allowed := map[string]bool{
		"internal/broker/openrouter/transport.go":       true,
		"internal/productprovider/credentials_linux.go": true,
		"internal/providerbridge/conversation.go":       true,
	}
	seams := []string{"WithTestTransport", "ConversationFactoryForTest", "HTTPTransport"}
	err := filepath.WalkDir("../..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel("../..", path)
		for _, seam := range seams {
			if strings.Contains(string(raw), seam) && !allowed[filepath.ToSlash(relative)] {
				t.Errorf("%s names the offline test seam %s", relative, seam)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProductServiceHostRefusesAnInvalidPublicationFence(t *testing.T) {
	for _, invalid := range []string{"missing", "symlink", "mode", "nonempty"} {
		t.Run(invalid, func(t *testing.T) {
			f := newProductHostFixture(t, false)
			fence := filepath.Join(f.projectDir, "lost+found")
			switch invalid {
			case "missing", "symlink":
				if err := os.Remove(fence); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if invalid == "symlink" {
					if err := os.Symlink(f.stateRoot, fence); err != nil {
						t.Fatal(err)
					}
				}
			case "mode":
				if err := os.MkdirAll(fence, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(fence, 0775); err != nil {
					t.Fatal(err)
				}
			case "nonempty":
				if err := os.MkdirAll(fence, 0700); err != nil {
					t.Fatal(err)
				}
				writeHostFile(t, filepath.Join(fence, "unexpected"), "data")
			}
			opts := f.options()
			opts.Host = newProductServiceHostProfile(f.cfg)
			svc, err := startProductService(f.cfg, opts)
			if err == nil {
				_ = svc.Shutdown()
				t.Fatal("an invalid publication fence was accepted")
			}
			if !strings.Contains(err.Error(), "publication fence") {
				t.Fatalf("wrong refusal: %v", err)
			}
			if _, err := os.Stat(f.socketPath); !os.IsNotExist(err) {
				t.Fatal("a refused service opened its socket")
			}
		})
	}
}
