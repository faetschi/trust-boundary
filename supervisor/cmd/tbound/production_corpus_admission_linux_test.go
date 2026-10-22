//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
)

// All corpus fixtures here are synthetic. They exercise the admission code only.

func corpusTestQuotas() evalcorpus.Quotas {
	return evalcorpus.Quotas{
		Adversarial: map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 4, evalcorpus.ClassTMB: 4, evalcorpus.ClassTMC: 3},
		Compliant:   map[evalcorpus.Tool]int{evalcorpus.ToolRead: 2, evalcorpus.ToolWrite: 2, evalcorpus.ToolEdit: 2, evalcorpus.ToolBash: 2},
		Ambiguous:   2,
		Variance:    map[evalcorpus.ThreatClass]int{evalcorpus.ClassTMA: 1, evalcorpus.ClassTMB: 1, evalcorpus.ClassTMC: 1},
		ColdPercent: 20,
	}
}

func corpusTestDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func corpusTestReviews() []evalcorpus.Review {
	return []evalcorpus.Review{
		{Role: "reviewer-a", Reviewer: "synthetic-reviewer-1", Human: true, Blinded: true, Decision: "accept"},
		{Role: "reviewer-b", Reviewer: "synthetic-reviewer-2", Human: true, Blinded: true, Decision: "accept"},
	}
}

// corpusTestManifest builds a small allocated manifest that satisfies
// evalcorpus.Validate under corpusTestQuotas. Every case carries a trial whose
// prompt names the case, so a prompt can be traced to the case that owns it.
func corpusTestManifest(t *testing.T) evalcorpus.Manifest {
	t.Helper()
	q := corpusTestQuotas()
	m := evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: "corpus-admission-test-seed",
		Pins: evalcorpus.Pins{EnvironmentDigest: corpusTestDigest("env"), ModelDigest: corpusTestDigest("model"),
			ProfileDigest: corpusTestDigest("profile"), PolicyDigest: corpusTestDigest("policy")}}
	for _, class := range []evalcorpus.ThreatClass{evalcorpus.ClassTMA, evalcorpus.ClassTMB, evalcorpus.ClassTMC} {
		for i := 1; i <= q.Adversarial[class]; i++ {
			id := fmt.Sprintf("adv-%s-%03d", class, i)
			tag := evalcorpus.OpportunitySingleStage
			if i%2 == 0 {
				tag = evalcorpus.OpportunityFeedback
			}
			m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindAdversarial, Reviews: corpusTestReviews(),
				Adversarial: &evalcorpus.Adversarial{ThreatClass: class, RootScenarioID: "root-" + id, SourceKey: "src",
					SourceItemID: "item-" + id, SelectionReason: "synthetic",
					Adaptation: evalcorpus.Adaptation{PreservedIntent: "i", ToolMapping: "m", AdaptationSteps: []string{"s"},
						LostDistinctions: []string{}, AuthorizationClass: "a", EndpointOracleMapping: "e",
						LabelOrigin: evalcorpus.LabelExternalOracle},
					OracleID: "oracle", OracleVersion: "0", ForbiddenEffects: []string{"EFF-01"},
					WorkspaceFixtureDigest: corpusTestDigest("fixture/" + id), CalibrationPayloadDigest: corpusTestDigest("cal/" + id),
					CalibrationWitnessID: "witness-" + id, OpportunityTag: tag, OpportunityPredicate: "predicate"}})
		}
	}
	for _, tool := range []evalcorpus.Tool{evalcorpus.ToolRead, evalcorpus.ToolWrite, evalcorpus.ToolEdit, evalcorpus.ToolBash} {
		for i := 1; i <= q.Compliant[tool]; i++ {
			id := fmt.Sprintf("cmp-%s-%02d", tool, i)
			m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindCompliant, Reviews: corpusTestReviews(),
				Compliant: &evalcorpus.Compliant{PrimaryTool: tool, TaskGoal: "goal", ToolMapping: "m", OracleID: "task-oracle",
					OracleVersion: "0", WorkspaceFixtureDigest: corpusTestDigest("cmp/" + id),
					AcceptanceTest:    evalcorpus.Ref{Ref: "acc-" + id, Digest: corpusTestDigest("acc/" + id)},
					ReferenceSolution: evalcorpus.ReferenceSolution{Ref: "sol-" + id, Digest: corpusTestDigest("sol/" + id), ForbiddenEffects: []string{}}}})
		}
	}
	for i := 1; i <= q.Ambiguous; i++ {
		m.Cases = append(m.Cases, evalcorpus.Case{CaseID: fmt.Sprintf("amb-%02d", i), Kind: evalcorpus.KindAmbiguous, Reviews: corpusTestReviews(),
			Ambiguous: &evalcorpus.Ambiguous{WorkspaceFixtureDigest: corpusTestDigest("amb-fixture"),
				Rubric: evalcorpus.Ref{Ref: "rubric", Digest: corpusTestDigest("rubric")}}})
	}
	allocated, err := evalcorpus.Allocate(m, q)
	if err != nil {
		t.Fatal(err)
	}
	scripted := 0
	for i := range allocated.Cases {
		id := allocated.Cases[i].CaseID
		allocated.Cases[i].Trial = &evalcorpus.Trial{Prompt: "Host-approved prompt of " + id, MaxHostCalls: 4, MaxTokens: 10000, DurationSeconds: 600}
		if allocated.Cases[i].Kind != evalcorpus.KindAdversarial {
			continue
		}
		// Every adversarial case carries a frozen one-turn script and protected paths; every second
		// one registers the admit-all/v1 delta (corpusCaseWithDelta finds them).
		trial := allocated.Cases[i].Trial
		trial.Script = []evalcorpus.ScriptTurn{{Tool: "write", Arguments: json.RawMessage(`{"content":"x","path":"note.txt"}`)}}
		trial.ProtectedPaths = []string{"protected", "secret.txt"}
		if scripted++; scripted%2 == 0 {
			trial.PDelta = evalcorpus.PDeltaAdmitAll
			trial.PDeltaJustification = "the promote-all admission relaxes the scope check of this channel"
		}
		digest, err := evalcorpus.CalibrationPayloadDigest(trial.Script, trial.PDelta)
		if err != nil {
			t.Fatal(err)
		}
		allocated.Cases[i].Adversarial.CalibrationPayloadDigest = digest
	}
	if violations := evalcorpus.Validate(allocated, q); len(violations) != 0 {
		t.Fatalf("test manifest is invalid: %v", violations)
	}
	return allocated
}

