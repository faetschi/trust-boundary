package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/guestevidence"
)

// The capture is exercised on a throwaway git repository that has the layout it needs: a module at
// supervisor/ with the two guest test packages, and the two pinned inputs.

func requireCaptureTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the capture is pinned to linux/amd64")
	}
	for _, tool := range []string{"git", "go", "gcc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed: the capture cannot run here", tool)
		}
	}
}

func goVersionLine(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

type miniFile struct {
	content string
	mode    os.FileMode
}

func baseMini() map[string]miniFile {
	return map[string]miniFile{
		"supervisor/go.mod":                                  {"module tbound/supervisor\n\ngo 1.26.0\n", 0o644},
		"supervisor/internal/alpha/doc.go":                   {"package alpha\n", 0o644},
		"supervisor/internal/alpha/a_test.go":                {"package alpha\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n", 0o644},
		"supervisor/internal/procoracle/doc.go":              {"package procoracle\n", 0o644},
		"supervisor/internal/procoracle/p_test.go":           {"package procoracle\n\nimport \"testing\"\n\nfunc TestRealCleanTeardownIsNoEffect(t *testing.T) {}\n", 0o644},
		"supervisor/cmd/tbound-cell-entrypoint/main.go":      {"package main\n\nfunc main() {}\n", 0o644},
		"supervisor/cmd/tbound-cell-entrypoint/main_test.go": {"package main\n\nimport \"testing\"\n\nfunc TestLandlockConnectRightIsPort8080OnlyAndOnlyForNetSink(t *testing.T) {}\n", 0o644},
		"infra/conformance/evq-mapping.json":                 {"{\"mapping\": 1}\n", 0o644},
		"infra/conformance/route-matrix.json":                {"{\"matrix\": 1}\n", 0o644},
		"scripts/run.sh":                                     {"#!/bin/sh\n", 0o755},
		"docs/note.md":                                       {"note\n", 0o644},
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func makeMiniRepo(t *testing.T, files map[string]miniFile, symlinks map[string]string) (repo, commit string) {
	t.Helper()
	repo = t.TempDir()
	gitIn(t, repo, "init", "-q")
	for name, f := range files {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.content), f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range symlinks {
		if err := os.Symlink(target, filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "update-index", "--chmod=+x", "scripts/run.sh")
	gitIn(t, repo, "commit", "-q", "-m", "mini")
	return repo, gitIn(t, repo, "rev-parse", "HEAD")
}

func (o evqCaptureOptions) with(repo, commit, out string, version string) evqCaptureOptions {
	o.repo, o.commit, o.out, o.goVersion = repo, commit, out, version
	o.goBin, o.gitBin, o.env, o.stdout = "go", "git", os.Environ(), &bytes.Buffer{}
	return o
}

// expectedTreeDigest recomputes the archive tree digest from the files the repository was made of.
func expectedTreeDigest(files map[string]miniFile) string {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	h := sha256.New()
	for _, n := range names {
		mode := "100644"
		if files[n].mode&0o111 != 0 {
			mode = "100755"
		}
		sum := sha256.Sum256([]byte(files[n].content))
		fmt.Fprintf(h, "%s %s %s\n", mode, hex.EncodeToString(sum[:]), n)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func TestEVQCaptureRecordsTheFrozenCommitsRun(t *testing.T) {
	requireCaptureTools(t)
	files := baseMini()
	repo, c := makeMiniRepo(t, files, nil)
	out := filepath.Join(t.TempDir(), "capture")
	version := goVersionLine(t)
	opts := evqCaptureOptions{}.with(repo, c, out, version)
	if err := runEVQCapture(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, evqCaptureRecord))
	if err != nil {
		t.Fatal(err)
	}
	var rec evqCapture
	if err := decodeStrict(data, &rec); err != nil {
		t.Fatal(err)
	}
	tree := gitIn(t, repo, "rev-parse", c+"^{tree}")
	if rec.Commit != c || rec.GitTree != tree || rec.ArchiveTreeDigest != expectedTreeDigest(files) || rec.ArchiveFiles != len(files) {
		t.Fatalf("record identifies the wrong tree: %+v (tree %s, digest %s)", rec, tree, expectedTreeDigest(files))
	}
	if !slices.Equal(rec.Argv, evqCaptureArgv) || rec.ExitStatus != 0 || rec.GoVersion != version || rec.WorkDir != "supervisor" {
		t.Fatalf("record invocation: %+v", rec)
	}
	for k, want := range evqPinnedEnv {
		if rec.Env[k] != want {
			t.Errorf("env %s = %q, want %q", k, rec.Env[k], want)
		}
	}
	wantPackages := []string{"tbound/supervisor/cmd/tbound-cell-entrypoint", "tbound/supervisor/internal/alpha", "tbound/supervisor/internal/procoracle"}
	if !slices.Equal(rec.Packages, wantPackages) {
		t.Fatalf("packages = %v", rec.Packages)
	}
	for _, p := range evqPinnedInputs {
		sum := sha256.Sum256([]byte(files[p].content))
		if rec.PinnedInputs[p] != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Errorf("pinned input %s = %s", p, rec.PinnedInputs[p])
		}
	}
	// The test JSON has the digest the record states, and the guest test binaries exist with their digests.
	if d, err := digestNonEmptyFile(filepath.Join(out, evqCaptureTestJSON)); err != nil || d != rec.TestJSONSHA256 {
		t.Fatalf("test json digest %s %v, record %s", d, err, rec.TestJSONSHA256)
	}
	if len(rec.TestBinaries) != len(guestevidence.TestPackages()) {
		t.Fatalf("test binaries = %v", rec.TestBinaries)
	}
	for pkg, digest := range rec.TestBinaries {
		bin := filepath.Join(out, evqGuestTestsDir, pkg[strings.LastIndex(pkg, "/")+1:]+".test")
		if d, err := digestNonEmptyFile(bin); err != nil || d != digest {
			t.Errorf("%s: %s %v, record %s", pkg, d, err, digest)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "work")); err == nil {
		t.Error("the work directory was left behind")
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("capture directory mode = %v %v", info, err)
	}
	text := opts.stdout.(*bytes.Buffer).String()
	for _, want := range []string{"commit " + c, "archive_tree_digest " + rec.ArchiveTreeDigest, "exit_status 0", "capture_sha256 sha256:"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	// The verifier accepts it for this commit only (when this machine has the pinned toolchain).
	if version == evqPinnedGoVersion {
		_, _, o, err := loadEVQCapture(out, c)
		if err != nil || !o.passed("tbound/supervisor/internal/alpha", "TestA") {
			t.Fatalf("loadEVQCapture: %v", err)
		}
		if _, _, _, err := loadEVQCapture(out, strings.Repeat("9", 40)); err == nil {
			t.Fatal("the capture was accepted for another commit")
		}
	}
	// A second capture into the same directory is refused and leaves the first alone.
	if err := runEVQCapture(context.Background(), opts); err == nil {
		t.Fatal("an existing capture directory was reused")
	}
	if after, _ := os.ReadFile(filepath.Join(out, evqCaptureRecord)); !bytes.Equal(after, data) {
		t.Fatal("the first capture was changed")
	}
}

func TestEVQCaptureOfAFailingRunIsStillARecord(t *testing.T) {
	requireCaptureTools(t)
	files := baseMini()
	files["supervisor/internal/alpha/a_test.go"] = miniFile{"package alpha\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { t.Fatal(\"broken\") }\n", 0o644}
	repo, c := makeMiniRepo(t, files, nil)
	out := filepath.Join(t.TempDir(), "capture")
	version := goVersionLine(t)
	if err := runEVQCapture(context.Background(), evqCaptureOptions{}.with(repo, c, out, version)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(out, evqCaptureRecord))
	var rec evqCapture
	if err := decodeStrict(data, &rec); err != nil || rec.ExitStatus != 1 {
		t.Fatalf("record = %+v %v", rec, err)
	}
	if version == evqPinnedGoVersion {
		_, _, o, err := loadEVQCapture(out, c)
		if err != nil || o.passed("tbound/supervisor/internal/alpha", "TestA") || !o.passed("tbound/supervisor/internal/procoracle", "TestRealCleanTeardownIsNoEffect") {
			t.Fatalf("failing capture: %v", err)
		}
	}
}

func TestEVQCaptureRefusals(t *testing.T) {
	requireCaptureTools(t)
	version := goVersionLine(t)
	repo, c := makeMiniRepo(t, baseMini(), nil)
	fresh := func() string { return filepath.Join(t.TempDir(), "capture") }
	run := func(o evqCaptureOptions) error { return runEVQCapture(context.Background(), o) }
	notARepo := t.TempDir()
	existing := t.TempDir()
	for name, o := range map[string]evqCaptureOptions{
		"abbreviated commit": evqCaptureOptions{}.with(repo, c[:12], fresh(), version),
		"a branch name":      evqCaptureOptions{}.with(repo, "master", fresh(), version),
		"upper case commit":  evqCaptureOptions{}.with(repo, strings.ToUpper(c), fresh(), version),
		"a commit not in it": evqCaptureOptions{}.with(repo, strings.Repeat("1", 40), fresh(), version),
		"the tree id":        evqCaptureOptions{}.with(repo, gitIn(t, repo, "rev-parse", c+"^{tree}"), fresh(), version),
		"not a repository":   evqCaptureOptions{}.with(notARepo, c, fresh(), version),
		"another toolchain":  evqCaptureOptions{}.with(repo, c, fresh(), "go version go1.1.1 linux/amd64"),
		"relative out":       evqCaptureOptions{}.with(repo, c, "capture", version),
		"out already exists": evqCaptureOptions{}.with(repo, c, existing, version),
		"no such go binary": func() evqCaptureOptions {
			o := evqCaptureOptions{}.with(repo, c, fresh(), version)
			o.goBin = "no-such-go"
			return o
		}(),
		"no such git binary": func() evqCaptureOptions {
			o := evqCaptureOptions{}.with(repo, c, fresh(), version)
			o.gitBin = "no-such-git"
			return o
		}(),
	} {
		err := run(o)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		// A refused capture leaves nothing that could pass for one.
		if name != "out already exists" && name != "relative out" {
			if _, statErr := os.Stat(o.out); statErr == nil {
				t.Errorf("%s: a refused capture left %s", name, o.out)
			}
		}
	}
}

func TestEVQCaptureRefusesATreeThatIsNotRegularFiles(t *testing.T) {
	requireCaptureTools(t)
	repo, c := makeMiniRepo(t, baseMini(), map[string]string{"link": "supervisor/go.mod"})
	out := filepath.Join(t.TempDir(), "capture")
	err := runEVQCapture(context.Background(), evqCaptureOptions{}.with(repo, c, out, goVersionLine(t)))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a commit with a symbolic link was captured: %v", err)
	}
}

// A test that writes into the tree it runs in would change what was frozen; the capture refuses it.
func TestEVQCaptureRefusesTestsThatChangeTheTree(t *testing.T) {
	requireCaptureTools(t)
	files := baseMini()
	files["supervisor/internal/alpha/a_test.go"] = miniFile{"package alpha\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestA(t *testing.T) {\n\tif err := os.WriteFile(\"stray.txt\", []byte(\"x\"), 0o644); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n", 0o644}
	repo, c := makeMiniRepo(t, files, nil)
	out := filepath.Join(t.TempDir(), "capture")
	err := runEVQCapture(context.Background(), evqCaptureOptions{}.with(repo, c, out, goVersionLine(t)))
	if err == nil || !strings.Contains(err.Error(), "changed the extracted tree") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("a capture of a tree-changing run was left behind")
	}
}

// ---- the extraction and its verification, without git ----

func miniTree(files map[string]miniFile) map[string]treeEntry {
	tree := map[string]treeEntry{}
	for n, f := range files {
		mode := "100644"
		if f.mode&0o111 != 0 {
			mode = "100755"
		}
		tree[n] = treeEntry{mode: mode, blob: gitBlobID([]byte(f.content))}
	}
	return tree
}

func gitBlobID(data []byte) string {
	return strings.TrimSpace(func() string {
		cmd := exec.Command("git", "hash-object", "--stdin")
		cmd.Stdin = bytes.NewReader(data)
		out, _ := cmd.Output()
		return string(out)
	}())
}

func tarOf(t *testing.T, comment string, entries ...*tar.Header) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if comment != "" {
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": comment}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range entries {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			w.Write(bytes.Repeat([]byte("x"), int(h.Size)))
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestExtractTarRefusesWhatIsNotTheCommitsTree(t *testing.T) {
	requireCaptureTools(t)
	tree := map[string]treeEntry{"a.txt": {mode: "100644", blob: gitBlobID([]byte("x"))}}
	reg := func(name string) *tar.Header {
		return &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: 1, Mode: 0o644}
	}
	for name, r := range map[string]*bytes.Reader{
		"parent traversal":    tarOf(t, c40, reg("../evil")),
		"absolute path":       tarOf(t, c40, reg("/etc/evil")),
		"a path with dotdot":  tarOf(t, c40, reg("a/../../evil")),
		"a file not in tree":  tarOf(t, c40, reg("b.txt")),
		"a symbolic link":     tarOf(t, c40, &tar.Header{Typeflag: tar.TypeSymlink, Name: "a.txt", Linkname: "/etc/passwd"}),
		"a hard link":         tarOf(t, c40, &tar.Header{Typeflag: tar.TypeLink, Name: "a.txt", Linkname: "b"}),
		"a device":            tarOf(t, c40, &tar.Header{Typeflag: tar.TypeChar, Name: "a.txt"}),
		"another commit":      tarOf(t, strings.Repeat("2", 40), reg("a.txt")),
		"no commit header":    tarOf(t, "", reg("a.txt")),
		"a file that is huge": tarOf(t, c40, &tar.Header{Typeflag: tar.TypeReg, Name: "a.txt", Size: 0, Mode: 0o644}),
	} {
		dir := t.TempDir()
		err := extractTar(r, tree, c40, dir)
		if name == "a file that is huge" {
			// An empty a.txt extracts, and then the blob check refuses it.
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if _, _, err := verifyTree(dir, tree); err == nil {
				t.Errorf("%s: the wrong content verified", name)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			t.Errorf("%s: left %s in the extraction", name, e.Name())
		}
	}
	if err := extractTar(tarOf(t, c40, reg("a.txt")), tree, c40, t.TempDir()); err != nil {
		t.Fatalf("the exact tree was refused: %v", err)
	}
}

const c40 = "0123456789abcdef0123456789abcdef01234567"

func TestVerifyTreeDetectsAnyDifference(t *testing.T) {
	requireCaptureTools(t)
	files := map[string]miniFile{"a.txt": {"alpha\n", 0o644}, "d/b.sh": {"#!/bin/sh\n", 0o755}}
	tree := miniTree(files)
	write := func(dir string) {
		for n, f := range files {
			p := filepath.Join(dir, filepath.FromSlash(n))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(f.content), f.mode)
			os.Chmod(p, f.mode)
		}
	}
	dir := t.TempDir()
	write(dir)
	digests, treeDigest, err := verifyTree(dir, tree)
	if err != nil || len(digests) != 2 || treeDigest != expectedTreeDigest(files) {
		t.Fatalf("clean tree: %v %v %s", digests, err, treeDigest)
	}
	for name, mutate := range map[string]func(dir string){
		"content changed":     func(d string) { os.WriteFile(filepath.Join(d, "a.txt"), []byte("alphA\n"), 0o644) },
		"file removed":        func(d string) { os.Remove(filepath.Join(d, "a.txt")) },
		"file added":          func(d string) { os.WriteFile(filepath.Join(d, "extra.txt"), []byte("x"), 0o644) },
		"made executable":     func(d string) { os.Chmod(filepath.Join(d, "a.txt"), 0o755) },
		"made non-executable": func(d string) { os.Chmod(filepath.Join(d, "d/b.sh"), 0o644) },
		"replaced by a link": func(d string) {
			os.Remove(filepath.Join(d, "a.txt"))
			os.Symlink("d/b.sh", filepath.Join(d, "a.txt"))
		},
	} {
		d := t.TempDir()
		write(d)
		mutate(d)
		if _, _, err := verifyTree(d, tree); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

func TestEVQCaptureRecordRoundTripsThroughStrictJSON(t *testing.T) {
	rec := evqCapture{Schema: evqCaptureSchema, Commit: c40, Argv: evqCaptureArgv, PinnedInputs: map[string]string{}, Env: map[string]string{}, TestBinaries: map[string]string{}}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back evqCapture
	if err := decodeStrict(data, &back); err != nil {
		t.Fatalf("a record does not decode under its own strict decoder: %v", err)
	}
}
