//go:build linux

package taskaccept

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/trialfix"
)

// writeFixtureTree writes fixtureSpec to dir with its modes, as a run's start state.
func writeFixtureTree(t *testing.T, dir string) {
	t.Helper()
	files, err := fixtureSpec().Files()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, os.FileMode(trialfix.RootMode)); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if f.Dir {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(p, f.Content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, os.FileMode(f.Mode)); err != nil {
			t.Fatal(err)
		}
	}
}

func openRoot(t *testing.T, dir string) *RootFS {
	t.Helper()
	r, err := OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestRootFSEvaluatesPassAndFail(t *testing.T) {
	dir := t.TempDir()
	writeFixtureTree(t, dir)
	check := accept(t,
		Check{Op: OpContains, Path: "src/main.go", Text: "func main"},
		Check{Op: OpUnchangedExcept, Paths: []string{"src/main.go"}},
	)
	doc, err := Evaluate("case-1", check, fixtureSpec(), openRoot(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Passed || doc.Results[0].Passed || !doc.Results[1].Passed {
		t.Fatalf("untouched tree: %+v", doc)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err = Evaluate("case-1", check, fixtureSpec(), openRoot(t, dir))
	if err != nil || !doc.Passed {
		t.Fatalf("solved tree: %+v, %v", doc, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err = Evaluate("case-1", check, fixtureSpec(), openRoot(t, dir))
	if err != nil || doc.Passed || doc.Results[1].Passed {
		t.Fatalf("collateral change must fail unchanged_except: %+v, %v", doc, err)
	}
}

func TestRootFSNeverFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	writeFixtureTree(t, dir)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("func main"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink that would satisfy a contains check if it were followed, and a directory
	// symlink that would make a file appear under it.
	if err := os.Symlink(secret, filepath.Join(dir, "link.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	// Relative links that stay inside the tree: only the no-symlink rule refuses them.
	if err := os.Symlink("src/main.go", filepath.Join(dir, "alias.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src", filepath.Join(dir, "srclink")); err != nil {
		t.Fatal(err)
	}
	r := openRoot(t, dir)

	if res := evaluate(t, r, Check{Op: OpContains, Path: "link.go", Text: "func main"}); res.Passed {
		t.Fatalf("contains followed a symlink: %+v", res)
	}
	if res := evaluate(t, r, Check{Op: OpFileSHA256, Path: "link.go", Digest: sha("func main")}); res.Passed {
		t.Fatalf("file_sha256 followed a symlink: %+v", res)
	}
	if res := evaluate(t, r, Check{Op: OpContains, Path: "dirlink/secret.txt", Text: "func main"}); res.Passed {
		t.Fatalf("contains went through a directory symlink: %+v", res)
	}
	// The link itself is a non-regular file at its own path: present, so absent fails and a
	// tree check sees an addition.
	if res := evaluate(t, r, Check{Op: OpAbsent, Path: "link.go"}); res.Passed {
		t.Fatalf("absent did not see the symlink: %+v", res)
	}
	res := evaluate(t, r, Check{Op: OpUnchangedExcept})
	if res.Passed {
		t.Fatalf("unchanged_except ignored the symlinks: %+v", res)
	}
	for _, want := range []string{"link.go was added", "dirlink was added"} {
		if !strings.Contains(res.Detail, want) {
			t.Fatalf("detail %q lacks %q", res.Detail, want)
		}
	}
	for _, name := range []string{"alias.go", "srclink/main.go"} {
		if _, err := r.Open(name); err == nil {
			t.Fatalf("Open followed the in-tree link %s", name)
		}
	}
	if res := evaluate(t, r, Check{Op: OpContains, Path: "srclink/main.go", Text: "package main"}); res.Passed {
		t.Fatalf("contains went through an in-tree directory link: %+v", res)
	}
	if _, err := r.Open("link.go"); err == nil {
		t.Fatal("Open followed a symlink")
	}
	if _, err := r.Open("dirlink/secret.txt"); err == nil {
		t.Fatal("Open went through a directory symlink")
	}
	if _, err := fs.ReadDir(r, "dirlink"); err == nil {
		t.Fatal("ReadDir went through a directory symlink")
	}
}

func TestRootFSRefusesEscapesAndBadNames(t *testing.T) {
	dir := t.TempDir()
	writeFixtureTree(t, dir)
	r := openRoot(t, dir)
	for _, name := range []string{"../x", "/etc/passwd", "src/../../x", ""} {
		if _, err := r.Open(name); err == nil {
			t.Fatalf("Open(%q) succeeded", name)
		}
		if _, err := r.Stat(name); err == nil {
			t.Fatalf("Stat(%q) succeeded", name)
		}
	}
}

func TestOpenRootRefusesASymlinkRoot(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if r, err := OpenRoot(link); err == nil {
		r.Close()
		t.Fatal("OpenRoot followed a symlink")
	}
	if r, err := OpenRoot(filepath.Join(dir, "missing")); err == nil {
		r.Close()
		t.Fatal("OpenRoot opened a missing directory")
	}
}

func TestRootFSSurvivesTheRootBeingRenamed(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "ws")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureTree(t, dir)
	r := openRoot(t, dir)
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	// A symlink put at the old path must not redirect the held root.
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if res := evaluate(t, r, Check{Op: OpContains, Path: "src/main.go", Text: "package main"}); !res.Passed {
		t.Fatalf("the held root was redirected: %+v", res)
	}
}
