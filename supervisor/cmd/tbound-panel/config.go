package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"tbound/supervisor/internal/panelaudit"
)

const configSchema = "tbound-panel-config/v1"

// hardCapUSD is the largest spend cap a config may set. A lower budget_usd is allowed.
const hardCapUSD = 10.0

// panelConfig is the owner-chosen panel. Models are configuration, never defaults.
type panelConfig struct {
	Schema string        `json:"schema"`
	Models []modelConfig `json:"models"`
	// ExampleModels documents candidate ids for the owner; it is never used to run.
	ExampleModels []string `json:"example_models,omitempty"`
	// PublicPolicy is the policy the agent is assumed to know (thesis 3.2.1); both phases show it.
	PublicPolicy       *publicPolicy `json:"public_policy"`
	Comment            string        `json:"_comment,omitempty"`
	BudgetUSD          float64       `json:"budget_usd,omitempty"`
	Seed               uint64        `json:"seed,omitempty"`
	BootstrapResamples int           `json:"bootstrap_resamples,omitempty"`
	AuditSample        int           `json:"audit_sample,omitempty"`
	TimeoutSeconds     int           `json:"timeout_seconds,omitempty"`
}

// publicPolicy is the service policy of the corpus run that is public to the agent: which
// workspace paths bash may change and which network destinations are allowed. The case's
// registered protected paths come from the manifest.
type publicPolicy struct {
	BashOutputPaths     []string `json:"bash_output_paths"`
	NetworkDestinations []string `json:"network_destinations"`
	Source              string   `json:"source,omitempty"`
}

// modelConfig is one panel member. Prices are USD per million tokens, copied by the owner
// from the OpenRouter model page; they bound the worst case of the next call.
type modelConfig = panelaudit.Model

func loadConfig(path string) (panelConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return panelConfig{}, err
	}
	var cfg panelConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return panelConfig{}, fmt.Errorf("config %s: %w", path, err)
	}
	if cfg.Schema != configSchema {
		return panelConfig{}, fmt.Errorf("config %s: schema must be %q", path, configSchema)
	}
	if cfg.PublicPolicy == nil {
		return panelConfig{}, fmt.Errorf("config %s: public_policy is required (copy it from panel.default.json and check it against the service config)", path)
	}
	if cfg.BudgetUSD == 0 {
		cfg.BudgetUSD = hardCapUSD
	}
	if cfg.BudgetUSD < 0 || cfg.BudgetUSD > hardCapUSD {
		return panelConfig{}, fmt.Errorf("config %s: budget_usd must be in (0, %.0f]", path, hardCapUSD)
	}
	if cfg.Seed == 0 {
		cfg.Seed = 20261024
	}
	if cfg.BootstrapResamples == 0 {
		cfg.BootstrapResamples = 2000
	}
	if cfg.AuditSample == 0 {
		cfg.AuditSample = 60
	}
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 300
	}
	for i := range cfg.Models {
		if cfg.Models[i].MaxTokens == 0 {
			cfg.Models[i].MaxTokens = 4000
		}
	}
	return cfg, nil
}

var excludedTokens = panelaudit.ExcludedTokens
var family = panelaudit.Family
var validateModels = panelaudit.ValidateModels
