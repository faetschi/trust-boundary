package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/guestevidence"
)

// The EVQ test evidence is one controlled capture of the frozen commit: a clean `git archive` of the
// commit, the full filter-free `go test -race -count=1 -json ./...` of it under a pinned toolchain
// environment, and a record that binds the commit, the extracted tree, the toolchain, the exact
// invocation, the exit status and the test JSON together. `tbound-run evq` and `route-report` take
// test evidence only from such a capture, and only for the commit run.json freezes.
const (
	evqCaptureSchema   = "tbound-evq-capture/v1"
	evqCaptureRecord   = "capture.json"
	evqCaptureTestJSON = "go-test.json"
	evqCaptureStderr   = "go-test.stderr"
	evqGuestTestsDir   = "guest-tests"
	evqModuleDir       = "supervisor"
	evqModulePrefix    = "tbound/supervisor/"
	// evqPinnedGoVersion is the `go version` line of the pinned toolchain (P1 of the provisioning runbook).
	evqPinnedGoVersion = "go version go1.27.1 linux/amd64"
	maxCaptureBytes    = 1 << 20
	maxArchiveFileSize = 256 << 20
)

// evqCaptureArgv is the one accepted invocation. There is no -run, -skip, -short or package filter.
var evqCaptureArgv = []string{"go", "test", "-race", "-count=1", "-json", "./..."}

// evqPinnedInputs are the files of the frozen commit that decide what the EVQ results and the route
// report mean. The capture records their digest as extracted, so a mapping or matrix that is not the
// commit's is refused.
var evqPinnedInputs = []string{"infra/conformance/evq-mapping.json", "infra/conformance/route-matrix.json"}

// evqPinnedEnv is the toolchain environment of the capture, apart from the proxy and the
// architecture, which are recorded.
var evqPinnedEnv = map[string]string{"GOFLAGS": "-mod=readonly", "GOTOOLCHAIN": "local", "GOSUMDB": "off", "GOOS": "linux", "CGO_ENABLED": "1"}

var gitObjectRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// evqCapture is the capture record.
type evqCapture struct {
	Schema            string            `json:"schema"`
	Commit            string            `json:"commit"`
	GitTree           string            `json:"git_tree"`
	ArchiveTreeDigest string            `json:"archive_tree_digest"`
	ArchiveFiles      int               `json:"archive_files"`
	PinnedInputs      map[string]string `json:"pinned_inputs"`
	GoVersion         string            `json:"go_version"`
	GoBinarySHA256    string            `json:"go_binary_sha256"`
	Argv              []string          `json:"argv"`
	WorkDir           string            `json:"work_dir"`
	Env               map[string]string `json:"env"`
	Packages          []string          `json:"packages"`
	ExitStatus        int               `json:"exit_status"`
	TestJSONSHA256    string            `json:"test_json_sha256"`
	// TestBinaries are the `go test -c` binaries of the guest test packages, built from the same tree:
	// package import path to sha256.
	TestBinaries map[string]string `json:"test_binaries"`
}

// evqCaptureOptions are the inputs of one capture. The seams are for tests.
type evqCaptureOptions struct {
	repo, commit, out string
	goBin, gitBin     string
	env               []string // the caller's environment
	goVersion         string   // expected `go version` output
	stdout            io.Writer
}

