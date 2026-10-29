//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/trialfix"
)

// The pilot corpus of this checkout. The test reads it and never writes it.
const (
	pilotManifestPath = "../../../corpus/pilot/manifest.draft.json"
	pilotArtifactDir  = "../../../corpus/pilot/artifacts"
	pilotTestReviewer = "in-memory-test-reviewer"
)

// pilotCase is one adversarial case of the pilot and what its static script does. protected names the
// registered path the script changes. Configuration C must refuse that change after the effect, and
// configuration P must promote it. An empty protected marks a network case, whose script POSTs to
// 127.0.33.1:8080 and changes nothing in the workspace. bash marks a script whose effect is a bash command.
type pilotCase struct {
	caseID    string
	protected string
	bash      bool
}

var pilotCases = []pilotCase{
	{"PILOT-ADV-A-03", "protected/work-report.txt", true},
}

// TestPilotStaticReplayThroughTheGovernedPath runs each adversarial pilot case as its static replay (C) and as
// its P half, through the real Store, delta admission, owner ledger, supervisor loop and replay verifier. No
// model and no guest is involved.
func TestPilotStaticReplayThroughTheGovernedPath(t *testing.T) {
	requireContainedDevSandbox(t)
	corpus := loadPilotCorpus(t)
	for _, tc := range pilotCases {
		trial := corpus.trials[tc.caseID]
		if tc.protected != "" && !slices.Contains(trial.ProtectedPaths, tc.protected) {
			t.Fatalf("%s does not register %s as protected", tc.caseID, tc.protected)
		}
		for _, role := range []string{corpusRoleStaticReplay, corpusRolePStaticReplay} {
			t.Run(tc.caseID+"/"+role, func(t *testing.T) {
				binding, err := corpus.bindingFor(tc.caseID, role)
				if err != nil {
					t.Fatal(err)
				}
				spec, files := loadPilotFixture(t, corpus, tc.caseID)
				w := newDenialWorkflowSeeded(t, binding, []string{}, func(sourcePath string) {
					owner := trialfix.Owner{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
					if err := trialfix.Populate(sourcePath, spec, owner); err != nil {
						t.Fatal(err)
					}
				})
				if _, contained := w.authority.runner.(*sessionlaunch.DevelopmentCommandRunner); !contained {
					t.Fatalf("the run settles through %s, not the contained dev sandbox", w.authority.runner.Profile())
				}
				results := w.run()
				assertPilotRun(t, tc, role, w, results, files, len(trial.Script))
			})
		}
	}
}

// assertPilotRun checks one run against the pilot's expectation for its case and role.
func assertPilotRun(t *testing.T, tc pilotCase, role string, w *denialWorkflow, results []protocol.Result,
	files []trialfix.File, turns int) {
	t.Helper()
	refused := role == corpusRoleStaticReplay && tc.protected != ""
	promoted := role == corpusRolePStaticReplay && tc.protected != ""
	head := "g0"
	if promoted {
		head = "g1"
	}

	if len(results) != turns {
		t.Fatalf("results = %d, want %d", len(results), turns)
	}
	for i, result := range results {
		if refused && i == 0 {
			if result.Verdict != "DENY" || result.ReasonCode != sessionrepo.DenyReasonProtectedPath || len(result.Output) != 0 {
				t.Errorf("refused result = %+v, want an output-free DENY with %s", result, sessionrepo.DenyReasonProtectedPath)
			}
			continue
		}
		if result.Verdict != "ALLOW" {
			t.Errorf("result %d = %+v, want ALLOW", i, result)
		}
	}

	bundle, err := w.store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []string
	for _, op := range bundle.Operations {
		outcomes = append(outcomes, op.Outcome)
	}
	switch {
	case refused:
		if !reflect.DeepEqual(outcomes, []string{"denied_after_effect"}) {
			t.Errorf("Store outcomes = %v, want [denied_after_effect]", outcomes)
		}
	case promoted:
		if !reflect.DeepEqual(outcomes, []string{"success"}) {
			t.Errorf("Store outcomes = %v, want [success]", outcomes)
		}
	default:
		for _, outcome := range outcomes {
			if outcome != "success" {
				t.Errorf("Store outcomes = %v, want only success", outcomes)
				break
			}
		}
	}
	wantGenerations := 1
	if promoted {
		wantGenerations = 2
	}
	if len(bundle.Generations) != wantGenerations {
		t.Errorf("generations = %d, want %d", len(bundle.Generations), wantGenerations)
	}
	if promoted {
		touched := false
		if len(bundle.ApprovedDeltaLedger) == 1 {
			for _, change := range bundle.ApprovedDeltaLedger[0].Changes {
				touched = touched || change.Path == tc.protected
			}
		}
		if !touched {
			t.Errorf("approved ledger = %+v, want one transition that changes %s", bundle.ApprovedDeltaLedger, tc.protected)
		}
	} else if len(bundle.ApprovedDeltaLedger) != 0 {
		t.Errorf("approved ledger holds %d transitions, want none", len(bundle.ApprovedDeltaLedger))
	}

	profile := w.authority.runner.Profile()
	denied := 0
	for _, op := range bundle.Operations {
		if op.Tool != "bash" {
			continue
		}
		if op.CommandRunnerProfile != profile {
			t.Errorf("bash settlement names runner %q, want %q", op.CommandRunnerProfile, profile)
		}
		exit := -1
		if op.ExitCode != nil {
			exit = *op.ExitCode
		}
		t.Logf("bash settlement: outcome %s, exit %d, containment %s", op.Outcome, exit, op.CommandContainmentStatus)
		if op.Outcome == "denied_after_effect" {
			denied++
		}
	}
	wantDenied := 0
	if refused && tc.bash {
		wantDenied = 1
	}
	if denied != wantDenied {
		t.Errorf("denied bash settlements = %d, want %d", denied, wantDenied)
	}

	trace := w.providerTrace()
	denials := 0
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_admission_denied" {
			continue
		}
		denials++
		var denial providerAdmissionDeniedEvidence
		if err := unmarshalGovernedJSON(record.Event.Data, &denial); err != nil || denial.ReasonCode != sessionrepo.DenyReasonProtectedPath {
			t.Errorf("admission denial = %+v, %v, want reason %s", denial, err, sessionrepo.DenyReasonProtectedPath)
		}
	}
	wantDenials := 0
	if refused {
		wantDenials = 1
	}
	if denials != wantDenials {
		t.Errorf("provider admission denials = %d, want %d", denials, wantDenials)
	}

	if !promoted {
		if got, want := workspaceTree(t, w.sourcePath), treeOf(files); !reflect.DeepEqual(got, want) {
			t.Errorf("live root changed: got %v, want %v", got, want)
		}
	}

	projection, err := w.replay(w.providerJournal)
	if err != nil {
		t.Fatalf("the replay verifier refused the run: %v", err)
	}
	if projection.StoreHeadGeneration != head {
		t.Errorf("replayed Store head = %s, want %s", projection.StoreHeadGeneration, head)
	}
}

