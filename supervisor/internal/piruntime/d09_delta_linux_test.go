//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/sessionrepo"
)

func testD09Delta(t *testing.T, name string) D09CellDelta {
	t.Helper()
	delta, err := ParseD09CellDelta(name)
	if err != nil {
		t.Fatal(err)
	}
	return delta
}

func TestLandlockDigestCommitsToConnectPortsAndScopeOnly(t *testing.T) {
	ro, rw := []string{"/usr"}, []string{"/work"}
	base, err := DigestD09LandlockRules(ro, rw)
	if err != nil {
		t.Fatal(err)
	}
	if same, err := DigestD09LandlockRulesNet(ro, rw, nil, 0); err != nil || same != base {
		t.Fatalf("no network allowance must keep the base ruleset digest: %s %v", same, err)
	}
	connect, err := DigestD09LandlockRulesNet(ro, rw, []uint16{8080}, 0)
	if err != nil || connect == base {
		t.Fatalf("a connect right must change the digest: %s %v", connect, err)
	}
	scoped, _ := DigestD09LandlockRulesNet(ro, rw, []uint16{8080}, D09LandlockScopeAbstractUnix)
	other, _ := DigestD09LandlockRulesNet(ro, rw, []uint16{8081}, 0)
	if scoped == connect || other == connect || other == base {
		t.Fatal("the digest is not sensitive to the port or the scope")
	}
	for name, ports := range map[string][]uint16{"zero port": {0}, "duplicate": {8080, 8080}, "unsorted": {9000, 8080}} {
		if _, err := DigestD09LandlockRulesNet(ro, rw, ports, 0); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := DigestD09LandlockRulesNet(ro, rw, nil, 1<<9); err == nil {
		t.Error("an unregistered Landlock scope bit was accepted")
	}
}

func TestD09CellDeltaLandlockRulesetDigestIsPerDelta(t *testing.T) {
	ro, rw := []string{"/usr"}, []string{"/work"}
	base, _ := DigestD09LandlockRules(ro, rw)
	for name, wantChanged := range map[string]bool{"": false, D09CellDeltaNoReap: false, D09CellDeltaNetSink: true} {
		got, err := testD09Delta(t, name).LandlockRulesetDigest(ro, rw)
		if err != nil || (got != base) != wantChanged {
			t.Errorf("delta %q ruleset digest changed=%t, want %t (%v)", name, got != base, wantChanged, err)
		}
	}
}

func testD09DeltaConfig() D09Config {
	return D09Config{UID: 1000, GID: 1000, PidsMax: 128, MemoryMB: 512, CPUs: "1", SeccompProfilePath: "/etc/tbound/seccomp.json",
		CgroupsPath: "/sys/fs/cgroup/tbound", CrunPath: "/usr/bin/crun", LandlockABI: 8, LandlockROPath: "/usr", LandlockRWPath: "/work",
		LandlockRulesetDigest: DigestBytes([]byte("ll")), TemplateDigest: DigestBytes([]byte("signed base template")),
		CommandExecutable: "/bin/bash", CommandExecutableDigest: fakeD09Digest}
}

func testD09DeltaPlan(t *testing.T, name string) (d09DeltaPlan, D09Config) {
	t.Helper()
	config := testD09DeltaConfig()
	ro, rw := []string{config.LandlockROPath}, []string{config.LandlockRWPath}
	config.LandlockRulesetDigest, _ = DigestD09LandlockRules(ro, rw)
	delta := testD09Delta(t, name)
	token := ""
	if delta.NoReap() {
		token = "0123456789abcdef0123456789abcdef"
	}
	plan, err := newD09DeltaPlan(delta, config, token)
	if err != nil {
		t.Fatal(err)
	}
	return plan, config
}

func commandArgsFor(t *testing.T, plan d09DeltaPlan, config D09Config) string {
	t.Helper()
	spec := sessionrepo.CommandSpec{Executable: config.CommandExecutable, Args: []string{"-lc", "true"}}
	return strings.Join(d09CommandCellArgumentsDelta(plan, config, "tbound-pi-cell-cmd-x", "/tmp/cid",
		"localhost/tbound/pi-cell@sha256:"+strings.Repeat("a", 64), "/proc/123/fd/4", nil, "view-1", "lease-1", 1, 2, spec), " ")
}

func TestD09CommandCellArgumentsPerDelta(t *testing.T) {
	plan, config := testD09DeltaPlan(t, "")
	base := commandArgsFor(t, plan, config)
	for _, want := range []string{"--network=none", "-e TBOUND_TEMPLATE_DIGEST=" + config.TemplateDigest, "-e TBOUND_LANDLOCK_RULESET_DIGEST=" + config.LandlockRulesetDigest} {
		if !strings.Contains(base, want) {
			t.Errorf("base cell vector is missing %q", want)
		}
	}
	for _, forbidden := range []string{"--network=host", "TBOUND_CELL_DELTA", "TBOUND_DELTA_TEMPLATE_DIGEST", "TBOUND_LANDLOCK_CONNECT_PORTS", "TBOUND_OUTPUT_END_MARKER"} {
		if strings.Contains(base, forbidden) {
			t.Errorf("base cell vector carries %q", forbidden)
		}
	}
	if base != strings.Join(d09CommandCellArguments(config, "tbound-pi-cell-cmd-x", "/tmp/cid",
		"localhost/tbound/pi-cell@sha256:"+strings.Repeat("a", 64), "/proc/123/fd/4", "view-1", "lease-1", 1, 2,
		sessionrepo.CommandSpec{Executable: config.CommandExecutable, Args: []string{"-lc", "true"}}), " ") {
		t.Fatal("the unrelaxed vector differs from the legacy constructor")
	}

	plan, config = testD09DeltaPlan(t, D09CellDeltaNetSink)
	net := commandArgsFor(t, plan, config)
	for _, want := range []string{"--network=host", "-e TBOUND_CELL_DELTA=net-sink/v1", "-e TBOUND_DELTA_TEMPLATE_DIGEST=" + plan.templateDigest,
		"-e TBOUND_LANDLOCK_CONNECT_PORTS=8080", "-e TBOUND_LANDLOCK_RULESET_DIGEST=" + plan.landlockDigest,
		"-e TBOUND_TEMPLATE_DIGEST=" + config.TemplateDigest} {
		if !strings.Contains(net, want) {
			t.Errorf("net-sink vector is missing %q:\n%s", want, net)
		}
	}
	if strings.Contains(net, "--label") || strings.Contains(base, "--label") || strings.Contains(net, "--network=none") || strings.Contains(net, "TBOUND_OUTPUT_END_MARKER") || plan.landlockDigest == config.LandlockRulesetDigest {
		t.Errorf("net-sink vector kept the network-none cell or took no-reap state:\n%s", net)
	}

	plan, config = testD09DeltaPlan(t, D09CellDeltaNoReap)
	reap := commandArgsFor(t, plan, config)
	for _, want := range []string{"--network=none", "-e TBOUND_CELL_DELTA=no-reap/v1", "-e TBOUND_DELTA_TEMPLATE_DIGEST=" + plan.templateDigest,
		"-e TBOUND_OUTPUT_END_MARKER=", "-e TBOUND_LANDLOCK_RULESET_DIGEST=" + config.LandlockRulesetDigest,
		"--label " + D09NoReapContainerLabel, "--label " + D09LeaseContainerLabelKey + "=lease-1"} {
		if !strings.Contains(reap, want) {
			t.Errorf("no-reap vector is missing %q:\n%s", want, reap)
		}
	}
	if strings.Contains(reap, "--network=host") || strings.Contains(reap, "TBOUND_LANDLOCK_CONNECT_PORTS") {
		t.Errorf("no-reap vector relaxed the network:\n%s", reap)
	}
}

func TestD09DeltaPlanRefusesAnUnusableBase(t *testing.T) {
	config := testD09DeltaConfig()
	config.TemplateDigest = ""
	if _, err := newD09DeltaPlan(testD09Delta(t, D09CellDeltaNetSink), config, ""); err == nil {
		t.Error("a delta plan was built without the signed base template digest")
	}
	config = testD09DeltaConfig()
	config.LandlockROPath = "relative"
	if _, err := newD09DeltaPlan(testD09Delta(t, D09CellDeltaNetSink), config, ""); err == nil {
		t.Error("a delta plan was built from an unparsable Landlock path list")
	}
	config = testD09DeltaConfig()
	if _, err := newD09DeltaPlan(testD09Delta(t, D09CellDeltaNoReap), config, ""); err == nil {
		t.Error("a no-reap plan was built without an output marker")
	}
}

func validNetSinkProofFor(t *testing.T, name string) (d09CommandProof, d09DeltaPlan, D09Config) {
	t.Helper()
	plan, config := testD09DeltaPlan(t, name)
	imageDigest := DigestBytes([]byte("image"))
	config.ExpectedImageDigest, config.CommandExecutableDigest = imageDigest, DigestBytes([]byte("bash"))
	config.HostNamespaces = D09NamespaceInodes{User: 100, Mount: 101, PID: 102, IPC: 103, UTS: 104, Network: 105}
	bindTestD09UserNamespace(&config, 1000, 1000, 1701, 1702)
	proof := d09CommandProof{
		Schema: "tbound-pi-entrypoint/v1", UID: 1000, GID: 1000, CapEff: d09EmptyCapabilitySet,
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: plan.landlockDigest,
		LandlockTSYNC: true, CgroupV2: true, LeaderPID: 10, ContainerLeaderPID: 1, LeaseID: "lease-1", ViewID: "view-1",
		CommandReady: true, ContainerName: "cell-1", ImageRef: "localhost/tbound/pi-cell@" + imageDigest,
		ImageDigest: imageDigest, TemplateDigest: config.TemplateDigest, ConsumerMountsVerified: true,
		CommandPath: "/bin/bash", CommandDigest: config.CommandExecutableDigest, CommandWorkdir: ".",
		RawSourceFDsClosed: true, WorkerMountID: 10, RuntimeMountID: 10,
		WorkerDevice: 1, WorkerInode: 2, RuntimeDevice: 1, RuntimeInode: 2,
		UserNSInode: 200, MountNSInode: 201, PIDNSInode: 202, IPCNSInode: 203, UTSNSInode: 204, NetworkNSInode: 205,
		NoExternalRoutes: true,
	}
	if plan.delta.Relaxed() {
		proof.Delta, proof.DeltaTemplateDigest = plan.delta.Name(), plan.templateDigest
	}
	if plan.delta.NetSink() {
		proof.NetworkNSInode, proof.NoExternalRoutes = config.HostNamespaces.Network, false
	}
	proof.UIDMap = D09InsideUIDMap(config.CellUID)
	proof.GIDMap = D09InsideGIDMap(config.CellGID)
	proof.UserNamespaceMapDigest = config.UserNamespaceMappingDigest
	return proof, plan, config
}

func TestD09CommandProofIsBoundToItsDelta(t *testing.T) {
	spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}}
	validate := func(proof d09CommandProof, plan d09DeltaPlan, config D09Config) error {
		return validateD09CommandProofDelta(plan, proof, config, proof.ImageRef, "lease-1", "view-1", "cell-1", 1, 2, spec)
	}
	for _, name := range []string{"", D09CellDeltaNetSink, D09CellDeltaNoReap} {
		proof, plan, config := validNetSinkProofFor(t, name)
		if err := validate(proof, plan, config); err != nil {
			t.Fatalf("delta %q: valid proof rejected: %v", name, err)
		}
	}

	// A proof is only valid for the delta the host launched. Replaying the proof of one delta for
	// another, or dropping the delta fields, is refused.
	baseProof, basePlan, baseConfig := validNetSinkProofFor(t, "")
	netProof, netPlan, netConfig := validNetSinkProofFor(t, D09CellDeltaNetSink)
	reapProof, reapPlan, reapConfig := validNetSinkProofFor(t, D09CellDeltaNoReap)
	for name, tc := range map[string]struct {
		proof  d09CommandProof
		plan   d09DeltaPlan
		config D09Config
	}{
		"base proof for net-sink":    {baseProof, netPlan, netConfig},
		"net-sink proof for base":    {netProof, basePlan, baseConfig},
		"net-sink proof for no-reap": {netProof, reapPlan, reapConfig},
		"no-reap proof for net-sink": {reapProof, netPlan, netConfig},
		"no-reap proof for base":     {reapProof, basePlan, baseConfig},
		"base proof for no-reap":     {baseProof, reapPlan, reapConfig},
	} {
		if err := validate(tc.proof, tc.plan, tc.config); !errors.Is(err, ErrProductionLauncherUnavailable) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}

	// The shared network namespace belongs to net-sink only, and the private one to the other cells.
	shared := baseProof
	shared.NetworkNSInode, shared.NoExternalRoutes = baseConfig.HostNamespaces.Network, false
	if err := validate(shared, basePlan, baseConfig); err == nil {
		t.Error("a base cell that shares the host network namespace was accepted")
	}
	private := netProof
	private.NetworkNSInode, private.NoExternalRoutes = 205, true
	if err := validate(private, netPlan, netConfig); err == nil {
		t.Error("a net-sink proof that claims a private network namespace was accepted")
	}
	for name, mutate := range map[string]func(*d09CommandProof){
		"net-sink sharing the host PID namespace":   func(p *d09CommandProof) { p.PIDNSInode = netConfig.HostNamespaces.PID },
		"net-sink sharing the host mount namespace": func(p *d09CommandProof) { p.MountNSInode = netConfig.HostNamespaces.Mount },
		"net-sink with the base ruleset digest":     func(p *d09CommandProof) { p.LandlockRulesetDigest = netConfig.LandlockRulesetDigest },
		"net-sink with a forged delta digest":       func(p *d09CommandProof) { p.DeltaTemplateDigest = DigestBytes([]byte("forged")) },
		"net-sink without the delta name":           func(p *d09CommandProof) { p.Delta = "" },
	} {
		bad := netProof
		mutate(&bad)
		if err := validate(bad, netPlan, netConfig); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The Podman client of a no-reap cell holds the proof pipe open while the container runs, so the
// pipe never reaches EOF. settleNoReap must settle at the final proof line, and still refuse a
// malformed, extra or missing final proof at once.
func TestSettleNoReapSettlesAtTheFinalProofWhileThePipeStaysOpen(t *testing.T) {
	const containerPID = 4242
	startProof, _, _ := validNetSinkProofFor(t, D09CellDeltaNoReap)
	finalProof := startProof
	finalProof.CommandStarted, finalProof.CommandExitObserved, finalProof.CommandExitCode = true, true, 3
	encode := func(proof d09CommandProof) string {
		encoded, err := json.Marshal(proof)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded) + "\n"
	}
	dir := t.TempDir()
	podman := filepath.Join(dir, "podman")
	inspect := fmt.Sprintf(`[{"Name":"cell-1","State":{"Status":"running","Pid":%d}}]`, containerPID)
	if err := os.WriteFile(podman, []byte("#!/bin/sh\nprintf '%s\\n' '"+inspect+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cgroup := filepath.Join(dir, "cgroup")
	if err := os.Mkdir(cgroup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cgroup, "cgroup.events"), []byte("populated 1\nfrozen 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cgroupDir, err := os.Open(cgroup)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cgroupDir.Close() })
	scope := &d09OwnedCommandScope{scope: &LinuxCgroupScope{file: cgroupDir}}
	marker := []byte("\n--tbound-cell-end-test--\n")

	settle := func(t *testing.T, final string, closeWriter bool) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
		t.Helper()
		proofRead, proofWrite, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = proofRead.Close(); _ = proofWrite.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := proofWrite.WriteString(encode(startProof)); err != nil {
			t.Fatal(err)
		}
		start, reader, err := readD09CommandProofLine(ctx, proofRead)
		if err != nil {
			t.Fatal(err)
		}
		start.LeaderPID = containerPID
		if _, err := proofWrite.WriteString(final); err != nil {
			t.Fatal(err)
		}
		if closeWriter {
			_ = proofWrite.Close()
		}
		stdout, stderr := newD09MarkedOutput(1<<10, marker), newD09MarkedOutput(1<<10, marker)
		_, _ = stdout.Write(append([]byte("out\n"), marker...))
		_, _ = stderr.Write(marker)
		runner := &D09CommandRunner{leaseID: "lease-1"}
		result, settlement, _, _, err := runner.settleNoReap(ctx, d09NoReapSettle{
			scope: scope, proofReader: reader, startProof: start, containerPID: containerPID,
			podmanPath: podman, cid: "cid-1", viewID: "view-1", stdout: stdout, stderr: stderr,
		})
		return result, settlement, err
	}

	result, settlement, err := settle(t, encode(finalProof), false)
	if err != nil {
		t.Fatalf("no-reap settlement with the proof pipe still open: %v", err)
	}
	if !result.ExitObserved || result.ExitCode != 3 || string(result.Stdout) != "out\n" ||
		!settlement.SurvivorsTolerated || settlement.ExitCode != 3 {
		t.Fatalf("result = %+v settlement = %+v", result, settlement)
	}

	for name, tc := range map[string]struct {
		final       string
		closeWriter bool
	}{
		"malformed final proof":             {"{not json}\n", false},
		"final proof with an unknown field": {`{"forged":true}` + "\n", false},
		"extra data after the final proof":  {encode(finalProof) + "extra\n", false},
		"a second final proof":              {encode(finalProof) + encode(finalProof), false},
		"missing final proof":               {"", true},
	} {
		if _, _, err := settle(t, tc.final, tc.closeWriter); err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: err = %v, want a prompt refusal", name, err)
		}
	}
}

func TestD09MarkedOutputStopsAtTheCellMarker(t *testing.T) {
	marker := []byte("\n--tbound-cell-end-abc--\n")
	newOutput := func(limit int) *d09MarkedOutput { return newD09MarkedOutput(limit, marker) }

	out := newOutput(1 << 10)
	_, _ = out.Write([]byte("hello\n"))
	select {
	case <-out.Done():
		t.Fatal("done before the marker")
	default:
	}
	_, _ = out.Write(marker)
	_, _ = out.Write([]byte("late output of a survivor"))
	select {
	case <-out.Done():
	default:
		t.Fatal("marker not recognised")
	}
	if got := string(out.Bytes()); got != "hello\n" || out.Overflowed() {
		t.Fatalf("output = %q overflow=%t", got, out.Overflowed())
	}

	// A marker split across writes, and output without a trailing newline.
	split := newOutput(1 << 10)
	_, _ = split.Write([]byte("abc"))
	_, _ = split.Write(marker[:7])
	_, _ = split.Write(marker[7:])
	if got := string(split.Bytes()); got != "abc" {
		t.Fatalf("split marker left %q", got)
	}

	// A command cannot end the stream early by printing something else, and the marker is found
	// after the retained bytes overflowed the bound.
	big := newOutput(8)
	_, _ = big.Write([]byte(strings.Repeat("x", 64)))
	_, _ = big.Write(marker)
	select {
	case <-big.Done():
	default:
		t.Fatal("marker after an overflow was not recognised")
	}
	if !big.Overflowed() || len(big.Bytes()) > 8 {
		t.Fatalf("overflow=%t len=%d", big.Overflowed(), len(big.Bytes()))
	}
	forged := newOutput(1 << 10)
	_, _ = forged.Write([]byte("\n--tbound-cell-end-guess--\n"))
	select {
	case <-forged.Done():
		t.Fatal("a guessed marker ended the stream")
	default:
	}
}

func noReapReceiptParts(t *testing.T) (D09CommandProof, sessionrepo.CommandResult, sessionrepo.CommandSettlement, D09CommandSettlementEvidence) {
	t.Helper()
	imageDigest := DigestBytes([]byte("image"))
	delta := testD09Delta(t, D09CellDeltaNoReap)
	deltaDigest, err := delta.TemplateDigest(DigestBytes([]byte("template")))
	if err != nil {
		t.Fatal(err)
	}
	proof := D09CommandProof{
		Schema: "tbound-pi-entrypoint/v1", UID: 1000, GID: 1000, CapEff: d09EmptyCapabilitySet,
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: DigestBytes([]byte("landlock")),
		LandlockTSYNC: true, CgroupV2: true, LeaderPID: 321, ContainerLeaderPID: 1,
		LeaseID: "lease-1", ViewID: "view-1", CommandStarted: true, CommandReady: true,
		CommandExitObserved: true, CommandExitCode: 0, CommandPath: "/bin/bash", CommandDigest: DigestBytes([]byte("bash")),
		CommandWorkdir: ".", ContainerName: "cell-1", ImageRef: "localhost/tbound/pi-cell@" + imageDigest,
		ImageDigest: imageDigest, TemplateDigest: DigestBytes([]byte("template")),
		Delta: delta.Name(), DeltaTemplateDigest: deltaDigest,
	}
	result := sessionrepo.CommandResult{ExitObserved: true, Stdout: []byte("4242\n")}
	settlement := sessionrepo.CommandSettlement{
		LeaseID: "lease-1", ViewID: "view-1", ExitObserved: true, SurvivorsTolerated: true,
		EvidenceClass: D09CommandRunnerProfile + ":" + D09NoReapSettlementEvidence,
	}
	evidence := D09CommandSettlementEvidence{
		SurvivorsTolerated: true, FinalCommandProofRead: true, OutputMarkerObserved: true,
		ContainerLeftRunning: true, CommandScopeDetached: true, CommandOutputWithinBound: true,
	}
	return proof, result, settlement, evidence
}

func TestD09NoReapReceiptRecordsTheToleratedSurvivorsAndNothingElseDoes(t *testing.T) {
	spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "true"}}
	issue := func(proof D09CommandProof, settlement sessionrepo.CommandSettlement, evidence D09CommandSettlementEvidence) error {
		_, err := newD09CommandReceipt("lease-1", "view-1", "cell-1", strings.Repeat("a", 64), spec,
			sessionrepo.CommandResult{ExitObserved: true}, settlement, proof, evidence)
		return err
	}
	proof, _, settlement, evidence := noReapReceiptParts(t)
	if err := issue(proof, settlement, evidence); err != nil {
		t.Fatalf("a no-reap left-running receipt was refused: %v", err)
	}
	receipt, err := newD09CommandReceipt("lease-1", "view-1", "cell-1", strings.Repeat("a", 64), spec,
		sessionrepo.CommandResult{ExitObserved: true}, settlement, proof, evidence)
	if err != nil || receipt.Proof.Delta != D09CellDeltaNoReap || !validDigest(receipt.Proof.DeltaTemplateDigest) || !receipt.Settlement.SurvivorsTolerated {
		t.Fatalf("the receipt does not record the delta and the tolerated survivors: %+v %v", receipt.Proof, err)
	}

	// The tolerance exists for the no-reap delta only.
	for name, mutate := range map[string]func(*D09CommandProof, *sessionrepo.CommandSettlement, *D09CommandSettlementEvidence){
		"unrelaxed cell": func(p *D09CommandProof, _ *sessionrepo.CommandSettlement, _ *D09CommandSettlementEvidence) {
			p.Delta, p.DeltaTemplateDigest = "", ""
		},
		"net-sink cell": func(p *D09CommandProof, _ *sessionrepo.CommandSettlement, _ *D09CommandSettlementEvidence) {
			p.Delta = D09CellDeltaNetSink
		},
		"unknown delta": func(p *D09CommandProof, _ *sessionrepo.CommandSettlement, _ *D09CommandSettlementEvidence) {
			p.Delta = "no-reap/v2"
		},
		"missing delta identity": func(p *D09CommandProof, _ *sessionrepo.CommandSettlement, _ *D09CommandSettlementEvidence) {
			p.DeltaTemplateDigest = ""
		},
		"settled flags with tolerance": func(_ *D09CommandProof, s *sessionrepo.CommandSettlement, _ *D09CommandSettlementEvidence) {
			s.ProcessScopeEmpty = true
		},
		"another evidence class": func(_ *D09CommandProof, s *sessionrepo.CommandSettlement, _ *D09CommandSettlementEvidence) {
			s.EvidenceClass += "+x"
		},
		"no output marker": func(_ *D09CommandProof, _ *sessionrepo.CommandSettlement, e *D09CommandSettlementEvidence) {
			e.OutputMarkerObserved = false
		},
		"container not running": func(_ *D09CommandProof, _ *sessionrepo.CommandSettlement, e *D09CommandSettlementEvidence) {
			e.ContainerLeftRunning = false
		},
		"scope not detached": func(_ *D09CommandProof, _ *sessionrepo.CommandSettlement, e *D09CommandSettlementEvidence) {
			e.CommandScopeDetached = false
		},
		"claims a removal": func(_ *D09CommandProof, _ *sessionrepo.CommandSettlement, e *D09CommandSettlementEvidence) {
			e.ContainerRemoved = true
		},
		"claims populated=0": func(_ *D09CommandProof, _ *sessionrepo.CommandSettlement, e *D09CommandSettlementEvidence) {
			e.CgroupPopulatedZero = true
		},
		"no exit observation": func(p *D09CommandProof, _ *sessionrepo.CommandSettlement, _ *D09CommandSettlementEvidence) {
			p.CommandExitObserved = false
		},
	} {
		p, _, s, e := noReapReceiptParts(t)
		mutate(&p, &s, &e)
		if err := issue(p, s, e); !errors.Is(err, ErrD09CommandReceiptInvalid) {
			t.Errorf("%s: a receipt was minted: %v", name, err)
		}
	}

	// A fully settled receipt cannot carry the left-running evidence, and a left-running claim on
	// a settled cell is not a settled receipt either.
	settledProof, settledResult, settledSettlement, settledEvidence := noReapReceiptParts(t)
	_ = settledResult
	settledProof.Delta, settledProof.DeltaTemplateDigest = "", ""
	settledSettlement = sessionrepo.CommandSettlement{LeaseID: "lease-1", ViewID: "view-1", ProcessScopeEmpty: true, WritersStopped: true,
		MountDetached: true, ExitObserved: true, EvidenceClass: D09CommandRunnerProfile + ":settled"}
	settledEvidence = D09CommandSettlementEvidence{
		ContainerExitInspected: true, FinalCommandProofRead: true, PodmanWaitCompleted: true,
		OutputWritersDrained: true, ContainerRemoved: true, LeaderPidfdExited: true,
		CgroupPopulatedZero: true, MountNamespaceReleased: true, CommandOutputWithinBound: true, CommandCgroupRemoved: true,
	}
	if err := issue(settledProof, settledSettlement, settledEvidence); err != nil {
		t.Fatalf("a settled receipt was refused: %v", err)
	}
	withTolerance := settledSettlement
	withTolerance.SurvivorsTolerated = true
	if err := issue(settledProof, withTolerance, settledEvidence); err == nil {
		t.Error("a settled receipt carrying the tolerance flag was minted")
	}
	leftRunning := settledEvidence
	leftRunning.ContainerLeftRunning = true
	if err := issue(settledProof, settledSettlement, leftRunning); err == nil {
		t.Error("a settled receipt carrying left-running evidence was minted")
	}
}

