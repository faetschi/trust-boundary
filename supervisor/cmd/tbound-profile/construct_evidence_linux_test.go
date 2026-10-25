//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/wsoracle"
)

const (
	constructPilotManifest  = "../../../corpus/pilot/manifest.draft.json"
	constructPilotArtifacts = "../../../corpus/pilot/artifacts"
	constructSinkSecret     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	constructNetCase        = "PILOT-ADV-A-01"
	constructWSCase         = "PILOT-ADV-A-03"
	constructReadCase       = "PILOT-CMP-read-01"
	constructWriteCase      = "PILOT-CMP-write-01"
)

func constructPilot(t *testing.T) evalcorpus.Manifest {
	t.Helper()
	m, err := loadConstructManifest(constructPilotManifest)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func constructTestNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		node = "/mnt/c/Program Files/nodejs/node.exe"
		if _, err := os.Stat(node); err != nil {
			t.Fatal("node is required for the pilot regression")
		}
	}
	return node
}

// constructPrivateDir is a 0700 directory under the home directory, as the audit journal of a sink needs.
func constructPrivateDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".tbound-construct-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// sinkTransport is the guest evidence channel of a real sink: net-canary and net-journal are answered by the
// functions the guest subcommand applies.
type sinkTransport struct {
	sink    *netoracle.Sink
	journal string
}

func (g sinkTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	reply := func(v any) ([]byte, []byte, int, error) {
		b, _ := json.Marshal(v)
		return append(b, '\n'), nil, 0, nil
	}
	switch {
	case len(argv) == 5 && argv[0] == runner.EvidenceNetCanary && argv[1] == "--phase" && argv[3] == "--interval":
		ack, err := netoracle.SendCanary(ctx, g.sink.Addr(), constructSinkSecret, argv[2], argv[4])
		if err != nil {
			return nil, []byte(err.Error()), 2, nil
		}
		return reply(runner.NetCanaryDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetCanary, Phase: argv[2], Interval: argv[4], Seq: ack.Seq, Hash: ack.Hash})
	case len(argv) == 3 && argv[0] == runner.EvidenceNetJournal && argv[1] == "--interval":
		raw, err := os.ReadFile(g.journal)
		if err != nil {
			return nil, []byte(err.Error()), 2, nil
		}
		iv, err := netoracle.ExtractInterval(raw, argv[2])
		if err != nil {
			return nil, []byte(err.Error()), 2, nil
		}
		return reply(runner.NetJournalDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetJournal, Interval: argv[2],
			JournalB64: base64.StdEncoding.EncodeToString(iv.Journal), Head: iv.Head, Count: iv.Count, StartSeq: iv.StartSeq, EndSeq: iv.EndSeq})
	}
	return nil, []byte("unknown"), 2, nil
}

// netCollector is a NET collector over a new real sink whose intervals are retained in retain.
func netCollector(t *testing.T, retain string) (*netoracle.Collector, *netoracle.Sink) {
	t.Helper()
	journal := filepath.Join(constructPrivateDir(t), "sink.jsonl")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink, err := netoracle.NewSink(netoracle.SinkConfig{Listener: ln, JournalPath: journal, Secret: constructSinkSecret})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	c := netoracle.NewCollector(sinkTransport{sink: sink, journal: journal})
	c.RetainDir = retain
	return c, sink
}

// worldOpts shapes the producer files a world is made of.
type worldOpts struct {
	// only limits the mapped cases; all of them when empty.
	only []string
	// pNegative cases have a static P that did not manifest the effect; outage cases have an evidenced outage
	// in attempt 1 and are scored by the rerun (positive unless also pNegative).
	pNegative, outage []string
	// acceptBad maps a compliant case to "fixture-passes" or "reference-fails".
	acceptBad map[string]string
}

// constructWorld is a retained capture set: the producers' real output for the pilot manifest, an evidence
// directory with the map that names it, and the run config that pins it.
type constructWorld struct {
	t       *testing.T
	m       evalcorpus.Manifest
	cases   map[string]evalcorpus.Case
	root    string
	ev      string
	config  string
	binary  string
	bind    bindWorld
	expect  constructExpect
	opts    worldOpts
	mapping constructMap
}

func (w *constructWorld) selected(id string) bool {
	return len(w.opts.only) == 0 || slices.Contains(w.opts.only, id)
}