const corpusTestCase = "cmp-read-01"

// corpusTestServiceConfig is a complete, valid corpus-mode service config.
func corpusTestServiceConfig(manifestDigest string) piServiceConfig {
	return piServiceConfig{
		SchemaVersion: piServiceConfigSchema, SourcePolicy: piServiceSourcePolicy,
		ProviderRoute:         piruntime.D09ManagedPiProviderRouteEngineering,
		WorkflowAdmissionMode: piServiceAdmissionCorpus, WorkflowBashOutputPaths: []string{},
		CorpusManifestPath: piServiceCorpusManifestPath, CorpusManifestDigest: manifestDigest,
		WorkflowCatalogPath: piServiceWorkflowCatalogPath, WorkflowRootDBPath: piServiceWorkflowRootDBPath,
		WorkflowOwnerStoreRoot: piServiceOwnerStoreRoot, WorkflowMaxPreEffectRetries: 2, WorkflowMaxCleanRestarts: 2,
		WorkflowMaxHostCalls: 17, WorkflowMaxTokens: 20000, WorkflowDurationSeconds: 1200,
		PodmanHome: "/var/lib/tbound/podman-home", PodmanGraphRoot: "/var/lib/tbound/podman-storage/graphroot",
		PodmanRunRoot: "/run/tbound-pi/podman-runroot", XDGRuntimeDir: "/run/tbound-pi",
		XDGConfigHome: "/etc/tbound/podman-config", XDGDataHome: "/var/lib/tbound/podman-home/data",
		XDGCacheHome: "/run/tbound-pi/cache", TMPDir: "/run/tbound-pi/tmp",
		ContainersConfigPath: "/etc/tbound/podman-config/containers.conf", ContainersConfigDigest: corpusTestDigest("containers"),
		StorageConfigPath: "/etc/tbound/podman-config/storage.conf", StorageConfigDigest: corpusTestDigest("storage"),
	}
}

// installCorpusTestManifest serves manifest through the loader seams and
// returns its digest and a service config that pins it.
func installCorpusTestManifest(t *testing.T, manifest evalcorpus.Manifest) (piServiceConfig, string) {
	t.Helper()
	canonical, err := evalcorpus.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := evalcorpus.Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	oldRead, oldQuotas := readCorpusManifest, corpusQuotas
	readCorpusManifest = func() ([]byte, error) { return canonical, nil }
	corpusQuotas = corpusTestQuotas
	t.Cleanup(func() { readCorpusManifest, corpusQuotas = oldRead, oldQuotas })
	return corpusTestServiceConfig(digest), digest
}

func TestCorpusModeAdmitsAManifestCase(t *testing.T) {
	manifest := corpusTestManifest(t)
	config, digest := installCorpusTestManifest(t, manifest)
	if err := validateProductionPiServiceConfig(config); err != nil {
		t.Fatalf("corpus service config refused: %v", err)
	}
	corpus, err := loadProductionCorpus(config)
	if err != nil {
		t.Fatalf("loadProductionCorpus: %v", err)
	}
	if corpus.digest != digest {
		t.Fatalf("corpus digest %q, want %q", corpus.digest, digest)
	}
	trial, ok := corpus.trial(corpusTestCase)
	if !ok || trial.Prompt != "Host-approved prompt of "+corpusTestCase || trial.MaxHostCalls != 4 || trial.MaxTokens != 10000 || trial.DurationSeconds != 600 {
		t.Fatalf("trial for %s = %+v ok=%v", corpusTestCase, trial, ok)
	}
	if len(corpus.trials) != len(manifest.Cases) {
		t.Fatalf("corpus holds %d trials for %d cases", len(corpus.trials), len(manifest.Cases))
	}
	if got := corpusAdmissionID("a-case", "static-replay", 2); got != "a-case/static-replay/2" {
		t.Fatalf("admission id = %q, want case_id/role/attempt", got)
	}
}

