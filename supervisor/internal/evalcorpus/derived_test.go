package evalcorpus

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// derivedCaseID is a synthetic TM-C case. derivedManifest gives it a complete derived record.
const derivedCaseID = "SYNTH-ADV-TM-C-001"

// goodRef is a well-formed taxonomy reference.
func goodRef(key, entry string) TaxonomyRef {
	return TaxonomyRef{Key: key, Revision: "2026-01", Digest: synthDigest("taxonomy/" + key), EntryID: entry}
}

// goodDerived returns a complete derived record. It carries no benchmark-source fields.
func goodDerived() *MeaningRecord {
	return &MeaningRecord{
		CaseOrigin:         OriginDerived,
		MechanismID:        "mech.tool-output-redirect",
		PreservedIntent:    "redirect a protected file through a tool output",
		TaskUnit:           "single injected instruction",
		AuthorizationClass: "injected-third-party-instruction",
		Transformations:    []string{"authored from the taxonomy entry"},
		LostDistinctions:   []string{},
		ToolMapping:        "Pi bash mv",
		OracleMapping:      "EFF-WS-02 removal",
		LabelOrigin:        LabelExternalOracle,
		Derived: &DerivedRecord{
			OWASP:      goodRef("OWASP-Agentic-Top-10", "ASI01"),
			MITRE:      goodRef("MITRE-ATTACK", "T1059"),
			Author:     "synthetic-author",
			AuthoredOn: "2026-11-02",
		},
	}
}

// goodEstablished returns a complete established record, the kind a main or top-up case carries.
func goodEstablished() *MeaningRecord {
	return &MeaningRecord{
		CaseOrigin: OriginEstablished, BenchmarkKey: "InjecAgent", BenchmarkRevision: "f19c9f2",
		BenchmarkDigest: synthDigest("item"), SourceItemID: "dh:line:10", RootID: "InjecAgent:dh:line:10",
		License: "MIT", Redistribution: "notice retained", PreservedIntent: "move a protected file away",
		TaskUnit: "single injected instruction", AuthorizationClass: "injected-third-party-instruction",
		Transformations: []string{"service tool mapped to bash mv"}, LostDistinctions: []string{},
		ToolMapping: "Pi bash mv", OracleMapping: "EFF-WS-02 removal", LabelOrigin: LabelExternalOracle,
	}
}

// derivedManifest returns the synthetic manifest with SYNTH-ADV-TM-C-001 as a derived tmc case.
func derivedManifest(t *testing.T) (Manifest, Quotas) {
	t.Helper()
	q := smallQuotas()
	m := allocated(t, synthManifest(q, synthSeed), q)
	c := findCase(t, &m, derivedCaseID)
	c.Adversarial.SamplePart = SampleTMC
	c.MeaningRecord = goodDerived()
	return m, q
}

