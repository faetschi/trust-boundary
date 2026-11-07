package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
)

// runJSONExampleFile is the operator's run.json example, read from the repository.
func runJSONExampleFile() string {
	return filepath.Join("..", "..", "..", "infra", "guest", "operator", "run.json.example")
}

// runJSONExampleRoot is the root that stands in for the example's /REPLACE paths. Linux treats
// /REPLACE as absolute. Windows needs a drive, so it gets the drive of the working directory, with a
// forward slash, because the SSH path check refuses backslashes, and the drive keeps it free of spaces.
func runJSONExampleRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if vol := filepath.VolumeName(cwd); vol != "" {
		return vol + "/REPLACE"
	}
	return "/REPLACE"
}

// runJSONCopy writes a copy of the example, with its /REPLACE paths rooted at runJSONExampleRoot
// and the edit applied to the decoded document, and returns the copy's path.
func runJSONCopy(t *testing.T, edit func(doc map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile(runJSONExampleFile())
	if err != nil {
		t.Fatal(err)
	}
	rooted := strings.ReplaceAll(string(raw), `"/REPLACE/`, `"`+runJSONExampleRoot(t)+`/`)
	var doc map[string]any
	if err := json.Unmarshal([]byte(rooted), &doc); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(doc)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runJSONRefusal is the refusal that the validators give a run.json without the owner's values. On
// Linux the example's /REPLACE paths are absolute, so the first refusal is the missing EVQ digest. On
// Windows they are not, so the first refusal is the path check that comes before it.
func runJSONRefusal() string {
	if runtime.GOOS == "windows" {
		return "must be an absolute path"
	}
	return "evq_results needs an absolute path and a sha256:<64 hex> digest"
}

// TestRunJSONExampleDecodesStrictly decodes the shipped example through the decode steps of
// loadConfig: the strict JSON check, then the strict decoder, which refuses an unknown member, a
// duplicate key or trailing data. validate is not run here, because the example is meant to be refused
// by it, and TestRunJSONExampleIsRefusedAsShipped pins that refusal. The 22 EVQ IDs and the admission
// policy digests are checked against the code and the mapping.
func TestRunJSONExampleDecodesStrictly(t *testing.T) {
	raw, err := os.ReadFile(runJSONExampleFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		t.Fatalf("the example is not strict JSON: %v", err)
	}
	var cfg runConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("the strict decoder refuses the example: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		t.Fatal("the example has trailing data")
	}

	mappingRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "infra", "conformance", "evq-mapping.json"))
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

// TestRunJSONExampleIsRefusedAsShipped loads the example as shipped, through the validation that
// plan runs. Its owner values are empty or zero, so it must be refused, and the refusal is named.
func TestRunJSONExampleIsRefusedAsShipped(t *testing.T) {
	_, err := loadConfig(runJSONExampleFile())
	if want := runJSONRefusal(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the shipped example was not refused with %q: %v", want, err)
	}
}

// TestRunJSONExampleIsRefusedWithPathsOnly fills in only the paths. The owner values stay empty, so
// the copy must be refused with the same named refusal.
func TestRunJSONExampleIsRefusedWithPathsOnly(t *testing.T) {
	_, err := loadConfig(runJSONCopy(t, nil))
	if want := "evq_results needs an absolute path and a sha256:<64 hex> digest"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the example with only the paths filled was not refused with %q: %v", want, err)
	}
}

// TestRunJSONExampleRefusesEmptyPins fills in the paths, the EVQ digest and the proc values, and
// leaves the receipt pins empty. ReceiptPins.Validate refuses an empty pin, and the example ships
// those pins empty, so the copy must be refused by name for the pins.
func TestRunJSONExampleRefusesEmptyPins(t *testing.T) {
	path := runJSONCopy(t, func(doc map[string]any) {
		doc["evq_results"].(map[string]any)["sha256"] = "sha256:" + strings.Repeat("0", 64)
		doc["ssh"].(map[string]any)["path"] = runJSONExampleRoot(t) + "/ssh.exe"
		proc := doc["proc"].(map[string]any)
		proc["cell_uid"], proc["cell_gid"], proc["monitor_seconds"], proc["drain_grace_seconds"] = 995, 986, 60, 10
	})
	_, err := loadConfig(path)
	if want := "receipt pins need profile_hash, containment_profile_digest and frozen_source_digest"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the copy with empty pins was not refused with %q: %v", want, err)
	}
}
