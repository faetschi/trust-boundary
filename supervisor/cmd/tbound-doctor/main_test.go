package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/piinstall"
)

func TestLandlockCheckRequiresExactD09ABI(t *testing.T) {
	for _, tc := range []struct {
		name, detail string
		abi          int
		err          error
		want         status
	}{
		{name: "exact", abi: 8, want: pass, detail: "kernel reports ABI 8 (matches the exact D09 requirement)"},
		{name: "older", abi: 4, want: warn, detail: "kernel reports ABI 4; D09 requires exactly ABI 8"},
		{name: "later", abi: 9, want: warn, detail: "kernel reports ABI 9; D09 requires exactly ABI 8"},
		{name: "unavailable", err: errors.New("not supported"), want: warn, detail: "Landlock ABI query failed: not supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := landlockCheckForABI(tc.abi, tc.err, 8)
			if got.Name != "landlock-abi" || got.Status != tc.want || got.Detail != tc.detail {
				t.Fatalf("unexpected Landlock check: %+v", got)
			}
			if tc.want == warn && got.Remediation == "" {
				t.Fatal("missing remediation for unavailable prerequisite")
			}
		})
	}
}

func cliOptions(path, version string) piinstall.Options {
	return piinstall.Options{ExplicitPi: path, Run: func(context.Context, string, string, ...string) ([]byte, error) { return []byte(version), nil }}
}

func TestBuildReportDevReadyWithPinnedPiCLI(t *testing.T) {
	prefix := t.TempDir()
	cli := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(cli, []byte("cli bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	rep := buildReport(context.Background(), "test", prefix, cliOptions(cli, piinstall.PinnedPiVersion))
	if !rep.DevReady {
		t.Fatalf("expected dev_ready with pinned CLI, got %+v", rep)
	}
	if rep.GovReady {
		t.Fatal("governed_ready must always be false (no admission result)")
	}
}

func TestBuildReportNotDevReadyWithoutPiCLI(t *testing.T) {
	prefix := t.TempDir()
	opts := piinstall.Options{Run: func(context.Context, string, string, ...string) ([]byte, error) { return nil, errors.New("missing") }, LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	rep := buildReport(context.Background(), "test", prefix, opts)
	if rep.DevReady {
		t.Fatalf("expected not dev-ready without Pi CLI, got %+v", rep)
	}
}