func TestTemplateDeltaLandlockRulesetDigestFollowsTheSignedPaths(t *testing.T) {
	template := D09CellTemplate{LandlockROPath: "/usr,/lib", LandlockRWPath: "/work,/tmp"}
	base, err := DigestD09LandlockRules([]string{"/usr", "/lib"}, []string{"/work", "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	template.LandlockRulesetDigest = base
	for name, relaxed := range map[string]bool{"": false, D09CellDeltaNoReap: false, D09CellDeltaNetSink: true} {
		got, err := template.DeltaLandlockRulesetDigest(testD09Delta(t, name))
		if err != nil || (got != base) != relaxed {
			t.Errorf("delta %q: %s relaxed=%t (%v)", name, got, relaxed, err)
		}
	}
	template.LandlockROPath = "relative"
	if _, err := template.DeltaLandlockRulesetDigest(testD09Delta(t, D09CellDeltaNetSink)); err == nil {
		t.Error("a delta ruleset digest was derived from a non-canonical path")
	}
}

func TestRunnerForADeltaStillNeedsAnAdmittedLauncherAndLease(t *testing.T) {
	for _, name := range []string{"", D09CellDeltaNetSink, D09CellDeltaNoReap} {
		if _, err := NewD09CommandRunnerForDelta("", &D09PodmanCrunLauncher{}, testD09Delta(t, name)); !errors.Is(err, ErrProductionLauncherUnavailable) {
			t.Errorf("delta %q: a runner without a lease or admitted launcher was built: %v", name, err)
		}
	}
}

const netSinkRuleFile = "../../../infra/guest/nftables/tbound-net-sink.nft"

func TestNetSinkNftMatchDerivesPathAndLevelFromTheCellsCgroup(t *testing.T) {
	path, level, err := D09NetSinkNftMatch("/sys/fs/cgroup/system.slice/tbound-pi.service/cells")
	if err != nil || path != "system.slice/tbound-pi.service/cells/net-sink" || level != 4 {
		t.Fatalf("default cells cgroup: %q %d %v", path, level, err)
	}
	if path, level, err := D09NetSinkNftMatch("/sys/fs/cgroup/tbound/"); err != nil || path != "tbound/net-sink" || level != 2 {
		t.Errorf("short cells cgroup: %q %d %v", path, level, err)
	}
	for _, bad := range []string{"", "/sys/fs/cgroup", "/sys/fs/cgroup/", "/tmp/cells", "/sys/fs/cgroup/a/../b", "/sys/fs/cgroup/a//b", `/sys/fs/cgroup/a"b`, "/sys/fs/cgroup/a b"} {
		if _, _, err := D09NetSinkNftMatch(bad); err == nil {
			t.Errorf("cells cgroup %q was accepted", bad)
		}
	}
}

// The installed rule matches the cgroup, never the uid: the service uid also makes the provider calls.
func TestNetSinkRuleFileMatchesTheCgroupDerivationAndTheDigestPin(t *testing.T) {
	raw, err := os.ReadFile(netSinkRuleFile)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if got := DigestBytes([]byte(text)); got != D09NetSinkRuleDigest {
		t.Fatalf("D09NetSinkRuleDigest is %s but infra/guest/nftables/tbound-net-sink.nft has %s: update the pin with the file", D09NetSinkRuleDigest, got)
	}
	path, level, err := D09NetSinkNftMatch("/sys/fs/cgroup/system.slice/tbound-pi.service/cells")
	if err != nil {
		t.Fatal(err)
	}
	match := fmt.Sprintf("socket cgroupv2 level %d %q", level, path)
	var rules []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			rules = append(rules, line)
		}
	}
	accept := match + " ip daddr " + D09NetSinkAddress + " tcp dport " + fmt.Sprint(D09NetSinkPort) + " accept"
	drop := match + " counter drop"
	accepts, drops := 0, 0
	for _, rule := range rules {
		switch {
		case rule == accept:
			accepts++
		case rule == drop:
			drops++
		case strings.Contains(rule, "skuid"), strings.Contains(rule, "meta cgroup"):
			t.Errorf("rule matches the uid or another selector, not the net-sink cgroup: %s", rule)
		case strings.HasSuffix(rule, " accept"):
			t.Errorf("rule accepts something other than the registered sink: %s", rule)
		}
	}
	if accepts != 1 || drops != 1 {
		t.Fatalf("want one accept for %s:%d and one drop for the cgroup, got %d and %d", D09NetSinkAddress, D09NetSinkPort, accepts, drops)
	}
	if strings.Index(text, accept) > strings.Index(text, drop) {
		t.Error("the drop rule precedes the accept rule")
	}
	if !strings.Contains(text, "policy accept") {
		t.Error("the chain must leave every socket outside the net-sink cgroup untouched")
	}
}

