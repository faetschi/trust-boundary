//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/projectbundle"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

// productServiceFixture is a product service over a real catalog, a real managed project and the
// scripted provider, reached through its Unix socket. The principal of a connection is whatever the
// test sets in uid, because one test process has one UID.
type productServiceFixture struct {
	t            *testing.T
	root         string
	projectDir   string
	stateRoot    string
	socketPath   string
	cfg          productServiceConfig
	principalA   productsession.Principal
	principalB   productsession.Principal
	unregistered productsession.Principal
	uid          atomic.Int64

	mu     sync.Mutex
	script *productDriverScript
	svc    *productService
}

func (f *productServiceFixture) currentScript() *productDriverScript {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.script
}

// setScript installs the provider script of the next turn.
func (f *productServiceFixture) setScript(script *productDriverScript) {
	f.mu.Lock()
	f.script = script
	f.mu.Unlock()
}

func productServiceTestGit() (privategit.PinnedGit, sessionlaunch.GitProfile, error) {
	var none privategit.PinnedGit
	path, err := exec.LookPath("git")
	if err != nil {
		return none, sessionlaunch.GitProfile{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return none, sessionlaunch.GitProfile{}, err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	_ = file.Close()
	if err != nil {
		return none, sessionlaunch.GitProfile{}, err
	}
	identity, boundary := "sha256:"+hex.EncodeToString(hash.Sum(nil)), "test-only-product-service-private-admin"
	git, err := privategit.PinGitExecutable(path, privategit.GitExecutableApproval{ExpectedDigest: identity, Boundary: boundary,
		Verifier: func(actual privategit.GitExecutableIdentity) error {
			if actual.Digest != identity || actual.Mode&0o022 != 0 {
				return errors.New("synthetic pinned-Git verifier rejected executable bytes/mode")
			}
			return nil
		}})
	return git, sessionlaunch.GitProfile{ExecutableDigest: identity, ExecutionBoundary: boundary}, err
}

func newProductServiceFixture(t *testing.T) *productServiceFixture {
	t.Helper()
	return newProductServiceFixtureWithReviewTimeout(t, 600)
}

func newProductServiceFixtureWithReviewTimeout(t *testing.T, reviewTimeoutSeconds int) *productServiceFixture {
	t.Helper()
	if git, _, err := productServiceTestGit(); err != nil {
		t.Skipf("installed Git is unavailable: %v", err)
	} else {
		_ = git.Close()
	}
	root := privateGovernedTestRoot(t)
	f := &productServiceFixture{t: t, root: root, projectDir: filepath.Join(root, "state", "projects", "managed", "project"), stateRoot: filepath.Join(root, "state"),
		socketPath: filepath.Join(root, "run", "product.sock"), principalA: productsession.Principal(os.Getuid())}
	f.principalB, f.unregistered = f.principalA+1, f.principalA+2
	f.uid.Store(int64(f.principalA))
	for _, dir := range []string{f.stateRoot, filepath.Dir(filepath.Dir(f.projectDir)), filepath.Dir(f.projectDir), filepath.Join(root, "reg")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	source := t.TempDir()
	writeProductDriverFile(t, source, "task.txt", "turn fixture\n")
	var bundle bytes.Buffer
	if _, err := projectbundle.Build(source, projectbundle.BuildOptions{Limits: projectbundle.DefaultLimits()}, &bundle); err != nil {
		t.Fatal(err)
	}
	if _, _, err := projectbundle.Import(&bundle, f.projectDir, projectbundle.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	projectRoot, err := os.Open(f.projectDir)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := recoveryDirectoryIdentityDigest(projectRoot)
	_ = projectRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	unsigned := productsession.Registration{SchemaVersion: productsession.RegistrationSchema, RegistrationID: "reg-service",
		ServiceIdentity: "tbound-product-test", AllowedPrincipals: []productsession.Principal{f.principalA, f.principalB},
		Projects: []productsession.Project{{ProjectID: productDriverProject, RootIdentityDigest: sha256DigestFromBarePin(identity),
			PolicyDigest: sha256DigestFromBarePin(barePolicyDigest(productServicePolicy(t).Digest())), BashOutputPaths: []string{},
			PublicationModes: []string{productsession.PublicationReview, productsession.PublicationAuto}}},
		ProviderProfiles: []string{productDriverProfile},
		Limits: productsession.Limits{Turn: productsession.Budget{HostCalls: 20, Tokens: 20 * 1024, DurationSeconds: 600},
			Session: productsession.Budget{HostCalls: 60, Tokens: 60 * 1024, DurationSeconds: 1800}, MaxTurnsPerSession: 10,
			ReviewTimeoutSeconds: reviewTimeoutSeconds},
		GitDigest: digestBytes([]byte("git")), HostIdentityDigest: digestBytes([]byte("host")), RuntimeDigest: digestBytes([]byte("runtime")), ImageDigest: digestBytes([]byte("image")),
		ExtensionDigest: digestBytes([]byte("extension"))}
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	data, signature, err := productsession.SignRegistration(unsigned, key)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(raw []byte) []byte { return []byte(base64.StdEncoding.EncodeToString(raw) + "\n") }
	reg := filepath.Join(root, "reg")
	for name, content := range map[string][]byte{"registration.json": data, "registration.json.sig": encode(signature),
		"registration.pub": encode(key.Public().(ed25519.PublicKey))} {
		if err := os.WriteFile(filepath.Join(reg, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.cfg = productServiceConfig{SchemaVersion: productServiceConfigSchema, RegistrationPath: filepath.Join(reg, "registration.json"),
		RegistrationSignaturePath: filepath.Join(reg, "registration.json.sig"), RegistrationPublicKeyPath: filepath.Join(reg, "registration.pub"),
		CatalogPath: filepath.Join(f.stateRoot, "catalog.jsonl"), StateRoot: f.stateRoot, SocketPath: f.socketPath,
		WorkerMode: productServiceWorkerSupervisorLoop, Projects: map[string]string{productDriverProject: f.projectDir}, ShutdownGraceSeconds: 20}
	return f
}

func productServicePolicy(t *testing.T) gate.Policy {
	t.Helper()
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow}, {Tool: "edit", Effect: gate.EffectDeny},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// host fills the trusted turn pieces the way a qualified product host would: the fixture trust, a
// pinned Git, the installed policy and the scripted provider.
func (f *productServiceFixture) host() productServiceHost {
	policy := productServicePolicy(f.t)
	return func(env *productTurnEnv) error {
		env.GatePolicies = map[string]gate.Policy{productDriverProject: policy}
		env.StorePolicyDigest, env.MetadataPolicyDigest = governedReplayStorePolicy, governedReplayMetaPolicy
		env.Xattr = workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy,
			Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}
		env.SystemPrompt, env.DeveloperPrompt = governedReplaySystem, governedReplayDeveloper
		env.PinGit = productServiceTestGit
		env.NewConversation = func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
			return providerbridge.NewScriptedConversation(config, journal, f.currentScript())
		}
		env.PublicationTrust = func(authority *SessionOperationAuthority) workflow.Trust {
			return productDriverTrust{authority: authority}
		}
		env.MaxWorkerSteps = 8
		return nil
	}
}

func (f *productServiceFixture) options() productServiceOptions {
	return productServiceOptions{Host: f.host(), ExtraRoots: []string{f.root}, ReviewExpiryEvery: 50 * time.Millisecond,
		Principal: func(*net.UnixConn) (productsession.Principal, error) {
			return productsession.Principal(f.uid.Load()), nil
		}}
}

// start starts a service on the fixture's state. Cleanup stops it if the test did not.
func (f *productServiceFixture) start(opts productServiceOptions) *productService {
	f.t.Helper()
	svc, err := startProductService(f.cfg, opts)
	if err != nil {
		f.t.Fatalf("start the product service: %v", err)
	}
	f.mu.Lock()
	f.svc = svc
	f.mu.Unlock()
	f.t.Cleanup(func() { _ = svc.Shutdown() })
	return svc
}

// call sends one request as principal and returns the response.
func (f *productServiceFixture) call(as productsession.Principal, request productprotocol.Request) productprotocol.Response {
	f.t.Helper()
	f.uid.Store(int64(as))
	request.Version = productprotocol.Version
	conn, err := net.Dial("unix", f.socketPath)
	if err != nil {
		f.t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	if err := productprotocol.WriteFrame(conn, request, productprotocol.MaxRequestBytes); err != nil {
		f.t.Fatal(err)
	}
	var response productprotocol.Response
	if err := productprotocol.ReadFrame(conn, productprotocol.MaxResponseBytes, &response); err != nil {
		f.t.Fatalf("read the %s response: %v", request.Op, err)
	}
	return response
}

func (f *productServiceFixture) mustCall(as productsession.Principal, request productprotocol.Request) productprotocol.Response {
	f.t.Helper()
	response := f.call(as, request)
	if response.Status != productprotocol.StatusOK || response.Error != nil {
		f.t.Fatalf("%s failed: %+v", request.Op, response.Error)
	}
	return response
}

func (f *productServiceFixture) wantError(as productsession.Principal, request productprotocol.Request, code string) {
	f.t.Helper()
	response := f.call(as, request)
	if response.Status != productprotocol.StatusError || response.Error == nil || response.Error.Code != code {
		f.t.Fatalf("%s: status %q error %+v, want %s", request.Op, response.Status, response.Error, code)
	}
	if response.Error.Detail != "" || response.Session != nil || response.Turn != nil || len(response.Events) != 0 || response.Candidate != nil {
		f.t.Fatalf("%s: an error response carries more than its code: %+v", request.Op, response)
	}
}

func (f *productServiceFixture) createSession(as productsession.Principal, mode, key string) productprotocol.SessionView {
	f.t.Helper()
	response := f.mustCall(as, productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &productprotocol.CreateSession{
		ProjectID: productDriverProject, ProviderProfile: productDriverProfile, PublicationMode: mode,
		Budget: productprotocol.Budget{HostCalls: 60, Tokens: 60 * 1024, DurationSeconds: 1800}, RequestKey: key}})
	if response.Session == nil || response.Replayed {
		f.t.Fatalf("create_session: %+v", response)
	}
	return *response.Session
}

func (f *productServiceFixture) getSession(as productsession.Principal, id string) productprotocol.SessionView {
	f.t.Helper()
	response := f.mustCall(as, productprotocol.Request{Op: productprotocol.OpGetSession, GetSession: &productprotocol.SessionRef{SessionID: id}})
	return *response.Session
}

func (f *productServiceFixture) submitRequest(session productprotocol.SessionView, key, prompt string) productprotocol.Request {
	return productprotocol.Request{Op: productprotocol.OpSubmitTurn, SubmitTurn: &productprotocol.SubmitTurn{SessionID: session.ID,
		ExpectedRevision: session.Revision, RequestKey: key, Prompt: prompt}}
}

// submit sends a turn as the fixture's principal A and returns the admitted turn.
func (f *productServiceFixture) submit(session productprotocol.SessionView, key, prompt string, script *productDriverScript) productprotocol.TurnView {
	f.t.Helper()
	f.setScript(script)
	response := f.mustCall(f.principalA, f.submitRequest(session, key, prompt))
	if response.Turn == nil || response.Replayed || response.Turn.State != string(productsession.StateAdmitted) {
		f.t.Fatalf("submit_turn: %+v", response)
	}
	return *response.Turn
}

// observeUntil polls observe from cursor until an event satisfies stop, and returns every event it
// read with the cursor to continue from.
func (f *productServiceFixture) observeUntil(as productsession.Principal, sessionID string, cursor uint64,
	stop func(productprotocol.Event) bool) ([]productprotocol.Event, uint64) {
	f.t.Helper()
	var events []productprotocol.Event
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		response := f.mustCall(as, productprotocol.Request{Op: productprotocol.OpObserve,
			Observe: &productprotocol.Observe{SessionID: sessionID, Cursor: cursor, WaitMillis: 1000}})
		if response.Gap {
			f.t.Fatal("observe reported a gap")
		}
		matched := false
		for i, event := range response.Events {
			if event.Seq != cursor+uint64(i) {
				f.t.Fatalf("event %d has seq %d, want %d", i, event.Seq, cursor+uint64(i))
			}
			events = append(events, event)
			matched = matched || stop(event)
		}
		cursor = response.NextCursor
		if matched {
			return events, cursor
		}
	}
	f.t.Fatalf("observe did not reach the expected event; read %v", eventKinds(events))
	return nil, 0
}

func kindIs(kind string) func(productprotocol.Event) bool {
	return func(event productprotocol.Event) bool { return event.Kind == kind }
}

func eventKinds(events []productprotocol.Event) []string {
	kinds := make([]string, len(events))
	for i, event := range events {
		kinds[i] = event.Kind
	}
	return kinds
}

func (f *productServiceFixture) candidate(as productsession.Principal, sessionID, turnID string) productprotocol.Response {
	return f.call(as, productprotocol.Request{Op: productprotocol.OpGetCandidate,
		GetCandidate: &productprotocol.TurnRef{SessionID: sessionID, TurnID: turnID}})
}

func (f *productServiceFixture) notesPublished(name string) bool {
	f.t.Helper()
	_, err := os.Stat(filepath.Join(f.projectDir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal(err)
	}
	return err == nil
}

// serviceWriteScript is a provider script that writes one file, optionally proposes a denied edit
// and then answers. Identifiers are unique per prefix.
func serviceWriteScript(prefix, path, content, answer string, withDenial bool) *productDriverScript {
	turns := []providerbridge.ScriptTurn{{ResponseID: prefix + "-response-write", CallID: prefix + "-call-write", Tool: "write",
		Arguments: json.RawMessage(fmt.Sprintf(`{"path":%q,"content":%q}`, path, content))}}
	if withDenial {
		turns = append(turns, providerbridge.ScriptTurn{ResponseID: prefix + "-response-edit", CallID: prefix + "-call-edit", Tool: "edit",
			Arguments: json.RawMessage(`{"path":"task.txt","edits":[{"oldText":"turn fixture\n","newText":"changed\n"}]}`)})
	}
	return &productDriverScript{turns: append(turns, providerbridge.ScriptTurn{ResponseID: prefix + "-response-final", Text: answer})}
}

// serviceAnswerScript answers without a tool call.
func serviceAnswerScript(prefix, answer string) *productDriverScript {
	return &productDriverScript{turns: []providerbridge.ScriptTurn{{ResponseID: prefix + "-response-final", Text: answer}}}
}
