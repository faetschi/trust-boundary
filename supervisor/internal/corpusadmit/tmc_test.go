package corpusadmit

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/evalcorpus"
)

// The derived TM-C cases of lane C, copied unchanged: the two development fixtures of the B6 qualification carrier
// proc-qual-dev-01 (PR #213 at e12f362f) and the derived TM-C manifest (PR #155 at 30ddbe73, corpus/third/tmc).
const (
	qualCasesPath = "testdata/tmc/proc-qual-dev-01-tmc-cases.json"
	c7Path        = "testdata/tmc/c7-manifest.draft.json"
	// qualCarrierDigest is the Go digest PR #213 states for its manifest.
	qualCarrierDigest = "sha256:f54057418913e2f181123dd22af649ae2daf582c4caa409dda085b41d220af46"
)

// qualificationCarrier rebuilds the proc-qual-dev-01 manifest: the T5 draft with the two fixtures after its eleven
// adversarial cases, as corpus/sampling/carrier.py appends them.
func qualificationCarrier(t *testing.T) evalcorpus.Manifest {
	t.Helper()
	var doc map[string]json.RawMessage
	var cases, fixtures []json.RawMessage
	for path, v := range map[string]any{t5DraftDir + "/manifest.draft.json": &doc, qualCasesPath: &fixtures} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := json.Unmarshal(doc["cases"], &cases); err != nil {
		t.Fatal(err)
	}
	doc["cases"], _ = json.Marshal(slices.Insert(cases, 11, fixtures...))
	raw, _ := json.Marshal(doc)
	m, err := evalcorpus.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if digest, _ := evalcorpus.Digest(m); digest != qualCarrierDigest {
		t.Fatalf("rebuilt carrier digest %s, want %s", digest, qualCarrierDigest)
	}
	return m
}

func loadC7Manifest(t *testing.T) evalcorpus.Manifest {
	t.Helper()
	f, err := os.Open(c7Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := evalcorpus.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The pilot profile admits a carrier's derived TM-C development fixtures (D48: TM-C has no fixed size) and changes
// no other count; a carrier without them keeps the rehearsal quotas exactly.
func TestPilotProfileAdmitsTheQualificationCarrier(t *testing.T) {
	m := qualificationCarrier(t)
	q, err := QuotasFor(ProfilePilot, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := evalcorpus.PilotQuotas()
	want.Adversarial[evalcorpus.ClassTMC] = 2
	if !equalQuotas(q, want) {
		t.Fatalf("quotas %+v, want %+v", q, want)
	}
	if v := evalcorpus.Validate(m, q); len(v) != 0 {
		t.Fatalf("Validate: %v", v)
	}
	allocated, err := evalcorpus.Allocate(m, q)
	if err != nil {
		t.Fatal(err)
	}
	if digest, _ := evalcorpus.Digest(allocated); digest != qualCarrierDigest {
		t.Fatalf("the carrier is not its own allocation: %s", digest)
	}
	report := CheckConstructs(m, t5Set(t), q, nil, pilotNode(t))
	for _, check := range report.Checks {
		if check.Name == "manifest-validation" && check.Status != "pass" {
			t.Errorf("%+v", check)
		}
	}
	for _, c := range report.Cases {
		for _, check := range c.Checks {
			if check.Name == "source-license" && check.Status != "pass" {
				t.Errorf("%s: %+v", c.CaseID, check)
			}
		}
	}

	t5 := slices.DeleteFunc(slices.Clone(m.Cases), func(c evalcorpus.Case) bool {
		return c.Adversarial != nil && c.Adversarial.ThreatClass == evalcorpus.ClassTMC
	})
	if q, err := QuotasFor(ProfilePilot, evalcorpus.Manifest{Cases: t5}, nil); err != nil || !equalQuotas(q, evalcorpus.PilotQuotas()) {
		t.Fatalf("a carrier without TM-C fixtures: quotas %+v, %v", q, err)
	}
	// A TM-C case without the derived sample part is not a development fixture: the count refuses it.
	m.Cases[11].Adversarial.SamplePart = ""
	m.Cases[11].MeaningRecord.Derived = nil
	if q, err = QuotasFor(ProfilePilot, m, nil); err != nil || q.Adversarial[evalcorpus.ClassTMC] != 1 {
		t.Fatalf("quotas %+v, %v", q, err)
	}
}

func equalQuotas(a, b evalcorpus.Quotas) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// Every derived TM-C case of lane C passes the source check without any benchmark pin or licence file, and a derived
// record that is not exactly that is refused.
func TestConstructSourceDerivedTMCCases(t *testing.T) {
	derived := append(slices.Clone(loadC7Manifest(t).Cases), qualificationCarrier(t).Cases[11:13]...)
	if len(derived) != 7 {
		t.Fatalf("%d derived cases", len(derived))
	}
	for _, c := range derived {
		if r := checkSource(c, fstest.MapFS{}); r.Status != "pass" || len(r.Conditions) != 0 {
			t.Errorf("%s: %+v", c.CaseID, r)
		}
	}
	for name, mutate := range map[string]func(c *evalcorpus.Case){
		"source key not derived":           func(c *evalcorpus.Case) { c.Adversarial.SourceKey = "AgentDojo" },
		"source item is not the mechanism": func(c *evalcorpus.Case) { c.Adversarial.SourceItemID = "other" },
		"no mechanism":                     func(c *evalcorpus.Case) { c.MeaningRecord.MechanismID, c.Adversarial.SourceItemID = "", "" },
		"not TM-C":                         func(c *evalcorpus.Case) { c.Adversarial.ThreatClass = evalcorpus.ClassTMA },
		"not sample part tmc":              func(c *evalcorpus.Case) { c.Adversarial.SamplePart = "" },
		"origin established":               func(c *evalcorpus.Case) { c.MeaningRecord.CaseOrigin = evalcorpus.OriginEstablished },
		"no derived record":                func(c *evalcorpus.Case) { c.MeaningRecord.Derived = nil },
		"names a benchmark":                func(c *evalcorpus.Case) { c.MeaningRecord.BenchmarkKey = "AgentDojo" },
		"names a benchmark digest": func(c *evalcorpus.Case) {
			c.MeaningRecord.BenchmarkDigest = derived[0].Adversarial.WorkspaceFixtureDigest
		},
		"names a licence":        func(c *evalcorpus.Case) { c.MeaningRecord.License = "MIT" },
		"no adversarial payload": func(c *evalcorpus.Case) { c.Adversarial = nil },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(derived[0])
			var c evalcorpus.Case
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			mutate(&c)
			if r := checkSource(c, fstest.MapFS{}); r.Status != "fail" {
				t.Fatalf("accepted: %+v", r)
			}
		})
	}
}
