//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/cgroupguard"
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
	cells := filepath.Join(service, "cells")
	if err := os.MkdirAll(cells, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{root, filepath.Join(root, "system.slice"), service, cells} {
		write(filepath.Join(dir, "cgroup.controllers"), "cpuset cpu io memory pids\n")
		write(filepath.Join(dir, "cgroup.subtree_control"), "cpu memory pids\n")
	}
	write(filepath.Join(cells, "cgroup.type"), "domain\n")
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
	return capabilityEnv{mountinfo: mountinfo, serviceCgroup: guestevidence.ServiceCgroup, seccompProfile: profile,
		cellsProbe: func(string) (guestevidence.CellsProbe, error) { return goodGECellsProbe(), nil }}
}

func TestProbeCapabilityReadsTheMachine(t *testing.T) {
	// Other capability tests may leave a filter on the wedged main thread. Start
	// fresh so its count cannot mask a new filter on a different thread.
	if os.Getenv("TBOUND_TEST_CAPABILITY_PROBE_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProbeCapabilityReadsTheMachine$", "-test.v", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), "TBOUND_TEST_CAPABILITY_PROBE_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fresh-process capability probe: %v\n%s", err, output)
		}
		if bytes.Contains(output, []byte("--- SKIP: "+t.Name())) {
			t.Skipf("fresh-process capability probe skipped:\n%s", output)
		}
		t.Logf("fresh-process capability probe:\n%s", output)
		return
	}
	env := geCgroupTree(t, true, "domain")
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	inheritedFilters := seccompFilterCount(t, status)
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if status, err := os.ReadFile("/proc/self/task/" + task.Name() + "/status"); err == nil && seccompFilterCount(t, status) != inheritedFilters {
			t.Fatal("fresh-process threads do not have a uniform inherited seccomp filter count")
		}
	}
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
	// The probe thread's filter must not have leaked to any other thread of this process. /proc/self is
	// the main thread: when the scheduler runs the probe goroutine there, the runtime wedges that thread
	// instead of ending it, so it keeps its filter and nothing runs on it. Any other thread with
	// more filters than the inherited baseline is a leak (an exiting thread is given a moment).
	if leaked := otherFilteredThreads(t, inheritedFilters); len(leaked) > 0 {
		t.Fatalf("the seccomp probe filter reached threads %v of this process", leaked)
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

// otherFilteredThreads returns threads with more filters than the inherited baseline,
// except the main thread, which the runtime wedges (and does not end) if the probe goroutine ran on it.
func otherFilteredThreads(t *testing.T, inheritedFilters int) []int {
	t.Helper()
	var filtered []int
	for range 200 {
		filtered = nil
		tasks, err := os.ReadDir("/proc/self/task")
		if err != nil {
			t.Skipf("threads of this process are not listed: %v", err)
		}
		for _, task := range tasks {
			tid, err := strconv.Atoi(task.Name())
			if err != nil || tid == os.Getpid() {
				continue
			}
			// A thread that exits between the listing and the read is gone, not filtered.
			if status, err := os.ReadFile("/proc/self/task/" + task.Name() + "/status"); err == nil && seccompFilterCount(t, status) > inheritedFilters {
				filtered = append(filtered, tid)
			}
		}
		if len(filtered) == 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return filtered
}

func seccompFilterCount(t *testing.T, status []byte) int {
	t.Helper()
	for _, line := range strings.Split(string(status), "\n") {
		if value, ok := strings.CutPrefix(line, "Seccomp_filters:"); ok {
			count, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || count < 0 {
				t.Fatalf("invalid Seccomp_filters count: %q", value)
			}
			return count
		}
	}
	t.Fatal("Seccomp_filters count is missing from /proc status")
	return 0
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
	return geTestsOutput(guestevidence.KindProcOracleTests, skip)
}

func geTestsOutput(kind, skip string) string {
	spec, _ := guestevidence.SpecFor(kind)
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
	run, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, bin, "")
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
	skipped, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, geTestBinary(t, geProcOracleOutput(spec.Required[0]), 0), "")
	if err != nil {
		t.Fatal(err)
	}
	expect.TestBinaries[spec.Package] = skipped.TestBinarySHA256
	if guestevidence.Validate(guestevidence.KindProcOracleTests, mustEncodeGE(t, guestevidence.KindProcOracleTests, skipped), expect) == nil {
		t.Fatal("a skipped required test passed")
	}
	// A failing exit is recorded, not hidden.
	failed, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, geTestBinary(t, geProcOracleOutput(""), 1), "")
	if err != nil || failed.ExitCode != 1 {
		t.Fatalf("failed run = %+v %v", failed, err)
	}
	if _, err := runGuestTests(context.Background(), guestevidence.KindCapability, bin, ""); err == nil {
		t.Error("a kind that is not a test run was accepted")
	}
	if _, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, "procoracle.test", ""); err == nil {
		t.Error("a relative test binary path was accepted")
	}
	if _, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, filepath.Join(t.TempDir(), "none"), ""); err == nil {
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

func geResponse(t *testing.T, w bindWorld, mutate func(receipt map[string]any)) string {
	t.Helper()
	receipt := map[string]any{"schema_version": "tbound-production-pi-receipt/v1", "run_id": "run-g1", "profile_hash": w.profileDigest, "containment_profile_digest": w.templateDigest, "frozen_source_digest": rtSource,
		"provider_exchange": true, "claim_bearing": false, "task_admitted": true, "turn_completed": true, "success": true,
		"disposition": "PUBLISHED", "publication_status": "SUCCEEDED",
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
	w := newBindWorld(t, dir)
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	args := func(response, out string) []string {
		return []string{"provider-exchange", "--commit", geCommit, "--response", response, "--run-config", w.runConfig, "--template", w.files.template,
			"--host-profile", w.files.hostProfile, "--host-profile-signature", w.files.hostProfileSig, "--host-profile-key", w.files.hostProfileKey,
			"--registration", w.files.registration, "--registration-signature", w.files.registrationSig, "--out", out}
	}
	var stdout, stderr bytes.Buffer
	good := write("good.txt", geResponse(t, w, nil)+"\n")
	out := filepath.Join(dir, "provider.json")
	if err := runGuestEvidence(args(good, out), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := selfDigest()
	if err := guestevidence.Validate(guestevidence.KindProviderExchange, data, withBinary(w.expect(), self)); err != nil {
		t.Fatalf("the written artifact does not validate: %v", err)
	}
	if !strings.Contains(stdout.String(), "binary_sha256 "+self) {
		t.Errorf("output = %s", stdout.String())
	}
	// Nothing is written for a response that does not show the exchange, and an artifact is never replaced.
	for name, response := range map[string]string{
		"read only run":   geResponse(t, w, func(r map[string]any) { r["tool_sequence"] = []string{"read"} }),
		"no exchange":     geResponse(t, w, func(r map[string]any) { r["provider_exchange"] = false }),
		"another profile": geResponse(t, w, func(r map[string]any) { r["profile_hash"] = geDigest("x") }),
		"two lines":       geResponse(t, w, nil) + "\n" + geResponse(t, w, nil),
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
		return out, runGuestEvidence([]string{"tests", "--commit", geCommit, "--kind", guestevidence.KindLandlockConnectTests, "--test-binary", bin, "--out", out}, &stdout, &stderr)
	}
	// The command cannot know which binary the capture built; the evq side compares the digest. Here the
	// run is valid in itself, so the artifact is written even though the digest is not yet compared.
	// (Producer-side validation is given the digest of the binary that ran.)
	// The procoracle kind needs a fresh cgroup (TestRunGuestTestsRunsTheRealCgroupTestsInAFreshCgroupAndRemovesIt).
	out, err := run(geTestBinary(t, geTestsOutput(guestevidence.KindLandlockConnectTests, ""), 0), "ok")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
	spec, _ := guestevidence.SpecFor(guestevidence.KindLandlockConnectTests)
	for name, bin := range map[string]string{
		"skipped":    geTestBinary(t, geTestsOutput(guestevidence.KindLandlockConnectTests, spec.Required[0]), 0),
		"exit":       geTestBinary(t, geTestsOutput(guestevidence.KindLandlockConnectTests, ""), 1),
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

func goodGECellsProbe() guestevidence.CellsProbe {
	p := guestevidence.CellsProbe{UID: guestevidence.ServiceUID, GID: guestevidence.ServiceGID, ChildCreated: true, ChildRemoved: true}
	for _, name := range guestevidence.CellsProbeFiles {
		p.Files = append(p.Files, guestevidence.CellsProbeFile{Name: name, Exists: true, Writable: true})
	}
	return p
}

func TestProbeCapabilityReadsTheDelegationChain(t *testing.T) {
	env := geCgroupTree(t, true, "domain")
	c, err := probeCapability(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Cgroup.Chain) != len(guestevidence.CapabilityChain) {
		t.Fatalf("chain = %+v", c.Cgroup.Chain)
	}
	for i, level := range c.Cgroup.Chain {
		if level.Path != guestevidence.CapabilityChain[i] || strings.Join(level.Controllers, ",") != "cpuset,cpu,io,memory,pids" ||
			strings.Join(level.SubtreeControl, ",") != "cpu,memory,pids" {
			t.Errorf("level %d = %+v", i, level)
		}
	}
	if last := c.Cgroup.Chain[len(c.Cgroup.Chain)-1]; last.Type != "domain" {
		t.Errorf("cells type = %q", last.Type)
	}
	if c.Cgroup.CellsProbe.UID != guestevidence.ServiceUID || len(c.Cgroup.CellsProbe.Files) != len(guestevidence.CellsProbeFiles) {
		t.Errorf("cells probe = %+v", c.Cgroup.CellsProbe)
	}
}

func TestProbeCapabilityChainMissingFactsAreNotInvented(t *testing.T) {
	for name, remove := range map[string]string{
		"slice does not enable controllers": "system.slice/cgroup.subtree_control",
		"service has no controllers file":   "system.slice/tbound-pi.service/cgroup.controllers",
		"cells has no subtree_control":      "system.slice/tbound-pi.service/cells/cgroup.subtree_control",
		"cells has no type":                 "system.slice/tbound-pi.service/cells/cgroup.type",
	} {
		env := geCgroupTree(t, true, "domain")
		root := filepath.Dir(env.seccompProfile)
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(remove))); err != nil {
			t.Fatal(err)
		}
		c, err := probeCapability(env)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if guestevidence.Validate(guestevidence.KindCapability, mustEncodeGE(t, guestevidence.KindCapability, c), guestevidence.Expect{Commit: geCommit, BinarySHA256: geDigest("self")}) == nil {
			t.Errorf("%s: a guest with this gap produced a valid capability artifact", name)
		}
	}
	env := geCgroupTree(t, true, "domain")
	if err := os.RemoveAll(filepath.Join(filepath.Dir(env.seccompProfile), "system.slice", "tbound-pi.service", "cells")); err != nil {
		t.Fatal(err)
	}
	if _, err := probeCapability(env); err == nil {
		t.Error("a guest without a cells cgroup was probed")
	}
}

func TestCellsChildProbeReportsWhatTheIdentityCanOpen(t *testing.T) {
	cells := t.TempDir()
	populate := func(writable map[string]bool) func(string) func() {
		return func(child string) func() {
			for _, name := range guestevidence.CellsProbeFiles {
				if name == "memory.max" {
					continue // absent: the controller is not enabled
				}
				mode := os.FileMode(0o644)
				if !writable[name] {
					mode = 0o444
				}
				if err := os.WriteFile(filepath.Join(child, name), nil, mode); err != nil {
					t.Fatal(err)
				}
			}
			return func() { // a real cgroup2 child has no files of its own to remove
				for _, name := range guestevidence.CellsProbeFiles {
					os.Remove(filepath.Join(child, name))
				}
			}
		}
	}
	p, err := probeCellsChild(cells, populate(map[string]bool{"cgroup.kill": true, "cgroup.procs": true, "cpu.max": true}))
	if err != nil {
		t.Fatal(err)
	}
	if !p.ChildCreated || !p.ChildRemoved || p.UID != os.Geteuid() || p.GID != os.Getegid() {
		t.Fatalf("probe = %+v", p)
	}
	got := map[string]guestevidence.CellsProbeFile{}
	for i, f := range p.Files {
		if f.Name != guestevidence.CellsProbeFiles[i] {
			t.Fatalf("files out of order: %+v", p.Files)
		}
		got[f.Name] = f
	}
	if !got["cgroup.kill"].Exists || !got["cpu.max"].Exists || got["memory.max"].Exists {
		t.Errorf("existence = %+v", got)
	}
	if os.Geteuid() != 0 && got["pids.max"].Writable {
		t.Errorf("a read-only pids.max was reported writable: %+v", got["pids.max"])
	}
	if !got["cgroup.kill"].Writable || got["memory.max"].Writable {
		t.Errorf("writability = %+v", got)
	}
	if entries, _ := os.ReadDir(cells); len(entries) != 0 {
		t.Errorf("the probe left %d entries in the cells cgroup", len(entries))
	}
	if _, err := probeCellsChild(filepath.Join(cells, "none"), nil); err == nil {
		t.Error("a missing cells cgroup was probed")
	}
}

func TestCellsProbeAsServiceNeedsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the identity switch would succeed")
	}
	cells := t.TempDir()
	if _, err := probeCellsAsService(cells); err == nil {
		t.Fatal("a non-root producer claimed to probe as the service identity")
	}
	if entries, _ := os.ReadDir(cells); len(entries) != 0 {
		t.Errorf("a refused probe left %d entries", len(entries))
	}
	// The switch happened on one thread only: this goroutine keeps its identity.
	if os.Geteuid() == guestevidence.ServiceUID {
		t.Fatal("the identity switch leaked into the test thread")
	}
}

func withBinary(e guestevidence.Expect, binary string) guestevidence.Expect {
	e.BinarySHA256 = binary
	return e
}

// A G1 run has a config, registration, template and signed profile of its own. Its artifact is accepted
// although none of them is the pilot's, and refused when the run does not share the pilot's base cell, source,
// image or admin key, or when the profile is not the one the receipt names.
func TestGuestEvidenceProviderExchangeIsBoundToWhatTheRunSharesWithThePilot(t *testing.T) {
	dir := t.TempDir()
	w := newBindWorld(t, dir)
	response := filepath.Join(dir, "response.txt")
	if err := os.WriteFile(response, []byte(geResponse(t, w, nil)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := productionRouteProbeEnv()
	ex, err := providerExchangeBound(response, w.files, w.pins, env)
	if err != nil {
		t.Fatalf("a run under its own profile is refused: %v", err)
	}
	if ex.Binding.ProfileDigest != w.profileDigest || ex.Binding.TemplateDigest != w.templateDigest ||
		ex.Binding.BaseTemplateDigest != w.pins.BaseTemplateDigest || !guestevidence.IsDigest(ex.Binding.RegistrationDigest) ||
		ex.Binding.RunnerDigest != w.pins.RunnerDigest || ex.Binding.RuntimeBundleDigest != w.pins.RuntimeBundleDigest {
		t.Errorf("binding = %+v", ex.Binding)
	}
	if ex.Binding.TemplateDigest == ex.Binding.BaseTemplateDigest {
		t.Error("the run's template is the base template: it was not given a registration of its own")
	}
	for name, mutate := range map[string]func(*guestevidence.RunBinding){
		"another base cell":      func(p *guestevidence.RunBinding) { p.BaseTemplateDigest = geDigest("another base") },
		"another source":         func(p *guestevidence.RunBinding) { p.FrozenSourceDigest = geDigest("another source") },
		"another image":          func(p *guestevidence.RunBinding) { p.ImageDigest = geDigest("another image") },
		"another admin key":      func(p *guestevidence.RunBinding) { p.AdminPubkeyDigest = geDigest("another key") },
		"another runtime bundle": func(p *guestevidence.RunBinding) { p.RuntimeBundleDigest = geDigest("another bundle") },
		"another runner (not the build of the frozen commit)": func(p *guestevidence.RunBinding) { p.RunnerDigest = geDigest("another runner") },
	} {
		pins := w.pins
		mutate(&pins)
		if _, err := providerExchangeBound(response, w.files, pins, env); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The receipt names a profile other than the signed one.
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(other, []byte(geResponse(t, w, func(r map[string]any) { r["profile_hash"] = geDigest("another profile") })+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := providerExchangeBound(other, w.files, w.pins, env); err == nil {
		t.Error("a receipt of another profile than the signed one was accepted")
	}
}

func TestRunGuestTestsStopsAHangingBinary(t *testing.T) {
	old := guestTestsTimeout
	guestTestsTimeout = time.Second
	t.Cleanup(func() { guestTestsTimeout = old })
	bin := filepath.Join(t.TempDir(), "procoracle.test")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, bin, ""); err == nil {
		t.Fatal("a binary killed at the timeout was recorded as a run")
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("the hanging binary was stopped only after %s", took)
	}
}

// The real-cgroup tests skip without the guard variable and refuse a login-session cgroup, so the
// producer runs them, and only them, in a fresh capped cgroup with the variable set, and removes it.
func TestRunGuestTestsRunsTheRealCgroupTestsInAFreshCgroupAndRemovesIt(t *testing.T) {
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Skip(err)
	}
	mount, err := cgroup2Mount(string(info))
	if err != nil {
		t.Skip(err)
	}
	own := ""
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if p, ok := strings.CutPrefix(line, "0::"); ok {
				own = p
			}
		}
	}
	if why := cgroupguard.RefuseBase(own); why != "" {
		t.Skipf("%s: run under systemd-run --user --scope -p Delegate=yes -p TasksMax=512 -p MemoryMax=2G; the fresh-cgroup run is NOT verified here", why)
	}
	if _, err := os.Stat(filepath.Join(mount, own, "pids.max")); err != nil {
		t.Skipf("no cgroup2 pids controller on %q (hybrid hierarchy?): the fresh-cgroup run is NOT verified here", own)
	}
	leftover := filepath.Join(mount, own, "tbound-guest-tests-leftover")
	if err := os.MkdirAll(filepath.Join(leftover, "procoracle-1"), 0o755); errors.Is(err, fs.ErrPermission) {
		// A hosted CI runner is not root, and its service cgroup is not delegated to it.
		t.Skipf("uid %d cannot create a cgroup below %q (not delegated): run under systemd-run -p Delegate=yes -p User=<uid>; the fresh-cgroup run is NOT verified here: %v", os.Geteuid(), own, err)
	} else if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	script := "#!/bin/sh\nprintf 'guard=%s\\n' \"$" + cgroupguard.Env + "\" > " + seen + "\ncat /proc/self/cgroup >> " + seen + "\n"
	bin := filepath.Join(dir, "procoracle.test")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, bin, filepath.Join(mount, own)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(seen)
	_, cg, _ := strings.Cut(string(data), "0::")
	cg = strings.TrimSpace(cg)
	if !strings.HasPrefix(string(data), "guard=1\n") || !strings.HasPrefix(cg, strings.TrimSuffix(own, "/")+"/tbound-guest-tests-") {
		t.Fatalf("the test binary did not run with the guard in a fresh child cgroup of %q:\n%s", own, data)
	}
	if _, err := os.Stat(filepath.Join(mount, cg)); !os.IsNotExist(err) {
		t.Fatalf("the fresh cgroup %q was not removed: %v", cg, err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("the leftover cgroup of an earlier run was not removed: %v", err)
	}
	// Without a cgroup parent nothing is created and the guard is not set.
	if _, err := runGuestTests(context.Background(), guestevidence.KindProcOracleTests, bin, ""); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(seen); !strings.HasPrefix(string(data), "guard=\n") {
		t.Fatalf("the guard was set without a fresh cgroup:\n%s", data)
	}
}
