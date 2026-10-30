//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/corpusadmit"
)

func TestConstructCommandRetainsFailedPilotReport(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pilot")
	var stdout, stderr bytes.Buffer
	err := run([]string{"corpus-check", "--manifest", "../../../corpus/pilot/manifest.draft.json", "--quota-profile", "pilot", "--out", out}, &stdout, &stderr)
	if err == nil {
		t.Fatal("missing node must fail the local gate")
	}
	raw, readErr := os.ReadFile(filepath.Join(out, "report.json"))
	if readErr != nil {
		t.Fatalf("report not retained: %v (command: %v)", readErr, err)
	}
	var report corpusadmit.ConstructReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Passed || len(report.Cases) != 33 {
		t.Fatalf("invalid report: %+v", report)
	}
	md, err := os.ReadFile(filepath.Join(out, "report.md"))
	if err != nil || !bytes.Contains(md, []byte("guest-only")) {
		t.Fatalf("missing markdown: %s (%v)", md, err)
	}
	if err := run([]string{"corpus-check", "--manifest", "../../../corpus/pilot/manifest.draft.json", "--quota-profile", "pilot", "--out", out}, &stdout, &stderr); err == nil {
		t.Fatal("existing report directory must be refused")
	}
}

func TestConstructCommandRetainsMalformedManifestReport(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(manifest, []byte(`{"schema_version":"wrong","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "report")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"corpus-check", "--manifest", manifest, "--out", out}, &stdout, &stderr); err == nil {
		t.Fatal("malformed manifest accepted")
	}
	raw, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report corpusadmit.ConstructReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.Summary["manifest-load"].Fail != 1 {
		t.Fatalf("invalid manifest failure report: %+v", report)
	}
}
