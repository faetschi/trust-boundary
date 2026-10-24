//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/taskaccept"
)

func constructPilot(t *testing.T, id string) (evalcorpus.Manifest, evalcorpus.Case) {
	t.Helper()
	m, err := loadConstructManifest("../../../corpus/pilot/manifest.draft.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Cases {
		if c.CaseID == id {
			return m, c
		}
	}
	t.Fatal("missing pilot case", id)
	return m, evalcorpus.Case{}
}

func constructFixture(t *testing.T, check string, negative bool) (evalcorpus.Manifest, constructEnvelope, constructExpect) {
	t.Helper()
	id := "PILOT-ADV-A-03"
	if check == "effect-satisfiable" {
		id = "PILOT-ADV-A-01"
	} else if check == "acceptance-cell" {
		id = "PILOT-CMP-read-01"
	}
	return constructFixtureForCase(t, check, negative, id)
}

func constructFixtureForCase(t *testing.T, check string, negative bool, id string) (evalcorpus.Manifest, constructEnvelope, constructExpect) {
	t.Helper()
	m, c := constructPilot(t, id)
	d := "sha256:" + strings.Repeat("a", 64)
	e := constructExpect{RunBinding: guestevidence.RunBinding{Commit: strings.Repeat("a", 40), FrozenSourceDigest: d,
		BaseTemplateDigest: d, ImageDigest: d, AdminPubkeyDigest: d, RuntimeBundleDigest: d, RunnerDigest: d}, BinarySHA256: d, PolicyDigest: d, MetadataPolicyDigest: d}
	manifest, err := evalcorpus.Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := corpusadmit.FixtureDigest(c)
	w := constructWitness{CaseID: id, ManifestDigest: manifest, FixtureDigest: fixture,
		Binding: guestevidence.ProfileBinding{ProfileDigest: d, TemplateDigest: "sha256:" + strings.Repeat("b", 64), BaseTemplateDigest: d,
			RegistrationDigest: d, SourceDigest: d, ImageDigest: d, AdminPubkeyDigest: d, RuntimeBundleDigest: d, RunnerDigest: d}}
	if c.Adversarial != nil {
		pred := c.Adversarial.ForbiddenEffects[0]
		s := runner.Step{CaseID: id, Role: runner.RoleStaticReplay, Configuration: runner.CfgP, Attempt: 1}
		script, err := evalcorpus.ScriptDigest(c.Trial.Script)
		if err != nil {
			t.Fatal(err)
		}
		o := &runner.Outcome{E01: runner.E01Identity{Digest: d, Verified: true}, Interval: runner.IntervalRef(s), Channel: pred,
			EvidenceLoss: "none",
			Calibration:  &runner.CalibrationEvidence{WitnessID: c.Adversarial.CalibrationWitnessID, WitnessPredicateID: pred, Attempts: 1},
			Receipt: &runner.ReceiptFacts{ProfileHash: w.Binding.ProfileDigest, ContainmentProfileDigest: w.Binding.TemplateDigest,
				FrozenSourceDigest: d, ProviderMode: "scripted", ScriptDigest: script, Delta: c.Trial.EffectivePDelta()},
			Oracles: map[string]runner.Finding{pred: runner.FindingObserved}, OracleRefs: map[string]runner.OracleRef{pred: {Version: c.Adversarial.OracleVersion, Report: strings.Repeat("a", 64)}}}
		o.Receipt.AdmissionPolicyDigest, err = evalcorpus.AdmissionPolicyDigest(e.PolicyDigest, e.MetadataPolicyDigest, c.Trial.EffectivePDelta() == evalcorpus.PDeltaAdmitAll, c.Trial.ProtectedPaths)
		if err != nil {
			t.Fatal(err)
		}
		if negative {
			o.Oracles[pred] = runner.FindingNone
		}
		w.P = &runner.Entry{Seq: 4, Kind: runner.EntryDone, StepID: s.ID(), CaseID: id, Attempt: 1, Outcome: o}
		if pred == runner.OracleNet01 {
			w.Net = constructNetFixture(t, o, e, !negative)
		}
		if !negative {
			o.Calibration.Evidence = []string{"oracle:" + pred + ":" + o.OracleRefs[pred].Report}
		}
	} else {
		name, _ := corpusadmit.AcceptArtifactName(c.Compliant.AcceptanceTest.Digest)
		raw, err := os.ReadFile(filepath.Join("../../../corpus/pilot/artifacts", name))
		if err != nil {
			t.Fatal(err)
		}
		var spec struct {
			Checks []taskaccept.Check `json:"checks"`
		}
		if err := json.Unmarshal(raw, &spec); err != nil {
			t.Fatal(err)
		}
		doc := taskaccept.AcceptDoc{Schema: taskaccept.ResultSchema, CaseID: id, SpecDigest: c.Compliant.AcceptanceTest.Digest, Passed: true}
		for _, check := range spec.Checks {
			path := check.Path
			if check.Op == taskaccept.OpUnchangedExcept {
				path = "."
			}
			doc.Results = append(doc.Results, taskaccept.Result{Op: check.Op, Path: path, Passed: true, Detail: "retained producer result"})
		}
		w.Acceptance = &constructAcceptance{SolutionDigest: c.Compliant.ReferenceSolution.Digest, Route: "trial-accept-cell", Reference: doc, Fixture: doc}
		w.Acceptance.Fixture.Results = append([]taskaccept.Result(nil), doc.Results...)
		w.Acceptance.Fixture.Passed, w.Acceptance.Fixture.Results[0].Passed = false, false
		if negative {
			w.Acceptance.Reference.Passed, w.Acceptance.Reference.Results[0].Passed = false, false
		}
	}
	return m, constructEnvelope{Schema: guestevidence.Schema, Kind: "guest-construct-" + check, Commit: e.Commit, BinarySHA256: e.BinarySHA256, Data: w}, e
}

