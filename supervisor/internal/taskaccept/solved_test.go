package taskaccept

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/trialfix"
)

func TestSolvedFixtureIsTheTreeTheSolutionLeaves(t *testing.T) {
	fixture := fixtureSpec()
	sol := solution(t,
		Op{Op: SolWrite, Path: "src/main.go", Mode: "0644", ContentB64: b64("package main\nfunc main() {}\n")},
		Op{Op: SolMkdir, Path: "out", Mode: "0755"},
		Op{Op: SolWrite, Path: "out/result.txt", Mode: "0600", ContentB64: b64("done\n")},
		Op{Op: SolRemove, Path: "notes.txt"})
	solved, err := sol.Solved(fixture)
	if err != nil {
		t.Fatal(err)
	}
	// What the acceptance witness sees of the solved spec is what it sees of the applied tree.
	post := fstest.MapFS{".": &fstest.MapFile{Mode: fs.ModeDir | 0o700}}
	files, err := solved.Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Dir {
			post[f.Path] = &fstest.MapFile{Mode: fs.ModeDir | fs.FileMode(f.Mode)}
		} else {
			post[f.Path] = &fstest.MapFile{Data: f.Content, Mode: fs.FileMode(f.Mode)}
		}
	}
	doc, err := Evaluate("case-1", accept(t, Check{Op: OpContains, Path: "src/main.go", Text: "func main"},
		Check{Op: OpAbsent, Path: "notes.txt"}, Check{Op: OpContains, Path: "out/result.txt", Text: "done"},
		Check{Op: OpUnchangedExcept, Paths: []string{"src/main.go", "out", "out/result.txt", "notes.txt"}}), fixture, post)
	if err != nil || !doc.Passed {
		t.Fatalf("the solved spec is not the applied tree: %+v %v", doc, err)
	}
	want, _ := trialfix.Digest(solved)
	if untouched, _ := trialfix.Digest(fixture); want == untouched {
		t.Fatal("the solved tree digests like the untouched fixture")
	}
	if _, err := solution(t, Op{Op: SolRemove, Path: "missing"}).Solved(fixture); err == nil {
		t.Fatal("a solution that does not apply produced a tree")
	}
}

func TestCaptureBindingsStayOutOfOtherResults(t *testing.T) {
	doc, err := Evaluate("case-1", accept(t, Check{Op: OpAbsent, Path: "gone"}), fixtureSpec(), fixtureFS(t))
	if err != nil {
		t.Fatal(err)
	}
	if raw := string(marshal(t, doc)); raw != `{"schema":"tbound-accept-result/v1","case_id":"case-1","spec_digest":"`+doc.SpecDigest+`","results":[{"op":"absent","path":"gone","passed":true,"detail":"the path is absent"}],"passed":true}` {
		t.Fatalf("a result that is not a capture changed its bytes: %s", raw)
	}
}
