package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/usdledger"
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
		Groups: &evalreport.Groups{Certification: evalreport.Certification{Certified: true, Digest: fixtureDigest("certify")}}}
	reg := evalreport.Registration{FreezeCommit: commit, CorpusManifestDigest: p.digest, QuotaProfile: quotaThesis,
		RunnerPlanDigest: p.sched.Digest, RunBinding: p.sched.RunBinding, AttributionRubricDigest: fixtureDigest("rubric"),
		EvalscoreSchema: evalscore.SchemaVersion, CertificationDigest: fixtureDigest("certify")}
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
		reasonMismatchPrefix + "certification_digest":   func(c *claimInputs) { c.rep.Groups.Certification.Digest = fixtureDigest("other certify") },
		reasonMismatchPrefix + "plan_steps":             func(c *claimInputs) { c.reg.PlanSteps = 7 },
		reasonMismatchPrefix + "evalscore_schema":       func(c *claimInputs) { c.reg.EvalscoreSchema = "evalscore-trials/v0" },
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
	// A run without an attribution file does not bind the registered rubric.
	c = claimFixture()
	c.p.attributions = attributionSet{}
	if got := c.reasons(); !slices.Equal(got, []string{reasonMismatchPrefix + "attribution_rubric_digest"}) {
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
		QuotaProfile: quotaPilot, RunnerPlanDigest: e.sched().Digest, PlanSteps: e.sched().Units(), EvalscoreSchema: evalscore.SchemaVersion,
		RunBinding: runBinding, AttributionRubricDigest: fixtureDigest("rubric"), CorpusCheckDigest: fixtureDigest("corpus-check"),
		Reviewers: []string{"owner"}}
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

// `run --registration` refuses a run that is not the registered one before any unit is spent, and the report
// names the mismatch; a report of the registered pilot run stays non-claim-bearing for its other reasons. `plan`
// has no registration check of its own: run's check comes first.
func TestRunAndReportCheckTheRegistration(t *testing.T) {
	e := newEnv(t)
	out, err := e.cliCfg(nil, "plan")
	if err != nil {
		t.Fatal(err)
	}
	binding := line(out, "run_binding")
	match := writeRegistration(t, e, binding, nil)
	if _, err := e.cliCfg(nil, "plan", "--registration", match); !errors.Is(err, errUsage) {
		t.Fatalf("plan --registration: %v, want a usage error", err)
	}
	other := writeRegistration(t, e, fixtureDigest("drain grace 30"), nil)
	tampered, _ := os.ReadFile(match)
	forged := e.file("forged.json", strings.Replace(string(tampered), `"quota_profile":"pilot"`, `"quota_profile":"thesis"`, 1))
	noCheck := writeRegistration(t, e, binding, func(r *evalreport.Registration) { r.CorpusCheckDigest = "" })
	for _, tc := range []struct{ record, want string }{
		{other, "run_binding"}, {forged, "record_digest"}, {noCheck, "corpus_check_digest is empty"},
	} {
		refused := newFake(e)
		if _, err := e.cliCfg(fakeWire(refused), "run", "--registration", tc.record); err == nil || !strings.Contains(err.Error(), tc.want) || len(refused.Calls()) != 0 {
			t.Fatalf("run with %s: %v, %d steps executed; want a refusal naming %s", tc.record, err, len(refused.Calls()), tc.want)
		}
	}
	if _, err := e.cliCfg(fakeWire(newFake(e)), "run", "--registration", match); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		registration string
		want, absent string
	}{
		"registered": {match, reasonNotThesis, reasonMismatchPrefix + "run_binding"},
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
		// The pilot run has no attribution file, so only the rubric differs from the registered run.
		if name == "registered" && (strings.Count(joined, reasonMismatchPrefix) != 1 ||
			!slices.Contains(meta.NotClaimBearing, reasonMismatchPrefix+"attribution_rubric_digest")) {
			t.Errorf("registered: reasons %v, want the attribution rubric as the only mismatch", meta.NotClaimBearing)
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

// A top-up or TM-C case without a record, or still waiting for its rerun, leaves the run unfinished, although the
// H1 coverage and rerun counts (over the main sample) are complete. A closed run (--close-reruns) has no pending
// rerun left there either. The reports are built by evalreport.Build.
func TestSeparateStrataLeaveTheRunIncomplete(t *testing.T) {
	build := func(topup *evalscore.AdversarialRecord, closed bool) evalreport.Report {
		in := evalreport.Inputs{
			Manifest: evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: "s", Cases: []evalcorpus.Case{
				advCase("main-1", evalcorpus.ClassTMA, evalcorpus.SampleMain, ""), advCase("topup-1", evalcorpus.ClassTMA, evalcorpus.SampleTopup, ""),
				cmpCase("task-1", evalcorpus.ToolRead)}},
			Trials: evalscore.Input{Schema: evalscore.SchemaVersion, EVQPassed: true,
				Adversarial: []evalscore.AdversarialRecord{cleanAdv("main-1")}, Compliant: []evalscore.CompliantRecord{cleanCmp("task-1")},
				RerunsClosed: closed},
			Ledger: testLedger(),
		}
		if topup != nil {
			in.Trials.Adversarial = append(in.Trials.Adversarial, *topup)
		}
		rep, err := evalreport.Build(in)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	pending := cleanAdv("topup-1")
	pending.EvidenceLoss, pending.OutageEvidenceDigest = evalscore.LossExternalOutage, fixtureDigest("outage")
	done := cleanAdv("topup-1")
	for name, tc := range map[string]struct {
		topup  *evalscore.AdversarialRecord
		closed bool
		want   bool
	}{"pending rerun": {&pending, false, true}, "no record": {nil, false, true}, "finished": {&done, false, false},
		"pending rerun, run closed": {&pending, true, false}} {
		rep := build(tc.topup, tc.closed)
		if !rep.Coverage.Exact || rep.Observed.PendingExternalReruns != 0 {
			t.Fatalf("%s: the H1 figures see the top-up: %+v %+v", name, rep.Coverage, rep.Observed)
		}
		c := claimFixture()
		c.rep.Separate, c.rep.Coverage, c.rep.Observed = rep.Separate, rep.Coverage, rep.Observed
		if got := slices.Contains(c.reasons(), reasonRunIncomplete); got != tc.want {
			t.Errorf("%s: run_incomplete %t, want %t (reasons %v, separate %+v)", name, got, tc.want, c.reasons(), rep.Separate)
		}
	}
}

func advCase(id string, class evalcorpus.ThreatClass, part evalcorpus.SamplePart, root string) evalcorpus.Case {
	return evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindAdversarial, Adversarial: &evalcorpus.Adversarial{ThreatClass: class,
		Partition: evalcorpus.PartitionNonCold, SamplePart: part, RootScenarioID: root}}
}

func cmpCase(id string, tool evalcorpus.Tool) evalcorpus.Case {
	return evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindCompliant, Compliant: &evalcorpus.Compliant{PrimaryTool: tool}}
}

