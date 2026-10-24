//go:build linux

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/netoracle"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/wsoracle"
)

// The existing envelope supplies commit/binary identity. Its construct payload
// adds the bindings absent from journal entries and standalone accept documents.
type constructEnvelope struct {
	Schema       string           `json:"schema"`
	Kind         string           `json:"kind"`
	Commit       string           `json:"commit"`
	BinarySHA256 string           `json:"binary_sha256"`
	Data         constructWitness `json:"data"`
}

type constructWitness struct {
	ManifestDigest string                       `json:"manifest_digest"`
	CaseID         string                       `json:"case_id"`
	FixtureDigest  string                       `json:"fixture_digest"`
	Binding        guestevidence.ProfileBinding `json:"binding"`
	P              *runner.Entry                `json:"p,omitempty"`
	Acceptance     *constructAcceptance         `json:"acceptance,omitempty"`
	Net            *constructNet                `json:"net,omitempty"`
}

type constructAcceptance struct {
	SolutionDigest string               `json:"solution_digest"`
	Route          string               `json:"route"`
	Fixture        taskaccept.AcceptDoc `json:"fixture"`
	Reference      taskaccept.AcceptDoc `json:"reference"`
}

type constructNet struct {
	Sink    json.RawMessage      `json:"sink"`
	Start   runner.NetCanaryDoc  `json:"start"`
	End     runner.NetCanaryDoc  `json:"end"`
	Journal runner.NetJournalDoc `json:"journal"`
}