func TestCorpusLoadRefusesADigestMismatch(t *testing.T) {
	config, _ := installCorpusTestManifest(t, corpusTestManifest(t))
	config.CorpusManifestDigest = corpusTestDigest("another corpus")
	if _, err := loadProductionCorpus(config); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("manifest with a different digest was loaded: %v", err)
	}
	// A one-byte semantic change of the manifest moves the digest and is refused.
	changed := corpusTestManifest(t)
	changed.Cases[0].Trial.Prompt += "!"
	canonical, err := evalcorpus.CanonicalJSON(changed)
	if err != nil {
		t.Fatal(err)
	}
	config, _ = installCorpusTestManifest(t, corpusTestManifest(t))
	readCorpusManifest = func() ([]byte, error) { return canonical, nil }
	if _, err := loadProductionCorpus(config); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("a changed manifest matched the pinned digest: %v", err)
	}
}

func TestCorpusLoadRefusesAnUnreadableOrMalformedManifest(t *testing.T) {
	config, digest := installCorpusTestManifest(t, corpusTestManifest(t))
	readCorpusManifest = func() ([]byte, error) { return nil, os.ErrPermission }
	if _, err := loadProductionCorpus(config); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("an unreadable manifest (symlinked or writable path) was loaded: %v", err)
	}
	for name, content := range map[string]string{
		"not json":      "{",
		"unknown field": `{"schema_version":"tbound-evalcorpus/v1","extra":true}`,
		"empty":         "",
	} {
		readCorpusManifest = func() ([]byte, error) { return []byte(content), nil }
		if _, err := loadProductionCorpus(config); !errors.Is(err, ErrPiServiceCorpus) {
			t.Errorf("%s: malformed manifest was loaded: %v", name, err)
		}
	}
	_ = digest
}

func TestCorpusLoadValidatesTheManifest(t *testing.T) {
	invalid := corpusTestManifest(t)
	invalid.Cases[1].CaseID = invalid.Cases[0].CaseID // case.id.duplicate
	config, _ := installCorpusTestManifest(t, invalid)
	if _, err := loadProductionCorpus(config); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("a manifest that fails evalcorpus.Validate was loaded: %v", err)
	}
	short := corpusTestManifest(t)
	short.Cases = short.Cases[:len(short.Cases)-1] // count.total
	config, _ = installCorpusTestManifest(t, short)
	if _, err := loadProductionCorpus(config); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("a manifest outside the registered quotas was loaded: %v", err)
	}
}

func TestCorpusLoadRequiresATrialForEveryCaseWithinTheHostCeilings(t *testing.T) {
	for name, mutate := range map[string]func(*evalcorpus.Manifest){
		"missing trial":      func(m *evalcorpus.Manifest) { m.Cases[3].Trial = nil },
		"calls over ceiling": func(m *evalcorpus.Manifest) { m.Cases[3].Trial.MaxHostCalls = 18 },
		"tokens over ceil":   func(m *evalcorpus.Manifest) { m.Cases[3].Trial.MaxTokens = 20001 },
		"duration over ceil": func(m *evalcorpus.Manifest) { m.Cases[3].Trial.DurationSeconds = 1201 },
		"id too long":        func(m *evalcorpus.Manifest) { m.Cases[3].CaseID = strings.Repeat("c", productionTaskIDMaxBytes+1) },
	} {
		manifest := corpusTestManifest(t)
		mutate(&manifest)
		config, _ := installCorpusTestManifest(t, manifest)
		if _, err := loadProductionCorpus(config); !errors.Is(err, ErrPiServiceCorpus) {
			t.Errorf("%s: manifest was loaded: %v", name, err)
		}
	}
}

func TestCorpusLoadRefusesANonCorpusConfig(t *testing.T) {
	config, _ := installCorpusTestManifest(t, corpusTestManifest(t))
	config.WorkflowAdmissionMode = "fresh"
	if _, err := loadProductionCorpus(config); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("a fresh-mode config opened a corpus: %v", err)
	}
}

