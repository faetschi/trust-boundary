//go:build linux

package piruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func corpusServiceConfig() D09ManagedPiServiceConfig {
	config := testD09ManagedPiServiceConfig()
	config.SchemaVersion = D09ManagedPiServiceConfigSchema
	config.WorkflowAdmissionMode = D09ManagedPiWorkflowAdmissionCorpus
	config.WorkflowAdmissionID, config.WorkflowTaskID, config.WorkflowPrompt = "", "", ""
	config.CorpusManifestPath = D09ManagedPiCorpusManifestPath
	config.CorpusManifestDigest = DigestBytes([]byte("frozen corpus"))
	return config
}

func TestServiceConfigSchemaIsBumpedAndKeepsTheSingleTaskSchema(t *testing.T) {
	if D09ManagedPiServiceConfigSchema != "tbound-pi-service-config/v4" {
		t.Fatalf("config schema was not bumped for corpus admission: %q", D09ManagedPiServiceConfigSchema)
	}
	if D09ManagedPiServiceConfigSchemaV3 != "tbound-pi-service-config/v3" {
		t.Fatalf("legacy single-task schema constant drifted: %q", D09ManagedPiServiceConfigSchemaV3)
	}
	if D09ManagedPiCorpusManifestPath != "/etc/tbound/corpus/manifest.json" {
		t.Fatalf("corpus manifest path drifted: %q", D09ManagedPiCorpusManifestPath)
	}
}

func TestCorpusServiceConfigValidates(t *testing.T) {
	config := corpusServiceConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("corpus config refused: %v", err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeD09ManagedPiServiceConfig(encoded)
	if err != nil || !reflect.DeepEqual(decoded, config) {
		t.Fatalf("corpus config did not survive the strict decoder: %v", err)
	}
}

func TestCorpusServiceConfigRefusals(t *testing.T) {
	for name, mutate := range map[string]func(*D09ManagedPiServiceConfig){
		"legacy schema":        func(c *D09ManagedPiServiceConfig) { c.SchemaVersion = D09ManagedPiServiceConfigSchemaV3 },
		"unknown schema":       func(c *D09ManagedPiServiceConfig) { c.SchemaVersion = "tbound-pi-service-config/v5" },
		"missing path":         func(c *D09ManagedPiServiceConfig) { c.CorpusManifestPath = "" },
		"other path":           func(c *D09ManagedPiServiceConfig) { c.CorpusManifestPath = "/tmp/manifest.json" },
		"relative path":        func(c *D09ManagedPiServiceConfig) { c.CorpusManifestPath = "etc/tbound/corpus/manifest.json" },
		"missing digest":       func(c *D09ManagedPiServiceConfig) { c.CorpusManifestDigest = "" },
		"malformed digest":     func(c *D09ManagedPiServiceConfig) { c.CorpusManifestDigest = "sha256:abc" },
		"uppercase digest":     func(c *D09ManagedPiServiceConfig) { c.CorpusManifestDigest = "sha256:" + strings.Repeat("A", 64) },
		"admission id present": func(c *D09ManagedPiServiceConfig) { c.WorkflowAdmissionID = "host-task" },
		"task id present":      func(c *D09ManagedPiServiceConfig) { c.WorkflowTaskID = "approved-task" },
		"prompt present":       func(c *D09ManagedPiServiceConfig) { c.WorkflowPrompt = "inline prompt" },
		"zero host calls":      func(c *D09ManagedPiServiceConfig) { c.WorkflowMaxHostCalls = 0 },
		"zero tokens":          func(c *D09ManagedPiServiceConfig) { c.WorkflowMaxTokens = 0 },
	} {
		config := corpusServiceConfig()
		mutate(&config)
		if err := config.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
			t.Errorf("%s: corpus config accepted: %v", name, err)
		}
	}
}

func TestSingleTaskModesRefuseCorpusMembersAndStayOnBothSchemas(t *testing.T) {
	for _, mode := range []string{"fresh", "resume"} {
		for _, schema := range []string{D09ManagedPiServiceConfigSchemaV3, D09ManagedPiServiceConfigSchema} {
			config := testD09ManagedPiServiceConfig()
			config.SchemaVersion, config.WorkflowAdmissionMode = schema, mode
			if err := config.Validate(); err != nil {
				t.Errorf("%s config on %s refused: %v", mode, schema, err)
			}
			config.CorpusManifestPath = D09ManagedPiCorpusManifestPath
			if err := config.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
				t.Errorf("%s config on %s accepted a corpus path: %v", mode, schema, err)
			}
			config.CorpusManifestPath = ""
			config.CorpusManifestDigest = DigestBytes([]byte("frozen corpus"))
			if err := config.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
				t.Errorf("%s config on %s accepted a corpus digest: %v", mode, schema, err)
			}
		}
	}
	config := testD09ManagedPiServiceConfig()
	config.SchemaVersion, config.WorkflowAdmissionMode = D09ManagedPiServiceConfigSchemaV3, D09ManagedPiWorkflowAdmissionCorpus
	if err := config.Validate(); !errors.Is(err, ErrD09ManagedLiveRootRefused) {
		t.Errorf("corpus mode on the legacy schema accepted: %v", err)
	}
}

// A single-task config document must serialize exactly as before: the corpus
// members are omitted when empty, so the pinned digest of an existing config
// does not move.
func TestSingleTaskConfigEncodingOmitsCorpusMembers(t *testing.T) {
	encoded, err := json.Marshal(testD09ManagedPiServiceConfig())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "corpus_manifest") {
		t.Fatalf("single-task config encoding carries corpus members: %s", encoded)
	}
}

func TestServiceConfigDigestChangesWithTheCorpusManifestDigest(t *testing.T) {
	digest := func(manifest string) string {
		config := corpusServiceConfig()
		config.CorpusManifestDigest = DigestBytes([]byte(manifest))
		encoded, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		return DigestBytes(encoded)
	}
	first, second := digest("corpus one"), digest("corpus two")
	if first == second {
		t.Fatal("the service config digest does not cover corpus_manifest_digest")
	}
	if digest("corpus one") != first {
		t.Fatal("the service config digest is not deterministic")
	}
}

// The v3 smoke template is the owner's fixed single-task template and stays
// valid after the schema bump.
func TestSmokeServiceConfigTemplateStaysValidOnTheLegacySchema(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "infra", "guest", "serve-pi-service.smoke.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var config D09ManagedPiServiceConfig
	if err := decoder.Decode(&config); err != nil {
		t.Fatalf("smoke config does not match the service config schema: %v", err)
	}
	if config.SchemaVersion != D09ManagedPiServiceConfigSchemaV3 || config.WorkflowAdmissionMode != "fresh" ||
		config.ProviderRoute != D09ManagedPiProviderRouteEngineering {
		t.Fatalf("smoke config changed: schema %q mode %q route %q", config.SchemaVersion, config.WorkflowAdmissionMode, config.ProviderRoute)
	}
	config.ContainersConfDigest, config.StorageConfDigest = DigestBytes([]byte("c")), DigestBytes([]byte("s"))
	if err := config.Validate(); err != nil {
		t.Fatalf("smoke config is no longer valid: %v", err)
	}
}