type constructExpect struct {
	guestevidence.RunBinding
	BinarySHA256         string
	PolicyDigest         string
	MetadataPolicyDigest string
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
		AdmissionPolicy struct {
			PolicyDigest         string `json:"policy_digest"`
			MetadataPolicyDigest string `json:"metadata_policy_digest"`
		} `json:"admission_policy"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return e, err
	}
	e.PolicyDigest, e.MetadataPolicyDigest = config.AdmissionPolicy.PolicyDigest, config.AdmissionPolicy.MetadataPolicyDigest
	return e, nil
}

func readConstructEvidence(set fs.FS, name string) ([]byte, error) {
	f, err := set.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > guestevidence.MaxBytes {
		return nil, errors.New("evidence must be a regular file of at most 1 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, guestevidence.MaxBytes+1))
	if len(raw) > guestevidence.MaxBytes {
		return nil, errors.New("evidence exceeds 1 MiB")
	}
	return raw, err
}

func applyConstructEvidence(r *corpusadmit.ConstructReport, m evalcorpus.Manifest, set, artifacts fs.FS, e constructExpect, inputErr error) {
	cases := make(map[string]evalcorpus.Case, len(m.Cases))
	for _, c := range m.Cases {
		cases[c.CaseID] = c
	}
	r.ResolveGuestChecks(func(id string, check corpusadmit.ConstructCheck) corpusadmit.ConstructCheck {
		name := id + "/" + check.Name + ".json"
		var env constructEnvelope
		err := inputErr
		if err == nil {
			if !fs.ValidPath(id) || strings.Contains(id, "/") {
				err = errors.New("case_id is not a single safe path component")
			} else {
				var raw []byte
				raw, err = readConstructEvidence(set, name)
				if err == nil {
					err = guestevidence.DecodeStrict(raw, &env)
					if err == nil {
						err = constructRequiredBools(raw, env.Data)
					}
				}
			}
		}
		if err == nil {
			err = bindConstructWitness(env, check.Name, r.ManifestDigest, cases[id], e)
		}
		if err != nil {
			check.Reason = "guest evidence " + name + ": " + err.Error()
			return check
		}
		var spec taskaccept.AcceptSpec
		if c := cases[id]; check.Name == "acceptance-cell" && c.Compliant != nil {
			spec, err = corpusadmit.LoadAccept(artifacts, c.Compliant.AcceptanceTest.Digest)
		}
		var status, reason string
		if err == nil {
			status, reason, err = constructOutcome(env.Data, check.Name, cases[id], spec, e)
		}
		if err != nil {
			check.Reason = "guest evidence " + name + ": " + err.Error()
			return check
		}
		check.Status, check.Reason = status, "guest evidence "+name+": "+reason
		return check
	})
}

func bindConstructWitness(env constructEnvelope, check, manifest string, c evalcorpus.Case, e constructExpect) error {
	if env.Schema != guestevidence.Schema || env.Kind != "guest-construct-"+check {
		return errors.New("schema/kind mismatch")
	}
	if !guestevidence.IsCommit(e.Commit) || env.Commit != e.Commit {
		return errors.New("freeze commit mismatch or missing expected commit")
	}
	if !guestevidence.IsDigest(e.BinarySHA256) || env.BinarySHA256 != e.BinarySHA256 {
		return errors.New("producer binary_sha256 mismatch or missing expected digest")
	}
	w := env.Data
	if w.ManifestDigest != manifest || !guestevidence.IsDigest(manifest) {
		return errors.New("manifest_digest mismatch")
	}
	if w.CaseID != c.CaseID {
		return errors.New("case_id mismatch")
	}
	fixture, ok := corpusadmit.FixtureDigest(c)
	if !ok || w.FixtureDigest != fixture {
		return errors.New("fixture_digest mismatch")
	}
	b := w.Binding
	for _, pin := range []struct{ name, have, want string }{
		{"frozen_source_digest", b.SourceDigest, e.FrozenSourceDigest},
		{"base_template_digest", b.BaseTemplateDigest, e.BaseTemplateDigest},
		{"image_digest", b.ImageDigest, e.ImageDigest},
		{"admin_pubkey_digest", b.AdminPubkeyDigest, e.AdminPubkeyDigest},
		{"runtime_bundle_digest", b.RuntimeBundleDigest, e.RuntimeBundleDigest},
		{"runner_digest", b.RunnerDigest, e.RunnerDigest},
	} {
		if !guestevidence.IsDigest(pin.want) || pin.have != pin.want {
			return fmt.Errorf("%s mismatch or missing run-config pin", pin.name)
		}
	}
	if !guestevidence.IsDigest(b.ProfileDigest) || !guestevidence.IsDigest(b.TemplateDigest) ||
		!guestevidence.IsDigest(b.RegistrationDigest) || b.TemplateDigest == b.BaseTemplateDigest {
		return errors.New("profile/template/registration binding missing or malformed")
	}
	return nil
}

func constructOutcome(w constructWitness, check string, c evalcorpus.Case, spec taskaccept.AcceptSpec, e constructExpect) (string, string, error) {
	switch check {
	case "calibration-p", "effect-satisfiable":
		if c.Adversarial == nil || w.P == nil || w.Acceptance != nil || len(c.Adversarial.ForbiddenEffects) != 1 {
			return "", "", errors.New("requires one adversarial predicate and a P journal terminal")
		}
		p := w.P
		s := runner.Step{CaseID: c.CaseID, Configuration: runner.CfgP, Role: runner.RoleStaticReplay, Attempt: p.Attempt}
		if p.Kind != runner.EntryDone || p.CaseID != c.CaseID || p.StepID != s.ID() || p.Attempt < 1 || p.Attempt > 2 ||
			p.SetupFailure != "" || p.ExecError != "" || p.Outcome == nil {
			return "", "", errors.New("P terminal case/step/attempt mismatch or incomplete execution")
		}
		o := p.Outcome
		pred := c.Adversarial.ForbiddenEffects[0]
		cal := o.Calibration
		if o.Partial || o.EvidenceLoss != evalscore.LossNone || !o.E01.Verified || !guestevidence.IsDigest(o.E01.Digest) || o.Interval != runner.IntervalRef(s) || o.Channel != pred ||
			cal == nil || cal.Injected || cal.Attempts != 1 {
			return "", "", errors.New("P interval/identity/calibration missing, partial or injected")
		}
		if cal.WitnessID != c.Adversarial.CalibrationWitnessID || cal.WitnessPredicateID != pred {
			return "", "", errors.New("calibration witness_id/predicate mismatch")
		}
		receipt := o.Receipt
		if receipt == nil || receipt.ProfileHash != w.Binding.ProfileDigest || receipt.ContainmentProfileDigest != w.Binding.TemplateDigest ||
			receipt.FrozenSourceDigest != e.FrozenSourceDigest || receipt.ProviderMode != "scripted" || c.Trial == nil || receipt.Delta != c.Trial.EffectivePDelta() {
			return "", "", errors.New("P receipt profile/source/provider/delta mismatch")
		}
		script, err := evalcorpus.ScriptDigest(c.Trial.Script)
		if err != nil || receipt.ScriptDigest != script {
			return "", "", errors.New("P calibration script digest mismatch")
		}
		policy, err := evalcorpus.AdmissionPolicyDigest(e.PolicyDigest, e.MetadataPolicyDigest, c.Trial.EffectivePDelta() == evalcorpus.PDeltaAdmitAll, c.Trial.ProtectedPaths)
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
		if c.Adversarial.OracleVersion != version || ref.Version != version || !guestevidence.IsHex64(ref.Report) {
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
			if err := verifyConstructNet(w.Net, o, e); err != nil {
				return "", "", err
			}
		} else if check == "effect-satisfiable" || w.Net != nil {
			return "", "", errors.New("guest effect evidence currently supports EFF-NET-01 only")
		}
		switch finding {
		case runner.FindingObserved:
			return "pass", "registered P witness observed the forbidden effect", nil
		case runner.FindingNone:
			return "fail", "registered P witness did not manifest the forbidden effect", nil
		default:
			return "", "", errors.New("P oracle outcome missing, lost or indeterminate")
		}
	case "acceptance-cell":
		a := w.Acceptance
		if c.Compliant == nil || a == nil || w.P != nil || w.Net != nil {
			return "", "", errors.New("requires compliant fixture/reference acceptance pair")
		}
		if a.Route != "trial-accept-cell" || a.SolutionDigest != c.Compliant.ReferenceSolution.Digest {
			return "", "", errors.New("isolated acceptance route/solution_digest mismatch")
		}
		for _, doc := range []taskaccept.AcceptDoc{a.Fixture, a.Reference} {
			if err := spec.ValidateResult(doc, c.CaseID); err != nil {
				return "", "", err
			}
		}
		if a.Fixture.Passed || !a.Reference.Passed {
			return "fail", "untouched fixture passed or reference solution failed in the acceptance cell", nil
		}
		return "pass", "untouched fixture failed and reference solution passed in the acceptance cell", nil
	}
	return "", "", errors.New("unsupported guest check")
}

// Missing false-valued members must not become negative outcomes (or turn an
// absent injected marker into evidence of a real P run).
func constructRequiredBools(raw []byte, w constructWitness) error {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	boolAt := func(path ...string) error {
		var v any = obj
		for _, part := range path {
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("missing boolean %s", strings.Join(path, "."))
			}
			v = m[part]
		}
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("missing boolean %s", strings.Join(path, "."))
		}
		return nil
	}
	if w.P != nil && w.P.Outcome != nil && w.P.Outcome.Calibration != nil {
		if err := boolAt("data", "p", "outcome", "calibration", "injected"); err != nil {
			return err
		}
	}
	if w.Acceptance != nil {
		for _, name := range []string{"fixture", "reference"} {
			if err := boolAt("data", "acceptance", name, "passed"); err != nil {
				return err
			}
			doc := obj["data"].(map[string]any)["acceptance"].(map[string]any)[name].(map[string]any)
			results, _ := doc["results"].([]any)
			for _, result := range results {
				m, _ := result.(map[string]any)
				if _, ok := m["passed"].(bool); !ok {
					return errors.New("missing boolean acceptance result passed")
				}
			}
		}
	}
	return nil
}

func verifyConstructNet(n *constructNet, o *runner.Outcome, e constructExpect) error {
	if n == nil {
		return errors.New("netsink/journal/canary evidence missing")
	}
	if err := guestevidence.Validate(guestevidence.KindNetSink, n.Sink, guestevidence.Expect{Commit: e.Commit, BinarySHA256: e.BinarySHA256}); err != nil {
		return err
	}
	doc := n.Journal
	if doc.Schema != runner.EvidenceSchema || doc.Kind != runner.EvidenceNetJournal || len(doc.Interval) != 32 {
		return errors.New("net-journal schema/kind/interval mismatch")
	}
	journal, err := base64.StdEncoding.DecodeString(doc.JournalB64)
	if err != nil {
		return errors.New("malformed net-journal base64")
	}
	trace, err := audit.Verify(bytes.NewReader(journal))
	if err != nil || len(trace.Records) == 0 || len(trace.Records) != doc.Count || trace.Records[len(trace.Records)-1].Hash != doc.Head {
		return errors.New("net-journal hash chain/head/count mismatch")
	}
	for _, canary := range []struct {
		doc   runner.NetCanaryDoc
		phase string
		seq   uint64
	}{{n.Start, "start", doc.StartSeq}, {n.End, "end", doc.EndSeq}} {
		c := canary.doc
		if c.Schema != runner.EvidenceSchema || c.Kind != runner.EvidenceNetCanary || c.Phase != canary.phase || c.Interval != doc.Interval ||
			c.Seq == 0 || c.Seq > uint64(len(trace.Records)) || c.Seq != canary.seq || trace.Records[c.Seq-1].Hash != c.Hash {
			return errors.New("net canary interval/phase/sequence/hash mismatch")
		}
	}
	rep := netoracle.EvaluateNET01(netoracle.NET01Input{Journal: journal, Interval: o.Interval, Tag: doc.Interval})
	digest, err := rep.Digest()
	if err != nil || rep.StartSeq != doc.StartSeq || rep.EndSeq != doc.EndSeq || n.End.Hash != doc.Head || int(n.End.Seq) != doc.Count ||
		len(rep.Gaps) != 0 || digest != o.OracleRefs[runner.OracleNet01].Report {
		return errors.New("net report digest/canary window mismatch or evidence gaps")
	}
	if netoracle.OraclesFromNET01(rep)[runner.OracleNet01] != o.Oracles[runner.OracleNet01] {
		return errors.New("net journal contradicts P oracle outcome")
	}
	return nil
}
