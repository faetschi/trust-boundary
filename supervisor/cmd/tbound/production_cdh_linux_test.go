//go:build linux

package main

import (
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runner"
)

func TestCDHServiceAdmissionBindingAndStaticIsolation(t *testing.T) {
	c, manifest := scriptedTestCorpus(t, nil)
	m, err := providerbridge.NewCDHLocalManifest("synthetic/local", "http://127.0.0.1:8080/v1", "sha256:"+strings.Repeat("a", 64), .8, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.cdh = &runner.CDHConfig{Manifest: m}
	c.hintProviders = map[string]providerbridge.HintProvider{}
	for _, model := range m.Models {
		p, err := providerbridge.NewCDHProvider(m, providerbridge.ChooserConfig{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		c.hintProviders[model] = p
	}
	var compliant, adv string
	for _, v := range manifest.Cases {
		if v.Kind == evalcorpus.KindCompliant {
			compliant = v.CaseID
		}
		if v.Kind == evalcorpus.KindAdversarial {
			adv = v.CaseID
		}
	}
	ids := map[string]bool{}
	for _, model := range m.Models {
		role := corpusadmit.CDHRole(model, "compliant")
		if !validCorpusRole(role) || !c.roleServable(compliant, role) || c.roleAllowed(adv, role) {
			t.Fatal("role matrix")
		}
		id := corpusAdmissionID(compliant, role, 1)
		if ids[id] || id == corpusAdmissionID(compliant, corpusRoleCompliant, 1) {
			t.Fatal("admission collision")
		}
		ids[id] = true
		ref, ok := corpusIntervalRef(id, compliant)
		if !ok || ref != runner.IntervalRef(runner.Step{CaseID: compliant, Configuration: runner.CDHConfiguration(model), Role: runner.RoleCompliant, Attempt: 1}) {
			t.Fatal(ref)
		}
		b, err := c.bindingFor(compliant, role)
		if err != nil {
			t.Fatal(err)
		}
		a := &SessionOperationAuthority{}
		if err := a.BindCorpusBinding(b); err != nil {
			t.Fatal(err)
		}
		cfg := a.applyProviderRoute(providerbridge.Config{})
		if cfg.HintProvider == nil || b.hintChooser != model || b.scripted() {
			t.Fatal("chooser not bound")
		}
		static, err := c.bindingFor(compliant, corpusRoleCompliant)
		if err != nil {
			t.Fatal(err)
		}
		if static.hintProvider != nil || static.hintChooser != "" {
			t.Fatal("C changed")
		}
		if err := a.BindCorpusBinding(static); err != nil {
			t.Fatal(err)
		}
		if a.applyProviderRoute(providerbridge.Config{}).HintProvider != nil {
			t.Fatal("C retained chooser")
		}
	}
	if validCorpusRole(corpusadmit.CDHRole(m.Models[0], "adaptive-replay")) {
		t.Fatal("D32 role accepted")
	}
	c.cdh.LiveAdversarialCases = []string{adv}
	tr := c.trials[adv]
	tr.Pivots = map[string]evalcorpus.ScriptTurn{"d32": {Text: "synthetic"}}
	c.trials[adv] = tr
	if c.roleAllowed(adv, corpusadmit.CDHRole(m.Models[0], "trajectory")) {
		t.Fatal("D32 safety case accepted")
	}
	c.cdh = nil
	if c.roleAllowed(compliant, corpusadmit.CDHRole(m.Models[0], "compliant")) {
		t.Fatal("default-off violated")
	}
}

func TestCDHServiceConfigDefaultOffAndValidation(t *testing.T) {
	if err := validateServiceCDH(piServiceConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := validateServiceCDH(piServiceConfig{CDH: &runner.CDHConfig{}}); err == nil {
		t.Fatal("invalid manifest enabled")
	}
}
