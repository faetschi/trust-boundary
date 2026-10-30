package panelaudit

import (
	"fmt"
	"strings"
)

type Model struct {
	Family                 string   `json:"family,omitempty"`
	ObservedResponseModels []string `json:"observed_response_models,omitempty"`
	ID                     string   `json:"id"`
	Temperature            float64  `json:"temperature"`
	MaxTokens              int      `json:"max_tokens"`
	PromptUSDPerMTok       float64  `json:"prompt_usd_per_mtok"`
	CompletionUSDPerMTok   float64  `json:"completion_usd_per_mtok"`
}

// ExcludedTokens name the case-author and evaluated families (D35 amendment).
var ExcludedTokens = []string{"anthropic", "claude", "openai", "gpt", "deepseek"}

// familyTokens map a substring of a model id to its model family, so a reseller-hosted
// fine-tune counts as its base family where the id reveals it. Unknown ids fall back to the
// vendor prefix. docs/guides/panel-run.md documents this table.
var familyTokens = []struct{ token, family string }{
	{"gemini", "gemini"}, {"gemma", "gemini"}, {"google/", "gemini"},
	{"qwen", "qwen"}, {"qwq", "qwen"},
	{"llama", "llama"},
	{"mistral", "mistral"}, {"mixtral", "mistral"}, {"stral", "mistral"}, {"wizardlm", "mistral"},
	{"grok", "grok"}, {"x-ai/", "grok"},
}

// family returns the model family of an OpenRouter model id.
func Family(id string) string {
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
func ValidateModels(models []Model) error {
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
		for _, tok := range ExcludedTokens {
			if strings.Contains(low, tok) {
				return fmt.Errorf("model %q belongs to an excluded family (%s): Claude, GPT and DeepSeek authored or are evaluated", m.ID, tok)
			}
		}
		f := Family(m.ID)
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

// PublicPolicy is the service policy of the corpus run that is public to the agent: which
// workspace paths bash may change and which network destinations are allowed. The panel shows
// it to every rater and records it in the receipt, so the installed service config can be held
// against what the raters saw. The case's registered protected paths come from the manifest.
type PublicPolicy struct {
	BashOutputPaths     []string `json:"bash_output_paths"`
	NetworkDestinations []string `json:"network_destinations"`
	Source              string   `json:"source,omitempty"`
}
