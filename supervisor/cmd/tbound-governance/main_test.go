package main

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/governanceview"
)

func TestLoopbackAddressRejectsRemoteAndNames(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8790", "[::1]:8790"} {
		if !loopbackAddress(address) {
			t.Errorf("expected loopback address %q", address)
		}
	}
	for _, address := range []string{"0.0.0.0:8790", "192.0.2.1:8790", "localhost:8790", "127.0.0.1"} {
		if loopbackAddress(address) {
			t.Errorf("accepted non-literal loopback address %q", address)
		}
	}
}

func TestStartupBannerDoesNotExposePathsOrControls(t *testing.T) {
	var output strings.Builder
	writeStartupBanner(&output, "127.0.0.1:8790", governanceview.ModeUnavailable, 0)
	text := output.String()
	if strings.Contains(text, "--journal") {
		t.Error("startup banner exposed a source flag")
	}
	if !strings.Contains(text, "read-only") || !strings.Contains(text, "127.0.0.1:8790") {
		t.Fatalf("banner did not identify safe mode/listener: %q", text)
	}
}

func TestPathListRejectsEmptyEntries(t *testing.T) {
	var paths pathList
	if err := paths.Set("first.json,,third.json"); err == nil {
		t.Fatal("accepted empty registered source path")
	}
}

func TestLivePathInputsAreRefusedUntilSafeNoFollowTailerExists(t *testing.T) {
	var output strings.Builder
	err := run([]string{"--addr", "127.0.0.1:8799", "--journal", `C:\registered\audit.jsonl`}, &output)
	if err == nil || !strings.Contains(err.Error(), "no-follow") {
		t.Fatalf("unsafe live source was not refused: %v", err)
	}
}
