package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/piinstall"
)

func fakeRun(version string) piinstall.Runner {
	return func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		if len(args) == 1 && args[0] == "--version" {
			return []byte(version), nil
		}
		return nil, errors.New("unexpected command")
	}
}

func TestBuildReportDevReadyWithNode(t *testing.T) {
	prefix := t.TempDir()
	opts := piinstall.Options{
		Prefix:       prefix,
		ExplicitNode: "/usr/bin/node",
		Run:          fakeRun("v24.15.0"),
		LookPath:     func(string) (string, error) { return "/usr/bin/node", nil },
	}
	rep := buildReport(context.Background(), "test", prefix, "", opts)
	if !rep.DevReady {
		t.Fatalf("expected dev_ready with a satisfying node, got %+v", rep)
	}
	if rep.GovReady {
		t.Fatal("governed_ready must always be false (no admission result)")
	}
}

func TestBuildReportNotDevReadyWithoutNode(t *testing.T) {
	prefix := t.TempDir()
	opts := piinstall.Options{
		Prefix: prefix,
		Run: func(context.Context, string, string, ...string) ([]byte, error) {
			return nil, errors.New("no node")
		},
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	}
	rep := buildReport(context.Background(), "test", prefix, "", opts)
	if rep.DevReady {
		t.Fatalf("expected not dev-ready without a node, got %+v", rep)
	}
}

func TestBuildReportDetectsPi(t *testing.T) {
	prefix := t.TempDir()
	piModules := piinstall.PrefixPIModules(prefix)
	pkgDir := filepath.Join(piModules, piinstall.PiCodingAgentPkg)
	if err := os.MkdirAll(pkgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"version":"`+piinstall.PinnedPiVersion+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := piinstall.Options{
		Prefix:       prefix,
		ExplicitNode: "/usr/bin/node",
		Run:          fakeRun("v24.15.0"),
		LookPath:     func(string) (string, error) { return "/usr/bin/node", nil },
	}
	rep := buildReport(context.Background(), "test", prefix, "", opts)
	for _, c := range rep.Checks {
		if c.Name == "pi" && c.Status != pass {
			t.Fatalf("expected pi check PASS, got %+v", c)
		}
	}
}
