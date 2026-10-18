//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/piruntime"
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
	// populated is what the fake cells cgroup reports.
	populated bool
	killed    []string // cgroup paths the kill ran on
	// dirtyAtKill records whether the old content was still in the root when the kill ran.
	dirtyAtKill bool
	killErr     error
	snapshotFn  func(root, label string) wsoracle.Manifest
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
		cell: func(context.Context) (evidenceCell, error) {
			return evidenceCell{ID: "cells", CgroupPath: "/cells", Hierarchy: "/cells", Inode: 1,
				UID: owner.UID + trialTestInCellOffset, GID: owner.GID + trialTestInCellOffset, HostUID: owner.UID, HostGID: owner.GID}, nil
		},
		pending:       func(context.Context) error { return e.pendingErr },
		cellPopulated: func(evidenceCell) (bool, error) { return e.populated, nil },
		killCell: func(cell evidenceCell) error {
			e.killed = append(e.killed, cell.CgroupPath)
			_, err := os.Stat(filepath.Join(root, "left-over"))
			e.dirtyAtKill = err == nil
			return e.killErr
		},
		snapshot: func(r, label string) wsoracle.Manifest {
			if e.snapshotFn != nil {
				return e.snapshotFn(r, label)
			}
			return wsoracle.Snapshot(r, label)
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
	e.snapshotFn = func(r, label string) wsoracle.Manifest {
		m := wsoracle.Snapshot(r, label)
		m.Entries = append(m.Entries, wsoracle.Entry{Path: "extra", Type: wsoracle.TypeFile, Mode: 0o600, UID: e.owner.UID, GID: e.owner.GID, Nlink: 1,
			SHA256: strings.Repeat("0", 64)})
		return m
	}
	if _, out, err := e.run(t, "reset", trialTestCase); err == nil || out != "" || !strings.Contains(err.Error(), "does not digest") {
		t.Fatalf("verification failure: err %v, output %q", err, out)
	}
	e.snapshotFn = func(r, label string) wsoracle.Manifest {
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
