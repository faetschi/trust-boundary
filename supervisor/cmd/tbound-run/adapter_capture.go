package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"tbound/supervisor/internal/guestevidence"
)

// The adapter tests are TypeScript. `tbound-run evq-adapter-capture` runs them from a clean `git archive`
// of the frozen commit, as `evq-capture` does for the Go tests: an offline `npm ci` from the lockfile
// and the cache, then `npm run check` (typecheck and every adapter test file, no filter), under a fixed
// environment. The artifact binds the commit's tree, adapter/, the lockfile, the Node runtime, the
// installed node_modules, the exact invocation, the exit status and the output (re-parsed by the
// validator) together. It is written only if it passes the validator `tbound-run evq` applies.
const adapterDir = "adapter"

type adapterCaptureOptions struct {
	repo, commit, out string
	nodeBin, npmBin   string
	gitBin            string
	npmCache          string
	// piCLIBin, when set, requires the official Pi CLI integration test and names the executable.
	piCLIBin string
	stdout   io.Writer
}

func (a *app) evqAdapterCaptureCmd(args []string) error {
	flags := flag.NewFlagSet("tbound-run evq-adapter-capture", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	var o adapterCaptureOptions
	flags.StringVar(&o.repo, "repo", "", "git repository that holds the frozen commit")
	flags.StringVar(&o.commit, "commit", "", "the frozen commit, 40 lowercase hex digits")
	flags.StringVar(&o.out, "out", "", "adapter-tests artifact file to create (an absolute path)")
	flags.StringVar(&o.nodeBin, "node", "", "the Node executable that runs the tests (absolute path)")
	flags.StringVar(&o.npmBin, "npm", "", "the npm executable that belongs to --node (absolute path)")
	flags.StringVar(&o.npmCache, "npm-cache", "", "prepared npm cache directory (the install is offline)")
	flags.StringVar(&o.piCLIBin, "pi-cli-bin", "", "optional official pi executable: requires the Pi CLI integration test (Linux, Node v24.21.0)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 || o.repo == "" || o.commit == "" || o.out == "" || o.nodeBin == "" || o.npmBin == "" || o.npmCache == "" {
		return fmt.Errorf("%w: evq-adapter-capture takes --repo, --commit, --out, --node, --npm and --npm-cache (and optional --pi-cli-bin)", errUsage)
	}
	if runtime.GOOS != "linux" {
		return errors.New("evq-adapter-capture runs the adapter tests under a POSIX environment: run it on Linux or WSL")
	}
	o.gitBin, o.stdout = "git", a.out
	return runAdapterCapture(a.ctx, o)
}

func runAdapterCapture(ctx context.Context, o adapterCaptureOptions) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !guestevidence.IsCommit(o.commit) {
		return fmt.Errorf("%w: --commit must be 40 lowercase hex digits, not a branch or an abbreviation", errUsage)
	}
	for name, p := range map[string]string{"--out": o.out, "--node": o.nodeBin, "--npm": o.npmBin, "--npm-cache": o.npmCache} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%w: %s must be an absolute path", errUsage, name)
		}
	}
	if _, statErr := os.Lstat(o.out); statErr == nil {
		return fmt.Errorf("%s exists: remove it deliberately to capture again", o.out)
	}
	if info, statErr := os.Stat(o.npmCache); statErr != nil || !info.IsDir() {
		return fmt.Errorf("npm cache %s is not a directory", o.npmCache)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	selfDigest, err := digestNonEmptyFile(self)
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "tbound-adapter-capture-")
	if err != nil {
		return err
	}
	defer func() {
		if rmErr := os.RemoveAll(work); rmErr != nil && err == nil {
			err = fmt.Errorf("remove %s: %w", work, rmErr)
		}
	}()
	src := filepath.Join(work, "src")
	for _, d := range []string{src, filepath.Join(work, "tmp"), filepath.Join(work, "home"), filepath.Join(work, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}

	rec := guestevidence.AdapterRun{Argv: slices.Clone(guestevidence.AdapterCheckArgv)}
	rec.Npm.InstallArgv = slices.Clone(guestevidence.AdapterInstallArgv)
	g := gitRunner{ctx: ctx, bin: o.gitBin, repo: o.repo}
	if rec.CommitTree, err = g.verifyCommit(o.commit); err != nil {
		return err
	}
	tree, err := g.listTree(o.commit)
	if err != nil {
		return err
	}
	out, err := g.output("rev-parse", "--verify", "--end-of-options", o.commit+":"+adapterDir)
	if err != nil {
		return err
	}
	if rec.AdapterTree = strings.TrimSpace(string(out)); !gitObjectRE.MatchString(rec.AdapterTree) {
		return fmt.Errorf("git returned %q as the tree id of %s", rec.AdapterTree, adapterDir)
	}
	if err := g.extract(o.commit, tree, src); err != nil {
		return err
	}
	files, treeDigest, err := verifyTree(src, tree)
	if err != nil {
		return err
	}
	prefix := adapterDir + "/"
	rec.AdapterTreeDigest = treeListingDigest(files, tree, prefix)
	for name := range files {
		if strings.HasPrefix(name, prefix) {
			rec.AdapterFiles++
		}
	}
	if rec.PackageJSONSHA256 = files[prefix+"package.json"]; rec.PackageJSONSHA256 == "" {
		return errors.New("the frozen commit has no adapter/package.json")
	}
	if rec.PackageLockSHA256 = files[prefix+"package-lock.json"]; rec.PackageLockSHA256 == "" {
		return errors.New("the frozen commit has no adapter/package-lock.json: there is nothing to install offline from")
	}

	run := adapterRunner{ctx: ctx, dir: filepath.Join(src, adapterDir), nodeBin: o.nodeBin, npmBin: o.npmBin}
	run.env = []string{
		"PATH=" + filepath.Dir(o.nodeBin) + ":/usr/bin:/bin", "HOME=" + filepath.Join(work, "home"), "TMPDIR=" + filepath.Join(work, "tmp"),
		"LANG=C", "LC_ALL=C", "NO_COLOR=1", "npm_config_cache=" + o.npmCache, "npm_config_logs_dir=" + filepath.Join(work, "logs"),
		"npm_config_update_notifier=false", "npm_config_audit=false", "npm_config_fund=false",
	}
	if o.piCLIBin != "" {
		run.env = append(run.env, "TBOUND_REQUIRE_PI_CLI_INTEGRATION=1", "TBOUND_PI_CLI_BIN="+o.piCLIBin)
	}
	if rec.Node.Version, rec.Node.Platform, rec.Node.Arch, err = run.nodeFacts(); err != nil {
		return err
	}
	if rec.Node.SHA256, err = digestNonEmptyFile(o.nodeBin); err != nil {
		return err
	}
	if rec.Npm.Version, err = run.npmVersion(); err != nil {
		return err
	}

	_, installExit, err := run.npm(guestevidence.AdapterInstallArgv[1:])
	if err != nil {
		return err
	}
	if rec.Npm.InstallExit = installExit; installExit != 0 {
		return fmt.Errorf("the offline install exited %d: the cache does not hold the lockfile's packages", installExit)
	}
	if rec.Npm.NodeModulesDigest, rec.Npm.NodeModulesFiles, err = digestNodeModules(filepath.Join(run.dir, "node_modules")); err != nil {
		return err
	}

	output, exit, err := run.npm(guestevidence.AdapterCheckArgv[1:])
	if err != nil {
		return err
	}
	if len(output) > guestevidence.MaxAdapterOutput {
		return fmt.Errorf("the check printed %d bytes, the artifact keeps at most %d", len(output), guestevidence.MaxAdapterOutput)
	}
	rec.ExitCode, rec.Output = exit, string(output)
	sum := sha256.Sum256(output)
	rec.OutputSHA256 = "sha256:" + hex.EncodeToString(sum[:])
	if rec.Results, _, _, err = guestevidence.ParseAdapterOutput(rec.Output); err != nil {
		return fmt.Errorf("the check output: %w", err)
	}
	rec.Env = map[string]string{}
	for _, kv := range run.env {
		k, v, _ := strings.Cut(kv, "=")
		rec.Env[k] = v
	}

	// The check must not have changed the tree it ran in. node_modules is the installed state, digested above.
	if err := os.RemoveAll(filepath.Join(run.dir, "node_modules")); err != nil {
		return err
	}
	if _, after, err := verifyTree(src, tree); err != nil || after != treeDigest {
		return fmt.Errorf("the check changed the extracted tree (digest %s, then %s): %v", treeDigest, after, err)
	}

	data, err := guestevidence.Encode(guestevidence.KindAdapterTests, o.commit, selfDigest, rec)
	if err != nil {
		return err
	}
	expect := guestevidence.Expect{Commit: o.commit, CommitTree: rec.CommitTree}
	if err := guestevidence.Validate(guestevidence.KindAdapterTests, data, expect); err != nil {
		return fmt.Errorf("the capture is not evidence, nothing is written: %w", err)
	}
	if err := writeNewFile(o.out, data); err != nil {
		return err
	}
	file := sha256.Sum256(data)
	fmt.Fprintf(o.stdout, "commit %s\ncommit_tree %s\nadapter_tree %s\nadapter_tree_digest %s\nadapter_files %d\npackage_lock %s\n",
		o.commit, rec.CommitTree, rec.AdapterTree, rec.AdapterTreeDigest, rec.AdapterFiles, rec.PackageLockSHA256)
	fmt.Fprintf(o.stdout, "node %s %s %s/%s\nnpm %s\nnode_modules %s (%d files)\nexit_status %d\ntop_level_results %d\noutput_sha256 %s\nartifact_sha256 sha256:%s\n",
		rec.Node.Version, rec.Node.SHA256, rec.Node.Platform, rec.Node.Arch, rec.Npm.Version, rec.Npm.NodeModulesDigest, rec.Npm.NodeModulesFiles,
		rec.ExitCode, len(rec.Results), rec.OutputSHA256, hex.EncodeToString(file[:]))
	return nil
}

