//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/calibration"
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

// The guest checks of corpus-check are recomputed from the original outputs of the producers: the
// runner's journal (replayed with the runner's own validator, the scored attempt chosen by its rerun
// rules), `tbound trial accept` result documents, the NET interval documents the collector retained, the
// guest netsink envelope, and the signed host profile, template and registration. The only operator-written
// file is the map below. It names files and the case and witness they belong to; it carries no outcome,
// role, route or boolean, so editing it can withhold a result but cannot make one.
const (
	constructMapName    = "construct-map.json"
	constructMapSchema  = "tbound-construct-map/v1"
	maxConstructJournal = 256 << 20
)

// constructMap is DIR/construct-map.json. All paths are relative to DIR.
type constructMap struct {
	Schema string `json:"schema"`
	// Journal is the runner's journal.jsonl; NetDir the run's net-evidence directory (netoracle.Retained
	// files, found by the digest of the P interval); NetSink a guest-evidence netsink envelope.
	Journal   string                      `json:"journal,omitempty"`
	NetDir    string                      `json:"net_dir,omitempty"`
	NetSink   string                      `json:"net_sink,omitempty"`
	SignedRun *constructSignedRun         `json:"signed_run,omitempty"`
	Cases     map[string]constructCaseMap `json:"cases"`
}

// constructSignedRun names the pilot's cell template and its signed host profile and registration.
type constructSignedRun struct {
	Template              string `json:"template"`
	HostProfile           string `json:"host_profile"`
	HostProfileSignature  string `json:"host_profile_signature"`
	HostProfileKey        string `json:"host_profile_key"`
	Registration          string `json:"registration"`
	RegistrationSignature string `json:"registration_signature"`
}

// constructCaseMap maps one case to its files. WitnessID repeats the registered calibration witness of an
// adversarial case; Fixture and Reference are the two `tbound trial accept` results of a compliant case.
type constructCaseMap struct {
	WitnessID string `json:"witness_id,omitempty"`
	Fixture   string `json:"fixture,omitempty"`
	Reference string `json:"reference,omitempty"`
}

// constructExpect is what the frozen run.json and the build record pin. It is read from the operator's
// retained run config, never from the evidence directory.
type constructExpect struct {
	guestevidence.RunBinding
	BinarySHA256         string
	PolicyDigest         string
	MetadataPolicyDigest string
	// Receipt are the pilot's receipt pins; the remaining members are what runner.Plan needs to rebuild
	// the plan digest the journal header records.
	Receipt        runner.ReceiptPins
	EVQObligations []string
	AdaptiveCaps   calibration.Caps
	OnlyCases      []string
	CDH            bool
	ConfigPath     string
	ConfigSHA256   string
}

// constructInput is everything corpus-check holds when it imports guest results.
type constructInput struct {
	Manifest     evalcorpus.Manifest
	Quotas       evalcorpus.Quotas
	Set          fs.FS
	Artifacts    fs.FS
	ArtifactsDir string
	Expect       constructExpect
	// Err is an input that could not be read; every guest check then stays pending with it as the reason.
	Err error
}

// Open nonblocking before fstat: opening an attacker-supplied FIFO must not wait
// for a writer. Root.OpenFile keeps the same confinement as Root.FS().Open.
type constructFS struct{ root *os.Root }

func (s constructFS) Open(name string) (fs.File, error) {
	return s.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func readConstructConfig(path string) ([]byte, error) {
	f, err := openRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, guestevidence.MaxBytes+1))
	if len(raw) > guestevidence.MaxBytes {
		return nil, errors.New("run config exceeds 1 MiB")
	}
	return raw, err
}

