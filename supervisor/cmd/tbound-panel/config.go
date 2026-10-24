package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
type modelConfig struct {
	ID                   string  `json:"id"`
	Temperature          float64 `json:"temperature"`
	MaxTokens            int     `json:"max_tokens"`
	PromptUSDPerMTok     float64 `json:"prompt_usd_per_mtok"`
	CompletionUSDPerMTok float64 `json:"completion_usd_per_mtok"`
}

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

// excludedTokens name the case-author and evaluated families (D35 amendment).
var excludedTokens = []string{"anthropic", "claude", "openai", "gpt", "deepseek"}

// familyTokens map a substring of a model id to its model family, so a reseller-hosted
// fine-tune counts as its base family where the id reveals it. Unknown ids fall back to the
// vendor prefix. docs/panel-run.md documents this table.
var familyTokens = []struct{ token, family string }{
	{"gemini", "gemini"}, {"gemma", "gemini"}, {"google/", "gemini"},
	{"qwen", "qwen"}, {"qwq", "qwen"},
	{"llama", "llama"},
	{"mistral", "mistral"}, {"mixtral", "mistral"}, {"stral", "mistral"}, {"wizardlm", "mistral"},
	{"grok", "grok"}, {"x-ai/", "grok"},
}

// family returns the model family of an OpenRouter model id.
func family(id string) string {
	low := strings.ToLower(id)
	for _, ft := range familyTokens {
		if strings.Contains(low, ft.token) {
			return ft.family
		}
	}
	vendor, _, _ := strings.Cut(low, "/")
	return vendor
}

// validateModels enforces the D35 panel: exactly three pinned models from three distinct
// families, none of them a case-author or evaluated family.
func validateModels(models []modelConfig) error {
	if len(models) != 3 {
		return fmt.Errorf("the panel needs exactly 3 models from 3 families, config has %d (set models in the config)", len(models))
	}
	seen := map[string]string{}
	for _, m := range models {
		low := strings.ToLower(m.ID)
		vendor, name, found := strings.Cut(low, "/")
		if !found || vendor == "" || name == "" {
			return fmt.Errorf("model %q is not a vendor/model OpenRouter id", m.ID)
		}
		if vendor == "openrouter" || strings.Contains(low, "router") || strings.HasPrefix(low, "~") ||
			strings.HasSuffix(low, "-latest") || strings.HasSuffix(low, ":latest") || strings.Contains(low, ":online") {
			return fmt.Errorf("model %q is a router, a moving alias or a tool-enabled variant, not a pinned model", m.ID)
		}
		for _, tok := range excludedTokens {
			if strings.Contains(low, tok) {
				return fmt.Errorf("model %q belongs to an excluded family (%s): Claude, GPT and DeepSeek authored or are evaluated", m.ID, tok)
			}
		}
		f := family(m.ID)
		if prev, dup := seen[f]; dup {
			return fmt.Errorf("models %q and %q are both family %q", prev, m.ID, f)
		}
		seen[f] = m.ID
		if m.MaxTokens <= 0 || m.PromptUSDPerMTok <= 0 || m.CompletionUSDPerMTok <= 0 {
			return fmt.Errorf("model %q needs max_tokens, prompt_usd_per_mtok and completion_usd_per_mtok > 0 (the budget cap needs a worst case)", m.ID)
		}
	}
	return nil
}