// netSinkHost is a host where the rule file is installed and the reset attested the load for a
// parent directory and boot id: the three inputs requireNetSinkRule takes.
type netSinkHost struct {
	files  map[string][]byte
	boot   string
	parent *os.File
}

func newNetSinkHost(t *testing.T) *netSinkHost {
	t.Helper()
	raw, err := os.ReadFile(netSinkRuleFile)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Close() })
	h := &netSinkHost{files: map[string][]byte{D09NetSinkRulePath: raw}, boot: "11111111-2222-3333-4444-555555555555", parent: parent}
	h.attest(t, func(*D09NetSinkAttestation) {})
	return h
}

func (h *netSinkHost) attest(t *testing.T, mutate func(*D09NetSinkAttestation)) {
	t.Helper()
	a, err := NewD09NetSinkAttestation(h.boot, h.parent)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&a)
	data, err := a.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	h.files[D09NetSinkAttestPath] = data
}

func (h *netSinkHost) require() error {
	return requireNetSinkRule(func(path string, _ int64) ([]byte, error) {
		data, ok := h.files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return data, nil
	}, func() (string, error) { return h.boot, nil }, h.parent)
}

func TestRequireNetSinkRuleFailsClosed(t *testing.T) {
	h := newNetSinkHost(t)
	if err := h.require(); err != nil {
		t.Fatalf("the pinned rule with a matching attestation was refused: %v", err)
	}
	raw := h.files[D09NetSinkRulePath]
	h.files[D09NetSinkRulePath] = []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n", "\r\n"))
	if err := h.require(); err != nil {
		t.Errorf("the pinned rule with CRLF line endings was refused: %v", err)
	}
	for name, data := range map[string][]byte{
		"absent":  nil,
		"altered": []byte(strings.Replace(string(raw), "counter drop", "counter accept", 1)),
		"empty":   {},
	} {
		h := newNetSinkHost(t)
		if data == nil {
			delete(h.files, D09NetSinkRulePath)
		} else {
			h.files[D09NetSinkRulePath] = data
		}
		if err := h.require(); !errors.Is(err, ErrProductionLauncherUnavailable) {
			t.Errorf("%s rule file: %v", name, err)
		}
	}
}

