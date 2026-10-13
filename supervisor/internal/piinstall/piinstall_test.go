package piinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePiVersion(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		ok          bool
	}{
		{"0.87.1", "0.87.1", true}, {"v0.87.1", "0.87.1", true}, {"pi 0.87.1", "0.87.1", true}, {"unknown", "", false},
	} {
		got, ok := ParsePiVersion(tc.input)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("ParsePiVersion(%q) = %q,%v", tc.input, got, ok)
		}
	}
}

func TestDiscoverPiPinsVersionAndFingerprintsExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi")
	contents := []byte("pinned cli bytes")
	if err := os.WriteFile(path, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{ExplicitPi: path, Run: func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) != 1 || args[0] != "--version" {
			t.Fatalf("unexpected CLI args: %v", args)
		}
		return []byte("0.87.1\n"), nil
	}}
	got, err := DiscoverPi(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(contents)
	if got.Version != PinnedPiVersion || got.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("unexpected Pi fingerprint: %+v", got)
	}
}

func TestDiscoverPiRejectsOtherVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(path, []byte("cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := DiscoverPi(context.Background(), Options{ExplicitPi: path, Run: func(context.Context, string, string, ...string) ([]byte, error) { return []byte("1.0.0"), nil }})
	if err == nil {
		t.Fatal("accepted an unpinned Pi CLI")
	}
}

func TestDiscoverPiRequiresInstalledCLI(t *testing.T) {
	_, err := DiscoverPi(context.Background(), Options{LookPath: func(string) (string, error) { return "", errors.New("missing") }, Run: func(context.Context, string, string, ...string) ([]byte, error) { return nil, nil }})
	if err == nil {
		t.Fatal("missing CLI passed discovery")
	}
}

func TestGovernedRPCArgsUsePinnedIsolationFlags(t *testing.T) {
	extensionPath := filepath.Join(t.TempDir(), "tbound-cli-extension.ts")
	args, err := GovernedRPCArgs(extensionPath)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{"--mode rpc", "--no-session", "--no-extensions", "--no-builtin-tools", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--offline", "--extension " + extensionPath, "--provider tbound-go-broker", "--model tbound-go-broker/nvidia/nemotron-3.5-lightning:free"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("arguments %q omit %q", joined, required)
		}
	}
	if strings.Contains(joined, "--no-mcp") || strings.Contains(joined, "--print") {
		t.Fatalf("unsupported or unmediated route in arguments: %q", joined)
	}
	if _, err := GovernedRPCArgs("relative.ts"); err == nil {
		t.Fatal("relative extension path accepted")
	}
}
