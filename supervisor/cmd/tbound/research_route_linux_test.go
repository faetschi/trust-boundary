//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/researchroute"
	"tbound/supervisor/internal/usdledger"
)

const researchRouteProfileJSON = `{
  "schema_version": 1,
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "deepseek/deepseek-v4.1-flash",
  "routing": {"order": ["example-provider"], "allow_fallbacks": false, "data_collection": "deny",
              "quantizations": [], "require_parameters": true, "zdr": true},
  "input_micro_usd_per_1m_tokens": 150000,
  "output_micro_usd_per_1m_tokens": 600000,
  "max_price_micro_usd_per_1m_tokens_in": 300000,
  "max_price_micro_usd_per_1m_tokens_out": 1200000,
  "max_input_tokens": 20000,
  "max_output_tokens": 2000,
  "max_requests_per_unit": 5,
  "temperature_milli": 0,
  "seed": 7,
  "reasoning": {"enabled": false},
  "timeout_ms": 5000,
  "frozen_at": "2026-10-17T00:00:00Z"
}`

const researchRouteLedgerConfigJSON = `{"schema_version":1,"ledger_id":"research-cmd-test","cap_micro_usd":5000000,"min_per_unit_worst_case_micro_usd":20000,"reserve_basis":"request"}`

func researchRoutePaths(t *testing.T) researchroute.Paths {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := researchroute.Paths{
		Profile: filepath.Join(dir, "research-profile.json"), LedgerConfig: filepath.Join(dir, "research-ledger.json"),
		Ledger: filepath.Join(dir, "usd-ledger"), Key: filepath.Join(dir, "openrouter.key"), OwnerUID: uint32(os.Getuid()),
	}
	for path, item := range map[string]struct {
		content string
		mode    os.FileMode
	}{
		paths.Profile:      {researchRouteProfileJSON, 0o600},
		paths.LedgerConfig: {researchRouteLedgerConfigJSON, 0o600},
		paths.Key:          {"cmd-research-test-key-123\n", 0o400},
	} {
		if err := os.WriteFile(path, []byte(item.content), item.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, item.mode); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func researchRouteGenesis(t *testing.T, paths researchroute.Paths) {
	t.Helper()
	if _, err := researchroute.Genesis(paths); err != nil {
		t.Fatalf("genesis: %v", err)
	}
}

func TestEngineeringRouteIsUnchangedAndTouchesNoLedgerOrResearchFile(t *testing.T) {
	dir := t.TempDir()
	// Every research path is absent. The engineering route must not even look.
	paths := researchroute.Paths{Profile: filepath.Join(dir, "p.json"), LedgerConfig: filepath.Join(dir, "l.json"),
		Ledger: filepath.Join(dir, "usd-ledger"), Key: filepath.Join(dir, "k"), OwnerUID: uint32(os.Getuid())}
	route, err := openProductionProviderRoute(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteEngineering, WorkflowMaxHostCalls: 4}, paths)
	if err != nil {
		t.Fatalf("engineering route refused: %v", err)
	}
	defer route.Close()
	if route.IsResearch() || route.Route() != (openrouter.Route{}) || route.Spend() != nil {
		t.Fatalf("engineering route carries research state: %+v", route.Route())
	}
	const host = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if route.ProviderProfileDigest(host) != host {
		t.Fatal("engineering route changed the host provider-profile digest")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("engineering route touched the filesystem: %v %v", entries, err)
	}
	var zero productionProviderRoute
	if zero.IsResearch() || zero.Spend() != nil || zero.Route() != (openrouter.Route{}) || zero.Close() != nil {
		t.Fatal("the zero route is not the engineering route")
	}
}

func TestEngineeringAuthorityKeepsTheFixedFactoryAndNoSpendHook(t *testing.T) {
	var a SessionOperationAuthority
	config := a.applyProviderRoute(providerbridge.Config{Route: openrouter.Route{Model: "attacker/model"}, Spend: nopSpend{}})
	if config.Route != (openrouter.Route{}) || config.Spend != nil {
		t.Fatalf("caller-supplied route or spend hook survived on an engineering authority: %+v", config.Route)
	}
	if a.providerFactory != nil {
		t.Fatal("engineering authority replaced the fixed service key-file factory")
	}
}

type nopSpend struct{ providerbridge.SpendAccountant }

func TestProviderRouteIsARequiredEnumInTheServiceConfig(t *testing.T) {
	paths := researchRoutePaths(t)
	for _, route := range []string{"", "Research", "openrouter", "opencode-go", "engineering,research"} {
		if _, err := openProductionProviderRoute(piServiceConfig{ProviderRoute: route, WorkflowMaxHostCalls: 4}, paths); err == nil {
			t.Errorf("provider_route %q accepted", route)
		}
	}
	if piServiceConfigSchema != piruntime.D09ManagedPiServiceConfigSchema {
		t.Fatalf("service config schemas differ: %q %q", piServiceConfigSchema, piruntime.D09ManagedPiServiceConfigSchema)
	}
	if piServiceConfigSchema != "tbound-pi-service-config/v3" {
		t.Fatalf("service config schemas differ or were not bumped: %q %q", piServiceConfigSchema, piruntime.D09ManagedPiServiceConfigSchema)
	}
}

func TestResearchRouteOpensWithAFrozenProfileKeyAndGenesisLedger(t *testing.T) {
	paths := researchRoutePaths(t)
	researchRouteGenesis(t, paths)
	route, err := openProductionProviderRoute(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteResearch, WorkflowMaxHostCalls: 4}, paths)
	if err != nil {
		t.Fatalf("research route refused: %v", err)
	}
	defer route.Close()
	if !route.IsResearch() || !route.Route().IsResearch() || route.Spend() == nil || route.Route().ProfileDigest == "" {
		t.Fatalf("research route is incomplete: %+v", route.Route())
	}
	const host = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if route.ProviderProfileDigest(host) == host {
		t.Fatal("the research profile digest is not bound into the provider-profile digest")
	}

	var a SessionOperationAuthority
	a.BindProviderRoute(route)
	config := a.applyProviderRoute(providerbridge.Config{Route: openrouter.Route{Model: "attacker/model"}})
	if config.Route != route.Route() || config.Spend == nil || a.providerFactory == nil {
		t.Fatalf("authority did not take the route, spend hook and key factory from the host: %+v", config.Route)
	}
}

