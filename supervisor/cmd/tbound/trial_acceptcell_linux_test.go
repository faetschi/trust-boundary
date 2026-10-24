package main

import (
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/vrun"
)

const acceptCellTests = "const assert = require('node:assert');\nassert.ok(typeof main === 'function');\n"

// newAcceptCellEnv is the v-run test env with a node_test acceptance spec over src/main.go.
func newAcceptCellEnv(t *testing.T) *vTestEnv {
	t.Helper()
	e := newVTestEnv(t)
	spec := `{"schema":"tbound-acceptance/v1","checks":[{"op":"node_test","path":"src/main.go","text":` +
		strconv.Quote(acceptCellTests) + `},{"op":"unchanged_except","paths":["src/main.go"]}]}`
	e.acceptDigest = taskaccept.DigestOf([]byte(spec))
	name, _ := corpusadmit.AcceptArtifactName(e.acceptDigest)
	e.files[name] = &fstest.MapFile{Data: []byte(spec)}
	return e
}

var markerRE = regexp.MustCompile(`console\.log\('(tbound-node-test-complete-[0-9a-f]{32})'\);\n$`)

// The acceptance command runs the agent's file only inside the cell: the argv has no network, a
// read-only root, one read-only mount holding only the program, no workspace, the cell uid and the
// fixed timeout; podman runs as the host identity; the container is removed by force afterwards.
func TestAcceptCellArgvIsolatesTheAgentFile(t *testing.T) {
	e := newAcceptCellEnv(t)
	e.solve(t)
	var checked bool
	e.podman.onRun = func(_ context.Context, inv podmanInvocation) (int, error) {
		args := strings.Join(inv.Args, " ")
		for _, want := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt no-new-privileges",
			"--user 1000:1000", "--timeout 30", "--entrypoint " + vrun.NodePath} {
			if !strings.Contains(args, want) {
				t.Errorf("argv lacks %q: %s", want, args)
			}
		}
		if strings.Contains(args, "dst="+vrun.WorkDir) || strings.Contains(args, e.root) || strings.Contains(args, e.keyFile) {
			t.Errorf("argv mounts the workspace, the live root or the key: %s", args)
		}
		if inv.UID != e.owner.UID || inv.GID != e.owner.GID {
			t.Errorf("podman runs as %d:%d, want the host identity %d:%d", inv.UID, inv.GID, e.owner.UID, e.owner.GID)
		}
		dir := mountSource(t, inv.Args, vrun.AcceptDir)
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != vrun.AcceptProgram {
			t.Fatalf("check dir holds %v (%v), want only %s", entries, err, vrun.AcceptProgram)
		}
		program, err := os.ReadFile(dir + "/" + vrun.AcceptProgram)
		if err != nil || !strings.HasPrefix(string(program), "package main\nfunc main() {}\n\n"+acceptCellTests) {
			t.Fatalf("program = %q, %v", program, err)
		}
		m := markerRE.FindSubmatch(program)
		if m == nil {
			t.Fatalf("program has no completion marker: %q", program)
		}
		checked = true
		_, err = inv.Stdout.Write(append(m[1], '\n'))
		return 0, err
	}
	doc, _, err := e.accept(t, trialTestCase)
	if err != nil || !doc.Passed || !checked {
		t.Fatalf("accept: %+v %v (cell ran: %v)", doc, err, checked)
	}
	if len(e.podman.removes) != 1 || !slices.Contains(e.podman.removes[0].Args, "-f") {
		t.Fatalf("the container was not removed by force: %+v", e.podman.removes)
	}
	if entries, _ := os.ReadDir(e.work); len(entries) != 0 {
		t.Fatalf("work root still holds %v", entries)
	}
}

// A failing test run is a failed check, not an error.
func TestAcceptCellFailingTestsFailTheCheck(t *testing.T) {
	e := newAcceptCellEnv(t)
	e.solve(t)
	e.podman.onRun = func(context.Context, podmanInvocation) (int, error) { return 1, nil }
	if doc, _, err := e.accept(t, trialTestCase); err != nil || doc.Passed {
		t.Fatalf("accept: %+v %v", doc, err)
	}
}

// Anything wrong with the cell refuses acceptance: an error, never a pass.
func TestAcceptCellFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(e *vTestEnv){
		"template unavailable": func(e *vTestEnv) {
			e.task.template = func(context.Context) (vTemplate, error) { return vTemplate{}, errors.New("no template") }
		},
		"preflight fails": func(e *vTestEnv) { e.task.preflight = func() error { return errors.New("no runtime dir") } },
		"podman setup error": func(e *vTestEnv) {
			e.podman.onRun = func(context.Context, podmanInvocation) (int, error) { return 125, nil }
		},
		"podman not runnable": func(e *vTestEnv) {
			e.podman.onRun = func(context.Context, podmanInvocation) (int, error) { return -1, errors.New("exec failed") }
		},
		"forced removal fails": func(e *vTestEnv) {
			e.podman.onRun = func(_ context.Context, inv podmanInvocation) (int, error) { return 0, nil }
			e.podman.onRemove = func(podmanInvocation) (int, error) { return 1, nil }
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newAcceptCellEnv(t)
			e.solve(t)
			mutate(e)
			if doc, _, err := e.accept(t, trialTestCase); err == nil || doc.Passed {
				t.Fatalf("got %+v, %v; want a refusal", doc, err)
			}
		})
	}
}

func TestAcceptCellTimeoutMatchesTheCheckTimeout(t *testing.T) {
	if vrun.AcceptTimeoutSeconds*1e9 != int64(taskaccept.NodeTestTimeout) {
		t.Fatalf("podman --timeout %ds differs from taskaccept.NodeTestTimeout %s", vrun.AcceptTimeoutSeconds, taskaccept.NodeTestTimeout)
	}
}
