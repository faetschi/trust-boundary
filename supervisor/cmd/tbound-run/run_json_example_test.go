package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
)

// TestRunJSONExampleDecodesStrictly decodes the operator's run.json example through loadConfig, the
// strict decoder. It refuses an unknown member, a duplicate key or trailing data, and validate runs.
// The example's paths start with /REPLACE, which only Linux treats as absolute. The test puts the
// drive of the working directory in front on Windows, as C:/REPLACE. A forward slash keeps the path
// free of backslashes, which the SSH path check refuses, and the drive keeps it free of spaces.
func TestRunJSONExampleDecodesStrictly(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "infra", "guest", "operator", "run.json.example"))
	if err != nil {
		t.Fatal(err)
	}
	base := "/REPLACE"
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if vol := filepath.VolumeName(cwd); vol != "" {
		base = vol + "/REPLACE"
	}
	doc := strings.ReplaceAll(string(raw), `"/REPLACE/`, `"`+base+`/`)
	path := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("the example is refused by the strict decoder: %v", err)
	}

	mappingRaw, err := os.ReadFile(filepath.Join(root, "infra", "conformance", "evq-mapping.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mapping struct {
		Obligations []struct {
			ID string `json:"id"`
		} `json:"obligations"`
	}
	if err := json.Unmarshal(mappingRaw, &mapping); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, o := range mapping.Obligations {
		want = append(want, o.ID)
	}
	if len(want) != 22 || !slices.Equal(cfg.EVQObligations, want) {
		t.Fatalf("evq_obligations = %v, want the %d obligations of evq-mapping.json in their order", cfg.EVQObligations, len(want))
	}
	if cfg.AdmissionPolicy.PolicyDigest != piruntime.DigestBytes([]byte(gate.ProductionSessionPolicyLabel)) ||
		cfg.AdmissionPolicy.MetadataPolicyDigest != piruntime.DigestBytes([]byte(gate.ProductionMetadataPolicyLabel)) {
		t.Fatal("admission_policy is not the sha256 of the production policy labels")
	}
}
