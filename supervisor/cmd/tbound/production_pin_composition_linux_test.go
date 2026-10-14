//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

const (
	productionPinFixtureHelperEnv = "TBOUND_PRODUCTION_PIN_FIXTURE_HELPER"
	productionPinFixtureRootEnv   = "TBOUND_PRODUCTION_PIN_FIXTURE_ROOT"
	productionPinFixtureCaseEnv   = "TBOUND_PRODUCTION_PIN_FIXTURE_CASE"
	productionPinFixtureWorkflow  = "workflow-production-pin-roundtrip"
	productionPinFixtureTaskID    = "production-pin-task"
	productionPinFixturePrompt    = "Read the prepared fixture file."
)

// This test runs its production initializer helper in an environment with an
// explicit safe-variable allowlist and a fixed dummy provider credential. The
// real initializer can construct its production conversation, but the fixture
// never calls NextTurn and therefore cannot send a provider request.
func TestProductionFreshAndRecoveryDigestComposition(t *testing.T) {
	if os.Getenv(productionPinFixtureHelperEnv) == "1" {
		runProductionPinFixtureHelper(t)
		return
	}
	cases := []struct {
		name, marker string
	}{
		{name: "roundtrip", marker: "PRODUCTION_PIN_ROUNDTRIP=checkpoint-runtime-and-private-git-bound"},
		{name: "expired-deadline", marker: "PRODUCTION_FRESH_DEADLINE_REJECTED"},
		{name: "uncertain-boundary-authorization", marker: "PRODUCTION_FRESH_AUTHORIZATION_UNCERTAIN"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.MkdirTemp(home, "tbound-production-pins-")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestProductionFreshAndRecoveryDigestComposition$")
			command.Env = []string{
				"PATH=/usr/bin:/bin",
				"HOME=" + root,
				"TMPDIR=" + root,
				"TBOUND_OPENROUTER_API_KEY=fixture-only-never-transmitted-key",
				"TBOUND_OPENROUTER_MODEL=" + providerbridge.ApprovedModel,
				"GIT_CONFIG_NOSYSTEM=1",
				"GIT_CONFIG_GLOBAL=/dev/null",
				"GIT_TERMINAL_PROMPT=0",
				productionPinFixtureHelperEnv + "=1",
				productionPinFixtureRootEnv + "=" + root,
				productionPinFixtureCaseEnv + "=" + testCase.name,
			}
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("isolated production %s fixture failed: %v\n%s", testCase.name, err, output)
			}
			if !strings.Contains(string(output), testCase.marker) {
				t.Fatalf("isolated helper did not report %q:\n%s", testCase.marker, output)
			}
		})
	}
}

