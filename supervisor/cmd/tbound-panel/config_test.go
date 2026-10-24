package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func panelModels(ids ...string) []modelConfig {
	var ms []modelConfig
	for _, id := range ids {
		ms = append(ms, modelConfig{ID: id, MaxTokens: 2000, PromptUSDPerMTok: 1, CompletionUSDPerMTok: 4})
	}
	return ms
}

func TestValidateModelsFamilies(t *testing.T) {
	ok := panelModels("google/gemini-2.5-pro", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411")
	if err := validateModels(ok); err != nil {
		t.Fatalf("valid panel refused: %v", err)
	}
	bad := map[string][]modelConfig{
		"fewer than three":    panelModels("google/gemini-2.5-pro", "qwen/qwen3-235b-a22b-2507"),
		"same family":         panelModels("google/gemini-2.5-pro", "google/gemma-3-27b-it", "qwen/qwen3-235b-a22b-2507"),
		"llama via reseller":  panelModels("meta-llama/llama-4-maverick", "nousresearch/hermes-3-llama-3.1-405b", "qwen/qwen3-235b-a22b-2507"),
		"anthropic":           panelModels("anthropic/claude-sonnet-4", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"openai":              panelModels("openai/gpt-oss-120b", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"deepseek distill":    panelModels("deepseek/deepseek-r1-distill-qwen-32b", "google/gemini-2.5-pro", "mistralai/mistral-large-2411"),
		"deepseek reseller":   panelModels("tngtech/deepseek-r1t-chimera", "google/gemini-2.5-pro", "mistralai/mistral-large-2411"),
		"auto router":         panelModels("openrouter/auto", "google/gemini-2.5-pro", "mistralai/mistral-large-2411"),
		"web plugin":          panelModels("qwen/qwen3-235b-a22b-2507:online", "google/gemini-2.5-pro", "mistralai/mistral-large-2411"),
		"no vendor":           panelModels("gemini", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"tilde alias":         panelModels("~google/gemini-pro-latest", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"latest alias":        panelModels("google/gemini-pro-latest", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"router id":           panelModels("switchpoint/router", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"wizardlm is mistral": panelModels("microsoft/wizardlm-2-8x22b", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"four models":         panelModels("google/gemini-2.5-pro", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411", "x-ai/grok-4"),
	}
	for name, ms := range bad {
		if err := validateModels(ms); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The shipped default lists no models, so it loads but cannot run until the owner chooses.
func TestDefaultConfigNamesNoModels(t *testing.T) {
	cfg, err := loadConfig("panel.default.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 0 || len(cfg.ExampleModels) == 0 || validateModels(cfg.Models) == nil || cfg.PublicPolicy == nil {
		t.Fatalf("default config must list no models but examples: %+v", cfg)
	}
}

func TestLoadConfigDefaultsAndCap(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "panel.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := loadConfig(write(`{"schema":"tbound-panel-config/v1","models":[]}`)); err == nil || !strings.Contains(err.Error(), "public_policy") {
		t.Fatalf("config without public_policy accepted: %v", err)
	}
	cfg, err := loadConfig(write(`{"schema":"tbound-panel-config/v1","models":[],"public_policy":{"bash_output_paths":[],"network_destinations":[]},"example_models":["google/gemini-2.5-pro"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BudgetUSD != 10 || cfg.Seed == 0 || cfg.BootstrapResamples != 2000 || cfg.AuditSample != 60 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if _, err := loadConfig(write(`{"schema":"tbound-panel-config/v1","models":[],"public_policy":{},"budget_usd":11}`)); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget above the hard cap accepted: %v", err)
	}
	if _, err := loadConfig(write(`{"schema":"tbound-panel-config/v1","models":[],"public_policy":{},"api_key":"x"}`)); err == nil {
		t.Fatal("unknown member (a key in the config) accepted")
	}
}
