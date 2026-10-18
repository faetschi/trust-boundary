package taskaccept

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEvaluateEachOperationPassesAndFails(t *testing.T) {
	post := fixtureFS(t)
	post["src/main.go"] = &fstest.MapFile{Data: []byte("package main\nfunc main() {}\n"), Mode: 0o644}
	for _, tc := range []struct {
		name  string
		check Check
		want  bool
		hint  string
	}{
		{"sha256 matches", Check{Op: OpFileSHA256, Path: "src/main.go", Digest: sha("package main\nfunc main() {}\n")}, true, ""},
		{"sha256 differs", Check{Op: OpFileSHA256, Path: "src/main.go", Digest: sha("package main\n")}, false, "digest is"},
		{"sha256 missing file", Check{Op: OpFileSHA256, Path: "src/none.go", Digest: sha("x")}, false, "missing"},
		{"sha256 of a directory", Check{Op: OpFileSHA256, Path: "src", Digest: sha("x")}, false, "not a regular file"},
		{"contains finds", Check{Op: OpContains, Path: "src/main.go", Text: "func main"}, true, ""},
		{"contains absent text", Check{Op: OpContains, Path: "src/main.go", Text: "func other"}, false, "not in the file"},
		{"contains missing file", Check{Op: OpContains, Path: "nope", Text: "x"}, false, "missing"},
		{"contains in a directory", Check{Op: OpContains, Path: "src", Text: "x"}, false, "regular"},
		{"absent for a missing path", Check{Op: OpAbsent, Path: "tmp.out"}, true, ""},
		{"absent for a file", Check{Op: OpAbsent, Path: "notes.txt"}, false, "present"},
		{"absent for a directory", Check{Op: OpAbsent, Path: "src"}, false, "present"},
		{"unchanged_except lists the edit", Check{Op: OpUnchangedExcept, Paths: []string{"src/main.go"}}, true, ""},
		{"unchanged_except misses the edit", Check{Op: OpUnchangedExcept}, false, "src/main.go has other content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := evaluate(t, post, tc.check)
			if r.Passed != tc.want || !strings.Contains(r.Detail, tc.hint) || r.Op != tc.check.Op {
				t.Fatalf("result %+v, want passed=%v detail containing %q", r, tc.want, tc.hint)
			}
		})
	}
}

func TestEvaluateDocument(t *testing.T) {
	raw := marshal(t, AcceptSpec{Schema: AcceptSchema, Checks: []Check{
		{Op: OpAbsent, Path: "gone"}, {Op: OpContains, Path: "notes.txt", Text: "nope"}}})
	spec, err := ParseAccept(raw)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Evaluate("cmp-7", spec, fixtureSpec(), fixtureFS(t))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != ResultSchema || doc.CaseID != "cmp-7" || doc.SpecDigest != sha(string(raw)) || doc.SpecDigest != spec.Digest() {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.Passed || len(doc.Results) != 2 || !doc.Results[0].Passed || doc.Results[1].Passed {
		t.Fatalf("results = %+v", doc.Results)
	}
	if doc.FirstFailure() == "" {
		t.Fatal("FirstFailure is empty for a failed document")
	}
	// A spec that did not come from ParseAccept has no digest and is refused.
	if _, err := Evaluate("x", AcceptSpec{Schema: AcceptSchema, Checks: spec.Checks}, fixtureSpec(), fixtureFS(t)); err == nil {
		t.Fatal("Evaluate accepted a spec without a parsed digest")
	}
}

func TestUnchangedExceptCatchesEveryKindOfChange(t *testing.T) {
	check := Check{Op: OpUnchangedExcept, Paths: []string{"notes.txt"}}
	if r := evaluate(t, fixtureFS(t), check); !r.Passed {
		t.Fatalf("the untouched fixture changed: %+v", r)
	}
	for _, tc := range []struct {
		name   string
		mutate func(fstest.MapFS)
		hint   string
	}{
		{"extra file", func(m fstest.MapFS) { m["extra.txt"] = &fstest.MapFile{Data: []byte("x"), Mode: 0o644} }, "extra.txt was added"},
		{"extra directory", func(m fstest.MapFS) { m["newdir"] = &fstest.MapFile{Mode: fs.ModeDir | 0o755} }, "newdir was added"},
		{"extra file in a directory", func(m fstest.MapFS) { m["src/new.go"] = &fstest.MapFile{Data: []byte("x"), Mode: 0o644} }, "src/new.go was added"},
		{"mode change", func(m fstest.MapFS) { m["run.sh"].Mode = 0o644 }, "run.sh changed from -rwxr-xr-x to -rw-r--r--"},
		{"directory mode change", func(m fstest.MapFS) { m["src"].Mode = fs.ModeDir | 0o700 }, "src changed"},
		{"deletion", func(m fstest.MapFS) { delete(m, "src/util.go") }, "src/util.go was deleted"},
		{"content change, same size", func(m fstest.MapFS) { m["run.sh"].Data = []byte("#!/bin/zs\n") }, "run.sh has other content"},
		{"content change, other size", func(m fstest.MapFS) { m["src/main.go"].Data = []byte("x") }, "src/main.go has other content"},
		{"file replaced by a symlink", func(m fstest.MapFS) {
			m["run.sh"] = &fstest.MapFile{Data: []byte("notes.txt"), Mode: fs.ModeSymlink | 0o777}
		}, "run.sh changed"},
		{"file replaced by a directory", func(m fstest.MapFS) { m["run.sh"] = &fstest.MapFile{Mode: fs.ModeDir | 0o755} }, "run.sh changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			post := fixtureFS(t)
			tc.mutate(post)
			r := evaluate(t, post, check)
			if r.Passed || !strings.Contains(r.Detail, tc.hint) {
				t.Fatalf("result %+v, want a failure containing %q", r, tc.hint)
			}
		})
	}
}

