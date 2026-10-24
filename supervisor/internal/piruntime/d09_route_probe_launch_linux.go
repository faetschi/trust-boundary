//go:build linux

package piruntime

import (
	"errors"
	"fmt"
)

// ErrD09RouteProbeLaunched is the cause of every launch from a route-probe template. The cell ran its
// probe, reported it in the launch proof and did not start Pi, so it never sends readiness and the launch
// yields no worker. The validated proof is in PiWorkerLaunchError.RouteProbe.
var ErrD09RouteProbeLaunched = errors.New("route-probe cell reported its probe and did not start Pi")

// D09RouteProbeLaunch is the validated launch proof of a route-probe cell and the PID of the launched
// process it is bound to.
type D09RouteProbeLaunch struct {
	LauncherPID int
	Proof       D09TCBProof
}

// d09WorkerProbeLaunch returns the entrypoint arguments and the extra environment of a worker cell. A
// production template (no route probe) gets the Pi arguments of the launch request and no probe variable.
// A route-probe template gets the probe argument alone (Pi is never started) and the variables the
// entrypoint checks against the template flag. The probe is chosen by the signed template only: neither
// the launch request nor its arguments can select it.
func d09WorkerProbeLaunch(config D09Config, requestArgs []string) (args, extraEnv []string) {
	if config.RouteProbe.Name == "" {
		return requestArgs, nil
	}
	return []string{D09RouteProbeArg}, []string{
		"TBOUND_TEMPLATE_DIGEST=" + config.TemplateDigest,
		"TBOUND_ROUTE_PROBE_TARGET=" + config.RouteProbe.Target,
		"TBOUND_ROUTE_PROBE_CONTROL=" + config.RouteProbe.Control,
		"TBOUND_ROUTE_PROBE_TEMPLATE_FLAG=" + config.RouteProbe.TemplateFlag(config.TemplateDigest),
	}
}

// validateD09RouteProbeProof requires the proof to carry a probe result exactly when the template names a
// probe, and then the complete result of that probe. Whether the attempts were denied is judged by the
// evidence producer, not here: a probe that reads the target is a finding, not a launch failure.
func validateD09RouteProbeProof(proof D09TCBProof, config D09Config) error {
	switch {
	case config.RouteProbe.Name == "" && proof.RouteProbe == nil:
		return nil
	case config.RouteProbe.Name == "":
		return errors.New("the cell reported a route probe result but its template names no probe")
	case proof.RouteProbe == nil:
		return errors.New("the template names a route probe and the cell reported no probe result")
	}
	if err := proof.RouteProbe.CheckShape(config.RouteProbe); err != nil {
		return fmt.Errorf("route probe result: %w", err)
	}
	return nil
}