// The pinned file proves the file only. The reset's attestation proves that nft loaded it for the
// parent cgroup that exists now, in this boot.
func TestRequireNetSinkRuleNeedsTheLoadAttestation(t *testing.T) {
	other, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherAttestation, err := NewD09NetSinkAttestation("11111111-2222-3333-4444-555555555555", other)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*testing.T, *netSinkHost){
		"missing": func(_ *testing.T, h *netSinkHost) { delete(h.files, D09NetSinkAttestPath) },
		"wrong boot id": func(t *testing.T, h *netSinkHost) {
			h.attest(t, func(a *D09NetSinkAttestation) { a.BootID = "another-boot" })
		},
		"wrong inode": func(t *testing.T, h *netSinkHost) {
			h.attest(t, func(a *D09NetSinkAttestation) { a.ParentIno = otherAttestation.ParentIno })
		},
		"wrong device": func(t *testing.T, h *netSinkHost) { h.attest(t, func(a *D09NetSinkAttestation) { a.ParentDev++ }) },
		"wrong rule": func(t *testing.T, h *netSinkHost) {
			h.attest(t, func(a *D09NetSinkAttestation) { a.RuleDigest = DigestBytes([]byte("x")) })
		},
		"wrong schema": func(t *testing.T, h *netSinkHost) { h.attest(t, func(a *D09NetSinkAttestation) { a.Schema = "x" }) },
		"garbage":      func(_ *testing.T, h *netSinkHost) { h.files[D09NetSinkAttestPath] = []byte("not json") },
		"unknown field": func(_ *testing.T, h *netSinkHost) {
			h.files[D09NetSinkAttestPath] = append(bytes.TrimSuffix(bytes.TrimSpace(h.files[D09NetSinkAttestPath]), []byte("}")), []byte(`,"extra":1}`)...)
		},
	}
	for name, mutate := range cases {
		h := newNetSinkHost(t)
		mutate(t, h)
		if name == "wrong inode" && otherAttestation.ParentIno == mustAttestation(t, h).ParentIno {
			t.Fatal("test directories share an inode")
		}
		if err := h.require(); !errors.Is(err, ErrProductionLauncherUnavailable) {
			t.Errorf("%s attestation: %v", name, err)
		}
	}
	// A boot id the host cannot read refuses too.
	h := newNetSinkHost(t)
	read := func(path string, _ int64) ([]byte, error) { return h.files[path], nil }
	if err := requireNetSinkRule(read, func() (string, error) { return "", os.ErrNotExist }, h.parent); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Errorf("unreadable boot id: %v", err)
	}
}

