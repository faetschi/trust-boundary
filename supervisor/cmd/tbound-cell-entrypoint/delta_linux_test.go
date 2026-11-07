//go:build linux

package main

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
)

func fakeEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func deltaEnv(t *testing.T, name string) map[string]string {
	t.Helper()
	base := piruntime.DigestBytes([]byte("signed base template"))
	env := map[string]string{"TBOUND_TEMPLATE_DIGEST": base}
	if name == "" {
		return env
	}
	delta, err := piruntime.ParseD09CellDelta(name)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := delta.TemplateDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	env["TBOUND_CELL_DELTA"], env["TBOUND_DELTA_TEMPLATE_DIGEST"] = name, digest
	if ports := delta.ConnectPortsText(); ports != "" {
		env["TBOUND_LANDLOCK_CONNECT_PORTS"] = ports
	}
	if delta.NoReap() {
		env["TBOUND_OUTPUT_END_MARKER"] = "0123456789abcdef0123456789abcdef"
	}
	return env
}

func TestCellDeltaFromEnvAcceptsOnlyWhatTheTemplateSays(t *testing.T) {
	for name, check := range map[string]func(cellDelta) bool{
		"": func(c cellDelta) bool { return !c.delta.Relaxed() && c.endMarker == "" },
		piruntime.D09CellDeltaNetSink: func(c cellDelta) bool {
			return c.delta.NetSink() && len(c.delta.ConnectPorts()) == 1 && c.delta.ConnectPorts()[0] == 8080
		},
		piruntime.D09CellDeltaNoReap: func(c cellDelta) bool {
			return c.delta.NoReap() && c.endMarker == piruntime.D09OutputEndMarker("0123456789abcdef0123456789abcdef")
		},
	} {
		got, err := cellDeltaFromEnv(fakeEnv(deltaEnv(t, name)), true)
		if err != nil || !check(got) {
			t.Errorf("delta %q: %+v %v", name, got, err)
		}
	}

	refuse := func(label string, commandMode bool, base string, mutate func(map[string]string)) {
		t.Helper()
		env := deltaEnv(t, base)
		mutate(env)
		if got, err := cellDeltaFromEnv(fakeEnv(env), commandMode); err == nil {
			t.Errorf("%s was accepted: %+v", label, got)
		}
	}
	// The connect right exists only for net-sink, and only for port 8080.
	refuse("connect ports on the base cell", true, "", func(e map[string]string) { e["TBOUND_LANDLOCK_CONNECT_PORTS"] = "8080" })
	refuse("connect ports on no-reap", true, piruntime.D09CellDeltaNoReap, func(e map[string]string) { e["TBOUND_LANDLOCK_CONNECT_PORTS"] = "8080" })
	for _, ports := range []string{"", "80", "8081", "8080,8081", "8080,22", "0", " 8080", "8080 ", "65535"} {
		refuse("net-sink with ports "+ports, true, piruntime.D09CellDeltaNetSink, func(e map[string]string) { e["TBOUND_LANDLOCK_CONNECT_PORTS"] = ports })
	}
	refuse("net-sink without the ports variable", true, piruntime.D09CellDeltaNetSink, func(e map[string]string) { delete(e, "TBOUND_LANDLOCK_CONNECT_PORTS") })
	// The delta is bound to the signed base template through the derived digest.
	refuse("a forged delta digest", true, piruntime.D09CellDeltaNetSink, func(e map[string]string) { e["TBOUND_DELTA_TEMPLATE_DIGEST"] = piruntime.DigestBytes([]byte("forged")) })
	refuse("a delta digest of another delta", true, piruntime.D09CellDeltaNetSink, func(e map[string]string) {
		e["TBOUND_DELTA_TEMPLATE_DIGEST"] = deltaEnv(t, piruntime.D09CellDeltaNoReap)["TBOUND_DELTA_TEMPLATE_DIGEST"]
	})
	refuse("a delta digest on the base cell", true, "", func(e map[string]string) { e["TBOUND_DELTA_TEMPLATE_DIGEST"] = piruntime.DigestBytes([]byte("x")) })
	refuse("an unknown delta", true, "", func(e map[string]string) { e["TBOUND_CELL_DELTA"] = "admit-all/v1" })
	refuse("a delta name with other case", true, piruntime.D09CellDeltaNetSink, func(e map[string]string) { e["TBOUND_CELL_DELTA"] = "NET-SINK/V1" })
	// A delta is a command-cell property; the Pi worker cell never takes one.
	refuse("a delta in the worker cell", false, piruntime.D09CellDeltaNetSink, func(map[string]string) {})
	refuse("a marker in the worker cell", false, "", func(e map[string]string) { e["TBOUND_OUTPUT_END_MARKER"] = "0123456789abcdef0123456789abcdef" })
	// The end-of-output marker belongs to no-reap alone and has the registered shape.
	refuse("a marker on the base cell", true, "", func(e map[string]string) { e["TBOUND_OUTPUT_END_MARKER"] = "0123456789abcdef0123456789abcdef" })
	refuse("a marker on net-sink", true, piruntime.D09CellDeltaNetSink, func(e map[string]string) { e["TBOUND_OUTPUT_END_MARKER"] = "0123456789abcdef0123456789abcdef" })
	refuse("no-reap without a marker", true, piruntime.D09CellDeltaNoReap, func(e map[string]string) { delete(e, "TBOUND_OUTPUT_END_MARKER") })
	for _, marker := range []string{"short", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcde\n", strings.Repeat("a", 64)} {
		refuse("no-reap marker "+marker, true, piruntime.D09CellDeltaNoReap, func(e map[string]string) { e["TBOUND_OUTPUT_END_MARKER"] = marker })
	}
}

