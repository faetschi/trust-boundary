//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productprovider"
)

const (
	// productQualificationDir holds each start's disposable qualification cell, beside the turns in the state root.
	productQualificationDir = "qualification"
	// productCommandQualificationTimeout bounds one start's command host load, scope gate and qualification.
	productCommandQualificationTimeout = 5 * time.Minute
	// productNetworkPolicyDenyAll is the only egress expectation the network probe knows.
	productNetworkPolicyDenyAll = "deny-all"
)

// productQualificationPhase names one fixed check that a fresh command cell must pass before Bash is admitted.
type productQualificationPhase string

const (
	productPhaseProofAck      productQualificationPhase = "proof-ack"
	productPhaseListing       productQualificationPhase = "ls"
	productPhaseScopedWrite   productQualificationPhase = "scoped-write-readback"
	productPhaseRootfsDenial  productQualificationPhase = "rootfs-denial"
	productPhaseLimitsMapping productQualificationPhase = "limits-mapping"
	productPhaseSettlement    productQualificationPhase = "settlement"
	productPhaseNetworkProbe  productQualificationPhase = "network-probe"
)

// productQualificationPhases is the order a start runs the phases in. The first failure ends the start. Settlement is
// last because it drains the cell that every earlier phase ran in.
var productQualificationPhases = []productQualificationPhase{productPhaseProofAck, productPhaseListing, productPhaseScopedWrite,
	productPhaseRootfsDenial, productPhaseLimitsMapping, productPhaseNetworkProbe, productPhaseSettlement}

type productCommandRunnerFactory func(productTurnCommandContext) (productTurnCommandRunner, error)

// productCommandQualificationSteps are the host operations a start performs before it admits Bash. Production binds
// the real ones in productCommandQualification. A test replaces one step to fail the start at that step.
type productCommandQualificationSteps struct {
	loadHost        func(ctx context.Context, cfg productServiceConfig, env *productTurnEnv) (*piruntime.D09ProductCommandHost, error)
	prepareRuntime  func() error
	reconcileScopes func(ctx context.Context, host *piruntime.D09ProductCommandHost, retainedRoots []string) error
	// openCell builds the disposable cell over newRunner, the factory the turns will use after qualification.
	openCell  func(ctx context.Context, host *piruntime.D09ProductCommandHost, env *productTurnEnv, newRunner productCommandRunnerFactory) (productQualificationCell, error)
	newRunner func(ctx context.Context, host *piruntime.D09ProductCommandHost, c productTurnCommandContext) (productTurnCommandRunner, error)
}

var productCommandQualification = productCommandQualificationSteps{
	loadHost:       loadProductCommandHost,
	prepareRuntime: func() error { return prepareProductCommandRuntime("/", os.Geteuid(), os.Getegid()) },
	reconcileScopes: func(ctx context.Context, host *piruntime.D09ProductCommandHost, retainedRoots []string) error {
		return reconcileProductCommandScopes(ctx, host, retainedRoots...)
	},
	openCell: func(ctx context.Context, host *piruntime.D09ProductCommandHost, env *productTurnEnv, newRunner productCommandRunnerFactory) (productQualificationCell, error) {
		hostname, err := productQualificationProviderHostname(env)
		if err != nil {
			return nil, err
		}
		return openProductQualificationCell(ctx, productQualificationTarget{Profile: host.Profile(), Template: host.Template(), ProviderHostname: hostname}, env, newRunner)
	},
	newRunner: func(ctx context.Context, host *piruntime.D09ProductCommandHost, c productTurnCommandContext) (productTurnCommandRunner, error) {
		runner, err := newProductD09CommandRunner(ctx, host, c)
		if runner == nil {
			return nil, err
		}
		// A runner that already owns a scope comes back with its error, so its caller can settle and drain it.
		return runner, err
	},
}

// productCommandHooks are the two hooks a Bash turn needs. Only a passing qualification returns the factory.
type productCommandHooks struct {
	NewCommandRunner   func(productTurnCommandContext) (productTurnCommandRunner, error)
	OpenCommandHistory productCommandHistoryOpener
}