func cleanAdv(id string) evalscore.AdversarialRecord {
	return evalscore.AdversarialRecord{CaseID: id, Attempt: 1, Channel: "tool_call", IdentityVerified: true,
		ObserverCoverageComplete: true, EvidenceLoss: evalscore.LossNone, CEffect: evalscore.CEffectNone,
		PStaticManifested: true, PAdaptiveManifested: true, CMatchedDelivery: true, PCEvidenceComplete: true}
}

func cleanCmp(id string) evalscore.CompliantRecord {
	return evalscore.CompliantRecord{TaskID: id, Attempt: 1, IdentityVerified: true, ObserverCoverageComplete: true,
		EvidenceLoss: evalscore.LossNone, BSuccess: true, CSuccess: true, VSuccess: true, Attribution: evalscore.AttributionNotApplicable}
}

func testLedger() usdledger.Snapshot {
	return usdledger.Snapshot{LedgerID: "l", CapMicroUSD: evalreport.RegisteredCapMicroUSD, MinPerUnitWorstCaseMicroUSD: 1,
		RemainingMicroUSD: evalreport.RegisteredCapMicroUSD, HeadSHA256: strings.Repeat("a", 64)}
}

// groupedReport builds through evalreport.Build a thesis report with real scenario and mechanism results: the
// main-draw test vector (evalcorpus testdata/d48) with its 299 main cases and 5 top-ups by their ledger v3 roots,
// 100 compliant tasks, every case clean, ledger v3 and the vector's certify output. mutate edits the trials.
func groupedReport(t *testing.T, mutate func(*evalscore.Input)) evalreport.Report {
	t.Helper()
	read := func(path string) []byte {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	draw := read("../../internal/evalcorpus/testdata/d48/draw-output.json")
	d, err := evalcorpus.ParseDraw(draw)
	if err != nil {
		t.Fatal(err)
	}
	in := evalreport.Inputs{
		Manifest: evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: "s", Pins: evalcorpus.Pins{DrawDigest: d.Digest}},
		Trials:   evalscore.Input{Schema: evalscore.SchemaVersion, EVQPassed: true},
		Ledger:   testLedger(),
		D48: &evalreport.D48Inputs{Draw: draw, Ledger: read("../../../corpus/sampling/d48-ledger-v3.jsonl"),
			Certification: read("../../internal/evalreport/testdata/certify-output.json")},
	}
	for _, class := range []evalcorpus.ThreatClass{evalcorpus.ClassTMA, evalcorpus.ClassTMB} {
		for _, part := range []evalcorpus.SamplePart{evalcorpus.SampleMain, evalcorpus.SampleTopup} {
			roots := d.Members[class]
			if part == evalcorpus.SampleTopup {
				roots = d.Topup[class]
			}
			for _, root := range roots {
				in.Manifest.Cases = append(in.Manifest.Cases, advCase("case-"+root, class, part, root))
				in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv("case-"+root))
			}
		}
	}
	for i := 0; i < evalscore.RegisteredCompliantTasks; i++ {
		id := fmt.Sprintf("task-%03d", i)
		in.Manifest.Cases = append(in.Manifest.Cases, cmpCase(id, []evalcorpus.Tool{evalcorpus.ToolRead, evalcorpus.ToolWrite, evalcorpus.ToolEdit, evalcorpus.ToolBash}[i%4]))
		in.Trials.Compliant = append(in.Trials.Compliant, cleanCmp(id))
	}
	if mutate != nil {
		mutate(&in.Trials)
	}
	rep, err := evalreport.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// A report built by evalreport.Build with real scenario and mechanism results is claim-bearing for the complete
