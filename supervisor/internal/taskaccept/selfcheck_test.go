package taskaccept

import (
	"strings"
	"testing"
)

func writeOp(path, content string) Op {
	return Op{Op: SolWrite, Path: path, Mode: "0644", ContentB64: b64(content)}
}

// fixAccept accepts a fixture in which src/main.go gained a main function, nothing else
// changed except that file, and the scratch file is absent.
func fixAccept(t *testing.T) AcceptSpec {
	t.Helper()
	return accept(t,
		Check{Op: OpContains, Path: "src/main.go", Text: "func main"},
		Check{Op: OpAbsent, Path: "scratch.tmp"},
		Check{Op: OpUnchangedExcept, Paths: []string{"src/main.go"}})
}

func TestSelfCheckPassesInBothDirections(t *testing.T) {
	sol := solution(t, writeOp("src/main.go", "package main\nfunc main() {}\n"))
	if err := SelfCheck(fixtureSpec(), fixAccept(t), sol); err != nil {
		t.Fatalf("SelfCheck: %v", err)
	}
}

func TestSelfCheckRefusals(t *testing.T) {
	goodSol := solution(t, writeOp("src/main.go", "package main\nfunc main() {}\n"))
	for _, tc := range []struct {
		name string
		acc  AcceptSpec
		sol  SolutionSpec
		want string
	}{
		{"the untouched fixture already passes",
			accept(t, Check{Op: OpAbsent, Path: "scratch.tmp"}, Check{Op: OpUnchangedExcept}), goodSol,
			"untouched fixture already passes"},
		{"the solution leaves a failing check",
			fixAccept(t), solution(t, writeOp("src/main.go", "package main\n// no entry\n")),
			"does not pass the acceptance test: contains src/main.go"},
		{"the solution changes another file",
			fixAccept(t), solution(t, writeOp("src/main.go", "package main\nfunc main() {}\n"), Op{Op: SolWrite, Path: "notes.txt", Mode: "0600", ContentB64: b64("x")}),
			"does not pass the acceptance test: unchanged_except .: notes.txt has other content"},
		{"the solution leaves an extra file",
			fixAccept(t), solution(t, writeOp("src/main.go", "package main\nfunc main() {}\n"), writeOp("scratch.tmp", "x")),
			"scratch.tmp"},
		{"the solution does not apply",
			fixAccept(t), solution(t, writeOp("nodir/main.go", "x")),
			"does not apply to the fixture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := SelfCheck(fixtureSpec(), tc.acc, tc.sol)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SelfCheck error %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestSelfCheckWithRemoveAndMkdir(t *testing.T) {
	acc := accept(t,
		Check{Op: OpAbsent, Path: "src"},
		Check{Op: OpContains, Path: "docs/README", Text: "hello"},
		Check{Op: OpUnchangedExcept, Paths: []string{"src", "src/main.go", "src/util.go", "docs", "docs/README"}})
	sol := solution(t,
		Op{Op: SolRemove, Path: "src"},
		Op{Op: SolMkdir, Path: "docs", Mode: "0755"},
		writeOp("docs/README", "hello\n"))
	if err := SelfCheck(fixtureSpec(), acc, sol); err != nil {
		t.Fatalf("SelfCheck: %v", err)
	}
}

func TestSolutionApplySemantics(t *testing.T) {
	base, err := treeOfFixture(fixtureSpec())
	if err != nil {
		t.Fatal(err)
	}
	for name, ops := range map[string][]Op{
		"remove of a missing path":        {{Op: SolRemove, Path: "nope"}},
		"mkdir over a file":               {{Op: SolMkdir, Path: "notes.txt", Mode: "0755"}},
		"mkdir over a directory":          {{Op: SolMkdir, Path: "src", Mode: "0755"}},
		"write over a directory":          {writeOp("src", "x")},
		"write below a file":              {writeOp("notes.txt/x", "x")},
		"write below a removed directory": {{Op: SolRemove, Path: "src"}, writeOp("src/a.go", "x")},
		"nested mkdir without the parent": {{Op: SolMkdir, Path: "a/b", Mode: "0755"}},
	} {
		if _, err := solution(t, ops...).apply(base); err == nil {
			t.Errorf("%s: apply succeeded", name)
		}
	}
	got, err := solution(t, Op{Op: SolRemove, Path: "src"}, Op{Op: SolMkdir, Path: "src", Mode: "0700"}, writeOp("src/new", "n")).apply(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["src/main.go"]; ok || got["src"].mode.Perm() != 0o700 || string(got["src/new"].content) != "n" {
		t.Fatalf("tree after apply = %+v", got)
	}
	if _, ok := base["src/main.go"]; !ok {
		t.Fatal("apply modified the fixture tree")
	}
	var many []Op
	for i := 0; i < 9; i++ {
		many = append(many, Op{Op: SolWrite, Path: string(rune('a'+i)) + "big", Mode: "0644", ContentB64: b64(strings.Repeat("x", 1<<20))})
	}
	if _, err := (SolutionSpec{ops: many}).apply(base); err == nil || !strings.Contains(err.Error(), "bytes of content") {
		t.Fatalf("a solution over the total content bound: %v", err)
	}
}
