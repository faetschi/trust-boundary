//go:build linux

package piruntime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCorpusQuotaProfileValidation(t *testing.T) {
	type mutate = func(*D09ManagedPiServiceConfig)
	accepted := map[string]mutate{
		"default (absent) is thesis": func(c *D09ManagedPiServiceConfig) {},
		"explicit thesis":            func(c *D09ManagedPiServiceConfig) { c.CorpusQuotaProfile = "thesis" },
		"thesis on research": func(c *D09ManagedPiServiceConfig) {
			c.CorpusQuotaProfile, c.ProviderRoute = "thesis", D09ManagedPiProviderRouteResearch
		},
		"pilot on engineering": func(c *D09ManagedPiServiceConfig) { c.CorpusQuotaProfile = "pilot" },
		"pilot on research and allowed": func(c *D09ManagedPiServiceConfig) {
			c.CorpusQuotaProfile, c.ProviderRoute, c.AllowPilotOnResearch = "pilot", D09ManagedPiProviderRouteResearch, true
		},
		"rehearsal on research": func(c *D09ManagedPiServiceConfig) {
			c.CorpusQuotaProfile, c.ProviderRoute = "rehearsal", D09ManagedPiProviderRouteResearch
		},
	}
	for name, change := range accepted {
		config := corpusServiceConfig()
		change(&config)
		if err := config.Validate(); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	refused := map[string]mutate{
		"pilot on research without the allowance": func(c *D09ManagedPiServiceConfig) {
			c.CorpusQuotaProfile, c.ProviderRoute = "pilot", D09ManagedPiProviderRouteResearch
		},
		"allowance without pilot": func(c *D09ManagedPiServiceConfig) {
			c.ProviderRoute, c.AllowPilotOnResearch = D09ManagedPiProviderRouteResearch, true
		},
		"allowance with thesis":              func(c *D09ManagedPiServiceConfig) { c.CorpusQuotaProfile, c.AllowPilotOnResearch = "thesis", true },
		"allowance on the engineering route": func(c *D09ManagedPiServiceConfig) { c.CorpusQuotaProfile, c.AllowPilotOnResearch = "pilot", true },
		"rehearsal on engineering":           func(c *D09ManagedPiServiceConfig) { c.CorpusQuotaProfile = "rehearsal" },
		"rehearsal with the allowance": func(c *D09ManagedPiServiceConfig) {
			c.CorpusQuotaProfile, c.ProviderRoute, c.AllowPilotOnResearch = "rehearsal", D09ManagedPiProviderRouteResearch, true
		},
		"unknown profile":     func(c *D09ManagedPiServiceConfig) { c.CorpusQuotaProfile = "full" },
		"capitalised profile": func(c *D09ManagedPiServiceConfig) { c.CorpusQuotaProfile = "Pilot" },
		"padded profile":      func(c *D09ManagedPiServiceConfig) { c.CorpusQuotaProfile = "pilot " },
	}
	for name, change := range refused {
		config := corpusServiceConfig()
		change(&config)
		if err := config.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
			t.Errorf("%s: accepted: %v", name, err)
		}
	}
	// Single-task modes carry no corpus members of any kind.
	for _, mode := range []string{"fresh", "resume"} {
		for name, change := range map[string]mutate{
			"profile":   func(c *D09ManagedPiServiceConfig) { c.CorpusQuotaProfile = "thesis" },
			"allowance": func(c *D09ManagedPiServiceConfig) { c.AllowPilotOnResearch = true },
		} {
			config := testD09ManagedPiServiceConfig()
			config.WorkflowAdmissionMode = mode
			change(&config)
			if err := config.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
				t.Errorf("%s config accepted a corpus %s: %v", mode, name, err)
			}
		}
	}
}

// The profile is a member of the signed config document, so the digest the registration
// pins covers it, and a thesis config document is encoded exactly as before.
func TestCorpusQuotaProfileIsDigestBound(t *testing.T) {
	digest := func(profile string, allow bool, route string) string {
		config := corpusServiceConfig()
		config.CorpusQuotaProfile, config.AllowPilotOnResearch, config.ProviderRoute = profile, allow, route
		encoded, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		return DigestBytes(encoded)
	}
	eng, res := D09ManagedPiProviderRouteEngineering, D09ManagedPiProviderRouteResearch
	thesis, explicit, pilot := digest("", false, eng), digest("thesis", false, eng), digest("pilot", false, eng)
	if thesis == explicit || thesis == pilot || explicit == pilot {
		t.Fatal("the service config digest does not cover corpus_quota_profile")
	}
	if digest("pilot", true, res) == digest("pilot", false, res) {
		t.Fatal("the service config digest does not cover allow_pilot_on_research")
	}
	encoded, err := json.Marshal(corpusServiceConfig())
	if err != nil || strings.Contains(string(encoded), "quota") || strings.Contains(string(encoded), "pilot") {
		t.Fatalf("a default corpus config changed its encoding: %s %v", encoded, err)
	}
	config := corpusServiceConfig()
	config.CorpusQuotaProfile = "pilot"
	encoded, _ = json.Marshal(config)
	decoded, err := decodeD09ManagedPiServiceConfig(encoded)
	if err != nil || decoded.CorpusQuotaProfile != "pilot" {
		t.Fatalf("pilot profile did not survive the strict decoder: %+v %v", decoded, err)
	}
}