// registered thesis run, whatever its verdict. An unfinished run with a main-sample escape is not, and it carries
// the observed-escape line; an unfinished run without an escape, or a finished run with one, does not.
func TestBuiltReportWithGroupsIsClaimBearing(t *testing.T) {
	escapeFirst := func(in *evalscore.Input) { in.Adversarial[0].CEffect = evalscore.CEffectObserved }
	dropSecond := func(in *evalscore.Input) { in.Adversarial = slices.Delete(in.Adversarial, 1, 2) }
	for name, tc := range map[string]struct {
		mutate     func(*evalscore.Input)
		reasons    []string
		escapeNote bool
	}{
		"clean":             {nil, nil, false},
		"finished escape":   {escapeFirst, nil, false},
		"unfinished":        {dropSecond, []string{reasonRunIncomplete}, false},
		"unfinished escape": {func(in *evalscore.Input) { escapeFirst(in); dropSecond(in) }, []string{reasonRunIncomplete}, true},
	} {
		c := claimFixture()
		c.rep = groupedReport(t, tc.mutate)
		c.reg.CertificationDigest = c.rep.Groups.Certification.Digest
		got := c.reasons()
		note := observedEscapeNote(got, c.rep)
		if !slices.Equal(got, tc.reasons) || (note != "") != tc.escapeNote || (tc.escapeNote && !strings.Contains(note, "1 main-sample case(s) escaped")) {
			t.Errorf("%s: reasons %v, observed escape %q", name, got, note)
		}
		if name == "finished escape" && (!c.rep.H1.ObservedSecurityFailure || c.rep.H1.RuleSatisfied) {
			t.Errorf("finished escape: H1 %+v, want an observed security failure", c.rep.H1)
		}
	}
}

// The registration record plans from a runner config that holds only what tbound-run's runnerConfig sets
// (tbound-profile preregistration-record refuses every other runner field): whatever run.json says, runnerConfig
// leaves those fields zero, so the two tools plan from the same config.
func TestRunnerConfigSetsOnlyTheRegistrableFields(t *testing.T) {
	e := newEnv(t)
	cfg, err := loadConfig(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OnlyCases, cfg.Purpose = []string{"a"}, runner.PurposeConstructWitness
	rc := cfg.runnerConfig()
	if rc.NET02Mandatory || rc.UnitCap != 0 || rc.RerunHeadroom != 0 || rc.NoRerunHeadroom || rc.CDH != nil {
		t.Fatalf("runnerConfig sets a field the registration refuses: %+v", rc)
	}
	raw, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"freeze_commit": true, "evq_obligations": true, "adaptive_caps": true, "only_cases": true, "purpose": true}
	for k, v := range fields {
		if !allowed[k] && fmt.Sprint(v) != "0" && fmt.Sprint(v) != "false" {
			t.Errorf("runnerConfig sets %s = %v", k, v)
		}
	}
}

func TestRegistrationMismatchesNamePlanV(t *testing.T) {
	var p plan
	p.sched.Config.PlanV = true
	reg := evalreport.Registration{EvalscoreSchema: evalscore.SchemaVersion, PlanSteps: p.sched.Units()}
	got := registrationMismatches(reg, p)
	if len(got) != 1 || got[0].field != "plan_v" {
		t.Fatalf("mismatches %+v, want only plan_v", got)
	}
	reg.PlanV = true
	if got := registrationMismatches(reg, p); len(got) != 0 {
		t.Fatalf("mismatches %+v, want none", got)
	}
}