// Each row mutates one case of the derived manifest and names the rule that must refuse it.
// An empty rule means the manifest is accepted with no violation.
func TestSamplePartAndDerivedRules(t *testing.T) {
	const tmA = "SYNTH-ADV-TM-A-001"
	const tmB = "SYNTH-ADV-TM-B-001"
	cases := []struct {
		name   string
		target string
		mutate func(c *Case)
		rule   string
	}{
		{"valid tmc with a derived record", derivedCaseID, func(c *Case) {}, ""},
		{"legacy case without a sample part", tmA, func(c *Case) {}, ""},
		{"unknown sample part", derivedCaseID, func(c *Case) { c.Adversarial.SamplePart = "warm" }, "field.invalid"},
		{"derived record on a main case", tmA, func(c *Case) {
			c.Adversarial.SamplePart = SampleMain
			c.MeaningRecord = goodDerived()
		}, "sample.derived"},
		{"derived record on a top-up case", tmB, func(c *Case) {
			c.Adversarial.SamplePart = SampleTopup
			c.MeaningRecord = goodDerived()
		}, "sample.derived"},
		{"derived record with no sample part", tmA, func(c *Case) {
			c.MeaningRecord = goodDerived()
		}, "sample.derived"},
		{"tmc without a meaning record", derivedCaseID, func(c *Case) {
			c.MeaningRecord = nil
		}, "sample.derived"},
		{"tmc with a derived origin but no derived record", derivedCaseID, func(c *Case) {
			c.MeaningRecord.Derived = nil
		}, "sample.derived"},
		{"tmc on a TM-A case", tmA, func(c *Case) {
			c.Adversarial.SamplePart = SampleTMC
			c.MeaningRecord = goodDerived()
		}, "sample.class"},
		{"main on a TM-C case", derivedCaseID, func(c *Case) {
			c.Adversarial.SamplePart = SampleMain
		}, "sample.class"},
		{"derived record on a compliant case", "SYNTH-CMP-read-01", func(c *Case) {
			c.MeaningRecord = goodDerived()
		}, "meaning.derived"},
		{"derived record on an ambiguous case", "SYNTH-AMB-01", func(c *Case) {
			c.MeaningRecord = goodDerived()
		}, "meaning.derived"},
		{"derived record with established origin", derivedCaseID, func(c *Case) {
			c.MeaningRecord.CaseOrigin = OriginEstablished
		}, "meaning.origin"},
		{"derived origin on a main case", tmA, func(c *Case) {
			c.Adversarial.SamplePart = SampleMain
			c.MeaningRecord = goodDerived()
			c.MeaningRecord.Derived = nil
		}, "meaning.origin"},
		{"main without a meaning record", tmA, func(c *Case) {
			c.Adversarial.SamplePart = SampleMain
			c.MeaningRecord = nil
		}, "sample.provenance"},
		{"topup without a meaning record", tmB, func(c *Case) {
			c.Adversarial.SamplePart = SampleTopup
			c.MeaningRecord = nil
		}, "sample.provenance"},
		{"main with an established record", tmA, func(c *Case) {
			c.Adversarial.SamplePart = SampleMain
			c.MeaningRecord = goodEstablished()
		}, ""},
		{"topup with an established record", tmB, func(c *Case) {
			c.Adversarial.SamplePart = SampleTopup
			c.MeaningRecord = goodEstablished()
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, q := derivedManifest(t)
			tc.mutate(findCase(t, &m, tc.target))
			vs := validate(m, q)
			if tc.rule == "" {
				wantClean(t, vs)
			} else {
				wantRule(t, vs, tc.rule)
			}
		})
	}
}

// Every field of a present derived record is required, and the taxonomy digests and the date are well formed.
func TestDerivedRecordFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(mr *MeaningRecord)
		rule   string
	}{
		{"owasp key empty", func(mr *MeaningRecord) { mr.Derived.OWASP.Key = "" }, "field.empty"},
		{"owasp revision empty", func(mr *MeaningRecord) { mr.Derived.OWASP.Revision = " " }, "field.empty"},
		{"owasp digest empty", func(mr *MeaningRecord) { mr.Derived.OWASP.Digest = "" }, "field.empty"},
		{"owasp digest uppercase hex", func(mr *MeaningRecord) {
			mr.Derived.OWASP.Digest = "sha256:" + strings.Repeat("A", 64)
		}, "digest.format"},
		{"owasp digest short", func(mr *MeaningRecord) { mr.Derived.OWASP.Digest = "sha256:" + strings.Repeat("a", 63) }, "digest.format"},
		{"owasp entry id empty", func(mr *MeaningRecord) { mr.Derived.OWASP.EntryID = "" }, "field.empty"},
		{"mitre key empty", func(mr *MeaningRecord) { mr.Derived.MITRE.Key = "" }, "field.empty"},
		{"mitre revision empty", func(mr *MeaningRecord) { mr.Derived.MITRE.Revision = "" }, "field.empty"},
		{"mitre digest empty", func(mr *MeaningRecord) { mr.Derived.MITRE.Digest = "" }, "field.empty"},
		{"mitre digest wrong algorithm", func(mr *MeaningRecord) {
			mr.Derived.MITRE.Digest = "sha512:" + strings.Repeat("a", 64)
		}, "digest.format"},
		{"mitre entry id empty", func(mr *MeaningRecord) { mr.Derived.MITRE.EntryID = "" }, "field.empty"},
		{"author empty", func(mr *MeaningRecord) { mr.Derived.Author = "" }, "field.empty"},
		{"authored_on empty", func(mr *MeaningRecord) { mr.Derived.AuthoredOn = "" }, "field.empty"},
		{"authored_on with slashes", func(mr *MeaningRecord) { mr.Derived.AuthoredOn = "2026/11/02" }, "field.invalid"},
		{"authored_on without zero padding", func(mr *MeaningRecord) { mr.Derived.AuthoredOn = "2026-1-2" }, "field.invalid"},
		{"authored_on not a calendar day", func(mr *MeaningRecord) { mr.Derived.AuthoredOn = "2026-02-30" }, "field.invalid"},
		{"authored_on with a time", func(mr *MeaningRecord) { mr.Derived.AuthoredOn = "2026-11-02T00:00:00Z" }, "field.invalid"},
		{"mechanism_id empty", func(mr *MeaningRecord) { mr.MechanismID = "" }, "field.empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, q := derivedManifest(t)
			tc.mutate(findCase(t, &m, derivedCaseID).MeaningRecord)
			wantRule(t, validate(m, q), tc.rule)
		})
	}
}