func constructNetFixture(t *testing.T, o *runner.Outcome, e constructExpect, observed bool) *constructNet {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".tbound-construct-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "journal.jsonl")
	j, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	tag := strings.Repeat("1", 32)
	second := int64(0)
	appendEvent := func(kind string, data any) audit.Record {
		t.Helper()
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		record, err := j.Append(audit.Event{Kind: kind, ID: "net", Data: raw})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	now := func() time.Time { second++; return time.Unix(second, 0).UTC() }
	canary := func(phase string) runner.NetCanaryDoc {
		appendEvent(netoracle.KindConnAccepted, netoracle.ConnAccepted{Conn: phase, Remote: "127.0.0.1:1234", Time: now()})
		record := appendEvent(netoracle.KindRequestStarted, netoracle.RequestStarted{Conn: phase, Req: 1, Method: "GET", Path: "/canary", Time: now(), Canary: &netoracle.CanaryRef{Phase: phase, Tag: tag}})
		return runner.NetCanaryDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetCanary, Phase: phase, Interval: tag, Seq: record.Sequence, Hash: record.Hash}
	}
	start := canary("start")
	appendEvent(netoracle.KindConnClosed, netoracle.ConnClosed{Conn: "start", Time: now()})
	if observed {
		appendEvent(netoracle.KindConnAccepted, netoracle.ConnAccepted{Conn: "attempt", Remote: "127.0.0.1:1235", Time: now()})
		appendEvent(netoracle.KindConnClosed, netoracle.ConnClosed{Conn: "attempt", Time: now()})
	}
	end := canary("end")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report := netoracle.EvaluateNET01(netoracle.NET01Input{Journal: raw, Interval: o.Interval, Tag: tag})
	if len(report.Gaps) != 0 {
		t.Fatal(report.Gaps)
	}
	digest, err := report.Digest()
	if err != nil {
		t.Fatal(err)
	}
	o.OracleRefs[runner.OracleNet01] = runner.OracleRef{Version: netoracle.OracleVersion, Report: digest}
	sink, err := guestevidence.Encode(guestevidence.KindNetSink, e.Commit, e.BinarySHA256, guestevidence.NetSink{
		BootID: "boot", Attestation: guestevidence.NetSinkAttestation{Schema: guestevidence.NetSinkAttestSchema, BootID: "boot", ParentIno: 1, RuleDigest: guestevidence.NetSinkRuleDigest},
		RuleFileSHA256: guestevidence.NetSinkRuleDigest, CellsCgroup: guestevidence.CellsCgroup, NftListingSHA256: e.BinarySHA256, RulesetVerified: true,
		Probes: guestevidence.NetSinkProbes{AllowedConnect: "connected", WrongPort: "timeout", OtherLoopback: "timeout", UDP: "sent", DropCounterAfter: 3}})
	if err != nil {
		t.Fatal(err)
	}
	return &constructNet{Sink: sink, Start: start, End: end, Journal: runner.NetJournalDoc{Schema: runner.EvidenceSchema, Kind: runner.EvidenceNetJournal, Interval: tag,
		JournalB64: base64.StdEncoding.EncodeToString(raw), Head: end.Hash, Count: int(end.Seq), StartSeq: start.Seq, EndSeq: end.Seq}}
}

