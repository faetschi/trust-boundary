//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
)

const (
	geCommit   = "0123456789abcdef0123456789abcdef01234567"
	geRuleFile = "../../../infra/guest/nftables/tbound-net-sink.nft"
)

func geDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestCgroup2MountFromMountinfo(t *testing.T) {
	info := "22 1 0:21 / /proc rw - proc proc rw\n" +
		"35 25 0:30 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:9 - cgroup2 cgroup2 rw,nsdelegate\n"
	if m, err := cgroup2Mount(info); err != nil || m != "/sys/fs/cgroup" {
		t.Fatalf("mount = %q, %v", m, err)
	}
	if m, err := cgroup2Mount("35 25 0:30 / /mnt/cg\\040two rw - cgroup2 cgroup2 rw\n"); err != nil || m != "/mnt/cg two" {
		t.Fatalf("escaped mount = %q, %v", m, err)
	}
	for name, in := range map[string]string{
		"empty":            "",
		"cgroup v1 only":   "30 25 0:26 / /sys/fs/cgroup/cpu rw - cgroup cgroup rw,cpu\n",
		"a path that says": "30 25 0:26 / /sys/fs/cgroup2 rw - tmpfs tmpfs rw\n",
	} {
		if _, err := cgroup2Mount(in); err == nil {
			t.Errorf("%s: a mount was found", name)
		}
	}
}

func geCgroupTree(t *testing.T, kill bool, kind string) capabilityEnv {
	t.Helper()
	root := t.TempDir()
	service := filepath.Join(root, "system.slice", "tbound-pi.service")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "cgroup.controllers"), "cpuset cpu io memory pids\n")
	if kind != "" {
		write(filepath.Join(service, "cgroup.type"), kind+"\n")
	}
	if kill {
		write(filepath.Join(service, "cgroup.kill"), "")
	}
	profile := filepath.Join(root, "seccomp.json")
	write(profile, `{"defaultAction":"SCMP_ACT_ERRNO"}`)
	mountinfo := filepath.Join(root, "mountinfo")
	write(mountinfo, "35 25 0:30 / "+root+" rw - cgroup2 cgroup2 rw\n")
	return capabilityEnv{mountinfo: mountinfo, serviceCgroup: guestevidence.ServiceCgroup, seccompProfile: profile}
}

func TestProbeCapabilityReadsTheMachine(t *testing.T) {
	env := geCgroupTree(t, true, "domain")
	c, err := probeCapability(env)
	if err != nil {
		t.Fatal(err)
	}
	if c.KernelRelease == "" || c.Arch == "" || c.Cgroup.FSType != "cgroup2" || c.Cgroup.ServiceCgroup != guestevidence.ServiceCgroup ||
		c.Cgroup.ServiceCgroupType != "domain" || !c.Cgroup.KillInterface || strings.Join(c.Cgroup.Controllers, ",") != "cpuset,cpu,io,memory,pids" {
		t.Fatalf("capability = %+v", c)
	}
	if want := geDigest(`{"defaultAction":"SCMP_ACT_ERRNO"}`); c.Seccomp.ProfileSHA256 != want {
		t.Fatalf("seccomp profile digest = %s, want %s", c.Seccomp.ProfileSHA256, want)
	}
	if !c.Seccomp.FilterInstalled || c.Seccomp.Mode != 2 {
		t.Skipf("this kernel does not allow the seccomp filter probe: %+v", c.Seccomp)
	}
	// The probe thread's filter must not have leaked into this process.
	if status, err := os.ReadFile("/proc/self/status"); err == nil && strings.Contains(string(status), "\nSeccomp:\t2") {
		t.Fatal("the seccomp probe filtered the whole process")
	}
	// The artifact is as honest as the kernel: it passes the validator only on Landlock ABI 8.
	data, err := guestevidence.Encode(guestevidence.KindCapability, geCommit, geDigest("self"), c)
	if err != nil {
		t.Fatal(err)
	}
	verr := guestevidence.Validate(guestevidence.KindCapability, data, guestevidence.Expect{Commit: geCommit, BinarySHA256: geDigest("self")})
	if c.Landlock.ABI == guestevidence.RequiredLandlockABI {
		if verr != nil {
			t.Fatalf("a kernel with Landlock ABI 8 was refused: %v", verr)
		}
	} else if verr == nil || !strings.Contains(verr.Error(), "Landlock ABI") {
		t.Fatalf("Landlock ABI %d was not refused for its ABI: %v", c.Landlock.ABI, verr)
	}
}

