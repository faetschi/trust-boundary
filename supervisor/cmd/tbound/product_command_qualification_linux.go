//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"tbound/supervisor/internal/piruntime"
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

// productQualificationPhases is the order a start runs the phases in. The first failure ends the start.
var productQualificationPhases = []productQualificationPhase{productPhaseProofAck, productPhaseListing, productPhaseScopedWrite,
	productPhaseRootfsDenial, productPhaseLimitsMapping, productPhaseSettlement, productPhaseNetworkProbe}

// errProductQualificationNotWired names the prerequisites of a Bash host that this build cannot yet prove.
var errProductQualificationNotWired = errors.New("the disposable command-cell qualification is not wired in this build; " +
	"a Bash host needs delegated cgroup v2, rootless Podman and the signed product command host")

// productCommandQualificationSteps are the host operations a start performs before it admits Bash. Production binds
// the real ones in productCommandQualification. A test replaces one step to fail the start at that step.
type productCommandQualificationSteps struct {
	loadHost        func(ctx context.Context, cfg productServiceConfig, env *productTurnEnv) (*piruntime.D09ProductCommandHost, error)
	prepareRuntime  func() error
	reconcileScopes func(ctx context.Context, host *piruntime.D09ProductCommandHost, retainedRoots []string) error
	runPhase        func(ctx context.Context, host *piruntime.D09ProductCommandHost, phase productQualificationPhase) error
	newRunner       func(ctx context.Context, host *piruntime.D09ProductCommandHost, c productTurnCommandContext) (productTurnCommandRunner, error)
}