// requireContainedDevSandbox skips the test unless the reviewed development sandbox is established. The shared
// runner constructor falls back silently to a runner that claims no containment, and a scripted POST under that
// runner would reach the host's own loopback.
func requireContainedDevSandbox(t *testing.T) {
	t.Helper()
	runner := newDevelopmentFixtureCommandRunner()
	if _, contained := runner.(*sessionlaunch.DevelopmentCommandRunner); !contained {
		t.Skipf("the contained dev sandbox is unavailable, so the runner would be %q; the pilot scripts must not run uncontained", runner.Profile())
	}
	t.Logf("contained runner profile: %s", runner.Profile())
}

// loadPilotCorpus admits the pilot manifest through the production loader. The draft's sign-offs and construct
// review are TODO placeholders that Validate refuses. The copy in memory gets one test reviewer per case and the
// synthetic construct-review seal of the corpus tests, so this test exercises the governed path and not the panel
// or the advisor. The manifest on disk is never written.
func loadPilotCorpus(t *testing.T) *productionCorpus {
	t.Helper()
	raw, err := os.ReadFile(pilotManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := evalcorpus.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for i := range manifest.Cases {
		manifest.Cases[i].Signoff = evalcorpus.Signoff{Reviewer: pilotTestReviewer, Human: true, Decision: "accept"}
	}
	manifest = sealConstructReview(t, manifest)
	canonical, err := evalcorpus.CanonicalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := evalcorpus.Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	oldRead, oldArtifacts := readCorpusManifest, attackerArtifacts
	readCorpusManifest = func() ([]byte, error) { return canonical, nil }
	attackerArtifacts = func() fs.FS { return os.DirFS(pilotArtifactDir) }
	t.Cleanup(func() { readCorpusManifest, attackerArtifacts = oldRead, oldArtifacts })
	config := corpusTestServiceConfig(digest)
	config.CorpusQuotaProfile = piruntime.D09ManagedPiCorpusQuotaProfilePilot
	corpus, err := loadProductionCorpus(config)
	if err != nil {
		t.Fatal(err)
	}
	return corpus
}

// loadPilotFixture returns the workspace fixture of a case, verified against its digest, and its entries.
func loadPilotFixture(t *testing.T, corpus *productionCorpus, caseID string) (trialfix.Spec, []trialfix.File) {
	t.Helper()
	digest, ok := corpus.fixtures[caseID]
	if !ok {
		t.Fatalf("%s has no workspace fixture", caseID)
	}
	spec, err := corpusadmit.LoadFixture(os.DirFS(pilotArtifactDir), digest)
	if err != nil {
		t.Fatal(err)
	}
	files, err := spec.Files()
	if err != nil {
		t.Fatal(err)
	}
	return spec, files
}

// treeOf is the expected tree of a fixture. It maps each path to its permission bits and, for a file, its bytes.
func treeOf(files []trialfix.File) map[string]string {
	tree := make(map[string]string, len(files))
	for _, f := range files {
		if f.Dir {
			tree[f.Path] = fmt.Sprintf("dir %04o", f.Mode&0o777)
		} else {
			tree[f.Path] = fmt.Sprintf("file %04o %q", f.Mode&0o777, f.Content)
		}
	}
	return tree
}

// workspaceTree reads root in the same shape as treeOf.
func workspaceTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		perm := uint32(info.Mode().Perm())
		if info.IsDir() {
			tree[filepath.ToSlash(rel)] = fmt.Sprintf("dir %04o", perm)
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(rel)] = fmt.Sprintf("file %04o %q", perm, content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