func TestProbeCapabilityMissingFactsAreNotInvented(t *testing.T) {
	env := geCgroupTree(t, false, "")
	c, err := probeCapability(env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Cgroup.KillInterface || c.Cgroup.ServiceCgroupType != "" {
		t.Fatalf("an absent cgroup.kill or cgroup.type was reported: %+v", c.Cgroup)
	}
	if guestevidence.Validate(guestevidence.KindCapability, mustEncodeGE(t, guestevidence.KindCapability, c), guestevidence.Expect{Commit: geCommit, BinarySHA256: geDigest("self")}) == nil {
		t.Fatal("a guest without cgroup.kill produced a valid capability artifact")
	}
	for name, mutate := range map[string]func(*capabilityEnv){
		"no cgroup2 mount": func(e *capabilityEnv) {
			os.WriteFile(e.mountinfo, []byte("30 25 0:26 / /sys rw - sysfs sysfs rw\n"), 0o644)
		},
		"no mountinfo":          func(e *capabilityEnv) { e.mountinfo = filepath.Join(filepath.Dir(e.mountinfo), "none") },
		"relative profile":      func(e *capabilityEnv) { e.seccompProfile = "seccomp.json" },
		"missing profile":       func(e *capabilityEnv) { e.seccompProfile += ".none" },
		"service cgroup escape": func(e *capabilityEnv) { e.serviceCgroup = "/../etc" },
		"relative service":      func(e *capabilityEnv) { e.serviceCgroup = "system.slice" },
	} {
		e := geCgroupTree(t, true, "domain")
		mutate(&e)
		if _, err := probeCapability(e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func mustEncodeGE(t *testing.T, kind string, data any) []byte {
	t.Helper()
	b, err := guestevidence.Encode(kind, geCommit, geDigest("self"), data)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---- test output ----

func TestParseTestOutput(t *testing.T) {
	out := strings.Join([]string{
		"=== RUN   TestA", "--- PASS: TestA (0.00s)",
		"=== RUN   TestB", "    sandbox_test.go:12: cgroup v2 is not delegated", "--- SKIP: TestB (0.00s)",
		"=== RUN   TestC", "=== RUN   TestC/sub", "    --- FAIL: TestC/sub (0.00s)", "--- FAIL: TestC (0.00s)",
		"=== RUN   TestUnfinished",
		"=== RUN   TestD", "--- PASS: TestD (0.01s)",
		"--- PASS: TestForged (0.00s) printed by a test", // a result line without a run line still counts as a result
		"PASS", "",
	}, "\n")
	want := map[string]string{"TestA": "pass", "TestB": "skip", "TestC": "fail", "TestUnfinished": "fail", "TestD": "pass", "TestForged": "pass"}
	got := map[string]string{}
	for _, r := range parseTestOutput([]byte(out)) {
		if _, dup := got[r.Test]; dup {
			t.Errorf("%s is reported twice", r.Test)
		}
		got[r.Test] = r.Action
	}
	if len(got) != len(want) {
		t.Fatalf("results = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// Subtests alone are not top-level results.
	if r := parseTestOutput([]byte("=== RUN   TestX/sub\n    --- PASS: TestX/sub (0.00s)\n")); len(r) != 0 {
		t.Errorf("subtest-only output gave %v", r)
	}
	// A test reported with two different results fails.
	if r := parseTestOutput([]byte("--- PASS: TestX (0s)\n--- FAIL: TestX (0s)\n")); len(r) != 1 || r[0].Action != "fail" {
		t.Errorf("conflicting results gave %v", r)
	}
}

// geTestBinary writes an executable script that prints the given verbose test output and exits with code.
func geTestBinary(t *testing.T, output string, code int) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(out, []byte(output), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat " + out + "\nexit " + string(rune('0'+code)) + "\n"
	bin := filepath.Join(dir, "procoracle.test")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func geProcOracleOutput(skip string) string {
	spec, _ := guestevidence.SpecFor(guestevidence.KindProcOracleTests)
	var b strings.Builder
	for _, name := range spec.Required {
		b.WriteString("=== RUN   " + name + "\n")
		if name == skip {
			b.WriteString("    cgroup_linux_test.go:30: cgroup delegation is missing\n--- SKIP: " + name + " (0.00s)\n")
			continue
		}
		b.WriteString("--- PASS: " + name + " (0.10s)\n")
	}
	b.WriteString("PASS\n")
	return b.String()
}

func TestRunGuestTestsRecordsTheBinaryAndTheResults(t *testing.T) {
	bin := geTestBinary(t, geProcOracleOutput(""), 0)
	run, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, bin)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(bin)
	spec, _ := guestevidence.SpecFor(guestevidence.KindProcOracleTests)
	if run.TestBinarySHA256 != geDigest(string(data)) || run.Package != spec.Package || run.ExitCode != 0 || len(run.Results) != len(spec.Required) {
		t.Fatalf("run = %+v", run)
	}
	expect := guestevidence.Expect{Commit: geCommit, BinarySHA256: geDigest("self"), TestBinaries: map[string]string{spec.Package: run.TestBinarySHA256}}
	if err := guestevidence.Validate(guestevidence.KindProcOracleTests, mustEncodeGE(t, guestevidence.KindProcOracleTests, run), expect); err != nil {
		t.Fatalf("a passing real-cgroup run was refused: %v", err)
	}

	// A required test that skipped (no delegated cgroups) is not evidence.
	skipped, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, geTestBinary(t, geProcOracleOutput(spec.Required[0]), 0))
	if err != nil {
		t.Fatal(err)
	}
	expect.TestBinaries[spec.Package] = skipped.TestBinarySHA256
	if guestevidence.Validate(guestevidence.KindProcOracleTests, mustEncodeGE(t, guestevidence.KindProcOracleTests, skipped), expect) == nil {
		t.Fatal("a skipped required test passed")
	}
	// A failing exit is recorded, not hidden.
	failed, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, geTestBinary(t, geProcOracleOutput(""), 1))
	if err != nil || failed.ExitCode != 1 {
		t.Fatalf("failed run = %+v %v", failed, err)
	}
	if _, err := runGuestTests(context.Background(), guestevidence.KindCapability, bin); err == nil {
		t.Error("a kind that is not a test run was accepted")
	}
	if _, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, "procoracle.test"); err == nil {
		t.Error("a relative test binary path was accepted")
	}
	if _, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("a missing test binary was accepted")
	}
}

// ---- net sink ----

func geNftJSON(t *testing.T, packets int) []byte {
	t.Helper()
	path, level, err := piruntime.D09NetSinkNftMatch("/sys/fs/cgroup" + guestevidence.CellsCgroup)
	if err != nil {
		t.Fatal(err)
	}
	cg := map[string]any{"match": map[string]any{"op": "==", "left": map[string]any{"socket": map[string]any{"key": "cgroupv2", "level": level}}, "right": path}}
	daddr := map[string]any{"match": map[string]any{"op": "==", "left": map[string]any{"payload": map[string]any{"protocol": "ip", "field": "daddr"}}, "right": piruntime.D09NetSinkAddress}}
	dport := map[string]any{"match": map[string]any{"op": "==", "left": map[string]any{"payload": map[string]any{"protocol": "tcp", "field": "dport"}}, "right": piruntime.D09NetSinkPort}}
	items := []map[string]any{
		{"metainfo": map[string]any{"version": "1.0.9"}},
		{"table": map[string]any{"family": "inet", "name": piruntime.D09NetSinkTable, "handle": 7}},
		{"chain": map[string]any{"family": "inet", "table": piruntime.D09NetSinkTable, "name": "output", "handle": 1, "type": "filter", "hook": "output", "prio": 0, "policy": "accept"}},
		{"rule": map[string]any{"family": "inet", "table": piruntime.D09NetSinkTable, "chain": "output", "handle": 2, "expr": []any{cg, daddr, dport, map[string]any{"accept": nil}}}},
		{"rule": map[string]any{"family": "inet", "table": piruntime.D09NetSinkTable, "chain": "output", "handle": 3, "expr": []any{cg, map[string]any{"counter": map[string]any{"packets": packets, "bytes": packets * 60}}, map[string]any{"drop": nil}}}},
	}
	data, err := json.Marshal(map[string]any{"nftables": items})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func geNetSinkEnv(t *testing.T) netSinkEnv {
	t.Helper()
	rule, err := os.ReadFile(geRuleFile)
	if err != nil {
		t.Fatal(err)
	}
	attest := piruntime.D09NetSinkAttestation{Schema: piruntime.D09NetSinkAttestSchema, BootID: "boot-1", ParentDev: 4, ParentIno: 99, RuleDigest: piruntime.D09NetSinkRuleDigest}
	raw, err := attest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	counter := 4
	return netSinkEnv{
		bootID: func() (string, error) { return "boot-1", nil },
		readFile: func(p string) ([]byte, error) {
			switch p {
			case "/run/tbound/net-sink.attest":
				return raw, nil
			case "/etc/tbound/net-sink.nft":
				return rule, nil
			case "mountinfo":
				return []byte("35 25 0:30 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n"), nil
			}
			return nil, os.ErrNotExist
		},
		mountinfo: "mountinfo", cellsPath: guestevidence.CellsCgroup, attestPath: "/run/tbound/net-sink.attest", rulePath: "/etc/tbound/net-sink.nft",
		nftList: func(context.Context) ([]byte, error) {
			defer func() { counter += 3 }() // the denied connections and the datagram were dropped between the listings
			return geNftJSON(t, counter), nil
		},
		probe: func(context.Context, string) (netSinkProbeResult, error) {
			return netSinkProbeResult{Cgroup: guestevidence.CellsCgroup + "/net-sink", AllowedConnect: "connected", WrongPort: "timeout", OtherLoopback: "timeout", UDP: "sent"}, nil
		},
	}
}

func geValidateNetSink(t *testing.T, n guestevidence.NetSink) error {
	t.Helper()
	return guestevidence.Validate(guestevidence.KindNetSink, mustEncodeGE(t, guestevidence.KindNetSink, n), guestevidence.Expect{Commit: geCommit, BinarySHA256: geDigest("self")})
}

func TestProbeNetSinkProducesAnArtifactThatPasses(t *testing.T) {
	env := geNetSinkEnv(t)
	n, err := probeNetSink(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if n.Probes.DropCounterBefore != 4 || n.Probes.DropCounterAfter != 7 || !n.RulesetVerified || n.BootID != "boot-1" || n.Attestation.ParentIno != 99 {
		t.Fatalf("net sink = %+v", n)
	}
	if err := geValidateNetSink(t, n); err != nil {
		t.Fatalf("a loaded, discriminating rule was refused: %v", err)
	}
}

func TestProbeNetSinkRefusesWhatIsNotTheLoadedRule(t *testing.T) {
	for name, mutate := range map[string]func(*netSinkEnv){
		"no attestation": func(e *netSinkEnv) {
			inner := e.readFile
			e.readFile = func(p string) ([]byte, error) {
				if p == e.attestPath {
					return nil, os.ErrNotExist
				}
				return inner(p)
			}
		},
		"attestation with an unknown member": func(e *netSinkEnv) {
			inner := e.readFile
			e.readFile = func(p string) ([]byte, error) {
				b, err := inner(p)
				if p == e.attestPath {
					b = bytes.Replace(b, []byte(`"schema"`), []byte(`"x":1,"schema"`), 1)
				}
				return b, err
			}
		},
		"the table is not loaded": func(e *netSinkEnv) {
			e.nftList = func(context.Context) ([]byte, error) { return nil, errors.New("Error: No such file or directory") }
		},
		"an extra rule": func(e *netSinkEnv) {
			e.nftList = func(context.Context) ([]byte, error) {
				var doc map[string][]map[string]any
				if err := json.Unmarshal(geNftJSON(t, 1), &doc); err != nil {
					t.Fatal(err)
				}
				doc["nftables"] = append(doc["nftables"], map[string]any{"rule": map[string]any{"expr": []any{map[string]any{"accept": nil}}}})
				b, _ := json.Marshal(doc)
				return b, nil
			}
		},
		"the probe ran outside the cgroup": func(e *netSinkEnv) {
			e.probe = func(context.Context, string) (netSinkProbeResult, error) {
				return netSinkProbeResult{Cgroup: "/user.slice", AllowedConnect: "connected", WrongPort: "timeout", OtherLoopback: "timeout", UDP: "sent"}, nil
			}
		},
		"the probe failed": func(e *netSinkEnv) {
			e.probe = func(context.Context, string) (netSinkProbeResult, error) {
				return netSinkProbeResult{}, errors.New("no cgroup")
			}
		},
		"no cgroup2": func(e *netSinkEnv) {
			inner := e.readFile
			e.readFile = func(p string) ([]byte, error) {
				if p == e.mountinfo {
					return []byte("30 25 0:26 / /sys rw - sysfs sysfs rw\n"), nil
				}
				return inner(p)
			}
		},
	} {
		env := geNetSinkEnv(t)
		mutate(&env)
		if _, err := probeNetSink(context.Background(), env); err == nil {
			t.Errorf("%s: a net-sink artifact was produced", name)
		}
	}
	// What the probe sees can also make a complete artifact that the validator refuses.
	for name, mutate := range map[string]func(*netSinkEnv){
		"a refused connect": func(e *netSinkEnv) {
			e.probe = func(context.Context, string) (netSinkProbeResult, error) {
				return netSinkProbeResult{Cgroup: guestevidence.CellsCgroup + "/net-sink", AllowedConnect: "connected", WrongPort: "refused", OtherLoopback: "timeout", UDP: "sent"}, nil
			}
		},
		"nothing was dropped": func(e *netSinkEnv) {
			e.nftList = func(context.Context) ([]byte, error) { return geNftJSON(t, 4), nil }
		},
		"another boot": func(e *netSinkEnv) { e.bootID = func() (string, error) { return "boot-2", nil } },
		"another rule file": func(e *netSinkEnv) {
			inner := e.readFile
			e.readFile = func(p string) ([]byte, error) {
				if p == e.rulePath {
					return []byte("table inet tbound_net_sink { }\n"), nil
				}
				return inner(p)
			}
		},
	} {
		env := geNetSinkEnv(t)
		mutate(&env)
		n, err := probeNetSink(context.Background(), env)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if geValidateNetSink(t, n) == nil {
			t.Errorf("%s: the validator accepted the artifact", name)
		}
	}
}

func TestDropCounter(t *testing.T) {
	if n, err := dropCounter(geNftJSON(t, 41)); err != nil || n != 41 {
		t.Fatalf("counter = %d, %v", n, err)
	}
	for _, bad := range []string{"", "{}", `{"nftables":[]}`, `{"nftables":[{"rule":{"expr":[{"accept":null}]}}]}`, "not json"} {
		if _, err := dropCounter([]byte(bad)); err == nil {
			t.Errorf("%q: a counter was found", bad)
		}
	}
}

func TestDialOutcomes(t *testing.T) {
	// A closed loopback port answers with a reset: that is "refused", never "timeout".
	if got := dialOutcome("127.0.0.1:1"); got != "refused" {
		t.Errorf("closed port = %q", got)
	}
	if got := udpOutcome("127.0.0.1:9"); got != "sent" {
		t.Errorf("udp = %q", got)
	}
}

// ---- the command ----

func geResponse(t *testing.T, mutate func(receipt map[string]any)) string {
	t.Helper()
	receipt := map[string]any{"schema_version": "tbound-production-pi-receipt/v1", "profile_hash": geDigest("profile"), "frozen_source_digest": geDigest("source"),
		"provider_exchange": true, "claim_bearing": false, "task_admitted": true, "turn_completed": true, "success": true,
		"tool_sequence": []string{"read", "edit", "bash", "read"}, "provider_turns": 5, "source_generation": "g0", "final_generation": "g2", "generation_count": 3,
		"settlement": "STOPPED", "exit_observed": true, "exit_code": 0, "descendants_settled": true}
	if mutate != nil {
		mutate(receipt)
	}
	b, err := json.Marshal(map[string]any{"schema_version": "tbound-pi-service-response/v1", "request_id": "r1", "status": "completed", "receipt": receipt})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGuestEvidenceProviderExchangeCommand(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	args := func(response, out string) []string {
		return []string{"provider-exchange", "--commit", geCommit, "--response", response, "--profile-hash", geDigest("profile"),
			"--frozen-source-digest", geDigest("source"), "--out", out}
	}
	var stdout, stderr bytes.Buffer
	good := write("good.txt", geResponse(t, nil)+"\n")
	out := filepath.Join(dir, "provider.json")
	if err := runGuestEvidence(args(good, out), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := selfDigest()
	if err := guestevidence.Validate(guestevidence.KindProviderExchange, data, guestevidence.Expect{Commit: geCommit, BinarySHA256: self,
		ProfileHash: geDigest("profile"), FrozenSourceDigest: geDigest("source")}); err != nil {
		t.Fatalf("the written artifact does not validate: %v", err)
	}
	if !strings.Contains(stdout.String(), "binary_sha256 "+self) {
		t.Errorf("output = %s", stdout.String())
	}
	// Nothing is written for a response that does not show the exchange, and an artifact is never replaced.
	for name, response := range map[string]string{
		"read only run":   geResponse(t, func(r map[string]any) { r["tool_sequence"] = []string{"read"} }),
		"no exchange":     geResponse(t, func(r map[string]any) { r["provider_exchange"] = false }),
		"another profile": geResponse(t, func(r map[string]any) { r["profile_hash"] = geDigest("x") }),
		"two lines":       geResponse(t, nil) + "\n" + geResponse(t, nil),
		"empty":           "",
	} {
		refused := filepath.Join(dir, "refused-"+strings.ReplaceAll(name, " ", "-")+".json")
		if err := runGuestEvidence(args(write("r.txt", response+"\n"), refused), &stdout, &stderr); err == nil {
			t.Errorf("%s: an artifact was written", name)
		}
		if _, err := os.Stat(refused); err == nil {
			t.Errorf("%s: a refused run left %s", name, refused)
		}
	}
	if err := runGuestEvidence(args(good, out), &stdout, &stderr); err == nil {
		t.Error("an existing artifact was replaced")
	}
	for name, bad := range map[string][]string{
		"no kind":          nil,
		"unknown kind":     {"cpu", "--commit", geCommit, "--out", filepath.Join(dir, "x1.json")},
		"short commit":     {"provider-exchange", "--commit", "abc", "--out", filepath.Join(dir, "x2.json")},
		"relative out":     {"provider-exchange", "--commit", geCommit, "--out", "x3.json"},
		"no response":      {"provider-exchange", "--commit", geCommit, "--out", filepath.Join(dir, "x4.json")},
		"positional":       append(args(good, filepath.Join(dir, "x5.json")), "extra"),
		"tests no binary":  {"tests", "--commit", geCommit, "--kind", "guest-tests-procoracle", "--out", filepath.Join(dir, "x6.json")},
		"tests wrong kind": {"tests", "--commit", geCommit, "--kind", "guest-capability", "--test-binary", good, "--out", filepath.Join(dir, "x7.json")},
	} {
		if err := runGuestEvidence(bad, &stdout, &stderr); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestGuestEvidenceTestsCommandWritesOnlyValidRuns(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	run := func(bin, name string) (string, error) {
		out := filepath.Join(dir, name+".json")
		return out, runGuestEvidence([]string{"tests", "--commit", geCommit, "--kind", "guest-tests-procoracle", "--test-binary", bin, "--out", out}, &stdout, &stderr)
	}
	// The command cannot know which binary the capture built; the evq side compares the digest. Here the
	// run is valid in itself, so the artifact is written even though the digest is not yet compared.
	// (Producer-side validation is given the digest of the binary that ran.)
	out, err := run(geTestBinary(t, geProcOracleOutput(""), 0), "ok")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
	spec, _ := guestevidence.SpecFor(guestevidence.KindProcOracleTests)
	for name, bin := range map[string]string{
		"skipped":    geTestBinary(t, geProcOracleOutput(spec.Required[1]), 0),
		"exit":       geTestBinary(t, geProcOracleOutput(""), 1),
		"no results": geTestBinary(t, "PASS\n", 0),
	} {
		out, err := run(bin, "bad-"+strings.ReplaceAll(name, " ", "-"))
		if err == nil {
			t.Errorf("%s: an artifact was written", name)
		}
		if _, statErr := os.Stat(out); statErr == nil {
			t.Errorf("%s: a refused run left %s", name, out)
		}
	}
}