var productCommandQualification = productCommandQualificationSteps{
	loadHost:       loadProductCommandHost,
	prepareRuntime: func() error { return prepareProductCommandRuntime("/", os.Geteuid(), os.Getegid()) },
	reconcileScopes: func(ctx context.Context, host *piruntime.D09ProductCommandHost, retainedRoots []string) error {
		return reconcileProductCommandScopes(ctx, host, retainedRoots...)
	},
	runPhase: runProductQualificationPhase,
	newRunner: func(ctx context.Context, host *piruntime.D09ProductCommandHost, c productTurnCommandContext) (productTurnCommandRunner, error) {
		runner, err := newProductD09CommandRunner(ctx, host, c)
		if err != nil {
			return nil, err
		}
		return runner, nil
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
		present, err := productCommandOwnerJournalsPresent(retainedRoots)
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
	for _, phase := range productQualificationPhases {
		if err := steps.runPhase(ctx, host, phase); err != nil {
			return productCommandHooks{}, fmt.Errorf("command qualification: phase %s: %w", phase, err)
		}
	}
	// The factory serves turns after startup, so it does not hold the start's context.
	hooks.NewCommandRunner = func(c productTurnCommandContext) (productTurnCommandRunner, error) {
		return steps.newRunner(context.Background(), host, c)
	}
	return hooks, nil
}

// productCommandOwnerJournalsPresent reports whether a retained root holds any command owner journal, empty or not.
// A journal is created before its cell, so an empty one can mark a cell that a crash left behind.
func productCommandOwnerJournalsPresent(roots []string) (bool, error) {
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
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

// runProductQualificationPhase runs one phase in a fresh disposable cell. It is not wired in this build, so every
// phase refuses and no factory exists.
// shortcut: the disposable Store, view and cell run is not built, so no phase can pass; upgrade when the demo VM
// qualifies the real D09 host (unit H).
func runProductQualificationPhase(context.Context, *piruntime.D09ProductCommandHost, productQualificationPhase) error {
	return errProductQualificationNotWired
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
	Policy string                `json:"-"`
	Node   string                `json:"node"`
	Routes []productNetworkRoute `json:"routes"`
	Checks []productNetworkCheck `json:"checks"`
}

// productNetworkCellRun runs one fixed program through the signed image's Node in the base cell and returns its stdout.
type productNetworkCellRun func(ctx context.Context, program string) ([]byte, error)

// productNetworkControl dials a listener that qualification opened on the service host, from the service context.
type productNetworkControl func(ctx context.Context, address string) error

// productDenyAllNetworkErrnos names, per check, the errnos a denied connection may record. A timeout is never a denial.
var productDenyAllNetworkErrnos = map[string][]string{
	"name-resolution": {"EAI_AGAIN", "ENOTFOUND", "EAI_NONAME"},
	"ipv4-connect":    {"ENETUNREACH", "EHOSTUNREACH", "EPERM", "EACCES"},
	"ipv6-connect":    {"ENETUNREACH", "EHOSTUNREACH", "EPERM", "EACCES"},
	"host-listener":   {"ENETUNREACH", "EHOSTUNREACH", "ECONNREFUSED", "EPERM", "EACCES"},
}

// productNetworkProbe proves the cell's network policy from inside the cell, against an outside control. The control
// dials the host listener first, so a dead network cannot pass as containment. Only deny-all exists in this step.
func productNetworkProbe(ctx context.Context, policy, listener string, run productNetworkCellRun, control productNetworkControl) (productNetworkReport, error) {
	if policy != productNetworkPolicyDenyAll {
		return productNetworkReport{}, fmt.Errorf("the network probe has no expectation for policy %q", policy)
	}
	if err := control(ctx, listener); err != nil {
		return productNetworkReport{}, fmt.Errorf("positive control: the host listener %s is not reachable from the service context: %w", listener, err)
	}
	output, err := run(ctx, productNetworkProgram(listener))
	if err != nil {
		return productNetworkReport{}, fmt.Errorf("the cell did not run the network probe: %w", err)
	}
	var report productNetworkReport
	if err := json.Unmarshal(output, &report); err != nil {
		return productNetworkReport{}, fmt.Errorf("the network probe printed no structured result: %w", err)
	}
	report.Policy = policy
	return report, verifyProductDenyAllNetwork(report)
}

func verifyProductDenyAllNetwork(report productNetworkReport) error {
	if !strings.HasPrefix(report.Node, "v") {
		return errors.New("the network probe did not report a Node version, so Node did not run in the cell")
	}
	for _, route := range report.Routes {
		if route.Iface != "lo" {
			return fmt.Errorf("the cell has a %s route through %s, not only the loopback", route.Family, route.Iface)
		}
	}
	seen := map[string]bool{}
	for _, check := range report.Checks {
		allowed, known := productDenyAllNetworkErrnos[check.ID]
		if !known || seen[check.ID] {
			return fmt.Errorf("the network probe recorded an unexpected or repeated check %q", check.ID)
		}
		seen[check.ID] = true
		if check.Outcome != "error" || !slices.Contains(allowed, check.Errno) {
			return fmt.Errorf("network probe check %s to %s was %s with errno %q, which is not a recorded denial",
				check.ID, check.Destination, check.Outcome, check.Errno)
		}
	}
	if len(seen) != len(productDenyAllNetworkErrnos) {
		return fmt.Errorf("the network probe recorded %d checks, want %d", len(seen), len(productDenyAllNetworkErrnos))
	}
	return nil
}

// productNetworkProgram is the fixed program the cell runs through the image's Node. It prints one JSON object: the
// Node version, the routing rows and one check per destination. Each check is bounded, so a hang becomes a timeout.
func productNetworkProgram(listener string) string {
	quoted, err := json.Marshal(listener)
	if err != nil {
		panic(err)
	}
	return strings.Replace(productNetworkScript, "__LISTENER__", string(quoted), 1)
}

const productNetworkScript = `"use strict";
const fs = require("node:fs");
const dns = require("node:dns").promises;
const net = require("node:net");
const listener = __LISTENER__;
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
  return bounded(dns.lookup(name).then(
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
    { id: "name-resolution", destination: "probe.invalid", ...(await lookup("probe.invalid")) },
    { id: "ipv4-connect", destination: "192.0.2.1:443", ...(await connect("192.0.2.1", 443, 4)) },
    { id: "ipv6-connect", destination: "[2001:db8::1]:443", ...(await connect("2001:db8::1", 443, 6)) },
    { id: "host-listener", destination: listener, ...(await connect(listener.slice(0, cut), Number(listener.slice(cut + 1)), 4)) },
  ];
  process.stdout.write(JSON.stringify({ node: process.version, routes: routes(), checks }) + "\n");
  process.exit(0);
})().catch((error) => {
  process.stderr.write(String(error && error.stack ? error.stack : error) + "\n");
  process.exit(1);
});
`