func runProductionPinFixtureHelper(t *testing.T) {
	t.Helper()
	fixtureCase := os.Getenv(productionPinFixtureCaseEnv)
	if fixtureCase == "" {
		fixtureCase = "roundtrip"
	}
	if fixtureCase != "roundtrip" && fixtureCase != "expired-deadline" &&
		fixtureCase != "uncertain-boundary-authorization" {
		t.Fatalf("unknown production pin fixture case %q", fixtureCase)
	}
	root := os.Getenv(productionPinFixtureRootEnv)
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatal("isolated production fixture root is not absolute and clean")
	}
	paths := map[string]string{
		"source": filepath.Join(root, "source"), "store": filepath.Join(root, "store"),
		"admin": filepath.Join(root, "admin"), "rootdir": filepath.Join(root, "recovery-root"),
		"ownerstore": filepath.Join(root, "owner-store"),
	}
	for _, name := range []string{"source", "store", "admin", "rootdir", "ownerstore"} {
		if err := os.Mkdir(paths[name], 0o700); err != nil {
			t.Fatal(err)
		}
	}
	paths["rootdb"] = filepath.Join(paths["rootdir"], "workflow-root.sqlite")
	if err := os.WriteFile(filepath.Join(paths["source"], "fixture.txt"), []byte("real prepared source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workflow := productionPinFixtureWorkflow
	storeJournalPath := filepath.Join(root, "store-audit.jsonl")
	providerJournalPath := filepath.Join(root, "provider-audit.jsonl")
	commandJournalPath := filepath.Join(root, "command-audit.jsonl")
	catalogJournalPath := filepath.Join(root, "workflow-catalog.jsonl")
	storeJournal := openProductionPinFixtureJournal(t, storeJournalPath)
	providerJournal := openProductionPinFixtureJournal(t, providerJournalPath)
	commandJournal := openProductionPinFixtureJournal(t, commandJournalPath)
	catalogJournal := openProductionPinFixtureJournal(t, catalogJournalPath)

	storePolicyDigest, metadataPolicyDigest := governedReplayStorePolicy, governedReplayMetaPolicy
	xattr := workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy, Complete: true}
	policy, err := productionGatePolicy()
	if err != nil {
		t.Fatal(err)
	}
	runner := newProductionCommandRunner(func(string) (sessionrepo.CommandRunner, error) {
		return nil, ErrProductionBashRunnerUnavailable
	})
	authority, err := NewSessionOperationAuthority(storeJournal, providerJournal, workflow,
		"conversation-production-pin-roundtrip", "production-pin-profile", policy,
		storePolicyDigest, metadataPolicyDigest, runner)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot, err := os.Open(paths["store"])
	if err != nil {
		t.Fatal(err)
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(), xattr, nil)
	_ = storeRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	store := managed.Store()
	source, err := os.Open(paths["source"])
	if err != nil {
		t.Fatal(err)
	}
	baseline, seedErr := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	_ = source.Close()
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	if err := authority.BindBaseline(store, baseline); err != nil {
		t.Fatal(err)
	}
	adminRoot, err := os.Open(paths["admin"])
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sessionlaunch.OpenAdminParent(adminRoot)
	_ = adminRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	git, gitProfile := pinGovernedTestGit(t)
	prepared, err := sessionlaunch.Prepare(context.Background(), managed, baseline, admin, git, gitProfile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	if err := authority.BindPreparedSession(prepared); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().UTC().Add(45 * time.Minute).Round(0)
	if fixtureCase == "expired-deadline" {
		deadline = time.Now().UTC().Add(-45 * time.Minute).Round(0)
	}
	limits := workflowrecovery.Limits{MaxPreEffectRetries: 2, MaxCleanRestarts: 2,
		MaxHostCalls: 4, MaxTokens: 4096, Deadline: deadline}
	admissionHash := digestBytes([]byte("production-pin-roundtrip-admission"))
	entry := piServiceWorkflowEntry{
		SchemaVersion: workflowCatalogSchema, HostAdmissionID: "production-pin-admission",
		AdmissionRecordHash: admissionHash, AdmissionSequence: 1,
		PeerUID: maxInt(1, os.Geteuid()), PeerGID: maxInt(1, os.Getegid()), PeerPID: os.Getpid(),
		RequestID: "production-pin-request", TaskID: productionPinFixtureTaskID,
		PayloadDigest: digestBytes([]byte("payload")), ServiceConfigDigest: digestBytes([]byte("service-config")),
		ServiceRegistrationID: digestBytes([]byte("service-registration")),
		WorkflowID:            workflow, RunID: "run-production-pin-roundtrip",
		SessionID: "session-production-pin-roundtrip", ConversationID: "conversation-production-pin-roundtrip",
		OwnerDBPath:   filepath.Join(paths["ownerstore"], "workflow.sqlite"),
		StoreRootPath: paths["store"], AdminRootPath: paths["admin"],
		StoreJournalPath: storeJournalPath, ProviderJournalPath: providerJournalPath,
		CommandJournalPath: commandJournalPath, TaskPrompt: productionPinFixturePrompt,
		TaskDigest:   digestBytes([]byte("catalog task identity")),
		PromptDigest: digestBytes([]byte(productionPinFixturePrompt)),
		Limits:       limits, ReservedAt: time.Now().UTC().Round(0),
	}
	if !validWorkflowCatalogEntry(entry) {
		t.Fatalf("fixture entry is not a valid production workflow catalog record: %+v", entry)
	}
	reservedEvent, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema,
		AdmissionHash: admissionHash, Entry: entry})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogJournal.Append(audit.Event{Kind: "pi_workflow_admission_reserved",
		ID: workflowCatalogEventID("reserved", admissionHash), Data: reservedEvent}); err != nil {
		t.Fatal(err)
	}
	catalog := &piServiceWorkflowCatalog{journal: catalogJournal,
		entries:           map[string]piServiceWorkflowEntry{admissionHash: entry},
		byHostAdmissionID: map[string]string{entry.HostAdmissionID: admissionHash},
		ownerScopes:       make(map[string]piServiceWorkflowOwnerScope)}
	var freshPolicy workflowrecovery.FreshWorkflowPolicy = catalog
	var uncertainPolicy *productionPinClockUncertainPolicy
	if fixtureCase == "uncertain-boundary-authorization" {
		uncertainPolicy = &productionPinClockUncertainPolicy{}
		freshPolicy = uncertainPolicy
	}
	boundary, err := workflowrecovery.NewLinuxHostBoundary(paths["ownerstore"], uint32(os.Geteuid()),
		verticalNoPriorOwnerDrain{}, freshPolicy)
	if err != nil {
		t.Fatal(err)
	}
	protectedRoot, err := workflowrecovery.CreateFreshProtectedRoot(paths["rootdb"], paths["ownerstore"], boundary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = protectedRoot.Close() })
	owner, err := workflowrecovery.CreateFreshSQLiteOwner(entry.OwnerDBPath, protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	candidateEpoch, err := owner.NextOwnerEpoch(context.Background(), workflow)
	if err != nil || candidateEpoch != 1 {
		t.Fatalf("fresh owner candidate epoch=%d err=%v; want one", candidateEpoch, err)
	}
	admission := &productionWorkflowAdmission{Mode: "fresh", Entry: entry, Catalog: catalog,
		Owner: owner, Root: protectedRoot, CandidateEpoch: candidateEpoch, OwnerIncarnation: owner.OwnerIncarnation()}

	d06Digest, privateGitDigest, repositoryName, err := prepared.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	storeTrace, err := storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	preInitializerGitPin, preInitializerGitErr := productionRecoveryGitPin(storeTrace, workflowrecovery.Pins{
		D06ProvenanceDigest:      bareWorkflowDigest(d06Digest),
		PrivateGitIdentityDigest: bareWorkflowDigest(privateGitDigest),
	})
	if preInitializerGitErr != nil || preInitializerGitPin.RepositoryName != repositoryName ||
		preInitializerGitPin.ProvenanceDigest != d06Digest ||
		preInitializerGitPin.RepositoryIdentityDigest != privateGitDigest || preInitializerGitPin.VerifyIdentity == nil {
		t.Errorf("production recovery binder did not resolve real prepared D06/private-Git pins: pin=%+v err=%v",
			preInitializerGitPin, preInitializerGitErr)
	}

	storeRootForInitializer, err := os.Open(paths["store"])
	if err != nil {
		t.Fatal(err)
	}
	adminRootForInitializer, err := os.Open(paths["admin"])
	if err != nil {
		_ = storeRootForInitializer.Close()
		t.Fatal(err)
	}
	profile := piruntime.HostProfile{
		Digest:                digestBytes([]byte("host profile")),
		ProviderProfile:       "production-pin-profile",
		ProviderProfileDigest: digestBytes([]byte("provider profile")),
		PiRuntimeBundleDigest: digestBytes([]byte("Pi runtime bundle")),
	}
	template := piruntime.D09CellTemplate{ExpectedImageDigest: digestBytes([]byte("D09 image"))}
	storeOptions := sessionrepo.Options{PolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest,
		Limits: workspace.DefaultLimits(), XattrVisibility: xattr, Journal: storeJournal}
	conversation, initErr := initializeProductionFreshWorkflow(context.Background(), productionServeConfig{}, admission,
		profile, template, storePolicyDigest, metadataPolicyDigest, managed, storeRootForInitializer,
		adminRootForInitializer, storeJournal, providerJournal, commandJournal, prepared, baseline,
		storeOptions, runner, authority, "isolated test system prompt", "isolated test developer prompt")
	_ = storeRootForInitializer.Close()
	_ = adminRootForInitializer.Close()
	if fixtureCase == "expired-deadline" || fixtureCase == "uncertain-boundary-authorization" {
		if conversation != nil {
			conversation.Close()
			t.Error("failed production fresh initialization returned a provider conversation")
		}
		if fixtureCase == "expired-deadline" && !errors.Is(initErr, workflowrecovery.ErrDeadline) {
			t.Errorf("production fresh initializer accepted or misclassified the expired absolute deadline: %v", initErr)
		}
		if fixtureCase == "uncertain-boundary-authorization" &&
			(initErr == nil || !uncertainPolicy.called || !strings.Contains(initErr.Error(), workflowrecovery.ErrFreshAuthorization.Error())) {
			t.Errorf("production fresh initializer did not fail closed on boundary clock uncertainty: called=%v err=%v",
				uncertainPolicy.called, initErr)
		}
		checkpoint, head, exists, loadErr := owner.LoadCheckpoint(context.Background(), workflow)
		if loadErr != nil || exists || head != "" || checkpoint.Pins.WorkflowID != "" {
			t.Errorf("failed production fresh initialization created a durable checkpoint: exists=%v head=%q pins=%+v err=%v",
				exists, head, checkpoint.Pins, loadErr)
		}
		providerTrace, traceErr := providerJournal.Trace()
		if traceErr != nil || len(providerTrace.Records) != 0 || len(providerTrace.Effects) != 0 {
			t.Errorf("failed production fresh initialization reached provider authority: records=%d effects=%d err=%v",
				len(providerTrace.Records), len(providerTrace.Effects), traceErr)
		}
		if fixtureCase == "expired-deadline" {
			t.Log("PRODUCTION_FRESH_DEADLINE_REJECTED before fresh root authorization; no checkpoint/provider effect")
		} else {
			t.Log("PRODUCTION_FRESH_AUTHORIZATION_UNCERTAIN boundary returned ErrClockUncertain; initializer remained fail-closed")
		}
		return
	}
	if initErr != nil || conversation == nil {
		t.Fatalf("production fresh initializer did not return its production provider conversation: conversation=%v err=%v",
			conversation != nil, initErr)
	}
	defer conversation.Close()
	providerTraceAfterInit, traceErr := providerJournal.Trace()
	if traceErr != nil || len(providerTraceAfterInit.Records) != 0 || len(providerTraceAfterInit.Effects) != 0 {
		t.Fatalf("production conversation construction made a provider exchange: records=%d effects=%d err=%v",
			len(providerTraceAfterInit.Records), len(providerTraceAfterInit.Effects), traceErr)
	}
	checkpoint, checkpointHead, exists, loadErr := owner.LoadCheckpoint(context.Background(), workflow)
	if loadErr != nil || !exists || checkpointHead == "" {
		t.Errorf("production fresh initializer did not persist its SQLite checkpoint before provider construction: exists=%v head=%q err=%v",
			exists, checkpointHead, loadErr)
		return
	}
	if !validBareWorkflowDigest(checkpoint.Pins.RuntimeDigest) {
		t.Errorf("production fresh initializer stored a non-bare or malformed runtime digest %q", checkpoint.Pins.RuntimeDigest)
	}
	if checkpoint.Pins.WorkflowID != workflow || checkpoint.Pins.SessionID != entry.SessionID ||
		checkpoint.Pins.D06ProvenanceDigest != bareWorkflowDigest(d06Digest) ||
		checkpoint.Pins.PrivateGitIdentityDigest != bareWorkflowDigest(privateGitDigest) ||
		len(checkpoint.Pins.ProviderTranscripts) != 1 || checkpoint.Pins.ProviderTranscripts[0].ConversationID != entry.ConversationID {
		t.Errorf("durable fresh checkpoint did not preserve exact catalog, Git, and first transcript pins: %+v", checkpoint.Pins)
	}
	resolvedGitPin, resolvedGitErr := productionRecoveryGitPin(storeTrace, checkpoint.Pins)
	if resolvedGitErr != nil || resolvedGitPin.RepositoryName != repositoryName ||
		resolvedGitPin.ProvenanceDigest != d06Digest ||
		resolvedGitPin.RepositoryIdentityDigest != privateGitDigest || resolvedGitPin.VerifyIdentity == nil {
		t.Errorf("production recovery binder did not round-trip checkpoint-authenticated D06/private-Git identity: pin=%+v err=%v",
			resolvedGitPin, resolvedGitErr)
	}
	invalidDigests := map[string]string{
		"prefixed":     "sha256:" + checkpoint.Pins.PrivateGitIdentityDigest,
		"uppercase":    strings.ToUpper(checkpoint.Pins.PrivateGitIdentityDigest),
		"wrong-length": checkpoint.Pins.PrivateGitIdentityDigest[:63],
		"nonhex":       strings.Repeat("z", 64),
	}
	for _, digestField := range []struct {
		name string
		set  func(*workflowrecovery.Pins, string)
	}{
		{name: "D06 provenance", set: func(pins *workflowrecovery.Pins, value string) { pins.D06ProvenanceDigest = value }},
		{name: "private-Git identity", set: func(pins *workflowrecovery.Pins, value string) { pins.PrivateGitIdentityDigest = value }},
	} {
		for label, invalid := range invalidDigests {
			invalidPins := checkpoint.Pins
			digestField.set(&invalidPins, invalid)
			if _, err := productionRecoveryGitPin(storeTrace, invalidPins); !errors.Is(err, workflowrecovery.ErrMissing) {
				t.Errorf("production recovery binder accepted %s %s pin %q: %v", label, digestField.name, invalid, err)
			}
		}
	}
	incarnation := admission.OwnerIncarnation
	if !validWorkflowOwnerIncarnation(incarnation) {
		t.Errorf("production workflow catalog rejected real owner-incarnation identity: %+v", incarnation)
	}
	for _, digestField := range []struct {
		name string
		get  func(workflowrecovery.OwnerIncarnation) string
		set  func(*workflowrecovery.OwnerIncarnation, string)
	}{
		{name: "incarnation", get: func(value workflowrecovery.OwnerIncarnation) string { return value.Digest },
			set: func(value *workflowrecovery.OwnerIncarnation, digest string) { value.Digest = digest }},
		{name: "cgroup", get: func(value workflowrecovery.OwnerIncarnation) string { return value.CgroupIdentityDigest },
			set: func(value *workflowrecovery.OwnerIncarnation, digest string) { value.CgroupIdentityDigest = digest }},
	} {
		original := digestField.get(incarnation)
		for label, invalid := range map[string]string{
			"prefixed":     "sha256:" + original,
			"uppercase":    strings.ToUpper(original),
			"wrong-length": original[:63],
			"nonhex":       strings.Repeat("z", 64),
		} {
			invalidIncarnation := incarnation
			digestField.set(&invalidIncarnation, invalid)
			if validWorkflowOwnerIncarnation(invalidIncarnation) {
				t.Errorf("production workflow catalog accepted %s %s digest %q", label, digestField.name, invalid)
			}
		}
	}
	t.Logf("PRODUCTION_PIN_ROUNDTRIP=checkpoint-runtime-and-private-git-bound runtime=%s repository=%s provider_exchange_records=%d",
		checkpoint.Pins.RuntimeDigest, repositoryName, len(providerTraceAfterInit.Records))
}

func openProductionPinFixtureJournal(t *testing.T, path string) *audit.Journal {
	t.Helper()
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	return journal
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

type productionPinClockUncertainPolicy struct {
	called bool
}

func (p *productionPinClockUncertainPolicy) AuthorizeFreshWorkflow(context.Context, string, uint64, string,
	workflowrecovery.OwnerIncarnation, time.Time, string, workflowrecovery.OwnerStoreIdentity) error {
	p.called = true
	return workflowrecovery.ErrClockUncertain
}