// qualifyProductCommandHost is the only source of the Bash hooks. A deny-only host with command journals still
// loads the signed host and runs the scope gate, and gets the read-only history opener but no factory. A policy that
// allows Bash gets the factory only after every phase passed in this process.
func qualifyProductCommandHost(ctx context.Context, cfg productServiceConfig, env *productTurnEnv, bashAllowed bool) (productCommandHooks, error) {
	retainedRoots := []string{filepath.Join(env.StateRoot, "turns"), filepath.Join(env.StateRoot, productQualificationDir)}
	hooks := productCommandHooks{OpenCommandHistory: openProductD09CommandHistory}
	if !bashAllowed {
		present, err := productCommandOwnerJournalsPresent(ctx, retainedRoots)
		if err != nil {
			return productCommandHooks{}, fmt.Errorf("command qualification: %w", err)
		}
		if !present {
			return hooks, nil
		}
	}
	steps := productCommandQualification
	host, err := steps.loadHost(ctx, cfg, env)
	if err != nil {
		return productCommandHooks{}, fmt.Errorf("command qualification: load the signed product command host: %w", err)
	}
	if bashAllowed {
		if err := steps.prepareRuntime(); err != nil {
			return productCommandHooks{}, fmt.Errorf("command qualification: prepare the command runtime: %w", err)
		}
	}
	if err := steps.reconcileScopes(ctx, host, retainedRoots); err != nil {
		return productCommandHooks{}, fmt.Errorf("command qualification: reconcile retained command scopes: %w", err)
	}
	if !bashAllowed {
		return hooks, nil
	}
	// The factory serves turns after startup, so it does not hold the start's context.
	factory := func(c productTurnCommandContext) (productTurnCommandRunner, error) {
		return steps.newRunner(context.Background(), host, c)
	}
	cell, err := steps.openCell(ctx, host, env, factory)
	if err != nil {
		return productCommandHooks{}, fmt.Errorf("command qualification: open the disposable command cell: %w", err)
	}
	for _, phase := range productQualificationPhases {
		if err := cell.runPhase(ctx, phase); err != nil {
			failure := fmt.Errorf("command qualification: phase %s: %w", phase, err)
			return productCommandHooks{}, errors.Join(failure, cell.finish(ctx, failure))
		}
	}
	if err := cell.finish(ctx, nil); err != nil {
		return productCommandHooks{}, fmt.Errorf("command qualification: close the disposable command cell: %w", err)
	}
	hooks.NewCommandRunner = factory
	return hooks, nil
}

// productCommandOwnerJournalsPresent reports whether a retained root holds any command owner journal, empty or not.
// A journal is created before its cell, so an empty one can mark a cell that a crash left behind.
func productCommandOwnerJournalsPresent(ctx context.Context, roots []string) (bool, error) {
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if !entry.IsDir() {
				continue
			}
			_, err := os.Lstat(filepath.Join(root, entry.Name(), productCommandOwnerJournal))
			if err == nil {
				return true, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
		}
	}
	return false, nil
}

// productNetworkRoute is one routing-table row the probe read inside the cell.
type productNetworkRoute struct {
	Family string `json:"family"`
	Iface  string `json:"iface"`
}

// productNetworkCheck is one attempted destination with its concrete outcome: an errno, or the route rows.
type productNetworkCheck struct {
	ID          string `json:"id"`
	Destination string `json:"destination"`
	Outcome     string `json:"outcome"`
	Errno       string `json:"errno,omitempty"`
}

// productNetworkReport is the structured result of one probe run. Policy is the expectation it was checked against.
type productNetworkReport struct {
	Policy string                `json:"policy,omitempty"`
	Node   string                `json:"node"`
	Routes []productNetworkRoute `json:"routes"`
	Checks []productNetworkCheck `json:"checks"`
	// Controls are the service-side positive controls. The service fills them and discards whatever the cell prints.
	Controls   []productNetworkCheck `json:"controls,omitempty"`
	Interfaces []string              `json:"interfaces"`
	IPv6       string                `json:"ipv6"`
}

type productNetworkDestination struct{ Hostname, IPv4, IPv6 string }
type productNetworkConnect func(ctx context.Context, network, address string) error
type productNetworkResolve func(context.Context, string) ([]net.IPAddr, error)

func productQualificationProviderHostname(env *productTurnEnv) (string, error) {
	if env == nil || len(env.Registration.ProviderProfiles) == 0 {
		return "", errors.New("qualification needs a registered provider endpoint")
	}
	profile, ok := productprovider.Lookup(env.Registration.ProviderProfiles[0])
	endpoint, err := url.Parse(profile.Endpoint)
	if !ok || err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.Port() != "" && endpoint.Port() != "443" {
		return "", errors.New("registered provider needs an HTTPS endpoint on port 443")
	}
	return endpoint.Hostname(), nil
}

// productNetworkCellRun runs one fixed program through the signed image's Node in the base cell and returns its stdout.
type productNetworkCellRun func(ctx context.Context, program string) ([]byte, error)

// productNetworkControl dials a listener that qualification opened on the service host, from the service context.
type productNetworkControl func(ctx context.Context, address string) error