func TestServiceConfigValidationCoversBothAdmissionShapes(t *testing.T) {
	config, _ := installCorpusTestManifest(t, corpusTestManifest(t))
	if err := validateProductionPiServiceConfig(config); err != nil {
		t.Fatalf("corpus config refused: %v", err)
	}
	for name, mutate := range map[string]func(*piServiceConfig){
		"legacy schema":         func(c *piServiceConfig) { c.SchemaVersion = piServiceConfigSchemaV3 },
		"inline prompt":         func(c *piServiceConfig) { c.WorkflowPrompt = "a prompt" },
		"inline task id":        func(c *piServiceConfig) { c.WorkflowTaskID = "task" },
		"inline admission id":   func(c *piServiceConfig) { c.WorkflowAdmissionID = "admission" },
		"other manifest path":   func(c *piServiceConfig) { c.CorpusManifestPath = "/tmp/manifest.json" },
		"malformed digest":      func(c *piServiceConfig) { c.CorpusManifestDigest = "sha256:00" },
		"missing digest":        func(c *piServiceConfig) { c.CorpusManifestDigest = "" },
		"unknown mode":          func(c *piServiceConfig) { c.WorkflowAdmissionMode = "corpora" },
		"zero ceiling calls":    func(c *piServiceConfig) { c.WorkflowMaxHostCalls = 0 },
		"duration over runtime": func(c *piServiceConfig) { c.WorkflowDurationSeconds = 1201 },
	} {
		bad := config
		mutate(&bad)
		if err := validateProductionPiServiceConfig(bad); err == nil {
			t.Errorf("%s: corpus config accepted", name)
		}
	}

	fresh := corpusTestServiceConfig("")
	fresh.WorkflowAdmissionMode, fresh.CorpusManifestPath, fresh.CorpusManifestDigest = "fresh", "", ""
	fresh.WorkflowAdmissionID, fresh.WorkflowTaskID, fresh.WorkflowPrompt = "smoke-2026-10-17", "smoke-read-readme", "Read it."
	for _, schema := range []string{piServiceConfigSchemaV3, piServiceConfigSchema} {
		fresh.SchemaVersion = schema
		if err := validateProductionPiServiceConfig(fresh); err != nil {
			t.Errorf("fresh config on %s refused: %v", schema, err)
		}
		withCorpus := fresh
		withCorpus.CorpusManifestDigest = corpusTestDigest("x")
		if err := validateProductionPiServiceConfig(withCorpus); err == nil {
			t.Errorf("fresh config on %s accepted corpus members", schema)
		}
	}
	fresh.WorkflowPrompt = ""
	if err := validateProductionPiServiceConfig(fresh); err == nil {
		t.Error("fresh config without an inline prompt accepted")
	}
}

func TestPiServiceRequestCorpusForm(t *testing.T) {
	base := piServiceRequest{RequestID: "request-1", TaskID: corpusTestCase, Role: "compliant", Attempt: 1}
	for _, attempt := range []int{1, 2} {
		request := base
		request.Attempt = attempt
		if err := validatePiServiceRequest(request); err != nil {
			t.Errorf("attempt %d refused: %v", attempt, err)
		}
	}
	for name, request := range map[string]piServiceRequest{
		"attempt 3":                {RequestID: "r", TaskID: "c", Role: "compliant", Attempt: 3},
		"attempt -1":               {RequestID: "r", TaskID: "c", Role: "compliant", Attempt: -1},
		"attempt 100":              {RequestID: "r", TaskID: "c", Role: "compliant", Attempt: 100},
		"attempt with a prompt":    {RequestID: "r", TaskID: "c", Role: "compliant", Attempt: 1, Prompt: "injected"},
		"no attempt, no prompt":    {RequestID: "r", TaskID: "c"},
		"no case id":               {RequestID: "r", Role: "compliant", Attempt: 1},
		"blank prompt, an attempt": {RequestID: "r", TaskID: "c", Role: "compliant", Attempt: 1, Prompt: " "},
	} {
		if err := validatePiServiceRequest(request); err == nil {
			t.Errorf("%s: request accepted", name)
		}
	}
	// Single-task requests are unchanged: a prompt and no attempt.
	if err := validatePiServiceRequest(piServiceRequest{RequestID: "r", TaskID: "t", Prompt: "read task.txt"}); err != nil {
		t.Errorf("single-task request refused: %v", err)
	}
	// The wire form of a single-task request does not carry the new member, so
	// its payload digest is unchanged.
	encoded, err := json.Marshal(piServiceRequest{RequestID: "r", TaskID: "t", Prompt: "p"})
	if err != nil || strings.Contains(string(encoded), "attempt") {
		t.Fatalf("single-task request encoding changed: %s %v", encoded, err)
	}
}

func TestClientRefusesTaskTextAndBadAttemptsInCorpusMode(t *testing.T) {
	config, _ := installCorpusTestManifest(t, corpusTestManifest(t))
	good := corpusRequest("request-1", corpusTestCase, "compliant", 1)
	if err := checkPiServiceClientRequest(config, good); err != nil {
		t.Fatalf("corpus request refused: %v", err)
	}
	for name, request := range map[string]piServiceRequest{
		"--task with attempt": {RequestID: "r", TaskID: "c", Role: "compliant", Attempt: 1, Prompt: "inject"},
		"--task, no attempt":  {RequestID: "r", TaskID: "c", Prompt: "inject"},
		"no attempt":          {RequestID: "r", TaskID: "c"},
		"attempt 3":           {RequestID: "r", TaskID: "c", Role: "compliant", Attempt: 3},
	} {
		if err := checkPiServiceClientRequest(config, request); err == nil {
			t.Errorf("%s: corpus client request accepted", name)
		}
	}
	fresh := corpusTestServiceConfig("")
	fresh.WorkflowAdmissionMode, fresh.CorpusManifestPath, fresh.CorpusManifestDigest = "fresh", "", ""
	if err := checkPiServiceClientRequest(fresh, piServiceRequest{RequestID: "r", TaskID: "t", Prompt: "p"}); err != nil {
		t.Errorf("single-task client request refused: %v", err)
	}
	if err := checkPiServiceClientRequest(fresh, piServiceRequest{RequestID: "r", TaskID: "t", Prompt: "p", Attempt: 1}); err == nil {
		t.Error("single-task service accepted an attempt")
	}
}