func resolveConstructFixture(t *testing.T, m evalcorpus.Manifest, env constructEnvelope, e constructExpect, check string, editRaw func([]byte) []byte) corpusadmit.ConstructCheck {
	t.Helper()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if editRaw != nil {
		raw = editRaw(raw)
	}
	r := corpusadmit.ConstructReport{ManifestDigest: env.Data.ManifestDigest, Cases: []corpusadmit.ConstructCase{{CaseID: env.Data.CaseID,
		Checks: []corpusadmit.ConstructCheck{{Name: check, Status: "guest-only"}, {Name: "local", Status: "fail", Reason: "local failure retained"}}}}}
	// The report's identities come from the manifest, never from the witness under test.
	r.ManifestDigest, _ = evalcorpus.Digest(m)
	for _, c := range m.Cases {
		if (check == "acceptance-cell" && c.CaseID == "PILOT-CMP-read-01") || (check == "calibration-p" && c.CaseID == "PILOT-ADV-A-03") ||
			(check == "effect-satisfiable" && c.CaseID == "PILOT-ADV-A-01") {
			r.Cases[0].CaseID = c.CaseID
		}
	}
	name := r.Cases[0].CaseID + "/" + check + ".json"
	applyConstructEvidence(&r, m, fstest.MapFS{name: &fstest.MapFile{Data: raw}}, os.DirFS("../../../corpus/pilot/artifacts"), e, nil)
	if r.Passed || r.Summary["local"].Fail != 1 || r.Cases[0].Checks[1].Reason != "local failure retained" {
		t.Fatal("guest evidence changed a local result", r)
	}
	result := r.Cases[0].Checks[0]
	counts := r.Summary[check]
	if (result.Status == "pass" && counts.Pass != 1) || (result.Status == "fail" && counts.Fail != 1) || (result.Status == "guest-only" && counts.GuestOnly != 1) {
		t.Fatal("stale summary", r)
	}
	return result
}

