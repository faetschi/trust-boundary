//go:build linux

package main

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runner"
)

func validateServiceCDH(c piServiceConfig) error {
	if c.CDH == nil {
		return nil
	}
	if c.WorkflowAdmissionMode != piServiceAdmissionCorpus || c.ProviderRoute != "research" {
		return errors.New("C-DH requires a corpus research service")
	}
	return c.CDH.Validate()
}

// prepareCDH installs only manifest-pinned clients. No discovery or hosted fallback exists.
func (c *productionCorpus) prepareCDH(cfg *runner.CDHConfig, route productionProviderRoute) error {
	if cfg == nil {
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	for _, id := range cfg.LiveAdversarialCases {
		info, ok := c.cases[id]
		if !ok || info.kind != evalcorpus.KindAdversarial || len(c.trials[id].Pivots) != 0 {
			return fmt.Errorf("C-DH refuses unregistered or D32 safety case %q", id)
		}
	}
	providers := map[string]providerbridge.HintProvider{}
	for i, model := range cfg.Manifest.Models {
		transport := cfg.Manifest.Transports[i]
		cc := providerbridge.ChooserConfig{Enabled: true, Model: model}
		if transport.Kind == "local" {
			t := http.DefaultTransport.(*http.Transport).Clone()
			t.Proxy = nil
			cc.Client = &http.Client{Transport: t, Timeout: time.Duration(cfg.Manifest.TimeoutMS) * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("chooser redirect refused") }}
		} else {
			if route.research == nil {
				return errors.New("hosted C-DH requires the research route")
			}
			credentials, err := route.research.Credentials()
			if err != nil {
				return err
			}
			client, err := credentials.NewHTTPDoerForEndpoint(transport.Endpoint+"/decisions", time.Duration(cfg.Manifest.TimeoutMS)*time.Millisecond)
			if err != nil {
				return err
			}
			cc.Client = client
		}
		provider, err := providerbridge.NewCDHProvider(cfg.Manifest, cc)
		if err != nil {
			return err
		}
		providers[model] = provider
	}
	c.cdh, c.hintProviders = cfg, providers
	return nil
}

func (c *productionCorpus) cdhAllowed(caseID, role string) bool {
	model, base, ok := corpusadmit.CDHRoleParts(role)
	if !ok || c == nil || c.cdh == nil || len(c.cdh.Manifest.Models) != 2 {
		return false
	}
	info, ok := c.cases[caseID]
	if !ok {
		return false
	}
	if !slices.Contains(c.cdh.Manifest.Models, model) || c.hintProviders[model] == nil {
		return false
	}
	if base == "compliant" {
		return info.kind == evalcorpus.KindCompliant
	}
	return info.kind == evalcorpus.KindAdversarial && slices.Contains(c.cdh.LiveAdversarialCases, caseID) && len(c.trials[caseID].Pivots) == 0
}

func cdhReceipt(receipt *productionPiReceipt, b *corpusBinding, trace audit.Trace) error {
	if b == nil || b.hintProvider == nil {
		return nil
	}
	receipt.HintChooser, receipt.HintManifestDigest = b.hintChooser, b.hintManifestDigest
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_tool_result" && record.Event.Kind != "provider_admission_denied" {
			continue
		}
		var evidence providerAdmissionDeniedEvidence
		if err := unmarshalGovernedJSON(record.Event.Data, &evidence); err != nil {
			return err
		}
		h := evidence.Hint
		if h == nil {
			continue
		}
		if h.Provider != "choice" || h.Model != b.hintChooser || h.ManifestDigest != b.hintManifestDigest {
			return errors.New("C-DH journal treatment mismatch")
		}
		receipt.Hints = append(receipt.Hints, evalscore.HintFact{CandidateID: h.CandidateID, FallbackReason: h.FallbackReason, LatencyNS: h.LatencyNS})
	}
	return nil
}
