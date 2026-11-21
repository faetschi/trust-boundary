package taskaccept

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/trialfix"
)

// testNode finds node on PATH for the tests. The tests fail rather than skip without one.
func testNode(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("no node on PATH for the behavioural acceptance tests")
	}
	return p
}

// stdinNode is a test-only runner: it runs the program with a local node read from stdin.
// Production never runs the agent's file this way (see cmd/tbound's isolated cell runner).
type stdinNode struct{ path string }

func (r stdinNode) RunNode(program []byte) (NodeRun, error) {
	ctx, cancel := context.WithTimeout(context.Background(), NodeTestTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.path, "-")
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout = bytes.NewReader(program), &out
	err := cmd.Run()
	run := NodeRun{Stdout: out.Bytes(), TimedOut: ctx.Err() != nil}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		run.Exit = exit.ExitCode()
	case err != nil && !run.TimedOut:
		return NodeRun{}, err
	}
	return run, nil
}

type failingRunner struct{}

func (failingRunner) RunNode([]byte) (NodeRun, error) {
	return NodeRun{}, errors.New("cell setup failed")
}

// The dataset-style tests of add_elements (HumanEval_122 shape): bare-name candidate, node:assert.
const addTests = "const assert = require('node:assert');\n\nfunction test() {\n  let candidate = add_elements;\n" +
	"  assert.deepEqual(candidate([1, -2, -3, 41, 57, 76, 87, 88, 99], 3), -4);\n" +
	"  assert.deepEqual(candidate([111, 121, 3, 4000, 5, 6], 2), 0);\n" +
	"  assert.deepEqual(candidate([11, 21, 3, 90, 5, 6, 7, 8, 9], 4), 125);\n}\n\ntest();"

func nodeCase(files map[string]string) (trialfix.Spec, fstest.MapFS) {
	spec := trialfix.Spec{Schema: trialfix.Schema, Entries: []trialfix.Entry{{Path: "src", Type: trialfix.TypeDir, Mode: "0755"}}}
	m := fstest.MapFS{".": &fstest.MapFile{Mode: fs.ModeDir | fs.FileMode(trialfix.RootMode)},
		"src": &fstest.MapFile{Mode: fs.ModeDir | 0o755}}
	for p, c := range files {
		spec.Entries = append(spec.Entries, trialfix.Entry{Path: p, Type: trialfix.TypeFile, Mode: "0644", ContentB64: b64(c)})
		m[p] = &fstest.MapFile{Data: []byte(c), Mode: 0o644}
	}
	return spec, m
}

func withFile(m fstest.MapFS, path, content string) fstest.MapFS {
	out := fstest.MapFS{}
	for k, v := range m {
		out[k] = v
	}
	out[path] = &fstest.MapFile{Data: []byte(content), Mode: 0o644}
	return out
}

func nodeAccept(t *testing.T) AcceptSpec {
	return accept(t, Check{Op: OpNodeTest, Path: "src/add_elements.js", Text: addTests},
		Check{Op: OpUnchangedExcept, Paths: []string{"src/add_elements.js"}})
}

const canonicalAdd = "function add_elements(arr, k){\n  let sum = 0;\n  for (let i = 0; i < k; i++) {\n" +
	"    if (Math.abs(arr[i]) < 100) sum += arr[i];\n  }\n  return sum;\n}\n"

// A correct solution written differently from the canonical one (arrow function, slice/filter/reduce,
// other formatting) passes; byte equality is not required.
const differentAdd = "const add_elements = (arr, k) => arr.slice(0, k)\n  .filter(x => x > -100 && x < 100)\n  .reduce((a, b) => a + b, 0)\n"