func TestConstructGuestEvidenceBindings(t *testing.T) {
	for _, check := range []string{"calibration-p", "acceptance-cell", "effect-satisfiable"} {
		t.Run(check, func(t *testing.T) {
			for _, tc := range []struct {
				name, status, reason string
				edit                 func(*constructEnvelope, *constructExpect)
			}{
				{"bound positive", "pass", "", nil},
				{"manifest", "guest-only", "manifest_digest", func(w *constructEnvelope, _ *constructExpect) { w.Data.ManifestDigest = "stale" }},
				{"case", "guest-only", "case_id", func(w *constructEnvelope, _ *constructExpect) { w.Data.CaseID = "another-case" }},
				{"freeze", "guest-only", "commit", func(w *constructEnvelope, _ *constructExpect) { w.Commit = strings.Repeat("b", 40) }},
				{"binary", "guest-only", "binary_sha256", func(w *constructEnvelope, _ *constructExpect) { w.BinarySHA256 = "sha256:" + strings.Repeat("b", 64) }},
				{"fixture", "guest-only", "fixture_digest", func(w *constructEnvelope, _ *constructExpect) { w.Data.FixtureDigest = "wrong" }},
				{"source", "guest-only", "frozen_source_digest", func(w *constructEnvelope, _ *constructExpect) { w.Data.Binding.SourceDigest = "wrong" }},
				{"bundle", "guest-only", "runtime_bundle_digest", func(w *constructEnvelope, _ *constructExpect) { w.Data.Binding.RuntimeBundleDigest = "wrong" }},
				{"runner", "guest-only", "runner_digest", func(w *constructEnvelope, _ *constructExpect) { w.Data.Binding.RunnerDigest = "wrong" }},
				{"template", "guest-only", "base_template_digest", func(w *constructEnvelope, _ *constructExpect) { w.Data.Binding.BaseTemplateDigest = "wrong" }},
				{"image", "guest-only", "image_digest", func(w *constructEnvelope, _ *constructExpect) { w.Data.Binding.ImageDigest = "wrong" }},
				{"admin", "guest-only", "admin_pubkey_digest", func(w *constructEnvelope, _ *constructExpect) { w.Data.Binding.AdminPubkeyDigest = "wrong" }},
				{"registration", "guest-only", "registration", func(w *constructEnvelope, _ *constructExpect) { w.Data.Binding.RegistrationDigest = "" }},
				{"missing expectations", "guest-only", "commit", func(_ *constructEnvelope, e *constructExpect) { *e = constructExpect{} }},
				{"wrong kind", "guest-only", "kind", func(w *constructEnvelope, _ *constructExpect) { w.Kind = guestevidence.KindCapability }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					m, w, e := constructFixture(t, check, false)
					if tc.edit != nil {
						tc.edit(&w, &e)
					}
					got := resolveConstructFixture(t, m, w, e, check, nil)
					if got.Status != tc.status || !strings.Contains(got.Reason, tc.reason) {
						t.Fatalf("got %+v; want %s containing %s", got, tc.status, tc.reason)
					}
				})
			}
			for _, malformed := range []string{"{", "null", "trailing", "duplicate", "unknown", "missing bool"} {
				t.Run(malformed, func(t *testing.T) {
					m, w, e := constructFixture(t, check, false)
					got := resolveConstructFixture(t, m, w, e, check, func(raw []byte) []byte {
						switch malformed {
						case "trailing":
							return append(raw, []byte(" {}")...)
						case "duplicate":
							return bytes.Replace(raw, []byte(`"case_id":`), []byte(`"case_id":"duplicate","case_id":`), 1)
						case "unknown":
							return append([]byte(`{"unknown":true,`), raw[1:]...)
						case "missing bool":
							if check == "acceptance-cell" {
								return bytes.Replace(raw, []byte(`"passed":false`), []byte(`"passed":null`), 1)
							}
							return bytes.Replace(raw, []byte(`"injected":false`), []byte(`"injected":null`), 1)
						}
						return []byte(malformed)
					})
					if got.Status != "guest-only" || got.Reason == "" {
						t.Fatal(got)
					}
				})
			}
			t.Run("bound negative", func(t *testing.T) {
				m, w, e := constructFixture(t, check, true)
				if got := resolveConstructFixture(t, m, w, e, check, nil); got.Status != "fail" {
					t.Fatal(got)
				}
			})
		})
	}
}

func TestConstructGuestEvidenceOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, check, reason string
		edit                func(*constructWitness)
	}{
		{"witness", "calibration-p", "witness_id", func(w *constructWitness) { w.P.Outcome.Calibration.WitnessID = "wrong" }},
		{"predicate", "calibration-p", "predicate", func(w *constructWitness) { w.P.Outcome.Calibration.WitnessPredicateID = "wrong" }},
		{"injection", "calibration-p", "injected", func(w *constructWitness) { w.P.Outcome.Calibration.Injected = true }},
		{"partial", "calibration-p", "partial", func(w *constructWitness) { w.P.Outcome.Partial = true }},
		{"step", "calibration-p", "step", func(w *constructWitness) { w.P.StepID = "wrong" }},
		{"receipt", "calibration-p", "receipt", func(w *constructWitness) { w.P.Outcome.Receipt.ProfileHash = "wrong" }},
		{"script", "calibration-p", "script", func(w *constructWitness) { w.P.Outcome.Receipt.ScriptDigest = "wrong" }},
		{"admission policy", "calibration-p", "policy", func(w *constructWitness) { w.P.Outcome.Receipt.AdmissionPolicyDigest = "wrong" }},
		{"evidence loss", "calibration-p", "partial", func(w *constructWitness) { w.P.Outcome.EvidenceLoss = "unclassified" }},
		{"lost", "calibration-p", "lost", func(w *constructWitness) { w.P.Outcome.Oracles[w.P.Outcome.Channel] = runner.FindingLost }},
		{"reference route", "acceptance-cell", "route", func(w *constructWitness) { w.Acceptance.Route = "local-node" }},
		{"solution", "acceptance-cell", "solution_digest", func(w *constructWitness) { w.Acceptance.SolutionDigest = "wrong" }},
		{"accept digest", "acceptance-cell", "spec_digest", func(w *constructWitness) { w.Acceptance.Reference.SpecDigest = "wrong" }},
		{"accept case", "acceptance-cell", "case_id", func(w *constructWitness) { w.Acceptance.Fixture.CaseID = "wrong" }},
		{"accept inconsistent", "acceptance-cell", "disagrees", func(w *constructWitness) { w.Acceptance.Reference.Passed = false }},
		{"accept missing checks", "acceptance-cell", "count", func(w *constructWitness) { w.Acceptance.Reference.Results = w.Acceptance.Reference.Results[:1] }},
		{"accept wrong check", "acceptance-cell", "operation/path", func(w *constructWitness) { w.Acceptance.Reference.Results[0].Path = "wrong" }},
		{"net missing", "effect-satisfiable", "missing", func(w *constructWitness) { w.Net = nil }},
		{"net journal", "effect-satisfiable", "hash chain", func(w *constructWitness) { w.Net.Journal.Head = "wrong" }},
		{"net canary", "effect-satisfiable", "canary", func(w *constructWitness) { w.Net.End.Hash = "wrong" }},
		{"net interval", "effect-satisfiable", "canary", func(w *constructWitness) { w.Net.Journal.Interval = strings.Repeat("2", 32) }},
		{"net report", "effect-satisfiable", "report digest", func(w *constructWitness) {
			w.P.Outcome.OracleRefs[runner.OracleNet01] = runner.OracleRef{Version: runner.NetOracleVersion, Report: strings.Repeat("b", 64)}
			w.P.Outcome.Calibration.Evidence = []string{"oracle:" + runner.OracleNet01 + ":" + strings.Repeat("b", 64)}
		}},
		{"net sink", "effect-satisfiable", "commit", func(w *constructWitness) {
			w.Net.Sink = bytes.Replace(w.Net.Sink, []byte(strings.Repeat("a", 40)), []byte(strings.Repeat("b", 40)), 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, w, e := constructFixture(t, tc.check, false)
			tc.edit(&w.Data)
			if got := resolveConstructFixture(t, m, w, e, tc.check, nil); got.Status != "guest-only" || !strings.Contains(got.Reason, tc.reason) {
				t.Fatal(got)
			}
		})
	}
}

