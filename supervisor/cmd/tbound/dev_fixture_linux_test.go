//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// TestServePiProductionRefusalUnchanged proves the plain production route and
// the explicit host-profile route still refuse exactly as before the
// development fixture flag was added.
func TestServePiProductionRefusalUnchanged(t *testing.T) {
	for _, testCase := range []struct {
		args []string
		want string
	}{
		{[]string{"serve", "--pi"}, "refused"},
		{[]string{"serve", "--pi", "--native-host-profile", "/no/such/attested/profile"}, "not an installed, attested production runtime"},
	} {
		var transcript bytes.Buffer
		err := run(testCase.args, &transcript, io.Discard)
		if err == nil {
			t.Fatalf("%v did not refuse production launch", testCase.args)
		}
		if !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("%v refusal = %q, want it to contain %q", testCase.args, err, testCase.want)
		}
		if transcript.Len() != 0 {
			t.Fatalf("%v wrote a receipt while refusing: %q", testCase.args, transcript.String())
		}
	}
}

// TestServePiDevFixtureIsMutuallyExclusive guards against an ambiguous
// combination of the two non-claim-bearing fixtures.
func TestServePiDevFixtureIsMutuallyExclusive(t *testing.T) {
	err := run([]string{"serve", "--pi", "--dev-fixture", "--native-fixture"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("ambiguous fixture combination = %v", err)
	}
}

// TestServePiDevFixtureRequiresExplicitEnvironment proves the development route
// is opt-in and never silently falls back to the Go-only fixture or the
// production refusal. With the explicit paths absent it must name the missing
// opt-in variables and produce no receipt.
func TestServePiDevFixtureRequiresExplicitEnvironment(t *testing.T) {
	for _, name := range []string{devFixtureNodeEnv, devFixtureAdapterEnv, devFixtureNodeModulesEnv, devFixtureRootEnv} {
		t.Setenv(name, "")
	}
	var transcript bytes.Buffer
	err := run([]string{"serve", "--pi", "--dev-fixture"}, &transcript, io.Discard)
	if err == nil {
		t.Fatal("development fixture launched without its explicit opt-in environment")
	}
	if !strings.Contains(err.Error(), "opt-in") {
		t.Fatalf("development fixture refusal = %q, want an explicit opt-in refusal", err)
	}
	if transcript.Len() != 0 {
		t.Fatalf("development fixture wrote a receipt without opt-in: %q", transcript.String())
	}
}

// TestServePiDevFixtureLaunchesActualPinnedPiOffline is opt-in. It runs the
// actual pinned Pi SDK worker through the explicit development composition and
// asserts the bounded, non-claim-bearing receipt with the observed four-tool
// lineage. It is skipped unless the same explicit Linux Node/pinned-dependency/
// private-ext4 environment the opt-in process tests use is provided.
func TestServePiDevFixtureLaunchesActualPinnedPiOffline(t *testing.T) {
	for _, name := range []string{devFixtureNodeEnv, devFixtureAdapterEnv, devFixtureNodeModulesEnv, devFixtureRootEnv} {
		if os.Getenv(name) == "" {
			t.Skipf("set %s/%s/%s/%s to run the actual offline development Pi fixture",
				devFixtureNodeEnv, devFixtureAdapterEnv, devFixtureNodeModulesEnv, devFixtureRootEnv)
		}
	}
	var transcript bytes.Buffer
	err := run([]string{"serve", "--pi", "--dev-fixture"}, &transcript, io.Discard)
	if err != nil {
		t.Fatalf("development fixture launch failed: %v\nreceipt=%s", err, transcript.String())
	}
	var receipt developmentFixtureReceipt
	if err := json.Unmarshal(bytes.TrimSpace(transcript.Bytes()), &receipt); err != nil {
		t.Fatalf("decode development fixture receipt: %v; receipt=%q", err, transcript.String())
	}
	if receipt.Mode != "dev-fixture" || receipt.ClaimBearing || receipt.ProviderExchange ||
		receipt.Containment != "not-established" || receipt.Settlement != "UNKNOWN" {
		t.Fatalf("development receipt lost its claim boundary: %+v", receipt)
	}
	if !receipt.WorkerReady || !receipt.PromptAdmitted || !receipt.TurnCompleted {
		t.Fatalf("development receipt lost lifecycle evidence: %+v", receipt)
	}
	if receipt.ToolCallCount != 4 || receipt.ToolResultCount != 4 || receipt.ProposalCount != 4 {
		t.Fatalf("development receipt lineage count mismatch: %+v", receipt)
	}
	if len(receipt.ToolLineage) != 4 || len(receipt.Generations) < 3 {
		t.Fatalf("development receipt lineage is incomplete: %+v", receipt)
	}
	wantTools := []string{"read", "edit", "write", "read"}
	for index, want := range wantTools {
		if receipt.ToolLineage[index].Tool != want {
			t.Fatalf("development lineage[%d].tool = %q, want %q", index, receipt.ToolLineage[index].Tool, want)
		}
	}
}
