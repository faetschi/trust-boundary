//go:build linux

package main

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
)

var (
	routeProbeTemplate = piruntime.DigestBytes([]byte("route-probe template"))
	routeProbeConfig   = piruntime.D09RouteProbe{Name: piruntime.D09RouteProbeName, Target: "/home/node/.profile", Control: piruntime.PiWorkerCellDefaultWorkingDirectory + "/src/release.txt"}
)

func probeEnv(overrides map[string]string) func(string) string {
	env := map[string]string{
		"TBOUND_TEMPLATE_DIGEST":           routeProbeTemplate,
		"TBOUND_ROUTE_PROBE_TARGET":        routeProbeConfig.Target,
		"TBOUND_ROUTE_PROBE_CONTROL":       routeProbeConfig.Control,
		"TBOUND_ROUTE_PROBE_TEMPLATE_FLAG": routeProbeConfig.TemplateFlag(routeProbeTemplate),
		"TBOUND_LANDLOCK_RO_PATH":          "/usr,/lib,/etc," + piruntime.PiWorkerCellDefaultWorkingDirectory,
		"TBOUND_LANDLOCK_RW_PATH":          "/work,/tmp",
	}
	for k, v := range overrides {
		if v == "\x00" {
			delete(env, k)
		} else {
			env[k] = v
		}
	}
	return func(name string) string { return env[name] }
}

var probeArgs = []string{"/entrypoint", "/proc/self/fd/3", piruntime.D09RouteProbeArg}

func TestEntrypointTakesTheProbeModeOnlyWithTheTemplateFlag(t *testing.T) {
	probe, err := routeProbeFromEnv(probeArgs, probeEnv(nil), false)
	if err != nil || probe == nil || *probe != routeProbeConfig {
		t.Fatalf("a probe launch with the template flag: %+v, %v", probe, err)
	}
	piArgs := []string{"/entrypoint", "/proc/self/fd/3", "--mode", "rpc"}
	none := map[string]string{"TBOUND_ROUTE_PROBE_TARGET": "\x00", "TBOUND_ROUTE_PROBE_CONTROL": "\x00", "TBOUND_ROUTE_PROBE_TEMPLATE_FLAG": "\x00"}
	if probe, err := routeProbeFromEnv(piArgs, probeEnv(none), false); err != nil || probe != nil {
		t.Fatalf("a Pi launch is not a probe launch: %+v, %v", probe, err)
	}

	refused := map[string]struct {
		args        []string
		env         map[string]string
		commandMode bool
	}{
		"the argument without the flag":                {probeArgs, map[string]string{"TBOUND_ROUTE_PROBE_TEMPLATE_FLAG": "\x00"}, false},
		"the argument with a flag of another template": {probeArgs, map[string]string{"TBOUND_TEMPLATE_DIGEST": piruntime.DigestBytes([]byte("other"))}, false},
		"the argument with a flag of another target":   {probeArgs, map[string]string{"TBOUND_ROUTE_PROBE_TARGET": "/home/node/.bashrc"}, false},
		"the argument without the template digest":     {probeArgs, map[string]string{"TBOUND_TEMPLATE_DIGEST": "\x00"}, false},
		"the argument without a target":                {probeArgs, map[string]string{"TBOUND_ROUTE_PROBE_TARGET": "\x00"}, false},
		"a target inside the ruleset":                  {probeArgs, map[string]string{"TBOUND_ROUTE_PROBE_TARGET": "/etc/passwd", "TBOUND_ROUTE_PROBE_TEMPLATE_FLAG": piruntime.D09RouteProbe{Name: piruntime.D09RouteProbeName, Target: "/etc/passwd", Control: routeProbeConfig.Control}.TemplateFlag(routeProbeTemplate)}, false},
		"the flag without the argument":                {[]string{"/entrypoint", "/proc/self/fd/3"}, nil, false},
		"the flag with Pi arguments":                   {[]string{"/entrypoint", "/proc/self/fd/3", "--mode", "rpc"}, nil, false},
		"the argument among Pi arguments":              {[]string{"/entrypoint", "/proc/self/fd/3", "--mode", piruntime.D09RouteProbeArg}, nil, false},
		"the argument before the exec target":          {[]string{"/entrypoint", piruntime.D09RouteProbeArg, "/proc/self/fd/3"}, nil, false},
		"the argument twice":                           {append(append([]string{}, probeArgs...), piruntime.D09RouteProbeArg), nil, false},
		"another probe name":                           {[]string{"/entrypoint", "/proc/self/fd/3", "--route-probe=direct-runtime/v2"}, nil, false},
		"the command cell":                             {probeArgs, nil, true},
	}
	for name, c := range refused {
		if probe, err := routeProbeFromEnv(c.args, probeEnv(c.env), c.commandMode); err == nil {
			t.Errorf("%s: accepted as %+v", name, probe)
		}
	}
	// The command cell refuses a probe variable as well.
	if _, err := routeProbeFromEnv([]string{"/entrypoint", "/bin/bash"}, probeEnv(nil), true); err == nil {
		t.Error("the command cell accepted probe variables")
	}
}

func TestProbeRefusalNamesTheGate(t *testing.T) {
	_, err := routeProbeFromEnv(probeArgs, probeEnv(map[string]string{"TBOUND_ROUTE_PROBE_TEMPLATE_FLAG": "\x00"}), false)
	if err == nil || !strings.Contains(err.Error(), "template flag") {
		t.Fatalf("the refusal does not name the template flag: %v", err)
	}
}