func TestUnchangedExceptListedPathsAreExempt(t *testing.T) {
	post := fixtureFS(t)
	post["notes.txt"] = &fstest.MapFile{Data: []byte("done\n"), Mode: 0o600}
	delete(post, "run.sh")
	post["out"] = &fstest.MapFile{Mode: fs.ModeDir | 0o755}
	post["out/report.txt"] = &fstest.MapFile{Data: []byte("r"), Mode: 0o644}
	check := Check{Op: OpUnchangedExcept, Paths: []string{"notes.txt", "run.sh", "out", "out/report.txt"}}
	if r := evaluate(t, post, check); !r.Passed {
		t.Fatalf("listed changes were refused: %+v", r)
	}
	// A listed directory does not exempt what is under it.
	check.Paths = []string{"notes.txt", "run.sh", "out"}
	if r := evaluate(t, post, check); r.Passed || !strings.Contains(r.Detail, "out/report.txt was added") {
		t.Fatalf("an unlisted child of a listed directory passed: %+v", r)
	}
}

func TestUnchangedExceptBoundsTheDetail(t *testing.T) {
	post := fixtureFS(t)
	for i := 0; i < 20; i++ {
		post[string(rune('a'+i))+".tmp"] = &fstest.MapFile{Data: []byte("x"), Mode: 0o644}
	}
	r := evaluate(t, post, Check{Op: OpUnchangedExcept})
	if r.Passed || !strings.Contains(r.Detail, "and 12 more") || r.Path != "." {
		t.Fatalf("result %+v", r)
	}
}

func TestEvaluateIgnoresLostFoundAndDoesNotFollowSymlinks(t *testing.T) {
	post := fixtureFS(t)
	post["lost+found"] = &fstest.MapFile{Mode: fs.ModeDir | 0o700}
	post["lost+found/x"] = &fstest.MapFile{Data: []byte("x"), Mode: 0o600}
	if r := evaluate(t, post, Check{Op: OpUnchangedExcept}); !r.Passed {
		t.Fatalf("lost+found counted as a change: %+v", r)
	}
	post = fixtureFS(t)
	post["link"] = &fstest.MapFile{Data: []byte("notes.txt"), Mode: fs.ModeSymlink | 0o777}
	if r := evaluate(t, post, Check{Op: OpContains, Path: "link", Text: "todo"}); r.Passed || !strings.Contains(r.Detail, "regular") {
		t.Fatalf("a symlink satisfied contains: %+v", r)
	}
	if r := evaluate(t, post, Check{Op: OpFileSHA256, Path: "link", Digest: sha("todo\n")}); r.Passed {
		t.Fatalf("a symlink satisfied file_sha256: %+v", r)
	}
	if r := evaluate(t, post, Check{Op: OpAbsent, Path: "link"}); r.Passed {
		t.Fatalf("a symlink counted as absent: %+v", r)
	}
}
