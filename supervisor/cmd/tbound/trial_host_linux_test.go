//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/trialfix"
)

const (
	hostTestRuleFile  = "../../../infra/guest/nftables/tbound-net-sink.nft"
	hostTestCellsPath = "/sys/fs/cgroup/system.slice/tbound-pi.service/cells"
	hostTestBootID    = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	hostTestCID       = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// nftJSON is what `nft -j list table inet tbound_net_sink` prints for the pinned rule file.
func nftJSON(t *testing.T, mutate func(items []map[string]any) []map[string]any) []byte {
	t.Helper()
	path, level, err := piruntime.D09NetSinkNftMatch(hostTestCellsPath)
	if err != nil {
		t.Fatal(err)
	}
	cg := map[string]any{"match": map[string]any{"op": "==", "left": map[string]any{"socket": map[string]any{"key": "cgroupv2", "level": level}}, "right": path}}
	daddr := map[string]any{"match": map[string]any{"op": "==", "left": map[string]any{"payload": map[string]any{"protocol": "ip", "field": "daddr"}}, "right": piruntime.D09NetSinkAddress}}
	dport := map[string]any{"match": map[string]any{"op": "==", "left": map[string]any{"payload": map[string]any{"protocol": "tcp", "field": "dport"}}, "right": piruntime.D09NetSinkPort}}
	items := []map[string]any{
		{"metainfo": map[string]any{"version": "1.0.9", "release_name": "x", "json_schema_version": 1}},
		{"table": map[string]any{"family": "inet", "name": piruntime.D09NetSinkTable, "handle": 7}},
		{"chain": map[string]any{"family": "inet", "table": piruntime.D09NetSinkTable, "name": "output", "handle": 1, "type": "filter", "hook": "output", "prio": 0, "policy": "accept"}},
		{"rule": map[string]any{"family": "inet", "table": piruntime.D09NetSinkTable, "chain": "output", "handle": 2, "expr": []any{cg, daddr, dport, map[string]any{"accept": nil}}}},
		{"rule": map[string]any{"family": "inet", "table": piruntime.D09NetSinkTable, "chain": "output", "handle": 3, "expr": []any{cg, map[string]any{"counter": map[string]any{"packets": 3, "bytes": 180}}, map[string]any{"drop": nil}}}},
	}
	if mutate != nil {
		items = mutate(items)
	}
	data, err := json.Marshal(map[string]any{"nftables": items})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVerifyNetSinkRulesetAcceptsExactlyTheExpectedRules(t *testing.T) {
	path, level, _ := piruntime.D09NetSinkNftMatch(hostTestCellsPath)
	if err := verifyNetSinkRuleset(nftJSON(t, nil), path, level); err != nil {
		t.Fatalf("the expected table was refused: %v", err)
	}
	rule := func(items []map[string]any, i int) map[string]any { return items[i]["rule"].(map[string]any) }
	exprs := func(items []map[string]any, i int) []any { return rule(items, i)["expr"].([]any) }
	match := func(items []map[string]any, i, j int) map[string]any {
		return exprs(items, i)[j].(map[string]any)["match"].(map[string]any)
	}
	cases := map[string]func([]map[string]any) []map[string]any{
		"no rules":   func(items []map[string]any) []map[string]any { return items[:3] },
		"no drop":    func(items []map[string]any) []map[string]any { return items[:4] },
		"extra rule": func(items []map[string]any) []map[string]any { return append(items, items[4]) },
		"extra accept all": func(items []map[string]any) []map[string]any {
			return append(items, map[string]any{"rule": map[string]any{"family": "inet", "table": piruntime.D09NetSinkTable, "chain": "output", "expr": []any{map[string]any{"accept": nil}}}})
		},
		"drop before accept": func(items []map[string]any) []map[string]any { items[3], items[4] = items[4], items[3]; return items },
		"other port": func(items []map[string]any) []map[string]any {
			match(items, 3, 2)["right"] = 8081
			return items
		},
		"other address": func(items []map[string]any) []map[string]any {
			match(items, 3, 1)["right"] = "127.0.0.1"
			return items
		},
		"other cgroup": func(items []map[string]any) []map[string]any {
			match(items, 4, 0)["right"] = "system.slice/other"
			return items
		},
		"other level": func(items []map[string]any) []map[string]any {
			match(items, 3, 0)["left"].(map[string]any)["socket"].(map[string]any)["level"] = 3
			return items
		},
		"drop is accept": func(items []map[string]any) []map[string]any {
			exprs(items, 4)[2] = map[string]any{"accept": nil}
			return items
		},
		"chain policy drop": func(items []map[string]any) []map[string]any {
			items[2]["chain"].(map[string]any)["policy"] = "drop"
			return items
		},
		"chain hook input": func(items []map[string]any) []map[string]any {
			items[2]["chain"].(map[string]any)["hook"] = "input"
			return items
		},
		"other table": func(items []map[string]any) []map[string]any {
			items[1]["table"].(map[string]any)["name"] = "other"
			return items
		},
		"second chain": func(items []map[string]any) []map[string]any {
			return append(items[:3], append([]map[string]any{items[2]}, items[3:]...)...)
		},
	}
	for name, mutate := range cases {
		if err := verifyNetSinkRuleset(nftJSON(t, mutate), path, level); err == nil {
			t.Errorf("%s: the table was accepted", name)
		}
	}
	for _, bad := range []string{"", "not json", `{"nftables":[]}`, `{}`} {
		if err := verifyNetSinkRuleset([]byte(bad), path, level); err == nil {
			t.Errorf("listing %q was accepted", bad)
		}
	}
}

type fakeNft struct {
	t        *testing.T
	calls    [][]string
	stdins   [][]byte
	loadErr  error
	listJSON []byte
	listErr  error
}

func (f *fakeNft) run(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	f.stdins = append(f.stdins, stdin)
	switch strings.Join(args, " ") {
	case "-f -":
		return nil, f.loadErr
	case "-j list table inet " + piruntime.D09NetSinkTable:
		return f.listJSON, f.listErr
	}
	f.t.Fatalf("unexpected nft call %v", args)
	return nil, nil
}

type hostFixture struct {
	host   trialHost
	nft    *fakeNft
	cells  string // the directory standing for the cells cgroup
	attest string
	rule   string
	cell   evidenceCell
}

func newHostFixture(t *testing.T) *hostFixture {
	t.Helper()
	dir := t.TempDir()
	rule, err := os.ReadFile(hostTestRuleFile)
	if err != nil {
		t.Fatal(err)
	}
	f := &hostFixture{cells: filepath.Join(dir, "cells"), attest: filepath.Join(dir, "net-sink.attest"), rule: filepath.Join(dir, "net-sink.nft")}
	if err := os.Mkdir(f.cells, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.rule, rule, 0o644); err != nil {
		t.Fatal(err)
	}
	f.nft = &fakeNft{t: t, listJSON: nftJSON(t, nil)}
	f.cell = evidenceCell{ID: "cells", CgroupPath: hostTestCellsPath}
	f.host = trialHost{
		nft: f.nft.run, ruleFile: f.rule, attestFile: f.attest, rootUID: uint32(os.Geteuid()),
		owner:      trialfix.Owner{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())},
		bootID:     func() (string, error) { return hostTestBootID, nil },
		openCells:  func(evidenceCell) (*os.File, error) { return os.Open(f.cells) },
		checkFS:    func(*os.File) error { return nil },
		runtimeDir: dir,
	}
	return f
}

func (f *hostFixture) parent(t *testing.T) *os.File {
	t.Helper()
	dir, err := os.Open(filepath.Join(f.cells, piruntime.D09NetSinkCgroupName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dir.Close() })
	return dir
}

func (f *hostFixture) attestation(t *testing.T) piruntime.D09NetSinkAttestation {
	t.Helper()
	raw, err := os.ReadFile(f.attest)
	if err != nil {
		t.Fatalf("no attestation: %v", err)
	}
	var a piruntime.D09NetSinkAttestation
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEnsureNetSinkCreatesTheParentLoadsTheRuleAndAttests(t *testing.T) {
	f := newHostFixture(t)
	if err := f.host.ensureNetSink(context.Background(), f.cell); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(f.cells, piruntime.D09NetSinkCgroupName))
	if err != nil || !info.IsDir() {
		t.Fatalf("the net-sink parent was not created: %v", err)
	}
	if st := info.Sys().(*syscall.Stat_t); st.Uid != f.host.owner.UID || st.Gid != f.host.owner.GID {
		t.Fatalf("parent owner %d:%d, want the service identity", st.Uid, st.Gid)
	}
	rule, _ := os.ReadFile(hostTestRuleFile)
	if len(f.nft.calls) != 2 || strings.Join(f.nft.calls[0], " ") != "-f -" || !bytes.Equal(f.nft.stdins[0], rule) ||
		strings.Join(f.nft.calls[1], " ") != "-j list table inet tbound_net_sink" {
		t.Fatalf("nft calls %v: want one atomic load of the pinned file from stdin, then the read-back", f.nft.calls)
	}
	want, err := piruntime.NewD09NetSinkAttestation(hostTestBootID, f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.attestation(t); got != want {
		t.Fatalf("attestation %+v, want %+v", got, want)
	}
	if st, err := os.Stat(f.attest); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("attestation mode: %v %v", st, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(f.attest)); len(entries) != 3 { // cells, rule, attestation: no temp file
		t.Fatalf("leftover files: %v", entries)
	}
}

// A reboot or a recreated parent cgroup is repaired by the next reset: the table is loaded again for
// the directory that exists now and the attestation follows it.
func TestEnsureNetSinkFollowsARecreatedParent(t *testing.T) {
	f := newHostFixture(t)
	if err := f.host.ensureNetSink(context.Background(), f.cell); err != nil {
		t.Fatal(err)
	}
	first := f.attestation(t)
	// The old directory stays, so the new one cannot reuse its inode number.
	if err := os.Rename(filepath.Join(f.cells, piruntime.D09NetSinkCgroupName), filepath.Join(f.cells, "old")); err != nil {
		t.Fatal(err)
	}
	f.host.bootID = func() (string, error) { return "another-boot", nil }
	if err := f.host.ensureNetSink(context.Background(), f.cell); err != nil {
		t.Fatal(err)
	}
	second := f.attestation(t)
	if second.BootID != "another-boot" || second.ParentIno == first.ParentIno {
		t.Fatalf("attestation did not follow the new boot and parent: %+v then %+v", first, second)
	}
	if len(f.nft.calls) != 4 {
		t.Fatalf("the rule was not reloaded: %v", f.nft.calls)
	}
}

func TestEnsureNetSinkFailsClosedAndRemovesAStaleAttestation(t *testing.T) {
	cases := map[string]func(*hostFixture){
		"nft load fails":  func(f *hostFixture) { f.nft.loadErr = errors.New("netlink: cgroup not found") },
		"read-back fails": func(f *hostFixture) { f.nft.listErr = errors.New("no such table") },
		"table differs": func(f *hostFixture) {
			f.nft.listJSON = nftJSON(t, func(items []map[string]any) []map[string]any { return items[:4] })
		},
		"rule file altered": func(f *hostFixture) {
			if err := os.WriteFile(f.rule, []byte("table inet tbound_net_sink\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"rule file owner": func(f *hostFixture) { f.host.rootUID++ },
		"rule file writable": func(f *hostFixture) {
			if err := os.Chmod(f.rule, 0o666); err != nil {
				t.Fatal(err)
			}
		},
		"boot id unreadable": func(f *hostFixture) { f.host.bootID = func() (string, error) { return "", errors.New("no /proc") } },
		"cells cgroup not the registered one": func(f *hostFixture) {
			f.host.openCells = func(evidenceCell) (*os.File, error) { return nil, errors.New("inode differs") }
		},
		"parent not a cgroup":      func(f *hostFixture) { f.host.checkFS = func(*os.File) error { return errors.New("not cgroup2") } },
		"cells path not derivable": func(f *hostFixture) { f.cell.CgroupPath = "/tmp/cells" },
	}
	for name, set := range cases {
		f := newHostFixture(t)
		if err := os.WriteFile(f.attest, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
		set(f)
		if err := f.host.ensureNetSink(context.Background(), f.cell); err == nil {
			t.Errorf("%s: the reset went on", name)
		}
		if _, err := os.Stat(f.attest); !os.IsNotExist(err) {
			t.Errorf("%s: a stale attestation survived: %v", name, err)
		}
	}
}

func TestEnsureNetSinkIsSkippedWhenTheRuleFileIsNotInstalled(t *testing.T) {
	f := newHostFixture(t)
	if err := os.Remove(f.rule); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.attest, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.host.ensureNetSink(context.Background(), f.cell); err != nil {
		t.Fatalf("a guest without the rule file must still reset: %v", err)
	}
	if len(f.nft.calls) != 0 {
		t.Fatalf("nft ran without a rule file: %v", f.nft.calls)
	}
	if _, err := os.Stat(f.attest); !os.IsNotExist(err) {
		t.Fatalf("a stale attestation survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.cells, piruntime.D09NetSinkCgroupName)); err == nil {
		t.Fatal("the net-sink parent was created without a rule file")
	}
}

type fakeResetPodman struct {
	t       *testing.T
	calls   [][]string
	ids     []string // what ps -a lists; emptied by rm
	rmErr   error
	keep    bool // rm "succeeds" but leaves the containers
	psError error
	psRaw   string
}

func (p *fakeResetPodman) run(_ context.Context, args ...string) ([]byte, error) {
	p.calls = append(p.calls, args)
	switch args[0] {
	case "ps":
		if p.psError != nil {
			return nil, p.psError
		}
		if p.psRaw != "" {
			return []byte(p.psRaw), nil
		}
		return []byte(strings.Join(p.ids, "\n") + "\n"), nil
	case "rm":
		if p.rmErr != nil {
			return nil, p.rmErr
		}
		if !p.keep {
			p.ids = nil
		}
		return nil, nil
	}
	p.t.Fatalf("unexpected podman call %v", args)
	return nil, nil
}

func TestCleanNoReapRemovesLabelledContainersAndTheNrCgroups(t *testing.T) {
	f := newHostFixture(t)
	pod := &fakeResetPodman{t: t, ids: []string{hostTestCID}}
	f.host.podman = pod.run
	for _, name := range []string{"tbound-nr-aa/child/grandchild", "tbound-nr-bb", piruntime.D09NetSinkCgroupName, "tbound-cmd-cc", "worker"} {
		if err := os.MkdirAll(filepath.Join(f.cells, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.host.cleanNoReap(context.Background(), f.cell); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(f.cells)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if got := strings.Join(left, " "); got != "net-sink tbound-cmd-cc worker" {
		t.Fatalf("cgroups left: %q; the persistent net-sink parent and what is not tbound-nr-* must stay", got)
	}
	// The containers are found by the label, removed forcibly, and gone when asked again.
	if len(pod.calls) != 3 || strings.Join(pod.calls[0], " ") != "ps -a -q --no-trunc --filter label="+piruntime.D09NoReapContainerLabel ||
		strings.Join(pod.calls[1], " ") != "rm -f -t 0 "+hostTestCID || pod.calls[2][0] != "ps" {
		t.Fatalf("podman calls: %v", pod.calls)
	}
	// Nothing left to do is a no-op that still checks.
	if err := f.host.cleanNoReap(context.Background(), f.cell); err != nil {
		t.Fatal(err)
	}
}

func TestCleanNoReapFailsClosed(t *testing.T) {
	cases := map[string]func(*hostFixture, *fakeResetPodman){
		"podman rm fails":   func(_ *hostFixture, p *fakeResetPodman) { p.rmErr = errors.New("cannot remove") },
		"container remains": func(_ *hostFixture, p *fakeResetPodman) { p.keep = true },
		"podman ps fails":   func(_ *hostFixture, p *fakeResetPodman) { p.psError = errors.New("no runroot") },
		"id is not an id":   func(_ *hostFixture, p *fakeResetPodman) { p.psRaw = "--force\n" },
		"cgroup in use": func(f *hostFixture, _ *fakeResetPodman) {
			if err := os.WriteFile(filepath.Join(f.cells, "tbound-nr-aa", "cgroup.procs"), []byte("1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"not a cgroup": func(f *hostFixture, _ *fakeResetPodman) {
			f.host.checkFS = func(*os.File) error { return errors.New("not cgroup2") }
		},
		"entry is not a directory": func(f *hostFixture, _ *fakeResetPodman) {
			if err := os.WriteFile(filepath.Join(f.cells, "tbound-nr-file"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, set := range cases {
		f := newHostFixture(t)
		pod := &fakeResetPodman{t: t, ids: []string{hostTestCID}}
		f.host.podman = pod.run
		if err := os.MkdirAll(filepath.Join(f.cells, "tbound-nr-aa"), 0o755); err != nil {
			t.Fatal(err)
		}
		set(f, pod)
		if err := f.host.cleanNoReap(context.Background(), f.cell); err == nil {
			t.Errorf("%s: the reset went on", name)
		}
	}
}

// Without the service's runtime directory there is no podman state to ask; the cgroups still go.
func TestCleanNoReapWithoutTheRuntimeDirSkipsPodman(t *testing.T) {
	f := newHostFixture(t)
	pod := &fakeResetPodman{t: t}
	f.host.podman = pod.run
	f.host.runtimeDir = filepath.Join(t.TempDir(), "absent")
	if err := os.Mkdir(filepath.Join(f.cells, "tbound-nr-aa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.host.cleanNoReap(context.Background(), f.cell); err != nil {
		t.Fatal(err)
	}
	if len(pod.calls) != 0 {
		t.Fatalf("podman ran: %v", pod.calls)
	}
	if _, err := os.Stat(filepath.Join(f.cells, "tbound-nr-aa")); !os.IsNotExist(err) {
		t.Fatalf("the cgroup stayed: %v", err)
	}
}

// The reset's podman is the service's: its uid and gid, its storage and runroot flags, its environment.
func TestServicePodmanCommandRunsAsTheServiceWithItsStorage(t *testing.T) {
	cmd := servicePodmanCommand(context.Background(), 995, 986, "ps", "-a")
	wantArgs := []string{"/usr/bin/podman", "--root", piruntime.D09ManagedPiPodmanGraphroot, "--runroot", piruntime.D09ManagedPiPodmanRunroot, "ps", "-a"}
	if strings.Join(cmd.Args, " ") != strings.Join(wantArgs, " ") || cmd.Path != "/usr/bin/podman" {
		t.Fatalf("args %v", cmd.Args)
	}
	if cred := cmd.SysProcAttr.Credential; cred == nil || cred.Uid != 995 || cred.Gid != 986 || cred.NoSetGroups || len(cred.Groups) != 0 {
		t.Fatalf("credential %+v", cmd.SysProcAttr.Credential)
	}
	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{"XDG_RUNTIME_DIR=/run/tbound-pi", "HOME=/var/lib/tbound/podman-home", "USER=tbound-pi",
		"CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf", "CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf",
		"XDG_CONFIG_HOME=/etc/tbound/podman-config", "TMPDIR=/run/tbound-pi/tmp", "XDG_CACHE_HOME=/run/tbound-pi/cache"} {
		if !strings.Contains(env, want) {
			t.Errorf("environment lacks %s", want)
		}
	}
	if len(cmd.Env) != 13 || cmd.Dir != "/" {
		t.Errorf("the environment must be the service's closed list (got %d entries), dir %q", len(cmd.Env), cmd.Dir)
	}
}