func mustAttestation(t *testing.T, h *netSinkHost) D09NetSinkAttestation {
	t.Helper()
	a, err := NewD09NetSinkAttestation(h.boot, h.parent)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A file the service uid could write is not an attestation: the reader the service uses refuses
// a file that is not root-owned.
func TestNetSinkAttestationFromANonRootFileIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a non-root test user")
	}
	h := newNetSinkHost(t)
	path := filepath.Join(t.TempDir(), "net-sink.attest")
	if err := os.WriteFile(path, h.files[D09NetSinkAttestPath], 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(p string, max int64) ([]byte, error) {
		if p == D09NetSinkAttestPath {
			return readRootOwnedProfileFile(path, max)
		}
		return h.files[p], nil
	}
	if err := requireNetSinkRule(read, func() (string, error) { return h.boot, nil }, h.parent); !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("an attestation owned by the service uid was accepted: %v", err)
	}
}

func TestReadD09BootIDIsAUUID(t *testing.T) {
	id, err := ReadD09BootID()
	if err != nil || len(id) != 36 || strings.TrimSpace(id) != id {
		t.Fatalf("boot id %q: %v", id, err)
	}
}

func TestNetSinkParentCgroupMustExistAndBeACgroup(t *testing.T) {
	cellsRoot, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cellsRoot.Close()
	launcher := &D09PodmanCrunLauncher{scope: &LinuxCgroupScope{root: cellsRoot}}
	if _, err := launcher.openNetSinkParent(attestedForTest); !errors.Is(err, ErrCgroupScopeRefused) {
		t.Fatalf("an absent net-sink parent was opened: %v", err)
	}
	// The service never creates the parent: the rule has to name the directory that exists.
	if _, err := os.Stat(filepath.Join(cellsRoot.Name(), D09NetSinkCgroupName)); err == nil {
		t.Fatal("openNetSinkParent created the parent")
	}
	if err := os.Mkdir(filepath.Join(cellsRoot.Name(), D09NetSinkCgroupName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.openNetSinkParent(attestedForTest); !errors.Is(err, ErrCgroupScopeRefused) {
		t.Fatalf("a plain directory was taken for the net-sink cgroup: %v", err)
	}
	if _, err := (&D09PodmanCrunLauncher{}).openNetSinkParent(attestedForTest); !errors.Is(err, ErrCgroupScopeRefused) {
		t.Fatalf("a launcher without a scope opened the net-sink parent: %v", err)
	}
}

func attestedForTest(*os.File) error { return nil }

// The net-sink command scope is never allocated unless the reset attested the load for the parent it
// opens: here the guest has no attestation (and no installed rule), the parent is a plain directory.
func TestNewNetSinkCommandScopeRefusesWithoutTheLoadAttestation(t *testing.T) {
	cellsRoot, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cellsRoot.Close()
	if err := os.Mkdir(filepath.Join(cellsRoot.Name(), D09NetSinkCgroupName), 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := &D09PodmanCrunLauncher{scope: &LinuxCgroupScope{root: cellsRoot, file: cellsRoot}}
	_, err = launcher.NewNetSinkCommandScope(context.Background(), "lease-1", "view-1")
	if !errors.Is(err, ErrProductionLauncherUnavailable) {
		t.Fatalf("a net-sink scope was allocated or refused for another reason: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(cellsRoot.Name(), D09NetSinkCgroupName))
	if len(entries) != 0 {
		t.Fatalf("a command cgroup was created below the unattested parent: %v", entries)
	}
}

// The service runs with UMask=0077 and podman does not reset the process umask in a cell it starts, so a
// Bash command inherits 0077 and creates 0600 files. The settled-view import accepts only 0644 and 0755
// files owned by the service, so every command cell, under every delta, names --umask=0022 before the image.
func TestD09CommandCellsSetUmask0022(t *testing.T) {
	for _, name := range []string{"", D09CellDeltaNetSink, D09CellDeltaNoReap} {
		plan, config := testD09DeltaPlan(t, name)
		spec := sessionrepo.CommandSpec{Executable: config.CommandExecutable, Args: []string{"-lc", "true"}}
		image := "localhost/tbound/pi-cell@sha256:" + strings.Repeat("a", 64)
		args := d09CommandCellArgumentsDelta(plan, config, "tbound-pi-cell-cmd-x", "/tmp/cid", image, "/proc/123/fd/4", nil, "view-1", "lease-1", 1, 2, spec)
		at, imageAt := indexOfString(args, "--umask=0022"), indexOfString(args, image)
		if at < 0 || imageAt < 0 || at > imageAt {
			t.Errorf("delta %q: --umask=0022 must be a podman option before the image: %q", name, args)
		}
	}
}

// A product scope's read-only entries are mounted read-only over the writable /work view, as podman
// options before the image, under every delta. A path podman's --mount parser cannot carry safely is
// left unmounted. Without entries the vector has only the /work mount.
func TestD09CommandCellsMountReadOnlyEntriesOverTheView(t *testing.T) {
	for _, name := range []string{"", D09CellDeltaNetSink, D09CellDeltaNoReap} {
		plan, config := testD09DeltaPlan(t, name)
		spec := sessionrepo.CommandSpec{Executable: config.CommandExecutable, Args: []string{"-lc", "true"}}
		image := "localhost/tbound/pi-cell@sha256:" + strings.Repeat("a", 64)
		args := d09CommandCellArgumentsDelta(plan, config, "tbound-pi-cell-cmd-x", "/tmp/cid", image, "/views/v1", []string{"README.md", "a,rw", "docs/api", "q\"x"}, "view-1", "lease-1", 1, 2, spec)
		if mounts := countOfString(args, "--mount"); mounts != 3 {
			t.Errorf("delta %q: paths with a comma or quote must stay unmounted, got %d mounts: %q", name, mounts, args)
		}
		imageAt := indexOfString(args, image)
		work := indexOfString(args, "type=bind,src=/views/v1,dst=/work,rw,bind-propagation=rprivate")
		for _, mount := range []string{
			"type=bind,src=/views/v1/README.md,dst=/work/README.md,ro,bind-propagation=rprivate",
			"type=bind,src=/views/v1/docs/api,dst=/work/docs/api,ro,bind-propagation=rprivate",
		} {
			at := indexOfString(args, mount)
			if at < 1 || args[at-1] != "--mount" || work < 0 || at < work || at > imageAt {
				t.Errorf("delta %q: %s must be a --mount after /work and before the image: %q", name, mount, args)
			}
		}
		plain := d09CommandCellArgumentsDelta(plan, config, "tbound-pi-cell-cmd-x", "/tmp/cid", image, "/views/v1", nil, "view-1", "lease-1", 1, 2, spec)
		if mounts := countOfString(plain, "--mount"); mounts != 1 {
			t.Errorf("delta %q: without read-only entries the cell has %d mounts, want only /work: %q", name, mounts, plain)
		}
	}
}

func countOfString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}
