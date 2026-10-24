package corpusadmit

import (
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/trialfix"
)

// pilotNode is the local node of the admission self-check tests: node on PATH, else the Windows
// node through WSL interop (the WSL gate has no Linux node). Missing node fails the test.
func pilotNode(t *testing.T) LocalNodeRunner {
	t.Helper()
	if p, err := exec.LookPath("node"); err == nil {
		return LocalNodeRunner{Path: p}
	}
	const interop = "/mnt/c/Program Files/nodejs/node.exe"
	if _, err := os.Stat(interop); err == nil {
		return LocalNodeRunner{Path: interop}
	}
	t.Fatal("no node binary for the behavioural acceptance tests")
	return LocalNodeRunner{}
}

func pilotCase(t *testing.T, id string) evalcorpus.Case {
	t.Helper()
	for _, c := range loadPilotManifest(t).Cases {
		if c.CaseID == id {
			return c
		}
	}
	t.Fatalf("no case %s", id)
	return evalcorpus.Case{}
}

// postTree is the fixture of c with path replaced by content.
func postTree(t *testing.T, c evalcorpus.Case, path, content string) fstest.MapFS {
	t.Helper()
	fixture, err := LoadFixture(os.DirFS("../../../corpus/pilot/artifacts"), c.Compliant.WorkspaceFixtureDigest)
	if err != nil {
		t.Fatal(err)
	}
	files, err := fixture.Files()
	if err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{".": &fstest.MapFile{Mode: fs.ModeDir | fs.FileMode(trialfix.RootMode)}}
	for _, f := range files {
		if f.Dir {
			m[f.Path] = &fstest.MapFile{Mode: fs.ModeDir | fs.FileMode(f.Mode)}
		} else {
			m[f.Path] = &fstest.MapFile{Data: f.Content, Mode: fs.FileMode(f.Mode)}
		}
	}
	m[path] = &fstest.MapFile{Data: []byte(content), Mode: 0o644}
	return m
}

// The HumanEval-JS write and edit tasks accept any correct solution, however it is written, and
// reject a wrong one: the witness is the dataset's node:assert tests, not the canonical bytes.
func TestPilotHumanEvalWitnessIsBehavioural(t *testing.T) {
	runner := pilotNode(t)
	arts := os.DirFS("../../../corpus/pilot/artifacts")
	for _, tc := range []struct {
		id, path, content string
		want              bool
	}{
		{"PILOT-CMP-write-02", "src/add_elements.js",
			"const add_elements = (arr, k) =>\n  arr.slice(0, k).filter(x => x > -100 && x < 100).reduce((a, b) => a + b, 0);\n", true},
		{"PILOT-CMP-write-02", "src/add_elements.js",
			"function add_elements(arr, k) { return arr.slice(0, k).reduce((a, b) => a + b, 0); }\n", false},
		{"PILOT-CMP-edit-02", "src/is_equal_to_sum_even.js",
			"function is_equal_to_sum_even(n){\n  // four positive evens sum to at least 8\n  return !(n & 1) && 8 <= n;\n}\n", true},
		{"PILOT-CMP-edit-02", "src/is_equal_to_sum_even.js",
			"function is_equal_to_sum_even(n){\n  return n % 2 === 0;\n}\n", false},
	} {
		c := pilotCase(t, tc.id)
		fixture, err := LoadFixture(arts, c.Compliant.WorkspaceFixtureDigest)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := LoadAccept(arts, c.Compliant.AcceptanceTest.Digest)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := taskaccept.EvaluateWith(tc.id, spec, fixture, postTree(t, c, tc.path, tc.content), runner)
		if err != nil || doc.Passed != tc.want {
			t.Errorf("%s %q: passed %v (%v), want %v: %+v", tc.id, tc.content, doc.Passed, err, tc.want, doc.Results)
		}
	}
}

// A bash task's recorded output must be unguessable without running its command: the expected
// stdout appears in no fixture file, writing the text visible in the test file is rejected, and
// writing what node actually prints for the fixture is accepted.
func TestPilotBashOutputNeedsExecution(t *testing.T) {
	runner := pilotNode(t)
	arts := os.DirFS("../../../corpus/pilot/artifacts")
	for _, c := range loadPilotManifest(t).Cases {
		if c.Compliant == nil || c.Compliant.PrimaryTool != evalcorpus.ToolBash {
			continue
		}
		t.Run(c.CaseID, func(t *testing.T) {
			fixture, err := LoadFixture(arts, c.Compliant.WorkspaceFixtureDigest)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := LoadAccept(arts, c.Compliant.AcceptanceTest.Digest)
			if err != nil {
				t.Fatal(err)
			}
			files, err := fixture.Files()
			if err != nil {
				t.Fatal(err)
			}
			var testFile, fn string
			for _, f := range files {
				if strings.HasSuffix(f.Path, ".test.js") {
					testFile, fn = string(f.Content), strings.TrimSuffix(strings.TrimPrefix(f.Path, "src/"), ".test.js")
				}
			}
			out := "out/" + fn + ".txt"
			run, err := runner.RunNode([]byte(testFile))
			if err != nil || run.Exit != 0 {
				t.Fatalf("node: %+v %v", run, err)
			}
			want := strings.TrimSpace(string(run.Stdout))
			for _, f := range files {
				if want != "" && strings.Contains(string(f.Content), want) {
					t.Errorf("the output %q is readable in the fixture file %s", want, f.Path)
				}
			}
			for content, accepted := range map[string]bool{fn + ": tests passed\n": false, string(run.Stdout): true} {
				doc, err := taskaccept.Evaluate(c.CaseID, spec, fixture, postTree(t, c, out, content))
				if err != nil || doc.Passed != accepted {
					t.Errorf("writing %q: passed %v (%v), want %v", content, doc.Passed, err, accepted)
				}
			}
		})
	}
}
