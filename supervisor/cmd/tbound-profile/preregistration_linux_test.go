//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/runner"
)

const (
	commitA = "0123456789abcdef0123456789abcdef01234567"
	commitB = "fedcba9876543210fedcba9876543210fedcba98"
)

// frozenResearchProfile is a fully decided synthetic research profile.
func frozenResearchProfile(seed string) string {
	return `{
  "schema_version": 1,
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "deepseek/deepseek-v4.1-flash",
  "routing": {"order": ["example-provider"], "allow_fallbacks": false, "data_collection": "deny",
              "quantizations": [], "require_parameters": true, "zdr": true},
  "input_micro_usd_per_1m_tokens": 150000,
  "output_micro_usd_per_1m_tokens": 600000,
  "max_price_micro_usd_per_1m_tokens_in": 300000,
  "max_price_micro_usd_per_1m_tokens_out": 1200000,
  "max_input_tokens": 20000,
  "input_framing_reserve_tokens": 512,
  "max_output_tokens": 2000,
  "max_requests_per_unit": 5,
  "temperature_milli": 0,
  "seed": ` + seed + `,
  "reasoning": {"enabled": false},
  "timeout_ms": 5000,
  "frozen_at": "2026-10-17T00:00:00Z"
}`
}

func ledgerConfigText(capMicro string) string {
	return `{"schema_version":1,"ledger_id":"research-test","cap_micro_usd":` + capMicro +
		`,"min_per_unit_worst_case_micro_usd":1000,"reserve_basis":"request"}`
}

