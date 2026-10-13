package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/piinstall"
)

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