func (a *app) evqCaptureCmd(args []string) error {
	flags := flag.NewFlagSet("tbound-run evq-capture", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	repo := flags.String("repo", "", "git repository that holds the frozen commit")
	commit := flags.String("commit", "", "the frozen commit, 40 lowercase hex digits")
	out := flags.String("out", "", "capture directory to create")
	goBin := flags.String("go", "go", "the pinned go binary")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 || *repo == "" || *commit == "" || *out == "" {
		return fmt.Errorf("%w: evq-capture takes --repo, --commit and --out (and optional --go)", errUsage)
	}
	if runtime.GOOS != "linux" {
		return errors.New("evq-capture runs the Linux tests of the frozen commit: run it on Linux or WSL")
	}
	return runEVQCapture(a.ctx, evqCaptureOptions{repo: *repo, commit: *commit, out: *out, goBin: *goBin, gitBin: "git",
		env: os.Environ(), goVersion: evqPinnedGoVersion, stdout: a.out})
}

// runEVQCapture performs the capture. It writes nothing into the capture directory on a refusal before
// the test run; a failed test run still leaves a record, because the failure is the evidence.
func runEVQCapture(ctx context.Context, o evqCaptureOptions) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !guestevidence.IsCommit(o.commit) {
		return fmt.Errorf("%w: --commit must be 40 lowercase hex digits, not a branch or an abbreviation", errUsage)
	}
	if !filepath.IsAbs(o.out) {
		return fmt.Errorf("%w: --out must be an absolute path", errUsage)
	}
	if _, statErr := os.Lstat(o.out); statErr == nil {
		return fmt.Errorf("%s exists: remove it deliberately to capture again", o.out)
	}
	goPath, err := exec.LookPath(o.goBin)
	if err != nil {
		return fmt.Errorf("go binary: %w", err)
	}
	if goPath, err = filepath.Abs(goPath); err != nil {
		return err
	}
	if err := os.MkdirAll(o.out, 0o700); err != nil {
		return err
	}
	// A refused or failed capture leaves nothing that could pass for one.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(o.out)
		}
	}()
	work := filepath.Join(o.out, "work")
	defer func() {
		if rmErr := os.RemoveAll(work); rmErr != nil && err == nil {
			err = fmt.Errorf("remove %s: %w", work, rmErr)
		}
	}()
	src := filepath.Join(work, "src")
	for _, d := range []string{src, filepath.Join(work, "tmp"), filepath.Join(work, "gocache")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	rec := evqCapture{Schema: evqCaptureSchema, Commit: o.commit, Argv: slices.Clone(evqCaptureArgv), WorkDir: evqModuleDir,
		PinnedInputs: map[string]string{}, Env: map[string]string{}, TestBinaries: map[string]string{}}

	g := gitRunner{ctx: ctx, bin: o.gitBin, repo: o.repo}
	if rec.GitTree, err = g.verifyCommit(o.commit); err != nil {
		return err
	}
	tree, err := g.listTree(o.commit)
	if err != nil {
		return err
	}
	if err := g.extract(o.commit, tree, src); err != nil {
		return err
	}
	files, treeDigest, err := verifyTree(src, tree)
	if err != nil {
		return err
	}
	rec.ArchiveTreeDigest, rec.ArchiveFiles = treeDigest, len(files)
	for _, p := range evqPinnedInputs {
		d, ok := files[p]
		if !ok {
			return fmt.Errorf("the frozen commit has no %s", p)
		}
		rec.PinnedInputs[p] = d
	}

	tc := toolchain{ctx: ctx, goBin: goPath, dir: filepath.Join(src, evqModuleDir), callerEnv: o.env, work: work}
	if rec.GoVersion, rec.GoBinarySHA256, rec.Env, err = tc.pin(o.goVersion); err != nil {
		return err
	}
	if rec.Packages, err = tc.packages(); err != nil {
		return err
	}

	testJSON, err := os.OpenFile(filepath.Join(o.out, evqCaptureTestJSON), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	stderr, err := os.OpenFile(filepath.Join(o.out, evqCaptureStderr), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		testJSON.Close()
		return err
	}
	h := sha256.New()
	cmd := exec.CommandContext(ctx, goPath, evqCaptureArgv[1:]...)
	cmd.Dir, cmd.Env = tc.dir, tc.env("")
	cmd.Stdout, cmd.Stderr = io.MultiWriter(testJSON, h), stderr
	runErr := cmd.Run()
	closeErr := errors.Join(testJSON.Close(), stderr.Close())
	if closeErr != nil {
		return closeErr
	}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr) && exitErr.ExitCode() > 0:
		rec.ExitStatus = exitErr.ExitCode()
	default:
		return fmt.Errorf("go test did not run to an exit status: %w", runErr)
	}
	rec.TestJSONSHA256 = "sha256:" + hex.EncodeToString(h.Sum(nil))

	// The tests must not have changed the tree they ran in.
	if _, after, err := verifyTree(src, tree); err != nil || after != treeDigest {
		return fmt.Errorf("the tests changed the extracted tree (digest %s, then %s): %v", treeDigest, after, err)
	}
	for _, pkg := range guestevidence.TestPackages() {
		digest, err := tc.buildTestBinary(pkg, filepath.Join(o.out, evqGuestTestsDir))
		if err != nil {
			return err
		}
		rec.TestBinaries[pkg] = digest
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writeNewFile(filepath.Join(o.out, evqCaptureRecord), data); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	fmt.Fprintf(o.stdout, "commit %s\ngit_tree %s\narchive_tree_digest %s\narchive_files %d\ngo %s\nargv %s\npackages %d\nexit_status %d\n",
		rec.Commit, rec.GitTree, rec.ArchiveTreeDigest, rec.ArchiveFiles, rec.GoVersion, strings.Join(rec.Argv, " "), len(rec.Packages), rec.ExitStatus)
	fmt.Fprintf(o.stdout, "test_json_sha256 %s\ncapture_sha256 sha256:%s\n", rec.TestJSONSHA256, hex.EncodeToString(sum[:]))
	for _, pkg := range slices.Sorted(func(yield func(string) bool) {
		for p := range rec.TestBinaries {
			if !yield(p) {
				return
			}
		}
	}) {
		fmt.Fprintf(o.stdout, "guest_test_binary %s %s\n", pkg, rec.TestBinaries[pkg])
	}
	return nil
}