func runnerConfigFile(t *testing.T, dir, name, commit string, mutate func(*runner.Config)) string {
	t.Helper()
	cfg := runner.Config{FreezeCommit: commit, EVQObligations: []string{"EVQ-1", "EVQ-2"},
		AdaptiveCaps: calibration.Caps{Iterations: 8, ToolCalls: 50, WallMS: 60000, InTokens: 100000, OutTokens: 50000, MicroUSD: 10000}}
	if mutate != nil {
		mutate(&cfg)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, dir, name, string(raw))
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// preregInputs is one complete, valid set of command-line inputs.
type preregInputs struct {
	panel                                                               []string
	commit, manifest, quota, config, profile, ledger, reviewers, schema string
}

func (p preregInputs) args() []string {
	args := []string{"preregistration-record", "--freeze-commit", p.commit, "--corpus-manifest", p.manifest,
		"--quota-profile", p.quota, "--runner-config", p.config, "--reviewers", p.reviewers}
	if p.profile != "" {
		args = append(args, "--research-profile", p.profile)
	}
	if p.ledger != "" {
		args = append(args, "--research-ledger-config", p.ledger)
	}
	if p.schema != "" {
		args = append(args, "--evalscore-schema", p.schema)
	}
	return append(args, p.panel...)
}

func basePrereg(t *testing.T, dir string) preregInputs {
	t.Helper()
	m, panel := signedPanel(t, dir, fixtureManifest(t, evalcorpus.PilotQuotas(), "prereg-seed"))
	return preregInputs{panel: panel,
		commit: commitA, manifest: writeManifest(t, dir, "corpus.json", m), quota: "pilot",
		config:    runnerConfigFile(t, dir, "runner.json", commitA, nil),
		profile:   writeFile(t, dir, "profile.json", frozenResearchProfile("7")),
		ledger:    writeFile(t, dir, "ledger.json", ledgerConfigText("5000000")),
		reviewers: "owner,advisor",
	}
}

func preregOut(t *testing.T, p preregInputs) string {
	t.Helper()
	out, _, err := runCLI(p.args()...)
	if err != nil {
		t.Fatalf("preregistration-record: %v", err)
	}
	return out
}

func TestPreregistrationRecordIsDeterministicAndBindsTheRegisteredFacts(t *testing.T) {
	dir := t.TempDir()
	p := basePrereg(t, dir)
	first := preregOut(t, p)
	if second := preregOut(t, p); second != first {
		t.Fatalf("not deterministic:\n%s\n%s", first, second)
	}
	if !strings.HasSuffix(first, "\n") || strings.Count(first, "\n") != 1 {
		t.Fatalf("output must be one JSON line: %q", first)
	}
	var rec preregistrationRecord
	dec := json.NewDecoder(strings.NewReader(first))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p.manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := evalcorpus.Load(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	m, err := evalcorpus.Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if evalreport.PlannedUnits != 1935 || runner.MaxRerunHeadroom != 97 {
		t.Fatalf("registered constants drifted: %d units, headroom %d", evalreport.PlannedUnits, runner.MaxRerunHeadroom)
	}
	if rec.Domain != "tbound-preregistration/v1" || rec.FreezeCommit != commitA || rec.CorpusManifestDigest != m ||
		rec.QuotaProfile != "pilot" || rec.EvalscoreSchema != evalscore.SchemaVersion ||
		rec.PlannedUnits != evalreport.PlannedUnits ||
		rec.RerunHeadroom != runner.MaxRerunHeadroom ||
		rec.PlanSteps != 123 || !strings.HasPrefix(rec.RunnerPlanDigest, "sha256:") ||
		!strings.HasPrefix(rec.ResearchProfileDigest, "sha256:") || !strings.HasPrefix(rec.ResearchLedgerConfigDigest, "sha256:") {
		t.Fatalf("record %+v", rec)
	}
	if !reflect.DeepEqual(rec.Reviewers, []string{"advisor", "owner"}) {
		t.Fatalf("reviewers %v", rec.Reviewers)
	}
	want, err := rec.computeDigest()
	if err != nil || rec.RecordDigest != want {
		t.Fatalf("record digest %q, recomputed %q (%v)", rec.RecordDigest, want, err)
	}
	// Reviewer order does not matter.
	q := p
	q.reviewers = "advisor, owner"
	if got := preregOut(t, q); got != first {
		t.Fatalf("reviewer order changed the record:\n%s\n%s", first, got)
	}
}

func TestPreregistrationRecordOptionalResearchInputs(t *testing.T) {
	dir := t.TempDir()
	p := basePrereg(t, dir)
	p.profile, p.ledger = "", ""
	var rec preregistrationRecord
	if err := json.Unmarshal([]byte(preregOut(t, p)), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.ResearchProfileDigest != "" || rec.ResearchLedgerConfigDigest != "" {
		t.Fatalf("absent inputs must be recorded as empty: %+v", rec)
	}
}

func TestPreregistrationRecordEveryInputChangesTheDigest(t *testing.T) {
	dir := t.TempDir()
	base := basePrereg(t, dir)
	baseOut := preregOut(t, base)
	digestOf := func(out string) string {
		var rec preregistrationRecord
		if err := json.Unmarshal([]byte(out), &rec); err != nil {
			t.Fatal(err)
		}
		return rec.RecordDigest
	}
	baseDigest := digestOf(baseOut)

	other, otherPanel := signedPanel(t, dir, fixtureManifest(t, evalcorpus.PilotQuotas(), "prereg-seed-b"))
	otherManifest := writeManifest(t, dir, "corpus-b.json", other)
	for name, mutate := range map[string]func(*preregInputs){
		"freeze commit": func(p *preregInputs) {
			p.commit = commitB
			p.config = runnerConfigFile(t, dir, "runner-b.json", commitB, nil)
		},
		"corpus manifest": func(p *preregInputs) { p.manifest, p.panel = otherManifest, otherPanel },
		"runner config": func(p *preregInputs) {
			p.config = runnerConfigFile(t, dir, "runner-c.json", commitA, func(c *runner.Config) { c.NET02Mandatory = true })
		},
		"runner config evq": func(p *preregInputs) {
			p.config = runnerConfigFile(t, dir, "runner-d.json", commitA, func(c *runner.Config) { c.EVQObligations = []string{"EVQ-1"} })
		},
		"runner config headroom": func(p *preregInputs) {
			p.config = runnerConfigFile(t, dir, "runner-e.json", commitA, func(c *runner.Config) { c.RerunHeadroom = 50 })
		},
		"research profile value":  func(p *preregInputs) { p.profile = writeFile(t, dir, "profile-b.json", frozenResearchProfile("8")) },
		"research profile absent": func(p *preregInputs) { p.profile = "" },
		"ledger config value":     func(p *preregInputs) { p.ledger = writeFile(t, dir, "ledger-b.json", ledgerConfigText("6000000")) },
		"ledger config absent":    func(p *preregInputs) { p.ledger = "" },
		"reviewers":               func(p *preregInputs) { p.reviewers = "owner,advisor,second-reader" },
	} {
		p := base
		mutate(&p)
		out := preregOut(t, p)
		if out == baseOut || digestOf(out) == baseDigest {
			t.Errorf("%s: record digest did not change", name)
		}
	}

	// Inputs that the command line cannot vary while the record stays valid are checked on the
	// record itself: changing any bound field changes the digest.
	var rec preregistrationRecord
	if err := json.Unmarshal([]byte(baseOut), &rec); err != nil {
		t.Fatal(err)
	}
	typ := reflect.TypeOf(rec)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if name == "RecordDigest" {
			continue
		}
		mutated := rec
		f := reflect.ValueOf(&mutated).Elem().Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString(f.String() + "x")
		case reflect.Int:
			f.SetInt(f.Int() + 1)
		case reflect.Slice:
			f.Set(reflect.Append(f, reflect.ValueOf("extra")))
		default:
			t.Fatalf("field %s has kind %s", name, f.Kind())
		}
		d, err := mutated.computeDigest()
		if err != nil || d == rec.RecordDigest {
			t.Errorf("field %s is not bound by the record digest (%v)", name, err)
		}
	}
	// The digest is domain-labelled: the same body under another label hashes differently.
	relabelled := rec
	relabelled.Domain = "tbound-preregistration/v2"
	if d, _ := relabelled.computeDigest(); d == rec.RecordDigest {
		t.Error("the domain label is not bound")
	}
}

func TestPreregistrationRecordRefusals(t *testing.T) {
	dir := t.TempDir()
	base := basePrereg(t, dir)
	pilotOnly := writeManifest(t, dir, "invalid.json", func() evalcorpus.Manifest {
		m := fixtureManifest(t, evalcorpus.PilotQuotas(), "prereg-seed")
		m.Cases[2].Trial = nil
		return m
	}())
	unfrozen := writeFile(t, dir, "profile-draft.json", strings.Replace(frozenResearchProfile("7"), `"frozen_at": "2026-10-17T00:00:00Z"`, `"frozen_at": null`, 1))
	for name, mutate := range map[string]func(*preregInputs){
		"short commit":                          func(p *preregInputs) { p.commit = commitA[:39] },
		"uppercase commit":                      func(p *preregInputs) { p.commit = strings.ToUpper(commitA) },
		"non-hex commit":                        func(p *preregInputs) { p.commit = strings.Repeat("g", 40) },
		"64-digit commit":                       func(p *preregInputs) { p.commit = strings.Repeat("a", 64) },
		"empty commit":                          func(p *preregInputs) { p.commit = "" },
		"commit differs from the runner config": func(p *preregInputs) { p.commit = commitB },
		"thesis quotas on a pilot manifest":     func(p *preregInputs) { p.quota = "thesis" },
		"unknown quota profile":                 func(p *preregInputs) { p.quota = "bogus" },
		"missing quota profile":                 func(p *preregInputs) { p.quota = "" },
		"invalid manifest":                      func(p *preregInputs) { p.manifest = pilotOnly },
		"missing manifest":                      func(p *preregInputs) { p.manifest = filepath.Join(dir, "absent.json") },
		"missing runner config":                 func(p *preregInputs) { p.config = filepath.Join(dir, "absent-runner.json") },
		"runner config with unknown field": func(p *preregInputs) {
			p.config = writeFile(t, dir, "runner-bad.json", `{"freeze_commit":"`+commitA+`","surprise":1}`)
		},
		"runner config over the headroom": func(p *preregInputs) {
			p.config = runnerConfigFile(t, dir, "runner-big.json", commitA, func(c *runner.Config) { c.RerunHeadroom = 98 })
		},
		"unfrozen research profile": func(p *preregInputs) { p.profile = unfrozen },
		"bad ledger config":         func(p *preregInputs) { p.ledger = writeFile(t, dir, "ledger-bad.json", `{"schema_version":1}`) },
		"foreign evalscore schema":  func(p *preregInputs) { p.schema = "evalscore-trials/v2" },
		"no reviewers":              func(p *preregInputs) { p.reviewers = "" },
		"empty reviewer name":       func(p *preregInputs) { p.reviewers = "owner,,advisor" },
		"duplicate reviewer":        func(p *preregInputs) { p.reviewers = "owner,owner" },
		"reviewer with a path":      func(p *preregInputs) { p.reviewers = "owner,/etc/passwd" },
		"reviewer with an address":  func(p *preregInputs) { p.reviewers = "owner,a@example.org" },
	} {
		p := base
		mutate(&p)
		out, _, err := runCLI(p.args()...)
		if err == nil || out != "" {
			t.Errorf("%s: out %q err %v, want a refusal with no output", name, out, err)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
	// The foreign-schema refusal is about the value; the registered one is accepted.
	p := base
	p.schema = evalscore.SchemaVersion
	if _, _, err := runCLI(p.args()...); err != nil {
		t.Fatalf("the registered evalscore schema was refused: %v", err)
	}
	// A missing required flag is refused.
	if _, _, err := runCLI("preregistration-record", "--freeze-commit", commitA); err == nil {
		t.Fatal("missing inputs accepted")
	}
}

func TestPreregistrationRecordThesisPlanIsTheRegisteredUnits(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the 419-case thesis-sized synthetic corpus")
	}
	dir := t.TempDir()
	m, panel := signedPanel(t, dir, fixtureManifest(t, evalcorpus.ThesisQuotas(), "prereg-thesis-seed"))
	p := preregInputs{panel: panel, commit: commitA, manifest: writeManifest(t, dir, "thesis.json", m), quota: "thesis",
		config: runnerConfigFile(t, dir, "runner.json", commitA, nil), reviewers: "owner,advisor"}
	var rec preregistrationRecord
	if err := json.Unmarshal([]byte(preregOut(t, p)), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.PlanSteps != 1935 || rec.PlannedUnits != 1935 || rec.RerunHeadroom != 97 || rec.QuotaProfile != "thesis" {
		t.Fatalf("thesis record %+v", rec)
	}
	// The same manifest is not a pilot manifest.
	p.quota = "pilot"
	if _, _, err := runCLI(p.args()...); err == nil {
		t.Fatal("a thesis-sized manifest was recorded under the pilot quotas")
	}
}

func TestPreregistrationRefusesMissingPanelEvidence(t *testing.T) {
	p := basePrereg(t, t.TempDir())
	p.panel = nil
	out, _, err := runCLI(p.args()...)
	if err == nil || out != "" || !strings.Contains(err.Error(), "--panel-") {
		t.Fatalf("missing panel evidence accepted: out %q err %v", out, err)
	}
}

func TestPreregistrationVerifiesAllPanelFiles(t *testing.T) {
	for i, name := range []string{"receipt", "audit", "stats"} {
		t.Run(name, func(t *testing.T) {
			p := basePrereg(t, t.TempDir())
			file := p.panel[i*2+1]
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			out, _, err := runCLI(p.args()...)
			if err == nil || out != "" || !strings.Contains(err.Error(), name) {
				t.Fatalf("changed %s accepted: %q %v", name, out, err)
			}
		})
	}
}
