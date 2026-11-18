package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/runner"
)

// claimInputs are the arguments of claimReasons.
type claimInputs struct {
	p          plan
	realLedger bool
	rep        evalreport.Report
	incomplete int
	reg        *evalreport.Registration
}

func (c claimInputs) reasons() []string {
	return claimReasons(c.p, c.realLedger, c.rep, c.incomplete, c.reg)
}

// claimFixture is a run that meets every condition: a thesis plan, a real ledger, a complete run with certified
// scenario and mechanism results, and a registration of exactly this run.
func claimFixture() claimInputs {
	p := plan{cfg: runConfig{QuotaProfile: quotaThesis, FreezeCommit: commit}, digest: fixtureDigest("manifest"),
		sched:        runner.Schedule{Digest: fixtureDigest("plan"), RunBinding: fixtureDigest("binding")},
		attributions: attributionSet{rubricDigest: fixtureDigest("rubric")}}
	rep := evalreport.Report{Coverage: evalreport.Coverage{Exact: true},
		Groups: &evalreport.Groups{Certification: evalreport.Certification{Certified: true}}}
	reg := evalreport.Registration{FreezeCommit: commit, CorpusManifestDigest: p.digest, QuotaProfile: quotaThesis,
		RunnerPlanDigest: p.sched.Digest, RunBinding: p.sched.RunBinding, AttributionRubricDigest: fixtureDigest("rubric")}
	return claimInputs{p: p, realLedger: true, rep: rep, reg: &reg}
}

// A report is claim-bearing only when every condition holds, and each missing condition is named.
func TestClaimReasonsNameEachMissingCondition(t *testing.T) {
	if got := claimFixture().reasons(); len(got) != 0 {
		t.Fatalf("the registered complete thesis run is not claim-bearing: %v", got)
	}
	for want, mutate := range map[string]func(*claimInputs){
		reasonLedgerSynthetic:                           func(c *claimInputs) { c.realLedger = false },
		reasonNotThesis:                                 func(c *claimInputs) { c.p.cfg.QuotaProfile, c.reg.QuotaProfile = quotaPilot, quotaPilot },
		reasonRunIncomplete:                             func(c *claimInputs) { c.rep.Coverage.Exact = false },
		reasonNoGroups:                                  func(c *claimInputs) { c.rep.Groups = nil },
		reasonNotCertified:                              func(c *claimInputs) { c.rep.Groups.Certification.Certified = false },
		reasonNotRegistered:                             func(c *claimInputs) { c.reg = nil },
		reasonMismatchPrefix + "run_binding":            func(c *claimInputs) { c.p.sched.RunBinding = fixtureDigest("drain grace 30") },
		reasonMismatchPrefix + "runner_plan_digest":     func(c *claimInputs) { c.p.sched.Digest = fixtureDigest("other plan") },
		reasonMismatchPrefix + "corpus_manifest_digest": func(c *claimInputs) { c.p.digest = fixtureDigest("other manifest") },
		reasonMismatchPrefix + "freeze_commit":          func(c *claimInputs) { c.p.cfg.FreezeCommit = strings.Repeat("f", 40) },
		reasonMismatchPrefix + "attribution_rubric_digest": func(c *claimInputs) {
			c.p.attributions.rubricDigest = fixtureDigest("other rubric")
		},
	} {
		c := claimFixture()
		mutate(&c)
		if got := c.reasons(); !slices.Equal(got, []string{want}) {
			t.Errorf("%s: reasons %v", want, got)
		}
	}
	// An unfinished step makes the run incomplete even when every case has a record.
	c := claimFixture()
	c.incomplete = 1
	if got := c.reasons(); !slices.Equal(got, []string{reasonRunIncomplete}) {
		t.Errorf("incomplete step: reasons %v", got)
	}
	// So does a unit still waiting for its registered rerun.
	c = claimFixture()
	c.rep.Observed.PendingExternalReruns = 1
	if got := c.reasons(); !slices.Equal(got, []string{reasonRunIncomplete}) {
		t.Errorf("pending rerun: reasons %v", got)
	}
	// A run without an attribution file has no rubric to compare: the friction candidates stay unresolved instead.
	c = claimFixture()
	c.p.attributions = attributionSet{}
	if got := c.reasons(); len(got) != 0 {
		t.Errorf("no attribution file: reasons %v", got)
	}
}