type adapterRunner struct {
	ctx             context.Context
	dir             string
	nodeBin, npmBin string
	env             []string
}

// npm runs npm with args in the adapter directory and returns its combined output and exit status.
func (r adapterRunner) npm(args []string) ([]byte, int, error) {
	cmd := exec.CommandContext(r.ctx, r.npmBin, args...)
	cmd.Dir, cmd.Env = r.dir, r.env
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return buf.Bytes(), 0, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() > 0:
		return buf.Bytes(), exitErr.ExitCode(), nil
	default:
		return nil, 0, fmt.Errorf("npm %s did not run to an exit status: %w", strings.Join(args, " "), err)
	}
}

func (r adapterRunner) npmVersion() (string, error) {
	out, code, err := r.npm([]string{"--version"})
	if err != nil || code != 0 {
		return "", fmt.Errorf("npm --version: exit %d: %v", code, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (r adapterRunner) nodeFacts() (version, platform, arch string, err error) {
	cmd := exec.CommandContext(r.ctx, r.nodeBin, "-p", "process.version+' '+process.platform+' '+process.arch")
	cmd.Dir, cmd.Env = r.dir, r.env
	out, err := cmd.Output()
	if err != nil {
		return "", "", "", fmt.Errorf("node: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 3 {
		return "", "", "", fmt.Errorf("node printed %q", out)
	}
	return fields[0], fields[1], fields[2], nil
}

// digestNodeModules digests the installed tree: every regular file (executable bit and sha256) and
// every symbolic link (its target), by path. Any other entry is refused.
func digestNodeModules(dir string) (string, int, error) {
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			lines = append(lines, fmt.Sprintf("link %s %s", filepath.ToSlash(target), name))
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			mode := "file"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "exec"
			}
			sum := sha256.Sum256(data)
			lines = append(lines, fmt.Sprintf("%s %s %s", mode, hex.EncodeToString(sum[:]), name))
		default:
			return fmt.Errorf("node_modules/%s is neither a regular file nor a symbolic link", name)
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	if len(lines) == 0 {
		return "", 0, errors.New("the install left an empty node_modules")
	}
	slices.Sort(lines)
	h := sha256.New()
	for _, l := range lines {
		fmt.Fprintln(h, l)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), len(lines), nil
}