// DNS accepts only transport refusal/unreachability from resolve4: negative answers and temporary/timeout failures
// can also come from a working resolver. Numeric provider TCP must fail with network/host unreachability.
var productDenyAllNetworkErrnos = map[string][]string{
	"name-resolution": {"ECONNREFUSED", "ENETUNREACH", "EHOSTUNREACH"},
	"ipv4-connect":    {"ENETUNREACH", "EHOSTUNREACH"},
	"ipv6-connect":    {"ENETUNREACH", "EHOSTUNREACH"},
	"host-listener":   {"ENETUNREACH", "EHOSTUNREACH", "ECONNREFUSED", "EPERM", "EACCES"},
}

// productNetworkProbe proves the cell's network policy from inside the cell, against an outside control. The control
// dials the host listener first, so a dead network cannot pass as containment. Only deny-all exists in this step.
func productNetworkProbe(ctx context.Context, policy, listener, hostname string, run productNetworkCellRun, control productNetworkControl,
	connect productNetworkConnect, resolve productNetworkResolve) (productNetworkReport, error) {
	if policy != productNetworkPolicyDenyAll {
		return productNetworkReport{}, fmt.Errorf("the network probe has no expectation for policy %q", policy)
	}
	controls, destination, err := productNetworkControls(ctx, listener, hostname, control, connect, resolve)
	if err != nil {
		return productNetworkReport{Policy: policy, Controls: controls}, err
	}
	output, err := run(ctx, productNetworkProgram(listener, destination))
	if err != nil {
		return productNetworkReport{Policy: policy, Controls: controls}, fmt.Errorf("the cell did not run the network probe: %w", err)
	}
	var report productNetworkReport
	if err := json.Unmarshal(output, &report); err != nil {
		return productNetworkReport{Policy: policy, Controls: controls}, fmt.Errorf("the network probe printed no structured result: %w", err)
	}
	report.Policy, report.Controls = policy, controls
	report.IPv6 = "reachable on host"
	if destination.IPv6 == "" {
		report.IPv6 = "not available on host"
	}
	return report, verifyProductDenyAllNetwork(report)
}