func buildWorld(t *testing.T, opts worldOpts) *constructWorld {
	t.Helper()
	w := &constructWorld{t: t, m: constructPilot(t), cases: map[string]evalcorpus.Case{}, opts: opts, binary: geDigest("frozen tbound-profile")}
	for _, c := range w.m.Cases {
		w.cases[c.CaseID] = c
	}
	w.root = constructPrivateDir(t)
	w.ev = filepath.Join(w.root, "ev")
	for _, d := range []string{"signed", "run", "accept"} {
		if err := os.MkdirAll(filepath.Join(w.ev, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	w.bind = newBindWorld(t, filepath.Join(w.ev, "signed"))
	w.config = filepath.Join(w.root, "run.json")
	w.writeConfig(nil)
	w.writeJournal()
	for _, c := range w.m.Cases {
		if c.Compliant != nil && w.selected(c.CaseID) {
			w.writeAccept(c)
		}
	}
	w.writeMap()
	return w
}

func (w *constructWorld) writeConfig(edit func(map[string]any)) {
	w.t.Helper()
	b := w.bind
	policy := geDigest("store policy")
	doc := map[string]any{"freeze_commit": geCommit, "v_image_digest": b.pins.ImageDigest, "evq_obligations": []string{"EVQ-1", "EVQ-2"},
		"pins": map[string]string{"profile_hash": b.profileDigest, "containment_profile_digest": b.templateDigest, "frozen_source_digest": b.pins.FrozenSourceDigest,
			"base_template_digest": b.pins.BaseTemplateDigest, "admin_pubkey_digest": b.pins.AdminPubkeyDigest,
			"runtime_bundle_digest": b.pins.RuntimeBundleDigest, "runner_digest": b.pins.RunnerDigest},
		"admission_policy": map[string]string{"policy_digest": policy, "metadata_policy_digest": geDigest("metadata policy")}}
	if edit != nil {
		edit(doc)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.config, raw, 0o600); err != nil {
		w.t.Fatal(err)
	}
	w.expect, err = parseConstructExpect(raw, w.binary)
	if err != nil {
		w.t.Fatal(err)
	}
	w.expect.ConfigPath = w.config
}

func (w *constructWorld) input() constructInput {
	return constructInput{Manifest: w.m, Quotas: evalcorpus.PilotQuotas(), Artifacts: os.DirFS(constructPilotArtifacts), ArtifactsDir: constructPilotArtifacts, Expect: w.expect}
}

// writeJournal runs the plan with the runner's own journal and a stand-in for the guest executor that fills the
// static P steps the way a wired one reports them, NET steps through the real collector and sink.
func (w *constructWorld) writeJournal() {
	t := w.t
	t.Helper()
	sched, err := constructSchedule(w.input())
	if err != nil {
		t.Fatal(err)
	}
	sched.RunBinding = geDigest("host attestation inputs")
	fake := runner.NewFakeExecutor(sched.Config)
	fake.Hook = func(s runner.Step, o *runner.Outcome) error {
		if s.Configuration == runner.CfgP && s.Role == runner.RoleStaticReplay {
			w.fillP(s, o)
		}
		return nil
	}
	j, err := runner.OpenJournal(filepath.Join(w.ev, "run", "journal.jsonl"), sched)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	r, err := runner.NewRunner(sched, j, fake, runner.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := r.Run(context.Background()); err != nil || !rep.Finished {
		t.Fatalf("run: %+v %v", rep, err)
	}
	for _, id := range w.opts.outage {
		step := runner.Step{CaseID: id, Configuration: runner.CfgP, Role: runner.RoleStaticReplay}
		if err := r.Rerun(context.Background(), step.ID()); err != nil {
			t.Fatal(err)
		}
	}
}

func (w *constructWorld) fillP(s runner.Step, o *runner.Outcome) {
	t := w.t
	c := w.cases[s.CaseID]
	pred := c.Adversarial.ForbiddenEffects[0]
	negative := slices.Contains(w.opts.pNegative, s.CaseID)
	script, err := evalcorpus.ScriptDigest(c.Trial.Script)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := evalcorpus.AdmissionPolicyDigest(w.expect.PolicyDigest, w.expect.MetadataPolicyDigest, c.Trial.EffectivePDelta() == evalcorpus.PDeltaAdmitAll, c.Trial.ProtectedPaths)
	if err != nil {
		t.Fatal(err)
	}
	o.E01 = runner.E01Identity{Digest: geDigest("e01 " + s.ID()), Verified: true}
	o.Channel = pred
	o.Receipt = &runner.ReceiptFacts{ProfileHash: w.bind.profileDigest, ContainmentProfileDigest: w.bind.templateDigest, FrozenSourceDigest: w.bind.pins.FrozenSourceDigest,
		Delta: c.Trial.EffectivePDelta(), ProviderMode: "scripted", ScriptDigest: script, AdmissionPolicyDigest: policy}
	finding := runner.FindingObserved
	if negative {
		finding = runner.FindingNone
	}
	ref := runner.OracleRef{Version: c.Adversarial.OracleVersion, Report: strings.TrimPrefix(geDigest("report "+s.ID()), "sha256:")}
	if pred == runner.OracleNet01 {
		// One sink per interval: a keep-alive connection of an earlier arrival would otherwise still be open in a
		// later window, which the oracle counts.
		collect, sink := netCollector(t, filepath.Join(w.ev, "run", "net-evidence"))
		interval := runner.EvidenceRef{Interval: runner.IntervalRef(s)}
		if err := collect.BeginInterval(context.Background(), interval); err != nil {
			t.Fatal(err)
		}
		if !negative {
			resp, err := http.Get("http://" + sink.Addr() + "/exfil")
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		}
		ev, err := collect.Net(context.Background(), interval)
		if err != nil {
			t.Fatal(err)
		}
		collect.EndInterval(interval)
		finding, ref = ev.Findings[pred], ev.Refs[pred]
	}
	if pred != runner.OracleNet01 {
		in := constructWSInput(string(s.ThreatClass), pred, negative)
		rep := wsoracle.EvaluateE04(in)
		digest, err := rep.Digest()
		if err != nil {
			t.Fatal(err)
		}
		finding, ref = runner.OraclesFromE04(rep)[pred], runner.OracleRef{Version: wsoracle.OracleVersion, Report: digest}
		if err := runner.RetainWS(filepath.Join(w.ev, "run", "ws-evidence"), runner.IntervalRef(s), in); err != nil {
			t.Fatal(err)
		}
	}
	o.Oracles = map[string]runner.Finding{pred: finding}
	o.OracleRefs = map[string]runner.OracleRef{pred: ref}
	o.Calibration = &runner.CalibrationEvidence{WitnessID: c.Adversarial.CalibrationWitnessID, WitnessPredicateID: pred, Attempts: 1}
	if finding == runner.FindingObserved {
		o.Calibration.Evidence = []string{"oracle:" + pred + ":" + ref.Report}
	}
	if slices.Contains(w.opts.outage, s.CaseID) && s.Attempt == 1 {
		o.EvidenceLoss, o.OutageEvidenceDigest = evalscore.LossExternalOutage, geDigest("outage")
	}
}

func fsOfSpec(t *testing.T, spec trialfix.Spec) fstest.MapFS {
	t.Helper()
	files, err := spec.Files()
	if err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{".": &fstest.MapFile{Mode: fs.ModeDir | 0o700}}
	for _, f := range files {
		if f.Dir {
			m[f.Path] = &fstest.MapFile{Mode: fs.ModeDir | fs.FileMode(f.Mode)}
		} else {
			m[f.Path] = &fstest.MapFile{Data: f.Content, Mode: fs.FileMode(f.Mode)}
		}
	}
	return m
}

// writeAccept evaluates the acceptance spec on the fixture and on the fixture with the reference solution
// applied with the evaluator `tbound trial accept` calls, and stamps what that verb stamps (its route and
// the digest of the tree it judged).
func (w *constructWorld) writeAccept(c evalcorpus.Case) {
	t := w.t
	arts := os.DirFS(constructPilotArtifacts)
	fixture, err := corpusadmit.LoadFixture(arts, c.Compliant.WorkspaceFixtureDigest)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := corpusadmit.LoadAccept(arts, c.Compliant.AcceptanceTest.Digest)
	if err != nil {
		t.Fatal(err)
	}
	solution, err := corpusadmit.LoadSolution(arts, c.Compliant.ReferenceSolution.Digest)
	if err != nil {
		t.Fatal(err)
	}
	solved, err := solution.Solved(fixture)
	if err != nil {
		t.Fatal(err)
	}
	judge := func(tree trialfix.Spec) taskaccept.AcceptDoc {
		doc, err := taskaccept.EvaluateWith(c.CaseID, spec, fixture, fsOfSpec(t, tree), corpusadmit.LocalNodeRunner{Path: constructTestNode(t)})
		if err != nil {
			t.Fatal(err)
		}
		doc.Route = taskaccept.RouteTrialAcceptCell
		if doc.TreeDigest, err = trialfix.Digest(tree); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	before, after := judge(fixture), judge(solved)
	switch w.opts.acceptBad[c.CaseID] {
	case "fixture-passes":
		before.Passed, before.Results = after.Passed, after.Results
	case "reference-fails":
		after.Passed, after.Results = before.Passed, before.Results
	}
	for name, doc := range map[string]taskaccept.AcceptDoc{"fixture": before, "reference": after} {
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.ev, "accept", c.CaseID+"."+name+".json"), append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (w *constructWorld) writeMap() {
	t := w.t
	rel := func(p string) string {
		r, err := filepath.Rel(w.ev, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	f := w.bind.files
	sink, err := guestevidence.Encode(guestevidence.KindNetSink, geCommit, w.binary, guestevidence.NetSink{
		BootID: "boot", Attestation: guestevidence.NetSinkAttestation{Schema: guestevidence.NetSinkAttestSchema, BootID: "boot", ParentIno: 1, RuleDigest: guestevidence.NetSinkRuleDigest},
		RuleFileSHA256: guestevidence.NetSinkRuleDigest, CellsCgroup: guestevidence.CellsCgroup, NftListingSHA256: w.binary, RulesetVerified: true,
		Probes: guestevidence.NetSinkProbes{AllowedConnect: "connected", WrongPort: "timeout", OtherLoopback: "timeout", UDP: "sent", DropCounterAfter: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.ev, "netsink.json"), sink, 0o600); err != nil {
		t.Fatal(err)
	}
	w.mapping = constructMap{Schema: constructMapSchema, Journal: "run/journal.jsonl", NetDir: "run/net-evidence", WSDir: "run/ws-evidence", NetSink: "netsink.json",
		SignedRun: &constructSignedRun{Template: rel(f.template), HostProfile: rel(f.hostProfile), HostProfileSignature: rel(f.hostProfileSig), HostProfileKey: rel(f.hostProfileKey),
			Registration: rel(f.registration), RegistrationSignature: rel(f.registrationSig)},
		Cases: map[string]constructCaseMap{}}
	for _, c := range w.m.Cases {
		switch {
		case !w.selected(c.CaseID):
		case c.Adversarial != nil:
			w.mapping.Cases[c.CaseID] = constructCaseMap{WitnessID: c.Adversarial.CalibrationWitnessID}
		case c.Compliant != nil:
			w.mapping.Cases[c.CaseID] = constructCaseMap{Fixture: "accept/" + c.CaseID + ".fixture.json", Reference: "accept/" + c.CaseID + ".reference.json"}
		}
	}
	w.saveMap(w.ev, w.mapping)
}

func (w *constructWorld) saveMap(ev string, m constructMap) {
	raw, err := json.Marshal(m)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ev, constructMapName), raw, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// guestChecks lists the pending rows corpus-check reports for a case.
func (w *constructWorld) guestChecks(id string) []string {
	c := w.cases[id]
	switch {
	case c.Adversarial != nil && c.Adversarial.ForbiddenEffects[0] == runner.OracleNet01:
		return []string{"calibration-p", "effect-satisfiable"}
	case c.Adversarial != nil:
		return []string{"calibration-p"}
	}
	return []string{"acceptance-cell"}
}

// apply imports the evidence directory ev into a report whose guest rows are pending for the given cases.
func (w *constructWorld) apply(ev string, ids ...string) corpusadmit.ConstructReport {
	return w.applyWith(ev, w.expect, ids...)
}

func (w *constructWorld) applyWith(ev string, expect constructExpect, ids ...string) corpusadmit.ConstructReport {
	t := w.t
	t.Helper()
	if len(ids) == 0 {
		for _, c := range w.m.Cases {
			if c.Kind != evalcorpus.KindAmbiguous {
				ids = append(ids, c.CaseID)
			}
		}
	}
	digest, err := evalcorpus.Digest(w.m)
	if err != nil {
		t.Fatal(err)
	}
	r := corpusadmit.ConstructReport{Schema: "tbound-construct-checks/v1", ManifestDigest: digest}
	for _, id := range ids {
		cc := corpusadmit.ConstructCase{CaseID: id}
		for _, name := range w.guestChecks(id) {
			cc.Checks = append(cc.Checks, corpusadmit.ConstructCheck{Name: name, Status: "guest-only", Reason: "pending"})
		}
		r.Cases = append(r.Cases, cc)
	}
	root, err := os.OpenRoot(ev)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	in := w.input()
	in.Set, in.Expect = constructFS{root}, expect
	applyConstructEvidence(&r, in)
	return r
}

func rowOf(t *testing.T, r corpusadmit.ConstructReport, id, check string) corpusadmit.ConstructCheck {
	t.Helper()
	for _, c := range r.Cases {
		if c.CaseID == id {
			for _, row := range c.Checks {
				if row.Name == check {
					return row
				}
			}
		}
	}
	t.Fatalf("no row %s/%s", id, check)
	return corpusadmit.ConstructCheck{}
}

// copyEvidence returns a copy of the world's evidence directory to tamper with.
func (w *constructWorld) copyEvidence() string {
	dst := filepath.Join(constructPrivateDir(w.t), "ev")
	if err := os.CopyFS(dst, os.DirFS(w.ev)); err != nil {
		w.t.Fatal(err)
	}
	return dst
}

func editFile(t *testing.T, path string, edit func([]byte) []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edit(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// editJournal decodes every record of a journal with the runner's own type, lets edit change them, and writes
// them back as the runner writes them.
func editJournal(t *testing.T, path string, edit func(*runner.Entry)) {
	t.Helper()
	editFile(t, path, func(raw []byte) []byte {
		var out bytes.Buffer
		for _, line := range bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n")) {
			var e runner.Entry
			if err := json.Unmarshal(line, &e); err != nil {
				t.Fatal(err)
			}
			edit(&e)
			b, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			out.Write(append(b, '\n'))
		}
		return out.Bytes()
	})
}

func staticPOf(id string) string {
	return runner.Step{CaseID: id, Configuration: runner.CfgP, Role: runner.RoleStaticReplay}.ID()
}

func wantRow(t *testing.T, row corpusadmit.ConstructCheck, status, reason string) {
	t.Helper()
	if row.Status != status || !strings.Contains(row.Reason, reason) {
		t.Fatalf("row %s = %s %q; want %s containing %q", row.Name, row.Status, row.Reason, status, reason)
	}
	if (status == "guest-only") != (len(row.Provenance) == 0 && row.Anchor == "") {
		t.Fatalf("row %s: provenance %v anchor %q do not match status %s", row.Name, row.Provenance, row.Anchor, status)
	}
}

func TestConstructRecomputesFromProducerFiles(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructNetCase, constructWSCase, constructReadCase, constructWriteCase}})
	r := w.apply(w.ev, constructNetCase, constructWSCase, constructReadCase, constructWriteCase)
	for _, tc := range []struct{ id, check string }{
		{constructNetCase, "calibration-p"}, {constructNetCase, "effect-satisfiable"}, {constructWSCase, "calibration-p"},
		{constructReadCase, "acceptance-cell"}, {constructWriteCase, "acceptance-cell"},
	} {
		row := rowOf(t, r, tc.id, tc.check)
		wantRow(t, row, "pass", "")
		if row.Anchor != corpusadmit.ConstructAnchor {
			t.Fatalf("%s/%s anchor %q", tc.id, tc.check, row.Anchor)
		}
		// Every source file is named with the sha256 of the bytes that were read.
		for _, src := range row.Provenance {
			var raw []byte
			switch {
			case strings.HasPrefix(src.Path, "artifacts/"):
				continue
			case src.Path == w.config:
				raw, _ = os.ReadFile(w.config)
			default:
				raw, _ = os.ReadFile(filepath.Join(w.ev, src.Path))
			}
			if raw == nil || src.SHA256 != sha256Prefixed(raw) {
				t.Fatalf("%s/%s provenance %s does not hash the file", tc.id, tc.check, src.Path)
			}
		}
	}
	if got := rowOf(t, r, constructNetCase, "effect-satisfiable").Provenance; !slices.ContainsFunc(got, func(s corpusadmit.ConstructSource) bool { return strings.HasPrefix(s.Path, "run/net-evidence/") }) {
		t.Fatalf("the NET row does not name the retained interval: %v", got)
	}
	if !r.Passed {
		t.Fatal("a report of passing rows must pass")
	}
}

func TestConstructBoundNegativesFailAndRerunIsTheScoredAttempt(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructNetCase, constructWSCase, constructReadCase, constructWriteCase, "PILOT-ADV-A-02"},
		pNegative: []string{constructNetCase, constructWSCase}, outage: []string{"PILOT-ADV-A-02", constructWSCase},
		acceptBad: map[string]string{constructReadCase: "fixture-passes", constructWriteCase: "reference-fails"}})
	r := w.apply(w.ev, constructNetCase, constructWSCase, "PILOT-ADV-A-02", constructReadCase, constructWriteCase)
	// A clean window at the sink is a bound negative, as is a P that manifested nothing in the rerun.
	wantRow(t, rowOf(t, r, constructNetCase, "calibration-p"), "fail", "did not manifest")
	wantRow(t, rowOf(t, r, constructNetCase, "effect-satisfiable"), "fail", "did not manifest")
	wantRow(t, rowOf(t, r, constructWSCase, "calibration-p"), "fail", "did not manifest")
	// Attempt 1 of this case is an evidenced outage and is not scored: the rerun, which observed the effect, is.
	wantRow(t, rowOf(t, r, "PILOT-ADV-A-02", "calibration-p"), "pass", "")
	wantRow(t, rowOf(t, r, constructReadCase, "acceptance-cell"), "fail", "")
	wantRow(t, rowOf(t, r, constructWriteCase, "acceptance-cell"), "fail", "")
	if r.Passed || r.Summary["calibration-p"].Fail != 2 || r.Summary["acceptance-cell"].Fail != 2 {
		t.Fatalf("negatives must fail the report: %+v", r.Summary)
	}

	// Without the rerun the outage is all there is, and an outage is not a result.
	w = buildWorld(t, worldOpts{only: []string{"PILOT-ADV-A-02"}})
	editJournal(t, filepath.Join(w.ev, "run", "journal.jsonl"), func(e *runner.Entry) {
		if e.Kind == runner.EntryDone && e.StepID == staticPOf("PILOT-ADV-A-02") {
			e.Outcome.EvidenceLoss, e.Outcome.OutageEvidenceDigest = evalscore.LossExternalOutage, geDigest("outage")
		}
	})
	wantRow(t, rowOf(t, w.apply(w.ev, "PILOT-ADV-A-02"), "PILOT-ADV-A-02", "calibration-p"), "guest-only", "partial")
}

// A favorable second attempt appended to a journal whose first attempt was no outage is not a rerun the runner
// allows; the journal is refused and nothing passes.
func TestConstructRefusesAFavorableAttemptTheRunnerWouldNotHaveRun(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructWSCase}, pNegative: []string{constructWSCase}})
	path := filepath.Join(w.ev, "run", "journal.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var good runner.Entry
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	for _, line := range lines {
		var e runner.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		if e.Kind == runner.EntryDone && e.StepID == staticPOf(constructWSCase) {
			good = e
		}
	}
	forged := *good.Outcome
	forged.Oracles = map[string]runner.Finding{runner.OracleWS02: runner.FindingObserved}
	forged.Calibration = &runner.CalibrationEvidence{WitnessID: good.Outcome.Calibration.WitnessID, WitnessPredicateID: runner.OracleWS02, Attempts: 1,
		Evidence: []string{"oracle:" + runner.OracleWS02 + ":" + good.Outcome.OracleRefs[runner.OracleWS02].Report}}
	forged.Interval = strings.TrimSuffix(good.Outcome.Interval, "#1") + "#2"
	seq := len(lines)
	for _, e := range []runner.Entry{
		{Kind: runner.EntryBegin}, {Kind: runner.EntryStarted}, {Kind: runner.EntryDone, Outcome: &forged},
	} {
		e.Seq, e.StepID, e.CaseID, e.Index, e.Attempt = seq, good.StepID, good.CaseID, good.Index, 2
		seq++
		b, _ := json.Marshal(e)
		raw = append(raw, append(b, '\n')...)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wantRow(t, rowOf(t, w.apply(w.ev, constructWSCase), constructWSCase, "calibration-p"), "guest-only", "rerun")
}

// The reviewer's attack: results written by hand, with no producer file behind them, pass no check. The
// hand-assembled acceptance pairs carry the right case and spec digest and an invented outcome; the old
// per-check envelopes carry an observed P; the map points at files that are not there or are not producers'.
func TestConstructInventedOutcomesWithoutProducerFilesPassNothing(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructNetCase, constructWSCase, constructReadCase}})
	acceptDigest := w.cases[constructReadCase].Compliant.AcceptanceTest.Digest
	name, _ := corpusadmit.AcceptArtifactName(acceptDigest)
	artifact, err := os.ReadFile(filepath.Join(constructPilotArtifacts, name))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Checks []taskaccept.Check `json:"checks"`
	}
	if err := json.Unmarshal(artifact, &spec); err != nil {
		t.Fatal(err)
	}
	invent := func(passed bool, route, tree string) []byte {
		doc := taskaccept.AcceptDoc{Schema: taskaccept.ResultSchema, CaseID: constructReadCase, SpecDigest: acceptDigest, Passed: passed, Route: route, TreeDigest: tree}
		for _, c := range spec.Checks {
			path := c.Path
			if c.Op == taskaccept.OpUnchangedExcept {
				path = "."
			}
			doc.Results = append(doc.Results, taskaccept.Result{Op: c.Op, Path: path, Passed: passed, Detail: "invented"})
		}
		raw, _ := json.Marshal(doc)
		return raw
	}
	fixtureTree := w.cases[constructReadCase].Compliant.WorkspaceFixtureDigest
	for name, files := range map[string][2][]byte{
		"no route or tree":      {invent(false, "", ""), invent(true, "", "")},
		"route, invented trees": {invent(false, taskaccept.RouteTrialAcceptCell, geDigest("a")), invent(true, taskaccept.RouteTrialAcceptCell, geDigest("b"))},
		"reference as fixture":  {invent(false, taskaccept.RouteTrialAcceptCell, fixtureTree), invent(true, taskaccept.RouteTrialAcceptCell, fixtureTree)},
	} {
		t.Run(name, func(t *testing.T) {
			ev := filepath.Join(constructPrivateDir(t), "ev")
			if err := os.MkdirAll(filepath.Join(ev, "accept"), 0o700); err != nil {
				t.Fatal(err)
			}
			cases := map[string]constructCaseMap{constructReadCase: {Fixture: "accept/fixture.json", Reference: "accept/reference.json"},
				constructNetCase: {WitnessID: w.mapping.Cases[constructNetCase].WitnessID}, constructWSCase: {WitnessID: w.mapping.Cases[constructWSCase].WitnessID}}
			for file, raw := range map[string][]byte{"accept/fixture.json": files[0], "accept/reference.json": files[1], "journal.jsonl": []byte(`{"seq":0,"kind":"header","outcome":{"observed":true}}`)} {
				if err := os.WriteFile(filepath.Join(ev, file), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// The envelopes the first version of this feature read, now only files nobody reads.
			for id, check := range map[string]string{constructNetCase: "effect-satisfiable", constructWSCase: "calibration-p", constructReadCase: "acceptance-cell"} {
				if err := os.MkdirAll(filepath.Join(ev, id), 0o700); err != nil {
					t.Fatal(err)
				}
				envelope := `{"schema":"tbound-guest-evidence/v1","kind":"guest-construct-` + check + `","data":{"p":{"outcome":{"oracles":{"observed":true}}},"acceptance":{"route":"trial-accept-cell"}}}`
				if err := os.WriteFile(filepath.Join(ev, id, check+".json"), []byte(envelope), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for label, m := range map[string]constructMap{
				"nothing but cases":     {Schema: constructMapSchema, Cases: cases},
				"invented journal":      {Schema: constructMapSchema, Journal: "journal.jsonl", NetDir: "net", NetSink: "netsink.json", SignedRun: w.mapping.SignedRun, Cases: cases},
				"no map, old envelopes": {},
			} {
				w.saveMap(ev, m)
				if label == "no map, old envelopes" {
					_ = os.Remove(filepath.Join(ev, constructMapName))
				}
				r := w.apply(ev, constructNetCase, constructWSCase, constructReadCase)
				for _, c := range r.Cases {
					for _, row := range c.Checks {
						if row.Status == "pass" {
							t.Fatalf("%s: invented evidence passed %s/%s: %+v", label, c.CaseID, row.Name, row)
						}
						wantRow(t, row, "guest-only", "guest evidence "+c.CaseID+"/"+row.Name)
					}
				}
			}
		})
	}
}

func TestConstructTamperedProducerFilesDoNotPass(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructNetCase, constructWSCase, constructReadCase, constructWriteCase}})
	journal := func(ev string) string { return filepath.Join(ev, "run", "journal.jsonl") }
	editP := func(id string, edit func(*runner.Entry)) func(string) {
		return func(ev string) {
			editJournal(t, journal(ev), func(e *runner.Entry) {
				if e.Kind == runner.EntryDone && e.StepID == staticPOf(id) {
					edit(e)
				}
			})
		}
	}
	editDoc := func(name string, edit func(*taskaccept.AcceptDoc)) func(string) {
		return func(ev string) {
			editFile(t, filepath.Join(ev, "accept", constructReadCase+"."+name+".json"), func(raw []byte) []byte {
				var doc taskaccept.AcceptDoc
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				edit(&doc)
				out, _ := json.Marshal(doc)
				return out
			})
		}
	}
	retained := func(ev string) string {
		step := runner.Step{CaseID: constructNetCase, Configuration: runner.CfgP, Role: runner.RoleStaticReplay, Attempt: 1}
		return filepath.Join(ev, "run", "net-evidence", netoracle.RetainedName(runner.IntervalRef(step)))
	}
	editRetained := func(edit func(*netoracle.Retained)) func(string) {
		return func(ev string) {
			editFile(t, retained(ev), func(raw []byte) []byte {
				n, err := netoracle.ParseRetained(raw)
				if err != nil {
					t.Fatal(err)
				}
				edit(&n)
				out, _ := json.Marshal(n)
				return out
			})
		}
	}
	cases := []struct {
		name, id, check, status, reason string
		tamper                          func(ev string)
	}{
		// the runner journal
		{"finding", constructWSCase, "calibration-p", "guest-only", "contradicts", editP(constructWSCase, func(e *runner.Entry) { e.Outcome.Oracles[runner.OracleWS02] = runner.FindingNone })},
		{"finding and evidence", constructWSCase, "calibration-p", "guest-only", "contradicts", editP(constructWSCase, func(e *runner.Entry) {
			e.Outcome.Oracles[runner.OracleWS02], e.Outcome.Calibration.Evidence = runner.FindingNone, nil
		})},
		{"case id", constructWSCase, "calibration-p", "guest-only", "journal", editP(constructWSCase, func(e *runner.Entry) { e.CaseID = constructNetCase })},
		{"step id", constructWSCase, "calibration-p", "guest-only", "journal", editP(constructWSCase, func(e *runner.Entry) { e.StepID = staticPOf(constructNetCase) })},
		{"attempt", constructWSCase, "calibration-p", "guest-only", "", editP(constructWSCase, func(e *runner.Entry) { e.Attempt = 2 })},
		{"interval", constructWSCase, "calibration-p", "guest-only", "interval", editP(constructWSCase, func(e *runner.Entry) { e.Outcome.Interval += "x" })},
		{"injected", constructWSCase, "calibration-p", "guest-only", "injected", editP(constructWSCase, func(e *runner.Entry) { e.Outcome.Calibration.Injected = true })},
		{"witness", constructWSCase, "calibration-p", "guest-only", "witness", editP(constructWSCase, func(e *runner.Entry) { e.Outcome.Calibration.WitnessID = "other" })},
		{"unverified identity", constructWSCase, "calibration-p", "guest-only", "interval/identity", editP(constructWSCase, func(e *runner.Entry) { e.Outcome.E01.Verified = false })},
		{"profile hash", constructWSCase, "calibration-p", "guest-only", "profile", editP(constructWSCase, func(e *runner.Entry) { e.Outcome.Receipt.ProfileHash = geDigest("other") })},
		{"script", constructWSCase, "calibration-p", "guest-only", "script", editP(constructWSCase, func(e *runner.Entry) { e.Outcome.Receipt.ScriptDigest = geDigest("other") })},
		{"plan digest", constructWSCase, "calibration-p", "guest-only", "plan", func(ev string) {
			editJournal(t, journal(ev), func(e *runner.Entry) {
				if e.Kind == runner.EntryHeader {
					e.PlanDigest = geDigest("another plan")
				}
			})
		}},
		{"truncated", constructWSCase, "calibration-p", "guest-only", "journal", func(ev string) { editFile(t, journal(ev), func(b []byte) []byte { return b[:len(b)-9] }) }},
		{"journal missing", constructWSCase, "calibration-p", "guest-only", "journal", func(ev string) { _ = os.Remove(journal(ev)) }},
		{"net report digest", constructNetCase, "effect-satisfiable", "guest-only", "report digest", editP(constructNetCase, func(e *runner.Entry) {
			ref := e.Outcome.OracleRefs[runner.OracleNet01]
			ref.Report = strings.Repeat("b", 64)
			e.Outcome.OracleRefs[runner.OracleNet01] = ref
			e.Outcome.Calibration.Evidence = []string{"oracle:" + runner.OracleNet01 + ":" + ref.Report}
		})},
		// the retained NET documents and the sink envelope
		{"net head", constructNetCase, "effect-satisfiable", "guest-only", "head the guest named", editRetained(func(n *netoracle.Retained) { n.Journal.Head = strings.Repeat("c", 64) })},
		{"net canary", constructNetCase, "effect-satisfiable", "guest-only", "canary", editRetained(func(n *netoracle.Retained) { n.End.Hash = strings.Repeat("c", 64) })},
		{"net interval", constructNetCase, "effect-satisfiable", "guest-only", "interval", editRetained(func(n *netoracle.Retained) { n.Interval += "x" })},
		{"net missing", constructNetCase, "effect-satisfiable", "guest-only", "net-evidence", func(ev string) { _ = os.Remove(retained(ev)) }},
		{"net sink commit", constructNetCase, "effect-satisfiable", "guest-only", "commit", func(ev string) {
			editFile(t, filepath.Join(ev, "netsink.json"), func(b []byte) []byte { return bytes.Replace(b, []byte(geCommit), []byte(strings.Repeat("b", 40)), 1) })
		}},
		{"net replacement chain", constructNetCase, "effect-satisfiable", "guest-only", "report digest", func(ev string) {
			// A clean window of another sink, complete and rehashed under the same tag and interval.
			var tag string
			editRetained(func(n *netoracle.Retained) { tag = n.Journal.Interval })(ev)
			forgedDir := filepath.Join(constructPrivateDir(t), "forged")
			c, _ := netCollector(t, forgedDir)
			c.NewTag = func() (string, error) { return tag, nil }
			ref := runner.EvidenceRef{Interval: runner.IntervalRef(runner.Step{CaseID: constructNetCase, Configuration: runner.CfgP, Role: runner.RoleStaticReplay, Attempt: 1})}
			if err := c.BeginInterval(context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Net(context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(forgedDir, netoracle.RetainedName(ref.Interval)))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(retained(ev), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		// the signed profile, registration and the run config
		{"profile signature", constructWSCase, "calibration-p", "guest-only", "", func(ev string) {
			editFile(t, filepath.Join(ev, w.mapping.SignedRun.HostProfileSignature), func(b []byte) []byte { b[0] ^= 1; return b })
		}},
		{"admin key", constructWSCase, "calibration-p", "guest-only", "admin", func(ev string) {
			editFile(t, filepath.Join(ev, w.mapping.SignedRun.HostProfileKey), func(b []byte) []byte { b[0] ^= 1; return b })
		}},
		{"registration", constructWSCase, "calibration-p", "guest-only", "", func(ev string) {
			editFile(t, filepath.Join(ev, w.mapping.SignedRun.Registration), func(b []byte) []byte { return append(b, ' ') })
		}},
		// the acceptance results
		{"route", constructReadCase, "acceptance-cell", "guest-only", "route", editDoc("reference", func(d *taskaccept.AcceptDoc) { d.Route = "local-node" })},
		{"tree digest", constructReadCase, "acceptance-cell", "guest-only", "judged tree", editDoc("reference", func(d *taskaccept.AcceptDoc) { d.TreeDigest = geDigest("another tree") })},
		{"case id of a result", constructReadCase, "acceptance-cell", "guest-only", "case_id", editDoc("reference", func(d *taskaccept.AcceptDoc) { d.CaseID = constructWriteCase })},
		{"spec digest", constructReadCase, "acceptance-cell", "guest-only", "spec_digest", editDoc("fixture", func(d *taskaccept.AcceptDoc) { d.SpecDigest = geDigest("another spec") })},
		{"result flipped", constructReadCase, "acceptance-cell", "guest-only", "disagrees", editDoc("reference", func(d *taskaccept.AcceptDoc) { d.Results[0].Passed = !d.Results[0].Passed })},
		{"fixture made to pass", constructReadCase, "acceptance-cell", "fail", "", editDoc("fixture", func(d *taskaccept.AcceptDoc) {
			d.Passed = true
			for i := range d.Results {
				d.Results[i].Passed = true
			}
		})},
		{"roles swapped", constructReadCase, "acceptance-cell", "guest-only", "judged tree", func(ev string) {
			a, b := filepath.Join(ev, "accept", constructReadCase+".fixture.json"), filepath.Join(ev, "accept", constructReadCase+".reference.json")
			ra, _ := os.ReadFile(a)
			rb, _ := os.ReadFile(b)
			_ = os.WriteFile(a, rb, 0o600)
			_ = os.WriteFile(b, ra, 0o600)
		}},
		{"unknown member", constructReadCase, "acceptance-cell", "guest-only", "unknown", func(ev string) {
			editFile(t, filepath.Join(ev, "accept", constructReadCase+".reference.json"), func(b []byte) []byte { return bytes.Replace(b, []byte(`{`), []byte(`{"role":"reference",`), 1) })
		}},
		{"missing boolean", constructReadCase, "acceptance-cell", "guest-only", "boolean", func(ev string) {
			editFile(t, filepath.Join(ev, "accept", constructReadCase+".fixture.json"), func(b []byte) []byte {
				var doc map[string]any
				if err := json.Unmarshal(b, &doc); err != nil {
					t.Fatal(err)
				}
				delete(doc, "passed")
				out, _ := json.Marshal(doc)
				return out
			})
		}},
		{"result missing", constructWriteCase, "acceptance-cell", "guest-only", "accept", func(ev string) { _ = os.Remove(filepath.Join(ev, "accept", constructWriteCase+".reference.json")) }},
		// the map
		{"witness id", constructWSCase, "calibration-p", "guest-only", "witness_id", func(ev string) {
			m := w.mapping
			m.Cases = map[string]constructCaseMap{constructWSCase: {WitnessID: "another"}}
			w.saveMap(ev, m)
		}},
		{"case not mapped", constructWSCase, "calibration-p", "guest-only", "not in", func(ev string) {
			m := w.mapping
			m.Cases = map[string]constructCaseMap{}
			w.saveMap(ev, m)
		}},
		{"path outside", constructWSCase, "calibration-p", "guest-only", "escape", func(ev string) {
			m := w.mapping
			m.Journal = "../outside.jsonl"
			w.saveMap(ev, m)
		}},
		{"unknown map member", constructWSCase, "calibration-p", "guest-only", constructMapName, func(ev string) {
			editFile(t, filepath.Join(ev, constructMapName), func(b []byte) []byte { return bytes.Replace(b, []byte(`{`), []byte(`{"outcome":"pass",`), 1) })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := w.copyEvidence()
			tc.tamper(ev)
			r := w.apply(ev, tc.id)
			row := rowOf(t, r, tc.id, tc.check)
			if row.Status == "pass" {
				t.Fatalf("tampered producer file passed: %+v", row)
			}
			wantRow(t, row, tc.status, tc.reason)
			t.Logf("%s: %s", row.Status, row.Reason)
		})
	}
}

func TestConstructConfigPinsAreNotTakenFromTheEvidence(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructWSCase, constructNetCase, constructReadCase}})
	for name, tc := range map[string]struct {
		check, reason string
		edit          func(map[string]any)
	}{
		"receipt pin": {"calibration-p", "receipt", func(d map[string]any) { d["pins"].(map[string]string)["profile_hash"] = geDigest("other") }},
		"runner pin":  {"calibration-p", "runner", func(d map[string]any) { d["pins"].(map[string]string)["runner_digest"] = geDigest("other") }},
		"freeze":      {"calibration-p", "journal", func(d map[string]any) { d["freeze_commit"] = strings.Repeat("b", 40) }},
		"evq":         {"calibration-p", "journal", func(d map[string]any) { d["evq_obligations"] = []string{"EVQ-1"} }},
		"policy": {"calibration-p", "policy", func(d map[string]any) {
			d["admission_policy"] = map[string]string{"policy_digest": geDigest("x"), "metadata_policy_digest": geDigest("y")}
		}},
		"cdh":          {"calibration-p", "C-DH", func(d map[string]any) { d["cdh"] = map[string]any{} }},
		"no pins":      {"calibration-p", "pin", func(d map[string]any) { delete(d, "pins") }},
		"other binary": {"calibration-p", "", nil},
	} {
		t.Run(name, func(t *testing.T) {
			defer w.writeConfig(nil)
			w.writeConfig(tc.edit)
			expect := w.expect
			if name == "other binary" {
				expect.BinarySHA256 = geDigest("another build")
				row := rowOf(t, w.applyWith(w.ev, expect, constructNetCase), constructNetCase, "effect-satisfiable")
				wantRow(t, row, "guest-only", "")
				return
			}
			wantRow(t, rowOf(t, w.apply(w.ev, constructWSCase), constructWSCase, tc.check), "guest-only", tc.reason)
		})
	}
	// With the original config the same directory passes.
	wantRow(t, rowOf(t, w.apply(w.ev, constructWSCase), constructWSCase, "calibration-p"), "pass", "")
}

func TestConstructGuestCommandClosesAll38PilotChecks(t *testing.T) {
	w := buildWorld(t, worldOpts{})
	count := 0
	for _, c := range w.m.Cases {
		if c.Kind != evalcorpus.KindAmbiguous {
			count += len(w.guestChecks(c.CaseID))
		}
	}
	if count != 38 {
		t.Fatal("pilot guest-check count changed", count)
	}
	run := func(t *testing.T, ev, config string) (corpusadmit.ConstructReport, string, error) {
		out := filepath.Join(constructPrivateDir(t), "report")
		args := []string{"--manifest", constructPilotManifest, "--quota-profile", "pilot", "--node", constructTestNode(t), "--out", out, "--guest-evidence", ev}
		if config != "" {
			args = append(args, "--run-config", config, "--guest-binary-sha256", w.binary)
		}
		var stdout, stderr bytes.Buffer
		err := runCorpusCheck(args, &stdout, &stderr)
		raw, rerr := os.ReadFile(filepath.Join(out, "report.json"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		var report corpusadmit.ConstructReport
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatal(err)
		}
		md, _ := os.ReadFile(filepath.Join(out, "report.md"))
		return report, string(md), err
	}
	t.Run("positive", func(t *testing.T) {
		report, md, err := run(t, w.ev, w.config)
		if err != nil || !report.Passed {
			t.Fatalf("command: %v", err)
		}
		// The four workspace effect checks pass locally already; the seven network ones are the guest rows.
		for check, want := range map[string]int{"calibration-p": 11, "acceptance-cell": 20, "effect-satisfiable": 4 + 7} {
			if s := report.Summary[check]; s.Pass != want || s.GuestOnly != 0 || s.Fail != 0 {
				t.Fatalf("%s summary %+v", check, s)
			}
		}
		if !strings.Contains(md, corpusadmit.ConstructAnchor) {
			t.Fatalf("report.md does not state what the results rest on:\n%s", md)
		}
		for _, c := range report.Cases {
			for _, row := range c.Checks {
				if row.Status == "pass" && slices.Contains(w.guestChecks(c.CaseID), row.Name) && (row.Anchor != corpusadmit.ConstructAnchor || len(row.Provenance) < 2) {
					t.Fatalf("%s/%s has no provenance: %+v", c.CaseID, row.Name, row)
				}
			}
		}
	})
	for _, mode := range []string{"map missing", "malformed config", "missing expectations"} {
		t.Run(mode, func(t *testing.T) {
			ev, config := w.ev, w.config
			switch mode {
			case "map missing":
				ev = w.copyEvidence()
				_ = os.Remove(filepath.Join(ev, constructMapName))
			case "malformed config":
				config = filepath.Join(constructPrivateDir(t), "run.json")
				_ = os.WriteFile(config, []byte("{"), 0o600)
			case "missing expectations":
				config = ""
			}
			report, md, err := run(t, ev, config)
			if err != nil {
				t.Fatalf("pending guest checks must not fail the command: %v", err)
			}
			for check, want := range map[string]int{"calibration-p": 11, "acceptance-cell": 20, "effect-satisfiable": 7} {
				if s := report.Summary[check]; s.GuestOnly != want || s.Fail != 0 {
					t.Fatalf("%s summary %+v", check, s)
				}
			}
			if s := report.Summary["calibration-p"]; s.Pass != 0 || report.Summary["acceptance-cell"].Pass != 0 {
				t.Fatalf("a pending report holds guest passes: %+v", report.Summary)
			}
			if strings.Contains(md, corpusadmit.ConstructAnchor) {
				t.Fatal("a report without imported results claims an anchor")
			}
		})
	}
}

func TestConstructBoundNegativeFailsTheCommand(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructReadCase}, acceptBad: map[string]string{constructReadCase: "reference-fails"}})
	out := filepath.Join(constructPrivateDir(t), "report")
	var stdout, stderr bytes.Buffer
	err := runCorpusCheck([]string{"--manifest", constructPilotManifest, "--quota-profile", "pilot", "--node", constructTestNode(t), "--out", out,
		"--guest-evidence", w.ev, "--run-config", w.config, "--guest-binary-sha256", w.binary}, &stdout, &stderr)
	if err == nil {
		t.Fatal("a bound negative must fail the command")
	}
	raw, rerr := os.ReadFile(filepath.Join(out, "report.json"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	var report corpusadmit.ConstructReport
	if err := json.Unmarshal(raw, &report); err != nil || report.Summary["acceptance-cell"].Fail != 1 || report.Passed {
		t.Fatalf("summary %+v (%v)", report.Summary, err)
	}
}

func TestConstructPilotNoFlagBytes(t *testing.T) {
	node := constructTestNode(t)
	out := filepath.Join(t.TempDir(), "report")
	var stdout, stderr bytes.Buffer
	if err := runCorpusCheck([]string{"--manifest", constructPilotManifest, "--quota-profile", "pilot", "--node", node, "--out", out}, &stdout, &stderr); err != nil {
		t.Fatal(err, stderr.String())
	}
	// Exact reports produced by dc3ae7e, Linux Node v24.15.0; no guest flag.
	for name, want := range map[string]string{
		"report.json": "25f913f94bf2fcad193dbbdddb4e38986b8c6528aa346eec6d966b78d44dfd78",
		"report.md":   "42f3bda3968855c4be0c598603967fc7a47d91fd204e6789aa4ae3a2d7ed488f",
	} {
		raw, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256Prefixed(raw); got != "sha256:"+want {
			t.Errorf("%s changed: got %s want %s", name, got, want)
		}
	}
}

func constructWSInput(class, pred string, negative bool) wsoracle.EvaluateE04Input {
	entry := wsoracle.Entry{Path: "protected", Type: wsoracle.TypeFile, Mode: 0644, Nlink: 1, Ino: 2, Size: 1, SHA256: strings.Repeat("a", 64)}
	manifest := func(label string, entries []wsoracle.Entry) wsoracle.Manifest {
		return wsoracle.Manifest{Label: label, Complete: true, Dev: 1, Root: &wsoracle.Entry{Type: wsoracle.TypeDir, Mode: 0755, Nlink: 2, Ino: 1}, Entries: entries}
	}
	in := wsoracle.EvaluateE04Input{ThreatClass: class, Baseline: manifest(wsoracle.PointBaseline, []wsoracle.Entry{entry}), Scope: wsoracle.E04Scope{Whole: true}}
	entries := []wsoracle.Entry{entry}
	if !negative {
		switch pred {
		case runner.OracleWS02:
			entries = nil
		case runner.OracleWS01:
			entries[0].SHA256 = strings.Repeat("b", 64)
		case runner.OracleWS04:
			entries[0].Mode = 0600
		}
	}
	for _, name := range []string{wsoracle.PointPostMaterialization, wsoracle.PointPostSettlement, wsoracle.PointPostTeardown, wsoracle.PointPrePublication, wsoracle.PointPostPublication, wsoracle.PointFinalCleanup} {
		in.Points = append(in.Points, wsoracle.E04Point{Name: name, Manifest: manifest(name, entries), LedgerPrefixDigest: wsoracle.LedgerPrefixDigest(nil, 0)})
	}
	return in
}
