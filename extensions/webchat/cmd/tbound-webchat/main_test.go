package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStartupBannerPrintsTheReactUrlFirst(t *testing.T) {
	lines := startupBanner("127.0.0.1:8791", nil)
	if len(lines) < 2 || lines[0] != "UI: http://127.0.0.1:8791/ (React app at /app/)" {
		t.Fatalf("banner = %q, want the React URL on the first line", lines)
	}
	if !strings.Contains(lines[1], "http://127.0.0.1:8791/legacy/") || !strings.Contains(lines[1], "deprecated") {
		t.Errorf("second banner line %q should name the deprecated /legacy/ URL", lines[1])
	}
	fallback := startupBanner("127.0.0.1:8791", errors.New("app manifest is missing"))
	if !strings.HasPrefix(fallback[0], "UI: http://127.0.0.1:8791/ (fallback") {
		t.Errorf("fallback banner first line = %q", fallback[0])
	}
}

func TestValidateLoopbackAddressRejectsRemoteAndNames(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8791", "192.168.1.20:8791", "localhost:8791", "127.0.0.1", ":8791"} {
		if err := validateLoopbackAddress(address); err == nil {
			t.Errorf("address %q should be refused", address)
		}
	}
	for _, address := range []string{"127.0.0.1:8791", "[::1]:8791"} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Errorf("address %q should be accepted: %v", address, err)
		}
	}
}

func TestSourceFlagsAreMutuallyExclusive(t *testing.T) {
	for _, args := range [][]string{
		{"--demo", "--replay", "x.json"},
		{"--receipt", "r.json", "--receipt-demo"},
		{"--receipt-demo", "--live"},
	} {
		if err := run(args, os.Stdout, os.Stderr); err == nil {
			t.Fatalf("args %v should be refused", args)
		}
	}
}

func TestLiveNeedsASocketAndSocketNeedsLive(t *testing.T) {
	for _, args := range [][]string{
		{"--live"},
		{"--socket", "/tmp/product.sock"},
		{"--demo", "--socket", "/tmp/product.sock"},
		{"--addr", "127.0.0.1:0", "--listen", "127.0.0.1:0"},
		{"--live", "--socket", "/tmp/product.sock", "--listen", "0.0.0.0:8791"},
	} {
		if err := run(args, os.Stdout, os.Stderr); err == nil {
			t.Errorf("args %v should be refused before anything is bound", args)
		}
	}
}

func TestLiveBannerCarriesThePairingUrlWithTheTokenInTheFragment(t *testing.T) {
	lines := strings.Join(liveBanner("127.0.0.1:8791", "/run/tbound-product/product.sock", "TOKEN"), " | ")
	for _, want := range []string{"http://127.0.0.1:8791/pair#TOKEN", "within 10 minutes", "/run/tbound-product/product.sock", "works once"} {
		if !strings.Contains(lines, want) {
			t.Errorf("live banner lacks %q in %s", want, lines)
		}
	}
}

func TestUnreadableFixtureIsRefusedBeforeListening(t *testing.T) {
	if err := run([]string{"--fixture", "does-not-exist.json"}, os.Stdout, os.Stderr); err == nil {
		t.Fatal("a missing fixture should fail closed")
	}
}
