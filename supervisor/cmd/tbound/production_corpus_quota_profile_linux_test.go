//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
)

// installCorpusManifestWithRealQuotas serves manifest through the read seam only. The
// quota seam stays the real corpusadmit.QuotasFor, so these tests exercise the profile
// selection itself.
func installCorpusManifestWithRealQuotas(t *testing.T, manifest evalcorpus.Manifest, profile string) piServiceConfig {
	t.Helper()
	canonical, err := evalcorpus.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := evalcorpus.Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	oldRead := readCorpusManifest
	readCorpusManifest = func() ([]byte, error) { return canonical, nil }
	t.Cleanup(func() { readCorpusManifest = oldRead })
	config := corpusTestServiceConfig(digest)
	config.CorpusQuotaProfile = profile
	return config
}

func TestCorpusPilotManifestLoadsOnlyUnderThePilotProfile(t *testing.T) {
	manifest := corpusTestManifest(t) // pilot-sized
	for _, profile := range []string{"", "thesis"} {
		config := installCorpusManifestWithRealQuotas(t, manifest, profile)
		if _, err := loadProductionCorpus(config); !errors.Is(err, ErrPiServiceCorpus) || !strings.Contains(err.Error(), "D48 main draw") {
			t.Fatalf("profile %q: a pilot manifest was loaded under the thesis quotas: %v", profile, err)
		}
	}
	config := installCorpusManifestWithRealQuotas(t, manifest, "pilot")
	if err := validateProductionPiServiceConfig(config); err != nil {
		t.Fatalf("pilot corpus config refused: %v", err)
	}
	corpus, err := loadProductionCorpus(config)
	if err != nil || corpus.quotaProfile != "pilot" || len(corpus.trials) != len(manifest.Cases) {
		t.Fatalf("pilot manifest under the pilot profile: %+v %v", corpus, err)
	}

	// The pilot profile relaxes the counts only. Any other violation, and a manifest with
	// the wrong pilot counts, is still refused.
	short := corpusTestManifest(t)
	short.Cases = short.Cases[:len(short.Cases)-1]
	if _, err := loadProductionCorpus(installCorpusManifestWithRealQuotas(t, short, "pilot")); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("a manifest with the wrong pilot counts was loaded: %v", err)
	}
	duplicate := corpusTestManifest(t)
	duplicate.Cases[1].CaseID = duplicate.Cases[0].CaseID
	if _, err := loadProductionCorpus(installCorpusManifestWithRealQuotas(t, duplicate, "pilot")); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("a structurally invalid pilot manifest was loaded: %v", err)
	}
}

