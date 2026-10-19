//go:build linux

package piruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// infra/guest/serve-pi-service.route-probe.json is the single-task template of the route-probe run in
// corpus/route-probe (the guest-route-bash-view artifact). Bash is allowed with no output scope, so the
// probe commands may change no workspace path.
func TestRouteProbeServiceConfigTemplateIsValidAndIsTheCase(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "infra", "guest", "serve-pi-service.route-probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var config D09ManagedPiServiceConfig
	if err := decoder.Decode(&config); err != nil {
		t.Fatalf("route-probe config does not match the service config schema: %v", err)
	}
	if config.SchemaVersion != D09ManagedPiServiceConfigSchemaV3 || config.WorkflowAdmissionMode != "fresh" ||
		config.ProviderRoute != D09ManagedPiProviderRouteEngineering {
		t.Fatalf("route-probe config header: schema %q mode %q route %q", config.SchemaVersion, config.WorkflowAdmissionMode, config.ProviderRoute)
	}
	config.ContainersConfDigest, config.StorageConfDigest = DigestBytes([]byte("c")), DigestBytes([]byte("s"))
	if err := config.Validate(); err != nil {
		t.Fatalf("route-probe config is not valid: %v", err)
	}
	caseData, err := os.ReadFile(filepath.Join(root, "corpus", "route-probe", "route-probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		TaskID   string            `json:"task_id"`
		Prompt   string            `json:"prompt"`
		Ceilings map[string]uint64 `json:"ceilings"`
	}
	if err := json.Unmarshal(caseData, &c); err != nil {
		t.Fatal(err)
	}
	if config.WorkflowTaskID != c.TaskID || config.WorkflowPrompt != c.Prompt {
		t.Error("the config's task or prompt differs from corpus/route-probe/route-probe.json")
	}
	if config.WorkflowBashOutputPaths == nil || len(config.WorkflowBashOutputPaths) != 0 {
		t.Errorf("bash output paths = %#v, want the explicit empty scope (--bash-no-outputs)", config.WorkflowBashOutputPaths)
	}
	if uint64(config.WorkflowMaxHostCalls) != c.Ceilings["max_host_calls"] || uint64(config.WorkflowMaxTokens) != c.Ceilings["max_tokens"] ||
		uint64(config.WorkflowDurationSeconds) != c.Ceilings["duration_seconds"] {
		t.Error("the config's ceilings differ from the case")
	}
}