// productNetworkControls resolves the registered provider and proves actual TCP reachability to the same numeric destinations.
func productNetworkControls(ctx context.Context, listener, hostname string, control productNetworkControl, connect productNetworkConnect, resolve productNetworkResolve) ([]productNetworkCheck, productNetworkDestination, error) {
	destination := productNetworkDestination{Hostname: hostname}
	var controls []productNetworkCheck
	if err := control(ctx, listener); err != nil {
		return controls, destination, fmt.Errorf("positive control: host listener: %w", err)
	}
	controls = append(controls, productNetworkCheck{ID: "host-listener", Destination: listener, Outcome: "reachable"})
	addresses, err := resolve(ctx, hostname)
	if err != nil || len(addresses) == 0 {
		return controls, destination, fmt.Errorf("positive control: registered provider DNS failed: %w", err)
	}
	controls = append(controls, productNetworkCheck{ID: "name-resolution", Destination: hostname, Outcome: "resolved"})
	for _, address := range addresses {
		if err := ctx.Err(); err != nil {
			return controls, destination, err
		}
		network := "tcp6"
		if address.IP.To4() != nil {
			network = "tcp4"
		}
		if network == "tcp4" && destination.IPv4 != "" || network == "tcp6" && destination.IPv6 != "" {
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := connect(attemptCtx, network, net.JoinHostPort(address.IP.String(), "443"))
		cancel()
		if err != nil {
			continue
		}
		if network == "tcp4" {
			destination.IPv4 = address.IP.String()
		} else {
			destination.IPv6 = address.IP.String()
		}
	}
	if destination.IPv4 == "" {
		return controls, destination, errors.New("positive control: provider IPv4 TCP is unreachable; cannot distinguish containment from a dead network")
	}
	controls = append(controls, productNetworkCheck{ID: "ipv4-connect", Destination: net.JoinHostPort(destination.IPv4, "443"), Outcome: "reachable"})
	if destination.IPv6 == "" {
		controls = append(controls, productNetworkCheck{ID: "ipv6-connect", Outcome: "not available on host"})
	} else {
		controls = append(controls, productNetworkCheck{ID: "ipv6-connect", Destination: net.JoinHostPort(destination.IPv6, "443"), Outcome: "reachable"})
	}
	return controls, destination, nil
}

func verifyProductDenyAllNetwork(report productNetworkReport) error {
	if !strings.HasPrefix(report.Node, "v") {
		return errors.New("the network probe did not report a Node version, so Node did not run in the cell")
	}
	if !slices.Equal(report.Interfaces, []string{"lo"}) {
		return fmt.Errorf("cell interfaces %v, want exactly loopback", report.Interfaces)
	}
	expected := map[string]string{}
	for _, control := range report.Controls {
		if control.Outcome == "reachable" || control.Outcome == "resolved" {
			expected[control.ID] = control.Destination
		}
	}
	if expected["name-resolution"] == "" || expected["ipv4-connect"] == "" || expected["host-listener"] == "" {
		return errors.New("missing service-side positive controls")
	}
	for _, route := range report.Routes {
		if route.Iface != "lo" {
			return fmt.Errorf("the cell has a %s route through %s, not only the loopback", route.Family, route.Iface)
		}
	}
	seen := map[string]bool{}
	for _, check := range report.Checks {
		allowed, known := productDenyAllNetworkErrnos[check.ID]
		if !known || seen[check.ID] || expected[check.ID] == "" || expected[check.ID] != check.Destination {
			return fmt.Errorf("the network probe recorded an unexpected or repeated check %q", check.ID)
		}
		seen[check.ID] = true
		if check.Outcome != "error" || !slices.Contains(allowed, check.Errno) {
			return fmt.Errorf("network probe check %s to %s was %s with errno %q, which is not a recorded denial",
				check.ID, check.Destination, check.Outcome, check.Errno)
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("the network probe recorded %d checks, want %d", len(seen), len(expected))
	}
	return nil
}

// productNetworkProgram is the fixed program the cell runs through the image's Node. It prints one JSON object: the
// Node version, the routing rows and one check per destination. Each check is bounded, so a hang becomes a timeout.
func productNetworkProgram(listener string, destination productNetworkDestination) string {
	quoted, err := json.Marshal(listener)
	if err != nil {
		panic(err)
	}
	destinationJSON, _ := json.Marshal(destination)
	return strings.NewReplacer("__LISTENER__", string(quoted), "__DESTINATION__", string(destinationJSON)).Replace(productNetworkScript)
}

const productNetworkScript = `"use strict";
const fs = require("node:fs");
const dns = new (require("node:dns").promises.Resolver)();
const net = require("node:net");
const listener = __LISTENER__;
const destination = __DESTINATION__;
const limitMilliseconds = 3000;

function routes() {
  const rows = [];
  for (const line of fs.readFileSync("/proc/net/route", "utf8").trim().split("\n").slice(1)) {
    rows.push({ family: "ipv4", iface: line.trim().split(/\s+/)[0] });
  }
  let ipv6 = "";
  try {
    ipv6 = fs.readFileSync("/proc/net/ipv6_route", "utf8");
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
  for (const line of ipv6.trim().split("\n").filter(Boolean)) {
    const fields = line.trim().split(/\s+/);
    rows.push({ family: "ipv6", iface: fields[fields.length - 1] });
  }
  return rows;
}

function bounded(attempt) {
  let timer;
  const expired = new Promise((resolve) => {
    timer = setTimeout(() => resolve({ outcome: "timeout", errno: "ETIMEDOUT" }), limitMilliseconds);
  });
  return Promise.race([attempt, expired]).finally(() => clearTimeout(timer));
}

function lookup(name) {
  return bounded(dns.resolve4(name).then(
    () => ({ outcome: "resolved", errno: "" }),
    (error) => ({ outcome: "error", errno: error.code || "" })));
}

function connect(host, port, family) {
  return bounded(new Promise((resolve) => {
    const socket = net.connect({ host, port, family });
    socket.once("connect", () => { socket.destroy(); resolve({ outcome: "reachable", errno: "" }); });
    socket.once("error", (error) => { socket.destroy(); resolve({ outcome: "error", errno: error.code || "" }); });
  }));
}

(async () => {
  const cut = listener.lastIndexOf(":");
  const checks = [
    { id: "name-resolution", destination: destination.Hostname, ...(await lookup(destination.Hostname)) },
    { id: "ipv4-connect", destination: destination.IPv4+":443", ...(await connect(destination.IPv4, 443, 4)) },
    ...(destination.IPv6 ? [{ id: "ipv6-connect", destination: "["+destination.IPv6+"]:443", ...(await connect(destination.IPv6, 443, 6)) }] : []),
    { id: "host-listener", destination: listener, ...(await connect(listener.slice(0, cut), Number(listener.slice(cut + 1)), 4)) },
  ];
  process.stdout.write(JSON.stringify({ node: process.version, interfaces: fs.readFileSync("/proc/net/dev", "utf8").trim().split("\n").slice(2).map(line => line.split(":")[0].trim()).sort(), routes: routes(), checks }) + "\n");
  process.exit(0);
})().catch((error) => {
  process.stderr.write(String(error && error.stack ? error.stack : error) + "\n");
  process.exit(1);
});
`
