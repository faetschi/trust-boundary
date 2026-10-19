//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"tbound/supervisor/internal/piruntime"
)

// The direct-runtime route probe mode (see internal/piruntime/d09_route_probe.go). The entrypoint takes it
// only when the host passes the probe argument after the exec target AND the template flag: variables
// derived from the signed route-probe template, which the entrypoint recomputes. The argument without the
// flag, the flag without the argument, any other position of the argument and the command cell are all
// refused, so a production Pi launch (no probe variable, Pi arguments) can never become a probe, and a
// probe launch can never start Pi.

const (
	routeProbeTargetEnv = "TBOUND_ROUTE_PROBE_TARGET"
	routeProbeControl   = "TBOUND_ROUTE_PROBE_CONTROL"
	routeProbeFlagEnv   = "TBOUND_ROUTE_PROBE_TEMPLATE_FLAG"

	// routeProbeHold bounds how long the probe cell stays up after it reported, so the host can finish
	// the launch (container inspection, cgroup membership, pidfd) before the cell exits.
	routeProbeHold = 5 * time.Minute
	// proofLineLimit is the host's limit on the proof line (readD09TCBProof).
	proofLineLimit = 4095
)

// routeProbeFromEnv returns the probe this launch selects, nil for a Pi launch, or an error for a launch
// that names the probe incompletely or inconsistently.
func routeProbeFromEnv(args []string, getenv func(string) string, commandMode bool) (*piruntime.D09RouteProbe, error) {
	target, control, flag := getenv(routeProbeTargetEnv), getenv(routeProbeControl), getenv(routeProbeFlagEnv)
	haveVariables := target != "" || control != "" || flag != ""
	// The probe argument follows the exec target and is the only member after it.
	asked := len(args) == 3 && args[2] == piruntime.D09RouteProbeArg
	for i, a := range args {
		if strings.HasPrefix(a, "--route-probe") && !(asked && i == 2) {
			return nil, fmt.Errorf("route probe argument %q is not the single argument after the exec target", a)
		}
	}
	switch {
	case commandMode && (asked || haveVariables):
		return nil, errors.New("the command cell takes no route probe")
	case !asked && !haveVariables:
		return nil, nil
	case !asked:
		return nil, errors.New("route probe variables without the route probe argument")
	case !haveVariables || flag == "":
		return nil, errors.New("the route probe argument needs the template flag")
	}
	probe := piruntime.D09RouteProbe{Name: piruntime.D09RouteProbeName, Target: target, Control: control}
	templateDigest := getenv("TBOUND_TEMPLATE_DIGEST")
	if !validDigest(templateDigest) || flag != probe.TemplateFlag(templateDigest) {
		return nil, errors.New("the route probe template flag does not match the signed template and probe")
	}
	if err := probe.Validate(splitPaths(getenv("TBOUND_LANDLOCK_RO_PATH")), splitPaths(getenv("TBOUND_LANDLOCK_RW_PATH"))); err != nil {
		return nil, err
	}
	return &probe, nil
}

// holdForHost waits, reading only, until the host closes the control stream or the limit passes, so the
// cell is still running while the host verifies the launch.
func holdForHost(limit time.Duration) {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
	}
}