// A derived record replaces the benchmark-source fields, so they may be absent. The other meaning
// fields stay required, and a benchmark digest that is present must still be well formed.
func TestDerivedRecordBenchmarkFieldsAreConditional(t *testing.T) {
	t.Run("accepted without benchmark-source fields", func(t *testing.T) {
		m, q := derivedManifest(t)
		mr := findCase(t, &m, derivedCaseID).MeaningRecord
		if mr.BenchmarkKey != "" || mr.BenchmarkRevision != "" || mr.BenchmarkDigest != "" ||
			mr.SourceItemID != "" || mr.RootID != "" || mr.License != "" || mr.Redistribution != "" {
			t.Fatal("fixture carries a benchmark-source field")
		}
		wantClean(t, validate(m, q))
	})
	t.Run("present benchmark digest is still checked", func(t *testing.T) {
		m, q := derivedManifest(t)
		findCase(t, &m, derivedCaseID).MeaningRecord.BenchmarkDigest = "sha256:zz"
		wantRule(t, validate(m, q), "digest.format")
	})
	stays := []struct {
		name   string
		mutate func(mr *MeaningRecord)
		rule   string
	}{
		{"preserved_intent", func(mr *MeaningRecord) { mr.PreservedIntent = "" }, "field.empty"},
		{"task_unit", func(mr *MeaningRecord) { mr.TaskUnit = "" }, "field.empty"},
		{"authorization_class", func(mr *MeaningRecord) { mr.AuthorizationClass = "" }, "field.empty"},
		{"tool_mapping", func(mr *MeaningRecord) { mr.ToolMapping = "" }, "field.empty"},
		{"oracle_mapping", func(mr *MeaningRecord) { mr.OracleMapping = "" }, "field.empty"},
		{"transformations", func(mr *MeaningRecord) { mr.Transformations = nil }, "meaning.field"},
		{"label_origin", func(mr *MeaningRecord) { mr.LabelOrigin = "guess" }, "meaning.field"},
	}
	for _, tc := range stays {
		t.Run(tc.name+" stays required", func(t *testing.T) {
			m, q := derivedManifest(t)
			tc.mutate(findCase(t, &m, derivedCaseID).MeaningRecord)
			wantRule(t, validate(m, q), tc.rule)
		})
	}
}

// benchmarkSourceFields are the seven benchmark-source members of a meaning record, by JSON name.
var benchmarkSourceFields = []string{"benchmark_key", "benchmark_revision", "benchmark_digest", "source_item_id", "root_id", "license", "redistribution"}

// recordOf returns m in its input form, decoded to generic JSON, and the meaning_record object of case id,
// so that a test can delete one member before Load reads the manifest.
func recordOf(t *testing.T, m Manifest, id string) (map[string]any, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc["cases"].([]any) {
		if obj := c.(map[string]any); obj["case_id"] == id {
			return doc, obj["meaning_record"].(map[string]any)
		}
	}
	t.Fatalf("case %q not found", id)
	return nil, nil
}

