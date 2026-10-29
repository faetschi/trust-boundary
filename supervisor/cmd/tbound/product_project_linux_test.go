//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func projectCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(append([]string{"project"}, args...), &stdout, &stderr)
	return stdout.String(), err
}

func projectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func projectPut(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, mode); err != nil {
		t.Fatal(err)
	}
}

func projectGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestProjectCommandStrictness(t *testing.T) {
	for name, args := range map[string][]string{
		"no command":          {},
		"unknown command":     {"sync"},
		"bundle without out":  {"bundle", "--source", "x"},
		"bundle unknown flag": {"bundle", "--source", "x", "--out", "y", "--follow-links"},
		"bundle positional":   {"bundle", "--source", "x", "--out", "y", "extra"},
		"import without dest": {"import", "--bundle", "x"},
		"export without base": {"export", "--current", "x", "--out", "y"},
		"apply without state": {"apply", "--bundle", "x", "--worktree", "y"},
	} {
		if _, err := projectCLI(t, args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestProjectBundleImportExportCommands(t *testing.T) {
	work := projectDir(t)
	src := filepath.Join(work, "src")
	projectPut(t, src, "main.go", "package main\n", 0o644)
	projectPut(t, src, "bin/run.sh", "#!/bin/sh\n", 0o755)
	projectPut(t, src, "secret/token", "hunter2", 0o600)
	projectPut(t, src, ".git/config", "[core]\n", 0o644)
	bundle := filepath.Join(work, "project.tbpb")

	out, err := projectCLI(t, "bundle", "--source", src, "--exclude", "secret", "--out", bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"included: 3 entries (2 files, 1 directories", "excluded: 2", "  .git\n", "  secret\n", "manifest digest: sha256:"} {
		if !strings.Contains(out, want) {
			t.Errorf("bundle output lacks %q:\n%s", want, out)
		}
	}
	if _, err := projectCLI(t, "bundle", "--source", src, "--out", bundle); err == nil {
		t.Error("bundle replaced an existing output file")
	}
	if _, err := projectCLI(t, "bundle", "--source", src, "--out", filepath.Join(src, "inside.tbpb")); err == nil {
		t.Error("bundle accepted an output file inside the source")
	}

	parent := projectDir(t)
	managed := filepath.Join(parent, "managed")
	manifest := filepath.Join(work, "baseline.json")
	out, err = projectCLI(t, "import", "--bundle", bundle, "--dest", managed, "--manifest-out", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "manifest digest: sha256:") {
		t.Errorf("import output:\n%s", out)
	}
	if _, err := projectCLI(t, "import", "--bundle", bundle, "--dest", managed); err == nil {
		t.Error("import replaced an existing destination")
	}
	if _, err := os.Stat(filepath.Join(managed, "secret")); err == nil {
		t.Error("an excluded path was imported")
	}

	if _, err := projectCLI(t, "export", "--baseline-manifest", manifest, "--current", managed, "--out", filepath.Join(work, "none.tbpx")); err == nil {
		t.Error("export of an unchanged project succeeded")
	}
	projectPut(t, managed, "main.go", "package main // edited\n", 0o600)
	projectPut(t, managed, "docs/readme.txt", "new\n", 0o600)
	exported := filepath.Join(work, "project.tbpx")
	out, err = projectCLI(t, "export", "--baseline-manifest", manifest, "--current", managed, "--out", exported)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "changes: 3 (create 2, modify 1, delete 0, chmod 0)") {
		t.Errorf("export output:\n%s", out)
	}
}

func TestProjectBundlePrintsEveryIncludedPath(t *testing.T) {
	work := projectDir(t)
	src := filepath.Join(work, "src")
	projectPut(t, src, "main.go", "package main\n", 0o644)
	projectPut(t, src, "bin/run.sh", "#!/bin/sh\n", 0o755)
	projectPut(t, src, "secret/token", "hunter2", 0o600)
	out, err := projectCLI(t, "bundle", "--source", src, "--exclude", "secret", "--out", filepath.Join(work, "project.tbpb"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\n  bin\n  bin/run.sh\n  main.go\nexcluded: 1\n") {
		t.Errorf("bundle output does not list each included path under the included line:\n%s", out)
	}
}

func TestProjectBundleRefusalNamesPathAndReason(t *testing.T) {
	work := projectDir(t)
	src := filepath.Join(work, "src")
	projectPut(t, src, "main.go", "package main\n", 0o644)
	if err := os.Symlink("main.go", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(work, "project.tbpb")
	_, err := projectCLI(t, "bundle", "--source", src, "--out", bundle)
	if err == nil || !strings.Contains(err.Error(), "link: symlink") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Lstat(bundle); statErr == nil {
		t.Error("a refused project left an output file")
	}
	if entries, _ := os.ReadDir(work); len(entries) != 1 {
		t.Errorf("work directory holds %d entries, want only the source", len(entries))
	}
}

func TestProjectApplyCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed: the apply command needs a real Git worktree")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	work := projectDir(t)
	tree := filepath.Join(work, "tree")
	projectPut(t, tree, "main.go", "package main\n", 0o644)
	projectPut(t, tree, "gone.txt", "bye\n", 0o644)
	projectGit(t, tree, "init", "-q")
	projectGit(t, tree, "add", "-A")
	projectGit(t, tree, "commit", "-q", "-m", "init")

	bundle := filepath.Join(work, "project.tbpb")
	manifest := filepath.Join(work, "baseline.json")
	managed := filepath.Join(projectDir(t), "managed")
	for _, args := range [][]string{
		{"bundle", "--source", tree, "--out", bundle},
		{"import", "--bundle", bundle, "--dest", managed, "--manifest-out", manifest},
	} {
		if _, err := projectCLI(t, args...); err != nil {
			t.Fatal(err)
		}
	}
	projectPut(t, managed, "main.go", "package main // edited\n", 0o600)
	projectPut(t, managed, "pkg/new.go", "package pkg\n", 0o600)
	if err := os.Remove(filepath.Join(managed, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	exported := filepath.Join(work, "project.tbpx")
	if _, err := projectCLI(t, "export", "--baseline-manifest", manifest, "--current", managed, "--out", exported); err != nil {
		t.Fatal(err)
	}

	state := projectDir(t)
	projectPut(t, tree, "main.go", "dirty\n", 0o644)
	if _, err := projectCLI(t, "apply", "--bundle", exported, "--worktree", tree, "--state-dir", state); err == nil {
		t.Fatal("apply to a dirty worktree succeeded")
	}
	projectGit(t, tree, "checkout", "--", "main.go")

	out, err := projectCLI(t, "apply", "--direct", "--writer-exclusion-held", "--bundle", exported, "--worktree", tree, "--state-dir", state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "applied 4 changes") {
		t.Errorf("apply output:\n%s", out)
	}
	for rel, want := range map[string]string{"main.go": "package main // edited\n", "pkg/new.go": "package pkg\n"} {
		if got, _ := os.ReadFile(filepath.Join(tree, rel)); string(got) != want {
			t.Errorf("%s = %q", rel, got)
		}
	}
	if _, err := os.Lstat(filepath.Join(tree, "gone.txt")); err == nil {
		t.Error("gone.txt was not deleted")
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Errorf("recovery record left after a verified apply: %d entries", len(entries))
	}
}

func TestProjectApplyDefaultPatchInstructions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	work := projectDir(t)
	tree := filepath.Join(work, "tree with 'quote'")
	projectPut(t, tree, "f", "original\x00", 0644)
	projectGit(t, tree, "init", "-q")
	projectGit(t, tree, "add", "-A")
	projectGit(t, tree, "commit", "-qm", "initial")
	bundle := filepath.Join(work, "bundle")
	manifest := filepath.Join(work, "manifest")
	managed := filepath.Join(work, "managed")
	export := filepath.Join(work, "export")
	state := filepath.Join(work, "state with 'quote'")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"bundle", "--source", tree, "--out", bundle},
		{"import", "--bundle", bundle, "--dest", managed, "--manifest-out", manifest},
	} {
		if _, err := projectCLI(t, args...); err != nil {
			t.Fatal(err)
		}
	}
	projectPut(t, managed, "f", "changed\x00", 0700)
	if _, err := projectCLI(t, "export", "--baseline-manifest", manifest, "--current", managed, "--out", export); err != nil {
		t.Fatal(err)
	}
	out, err := projectCLI(t, "apply", "--bundle", export, "--worktree", tree, "--state-dir", state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "worktree unchanged") {
		t.Fatalf("output: %s", out)
	}
	if data, _ := os.ReadFile(filepath.Join(tree, "f")); string(data) != "original\x00" {
		t.Fatal("default apply wrote worktree")
	}
	commands := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "  git ") {
			continue
		}
		if output, err := exec.Command("sh", "-c", strings.TrimSpace(line)).CombinedOutput(); err != nil {
			t.Fatalf("printed command failed: %v: %s", err, output)
		}
		commands++
	}
	if commands != 2 {
		t.Fatalf("printed %d commands", commands)
	}
	if data, _ := os.ReadFile(filepath.Join(tree, "f")); string(data) != "changed\x00" {
		t.Fatal("printed git apply did not apply content")
	}
}
