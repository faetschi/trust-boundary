package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/providerbridge"
)

func testCDH(t *testing.T) *CDHConfig {
	t.Helper()
	m, err := providerbridge.NewCDHLocalManifest("synthetic/local", "http://127.0.0.1:8080/v1", "sha256:"+strings.Repeat("a", 64), .8, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return &CDHConfig{Manifest: m, LiveAdversarialCases: []string{}}
}

func TestCDHPlanAndQualifiedIdentity(t *testing.T) {
	src, alloc := synth(t, legacyThesisQuotas())
	cfg := testConfig()
	cfg.CDH = testCDH(t)
	s, err := Plan(src, alloc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.Units() != 200 {
		t.Fatalf("units %d", s.Units())
	}
	counts := map[Configuration]int{}
	admissions := map[string]bool{}
	for _, st := range s.Steps {
		counts[st.Configuration]++
		if st.Kind != evalcorpus.KindCompliant {
			t.Fatal(st)
		}
		g := GovernedC{Configuration: st.Configuration, CDH: cfg.CDH, PlanDigest: s.Digest}
		r, err := g.Request(st)
		if err != nil {
			t.Fatal(err)
		}
		model, role, ok := corpusadmit.CDHRoleParts(r.Role)
		if !ok || CDHConfiguration(model) != st.Configuration || role != "compliant" {
			t.Fatal(r)
		}
		id := corpusadmit.AdmissionID(r.TaskID, r.Role, r.Attempt)
		if admissions[id] {
			t.Fatal("admission collision")
		}
		admissions[id] = true
		if id == corpusadmit.AdmissionID(st.CaseID, "compliant", 1) || id == corpusadmit.AdmissionID(st.CaseID, "b-compliant", 1) {
			t.Fatal("C/B collision")
		}
	}
	for _, model := range cfg.CDH.Manifest.Models {
		if counts[CDHConfiguration(model)] != 100 {
			t.Fatal(counts)
		}
	}
	cfg.CDH.Manifest.TimeoutMS = 500
	s2, err := Plan(src, alloc, cfg)
	if err != nil || s2.Digest == s.Digest {
		t.Fatalf("manifest not bound: %v", err)
	}
}

func TestCDHSafetyAndD32Refusal(t *testing.T) {
	src, alloc := synth(t, smallQuotas())
	cfg := smallConfig()
	cfg.CDH = testCDH(t)
	for i, c := range alloc.Cases {
		if c.Kind == evalcorpus.KindAdversarial {
			src.Cases[i].Trial = &evalcorpus.Trial{}
			alloc.Cases[i].Trial = &evalcorpus.Trial{}
			cfg.CDH.LiveAdversarialCases = []string{c.CaseID}
			s, err := Plan(src, alloc, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if s.Units() != 18 {
				t.Fatal(s.Units())
			}
			alloc.Cases[i].Trial.Pivots = map[string]evalcorpus.ScriptTurn{"d32": {Text: "synthetic"}}
			src.Cases[i].Trial.Pivots = alloc.Cases[i].Trial.Pivots
			if _, err := Plan(src, alloc, cfg); err == nil {
				t.Fatal("D32 accepted")
			}
			return
		}
	}
	t.Fatal("no adversarial fixture")
}

func TestCDHExportAndIndependentReruns(t *testing.T) {
	cfg := smallConfig()
	cfg.CDH = testCDH(t)
	s := smallSchedule(t, cfg)
	digest, _ := cfg.CDH.Manifest.Digest()
	fake := NewFakeExecutor(cfg)
	fake.Hook = func(st Step, o *Outcome) error {
		o.Receipt = &ReceiptFacts{HintChooser: strings.TrimPrefix(string(st.Configuration), "C-DH/"), HintManifestDigest: digest, Hints: []evalscore.HintFact{{CandidateID: "0", FallbackReason: "timeout", LatencyNS: 99}}}
		if st.CaseID == s.Steps[0].CaseID && st.Attempt == 1 {
			o.EvidenceLoss = evalscore.LossExternalOutage
			o.OutageEvidenceDigest = "sha256:" + strings.Repeat("b", 64)
		}
		return nil
	}
	j, _ := runAll(t, s, t.TempDir(), fake)
	defer j.Close()
	r := mustRunner(t, s, j, fake, Options{})
	for _, st := range s.Steps[:2] {
		if err := r.Rerun(context.Background(), st.ID()); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Input.Compliant) != 0 || len(res.Input.Adversarial) != 0 || len(res.Input.CDH.Trials) != 18 {
		t.Fatalf("pooled or lost trials: %+v", res)
	}
	var b bytes.Buffer
	if err := EncodeTrials(&b, res.Input); err != nil {
		t.Fatal(err)
	}
	loaded, err := evalscore.Load(&b)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.CDH.Trials) != 18 {
		t.Fatal("load lost trials")
	}
}

// Re-measured once for the D58 amendment: with no V step every compliant record exports v_missing.
func TestStaticCExportBytesUnchanged(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	j, _ := runAll(t, s, t.TempDir(), NewFakeExecutor(s.Config))
	defer j.Close()
	res, err := Export(s, j, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := EncodeTrials(&b, res.Input); err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(b.Bytes()))
	const want = "87367f9fdb1550c55fa99c646b568e97d4c7e3496ae39ce2b350722d1b385767"
	if got != want {
		t.Fatalf("static C export SHA256 = %s, want %s", got, want)
	}
}