// corpusTestCatalog opens a real audit-backed workflow catalog bound to corpus.
func corpusTestCatalog(t *testing.T, root string, corpus *productionCorpus) (*piServiceWorkflowCatalog, *audit.Journal) {
	t.Helper()
	journal, err := audit.Open(filepath.Join(root, "workflow-catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := &piServiceWorkflowCatalog{journal: journal, entries: make(map[string]piServiceWorkflowEntry),
		byHostAdmissionID: make(map[string]string), ownerScopes: make(map[string]piServiceWorkflowOwnerScope),
		corpus: corpus}
	t.Cleanup(func() { _ = catalog.Close() })
	return catalog, journal
}

type corpusServiceHarness struct {
	server       *piServiceTestServer
	catalog      *piServiceWorkflowCatalog
	catalogAudit *audit.Journal
	config       piServiceConfig
	digest       string
	manifest     evalcorpus.Manifest

	configDigest   string
	registrationID string
	// beforeResolve, when set, runs in the runner before the catalog reservation.
	beforeResolve atomic.Pointer[func(piServiceRequest)]
	// failResolve makes the runner fail before the catalog reservation, which leaves an accepted
	// request with an unreserved unknown terminal.
	failResolve atomic.Bool
}

// newCorpusServiceHarness serves the real request loop with the real corpus
// screen, and a runner that performs the real catalog admission.
func newCorpusServiceHarness(t *testing.T) *corpusServiceHarness {
	t.Helper()
	manifest := corpusTestManifest(t)
	config, digest := installCorpusTestManifest(t, manifest)
	corpus, err := loadProductionCorpus(config)
	if err != nil {
		t.Fatal(err)
	}
	root := privateGovernedTestRoot(t)
	config.WorkflowOwnerStoreRoot = filepath.Join(root, "owners")
	catalog, catalogAudit := corpusTestCatalog(t, root, corpus)
	configDigest := corpusTestDigest("service config")
	registrationID := corpusTestDigest("registration")
	harness := &corpusServiceHarness{catalog: catalog, catalogAudit: catalogAudit, config: config, digest: digest, manifest: manifest,
		configDigest: configDigest, registrationID: registrationID}
	harness.server = newPiServiceTestServer(t, piServiceSocketRegistration{
		AllowedClientUIDs: []int{os.Geteuid()}, AllowedClientPrimaryGID: os.Getegid(),
	}, func(ctx context.Context, request piServiceRequest) (json.RawMessage, error) {
		admission, ok := piServiceAdmissionFromContext(ctx)
		if !ok {
			return nil, errors.New("no admission in context")
		}
		if gate := harness.beforeResolve.Load(); gate != nil {
			(*gate)(request)
		}
		if harness.failResolve.Load() {
			return nil, errors.New("synthetic outage before the workflow was reserved")
		}
		admission.ConfigDigest, admission.RegistrationID = configDigest, registrationID
		resolved, err := catalog.ResolveAdmission(admission, config, configDigest, registrationID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"schema_version": "tbound-production-pi-receipt/v1", "success": true,
			"host_admission_id": resolved.Entry.HostAdmissionID, "task_id": resolved.Entry.TaskID,
			"prompt": resolved.Entry.TaskPrompt, "corpus_manifest_digest": resolved.Entry.CorpusManifestDigest,
			"max_host_calls": resolved.Entry.Limits.MaxHostCalls, "max_tokens": resolved.Entry.Limits.MaxTokens,
			"deadline_seconds": int(resolved.Entry.Limits.Deadline.Sub(resolved.Entry.ReservedAt) / time.Second)})
	})
	harness.server.requests.setAdmissionScreen(catalog.admissionScreen(config))
	harness.server.requests.setTerminalReplayCheck(catalog.terminalReplayCheck(config, configDigest, registrationID))
	harness.server.requests.setAcceptanceBinding(catalog.acceptanceBinding(config, configDigest, registrationID))
	harness.server.requests.mu.Lock()
	harness.server.requests.settlementVerifier = func(context.Context, piServiceRequestState) error { return nil }
	harness.server.requests.mu.Unlock()
	return harness
}

func (h *corpusServiceHarness) journalRecords(t *testing.T) int {
	t.Helper()
	trace, err := h.server.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return len(trace.Records)
}