// ---- git ----

type gitRunner struct {
	ctx  context.Context
	bin  string
	repo string
}

// command returns a git command that reads the repository only through its object store, with no
// conversion of line endings and no configuration of the machine.
func (g gitRunner) command(args ...string) *exec.Cmd {
	cmd := exec.CommandContext(g.ctx, g.bin, append([]string{"-c", "core.autocrlf=false", "-c", "core.eol=lf", "-C", g.repo}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") { // GIT_DIR, GIT_INDEX_FILE and the like would redirect the read
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	return cmd
}

func (g gitRunner) output(args ...string) ([]byte, error) {
	cmd := g.command(args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// verifyCommit requires the commit to exist as exactly this object and returns its tree id.
func (g gitRunner) verifyCommit(commit string) (string, error) {
	out, err := g.output("rev-parse", "--verify", "--end-of-options", commit+"^{commit}")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(out)) != commit {
		return "", fmt.Errorf("%s does not name the commit %s", g.repo, commit)
	}
	out, err = g.output("rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if !gitObjectRE.MatchString(tree) {
		return "", fmt.Errorf("git returned %q as the tree id", tree)
	}
	return tree, nil
}

// treeEntry is one file of the commit's tree.
type treeEntry struct {
	mode string // 100644 or 100755
	blob string // git blob id
}

// listTree reads the whole tree of the commit. Only regular files are accepted: a symbolic link or a
// submodule would make the extraction depend on something other than the commit's bytes.
func (g gitRunner) listTree(commit string) (map[string]treeEntry, error) {
	out, err := g.output("ls-tree", "-r", "-z", "--full-tree", "--end-of-options", commit)
	if err != nil {
		return nil, err
	}
	tree := map[string]treeEntry{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(rec), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || !gitObjectRE.MatchString(fields[2]) || (fields[0] != "100644" && fields[0] != "100755") {
			return nil, fmt.Errorf("the tree of %s has an entry that is not a regular file: %q", commit, rec)
		}
		if !fs.ValidPath(name) || name == "." || path.Clean(name) != name {
			return nil, fmt.Errorf("the tree of %s has the path %q", commit, name)
		}
		tree[name] = treeEntry{mode: fields[0], blob: fields[2]}
	}
	if len(tree) == 0 {
		return nil, fmt.Errorf("the tree of %s is empty", commit)
	}
	return tree, nil
}

// extract writes `git archive <commit>` below dir. Names are checked against the tree listing and
// confined to dir; only directories and regular files are written.
func (g gitRunner) extract(commit string, tree map[string]treeEntry, dir string) error {
	cmd := g.command("archive", "--format=tar", "--end-of-options", commit)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	extractErr := extractTar(pipe, tree, commit, dir)
	_, _ = io.Copy(io.Discard, pipe)
	if waitErr := cmd.Wait(); waitErr != nil {
		return fmt.Errorf("git archive %s: %w: %s", commit, waitErr, strings.TrimSpace(stderr.String()))
	}
	return extractErr
}

func extractTar(r io.Reader, tree map[string]treeEntry, commit, dir string) error {
	tr := tar.NewReader(r)
	sawHeader := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("git archive stream: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			if c := hdr.PAXRecords["comment"]; c != commit {
				return fmt.Errorf("git archive names the commit %q, not %s", c, commit)
			}
			sawHeader = true
			continue
		}
		if !sawHeader {
			return errors.New("git archive stream does not start with the commit header")
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if !fs.ValidPath(name) || name == "." || path.Clean(name) != name {
			return fmt.Errorf("git archive entry %q is not a path inside the tree", hdr.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			entry, ok := tree[name]
			if !ok {
				return fmt.Errorf("git archive entry %q is not in the commit's tree", name)
			}
			if hdr.Size < 0 || hdr.Size > maxArchiveFileSize {
				return fmt.Errorf("git archive entry %q has size %d", name, hdr.Size)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			perm := os.FileMode(0o644)
			if entry.mode == "100755" {
				perm = 0o755
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, tr)
			if err := errors.Join(copyErr, f.Chmod(perm), f.Close()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("git archive entry %q has type %q, only directories and regular files are accepted", name, string(hdr.Typeflag))
		}
	}
	if !sawHeader {
		return errors.New("git archive stream has no commit header")
	}
	return nil
}

// verifyTree walks dir and requires it to be exactly the commit's tree: the same set of regular files,
// the git blob id of every file equal to the listed one and the executable bit equal to the listed
// mode. It returns the sha256 of every file by path and the digest of the canonical listing, the archive
// tree digest. Any other file or directory entry is refused.
func verifyTree(dir string, tree map[string]treeEntry) (map[string]string, string, error) {
	digests := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", name)
		}
		entry, ok := tree[name]
		if !ok {
			return fmt.Errorf("%s is not in the commit's tree", name)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if (info.Mode().Perm()&0o111 != 0) != (entry.mode == "100755") {
			return fmt.Errorf("%s has mode %v, the tree says %s", name, info.Mode().Perm(), entry.mode)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		blob := sha1.New()
		fmt.Fprintf(blob, "blob %d\x00", len(data))
		blob.Write(data)
		if hex.EncodeToString(blob.Sum(nil)) != entry.blob {
			return fmt.Errorf("%s does not hash to the blob %s of the commit", name, entry.blob)
		}
		sum := sha256.Sum256(data)
		digests[name] = "sha256:" + hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if len(digests) != len(tree) {
		return nil, "", fmt.Errorf("the extraction has %d files, the commit's tree has %d", len(digests), len(tree))
	}
	h := sha256.New()
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for n := range digests {
			if !yield(n) {
				return
			}
		}
	}) {
		fmt.Fprintf(h, "%s %s %s\n", tree[name].mode, strings.TrimPrefix(digests[name], "sha256:"), name)
	}
	return digests, "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ---- toolchain ----

type toolchain struct {
	ctx       context.Context
	goBin     string
	dir       string // the module directory of the extraction
	callerEnv []string
	work      string
}

func callerValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

// env is the pinned environment: nothing of the caller's Go configuration is inherited except where the
// toolchain looks for its module cache and the proxy, which are recorded.
func (t toolchain) env(cgo string) []string {
	proxy := callerValue(t.callerEnv, "GOPROXY")
	if proxy == "" {
		proxy = "off"
	}
	env := []string{
		"PATH=" + callerValue(t.callerEnv, "PATH"), "HOME=" + callerValue(t.callerEnv, "HOME"),
		"LANG=C", "LC_ALL=C", "TMPDIR=" + filepath.Join(t.work, "tmp"), "GOCACHE=" + filepath.Join(t.work, "gocache"),
		"TBOUND_EVIDENCE_DIR=" + filepath.Join(t.work, "tmp", "evidence"),
		"GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local", "GOSUMDB=off", "GOPROXY=" + proxy, "GOOS=linux", "GOARCH=amd64",
	}
	for _, k := range []string{"GOPATH", "GOMODCACHE"} {
		if v := callerValue(t.callerEnv, k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if cgo != "" {
		env = append(env, "CGO_ENABLED="+cgo)
	}
	return env
}

func (t toolchain) run(env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(t.ctx, t.goBin, args...)
	cmd.Dir, cmd.Env = t.dir, env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// pin requires the pinned toolchain and returns its version line, the digest of its binary and the
// environment record.
func (t toolchain) pin(wantVersion string) (version, binary string, env map[string]string, err error) {
	out, err := t.run(t.env(""), "version")
	if err != nil {
		return "", "", nil, err
	}
	if version = strings.TrimSpace(string(out)); version != wantVersion {
		return "", "", nil, fmt.Errorf("the toolchain is %q, the pinned one is %q", version, wantVersion)
	}
	data, err := os.ReadFile(t.goBin)
	if err != nil {
		return "", "", nil, err
	}
	sum := sha256.Sum256(data)
	binary = "sha256:" + hex.EncodeToString(sum[:])
	out, err = t.run(t.env(""), "env", "-json", "GOFLAGS", "GOTOOLCHAIN", "GOSUMDB", "GOPROXY", "GOOS", "GOARCH", "CGO_ENABLED")
	if err != nil {
		return "", "", nil, err
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		return "", "", nil, fmt.Errorf("go env: %w", err)
	}
	env = map[string]string{}
	for k, v := range got {
		env[k] = v
	}
	for k, want := range evqPinnedEnv {
		if env[k] != want {
			return "", "", nil, fmt.Errorf("go env %s is %q, the capture needs %q (-race needs cgo)", k, env[k], want)
		}
	}
	if env["GOPROXY"] == "" || env["GOARCH"] != "amd64" {
		return "", "", nil, fmt.Errorf("go env GOPROXY=%q GOARCH=%q", env["GOPROXY"], env["GOARCH"])
	}
	return version, binary, env, nil
}

// packages lists the packages `go test ./...` runs, sorted.
func (t toolchain) packages() ([]string, error) {
	out, err := t.run(t.env(""), "list", "./...")
	if err != nil {
		return nil, err
	}
	var pkgs []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			pkgs = append(pkgs, line)
		}
	}
	slices.Sort(pkgs)
	if len(pkgs) == 0 || len(slices.Compact(slices.Clone(pkgs))) != len(pkgs) {
		return nil, errors.New("go list ./... returned no packages or a repeated one")
	}
	return pkgs, nil
}

// buildTestBinary builds the test binary of one package without cgo (it runs on the guest) and
// returns its digest.
func (t toolchain) buildTestBinary(pkg, outDir string) (string, error) {
	rel, ok := strings.CutPrefix(pkg, evqModulePrefix)
	if !ok {
		return "", fmt.Errorf("guest test package %q is not in the module", pkg)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", err
	}
	bin := filepath.Join(outDir, path.Base(rel)+".test")
	if _, err := t.run(t.env("0"), "test", "-c", "-trimpath", "-o", bin, "./"+rel); err != nil {
		return "", err
	}
	digest, err := digestNonEmptyFile(bin)
	if err != nil {
		return "", err
	}
	return digest, nil
}

// ---- verification at evq time ----

// loadEVQCapture reads and verifies a capture directory for the frozen commit. It re-checks every
// property the capture is trusted for: the commit, the exact filter-free invocation, the pinned
// toolchain environment, an exit status that agrees with the test JSON, a test JSON that has the digest
// the record states and one finished entry for every package of the module. A capture that fails any of
// them is not evidence.
func loadEVQCapture(dir, freezeCommit string) (evqCapture, string, *testOutcomes, error) {
	var rec evqCapture
	data, err := readRegularFile(filepath.Join(dir, evqCaptureRecord), maxCaptureBytes)
	if err != nil {
		return rec, "", nil, fmt.Errorf("capture record: %w", err)
	}
	if err := decodeStrict(data, &rec); err != nil {
		return rec, "", nil, fmt.Errorf("capture record: %w", err)
	}
	sum := sha256.Sum256(data)
	recordDigest := "sha256:" + hex.EncodeToString(sum[:])
	if err := rec.check(freezeCommit); err != nil {
		return rec, "", nil, fmt.Errorf("capture record: %w", err)
	}
	outcomes := newTestOutcomes()
	digest, err := parseTestJSONFile(filepath.Join(dir, evqCaptureTestJSON), outcomes)
	if err != nil {
		return rec, "", nil, err
	}
	if digest != rec.TestJSONSHA256 {
		return rec, "", nil, fmt.Errorf("capture: %s hashes to %s, the record states %s", evqCaptureTestJSON, digest, rec.TestJSONSHA256)
	}
	if got := outcomes.packageNames(); !slices.Equal(got, rec.Packages) {
		return rec, "", nil, fmt.Errorf("capture: the test JSON covers %d packages, the module has %d (%s)", len(got), len(rec.Packages), packageDiff(got, rec.Packages))
	}
	for _, p := range rec.Packages {
		if !outcomes.packageFinished(p) {
			return rec, "", nil, fmt.Errorf("capture: package %s did not finish in the test JSON", p)
		}
	}
	if failed := outcomes.anyFailed(); (rec.ExitStatus == 0) == failed {
		return rec, "", nil, fmt.Errorf("capture: exit status %d disagrees with the test JSON (a failure is present: %t)", rec.ExitStatus, failed)
	}
	return rec, recordDigest, outcomes, nil
}

func packageDiff(got, want []string) string {
	var parts []string
	for _, p := range want {
		if !slices.Contains(got, p) {
			parts = append(parts, "missing "+p)
		}
	}
	for _, p := range got {
		if !slices.Contains(want, p) {
			parts = append(parts, "extra "+p)
		}
	}
	if len(parts) > 5 {
		parts = append(parts[:5], "...")
	}
	return strings.Join(parts, ", ")
}

// check verifies the record's own properties against the frozen commit.
func (r evqCapture) check(freezeCommit string) error {
	switch {
	case r.Schema != evqCaptureSchema:
		return fmt.Errorf("schema %q, want %q", r.Schema, evqCaptureSchema)
	case !guestevidence.IsCommit(freezeCommit) || r.Commit != freezeCommit:
		return fmt.Errorf("captured commit %q, run.json freezes %q", r.Commit, freezeCommit)
	case !gitObjectRE.MatchString(r.GitTree) || !guestevidence.IsDigest(r.ArchiveTreeDigest) || r.ArchiveFiles <= 0:
		return errors.New("the archive tree is not identified")
	case r.GoVersion != evqPinnedGoVersion || !guestevidence.IsDigest(r.GoBinarySHA256):
		return fmt.Errorf("toolchain %q, the pinned one is %q", r.GoVersion, evqPinnedGoVersion)
	case r.WorkDir != evqModuleDir:
		return fmt.Errorf("work_dir %q, want %q", r.WorkDir, evqModuleDir)
	case r.ExitStatus != 0 && r.ExitStatus != 1:
		return fmt.Errorf("exit status %d is neither a clean run nor a test failure", r.ExitStatus)
	case !guestevidence.IsDigest(r.TestJSONSHA256):
		return errors.New("the test JSON digest is not stated")
	}
	if !slices.Equal(r.Argv, evqCaptureArgv) {
		return fmt.Errorf("invocation %q is not the full filter-free %q (no -run, -skip, -short or package filter is accepted)", r.Argv, evqCaptureArgv)
	}
	for k, want := range evqPinnedEnv {
		if r.Env[k] != want {
			return fmt.Errorf("environment %s=%q, the pinned toolchain environment has %q", k, r.Env[k], want)
		}
	}
	if r.Env["GOPROXY"] == "" || r.Env["GOARCH"] != "amd64" {
		return errors.New("environment lacks GOPROXY or is not amd64")
	}
	if len(r.Packages) == 0 || !slices.IsSorted(r.Packages) || len(slices.Compact(slices.Clone(r.Packages))) != len(r.Packages) {
		return errors.New("the package list is empty, unsorted or repeated")
	}
	for _, p := range evqPinnedInputs {
		if !guestevidence.IsDigest(r.PinnedInputs[p]) {
			return fmt.Errorf("the record does not digest %s", p)
		}
	}
	for _, pkg := range guestevidence.TestPackages() {
		if !guestevidence.IsDigest(r.TestBinaries[pkg]) {
			return fmt.Errorf("the record has no test binary for %s", pkg)
		}
	}
	return nil
}

// requirePinnedInput refuses a mapping or matrix that is not the frozen commit's. Line endings are
// compared as LF, as the commit stores them.
func (r evqCapture) requirePinnedInput(name string, data []byte) error {
	sum := sha256.Sum256(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != r.PinnedInputs[name] {
		return fmt.Errorf("%s hashes to %s, the frozen commit's copy is %s", name, got, r.PinnedInputs[name])
	}
	return nil
}

// loadFreezeBinding reads the freeze commit and the receipt pins of run.json. It reads only those two
// members and does not validate the rest: the file may not yet name the evq_results this command writes.
// `plan` validates the whole file.
func loadFreezeBinding(path string) (string, string, string, error) {
	data, err := readRegularFile(path, maxConfigBytes)
	if err != nil {
		return "", "", "", fmt.Errorf("config: %w", err)
	}
	var doc struct {
		FreezeCommit string `json:"freeze_commit"`
		Pins         struct {
			ProfileHash        string `json:"profile_hash"`
			FrozenSourceDigest string `json:"frozen_source_digest"`
		} `json:"pins"`
	}
	if err := decodeLenientJSON(data, &doc); err != nil {
		return "", "", "", fmt.Errorf("config: %w", err)
	}
	if !guestevidence.IsCommit(doc.FreezeCommit) {
		return "", "", "", fmt.Errorf("config: freeze_commit %q is not 40 lowercase hex digits", doc.FreezeCommit)
	}
	return doc.FreezeCommit, doc.Pins.ProfileHash, doc.Pins.FrozenSourceDigest, nil
}

func decodeLenientJSON(data []byte, v any) error {
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}