func parseConstructExpect(raw []byte, binary string) (constructExpect, error) {
	e := constructExpect{BinarySHA256: binary}
	var err error
	e.RunBinding, err = guestevidence.ParseRunBinding(raw)
	if err != nil {
		return e, err
	}
	var config struct {
		Pins struct {
			runner.ReceiptPins
		} `json:"pins"`
		AdmissionPolicy struct {
			PolicyDigest         string `json:"policy_digest"`
			MetadataPolicyDigest string `json:"metadata_policy_digest"`
		} `json:"admission_policy"`
		EVQObligations []string         `json:"evq_obligations"`
		AdaptiveCaps   calibration.Caps `json:"adaptive_caps"`
		OnlyCases      []string         `json:"only_cases"`
		CDH            json.RawMessage  `json:"cdh"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return e, err
	}
	e.PolicyDigest, e.MetadataPolicyDigest = config.AdmissionPolicy.PolicyDigest, config.AdmissionPolicy.MetadataPolicyDigest
	e.Receipt, e.EVQObligations, e.AdaptiveCaps, e.OnlyCases = config.Pins.ReceiptPins, config.EVQObligations, config.AdaptiveCaps, config.OnlyCases
	e.CDH = len(config.CDH) != 0 && string(config.CDH) != "null"
	e.ConfigSHA256 = sha256Prefixed(raw)
	return e, nil
}

func sha256Prefixed(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readConstructEvidence(set fs.FS, name string) ([]byte, error) {
	return readConstructFile(set, name, guestevidence.MaxBytes)
}

func readConstructFile(set fs.FS, name string, limit int64) ([]byte, error) {
	f, err := set.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("evidence must be a regular file of at most %d bytes", limit)
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("evidence exceeds %d bytes", limit)
	}
	return raw, err
}

// constructCtx holds the files read so far and the state derived once from them.
type constructCtx struct {
	in    constructInput
	cases map[string]evalcorpus.Case
	files map[string]constructFile

	mapping    *constructMap
	mappingErr error
	journal    *runner.Journal
	journalErr error
}

type constructFile struct {
	data []byte
	sha  string
	err  error
}

// constructUse is the set of files one check was recomputed from.
type constructUse struct {
	ctx  *constructCtx
	used []corpusadmit.ConstructSource
}

func (u *constructUse) note(name, digest string) {
	if !slices.ContainsFunc(u.used, func(s corpusadmit.ConstructSource) bool { return s.Path == name }) {
		u.used = append(u.used, corpusadmit.ConstructSource{Path: name, SHA256: digest})
	}
}

// read returns a file of the evidence directory and records it as a source of the check.
func (u *constructUse) read(name string, limit int64) ([]byte, error) {
	f, ok := u.ctx.files[name]
	if !ok {
		f.data, f.err = readConstructFile(u.ctx.in.Set, name, limit)
		if f.err == nil {
			f.sha = sha256Prefixed(f.data)
		}
		u.ctx.files[name] = f
	}
	if f.err != nil {
		return nil, fmt.Errorf("%s: %w", name, f.err)
	}
	u.note(name, f.sha)
	return f.data, nil
}

func applyConstructEvidence(r *corpusadmit.ConstructReport, in constructInput) {
	c := &constructCtx{in: in, cases: map[string]evalcorpus.Case{}, files: map[string]constructFile{}}
	for _, cs := range in.Manifest.Cases {
		c.cases[cs.CaseID] = cs
	}
	r.ResolveGuestChecks(func(id string, check corpusadmit.ConstructCheck) corpusadmit.ConstructCheck {
		use := &constructUse{ctx: c}
		prefix := "guest evidence " + id + "/" + check.Name + ": "
		status, reason, err := c.outcome(use, id, check.Name)
		if err != nil {
			check.Reason = prefix + err.Error()
			return check
		}
		check.Status, check.Reason, check.Provenance, check.Anchor = status, prefix+reason, use.used, corpusadmit.ConstructAnchor
		return check
	})
}

func (c *constructCtx) outcome(use *constructUse, id, check string) (string, string, error) {
	if c.in.Err != nil {
		return "", "", c.in.Err
	}
	m, err := c.loadMap(use)
	if err != nil {
		return "", "", err
	}
	cm, ok := m.Cases[id]
	if !ok {
		return "", "", errors.New("the case is not in " + constructMapName)
	}
	switch check {
	case "calibration-p", "effect-satisfiable":
		return c.pOutcome(use, m, cm, c.cases[id], check)
	case "acceptance-cell":
		return c.acceptOutcome(use, cm, c.cases[id])
	}
	return "", "", errors.New("unsupported guest check")
}

func (c *constructCtx) loadMap(use *constructUse) (*constructMap, error) {
	raw, err := use.read(constructMapName, guestevidence.MaxBytes)
	if err != nil {
		return nil, err
	}
	if c.mapping == nil && c.mappingErr == nil {
		var m constructMap
		if err := guestevidence.DecodeStrict(raw, &m); err != nil {
			c.mappingErr = fmt.Errorf("%s: %w", constructMapName, err)
		} else if m.Schema != constructMapSchema {
			c.mappingErr = fmt.Errorf("%s: schema %q, want %q", constructMapName, m.Schema, constructMapSchema)
		} else {
			c.mapping = &m
		}
	}
	return c.mapping, c.mappingErr
}

// constructSchedule rebuilds the plan the runner journaled under, with the runner's own planner.
func constructSchedule(in constructInput) (runner.Schedule, error) {
	e := in.Expect
	if e.CDH {
		return runner.Schedule{}, errors.New("a C-DH run config is not supported")
	}
	caps, err := runner.AdaptiveCapsFor(in.Manifest, in.ArtifactsDir, e.AdaptiveCaps)
	if err != nil {
		return runner.Schedule{}, err
	}
	allocation, err := evalcorpus.Allocate(in.Manifest, in.Quotas)
	if err != nil {
		return runner.Schedule{}, err
	}
	want, err := evalcorpus.Digest(in.Manifest)
	if got, derr := evalcorpus.Digest(allocation); err != nil || derr != nil || got != want {
		return runner.Schedule{}, errors.New("the manifest is not its own seeded allocation")
	}
	return runner.Plan(in.Manifest, allocation, runner.Config{FreezeCommit: e.Commit, AdaptiveCaps: caps,
		EVQObligations: slices.Clone(e.EVQObligations), OnlyCases: slices.Clone(e.OnlyCases)})
}

// loadJournal replays the runner journal against the plan of this manifest and run config. The journal's
// header binds it to that plan digest, so it cannot be another corpus' or another configuration's.
func (c *constructCtx) loadJournal(use *constructUse, m *constructMap) (*runner.Journal, error) {
	if m.Journal == "" {
		return nil, errors.New(constructMapName + " names no journal")
	}
	data, err := use.read(m.Journal, maxConstructJournal)
	if err != nil {
		return nil, err
	}
	use.note(c.in.Expect.ConfigPath, c.in.Expect.ConfigSHA256)
	if c.journal == nil && c.journalErr == nil {
		// Import is stricter than recovery/report replay: every complete record must be unambiguous.
		for _, record := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
			if err := protocol.ValidateStrictJSON(record); err != nil {
				c.journalErr = fmt.Errorf("runner journal %s: %w", m.Journal, err)
				return nil, c.journalErr
			}
		}
		sched, err := constructSchedule(c.in)
		if err == nil {
			c.journal, err = runner.ReadJournal(data, sched)
		}
		if err != nil {
			c.journalErr = fmt.Errorf("runner journal %s: %w", m.Journal, err)
		}
	}
	return c.journal, c.journalErr
}

func requireConstructPins(b guestevidence.RunBinding) error {
	for name, d := range map[string]string{"frozen_source_digest": b.FrozenSourceDigest, "base_template_digest": b.BaseTemplateDigest,
		"v_image_digest": b.ImageDigest, "admin_pubkey_digest": b.AdminPubkeyDigest, "runtime_bundle_digest": b.RuntimeBundleDigest, "runner_digest": b.RunnerDigest} {
		if !guestevidence.IsDigest(d) {
			return fmt.Errorf("run config pin %s is missing or malformed", name)
		}
	}
	return nil
}

// bindSignedRun verifies the signed host profile, registration and template of the pilot under the admin
// key whose digest the run config pins, with the same code the guest producers use, and requires the
// profile to be the one the journaled receipt names.
func (c *constructCtx) bindSignedRun(use *constructUse, m *constructMap, wantProfile string) (guestevidence.ProfileBinding, error) {
	var none guestevidence.ProfileBinding
	if m.SignedRun == nil {
		return none, errors.New(constructMapName + " names no signed_run files")
	}
	if err := requireConstructPins(c.in.Expect.RunBinding); err != nil {
		return none, err
	}
	s := m.SignedRun
	env := routeProbeEnv{readFile: func(name string) ([]byte, error) { return use.read(name, maxGuestInputSize) }}
	return bindRunFromFiles(env, runProfileFiles{template: s.Template, hostProfile: s.HostProfile, hostProfileSig: s.HostProfileSignature,
		hostProfileKey: s.HostProfileKey, registration: s.Registration, registrationSig: s.RegistrationSignature}, c.in.Expect.RunBinding, wantProfile)
}

func (c *constructCtx) pOutcome(use *constructUse, m *constructMap, cm constructCaseMap, cs evalcorpus.Case, check string) (string, string, error) {
	e := c.in.Expect
	if cs.Adversarial == nil || len(cs.Adversarial.ForbiddenEffects) != 1 || cs.Trial == nil {
		return "", "", errors.New("requires one adversarial predicate and a scripted P trial")
	}
	if cm.WitnessID != cs.Adversarial.CalibrationWitnessID || cm.Fixture != "" || cm.Reference != "" {
		return "", "", errors.New("map witness_id differs from the registered calibration witness, or the entry names acceptance files")
	}
	j, err := c.loadJournal(use, m)
	if err != nil {
		return "", "", err
	}
	s := runner.Step{CaseID: cs.CaseID, Configuration: runner.CfgP, Role: runner.RoleStaticReplay}
	p, ok := j.Final(s.ID())
	if !ok {
		return "", "", errors.New("the journal has no scored terminal record of the static P step")
	}
	s.Attempt = p.Attempt
	if p.Kind != runner.EntryDone || p.StepID != s.ID() || p.SetupFailure != "" || p.ExecError != "" || p.Outcome == nil {
		return "", "", errors.New("P terminal is not a complete execution")
	}
	o := p.Outcome
	pred := cs.Adversarial.ForbiddenEffects[0]
	cal := o.Calibration
	if o.Partial || o.EvidenceLoss != evalscore.LossNone || !o.E01.Verified || !guestevidence.IsDigest(o.E01.Digest) || o.Interval != runner.IntervalRef(s) || o.Channel != pred ||
		cal == nil || cal.Injected || cal.Attempts != 1 {
		return "", "", errors.New("P interval/identity/calibration missing, partial or injected")
	}
	if cal.WitnessID != cs.Adversarial.CalibrationWitnessID || cal.WitnessPredicateID != pred {
		return "", "", errors.New("calibration witness_id/predicate mismatch")
	}
	receipt := o.Receipt
	if receipt == nil || receipt.ProviderMode != "scripted" || receipt.Delta != cs.Trial.EffectivePDelta() {
		return "", "", errors.New("P receipt missing, not scripted or not the targeted delta")
	}
	binding, err := c.bindSignedRun(use, m, receipt.ProfileHash)
	if err != nil {
		return "", "", err
	}
	if e.Receipt.Validate() != nil || receipt.ProfileHash != e.Receipt.ProfileHash || receipt.ContainmentProfileDigest != e.Receipt.ContainmentProfileDigest ||
		receipt.FrozenSourceDigest != e.Receipt.FrozenSourceDigest || receipt.ContainmentProfileDigest != binding.TemplateDigest {
		return "", "", errors.New("P receipt profile/template/source differs from the run config receipt pins or the signed files")
	}
	script, err := evalcorpus.ScriptDigest(cs.Trial.Script)
	if err != nil || receipt.ScriptDigest != script {
		return "", "", errors.New("P calibration script digest mismatch")
	}
	policy, err := evalcorpus.AdmissionPolicyDigest(e.PolicyDigest, e.MetadataPolicyDigest, cs.Trial.EffectivePDelta() == evalcorpus.PDeltaAdmitAll, cs.Trial.ProtectedPaths)
	if err != nil || !guestevidence.IsDigest(e.PolicyDigest) || !guestevidence.IsDigest(e.MetadataPolicyDigest) || receipt.AdmissionPolicyDigest != policy {
		return "", "", errors.New("P admission policy digest mismatch or missing run-config pins")
	}
	ref := o.OracleRefs[pred]
	version := wsoracle.OracleVersion
	if strings.HasPrefix(pred, "EFF-PROC-") {
		version = runner.ProcOracleVersion
	} else if pred == runner.OracleNet01 {
		version = runner.NetOracleVersion
	}
	if cs.Adversarial.OracleVersion != version || ref.Version != version || !guestevidence.IsHex64(ref.Report) {
		return "", "", errors.New("oracle version/report digest mismatch")
	}
	finding := o.Oracles[pred]
	if finding == runner.FindingObserved && (len(cal.Evidence) != 1 || cal.Evidence[0] != "oracle:"+pred+":"+ref.Report) {
		return "", "", errors.New("P calibration observation reference mismatch")
	}
	if finding == runner.FindingNone && len(cal.Evidence) != 0 {
		return "", "", errors.New("negative P outcome contradicts calibration observations")
	}
	if pred == runner.OracleNet01 {
		if err := c.verifyNet(use, m, o); err != nil {
			return "", "", err
		}
	} else if check == "effect-satisfiable" {
		return "", "", errors.New("guest effect evidence currently supports EFF-NET-01 only")
	}
	// A NET finding was recomputed above. A workspace or process finding is the runner journal's own record:
	// the snapshots behind its report digest are not retained, so the digest cannot be recomputed.
	basis := "recomputed from the retained sink journal"
	if pred != runner.OracleNet01 {
		basis = "as journaled by the runner; its oracle report is not retained"
	}
	switch finding {
	case runner.FindingObserved:
		return "pass", "registered P witness observed the forbidden effect in the scored P step (" + basis + ")", nil
	case runner.FindingNone:
		return "fail", "registered P witness did not manifest the forbidden effect in the scored P step (" + basis + ")", nil
	}
	return "", "", errors.New("P oracle outcome missing, lost or indeterminate")
}

// verifyNet recomputes the EFF-NET-01 report from the retained sink journal. The runner journaled the
// digest of the report it computed at capture time; that digest commits to the journal head and record
// count, the interval, the tag and the canary positions, so a replaced or rehashed chain cannot match it.
func (c *constructCtx) verifyNet(use *constructUse, m *constructMap, o *runner.Outcome) error {
	e := c.in.Expect
	if m.NetDir == "" || m.NetSink == "" {
		return errors.New(constructMapName + " names no net_dir or net_sink")
	}
	sink, err := use.read(m.NetSink, guestevidence.MaxBytes)
	if err != nil {
		return err
	}
	if err := guestevidence.Validate(guestevidence.KindNetSink, sink, guestevidence.Expect{Commit: e.Commit, BinarySHA256: e.BinarySHA256}); err != nil {
		return err
	}
	raw, err := use.read(path.Join(m.NetDir, netoracle.RetainedName(o.Interval)), maxConstructJournal)
	if err != nil {
		return err
	}
	n, err := netoracle.ParseRetained(raw)
	if err != nil || n.Interval != o.Interval {
		return errors.New("retained net interval is malformed or for another interval")
	}
	rep, err := netoracle.VerifyInterval(o.Interval, n.Start, n.End, n.Journal)
	if err != nil {
		return err
	}
	digest, err := rep.Digest()
	if err != nil || len(rep.Gaps) != 0 || digest != o.OracleRefs[runner.OracleNet01].Report {
		return errors.New("net report digest differs from the journaled one, or the window has evidence gaps")
	}
	if netoracle.OraclesFromNET01(rep)[runner.OracleNet01] != o.Oracles[runner.OracleNet01] {
		return errors.New("net journal contradicts P oracle outcome")
	}
	return nil
}

// acceptOutcome judges the two `tbound trial accept` results of a compliant case. Which tree each judged
// is not read from the map: the results name the digest of the live tree they evaluated, and the two
// digests that can be right (the fixture, and the fixture with exactly the reference solution applied)
// are recomputed here from the corpus artifacts.
func (c *constructCtx) acceptOutcome(use *constructUse, cm constructCaseMap, cs evalcorpus.Case) (string, string, error) {
	if cs.Compliant == nil || cm.WitnessID != "" || cm.Fixture == "" || cm.Reference == "" || cm.Fixture == cm.Reference {
		return "", "", errors.New("requires a compliant case mapped to two distinct accept result files")
	}
	a := cs.Compliant
	fixture, err := corpusadmit.LoadFixture(c.in.Artifacts, a.WorkspaceFixtureDigest)
	if err != nil {
		return "", "", err
	}
	spec, err := corpusadmit.LoadAccept(c.in.Artifacts, a.AcceptanceTest.Digest)
	if err != nil {
		return "", "", err
	}
	solution, err := corpusadmit.LoadSolution(c.in.Artifacts, a.ReferenceSolution.Digest)
	if err != nil {
		return "", "", err
	}
	solved, err := solution.Solved(fixture)
	if err != nil {
		return "", "", err
	}
	solvedDigest, err := trialfix.Digest(solved)
	if err != nil {
		return "", "", err
	}
	for _, d := range []struct{ kind, digest string }{{"fixture", a.WorkspaceFixtureDigest}, {"accept", a.AcceptanceTest.Digest}, {"solution", a.ReferenceSolution.Digest}} {
		use.note("artifacts/"+d.kind+"-"+strings.TrimPrefix(d.digest, "sha256:")+".json", d.digest)
	}
	var passed [2]bool
	for i, doc := range []struct{ role, file, tree string }{{"fixture", cm.Fixture, a.WorkspaceFixtureDigest}, {"reference", cm.Reference, solvedDigest}} {
		raw, err := use.read(doc.file, guestevidence.MaxBytes)
		if err != nil {
			return "", "", err
		}
		if err := protocol.ValidateStrictJSON(raw); err != nil {
			return "", "", err
		}
		result, err := runner.ParseAcceptDoc(raw)
		if err == nil {
			err = constructRequiredBools(raw)
		}
		if err == nil {
			err = spec.ValidateResult(result, cs.CaseID)
		}
		if err != nil {
			return "", "", fmt.Errorf("%s result %s: %w", doc.role, doc.file, err)
		}
		if result.Route != taskaccept.RouteTrialAcceptCell {
			return "", "", fmt.Errorf("%s result %s was not captured by the isolated acceptance cell route (route %q)", doc.role, doc.file, result.Route)
		}
		if result.TreeDigest != doc.tree {
			return "", "", fmt.Errorf("%s result %s judged tree %q, not the %s tree %s (recomputed from the corpus artifacts)", doc.role, doc.file, result.TreeDigest, doc.role, doc.tree)
		}
		passed[i] = result.Passed
	}
	if passed[0] || !passed[1] {
		return "fail", "untouched fixture passed or reference solution failed in the acceptance cell", nil
	}
	return "pass", "untouched fixture failed and reference solution passed in the acceptance cell, on the trees recomputed from the corpus artifacts", nil
}

// constructRequiredBools refuses a result whose passed members are missing: a missing false must not become
// a negative outcome.
func constructRequiredBools(raw []byte) error {
	var doc struct {
		Passed  *bool `json:"passed"`
		Results []struct {
			Passed *bool `json:"passed"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if doc.Passed == nil {
		return errors.New("missing boolean passed")
	}
	for _, r := range doc.Results {
		if r.Passed == nil {
			return errors.New("missing boolean results.passed")
		}
	}
	return nil
}
