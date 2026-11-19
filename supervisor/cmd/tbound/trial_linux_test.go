//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/researchroute"
	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/wsoracle"
)

const trialTestCase = "adv-TM-A-001"

// trialTestInCellOffset keeps the in-cell uid and gid of the test cell apart from the host
// identity that owns the fixtures, as under the signed single-own-uid-gid mapping.
const trialTestInCellOffset = 5000

func trialTestSpec(content string) trialfix.Spec {
	return trialfix.Spec{Schema: trialfix.Schema, Entries: []trialfix.Entry{
		{Path: "src", Type: trialfix.TypeDir, Mode: "0755"},
		{Path: "src/main.go", Type: trialfix.TypeFile, Mode: "0644", ContentB64: base64.StdEncoding.EncodeToString([]byte(content))},
		{Path: "run.sh", Type: trialfix.TypeFile, Mode: "0755", ContentB64: base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\n"))},
	}}
}

func trialTestArtifact(t *testing.T, spec trialfix.Spec) (string, *fstest.MapFile) {
	t.Helper()
	digest, err := trialfix.Digest(spec)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := corpusadmit.FixtureArtifactName(digest)
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return name, &fstest.MapFile{Data: raw}
}

// trialTestEnv is a reset against a temporary live root, owned by the test user, that the
// fake cell, ledger and process scan all agree is idle.
type trialTestEnv struct {
	trialEnv
	root       string
	spec       trialfix.Spec
	digest     string // the fixture digest the pinned corpus gives trialTestCase
	files      fstest.MapFS
	pendingErr error
	// usdStopErr is what the research ledger's check before the unit returns.
	usdStopErr error
	// populated is what the fake cells cgroup reports.
	populated bool
	killed    []string // cgroup paths the kill ran on
	// dirtyAtKill records whether the old content was still in the root when the kill ran.
	dirtyAtKill bool
	killErr     error
	snapshotFn  func(root, label string) wsoracle.Manifest
	// snapshotDirFn replaces the digest snapshot of the pinned root.
	snapshotDirFn func(dir *os.File, label string) wsoracle.Manifest
	// events is the order in which the reset used its seams.
	events     []string
	lockErr    error
	noReapErr  error
	netSinkErr error
	// leftovers and leftoverErr are what the fake leftover-container query returns.
	leftovers   []string
	leftoverErr error
}

func newTrialTestEnv(t *testing.T) *trialTestEnv {
	t.Helper()
	root := filepath.Join(t.TempDir(), "live")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "left-over"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &trialTestEnv{root: root, spec: trialTestSpec("package main\n")}
	name, file := trialTestArtifact(t, e.spec)
	e.files = fstest.MapFS{name: file}
	e.digest, _ = trialfix.Digest(e.spec)
	owner := trialfix.Owner{UID: st.Uid, GID: st.Gid}
	e.trialEnv = trialEnv{
		geteuid: func() int { return 0 },
		getenv:  func(string) string { return "" },
		fixtureDigest: func(_ context.Context, id string) (string, error) {
			if id != trialTestCase {
				return "", errors.New("unknown case")
			}
			return e.digest, nil
		},
		artifacts: e.files,
		liveRoot:  root,
		owner:     owner,
		registeredRoot: func(context.Context) (uint64, uint64, error) {
			var st syscall.Stat_t
			if err := syscall.Stat(root, &st); err != nil {
				return 0, 0, err
			}
			return uint64(st.Dev), st.Ino, nil
		},
		cell: func(context.Context) (evidenceCell, error) {
			return evidenceCell{ID: "cells", CgroupPath: "/cells", Hierarchy: "/cells", Inode: 1,
				UID: owner.UID + trialTestInCellOffset, GID: owner.GID + trialTestInCellOffset, HostUID: owner.UID, HostGID: owner.GID}, nil
		},
		lock: func() (func(), error) {
			if e.lockErr != nil {
				return nil, e.lockErr
			}
			e.events = append(e.events, "lock")
			return func() { e.events = append(e.events, "unlock") }, nil
		},
		pending: func(context.Context) error {
			e.events = append(e.events, "pending")
			return e.pendingErr
		},
		usdStop: func(context.Context) error { return e.usdStopErr },
		cleanNoReap: func(context.Context, evidenceCell) error {
			e.events = append(e.events, "no-reap")
			return e.noReapErr
		},
		netSink: func(context.Context, evidenceCell) error {
			e.events = append(e.events, "net-sink")
			return e.netSinkErr
		},
		leftoverCells: func(context.Context) ([]string, error) {
			e.events = append(e.events, "leftovers")
			return e.leftovers, e.leftoverErr
		},
		cellPopulated: func(evidenceCell) (bool, error) { return e.populated, nil },
		killCell: func(cell evidenceCell) error {
			e.events = append(e.events, "kill")
			e.killed = append(e.killed, cell.CgroupPath)
			_, err := os.Stat(filepath.Join(root, "left-over"))
			e.dirtyAtKill = err == nil
			return e.killErr
		},
		snapshot: func(r, label string) wsoracle.Manifest {
			e.events = append(e.events, "verify")
			if e.snapshotFn != nil {
				return e.snapshotFn(r, label)
			}
			return wsoracle.Snapshot(r, label)
		},
		snapshotDir: func(dir *os.File, label string) wsoracle.Manifest {
			e.events = append(e.events, "verify")
			if e.snapshotDirFn != nil {
				return e.snapshotDirFn(dir, label)
			}
			return wsoracle.SnapshotDir(dir, label)
		},
	}
	return e
}

func (e *trialTestEnv) run(t *testing.T, args ...string) (trialfix.ResetDoc, string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runTrial(context.Background(), args, &out, e.trialEnv)
	var doc trialfix.ResetDoc
	if err == nil {
		if derr := json.Unmarshal(bytes.TrimSuffix(out.Bytes(), []byte("\n")), &doc); derr != nil {
			t.Fatalf("output %q is not a ResetDoc: %v", out.String(), derr)
		}
	}
	return doc, out.String(), err
}

func (e *trialTestEnv) rootUntouched(t *testing.T) {
	t.Helper()
	if got, err := os.ReadFile(filepath.Join(e.root, "left-over")); err != nil || string(got) != "old" {
		t.Fatalf("the live root was changed by a refused reset: %q, %v", got, err)
	}
}

func TestTrialResetWipesPopulatesAndReportsTheDigests(t *testing.T) {
	e := newTrialTestEnv(t)
	doc, _, err := e.run(t, "reset", trialTestCase)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	want, _ := trialfix.DigestOwned(e.spec, e.owner)
	if doc.Schema != trialfix.ResetSchema || doc.CaseID != trialTestCase || doc.FixtureDigest != e.digest || doc.TreeDigest != want || doc.Check {
		t.Fatalf("doc = %+v, want tree digest %s", doc, want)
	}
	if _, err := os.Stat(filepath.Join(e.root, "left-over")); !os.IsNotExist(err) {
		t.Fatalf("the old content survived: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(e.root, "src", "main.go")); err != nil || string(got) != "package main\n" {
		t.Fatalf("src/main.go = %q, %v", got, err)
	}
	// The processes left in the cells cgroup are killed, and before the tree is wiped, so that one
	// cannot write into the fresh tree.
	if len(e.killed) != 1 || e.killed[0] != "/cells" || !e.dirtyAtKill {
		t.Fatalf("kills = %v, dirty at kill %v", e.killed, e.dirtyAtKill)
	}
	// A second reset gives the same document.
	again, _, err := e.run(t, "reset", trialTestCase)
	if err != nil || again != doc {
		t.Fatalf("second reset: %+v, %v", again, err)
	}
}

func TestTrialResetCheckOnlyVerifiesPreconditionsAndTheArtifact(t *testing.T) {
	e := newTrialTestEnv(t)
	doc, _, err := e.run(t, "reset", trialTestCase, "--check")
	if err != nil {
		t.Fatalf("reset --check: %v", err)
	}
	if doc.Schema != trialfix.ResetSchema || doc.CaseID != trialTestCase || doc.FixtureDigest != e.digest || doc.TreeDigest != "" || !doc.Check {
		t.Fatalf("doc = %+v", doc)
	}
	e.rootUntouched(t)
	if len(e.killed) != 0 {
		t.Fatalf("--check killed %v", e.killed)
	}
}

func TestTrialResetCheckProductionReadinessIgnoresOnlyNoReapSurvivors(t *testing.T) {
	e := newTrialTestEnv(t)
	_, path := fakeCgroupDir(t, true, "populated 1\n")
	if err := os.WriteFile(filepath.Join(path, "cgroup.procs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	addScope := func(name string) {
		t.Helper()
		child := filepath.Join(path, name)
		if err := os.Mkdir(child, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(child, "cgroup.events"), []byte("populated 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	addScope("tbound-nr-aa")
	old := openResetCheckCgroup
	t.Cleanup(func() { openResetCheckCgroup = old })
	openResetCheckCgroup = func(evidenceCell) (*os.File, error) { return os.Open(path) }
	// Keep the production --check predicate: replacing it with the helper would miss a wiring regression.
	e.cellPopulated = productionTrialEnv().cellPopulated
	doc, _, err := e.run(t, "reset", trialTestCase, "--check")
	if err != nil || !doc.Check || doc.FixtureDigest != e.digest {
		t.Fatalf("--check with only no-reap survivors: doc %+v, err %v", doc, err)
	}
	addScope("tbound-cmd-bb")
	if _, out, err := e.run(t, "reset", trialTestCase, "--check"); err == nil || !strings.Contains(err.Error(), "cells cgroup holds processes") || out != "" {
		t.Fatalf("--check with a live command scope: err %v, output %q; want a refusal", err, out)
	}
	e.rootUntouched(t)
	if got := strings.Join(e.events, ","); got != "lock,pending,leftovers,unlock,lock,pending,leftovers,unlock" {
		t.Fatalf("--check did work beyond readiness: %s", got)
	}
}

// A populated cells cgroup is a refusal of --check, and the work of the reset: it is killed
// before the tree is wiped, and the reset goes on.
func TestTrialResetKillsAPopulatedCellsCgroupAndCheckRefuses(t *testing.T) {
	e := newTrialTestEnv(t)
	e.populated = true
	if _, out, err := e.run(t, "reset", trialTestCase, "--check"); err == nil || !strings.Contains(err.Error(), "cells cgroup") || out != "" {
		t.Fatalf("--check: err %v, output %q; want a refusal naming the cells cgroup", err, out)
	}
	e.rootUntouched(t)
	if len(e.killed) != 0 {
		t.Fatalf("--check killed %v", e.killed)
	}
	if _, _, err := e.run(t, "reset", trialTestCase); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if len(e.killed) != 1 || !e.dirtyAtKill {
		t.Fatalf("kills = %v, dirty at kill %v", e.killed, e.dirtyAtKill)
	}
}

func TestTrialResetRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(e *trialTestEnv)
		want   string
	}{
		"not root": {func(e *trialTestEnv) { e.geteuid = func() int { return 1000 } }, "uid 0"},
		"unknown case": {func(e *trialTestEnv) {
			e.digest = ""
			e.fixtureDigest = func(context.Context, string) (string, error) { return "", errors.New("unknown case") }
		}, "unknown case"},
		"artifact missing": {func(e *trialTestEnv) {
			e.files = fstest.MapFS{}
			e.artifacts = e.files
		}, "fixture artifact"},
		"artifact of another fixture": {func(e *trialTestEnv) {
			name, _ := corpusadmit.FixtureArtifactName(e.digest)
			_, other := trialTestArtifact(t, trialTestSpec("other\n"))
			e.files[name] = other
		}, "digests to"},
		"workflow pending": {func(e *trialTestEnv) { e.pendingErr = errors.New("workflow abc is not terminal") }, "not terminal"},
		"cell host uid is not the fixture owner": {func(e *trialTestEnv) {
			e.cell = func(context.Context) (evidenceCell, error) {
				return evidenceCell{CgroupPath: "/cells", UID: 1000, GID: 1000, HostUID: e.owner.UID + 1, HostGID: e.owner.GID}, nil
			}
		}, "fixture owner"},
		"cell host gid is not the fixture owner": {func(e *trialTestEnv) {
			e.cell = func(context.Context) (evidenceCell, error) {
				return evidenceCell{CgroupPath: "/cells", UID: 1000, GID: 1000, HostUID: e.owner.UID, HostGID: e.owner.GID + 1}, nil
			}
		}, "fixture owner"},
		"cell has no host identity": {func(e *trialTestEnv) {
			e.cell = func(context.Context) (evidenceCell, error) {
				return evidenceCell{CgroupPath: "/cells", UID: e.owner.UID, GID: e.owner.GID}, nil
			}
		}, "fixture owner"},
		"cell unresolvable": {func(e *trialTestEnv) {
			e.cell = func(context.Context) (evidenceCell, error) { return evidenceCell{}, errors.New("no registration") }
		}, "no registration"},
		"cgroup unreadable": {func(e *trialTestEnv) {
			e.cellPopulated = func(evidenceCell) (bool, error) { return false, errors.New("cgroup.events unreadable") }
		}, "unreadable"},
		// procoracle F1: an acceptance or V cell left running would score as a cell escape.
		"leftover V cell": {func(e *trialTestEnv) {
			e.leftovers = []string{"tbound-v-0123456789abcdef"}
		}, "tbound-v-0123456789abcdef"},
		"leftover acceptance cell": {func(e *trialTestEnv) {
			e.leftovers = []string{"tbound-accept-0123456789abcdef"}
		}, "tbound-accept-0123456789abcdef"},
		"container list unavailable": {func(e *trialTestEnv) {
			e.leftoverErr = errors.New("podman: no runroot")
		}, "no runroot"},
	} {
		for _, args := range [][]string{{"reset", trialTestCase}, {"reset", trialTestCase, "--check"}} {
			t.Run(name+" "+strings.Join(args[2:], ""), func(t *testing.T) {
				e := newTrialTestEnv(t)
				tc.mutate(e)
				_, out, err := e.run(t, args...)
				if err == nil || !strings.Contains(err.Error(), tc.want) || out != "" {
					t.Fatalf("err %v, output %q; want a refusal containing %q and no output", err, out, tc.want)
				}
				e.rootUntouched(t)
				if len(e.killed) != 0 {
					t.Fatalf("a refused reset killed %v", e.killed)
				}
			})
		}
	}
}

func TestTrialResetFailsWhenTheKillOrTheVerificationFails(t *testing.T) {
	e := newTrialTestEnv(t)
	e.killErr = errors.New("a process survived")
	if _, out, err := e.run(t, "reset", trialTestCase); err == nil || out != "" || !strings.Contains(err.Error(), "survived") {
		t.Fatalf("kill failure: err %v, output %q", err, out)
	}
	e.rootUntouched(t) // the kill precedes the wipe

	e = newTrialTestEnv(t)
	e.snapshotDirFn = func(dir *os.File, label string) wsoracle.Manifest {
		m := wsoracle.SnapshotDir(dir, label)
		m.Entries = append(m.Entries, wsoracle.Entry{Path: "extra", Type: wsoracle.TypeFile, Mode: 0o600, UID: e.owner.UID, GID: e.owner.GID, Nlink: 1,
			SHA256: strings.Repeat("0", 64)})
		return m
	}
	if _, out, err := e.run(t, "reset", trialTestCase); err == nil || out != "" || !strings.Contains(err.Error(), "does not digest") {
		t.Fatalf("verification failure: err %v, output %q", err, out)
	}
	e.snapshotDirFn = func(dir *os.File, label string) wsoracle.Manifest {
		return wsoracle.Manifest{Label: label, Gaps: []wsoracle.Gap{{Manifest: label, Kind: wsoracle.GapUnreadable}}}
	}
	if _, out, err := e.run(t, "reset", trialTestCase); err == nil || out != "" {
		t.Fatalf("incomplete snapshot: err %v, output %q", err, out)
	}
}

func TestTrialArgumentGrammar(t *testing.T) {
	e := newTrialTestEnv(t)
	for name, args := range map[string][]string{
		"no verb":         {},
		"unknown verb":    {"wipe", trialTestCase},
		"no case":         {"reset"},
		"extra argument":  {"reset", trialTestCase, "--check", "x"},
		"unknown flag":    {"reset", trialTestCase, "--force"},
		"flag first":      {"reset", "--check", trialTestCase},
		"leading dash":    {"reset", "-x"},
		"outside charset": {"reset", "a;b"},
		"too long":        {"reset", strings.Repeat("a", evidenceMaxValueBytes+1)},
	} {
		if _, out, err := e.run(t, args...); err == nil || out != "" {
			t.Errorf("%s: err = %v, output %q", name, err, out)
		}
	}
	e.rootUntouched(t)
}

func TestTrialIsDispatchedByTheForcedCommand(t *testing.T) {
	e := newTrialTestEnv(t)
	plain := "/usr/local/bin/tbound trial reset " + trialTestCase + " --check"
	quoted := `'/usr/local/bin/tbound' 'trial' 'reset' '` + trialTestCase + `' '--check'`
	for name, command := range map[string]string{"plain": plain, "quoted": quoted} {
		e.getenv = func(string) string { return command }
		doc, _, err := e.run(t, "ssh-original-command")
		if err != nil || doc.CaseID != trialTestCase || !doc.Check {
			t.Fatalf("%s: %+v, %v", name, doc, err)
		}
	}
	for name, command := range map[string]string{
		"command substitution": "/usr/local/bin/tbound trial reset $(id)",
		"semicolon":            "/usr/local/bin/tbound trial reset x;id",
		"double space":         "/usr/local/bin/tbound  trial reset " + trialTestCase,
		"too long":             "/usr/local/bin/tbound trial reset " + strings.Repeat("a", 3000),
		"extra word":           plain + " extra",
		"other binary":         "/bin/cat trial reset " + trialTestCase,
		"evidence verb":        "/usr/local/bin/tbound evidence proc-cell",
		"nested":               "/usr/local/bin/tbound trial ssh-original-command",
		"empty":                "",
	} {
		e.getenv = func(string) string { return command }
		if _, out, err := e.run(t, "ssh-original-command"); err == nil || out != "" {
			t.Errorf("%s: err = %v, output %q", name, err, out)
		}
	}
	e.getenv = func(string) string { return plain }
	if _, _, err := e.run(t, "ssh-original-command", "extra"); err == nil {
		t.Error("ssh-original-command accepted an argument")
	}
	e.rootUntouched(t)
}

func TestTrialForcedCommandNeedsRootToo(t *testing.T) {
	e := newTrialTestEnv(t)
	e.geteuid = func() int { return 1000 }
	e.getenv = func(string) string { return "/usr/local/bin/tbound trial reset " + trialTestCase }
	if _, out, err := e.run(t, "ssh-original-command"); err == nil || out != "" {
		t.Fatalf("err = %v, output %q", err, out)
	}
	e.rootUntouched(t)
}

// The dispatch in init runs before main, as for evidence: the test binary re-executed with the
// verb ends in trialMain with the production environment, which refuses any uid but root and,
// for root, a host that has no registration.
func TestTrialIsDispatchedBeforeMain(t *testing.T) {
	cmd := exec.Command(os.Args[0], "trial", "reset", trialTestCase, "--check")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "tbound trial: ") {
		t.Fatalf("err %v, stdout %q, stderr %q; want exit 2 from trialMain", err, stdout.String(), stderr.String())
	}
}

func TestTrialMainHelpPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if rc := trialMain([]string{"--help"}, &stdout, &stderr, trialEnv{}); rc != 0 || !strings.Contains(stderr.String(), "tbound trial reset <case_id> [--check]") {
		t.Fatalf("rc %d, stderr %q", rc, stderr.String())
	}
}

// The registration shape of the real guest: the cell template names the in-cell identity 1000:1000,
// and the signed rootless-single-own-uid-gid mapping puts it on the service's host identity
// 995:986, which owns the live root and the fixtures.
func realMappingShape() (piruntime.D09ManagedLiveRootRegistration, piruntime.D09CellTemplate) {
	reg := piruntime.D09ManagedLiveRootRegistration{ServiceUID: 995, ServiceGID: 986}
	tmpl := piruntime.D09CellTemplate{CellUID: 1000, CellGID: 1000, UserNamespaceMappingProfile: piruntime.D09UserNamespaceMappingProfile,
		UserNamespaceMappingDigest: piruntime.DigestD09UserNamespaceMapping(1000, 1000, 995, 986)}
	return reg, tmpl
}

func TestCellHostIdentityIsTheServiceIdentityTheSignedMappingNames(t *testing.T) {
	reg, tmpl := realMappingShape()
	uid, gid, err := cellHostIdentity(reg, tmpl)
	if err != nil || uid != 995 || gid != 986 {
		t.Fatalf("host identity = %d:%d, %v; want 995:986", uid, gid, err)
	}
	// It is the canonical fixture owner: a reset of the real registration is not refused.
	if uid != trialfix.Canonical.UID || gid != trialfix.Canonical.GID {
		t.Fatalf("the real host identity %d:%d is not the canonical owner %d:%d", uid, gid, trialfix.Canonical.UID, trialfix.Canonical.GID)
	}
	for name, mutate := range map[string]func(r *piruntime.D09ManagedLiveRootRegistration, c *piruntime.D09CellTemplate){
		"mapping digest of another host uid": func(r *piruntime.D09ManagedLiveRootRegistration, c *piruntime.D09CellTemplate) {
			c.UserNamespaceMappingDigest = piruntime.DigestD09UserNamespaceMapping(1000, 1000, 996, 986)
		},
		"mapping digest of another in-cell gid": func(r *piruntime.D09ManagedLiveRootRegistration, c *piruntime.D09CellTemplate) {
			c.CellGID = 1001
		},
		"other mapping profile": func(r *piruntime.D09ManagedLiveRootRegistration, c *piruntime.D09CellTemplate) {
			c.UserNamespaceMappingProfile = "rootless-subuid-range/v1"
		},
		"no mapping": func(r *piruntime.D09ManagedLiveRootRegistration, c *piruntime.D09CellTemplate) {
			c.UserNamespaceMappingDigest = ""
		},
		"root service":   func(r *piruntime.D09ManagedLiveRootRegistration, c *piruntime.D09CellTemplate) { r.ServiceUID = 0 },
		"no service gid": func(r *piruntime.D09ManagedLiveRootRegistration, c *piruntime.D09CellTemplate) { r.ServiceGID = 0 },
		"root cell":      func(r *piruntime.D09ManagedLiveRootRegistration, c *piruntime.D09CellTemplate) { c.CellUID = 0 },
	} {
		r, c := reg, tmpl
		mutate(&r, &c)
		if uid, gid, err := cellHostIdentity(r, c); err == nil {
			t.Errorf("%s: accepted as %d:%d", name, uid, gid)
		}
	}
}

// Reset passes the real shape (in-cell 1000, host 995 equal to the owner) and refuses a live
// root owned by anybody else.
func TestResetComparesTheHostIdentityOfTheMappingWithTheFixtureOwner(t *testing.T) {
	reg, tmpl := realMappingShape()
	hostUID, hostGID, err := cellHostIdentity(reg, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		owner trialfix.Owner
		ok    bool
	}{
		"owner is the mapped host identity": {trialfix.Owner{UID: hostUID, GID: hostGID}, true},
		"owner is the in-cell identity":     {trialfix.Owner{UID: 1000, GID: 1000}, false},
		"owner is another host uid":         {trialfix.Owner{UID: hostUID + 1, GID: hostGID}, false},
		"owner is another host gid":         {trialfix.Owner{UID: hostUID, GID: hostGID + 1}, false},
	} {
		e := newTrialTestEnv(t)
		e.owner = tc.owner
		e.cell = func(context.Context) (evidenceCell, error) {
			return evidenceCell{ID: "cells", CgroupPath: "/cells", Hierarchy: "/cells", Inode: 1, UID: 1000, GID: 1000, HostUID: hostUID, HostGID: hostGID}, nil
		}
		_, _, err := e.run(t, "reset", trialTestCase, "--check")
		if tc.ok && err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), "fixture owner")) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The reset holds the maintenance lock from before the pending check to after the final digest
// check, and cleans the no-reap leftovers and (re)loads the net-sink rule after the kill and before
// the tree is touched.
func TestTrialResetHoldsTheMaintenanceLockAcrossTheWholeReset(t *testing.T) {
	e := newTrialTestEnv(t)
	if _, _, err := e.run(t, "reset", trialTestCase); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(e.events, " "), "lock pending leftovers kill no-reap net-sink verify unlock"; got != want {
		t.Fatalf("seam order %q, want %q", got, want)
	}
}

func TestTrialResetRefusesWhenAWorkflowOrAnotherResetHoldsTheLock(t *testing.T) {
	for name, lockErr := range map[string]error{
		"busy":        fmt.Errorf("%w: held", errMaintenanceBusy),
		"unavailable": fmt.Errorf("%w: absent", errMaintenanceUnavailable),
	} {
		for _, args := range [][]string{{"reset", trialTestCase}, {"reset", trialTestCase, "--check"}} {
			e := newTrialTestEnv(t)
			e.lockErr = lockErr
			if _, out, err := e.run(t, args...); err == nil || out != "" || !strings.Contains(err.Error(), "maintenance lock") {
				t.Fatalf("%s %v: err %v, output %q; want a refusal naming the maintenance lock", name, args, err, out)
			}
			e.rootUntouched(t)
			if len(e.killed) != 0 || len(e.events) != 0 {
				t.Fatalf("%s %v: the reset went on: kills %v, events %v", name, args, e.killed, e.events)
			}
		}
	}
}

// --check changes nothing: it takes the lock for its reads (the leftover-container query among them) and
// releases it, and runs no cleanup. An empty container list lets it pass.
func TestTrialResetCheckTakesTheLockAndCleansNothing(t *testing.T) {
	e := newTrialTestEnv(t)
	if _, _, err := e.run(t, "reset", trialTestCase, "--check"); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(e.events, " "), "lock pending leftovers unlock"; got != want {
		t.Fatalf("seam order %q, want %q", got, want)
	}
}

func TestTrialResetRefusesBeforeWipingWhenTheCleanupFails(t *testing.T) {
	for name, set := range map[string]func(*trialTestEnv){
		"no-reap leftovers": func(e *trialTestEnv) { e.noReapErr = errors.New("container x remains") },
		"net-sink rule":     func(e *trialTestEnv) { e.netSinkErr = errors.New("nft refused") },
	} {
		e := newTrialTestEnv(t)
		set(e)
		if _, out, err := e.run(t, "reset", trialTestCase); err == nil || out != "" {
			t.Fatalf("%s: err %v, output %q; want a refusal", name, err, out)
		}
		e.rootUntouched(t)
		if last := e.events[len(e.events)-1]; last != "unlock" {
			t.Fatalf("%s: the lock was not released: %v", name, e.events)
		}
	}
}

// The research route's USD stop rule refuses the check before a unit, so the runner's Prepare voids it; the reset
// that follows a passed check does not consult it again.
func TestTrialResetCheckRefusesOnTheUSDStopRule(t *testing.T) {
	e := newTrialTestEnv(t)
	e.usdStopErr = researchroute.ErrStopRule
	if _, out, err := e.run(t, "reset", trialTestCase, "--check"); err == nil || !strings.Contains(err.Error(), "stop rule") || out != "" {
		t.Fatalf("check: err %v, output %q; want a stop-rule refusal", err, out)
	}
	e.rootUntouched(t)
	if _, _, err := e.run(t, "reset", trialTestCase); err != nil {
		t.Fatalf("the reset after the check consulted the stop rule: %v", err)
	}
}

// Only the research route has a USD ledger: on the engineering route the check reads nothing, on the research route
// a ledger it cannot read refuses the unit.
func TestUSDStopReadsTheLedgerOnTheResearchRouteOnly(t *testing.T) {
	missing := researchroute.Paths{LedgerConfig: filepath.Join(t.TempDir(), "absent.json")}
	if err := usdStopFor(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteEngineering}, missing); err != nil {
		t.Fatalf("engineering route: %v", err)
	}
	if err := usdStopFor(piServiceConfig{ProviderRoute: piruntime.D09ManagedPiProviderRouteResearch}, missing); err == nil {
		t.Fatal("research route with an unreadable ledger configuration admitted the unit")
	}
}
