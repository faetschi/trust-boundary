//go:build linux

package piruntime

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

// The worker's tool schema digests are the ones adapter/profile.json pins for the schemas the adapter registers
// and the broker advertises, so the ready event the worker echoes names the schemas that are really in force.
func TestPiWorkerToolSchemaDigestsEqualTheAdapterProfilePins(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "adapter", "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Digests map[string]string `json:"schema_sha256"`
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(profile.Digests, piWorkerToolSchemaDigests()) || !maps.Equal(profile.Digests, cloneToolSchemaDigests()) {
		t.Fatalf("worker tool schema digests %v differ from adapter/profile.json %v", piWorkerToolSchemaDigests(), profile.Digests)
	}
}
