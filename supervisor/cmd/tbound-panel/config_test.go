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
		"fewer than three":   panelModels("google/gemini-2.5-pro", "qwen/qwen3-235b-a22b-2507"),
		"same family":        panelModels("google/gemini-2.5-pro", "google/gemma-3-27b-it", "qwen/qwen3-235b-a22b-2507"),
		"llama via reseller": panelModels("meta-llama/llama-4-maverick", "nousresearch/hermes-3-llama-3.1-405b", "qwen/qwen3-235b-a22b-2507"),
		"anthropic":          panelModels("anthropic/claude-sonnet-4", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"openai":             panelModels("openai/gpt-oss-120b", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"deepseek distill":   panelModels("deepseek/deepseek-r1-distill-qwen-32b", "google/gemini-2.5-pro", "mistralai/mistral-large-2411"),
		"deepseek reseller":  panelModels("tngtech/deepseek-r1t-chimera", "google/gemini-2.5-pro", "mistralai/mistral-large-2411"),
		"auto router":        panelModels("openrouter/auto", "google/gemini-2.5-pro", "mistralai/mistral-large-2411"),
		"web plugin":         panelModels("qwen/qwen3-235b-a22b-2507:online", "google/gemini-2.5-pro", "mistralai/mistral-large-2411"),
		"no vendor":          panelModels("gemini", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"),
		"four models":        panelModels("google/gemini-2.5-pro", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411", "x-ai/grok-4"),
	}
	for name, ms := range bad {
		if err := validateModels(ms); err == nil {
			t.Errorf("%s: accepted", name)
		}
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
	cfg, err := loadConfig(write(`{"schema":"tbound-panel-config/v1","models":[],"example_models":["google/gemini-2.5-pro"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BudgetUSD != 10 || cfg.Seed == 0 || cfg.BootstrapResamples != 2000 || cfg.AuditSample != 60 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if _, err := loadConfig(write(`{"schema":"tbound-panel-config/v1","models":[],"budget_usd":11}`)); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget above the hard cap accepted: %v", err)
	}
	if _, err := loadConfig(write(`{"schema":"tbound-panel-config/v1","models":[],"api_key":"x"}`)); err == nil {
		t.Fatal("unknown member (a key in the config) accepted")
	}
}
