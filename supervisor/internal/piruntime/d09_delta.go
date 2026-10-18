package piruntime

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The registered command-cell deltas of the P half of a pair (design B2). A delta is a
// code-defined, closed relaxation of the D09 command cell. It never changes the registered,
// signed base template: the template of a delta cell is derived from the signed base template
// digest and the constants below (TemplateDigest), so one signature on the host profile covers
// the base cell and every delta cell, and a change to a delta's policy changes its digest.
//
// A delta is selected only by the service, from the admitted role and the pinned manifest. No
// request, config member, or environment variable chooses one.
const (
	// D09CellDeltaNetSink opens exactly one TCP destination to the command cell: 127.0.33.1:8080,
	// the registered sink in the guest root network namespace.
	D09CellDeltaNetSink = "net-sink/v1"
	// D09CellDeltaNoReap leaves the descendants of a command alive at its settlement. The
	// command cell's init lingers after the command exits, and its cgroup is not killed.
	D09CellDeltaNoReap = "no-reap/v1"

	// D09NetSinkAddress and D09NetSinkPort are the one destination of net-sink/v1.
	D09NetSinkAddress = "127.0.33.1"
	D09NetSinkPort    = 8080

	d09BaseNamespaceProfile    = "user,mount,pid,ipc,uts,network-none/v1"
	d09NetSinkNamespaceProfile = "user,mount,pid,ipc,uts,network-host-sink/v1"
	d09DeltaTemplateLabel      = "tbound-d09-command-cell-delta-template/v1"

	// D09NoReapSettlementEvidence is the evidence class suffix of a command cell that was left
	// running by no-reap/v1. It follows D09CommandRunnerProfile + ":".
	D09NoReapSettlementEvidence = "no-reap-v1:proof-exit+output-marker+container-left-running+scope-detached"
)

// ErrD09CellDeltaUnknown reports a delta name outside the closed set.
var ErrD09CellDeltaUnknown = errors.New("unknown D09 command-cell delta")

// D09CellDelta is one registered command-cell delta. The zero value is the unrelaxed cell. Only
// ParseD09CellDelta builds a relaxed value, so a relaxed delta is always one of the registered
// names.
type D09CellDelta struct{ name string }

// ParseD09CellDelta maps a cell delta name to its value. The empty string is the unrelaxed cell.
// A name that is not a cell delta (for example admit-all/v1, which relaxes admission and not the
// cell) is refused: the caller maps those to the unrelaxed cell on purpose.
func ParseD09CellDelta(name string) (D09CellDelta, error) {
	switch name {
	case "":
		return D09CellDelta{}, nil
	case D09CellDeltaNetSink, D09CellDeltaNoReap:
		return D09CellDelta{name: name}, nil
	}
	return D09CellDelta{}, fmt.Errorf("%w: %q", ErrD09CellDeltaUnknown, name)
}

// Name is the registered delta name, empty for the unrelaxed cell.
func (d D09CellDelta) Name() string { return d.name }

// Relaxed reports whether the cell differs from the signed base template.
func (d D09CellDelta) Relaxed() bool { return d.name != "" }

// NetSink reports whether the cell may connect to the registered sink.
func (d D09CellDelta) NetSink() bool { return d.name == D09CellDeltaNetSink }

// NoReap reports whether the cell's descendants are left alive at settlement.
func (d D09CellDelta) NoReap() bool { return d.name == D09CellDeltaNoReap }

// NetworkMode is the Podman --network value of the cell.
func (d D09CellDelta) NetworkMode() string {
	if d.NetSink() {
		return "host"
	}
	return "none"
}

// NamespaceProfile is the namespace profile the cell's proof must carry.
func (d D09CellDelta) NamespaceProfile() string {
	if d.NetSink() {
		return d09NetSinkNamespaceProfile
	}
	return d09BaseNamespaceProfile
}

// ConnectPorts are the TCP ports the cell's Landlock ruleset allows to connect to: exactly 8080
// for net-sink/v1 and none otherwise. Landlock network rules are port based, so the address is
// held by the guest nftables rule (infra/guest/nftables/tbound-net-sink.nft).
func (d D09CellDelta) ConnectPorts() []uint16 {
	if d.NetSink() {
		return []uint16{D09NetSinkPort}
	}
	return nil
}

// ExpectNoExternalRoutes is the value the cell's proof must report for no_external_routes. A
// net-sink cell shares the host network namespace, where the check does not hold and is not made.
func (d D09CellDelta) ExpectNoExternalRoutes() bool { return !d.NetSink() }

// ScopedAbstractUnix reports whether the Landlock ruleset scopes abstract unix sockets. The
// net-sink cell shares the host network namespace and with it the abstract unix socket namespace;
// the scope confines it to abstract sockets of its own Landlock domain.
func (d D09CellDelta) ScopedAbstractUnix() bool { return d.NetSink() }