func TestCorpusQuotaProfileConfigRules(t *testing.T) {
	base := func() piServiceConfig { return corpusTestServiceConfig(corpusTestDigest("m")) }
	research := piruntime.D09ManagedPiProviderRouteResearch
	for name, change := range map[string]func(*piServiceConfig){
		"default":              func(c *piServiceConfig) {},
		"thesis":               func(c *piServiceConfig) { c.CorpusQuotaProfile = "thesis" },
		"thesis on research":   func(c *piServiceConfig) { c.CorpusQuotaProfile, c.ProviderRoute = "thesis", research },
		"pilot on engineering": func(c *piServiceConfig) { c.CorpusQuotaProfile = "pilot" },
		"pilot on research with": func(c *piServiceConfig) {
			c.CorpusQuotaProfile, c.ProviderRoute, c.AllowPilotOnResearch = "pilot", research, true
		},
	} {
		config := base()
		change(&config)
		if err := validateProductionPiServiceConfig(config); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	for name, change := range map[string]func(*piServiceConfig){
		"pilot on research without the allowance": func(c *piServiceConfig) { c.CorpusQuotaProfile, c.ProviderRoute = "pilot", research },
		"allowance on research without pilot":     func(c *piServiceConfig) { c.ProviderRoute, c.AllowPilotOnResearch = research, true },
		"allowance on engineering":                func(c *piServiceConfig) { c.CorpusQuotaProfile, c.AllowPilotOnResearch = "pilot", true },
		"unknown profile":                         func(c *piServiceConfig) { c.CorpusQuotaProfile = "full" },
		"single-task profile": func(c *piServiceConfig) {
			c.WorkflowAdmissionMode, c.CorpusManifestPath, c.CorpusManifestDigest = "fresh", "", ""
			c.WorkflowAdmissionID, c.WorkflowTaskID, c.WorkflowPrompt = "host-task", "task", "prompt"
			c.CorpusQuotaProfile = "thesis"
		},
	} {
		config := base()
		change(&config)
		if err := validateProductionPiServiceConfig(config); !errors.Is(err, ErrPiServiceUnavailable) {
			t.Errorf("%s: accepted: %v", name, err)
		}
	}
}

// The profile is read from a signed document and decoded strictly: a config file that
// names it round-trips, and the digest of the file covers it.
func TestCorpusQuotaProfileIsPartOfTheConfigDocument(t *testing.T) {
	config := corpusTestServiceConfig(corpusTestDigest("m"))
	thesisBytes, _ := json.Marshal(config)
	config.CorpusQuotaProfile = "pilot"
	pilotBytes, _ := json.Marshal(config)
	if digestBytes(thesisBytes) == digestBytes(pilotBytes) || strings.Contains(string(thesisBytes), "quota") {
		t.Fatalf("profile is not digest-bound or changed the default encoding:\n%s\n%s", thesisBytes, pilotBytes)
	}
	var decoded piServiceConfig
	if err := unmarshalGovernedJSON(pilotBytes, &decoded); err != nil || decoded.CorpusQuotaProfile != "pilot" {
		t.Fatalf("profile did not survive the strict decoder: %+v %v", decoded, err)
	}
}

// The envelope, the reserved catalog entry, the recovery request and the runtime digest
// all carry the profile, so a pilot workflow can never be mistaken for a thesis one.
func TestCorpusQuotaProfileIsPinnedInEntryEnvelopeAndRuntimeDigest(t *testing.T) {
	manifest := corpusTestManifest(t)
	for _, profile := range []string{"thesis", "pilot"} {
		config := installCorpusManifestWithRealQuotas(t, manifest, profile)
		if profile == "thesis" {
			// the registered quotas are the 299-case ones; stand in the small ones for this case only
			oldQuotas := corpusQuotasFor
			corpusQuotasFor = func(string, evalcorpus.Manifest) (evalcorpus.Quotas, error) { return corpusTestQuotas(), nil }
			t.Cleanup(func() { corpusQuotasFor = oldQuotas })
		}
		corpus, err := loadProductionCorpus(config)
		if err != nil {
			t.Fatalf("%s: %v", profile, err)
		}
		root := privateGovernedTestRoot(t)
		journal, err := audit.Open(filepath.Join(root, "workflow-catalog.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		catalog := &piServiceWorkflowCatalog{journal: journal, entries: make(map[string]piServiceWorkflowEntry),
			byHostAdmissionID: make(map[string]string), ownerScopes: make(map[string]piServiceWorkflowOwnerScope), corpus: corpus}
		t.Cleanup(func() { _ = catalog.Close() })
		config.WorkflowOwnerStoreRoot = filepath.Join(root, "owners")

		request := corpusRequest("tbr-profile-"+profile, corpusTestCase, "compliant", 1)
		envelope, ok := catalog.envelopeFor(config, request)
		if !ok || envelope.CorpusQuotaProfile != profile {
			t.Fatalf("%s: envelope %+v ok=%v", profile, envelope, ok)
		}
		peer := piServicePeerCredentials{PID: 4242, UID: os.Geteuid(), GID: os.Getegid()}
		digest := mustPiServicePayloadDigest(t, request)
		requests := corpusTestRequestJournal(t, root)
		record, err := requests.begin(context.Background(), peer, request, digest)
		if err != nil {
			t.Fatal(err)
		}
		state, _, _ := requests.lookup(peer, request, digest)
		admission, err := catalog.ResolveAdmission(piServiceAdmissionContext{Peer: peer, CurrentPeer: peer, Request: request,
			PayloadDigest: digest, Accepted: state.accepted, AcceptedRecord: record},
			config, corpusTestDigest("cfg"), corpusTestDigest("reg"))
		if err != nil || admission.Entry.CorpusQuotaProfile != profile {
			t.Fatalf("%s: reserved entry %+v err=%v", profile, admission.Entry.CorpusQuotaProfile, err)
		}
		// A service whose config names the other profile does not recognise the entry.
		other := config
		other.CorpusQuotaProfile = map[string]string{"thesis": "pilot", "pilot": "thesis"}[profile]
		if _, ok := catalog.envelopeFor(other, request); ok {
			t.Fatalf("%s: an envelope was selected under a config naming the other profile", profile)
		}
		if code := catalog.screenTerminalReplay(config, corpusTestDigest("cfg"), corpusTestDigest("reg"), request,
			piServiceRequestState{accepted: state.accepted, acceptedRecord: record,
				terminal: &piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID, Status: "completed", Receipt: anchorTestReceiptJSON(t)}}); code != "" {
			t.Fatalf("%s: matching binding refused: %s", profile, code)
		}
		tampered := catalog.entries[record.Hash]
		tampered.CorpusQuotaProfile = other.CorpusQuotaProfile
		catalog.entries[record.Hash] = tampered
		if code := catalog.screenTerminalReplay(config, corpusTestDigest("cfg"), corpusTestDigest("reg"), request,
			piServiceRequestState{accepted: state.accepted, acceptedRecord: record,
				terminal: &piServiceResponse{SchemaVersion: piServiceResponseSchema, RequestID: request.RequestID, Status: "completed", Receipt: anchorTestReceiptJSON(t)}}); code != replayBindingMismatch {
			t.Fatalf("%s: an entry reserved under the other profile replayed: %q", profile, code)
		}
	}
}

func TestRuntimeBindingCoversTheCorpusQuotaProfile(t *testing.T) {
	binding := governedRecoveryRuntimeBinding{SchemaVersion: governedRecoveryRuntimeSchema, StoreRootIdentity: "store",
		CommandJournalIdentities: []string{}, BashOutputPaths: []string{}, CorpusManifestDigest: corpusTestDigest("corpus")}
	thesis, pilot := binding, binding
	thesis.CorpusQuotaProfile, pilot.CorpusQuotaProfile = "thesis", "pilot"
	dThesis, err1 := thesis.digest()
	dPilot, err2 := pilot.digest()
	dNone, err3 := binding.digest()
	if err1 != nil || err2 != nil || err3 != nil || dThesis == dPilot || dThesis == dNone || dPilot == dNone {
		t.Fatalf("runtime digest does not cover the quota profile: %q %q %q %v %v %v", dNone, dThesis, dPilot, err1, err2, err3)
	}
	encoded, _ := json.Marshal(binding)
	if strings.Contains(string(encoded), "quota") {
		t.Fatalf("a single-task runtime binding changed its encoding: %s", encoded)
	}
}

// researchPilotManifest turns the rehearsal-sized test manifest (TM-A 7, TM-B 4) into the research pilot of the
// pilot-draw test vector: TM-A 9, TM-B 2, its roots the drawn ledger cases, the draw pinned and the draw file
// behind the artifact seam.
func researchPilotManifest(t *testing.T) (evalcorpus.Manifest, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "evalcorpus", "testdata", "d48", "pilot-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := evalcorpus.ParseDraw(raw)
	if err != nil {
		t.Fatal(err)
	}
	q, err := evalcorpus.PilotQuotasFromDraw(d)
	if err != nil {
		t.Fatal(err)
	}
	m := corpusTestManifest(t)
	m.Pins.DrawDigest = d.Digest
	next := map[evalcorpus.ThreatClass]int{}
	for i := range m.Cases {
		a := m.Cases[i].Adversarial
		if a == nil {
			continue
		}
		if a.ThreatClass == evalcorpus.ClassTMB && next[evalcorpus.ClassTMB] == len(d.Members[evalcorpus.ClassTMB]) {
			a.ThreatClass = evalcorpus.ClassTMA
		}
		a.RootScenarioID = d.Members[a.ThreatClass][next[a.ThreatClass]]
		next[a.ThreatClass]++
	}
	m, err = evalcorpus.Allocate(m, q)
	if err != nil {
		t.Fatal(err)
	}
	m = sealConstructReview(t, m)
	if v := evalcorpus.Validate(m, q); len(v) != 0 {
		t.Fatalf("research pilot test manifest is invalid: %v", v)
	}
	return m, raw
}

func serveDraw(t *testing.T, raw []byte) {
	t.Helper()
	name, _ := corpusadmit.DrawArtifactName(evalcorpus.DrawDigest(raw))
	old := attackerArtifacts
	attackerArtifacts = func() fs.FS { return fstest.MapFS{name: &fstest.MapFile{Data: raw}} }
	t.Cleanup(func() { attackerArtifacts = old })
}

// The ledger-v3 pilot split (TM-A 9, TM-B 2) was refused at admission while the pilot quotas were the rehearsal's
// 7 and 4. It is admitted from the pinned draw; a tampered or missing draw file refuses the service.
func TestCorpusResearchPilotIsAdmittedFromThePinnedDraw(t *testing.T) {
	m, raw := researchPilotManifest(t)
	serveDraw(t, raw)
	corpus, err := loadProductionCorpus(installCorpusManifestWithRealQuotas(t, m, "pilot"))
	if err != nil || len(corpus.trials) != len(m.Cases) {
		t.Fatalf("research pilot refused: %v", err)
	}
	if _, err := loadProductionCorpus(installCorpusManifestWithRealQuotas(t, m, "thesis")); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("a pilot draw stood in for the main draw: %v", err)
	}

	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-2] = ' ' // same name, other bytes
	name, _ := corpusadmit.DrawArtifactName(m.Pins.DrawDigest)
	attackerArtifacts = func() fs.FS { return fstest.MapFS{name: &fstest.MapFile{Data: tampered}} }
	if _, err := loadProductionCorpus(installCorpusManifestWithRealQuotas(t, m, "pilot")); !errors.Is(err, ErrPiServiceCorpus) || !strings.Contains(err.Error(), "digests to") {
		t.Fatalf("a tampered draw file was used: %v", err)
	}
	attackerArtifacts = func() fs.FS { return fstest.MapFS{} }
	if _, err := loadProductionCorpus(installCorpusManifestWithRealQuotas(t, m, "pilot")); !errors.Is(err, ErrPiServiceCorpus) {
		t.Fatalf("a missing draw file was accepted: %v", err)
	}
}

// On the research route a pilot corpus is the research pilot: without a pinned pilot draw it is refused, and with one
// it is admitted.
func TestCorpusResearchRoutePilotNeedsThePilotDraw(t *testing.T) {
	research := func(config piServiceConfig) piServiceConfig {
		config.ProviderRoute, config.AllowPilotOnResearch = piruntime.D09ManagedPiProviderRouteResearch, true
		return config
	}
	rehearsal := research(installCorpusManifestWithRealQuotas(t, corpusTestManifest(t), "pilot"))
	if _, err := loadProductionCorpus(rehearsal); !errors.Is(err, ErrPiServiceCorpus) || !strings.Contains(err.Error(), "research pilot") {
		t.Fatalf("a rehearsal-sized pilot without a draw was loaded on the research route: %v", err)
	}
	m, raw := researchPilotManifest(t)
	serveDraw(t, raw)
	if _, err := loadProductionCorpus(research(installCorpusManifestWithRealQuotas(t, m, "pilot"))); err != nil {
		t.Fatalf("the research pilot was refused on the research route: %v", err)
	}
}
