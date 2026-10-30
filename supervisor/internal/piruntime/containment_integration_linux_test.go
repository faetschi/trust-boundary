//go:build linux && d09integration

package piruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRealD09Prerequisites is the opt-in, real-host D09 prerequisite test.
//
// Opt-in semantics: the test skips ONLY when TBOUND_D09_INTEGRATION is unset.
// Once it is set, any missing or invalid prerequisite (registered template,
// operator template, podman/crun/cosign, pinned local image, local cosign
// verification) is a FAILURE, never a skip. It never mocks: it probes the real
// host and verifies the real local image.
//
// Enable with:
//
//	TBOUND_D09_INTEGRATION=1 go test -tags d09integration -run TestRealD09Prerequisites ./internal/piruntime
//
// Optional: TBOUND_D09_TEMPLATE selects an operator-supplied measured template
// (otherwise the registered /etc/tbound/d09-cell.json is required).
//
// A full worker launch additionally needs the sealed session composition (sealed
// D06 exposure, runtime bundle, descriptor role mapping) supplied by the CLI
// composition owner; that path is not exercised here and is a known code
// blocker, not a mocked pass.
func TestRealD09Prerequisites(t *testing.T) {
	if os.Getenv("TBOUND_D09_INTEGRATION") == "" {
		t.Skip("TBOUND_D09_INTEGRATION is not set; opt-in integration test not requested")
	}

	var config D09Config
	if templatePath := os.Getenv("TBOUND_D09_TEMPLATE"); templatePath != "" {
		raw, err := os.ReadFile(templatePath)
		if err != nil {
			t.Fatalf("opted in but could not read D09 template %q: %v", templatePath, err)
		}
		template, err := DecodeD09CellTemplate(raw)
		if err != nil {
			t.Fatalf("opted in but the operator D09 template is invalid: %v", err)
		}
		config = template.Config()
	} else {
		template, _, err := LoadD09CellTemplate(context.Background())
		if err != nil {
			t.Fatalf("opted in but the registered D09 template is unavailable: %v", err)
		}
		config = template.Config()
	}

	launcher := NewD09PodmanCrunLauncher(config)
	report, err := launcher.Probe(context.Background())
	if err != nil {
		t.Fatalf("opted in but D09 prerequisites are not satisfied: %v", err)
	}
	if !report.PodmanPresent || !report.CrunPresent || !report.CosignPresent || !report.ImageVerified {
		t.Fatalf("opted in but the measured D09 report is incomplete: %+v", report)
	}
	t.Logf("real D09 prerequisites verified: podman=%s runtime=%s cgroup=%s image=%s",
		report.PodmanVersion, report.OCIRuntime, report.CgroupVersion, report.ImageRef)
}

// TestRealD09SignatureIsNotAcceptsArbitraryTemplate proves the registered
// loader distinguishes a present-but-invalid template from an absent one.
func TestRealD09SignatureIsNotAcceptsArbitraryTemplate(t *testing.T) {
	if os.Getenv("TBOUND_D09_INTEGRATION") == "" {
		t.Skip("TBOUND_D09_INTEGRATION is not set; opt-in integration test not requested")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "d09-cell.json")
	if err := os.WriteFile(bad, []byte(`{"image":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeD09CellTemplate([]byte(`{"image":"x"}`)); err == nil {
		t.Fatal("present-but-invalid template was accepted")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(`{"image":"x"}`), &decoded); err != nil {
		t.Fatal(err)
	}
}
