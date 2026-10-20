//go:build linux

package main

import (
	"fmt"
	"regexp"
	"syscall"
	"time"

	"tbound/supervisor/internal/piruntime"
)

// cellDelta is the registered command-cell delta the host launched this cell under, as the
// entrypoint accepts it. The host passes it through the environment, but the entrypoint takes
// nothing from the environment that the delta does not already define: the connect port list and
// the template identity are checked against the code-defined values of the named delta, so a
// relaxation that the signed template does not name is refused.
type cellDelta struct {
	delta     piruntime.D09CellDelta
	endMarker string // no-reap/v1 only: the end-of-output marker text
}

var endMarkerToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

// cellDeltaFromEnv reads and verifies TBOUND_CELL_DELTA, TBOUND_DELTA_TEMPLATE_DIGEST,
// TBOUND_LANDLOCK_CONNECT_PORTS and TBOUND_OUTPUT_END_MARKER. The worker cell takes none of them.
func cellDeltaFromEnv(getenv func(string) string, commandMode bool) (cellDelta, error) {
	name, deltaDigest := getenv("TBOUND_CELL_DELTA"), getenv("TBOUND_DELTA_TEMPLATE_DIGEST")
	ports, token := getenv("TBOUND_LANDLOCK_CONNECT_PORTS"), getenv("TBOUND_OUTPUT_END_MARKER")
	if !commandMode && (name != "" || deltaDigest != "" || ports != "" || token != "") {
		return cellDelta{}, fmt.Errorf("a command-cell delta variable is set in the worker cell")
	}
	delta, err := piruntime.ParseD09CellDelta(name)
	if err != nil {
		return cellDelta{}, err
	}
	wantDigest := ""
	if delta.Relaxed() {
		if wantDigest, err = delta.TemplateDigest(getenv("TBOUND_TEMPLATE_DIGEST")); err != nil {
			return cellDelta{}, err
		}
	}
	if deltaDigest != wantDigest {
		return cellDelta{}, fmt.Errorf("delta template digest %q does not match the signed template and delta %q", deltaDigest, name)
	}
	if ports != delta.ConnectPortsText() {
		return cellDelta{}, fmt.Errorf("TCP connect ports %q are not the ports of delta %q (%q)", ports, name, delta.ConnectPortsText())
	}
	if delta.NoReap() != (token != "") || token != "" && !endMarkerToken.MatchString(token) {
		return cellDelta{}, fmt.Errorf("the end-of-output marker is for no-reap/v1 only and has a fixed shape")
	}
	out := cellDelta{delta: delta}
	if token != "" {
		out.endMarker = piruntime.D09OutputEndMarker(token)
	}
	return out, nil
}

// networkIsolationRequired reports whether the cell must have a private network namespace with
// no external route. Only net-sink/v1 shares the host network namespace.
func networkIsolationRequired(delta piruntime.D09CellDelta) bool { return !delta.NetSink() }

// landlockPlan is the Landlock ruleset the entrypoint installs for a delta: the filesystem rules
// of the signed template, the handled network rights (bind and connect, always both), the TCP
// ports on which connect is allowed, and the scoped rights. Nothing but a connect right on a port
// of the delta is ever allowed, and bind is never allowed.
type landlockPlan struct {
	delta        piruntime.D09CellDelta
	handledFS    uint64
	handledNet   uint64
	scoped       uint64
	connectPorts []uint16
	pathRules    []landlockRule
	// heldFDRules are file rules on descriptors the cell already holds: the verified Node binary at
	// fd3, which lives on a host mount no path rule covers.
	heldFDRules []landlockFDRule
}

type landlockFDRule struct {
	fd     int
	access uint64
}

// landlockHeldFDAccessMask is every right a held-descriptor rule may carry: execute and read.
const landlockHeldFDAccessMask = piruntime.D09LandlockHeldBinaryAccess

func checkHeldFDRule(rule landlockFDRule) error {
	if rule.fd != piruntime.D09LandlockHeldBinaryFD || rule.access == 0 || rule.access&^landlockHeldFDAccessMask != 0 {
		return fmt.Errorf("Landlock descriptor rule fd %d with access %#x exceeds the held-binary profile (fd %d, access %#x)",
			rule.fd, rule.access, piruntime.D09LandlockHeldBinaryFD, landlockHeldFDAccessMask)
	}
	return nil
}

func planLandlock(delta piruntime.D09CellDelta, roPaths, rwPaths []string) landlockPlan {
	plan := landlockPlan{delta: delta, handledFS: landlockAccessAllABI8, handledNet: landlockAccessAllNet,
		connectPorts: delta.ConnectPorts(), pathRules: appendRules(roPaths, landlockReadExecute, rwPaths, landlockAccessAllABI8),
		heldFDRules: []landlockFDRule{{fd: piruntime.D09LandlockHeldBinaryFD, access: piruntime.D09LandlockHeldBinaryAccess}}}
	if delta.ScopedAbstractUnix() {
		plan.scoped = piruntime.D09LandlockScopeAbstractUnix
	}
	return plan
}

// rulesetDigest is the digest the host expects in the proof, derived by the same function.
func (p landlockPlan) rulesetDigest(roPaths, rwPaths []string) string {
	digest, err := p.delta.LandlockRulesetDigest(roPaths, rwPaths)
	if err != nil {
		return ""
	}
	return digest
}

// lingerAsInit keeps the cell init alive after the command exited and reaps what its orphaned
// descendants leave behind. A no-reap/v1 cell must not exit: the exit of PID 1 would end the PID
// namespace and kill every survivor. The operator reset ends it. It never returns.
func lingerAsInit() {
	for {
		var status syscall.WaitStatus
		if _, err := syscall.Wait4(-1, &status, 0, nil); err != nil && err != syscall.EINTR {
			time.Sleep(time.Second)
		}
	}
}
