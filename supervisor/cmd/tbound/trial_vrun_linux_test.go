//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/vrun"
)

// The in-container identity of the template; the host identity is the test user.
const (
	vTestCellUID = 1000
	vTestCellGID = 1000
)

const vTestImageDigest = "sha256:" + "5d0c1e9a3b7f4e2d8c6a1b0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a291807"

// fakePodman is the podman seam: it records every invocation and lets a test play the
// container.
type fakePodman struct {
	mu      sync.Mutex
	runs    []podmanInvocation
	removes []podmanInvocation
	// onRun plays `podman run`. The default does nothing and exits 0.
	onRun func(ctx context.Context, inv podmanInvocation) (int, error)
	// onRemove plays `podman rm`.
	onRemove func(inv podmanInvocation) (int, error)
}

func (f *fakePodman) Run(ctx context.Context, inv podmanInvocation) (int, error) {
	f.mu.Lock()
	isRun := len(inv.Args) > 0 && inv.Args[0] == "run"
	if isRun {
		f.runs = append(f.runs, inv)
	} else {
		f.removes = append(f.removes, inv)
	}
	onRun, onRemove := f.onRun, f.onRemove
	f.mu.Unlock()
	switch {
	case isRun && onRun != nil:
		return onRun(ctx, inv)
	case !isRun && onRemove != nil:
		return onRemove(inv)
	}
	return 0, nil
}

// mountSource returns the host source of the mount whose destination is dst.
func mountSource(t *testing.T, args []string, dst string) string {
	t.Helper()
	for i, a := range args {
		if a == "--mount" && i+1 < len(args) {
			fields := map[string]string{}
			for _, kv := range strings.Split(args[i+1], ",") {
				k, v, _ := strings.Cut(kv, "=")
				fields[k] = v
			}
			if fields["dst"] == dst {
				return fields["src"]
			}
		}
	}
	t.Fatalf("no mount at %s in %q", dst, args)
	return ""
}

type vTestEnv struct {
	*taskTestEnv
	podman  *fakePodman
	work    string
	keyFile string
	clock   int64
	tokens  int
}

