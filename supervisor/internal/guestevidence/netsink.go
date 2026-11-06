package guestevidence

import (
	"errors"
	"fmt"
	"strings"
)

// The net-sink facts that are pinned in code. internal/piruntime holds the same values; a test keeps
// them equal.
const (
	NetSinkAttestSchema = "tbound-net-sink-attest/v1"
	NetSinkRuleDigest   = "sha256:b2a5618fe4c9f1461d3120482e133a44252aee28b553083d68da8689d9f378e4"
	CellsCgroup         = "/system.slice/tbound-pi.service/cells"
)

// Probe outcomes. A dropped SYN times out. A refusal means the packet reached the host, so the rule
// did not drop it, and it does not count as blocked.
const (
	ProbeConnected = "connected"
	ProbeTimeout   = "timeout"
	ProbeSent      = "sent"
	// ProbeDroppedEPERM: the output hook dropped the datagram, so the sender's write failed with EPERM.
	ProbeDroppedEPERM = "dropped-eperm"
)

// MinDroppedPackets is the least the drop counter must rise: one denied TCP port, one denied
// loopback address and one UDP datagram, each dropped at least once.
const MinDroppedPackets = 3

// NetSink is the data of a guest-netsink artifact.
type NetSink struct {
	BootID           string             `json:"boot_id"`
	Attestation      NetSinkAttestation `json:"attestation"`
	RuleFileSHA256   string             `json:"rule_file_sha256"`
	CellsCgroup      string             `json:"cells_cgroup"`
	NftListingSHA256 string             `json:"nft_listing_sha256"`
	RulesetVerified  bool               `json:"ruleset_verified"`
	Probes           NetSinkProbes      `json:"probes"`
}

// NetSinkAttestation mirrors piruntime.D09NetSinkAttestation, the file `tbound trial reset` writes.
type NetSinkAttestation struct {
	Schema     string `json:"schema"`
	BootID     string `json:"boot_id"`
	ParentDev  uint64 `json:"parent_dev"`
	ParentIno  uint64 `json:"parent_ino"`
	RuleDigest string `json:"rule_digest"`
}

// NetSinkProbes are the results of connections made by a process inside the net-sink cgroup.
type NetSinkProbes struct {
	// AllowedConnect is a TCP connect to 127.0.33.1:8080, the one permitted destination.
	AllowedConnect string `json:"allowed_connect"`
	// WrongPort is a TCP connect to 127.0.33.1:8081.
	WrongPort string `json:"wrong_port"`
	// OtherLoopback is a TCP connect to 127.0.0.1:8080.
	OtherLoopback string `json:"other_loopback"`
	// UDP is a datagram to 127.0.33.1:8080.
	UDP               string `json:"udp"`
	DropCounterBefore uint64 `json:"drop_counter_before"`
	DropCounterAfter  uint64 `json:"drop_counter_after"`
}

func validateNetSink(raw []byte) error {
	var n NetSink
	if err := DecodeStrict(raw, &n); err != nil {
		return err
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if strings.TrimSpace(n.BootID) == "" {
		add("boot_id is empty")
	}
	a := n.Attestation
	if a.Schema != NetSinkAttestSchema || a.BootID != n.BootID || a.RuleDigest != NetSinkRuleDigest || a.ParentIno == 0 {
		add("the load attestation is not for this boot and the pinned rule (schema %q, boot %q, rule %q, parent inode %d)", a.Schema, a.BootID, a.RuleDigest, a.ParentIno)
	}
	if n.RuleFileSHA256 != NetSinkRuleDigest {
		add("the installed rule file is %q, want the pinned %s", n.RuleFileSHA256, NetSinkRuleDigest)
	}
	if n.CellsCgroup != CellsCgroup {
		add("cells cgroup %q, want %q", n.CellsCgroup, CellsCgroup)
	}
	if !IsDigest(n.NftListingSHA256) {
		add("the nft listing digest %q is not a sha256 digest", n.NftListingSHA256)
	}
	if !n.RulesetVerified {
		add("the live nft ruleset was not verified to be exactly the pinned rule")
	}
	p := n.Probes
	if p.AllowedConnect != ProbeConnected {
		add("the connect to the sink address and port was %q, want %q", p.AllowedConnect, ProbeConnected)
	}
	for name, got := range map[string]string{"wrong port": p.WrongPort, "other loopback address": p.OtherLoopback} {
		if got != ProbeTimeout {
			add("the connect to the %s was %q, want %q (dropped)", name, got, ProbeTimeout)
		}
	}
	// EPERM is the drop itself; the counter check below proves nftables did it.
	if p.UDP != ProbeSent && p.UDP != ProbeDroppedEPERM {
		add("the UDP datagram probe was %q, want %q or %q", p.UDP, ProbeSent, ProbeDroppedEPERM)
	}
	if p.DropCounterAfter < p.DropCounterBefore || p.DropCounterAfter-p.DropCounterBefore < MinDroppedPackets {
		add("the drop counter rose from %d to %d, want at least %d", p.DropCounterBefore, p.DropCounterAfter, MinDroppedPackets)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