func TestResearchRouteRefusesWithoutAGenesisLedger(t *testing.T) {
	paths := researchRoutePaths(t)
	_, err := openProductionProviderRoute(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteResearch, WorkflowMaxHostCalls: 4}, paths)
	if !errors.Is(err, usdledger.ErrMissingLedger) {
		t.Fatalf("research route started without a genesis ledger: %v", err)
	}
	if _, statErr := os.Lstat(paths.Ledger); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the service created the ledger itself: %v", statErr)
	}
}

func TestResearchRouteRefusesAMissingKeyAndAnUnfrozenProfile(t *testing.T) {
	paths := researchRoutePaths(t)
	researchRouteGenesis(t, paths)
	if err := os.Remove(paths.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := openProductionProviderRoute(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteResearch, WorkflowMaxHostCalls: 4}, paths); !errors.Is(err, researchroute.ErrKey) {
		t.Fatalf("missing key accepted: %v", err)
	}
	paths = researchRoutePaths(t)
	open := strings.Replace(researchRouteProfileJSON, `"seed": 7`, `"seed": null`, 1)
	if err := os.WriteFile(paths.Profile, []byte(open), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openProductionProviderRoute(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteResearch, WorkflowMaxHostCalls: 4}, paths); err == nil {
		t.Fatal("unfrozen profile accepted")
	}
}

func TestResearchRouteBoundsWorkflowHostCallsByTheProfileRequestsPerUnit(t *testing.T) {
	paths := researchRoutePaths(t)
	researchRouteGenesis(t, paths)
	// The profile allows five requests per unit; the durable workflow ledger
	// enforces workflow_max_host_calls, so a larger value must be refused.
	if _, err := openProductionProviderRoute(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteResearch, WorkflowMaxHostCalls: 6}, paths); err == nil {
		t.Fatal("workflow host-call limit above max_requests_per_unit accepted")
	}
	route, err := openProductionProviderRoute(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteResearch, WorkflowMaxHostCalls: 5}, paths)
	if err != nil {
		t.Fatalf("limit equal to max_requests_per_unit refused: %v", err)
	}
	_ = route.Close()
}

func TestResearchRoutePathsAreFixedAndWritableUnderTheUnchangedServiceUnit(t *testing.T) {
	defaults := researchroute.DefaultPaths()
	if defaults.Key != piruntime.D09ManagedPiResearchCredentialPath {
		t.Fatalf("research key path differs from the piruntime constant: %q", defaults.Key)
	}
	if defaults.Key != "/etc/tbound/provider/openrouter.key" ||
		defaults.Profile != "/etc/tbound/provider/research-profile.json" ||
		defaults.LedgerConfig != "/etc/tbound/provider/research-ledger.json" ||
		defaults.Ledger != "/var/lib/tbound/sessions/usd-ledger" || defaults.OwnerUID != 0 {
		t.Fatalf("fixed research paths drifted: %+v", defaults)
	}
	_, _, writable, _ := piServiceUnitFixture()
	covered := false
	for _, path := range writable {
		covered = covered || strings.HasPrefix(defaults.Ledger, path+"/")
	}
	if !covered {
		t.Fatalf("ledger path %s is outside the service ReadWritePaths %v", defaults.Ledger, writable)
	}
	template, err := os.ReadFile(filepath.Join("..", "..", "..", "infra", "guest", "systemd", "tbound-pi.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(template), "/var/lib/tbound/sessions") {
		t.Fatal("the committed service unit no longer grants /var/lib/tbound/sessions")
	}
}

// A changed ledger configuration (cap, stop rule, reserve basis) must change the
// provider-profile digest that the workflow runtime pin, recovery and replay
// compare, exactly as a changed research profile does.
func TestLedgerConfigChangeChangesTheProviderProfileDigestPin(t *testing.T) {
	paths := researchRoutePaths(t)
	researchRouteGenesis(t, paths)
	const host = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pin := func() string {
		route, err := openProductionProviderRoute(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteResearch, WorkflowMaxHostCalls: 4}, paths)
		if err != nil {
			t.Fatalf("research route refused: %v", err)
		}
		defer route.Close()
		return route.ProviderProfileDigest(host)
	}
	before := pin()
	if again := pin(); again != before {
		t.Fatal("the pin is not deterministic")
	}
	// min_per_unit is not stored in the ledger file, so the ledger still opens.
	changed := strings.Replace(researchRouteLedgerConfigJSON, `"min_per_unit_worst_case_micro_usd":20000`, `"min_per_unit_worst_case_micro_usd":25000`, 1)
	if changed == researchRouteLedgerConfigJSON {
		t.Fatal("fixture did not change")
	}
	if err := os.Remove(paths.LedgerConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.LedgerConfig, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if after := pin(); after == before {
		t.Fatal("a changed ledger configuration left the provider-profile digest pin unchanged")
	}
}
