//go:build linux

package piruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// infra/guest/serve-pi-service.g1.json is the single-task template of the engineering G1 case in
// corpus/g1 (EVQ-PROVIDER-REAL-EXCHANGE). It must stay a valid fresh-mode v3 config whose task,
// prompt, Bash output scope and ceilings are the case's.
func TestG1ServiceConfigTemplateIsValidAndIsTheCase(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "infra", "guest", "serve-pi-service.g1.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var config D09ManagedPiServiceConfig
	if err := decoder.Decode(&config); err != nil {
		t.Fatalf("g1 config does not match the service config schema: %v", err)
	}
	if config.SchemaVersion != D09ManagedPiServiceConfigSchemaV3 || config.WorkflowAdmissionMode != "fresh" ||
		config.ProviderRoute != D09ManagedPiProviderRouteEngineering {
		t.Fatalf("g1 config header: schema %q mode %q route %q", config.SchemaVersion, config.WorkflowAdmissionMode, config.ProviderRoute)
	}
	config.ContainersConfDigest, config.StorageConfDigest = DigestBytes([]byte("c")), DigestBytes([]byte("s"))
	if err := config.Validate(); err != nil {
		t.Fatalf("g1 config is not valid: %v", err)
	}
	caseData, err := os.ReadFile(filepath.Join(root, "corpus", "g1", "g1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		TaskID          string            `json:"task_id"`
		Prompt          string            `json:"prompt"`
		BashOutputPaths []string          `json:"bash_output_paths"`
		Ceilings        map[string]uint64 `json:"ceilings"`
	}
	if err := json.Unmarshal(caseData, &c); err != nil {
		t.Fatal(err)
	}
	if config.WorkflowTaskID != c.TaskID || config.WorkflowPrompt != c.Prompt || !slices.Equal(config.WorkflowBashOutputPaths, c.BashOutputPaths) {
		t.Error("the config's task, prompt or Bash output scope differs from corpus/g1/g1.json")
	}
	if uint64(config.WorkflowMaxHostCalls) != c.Ceilings["max_host_calls"] || uint64(config.WorkflowMaxTokens) != c.Ceilings["max_tokens"] ||
		uint64(config.WorkflowDurationSeconds) != c.Ceilings["duration_seconds"] {
		t.Error("the config's ceilings differ from corpus/g1/g1.json")
	}
	// A provider turn (ReserveHostCall) and every tool effect (ReserveEffect, which `read` also passes
	// through) each charge one workflow host call. The case is four tool calls plus the final text turn:
	// 4 effects + 5 provider turns = 9 host calls. workflowrecovery.TestG1TurnPatternNeedsNineHostCalls
	// shows that 8 refuses the fifth turn. Retries re-spend provider turns, so the ceiling keeps headroom.
	const effects, providerTurns = 4, 5
	if config.WorkflowMaxHostCalls < effects+providerTurns || len(config.WorkflowBashOutputPaths) == 0 {
		t.Errorf("host calls %d, bash output paths %v: no room for %d effects and %d provider turns, or no Bash output scope",
			config.WorkflowMaxHostCalls, config.WorkflowBashOutputPaths, effects, providerTurns)
	}
}