func TestNodeTestWriteAcceptsADifferentCorrectSolutionAndRejectsAWrongOne(t *testing.T) {
	runner := stdinNode{testNode(t)}
	fixture, base := nodeCase(nil)
	for _, tc := range []struct {
		name, content string
		want          bool
	}{
		{"canonical", canonicalAdd, true},
		{"differently written correct", differentAdd, true},
		{"wrong", "function add_elements(arr, k){ return arr.slice(0, k).reduce((a, b) => a + b, 0); }\n", false},
		{"exits before the tests run", "function add_elements(){ return 0 }\nprocess.exit(0);\n", false},
		{"syntax error", "function add_elements(arr, k){ return\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := EvaluateWith("case-w", nodeAccept(t), fixture, withFile(base, "src/add_elements.js", tc.content), runner)
			if err != nil {
				t.Fatalf("EvaluateWith: %v", err)
			}
			if doc.Passed != tc.want {
				t.Fatalf("passed = %v, want %v: %+v", doc.Passed, tc.want, doc.Results)
			}
		})
	}
	// The untouched write fixture has no file: it fails without running anything.
	doc, err := EvaluateWith("case-w", nodeAccept(t), fixture, base, failingRunner{})
	if err != nil || doc.Passed {
		t.Fatalf("missing file: %+v %v", doc, err)
	}
}

func TestNodeTestEditRequiresTheSeededBugFixed(t *testing.T) {
	runner := stdinNode{testNode(t)}
	seeded := strings.Replace(canonicalAdd, "< 100", "< 1000", 1)
	fixture, base := nodeCase(map[string]string{"src/add_elements.js": seeded})
	acc := nodeAccept(t)
	// The seeded version must fail, so a run that leaves it untouched is not accepted.
	if doc, err := EvaluateWith("case-e", acc, fixture, base, runner); err != nil || doc.Passed {
		t.Fatalf("seeded fixture: %+v %v", doc, err)
	}
	// A repair formatted differently from the canonical solution is accepted.
	repaired := strings.Replace(seeded, "Math.abs(arr[i]) < 1000", "arr[i] > -100 && arr[i] < 100", 1)
	if doc, err := EvaluateWith("case-e", acc, fixture, withFile(base, "src/add_elements.js", repaired), runner); err != nil || !doc.Passed {
		t.Fatalf("repaired: %+v %v", doc, err)
	}
	// SelfCheckWith proves both directions with the canonical reference solution.
	if err := SelfCheckWith(fixture, acc, solution(t, writeOp("src/add_elements.js", canonicalAdd)), runner); err != nil {
		t.Fatalf("SelfCheckWith: %v", err)
	}
}

// Without an isolated runner, or when the runner fails, a behavioural check is refused: an error,
// never a pass.
func TestNodeTestFailsClosedWithoutARunner(t *testing.T) {
	fixture, base := nodeCase(nil)
	post := withFile(base, "src/add_elements.js", canonicalAdd)
	for name, runner := range map[string]NodeRunner{"no runner": nil, "runner error": failingRunner{}} {
		doc, err := EvaluateWith("case-r", nodeAccept(t), fixture, post, runner)
		if err == nil || doc.Passed {
			t.Errorf("%s: got %+v, %v; want a refusal", name, doc, err)
		}
	}
	if _, err := EvaluateWith("case-r", nodeAccept(t), fixture, post, nil); err == nil {
		t.Error("Evaluate without a runner accepted a behavioural check")
	}
	if err := SelfCheckWith(fixture, nodeAccept(t), solution(t, writeOp("src/add_elements.js", canonicalAdd)), nil); err == nil {
		t.Error("SelfCheck without a runner accepted a behavioural check")
	}
}

func TestNodeTestSpecValidation(t *testing.T) {
	for name, c := range map[string]Check{
		"no text":      {Op: OpNodeTest, Path: "src/a.js"},
		"no path":      {Op: OpNodeTest, Text: addTests},
		"stray digest": {Op: OpNodeTest, Path: "src/a.js", Text: addTests, Digest: sha("x")},
	} {
		if _, err := ParseAccept(marshal(t, acceptWire{Schema: AcceptSchema, Checks: []Check{c}})); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
