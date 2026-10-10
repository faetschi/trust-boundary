package piinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNodeVersion(t *testing.T) {
	cases := []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"v24.15.0", 24, 15, true},
		{"22.19.1", 22, 19, true},
		{"v20.11.0\n", 20, 11, true},
		{"garbage", 0, 0, false},
	}
	for _, c := range cases {
		major, minor, ok := ParseNodeVersion(c.in)
		if ok != c.ok || major != c.major || minor != c.minor {
			t.Fatalf("ParseNodeVersion(%q) = %d,%d,%v; want %d,%d,%v", c.in, major, minor, ok, c.major, c.minor, c.ok)
		}
	}
}

func TestNodeSatisfies(t *testing.T) {
	if !(NodeInfo{Major: 22, Minor: 19}).Satisfies() {
		t.Fatal("22.19 should satisfy")
	}
	if (NodeInfo{Major: 22, Minor: 18}).Satisfies() {
		t.Fatal("22.18 should not satisfy")
	}
	if !(NodeInfo{Major: 24, Minor: 0}).Satisfies() {
		t.Fatal("24.0 should satisfy")
	}
}

func TestDiscoverNodePrefersExplicit(t *testing.T) {
	o := Options{
		Run: func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
			if name == "/opt/node/bin/node" {
				return []byte("v24.15.0\n"), nil
			}
			return nil, errors.New("unexpected node " + name)
		},
		LookPath:     func(string) (string, error) { return "/usr/bin/node", nil },
		ExplicitNode: "/opt/node/bin/node",
	}
	n, err := DiscoverNode(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if n.Path != "/opt/node/bin/node" || n.Source != "explicit" || !n.Satisfies() {
		t.Fatalf("got %+v", n)
	}
}

func TestDiscoverNodeSkipsBrokenCandidate(t *testing.T) {
	o := Options{
		Run: func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
			if name == "/broken/node" {
				return nil, errors.New("not executable")
			}
			return []byte("v22.19.0"), nil
		},
		LookPath:     func(string) (string, error) { return "/usr/bin/node", nil },
		ExplicitNode: "/broken/node",
	}
	n, err := DiscoverNode(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if n.Path != "/usr/bin/node" || n.Source != "path" {
		t.Fatalf("got %+v", n)
	}
}

func TestDiscoverNodeNoneFound(t *testing.T) {
	o := Options{
		Run:      func(context.Context, string, string, ...string) ([]byte, error) { return nil, errors.New("no") },
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	}
	if _, err := DiscoverNode(context.Background(), o); err == nil {
		t.Fatal("expected error when no node is found")
	}
}

func TestDetectNodeModulesPi(t *testing.T) {
	dir := t.TempDir()
	modules := filepath.Join(dir, "node_modules")
	pkgDir := filepath.Join(modules, PiCodingAgentPkg)
	if err := os.MkdirAll(pkgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"`+PiCodingAgentPkg+`","version":"0.87.1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, ok := DetectNodeModulesPi(modules); !ok || v != "0.87.1" {
		t.Fatalf("DetectNodeModulesPi = %q,%v", v, ok)
	}
	if _, ok := DetectNodeModulesPi(filepath.Join(dir, "missing")); ok {
		t.Fatal("missing modules should not be detected")
	}
}

func TestInstallPinnedPackagesRequiresNetworkOptIn(t *testing.T) {
	err := InstallPinnedPackages(context.Background(), Options{Prefix: t.TempDir()}, false)
	if !errors.Is(err, ErrNetworkRequired) {
		t.Fatalf("want ErrNetworkRequired, got %v", err)
	}
}

func TestInstallWritesLowercaseManifest(t *testing.T) {
	prefix := t.TempDir()
	var manifest string
	o := Options{
		Prefix:       prefix,
		ExplicitNode: "/usr/bin/node",
		Run: func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
			if len(args) == 1 && args[0] == "--version" {
				return []byte("v24.15.0"), nil
			}
			b, _ := os.ReadFile(filepath.Join(prefix, "pi", "package.json"))
			manifest = string(b)
			pkgDir := filepath.Join(PrefixPIModules(prefix), PiCodingAgentPkg)
			if err := os.MkdirAll(pkgDir, 0o700); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"version":"0.87.1"}`), 0o600)
		},
	}
	if err := InstallPinnedPackages(context.Background(), o, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(manifest, `"Name"`) || strings.Contains(manifest, `"Dependencies"`) {
		t.Fatalf("manifest uses non-npm (uppercase) keys: %s", manifest)
	}
	if !strings.Contains(manifest, `"dependencies"`) || !strings.Contains(manifest, `"name"`) {
		t.Fatalf("manifest missing lowercase npm fields: %s", manifest)
	}
}

func TestInstallPinnedPackagesVerifiesVersion(t *testing.T) {
	prefix := t.TempDir()
	node := filepath.Join(prefix, "node", "bin", "node")
	o := Options{
		Prefix:       prefix,
		ExplicitNode: node,
		Run: func(_ context.Context, dir string, name string, args ...string) ([]byte, error) {
			if len(args) == 1 && args[0] == "--version" {
				return []byte("v24.15.0"), nil
			}
			// Simulate npm creating the pinned package.
			pkgDir := filepath.Join(PrefixPIModules(prefix), PiCodingAgentPkg)
			if err := os.MkdirAll(pkgDir, 0o700); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"version":"0.87.1"}`), 0o600)
		},
	}
	if err := InstallPinnedPackages(context.Background(), o, true); err != nil {
		t.Fatal(err)
	}
}