// ConnectPortsText is the comma separated form of ConnectPorts that the host passes to the
// entrypoint, empty when no port is allowed.
func (d D09CellDelta) ConnectPortsText() string {
	ports := d.ConnectPorts()
	text := make([]string, len(ports))
	for i, port := range ports {
		text[i] = strconv.Itoa(int(port))
	}
	return strings.Join(text, ",")
}

// TemplateDigest is the identity of the delta cell derived from the digest of the signed base
// template: the base digest itself for the unrelaxed cell, otherwise a digest of the base digest
// and every code-defined parameter of the delta. It is recorded in the receipt and in the cell's
// proof.
func (d D09CellDelta) TemplateDigest(baseTemplateDigest string) (string, error) {
	if !validDigest(baseTemplateDigest) {
		return "", errors.New("D09 delta template digest needs a valid base template digest")
	}
	if !d.Relaxed() {
		return baseTemplateDigest, nil
	}
	settlement := "kill-at-settlement"
	if d.NoReap() {
		settlement = "observe-only-linger-detached-scope"
	}
	return DigestBytes([]byte(strings.Join([]string{d09DeltaTemplateLabel, baseTemplateDigest, d.name,
		"network=" + d.NetworkMode(), "namespace=" + d.NamespaceProfile(), "connect=" + d.ConnectPortsText(),
		"scope-abstract-unix=" + strconv.FormatBool(d.ScopedAbstractUnix()), "settlement=" + settlement}, "\x00"))), nil
}

// D09OutputEndMarker is the text the no-reap/v1 cell init writes to its stdout and stderr after
// the command exited. The host finishes both streams at it, so the bytes before it are the
// command's complete output although a surviving descendant may hold the streams open. The
// token is random per command and passed to the cell init only.
func D09OutputEndMarker(token string) string { return "\n--tbound-cell-end-" + token + "--\n" }

// The guest side of net-sink/v1. The service puts every net-sink command cell under one fixed,
// persistent parent cgroup of the cells cgroup and the guest nftables rule matches that cgroup, so
// the service's own sockets (the provider HTTPS client runs in the service process) stay untouched.
const (
	// D09NetSinkCgroupName is the parent of the net-sink command scopes, a direct child of the
	// delegated cells cgroup. The operator creates it before the rule is loaded; the service only
	// opens it.
	D09NetSinkCgroupName = "net-sink"
	// D09NetSinkRulePath is the installed copy of infra/guest/nftables/tbound-net-sink.nft.
	D09NetSinkRulePath = "/etc/tbound/net-sink.nft"
	// D09NetSinkRuleDigest pins the rule file (LF line endings). The service refuses net-sink unless
	// the installed file has this digest and the reset attested its load (D09NetSinkAttestPath).
	D09NetSinkRuleDigest = "sha256:b2a5618fe4c9f1461d3120482e133a44252aee28b553083d68da8689d9f378e4"

	// D09NetSinkAttestPath is the root-owned attestation the operator reset writes after it loaded
	// the pinned rule and read it back (tbound trial reset). The service refuses net-sink unless it
	// matches this boot, the net-sink parent cgroup it opens and the pinned rule digest.
	D09NetSinkAttestPath   = "/run/tbound/net-sink.attest"
	D09NetSinkAttestSchema = "tbound-net-sink-attest/v1"
	// D09NetSinkTable is the nftables table the rule file declares.
	D09NetSinkTable = "tbound_net_sink"

	// D09NoReapContainerLabel and D09LeaseContainerLabelKey label the containers of no-reap/v1
	// command cells, which are left running and not removed by the lease. The operator reset removes
	// the containers that carry the first label.
	D09NoReapContainerLabel   = "tbound.delta=no-reap/v1"
	D09LeaseContainerLabelKey = "tbound.lease"

	cgroup2MountPrefix = "/sys/fs/cgroup/"
)

// D09NetSinkNftMatch returns the cgroup path (relative to the cgroup2 mount) and the level that the
// nftables rule `socket cgroupv2 level <level> "<path>"` must name for the delegated cells cgroup of
// the signed template, for example /sys/fs/cgroup/system.slice/tbound-pi.service/cells gives
// "system.slice/tbound-pi.service/cells/net-sink" and level 4: the number of path components.
func D09NetSinkNftMatch(cellsCgroupPath string) (path string, level int, err error) {
	clean := strings.TrimSuffix(cellsCgroupPath, "/")
	relative, found := strings.CutPrefix(clean, cgroup2MountPrefix)
	if !found || relative == "" || strings.Contains(relative, "..") || strings.Contains(relative, "//") || strings.ContainsAny(relative, "\"\\ \t\n") {
		return "", 0, fmt.Errorf("cells cgroup %q is not a clean path below %s", cellsCgroupPath, cgroup2MountPrefix)
	}
	path = relative + "/" + D09NetSinkCgroupName
	return path, strings.Count(path, "/") + 1, nil
}