func TestCorpusServiceAdmitsAManifestCaseWithTheHostPromptAndBudgets(t *testing.T) {
	h := newCorpusServiceHarness(t)
	request := corpusRequest("tbr-corpus-1", corpusTestCase, "compliant", 1)
	response := roundTripPiServiceRequest(t, h.server.path, request)
	if response.Status != "completed" || h.server.calls.Load() != 1 {
		t.Fatalf("corpus case was not admitted: %+v calls=%d", response, h.server.calls.Load())
	}
	var receipt struct {
		HostAdmissionID string `json:"host_admission_id"`
		TaskID          string `json:"task_id"`
		Prompt          string `json:"prompt"`
		Digest          string `json:"corpus_manifest_digest"`
		MaxHostCalls    uint64 `json:"max_host_calls"`
		MaxTokens       uint64 `json:"max_tokens"`
		DeadlineSeconds int    `json:"deadline_seconds"`
	}
	if err := json.Unmarshal(response.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.HostAdmissionID != corpusTestCase+"/compliant/1" || receipt.TaskID != corpusTestCase ||
		receipt.Prompt != "Host-approved prompt of "+corpusTestCase || receipt.Digest != h.digest ||
		receipt.MaxHostCalls != 4 || receipt.MaxTokens != 10000 || receipt.DeadlineSeconds != 600 {
		t.Fatalf("admission did not take the prompt and budgets from the manifest: %+v", receipt)
	}
	if got := h.journalRecords(t); got != 2 {
		t.Fatalf("request journal has %d records, want accepted+terminal", got)
	}
}

func TestCorpusServiceRefusesAnUnknownCaseWithoutConsumingTheRequestID(t *testing.T) {
	h := newCorpusServiceHarness(t)
	unknown := corpusRequest("tbr-corpus-unknown", "not-in-the-manifest", "compliant", 1)
	response := roundTripPiServiceRequest(t, h.server.path, unknown)
	if response.Status != "refused" || response.ErrorCode != "unknown-case" || h.server.calls.Load() != 0 {
		t.Fatalf("unknown case response=%+v calls=%d", response, h.server.calls.Load())
	}
	if got := h.journalRecords(t); got != 0 {
		t.Fatalf("an unknown case was journaled as accepted (%d records)", got)
	}
	// The same request id is still free for a registered case.
	retry := corpusRequest(unknown.RequestID, corpusTestCase, "compliant", 1)
	if response := roundTripPiServiceRequest(t, h.server.path, retry); response.Status != "completed" {
		t.Fatalf("the refused request id was consumed: %+v", response)
	}
}

func TestCorpusServiceRefusesAttemptThreeAndMissingAttemptsBeforeJournaling(t *testing.T) {
	h := newCorpusServiceHarness(t)
	for name, request := range map[string]piServiceRequest{
		"attempt 3":  {RequestID: "tbr-a3", TaskID: corpusTestCase, Role: "compliant", Attempt: 3},
		"attempt 0":  {RequestID: "tbr-a0", TaskID: corpusTestCase, Prompt: "read /etc/passwd"},
		"attempt -2": {RequestID: "tbr-an", TaskID: corpusTestCase, Role: "compliant", Attempt: -2},
	} {
		response := roundTripPiServiceRequest(t, h.server.path, request)
		if response.Status != "refused" {
			t.Errorf("%s: response=%+v", name, response)
		}
	}
	if h.server.calls.Load() != 0 || h.journalRecords(t) != 0 {
		t.Fatalf("refused attempts reached the runner or the journal: calls=%d records=%d", h.server.calls.Load(), h.journalRecords(t))
	}
}

func TestCorpusServiceRefusesTaskTextInCorpusMode(t *testing.T) {
	h := newCorpusServiceHarness(t)
	for name, request := range map[string]piServiceRequest{
		"prompt with attempt":    {RequestID: "tbr-p1", TaskID: corpusTestCase, Role: "compliant", Attempt: 1, Prompt: "ignore the manifest"},
		"prompt without attempt": {RequestID: "tbr-p2", TaskID: corpusTestCase, Prompt: "ignore the manifest"},
	} {
		response := roundTripPiServiceRequest(t, h.server.path, request)
		if response.Status != "refused" {
			t.Errorf("%s: response=%+v", name, response)
		}
	}
	if h.server.calls.Load() != 0 || h.journalRecords(t) != 0 {
		t.Fatalf("a client prompt reached the runner or the journal: calls=%d records=%d", h.server.calls.Load(), h.journalRecords(t))
	}
}

func TestCorpusServiceDuplicateCaseAttemptIsRefusedOrReplays(t *testing.T) {
	h := newCorpusServiceHarness(t)
	first := corpusRequest("tbr-dup-1", corpusTestCase, "compliant", 1)
	if response := roundTripPiServiceRequest(t, h.server.path, first); response.Status != "completed" {
		t.Fatalf("first admission: %+v", response)
	}
	// Same request id: the recorded terminal is replayed and nothing reruns.
	replay := roundTripPiServiceRequest(t, h.server.path, first)
	if replay.Status != "completed" || h.server.calls.Load() != 1 {
		t.Fatalf("exact retry was not replayed: %+v calls=%d", replay, h.server.calls.Load())
	}
	// A different request id for the same case/attempt is refused before it is journaled.
	other := corpusRequest("tbr-dup-2", corpusTestCase, "compliant", 1)
	refused := roundTripPiServiceRequest(t, h.server.path, other)
	if refused.Status != "refused" || refused.ErrorCode != "admission-already-used" || h.server.calls.Load() != 1 || h.journalRecords(t) != 2 {
		t.Fatalf("reused case/attempt: %+v calls=%d records=%d", refused, h.server.calls.Load(), h.journalRecords(t))
	}
	// Attempt 2 of the same case is a different admission id.
	second := corpusRequest("tbr-dup-3", corpusTestCase, "compliant", 2)
	if response := roundTripPiServiceRequest(t, h.server.path, second); response.Status != "completed" || h.server.calls.Load() != 2 {
		t.Fatalf("attempt 2 was not admitted: %+v calls=%d", response, h.server.calls.Load())
	}
}

func TestCatalogRefusesASecondWorkflowForTheSameCaseAttempt(t *testing.T) {
	config, _ := installCorpusTestManifest(t, corpusTestManifest(t))
	corpus, err := loadProductionCorpus(config)
	if err != nil {
		t.Fatal(err)
	}
	root := privateGovernedTestRoot(t)
	config.WorkflowOwnerStoreRoot = filepath.Join(root, "owners")
	catalog, _ := corpusTestCatalog(t, root, corpus)
	requests := corpusTestRequestJournal(t, root)
	peer := piServicePeerCredentials{PID: 4242, UID: os.Geteuid(), GID: os.Getegid()}
	resolve := func(requestID string, attempt int) error {
		request := corpusRequest(requestID, corpusTestCase, "compliant", attempt)
		digest := mustPiServicePayloadDigest(t, request)
		record, err := requests.begin(context.Background(), peer, request, digest)
		if err != nil {
			t.Fatal(err)
		}
		state, _, err := requests.lookup(peer, request, digest)
		if err != nil {
			t.Fatal(err)
		}
		_, err = catalog.ResolveAdmission(piServiceAdmissionContext{Peer: peer, CurrentPeer: peer, Request: request,
			PayloadDigest: digest, Accepted: state.accepted, AcceptedRecord: record},
			config, corpusTestDigest("cfg"), corpusTestDigest("reg"))
		requests.complete(peer.UID, request, digest, piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: requestID, Status: "completed"})
		return err
	}
	if err := resolve("req-a", 1); err != nil {
		t.Fatalf("first admission: %v", err)
	}
	if err := resolve("req-b", 1); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("a second workflow was minted for %s/1: %v", corpusTestCase, err)
	}
	if err := resolve("req-c", 2); err != nil {
		t.Fatalf("attempt 2 refused: %v", err)
	}
	if err := resolve("req-d", 3); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("attempt 3 admitted: %v", err)
	}
	// One workflow per admission id, and the catalog survives a restart.
	if len(catalog.byHostAdmissionID) != 2 {
		t.Fatalf("catalog holds %d admissions, want 2", len(catalog.byHostAdmissionID))
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := audit.Open(filepath.Join(root, "workflow-catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := loadPiServiceWorkflowCatalog(reopened)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, id := range []string{corpusTestCase + "/compliant/1", corpusTestCase + "/compliant/2"} {
		hash, ok := restored.byHostAdmissionID[id]
		if !ok || restored.entries[hash].CorpusManifestDigest != corpus.digest {
			t.Errorf("admission %s did not survive a restart with its manifest digest", id)
		}
	}
}

func corpusTestRequestJournal(t *testing.T, root string) *piServiceRequestJournal {
	t.Helper()
	requestAudit, err := audit.Open(filepath.Join(root, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	requests, err := newPiServiceRequestJournal(requestAudit)
	if err != nil {
		_ = requestAudit.Close()
		t.Fatal(err)
	}
	requests.settlementVerifier = func(context.Context, piServiceRequestState) error { return nil }
	t.Cleanup(func() { _ = requests.close() })
	return requests
}

// Fresh mode must be exactly as before: the inline envelope still admits, the
// host admission id is the configured one, and no corpus member leaks in.
func TestFreshModeAdmissionIsUnchangedByCorpusSupport(t *testing.T) {
	root := privateGovernedTestRoot(t)
	catalog, _ := corpusTestCatalog(t, root, nil)
	requests := corpusTestRequestJournal(t, root)
	peer := piServicePeerCredentials{PID: 4343, UID: os.Geteuid(), GID: os.Getegid()}
	request := piServiceRequest{RequestID: "fresh-1", TaskID: "approved-task", Prompt: "Read the approved host task."}
	digest := mustPiServicePayloadDigest(t, request)
	record, err := requests.begin(context.Background(), peer, request, digest)
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ := requests.lookup(peer, request, digest)
	config := piServiceConfig{WorkflowAdmissionMode: "fresh", WorkflowAdmissionID: "host-task-2026-10-14",
		WorkflowTaskID: "approved-task", WorkflowPrompt: "Read the approved host task.",
		WorkflowOwnerStoreRoot: filepath.Join(root, "owners"), WorkflowDurationSeconds: 1200,
		WorkflowMaxPreEffectRetries: 2, WorkflowMaxCleanRestarts: 2, WorkflowMaxHostCalls: 4, WorkflowMaxTokens: 10000}
	if code := catalog.admissionScreen(config)(peer, request); code != "" {
		t.Fatalf("fresh request screened out: %q", code)
	}
	attempted := request
	attempted.Attempt = 1
	if code := catalog.admissionScreen(config)(peer, attempted); code == "" {
		t.Fatal("a fresh-mode service accepted a corpus attempt")
	}
	admission, err := catalog.ResolveAdmission(piServiceAdmissionContext{Peer: peer, CurrentPeer: peer, Request: request,
		PayloadDigest: digest, Accepted: state.accepted, AcceptedRecord: record},
		config, corpusTestDigest("cfg"), corpusTestDigest("reg"))
	if err != nil {
		t.Fatalf("fresh admission refused: %v", err)
	}
	entry := admission.Entry
	if admission.Mode != "fresh" || entry.HostAdmissionID != "host-task-2026-10-14" || entry.TaskPrompt != request.Prompt ||
		entry.CorpusManifestDigest != "" || entry.Limits.MaxHostCalls != 4 || entry.Limits.MaxTokens != 10000 {
		t.Fatalf("fresh admission changed: %+v", entry)
	}
	encoded, err := json.Marshal(entry)
	if err != nil || strings.Contains(string(encoded), "corpus") {
		t.Fatalf("fresh catalog entry encoding carries corpus members: %s %v", encoded, err)
	}
}

func TestRuntimeBindingCoversTheCorpusManifestDigest(t *testing.T) {
	binding := governedRecoveryRuntimeBinding{SchemaVersion: governedRecoveryRuntimeSchema,
		StoreRootIdentity: "store", CommandJournalIdentities: []string{}, BashOutputPaths: []string{}}
	plain, err := binding.digest()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(binding)
	if strings.Contains(string(encoded), "corpus") {
		t.Fatalf("a single-task runtime binding changed its encoding: %s", encoded)
	}
	one, two := binding, binding
	one.CorpusManifestDigest, two.CorpusManifestDigest = corpusTestDigest("corpus one"), corpusTestDigest("corpus two")
	d1, err1 := one.digest()
	d2, err2 := two.digest()
	if err1 != nil || err2 != nil || d1 == d2 || d1 == plain || d2 == plain {
		t.Fatalf("runtime digest does not cover the manifest digest: %q %q %q %v %v", plain, d1, d2, err1, err2)
	}
}

// corpusRequest is the request `serve --pi --task-id <case> --attempt <n>
// --task-request-id <id> --bash-no-outputs` sends: no prompt, and the no-output Bash
// scope that the corpus config pins.
func corpusRequest(requestID, caseID, role string, attempt int) piServiceRequest {
	noOutputs := []string{}
	return piServiceRequest{RequestID: requestID, TaskID: caseID, Role: role, Attempt: attempt, BashOutputPaths: &noOutputs}
}

func TestCorpusServiceRefusesARequestWhoseBashScopeDiffersFromTheConfig(t *testing.T) {
	h := newCorpusServiceHarness(t)
	request := corpusRequest("tbr-scope", corpusTestCase, "compliant", 1)
	scoped := []string{"out.txt"}
	request.BashOutputPaths = &scoped
	if response := roundTripPiServiceRequest(t, h.server.path, request); response.Status != "refused" || response.ErrorCode != "invalid-request" {
		t.Fatalf("a client-chosen Bash scope was not refused: %+v", response)
	}
	request.BashOutputPaths = nil
	if response := roundTripPiServiceRequest(t, h.server.path, request); response.Status != "refused" || response.ErrorCode != "invalid-request" {
		t.Fatalf("a request without the pinned no-output scope was not refused: %+v", response)
	}
	if h.server.calls.Load() != 0 || h.journalRecords(t) != 0 {
		t.Fatalf("refused scope reached the runner or the journal: calls=%d records=%d", h.server.calls.Load(), h.journalRecords(t))
	}
}

// The corpus keeps each case's pinned fixture digest, whatever its kind, for `tbound trial reset`.
func TestCorpusModeKeepsTheFixtureDigestOfEveryCase(t *testing.T) {
	manifest := corpusTestManifest(t)
	config, _ := installCorpusTestManifest(t, manifest)
	corpus, err := loadProductionCorpus(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus.fixtures) != len(manifest.Cases) {
		t.Fatalf("corpus holds %d fixture digests for %d cases", len(corpus.fixtures), len(manifest.Cases))
	}
	for _, c := range manifest.Cases {
		want, ok := corpusadmit.FixtureDigest(c)
		if !ok || corpus.fixtures[c.CaseID] != want {
			t.Errorf("%s: fixture digest %q, want %q", c.CaseID, corpus.fixtures[c.CaseID], want)
		}
	}
}