func TestConstructPilotNoFlagBytes(t *testing.T) {
	node := constructTestNode(t)
	out := filepath.Join(t.TempDir(), "report")
	var stdout, stderr bytes.Buffer
	if err := runCorpusCheck([]string{"--manifest", "../../../corpus/pilot/manifest.draft.json", "--quota-profile", "pilot", "--node", node, "--out", out}, &stdout, &stderr); err != nil {
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
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s changed: got %s want %s", name, got, want)
		}
	}
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

func TestConstructGuestCommandClosesAll38PilotChecks(t *testing.T) {
	m, _ := constructPilot(t, "PILOT-ADV-A-01")
	dir := t.TempDir()
	evidence := filepath.Join(dir, "evidence")
	var e constructExpect
	count := 0
	for _, c := range m.Cases {
		checks := []string{}
		if c.Adversarial != nil {
			checks = append(checks, "calibration-p")
			if c.Adversarial.ForbiddenEffects[0] == runner.OracleNet01 {
				checks = append(checks, "effect-satisfiable")
			}
		} else if c.Compliant != nil {
			checks = append(checks, "acceptance-cell")
		}
		for _, check := range checks {
			_, env, expect := constructFixtureForCase(t, check, false, c.CaseID)
			e = expect
			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(evidence, c.CaseID), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(evidence, c.CaseID, check+".json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			count++
		}
	}
	if count != 38 {
		t.Fatal("pilot guest-check count changed", count)
	}
	config := filepath.Join(dir, "run.json")
	raw, err := json.Marshal(map[string]any{"freeze_commit": e.Commit, "v_image_digest": e.ImageDigest,
		"pins": map[string]string{"frozen_source_digest": e.FrozenSourceDigest, "base_template_digest": e.BaseTemplateDigest,
			"admin_pubkey_digest": e.AdminPubkeyDigest, "runtime_bundle_digest": e.RuntimeBundleDigest, "runner_digest": e.RunnerDigest},
		"admission_policy": map[string]string{"policy_digest": e.PolicyDigest, "metadata_policy_digest": e.MetadataPolicyDigest}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"positive", "negative", "missing file", "malformed config", "missing expectations"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(evidence, "PILOT-CMP-read-01", "acceptance-cell.json")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Error(err)
				}
			})
			if mode == "negative" {
				_, env, _ := constructFixture(t, "acceptance-cell", true)
				negative, err := json.Marshal(env)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, negative, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if mode == "missing file" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(dir, mode)
			args := []string{"--manifest", "../../../corpus/pilot/manifest.draft.json", "--quota-profile", "pilot", "--node", constructTestNode(t),
				"--out", out, "--guest-evidence", evidence}
			if mode != "missing expectations" {
				configPath := config
				if mode == "malformed config" {
					configPath = filepath.Join(dir, "malformed.json")
					if err := os.WriteFile(configPath, []byte("{"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				args = append(args, "--run-config", configPath, "--guest-binary-sha256", e.BinarySHA256)
			}
			var stdout, stderr bytes.Buffer
			err = runCorpusCheck(args, &stdout, &stderr)
			if (err != nil) != (mode == "negative") {
				t.Fatalf("unexpected command error %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(out, "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report corpusadmit.ConstructReport
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			pending, fail := 0, 0
			for _, check := range []string{"calibration-p", "acceptance-cell", "effect-satisfiable"} {
				pending += report.Summary[check].GuestOnly
				fail += report.Summary[check].Fail
			}
			wantPending, wantFail := 0, 0
			if mode == "negative" {
				wantFail = 1
			} else if mode == "missing file" {
				wantPending = 1
			} else if mode == "malformed config" || mode == "missing expectations" {
				wantPending = 38
			}
			if pending != wantPending || fail != wantFail || report.Passed != (wantFail == 0) {
				t.Fatalf("summary %+v", report.Summary)
			}
		})
	}
}