func newVTestEnv(t *testing.T) *vTestEnv {
	t.Helper()
	base := newTaskTestEnv(t)
	dir := t.TempDir()
	e := &vTestEnv{taskTestEnv: base, podman: &fakePodman{}, work: filepath.Join(dir, "v-work"), keyFile: filepath.Join(dir, "opencode-go.key")}
	if err := os.WriteFile(e.keyFile, []byte("not-a-real-key\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	owner := e.owner
	e.task.template = func(context.Context) (vTemplate, error) {
		return vTemplate{ImageRef: "localhost/tbound/pi-cell@" + vTestImageDigest, ImageDigest: vTestImageDigest,
			PodmanPath: "/usr/bin/podman", CrunPath: "/usr/bin/crun", CPUs: "1", PidsMax: 128, MemoryMB: 1024,
			HostUID: owner.UID, HostGID: owner.GID, CellUID: vTestCellUID, CellGID: vTestCellGID}, nil
	}
	e.task.preflight = func() error { return nil }
	e.task.podman = e.podman
	e.task.workRoot = e.work
	e.task.runtimeBundle = filepath.Join(dir, "runtime")
	e.task.keyPath = e.keyFile
	e.task.now = func() time.Time { e.clock += int64(time.Second); return time.Unix(0, e.clock) }
	e.task.token = func() (string, error) { e.tokens++; return fmt.Sprintf("%016x", e.tokens), nil }
	e.task.deadline = func(s uint64) time.Duration { return time.Duration(s) * time.Millisecond }
	e.task.killGrace = 50 * time.Millisecond
	return e
}

func (e *vTestEnv) vrun(t *testing.T, args ...string) (vrun.Doc, string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runTrial(context.Background(), append([]string{"v-run"}, args...), &out, e.trialEnv)
	var doc vrun.Doc
	if err == nil {
		d, perr := vrun.Parse(bytes.TrimSuffix(out.Bytes(), []byte("\n")))
		if perr != nil {
			t.Fatalf("output %q is not a v-run document: %v", out.String(), perr)
		}
		doc = d
	}
	return doc, out.String(), err
}

// piEvents is what a successful native Pi run writes on stdout: two completed assistant messages.
func piEvents() string {
	msg := func(in, out int, cost float64) string {
		return fmt.Sprintf(`{"type":"message_end","message":{"role":"assistant","usage":{"input":%d,"output":%d,"cacheRead":0,"cacheWrite":0,"totalTokens":%d,"cost":{"total":%v}}}}`, in, out, in+out, cost)
	}
	return strings.Join([]string{`{"type":"session","version":3}`, msg(120, 30, 0.00021),
		`{"type":"tool_execution_end","toolCallId":"c","toolName":"edit","result":{},"isError":false}`, msg(80, 20, 0.00014)}, "\n") + "\n"
}

// solvingContainer plays a Pi that edits src/main.go in the workspace it is given.
func solvingContainer(t *testing.T, seen *[]string) func(context.Context, podmanInvocation) (int, error) {
	return func(_ context.Context, inv podmanInvocation) (int, error) {
		ws := mountSource(t, inv.Args, vrun.WorkDir)
		if seen != nil {
			data, err := os.ReadFile(filepath.Join(ws, "src", "main.go"))
			if err != nil {
				t.Errorf("the workspace has no fixture: %v", err)
			}
			*seen = append(*seen, ws+"|"+string(data))
		}
		if err := os.WriteFile(filepath.Join(ws, "src", "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
			t.Errorf("write in the workspace: %v", err)
		}
		_, _ = inv.Stdout.Write([]byte(piEvents()))
		return 0, nil
	}
}

func TestVRunArgumentShapeAndDocument(t *testing.T) {
	e := newVTestEnv(t)
	e.podman.onRun = func(ctx context.Context, inv podmanInvocation) (int, error) {
		ws := mountSource(t, inv.Args, vrun.WorkDir)
		// At the time podman runs the workspace is private to the cell identity and holds the
		// fixture; the work directory is private too.
		st, err := os.Stat(ws)
		if err != nil {
			t.Fatalf("workspace: %v", err)
		}
		sys := st.Sys().(*syscall.Stat_t)
		if st.Mode().Perm() != 0o700 || sys.Uid != e.owner.UID || sys.Gid != e.owner.GID {
			t.Errorf("workspace mode %v owner %d:%d, want 0700 %d:%d", st.Mode().Perm(), sys.Uid, sys.Gid, e.owner.UID, e.owner.GID)
		}
		if rel, err := filepath.Rel(e.work, ws); err != nil || strings.HasPrefix(rel, "..") || filepath.Base(ws) != "ws" {
			t.Errorf("workspace %s is not under the work root %s", ws, e.work)
		}
		return solvingContainer(t, nil)(ctx, inv)
	}
	doc, _, err := e.vrun(t, trialTestCase)
	if err != nil {
		t.Fatalf("v-run: %v", err)
	}
	if len(e.podman.runs) != 1 {
		t.Fatalf("podman run called %d times", len(e.podman.runs))
	}
	inv := e.podman.runs[0]
	// Identity and privilege of the process that starts podman.
	if inv.Path != "/usr/bin/podman" || inv.UID != e.owner.UID || inv.GID != e.owner.GID {
		t.Errorf("podman is started as %s %d:%d, want /usr/bin/podman %d:%d", inv.Path, inv.UID, inv.GID, e.owner.UID, e.owner.GID)
	}
	if !strings.HasPrefix(inv.Dir, e.work+string(filepath.Separator)) {
		t.Errorf("podman working directory %q is not inside the work dir", inv.Dir)
	}
	for _, kv := range inv.Env {
		if strings.Contains(kv, "OPENCODE") || strings.Contains(kv, "KEY") {
			t.Errorf("podman environment carries %q", kv)
		}
	}
	if !slices.Contains(inv.Env, "HOME="+"/var/lib/tbound/podman-home") || !slices.ContainsFunc(inv.Env, func(s string) bool { return strings.HasPrefix(s, "PATH=") }) {
		t.Errorf("podman environment lacks the service's podman profile: %q", inv.Env)
	}
	// The pinned image digest, the identity, a read-only root and the three mounts.
	args := inv.Args
	pinned := "localhost/tbound/pi-cell@" + vTestImageDigest
	if !slices.Contains(args, pinned) || !slices.Contains(args, "--pull=never") || !slices.Contains(args, "--read-only") || !slices.Contains(args, "--rm") {
		t.Errorf("args lack the pinned image, --pull=never, --read-only or --rm: %q", args)
	}
	uid, gid := strconv.Itoa(vTestCellUID), strconv.Itoa(vTestCellGID)
	for _, pair := range [][]string{{"--user", uid + ":" + gid}, {"--uidmap", uid + ":0:1"}, {"--gidmap", gid + ":0:1"}, {"--timeout", "60"}, {"--entrypoint", "/bin/bash"}} {
		if i := slices.Index(args, pair[0]); i < 0 || args[i+1] != pair[1] {
			t.Errorf("args lack %v: %q", pair, args)
		}
	}
	if got := mountSource(t, args, vrun.KeyFile); got != e.keyFile {
		t.Errorf("key mount source = %q, want %q", got, e.keyFile)
	}
	if got := mountSource(t, args, vrun.RuntimeDir); got != e.task.runtimeBundle {
		t.Errorf("runtime mount source = %q", got)
	}
	for _, a := range args {
		if strings.Contains(a, "not-a-real-key") {
			t.Errorf("the key is in an argument: %q", a)
		}
	}
	if last := args[len(args)-1]; last != e.prompt {
		t.Errorf("the prompt is %q, want the manifest's %q", last, e.prompt)
	}
	// The document.
	if doc.CaseID != trialTestCase || doc.ImageDigest != vTestImageDigest || doc.FixtureDigest != e.digest || doc.Exit != 0 || doc.TimedOut {
		t.Errorf("doc = %+v", doc)
	}
	if doc.TokensIn != 200 || doc.TokensOut != 50 || doc.CostMicroUSD != 350 || doc.ToolCalls != 1 || doc.UsageEvents != 2 {
		t.Errorf("usage = %+v", doc)
	}
	if doc.Accept == nil || !doc.Accept.Passed || doc.Accept.SpecDigest != e.acceptDigest || doc.Accept.CaseID != trialTestCase || doc.AcceptError != "" {
		t.Errorf("accept = %+v (%s)", doc.Accept, doc.AcceptError)
	}
	if doc.StartedNS <= 0 || doc.FinishedNS <= doc.StartedNS {
		t.Errorf("times = %d, %d", doc.StartedNS, doc.FinishedNS)
	}
	// The work dir is gone, and the live root was never touched.
	if left, _ := os.ReadDir(e.work); len(left) != 0 {
		t.Errorf("work dir not removed: %v", left)
	}
	if got, err := os.ReadFile(filepath.Join(e.root, "src", "main.go")); err != nil || string(got) != "package main\n" {
		t.Errorf("the live root changed: %q, %v", got, err)
	}
}

func TestVRunReportsAFailedAcceptanceAsData(t *testing.T) {
	e := newVTestEnv(t)
	e.podman.onRun = func(_ context.Context, inv podmanInvocation) (int, error) {
		_, _ = inv.Stdout.Write([]byte(piEvents()))
		return 1, nil
	}
	doc, _, err := e.vrun(t, trialTestCase)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Exit != 1 || doc.Accept == nil || doc.Accept.Passed {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestVRunGivesEveryRunAFreshFixtureCopy(t *testing.T) {
	e := newVTestEnv(t)
	var seen []string
	e.podman.onRun = solvingContainer(t, &seen)
	for range 2 {
		if _, _, err := e.vrun(t, trialTestCase); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("seen = %v", seen)
	}
	wsA, dataA, _ := strings.Cut(seen[0], "|")
	wsB, dataB, _ := strings.Cut(seen[1], "|")
	// The first run edited its workspace; the second still starts from the fixture, in a
	// different directory.
	if dataA != "package main\n" || dataB != "package main\n" || wsA == wsB {
		t.Fatalf("workspaces not fresh: %v", seen)
	}
	if left, _ := os.ReadDir(e.work); len(left) != 0 {
		t.Errorf("work dir not removed: %v", left)
	}
}

func TestVRunKillsAtTheDeadlineAndRemovesTheContainer(t *testing.T) {
	e := newVTestEnv(t)
	e.duration = 40 // ms through the test deadline function
	var started time.Time
	e.podman.onRun = func(ctx context.Context, inv podmanInvocation) (int, error) {
		started = time.Now()
		_, _ = inv.Stdout.Write([]byte(piEvents()))
		<-ctx.Done()
		return 0, ctx.Err()
	}
	doc, _, err := e.vrun(t, trialTestCase)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.TimedOut || doc.Exit != 137 {
		t.Fatalf("doc = %+v", doc)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("the deadline took %v", took)
	}
	// Usage written before the kill still counts, and acceptance is judged on what the run left.
	if doc.UsageEvents != 2 || doc.Accept == nil || doc.Accept.Passed {
		t.Fatalf("doc = %+v", doc)
	}
	// The container is removed by name, forcefully, through the same seam.
	if len(e.podman.removes) != 1 {
		t.Fatalf("removes = %v", e.podman.removes)
	}
	rm := e.podman.removes[0]
	name := e.podman.runs[0].Args[slices.Index(e.podman.runs[0].Args, "--name")+1]
	if !slices.Contains(rm.Args, "rm") || !slices.Contains(rm.Args, "-f") || !slices.Contains(rm.Args, name) || rm.UID != e.owner.UID {
		t.Fatalf("rm = %+v, container %s", rm, name)
	}
	if left, _ := os.ReadDir(e.work); len(left) != 0 {
		t.Errorf("work dir not removed: %v", left)
	}
}

func TestVRunPodmanFailureIsAnErrorNotADocument(t *testing.T) {
	e := newVTestEnv(t)
	e.podman.onRun = func(_ context.Context, inv podmanInvocation) (int, error) {
		_, _ = inv.Stderr.Write([]byte("Error: image not known\n"))
		return 125, nil
	}
	_, out, err := e.vrun(t, trialTestCase)
	if err == nil || !strings.Contains(err.Error(), "image not known") || out != "" {
		t.Fatalf("err = %v, out = %q", err, out)
	}
	if left, _ := os.ReadDir(e.work); len(left) != 0 {
		t.Errorf("work dir not removed after a failure: %v", left)
	}
	e.podman.onRun = func(context.Context, podmanInvocation) (int, error) { return 0, errors.New("exec: setpriv not found") }
	if _, _, err := e.vrun(t, trialTestCase); err == nil || !strings.Contains(err.Error(), "setpriv") {
		t.Fatalf("err = %v", err)
	}
	if left, _ := os.ReadDir(e.work); len(left) != 0 {
		t.Errorf("work dir not removed after a failure: %v", left)
	}
}

func TestVRunARemovalFailureIsReported(t *testing.T) {
	e := newVTestEnv(t)
	e.duration = 20
	e.podman.onRun = func(ctx context.Context, _ podmanInvocation) (int, error) { <-ctx.Done(); return 0, ctx.Err() }
	e.podman.onRemove = func(podmanInvocation) (int, error) { return 125, nil }
	if _, _, err := e.vrun(t, trialTestCase); err == nil || !strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("err = %v", err)
	}
}

func TestVRunRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(e *vTestEnv)
		args   []string
		want   string
	}{
		"not root":     {func(e *vTestEnv) { e.geteuid = func() int { return 1000 } }, []string{trialTestCase}, "uid 0"},
		"no case":      {func(e *vTestEnv) {}, nil, "case id"},
		"extra":        {func(e *vTestEnv) {}, []string{trialTestCase, "x"}, "takes"},
		"unsafe case":  {func(e *vTestEnv) {}, []string{"a;b"}, "safe alphabet"},
		"unknown case": {func(e *vTestEnv) {}, []string{"nope"}, "pinned corpus"},
		"ambiguous":    {func(e *vTestEnv) { e.kind = evalcorpus.KindAmbiguous }, []string{trialTestCase}, "compliant"},
		"adversarial":  {func(e *vTestEnv) { e.kind = evalcorpus.KindAdversarial }, []string{trialTestCase}, "compliant"},
		"no prompt":    {func(e *vTestEnv) { e.prompt = "" }, []string{trialTestCase}, "prompt"},
		"no duration":  {func(e *vTestEnv) { e.duration = 0 }, []string{trialTestCase}, "duration"},
		"host identity is not the fixture owner": {func(e *vTestEnv) {
			e.task.template = func(context.Context) (vTemplate, error) {
				return vTemplate{ImageRef: "localhost/tbound/pi-cell@" + vTestImageDigest, ImageDigest: vTestImageDigest, PodmanPath: "/usr/bin/podman",
					CrunPath: "/usr/bin/crun", CPUs: "1", PidsMax: 1, MemoryMB: 1, HostUID: e.owner.UID + 1, HostGID: e.owner.GID, CellUID: vTestCellUID, CellGID: vTestCellGID}, nil
			}
		}, []string{trialTestCase}, "fixture owner"},
		"template fails": {func(e *vTestEnv) {
			e.task.template = func(context.Context) (vTemplate, error) { return vTemplate{}, errors.New("no template") }
		}, []string{trialTestCase}, "no template"},
		"preflight fails": {func(e *vTestEnv) { e.task.preflight = func() error { return errors.New("no runtime dir") } }, []string{trialTestCase}, "no runtime dir"},
		"key missing":     {func(e *vTestEnv) { e.task.keyPath = filepath.Join(filepath.Dir(e.keyFile), "missing.key") }, []string{trialTestCase}, "provider key"},
		"key is a symlink": {func(e *vTestEnv) {
			link := filepath.Join(filepath.Dir(e.keyFile), "link.key")
			if err := os.Symlink(e.keyFile, link); err != nil {
				t.Fatal(err)
			}
			e.task.keyPath = link
		}, []string{trialTestCase}, "provider key"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newVTestEnv(t)
			tc.mutate(e)
			_, out, err := e.vrun(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if out != "" || len(e.podman.runs) != 0 {
				t.Fatalf("a refused v-run wrote %q and ran podman %d times", out, len(e.podman.runs))
			}
			if left, _ := os.ReadDir(e.work); len(left) != 0 {
				t.Errorf("a refused v-run left %v", left)
			}
		})
	}
}

func TestVRunThroughTheForcedCommand(t *testing.T) {
	e := newVTestEnv(t)
	e.podman.onRun = solvingContainer(t, nil)
	e.getenv = func(k string) string {
		if k == "SSH_ORIGINAL_COMMAND" {
			return runner.EvidenceBinaryPath + " trial v-run " + trialTestCase
		}
		return ""
	}
	var out bytes.Buffer
	if err := runTrial(context.Background(), []string{"ssh-original-command"}, &out, e.trialEnv); err != nil {
		t.Fatal(err)
	}
	doc, err := vrun.Parse(bytes.TrimSuffix(out.Bytes(), []byte("\n")))
	if err != nil || doc.Accept == nil || !doc.Accept.Passed {
		t.Fatalf("forced v-run = %q, %v", out.String(), err)
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(out.Bytes(), &raw) != nil || string(raw["accept"]) == "null" {
		t.Fatalf("accept missing in %s", out.String())
	}
}

func TestExecPodmanRunnerKillsTheProcessGroupAtTheDeadline(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := filepath.Join(dir, "podman")
	body := "#!/bin/sh\necho started\nsleep 60 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	r := execPodmanRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	var out, errOut bytes.Buffer
	start := time.Now()
	exit, err := r.Run(ctx, podmanInvocation{Path: script, Env: []string{"PATH=/usr/bin:/bin"}, Dir: dir,
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Stdout: &out, Stderr: &errOut})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exit %d, err %v", exit, err)
	}
	if time.Since(start) > 10*time.Second || !strings.Contains(out.String(), "started") {
		t.Fatalf("took %v, stdout %q", time.Since(start), out.String())
	}
	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatal(rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	for range 100 {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the child process %d of the killed run is still alive", pid)
}

func TestExecPodmanRunnerReportsTheExitCodeAndUsesAnExplicitEnvironment(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "podman")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nenv | sort\necho oops >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_API_KEY", "leak-me")
	var out, errOut bytes.Buffer
	exit, err := execPodmanRunner{}.Run(context.Background(), podmanInvocation{Path: script, Env: []string{"A=b", "PATH=/usr/bin:/bin"}, Dir: dir,
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Stdout: &out, Stderr: &errOut})
	if err != nil || exit != 7 {
		t.Fatalf("exit %d, err %v", exit, err)
	}
	if strings.Contains(out.String(), "leak-me") || !strings.Contains(out.String(), "A=b") || !strings.Contains(errOut.String(), "oops") {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}
}

// v-run and reset resolve the host identity of the cell the same way.
func TestVTemplateFromTheRealMappingAgreesWithReset(t *testing.T) {
	reg, tmpl := realMappingShape()
	tmpl.Image = "localhost/tbound/pi-cell:pinned"
	tmpl.ExpectedImageDigest = vTestImageDigest
	tmpl.PodmanPath, tmpl.CrunPath, tmpl.CPUs, tmpl.PidsMax, tmpl.MemoryMB = "/usr/bin/podman", "/usr/bin/crun", "1", 128, 1024
	v, err := vTemplateFrom(reg, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	uid, gid, err := cellHostIdentity(reg, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if v.HostUID != uid || v.HostGID != gid || v.HostUID != 995 || v.HostGID != 986 || v.CellUID != 1000 || v.CellGID != 1000 {
		t.Fatalf("v-run template identity host %d:%d cell %d:%d; reset host %d:%d", v.HostUID, v.HostGID, v.CellUID, v.CellGID, uid, gid)
	}
	if v.ImageRef != "localhost/tbound/pi-cell@"+vTestImageDigest || v.ImageDigest != vTestImageDigest {
		t.Fatalf("image = %q %q", v.ImageRef, v.ImageDigest)
	}
	tmpl.UserNamespaceMappingDigest = piruntime.DigestD09UserNamespaceMapping(1000, 1000, 996, 986)
	if _, err := vTemplateFrom(reg, tmpl); err == nil {
		t.Fatal("a template whose mapping digest names another host uid was accepted")
	}
}