// writeRegistration writes the record of the env's run, with mutate applied before the digest.
func writeRegistration(t *testing.T, e *env, runBinding string, mutate func(*evalreport.Registration)) string {
	t.Helper()
	digest, err := evalcorpus.Digest(e.manifest)
	if err != nil {
		t.Fatal(err)
	}
	reg := evalreport.Registration{Domain: evalreport.RegistrationDomain, FreezeCommit: commit, CorpusManifestDigest: digest,
		QuotaProfile: quotaPilot, RunnerPlanDigest: e.sched().Digest, RunBinding: runBinding, Reviewers: []string{"owner"}}
	if mutate != nil {
		mutate(&reg)
	}
	if reg.RecordDigest, err = reg.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	return e.file("registration-"+reg.RecordDigest[7:15]+".json", string(raw))
}

// `plan --registration` refuses a run that is not the registered one before any unit is spent, and the report
// names the mismatch; a report of the registered pilot run stays non-claim-bearing for its other reasons.
func TestPlanAndReportCheckTheRegistration(t *testing.T) {
	e := newEnv(t)
	out, err := e.cliCfg(nil, "plan")
	if err != nil {
		t.Fatal(err)
	}
	binding := line(out, "run_binding")
	match := writeRegistration(t, e, binding, nil)
	if out, err := e.cliCfg(nil, "plan", "--registration", match); err != nil || !strings.Contains(out, "matches") {
		t.Fatalf("the registered run was refused: %v\n%s", err, out)
	}
	other := writeRegistration(t, e, fixtureDigest("drain grace 30"), nil)
	if _, err := e.cliCfg(nil, "plan", "--registration", other); err == nil || !strings.Contains(err.Error(), "run_binding") {
		t.Fatalf("a run with another run binding passed the registration: %v", err)
	}
	tampered, _ := os.ReadFile(match)
	forged := e.file("forged.json", strings.Replace(string(tampered), `"quota_profile":"pilot"`, `"quota_profile":"thesis"`, 1))
	if _, err := e.cliCfg(nil, "plan", "--registration", forged); err == nil || !strings.Contains(err.Error(), "record_digest") {
		t.Fatalf("an edited record was accepted: %v", err)
	}

	if _, err := e.cliCfg(fakeWire(newFake(e)), "run"); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		registration string
		want, absent string
	}{
		"registered": {match, reasonNotThesis, reasonMismatchPrefix},
		"other":      {other, reasonMismatchPrefix + "run_binding", reasonNotRegistered},
		"none":       {"", reasonNotRegistered, reasonMismatchPrefix},
	} {
		args := []string{"report", "--out", e.path("report-" + name)}
		if tc.registration != "" {
			args = append(args, "--registration", tc.registration)
		}
		out, err := e.cliCfg(nil, args...)
		if err != nil || line(out, "claim_bearing") != "false" {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		var meta struct {
			NotClaimBearing []string `json:"not_claim_bearing"`
			Registration    string   `json:"registration_record_digest"`
		}
		raw, _ := os.ReadFile(filepath.Join(e.path("report-"+name), "report.meta.json"))
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(meta.NotClaimBearing, ",")
		if !strings.Contains(joined, tc.want) || strings.Contains(joined, tc.absent) ||
			!slices.Contains(meta.NotClaimBearing, reasonLedgerSynthetic) || !slices.Contains(meta.NotClaimBearing, reasonNoGroups) {
			t.Errorf("%s: reasons %v, want %s and not %s", name, meta.NotClaimBearing, tc.want, tc.absent)
		}
		md, _ := os.ReadFile(filepath.Join(e.path("report-"+name), "report.md"))
		if !strings.HasPrefix(string(md), "> NON-CLAIM-BEARING ("+strings.Join(meta.NotClaimBearing, ", ")+")") {
			t.Errorf("%s: report.md does not lead with the reasons:\n%.300s", name, md)
		}
	}
	// The pilot manifest pins no main draw: scenario and mechanism results cannot be built from it.
	if _, err := e.cliCfg(nil, "report", "--out", e.path("report-groups"), "--ledger-v3", e.file("ledger.jsonl", "")); err == nil ||
		!strings.Contains(err.Error(), "pins no D48 draw") {
		t.Fatalf("--ledger-v3 without a pinned draw: %v", err)
	}
}