func TestLandlockPlanGrantsTheConnectRightOnlyToNetSinkAndOnlyForItsPort(t *testing.T) {
	ro, rw := []string{"/usr"}, []string{"/work"}
	for _, name := range []string{"", piruntime.D09CellDeltaNoReap} {
		delta, _ := piruntime.ParseD09CellDelta(name)
		plan := planLandlock(delta, ro, rw)
		if len(plan.connectPorts) != 0 || plan.scoped != 0 || plan.handledNet != piruntime.D09LandlockHandledNet || len(plan.pathRules) != 2 {
			t.Errorf("delta %q must keep the deny-all network ruleset: %+v", name, plan)
		}
	}
	netSink, _ := piruntime.ParseD09CellDelta(piruntime.D09CellDeltaNetSink)
	plan := planLandlock(netSink, ro, rw)
	if len(plan.connectPorts) != 1 || plan.connectPorts[0] != 8080 {
		t.Fatalf("net-sink must allow exactly port 8080: %+v", plan)
	}
	if plan.handledNet&piruntime.D09LandlockNetBindTCP == 0 || plan.handledNet&piruntime.D09LandlockNetConnectTCP == 0 {
		t.Errorf("bind and connect must both stay handled so that everything not allowed is denied: %b", plan.handledNet)
	}
	if plan.scoped != piruntime.D09LandlockScopeAbstractUnix {
		t.Errorf("the host-network cell must scope abstract unix sockets: %b", plan.scoped)
	}
	if got, want := plan.rulesetDigest(ro, rw), mustDigest(t, netSink, ro, rw); got != want {
		t.Errorf("the applied ruleset digest %s differs from the template-derived digest %s", got, want)
	}
}

func mustDigest(t *testing.T, delta piruntime.D09CellDelta, ro, rw []string) string {
	t.Helper()
	digest, err := delta.LandlockRulesetDigest(ro, rw)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestNamespaceMeasurementSharesTheNetworkNamespaceOnlyForNetSink(t *testing.T) {
	if !networkIsolationRequired(piruntime.D09CellDelta{}) {
		t.Error("the base cell must keep its private network namespace")
	}
	noReap, _ := piruntime.ParseD09CellDelta(piruntime.D09CellDeltaNoReap)
	netSink, _ := piruntime.ParseD09CellDelta(piruntime.D09CellDeltaNetSink)
	if !networkIsolationRequired(noReap) || networkIsolationRequired(netSink) {
		t.Error("only net-sink may share the host network namespace")
	}
}

// rulesetDigest is the digest the host expects in the proof, derived by the same function.
func (p landlockPlan) rulesetDigest(roPaths, rwPaths []string) string {
	digest, err := p.delta.LandlockRulesetDigest(roPaths, rwPaths)
	if err != nil {
		return ""
	}
	return digest
}
