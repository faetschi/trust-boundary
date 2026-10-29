package corpusadmit

import (
	"encoding/json"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
)

func pilotSet(t *testing.T) fstest.MapFS {
	t.Helper()
	result := fstest.MapFS{}
	root := os.DirFS("../../../corpus/pilot")
	if err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(path, "artifacts/") || strings.HasPrefix(path, "licenses/") || path == "sources.json" || path == "THIRD_PARTY_NOTICES.md" {
			b, err := fs.ReadFile(root, path)
			if err != nil {
				return err
			}
			result[path] = &fstest.MapFile{Data: b}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestConstructSourcesAndOverlap(t *testing.T) {
	m := loadPilotManifest(t)
	set := pilotSet(t)
	c := m.Cases[0]
	if r := checkSource(c, set); r.Status != "pass" {
		t.Fatalf("source: %+v", r)
	}
	delete(set, "licenses/AgentDojo-LICENSE")
	if r := checkSource(c, set); r.Status != "fail" {
		t.Fatalf("missing license accepted: %+v", r)
	}
	if r := checkOverlap(c, nil); r.Status != "not-checked" {
		t.Fatalf("absent second corpus: %+v", r)
	}
	if r := checkOverlap(c, &m); r.Status != "fail" {
		t.Fatalf("overlapping corpus accepted: %+v", r)
	}
	duplicate := append(append([]evalcorpus.Case{}, m.Cases...), c)
	if r := checkRoots(c, duplicate); r.Status != "fail" {
		t.Fatalf("duplicate root accepted: %+v", r)
	}
}

func TestConstructSourcePinCannotBorrowAnotherSourcesDigest(t *testing.T) {
	set := pilotSet(t)
	m := loadPilotManifest(t)
	c := m.Cases[0]
	c.MeaningRecord.BenchmarkDigest = m.Cases[len(m.Cases)-1].MeaningRecord.BenchmarkDigest
	if r := checkSource(c, set); r.Status != "fail" {
		t.Fatalf("unrelated pinned digest accepted: %+v", r)
	}
}

func TestConstructReportsEveryCaseAndPreservesGuestGates(t *testing.T) {
	m := loadPilotManifest(t)
	r := CheckConstructs(m, pilotSet(t), evalcorpus.PilotQuotas(), nil, pilotNode(t))
	if len(r.DeferredReview) != 0 {
		t.Fatalf("obsolete review deferrals: %v", r.DeferredReview)
	}
	if len(r.Cases) != 33 {
		t.Fatalf("got %d case results", len(r.Cases))
	}
	for _, c := range r.Cases {
		if len(c.Checks) == 0 {
			t.Errorf("no checks for %s", c.CaseID)
		}
		for _, check := range c.Checks {
			if check.Reason == "" {
				t.Errorf("no reason: %+v", check)
			}
		}
	}
	if r.Summary["calibration-p"].GuestOnly != 11 {
		t.Fatalf("calibration summary: %+v", r.Summary)
	}
	if r.Summary["mutation"].Pass != 20 {
		t.Fatalf("mutation summary: %+v", r.Summary["mutation"])
	}
	if r.Summary["effect-satisfiable"].Pass != 4 || r.Summary["effect-satisfiable"].GuestOnly != 7 {
		t.Fatalf("effect summary: %+v", r.Summary["effect-satisfiable"])
	}
	// RepoQA retrievals read their answer from the fixture: reported as not-checked, never as pass.
	reads := 0
	for _, c := range r.Cases {
		for _, check := range c.Checks {
			if check.Name == "leak-fixture" && strings.HasPrefix(check.Reason, "read retrieval") {
				reads++
				if check.Status != "not-checked" {
					t.Errorf("%s: %+v", c.CaseID, check)
				}
			}
		}
	}
	if reads != 5 || r.Summary["leak-fixture"].Fail != 0 {
		t.Fatalf("read retrievals %d, leak-fixture %+v", reads, r.Summary["leak-fixture"])
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
}

func TestExactLeaks(t *testing.T) {
	if got := ExactLeaks("answer-value", []string{" value \n", "", "answer"}); len(got) != 2 {
		t.Fatalf("leaks: %v", got)
	}
}

func TestConstructEmptyCorpusCannotPass(t *testing.T) {
	r := CheckConstructs(evalcorpus.Manifest{}, fstest.MapFS{}, evalcorpus.PilotQuotas(), nil, nil)
	if r.Passed {
		t.Fatal("empty corpus passed validation")
	}
}

func TestConstructSourceNoticesAndRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		edit      func(fstest.MapFS)
		status    string
		condition string
	}{
		{"unlisted license", func(set fstest.MapFS) {
			set["THIRD_PARTY_NOTICES.md"].Data = []byte(loadPilotManifest(t).Cases[0].MeaningRecord.BenchmarkRevision)
		}, "fail", ""},
		{"noncommercial", func(set fstest.MapFS) {
			set["licenses/AgentDojo-LICENSE"].Data = []byte("Non-commercial use only.\nNo deployment for profit.\n")
		}, "pass", "No deployment for profit."},
		{"safety condition", func(set fstest.MapFS) {
			set["licenses/AgentDojo-LICENSE"].Data = []byte("Safety restrictions: do not use to cause harm.\n")
		}, "pass", "do not use to cause harm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := pilotSet(t)
			tc.edit(set)
			r := checkSource(loadPilotManifest(t).Cases[0], set)
			if r.Status != tc.status || (tc.condition != "" && !strings.Contains(strings.Join(r.Conditions, " "), tc.condition)) {
				t.Fatalf("source check: %+v", r)
			}
		})
	}
}

func TestConstructLeaksDetectPromptOracleAndCanary(t *testing.T) {
	set := pilotSet(t)
	c := loadPilotManifest(t).Cases[0]
	digest, _ := FixtureDigest(c)
	fixture, err := LoadFixture(mustArtifacts(t, set), digest)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"netoracle", "EFF-NET-01", "tb-a01-msg"} {
		c.Trial.Prompt = "The hidden value is " + value
		checks := constructLeaks(c, fixture, taskaccept.AcceptSpec{}, taskaccept.SolutionSpec{})
		if checks[0].Status != "fail" {
			t.Errorf("disclosure %q passed: %+v", value, checks)
		}
	}
}

func mustArtifacts(t *testing.T, set fs.FS) fs.FS {
	t.Helper()
	arts, err := fs.Sub(set, "artifacts")
	if err != nil {
		t.Fatal(err)
	}
	return arts
}

func TestConstructOverlapAndMissingMeaningRecords(t *testing.T) {
	m := loadPilotManifest(t)
	c := m.Cases[0]
	peer := evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, Cases: []evalcorpus.Case{m.Cases[1]}}
	if r := checkOverlap(c, &peer); r.Status != "pass" {
		t.Fatalf("disjoint roots: %+v", r)
	}
	peer.Cases[0].MeaningRecord = nil
	if r := checkOverlap(c, &peer); r.Status != "fail" {
		t.Fatalf("missing peer root: %+v", r)
	}
	c.MeaningRecord = nil
	if r := checkSource(c, pilotSet(t)); r.Status != "fail" {
		t.Fatalf("missing meaning record: %+v", r)
	}
}