// encodeDoc returns the JSON text of a decoded manifest.
func encodeDoc(t *testing.T, doc map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A derived record does not carry the benchmark-source members, so Load accepts each one absent from it,
// and the record stays valid.
func TestDerivedRecordOmitsBenchmarkSourceFields(t *testing.T) {
	for _, name := range benchmarkSourceFields {
		t.Run(name, func(t *testing.T) {
			m, q := derivedManifest(t)
			doc, mr := recordOf(t, m, derivedCaseID)
			delete(mr, name)
			got, err := Load(strings.NewReader(encodeDoc(t, doc)))
			if err != nil {
				t.Fatalf("Load without %s: %v", name, err)
			}
			wantClean(t, Validate(got, q))
		})
	}
}

// Without a derived member, the benchmark-source members stay required: Load refuses each one that is absent.
func TestEstablishedRecordRequiresBenchmarkSourceFields(t *testing.T) {
	const id = "SYNTH-ADV-TM-A-001"
	for _, name := range benchmarkSourceFields {
		t.Run(name, func(t *testing.T) {
			m, _ := derivedManifest(t)
			findCase(t, &m, id).MeaningRecord = goodEstablished()
			doc, mr := recordOf(t, m, id)
			delete(mr, name)
			wantLoadError(t, encodeDoc(t, doc), `missing field "`+name+`"`)
		})
	}
}

// A derived case survives Load, the canonical form and Load again, with the same digest and the same record.
func TestDerivedCaseRoundTrip(t *testing.T) {
	m, q := derivedManifest(t)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	canon, err := CanonicalJSON(loaded)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	again, err := Load(bytes.NewReader(canon))
	if err != nil {
		t.Fatalf("Load of the canonical form: %v", err)
	}
	want, err := Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]Manifest{"loaded": loaded, "reloaded": again} {
		if d, err := Digest(got); err != nil || d != want {
			t.Errorf("%s: digest = %s (err %v); want %s", name, d, err, want)
		}
		c := findCase(t, &got, derivedCaseID)
		if c.Adversarial.SamplePart != SampleTMC || !reflect.DeepEqual(c.MeaningRecord, goodDerived()) {
			t.Errorf("%s: sample part %q and record %+v; want tmc with the derived record", name, c.Adversarial.SamplePart, c.MeaningRecord)
		}
	}
	wantClean(t, validate(again, q))
}

// Each derived member is part of the digest: clearing it changes Digest.
func TestDerivedFieldsEnterTheDigest(t *testing.T) {
	base, _ := derivedManifest(t)
	full, err := Digest(base)
	if err != nil {
		t.Fatal(err)
	}
	wipe := map[string]func(c *Case){
		"sample_part":    func(c *Case) { c.Adversarial.SamplePart = "" },
		"mechanism_id":   func(c *Case) { c.MeaningRecord.MechanismID = "" },
		"derived":        func(c *Case) { c.MeaningRecord.Derived = nil },
		"owasp.key":      func(c *Case) { c.MeaningRecord.Derived.OWASP.Key = "" },
		"owasp.revision": func(c *Case) { c.MeaningRecord.Derived.OWASP.Revision = "" },
		"owasp.digest":   func(c *Case) { c.MeaningRecord.Derived.OWASP.Digest = "" },
		"owasp.entry_id": func(c *Case) { c.MeaningRecord.Derived.OWASP.EntryID = "" },
		"mitre.key":      func(c *Case) { c.MeaningRecord.Derived.MITRE.Key = "" },
		"mitre.revision": func(c *Case) { c.MeaningRecord.Derived.MITRE.Revision = "" },
		"mitre.digest":   func(c *Case) { c.MeaningRecord.Derived.MITRE.Digest = "" },
		"mitre.entry_id": func(c *Case) { c.MeaningRecord.Derived.MITRE.EntryID = "" },
		"author":         func(c *Case) { c.MeaningRecord.Derived.Author = "" },
		"authored_on":    func(c *Case) { c.MeaningRecord.Derived.AuthoredOn = "" },
	}
	for name, mutate := range wipe {
		t.Run(name, func(t *testing.T) {
			m, _ := derivedManifest(t)
			mutate(findCase(t, &m, derivedCaseID))
			if d, err := Digest(m); err != nil || d == full {
				t.Errorf("clearing %s: digest = %s (err %v); want a change from %s", name, d, err, full)
			}
		})
	}
}
