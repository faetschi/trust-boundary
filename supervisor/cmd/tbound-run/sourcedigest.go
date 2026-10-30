package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"tbound/supervisor/internal/guestevidence"
)

// archiveVerified extracts `git archive` of the commit into dst, requires the extraction to be the commit's tree
// (the same regular files, every git blob id and executable bit equal) and returns the commit's tree id, the
// content digest of every file and the digest of the whole listing. It is the one definition of the digest of a
// frozen source: evq-capture records it as archive_tree_digest, `source-digest` prints it, and the host profile
// pins it as SourceProvenanceDigest (run.json pins.frozen_source_digest).
func archiveVerified(g gitRunner, commit, dst string) (gitTree string, tree map[string]treeEntry, files map[string]string, digest string, err error) {
	if gitTree, err = g.verifyCommit(commit); err != nil {
		return "", nil, nil, "", err
	}
	if tree, err = g.listTree(commit); err != nil {
		return "", nil, nil, "", err
	}
	if err = g.extract(commit, tree, dst); err != nil {
		return "", nil, nil, "", err
	}
	files, digest, err = verifyTree(dst, tree)
	return gitTree, tree, files, digest, err
}

func (a *app) sourceDigestCmd(args []string) error {
	flags := flag.NewFlagSet("tbound-run source-digest", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	repo := flags.String("repo", "", "git repository that holds the frozen commit")
	commit := flags.String("commit", "", "the frozen commit, 40 lowercase hex digits")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 || *repo == "" || *commit == "" {
		return fmt.Errorf("%w: source-digest takes --repo and --commit", errUsage)
	}
	return runSourceDigest(a.ctx, *repo, *commit, "git", a.out)
}

// runSourceDigest prints the digest of the frozen source of commit. Nothing is printed on a refusal.
func runSourceDigest(ctx context.Context, repo, commit, gitBin string, stdout io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !guestevidence.IsCommit(commit) {
		return fmt.Errorf("%w: --commit must be 40 lowercase hex digits, not a branch or an abbreviation", errUsage)
	}
	dir, err := os.MkdirTemp("", "tbound-source-digest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o700); err != nil {
		return err
	}
	gitTree, _, files, digest, err := archiveVerified(gitRunner{ctx: ctx, bin: gitBin, repo: repo}, commit, src)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("the commit holds no files")
	}
	_, err = fmt.Fprintf(stdout, "commit %s\ngit_tree %s\narchive_files %d\nsource_digest %s\n", commit, gitTree, len(files), digest)
	return err
}

// requireFrozenSource refuses a run.json whose frozen_source_digest is not the archive tree digest of the commit
// the capture ran the tests on: the pilot's source pin, the host profile's SourceProvenanceDigest, then binds F.
func requireFrozenSource(pin string, rec evqCapture) error {
	if pin == "" || pin != rec.ArchiveTreeDigest {
		return fmt.Errorf("run.json pins.frozen_source_digest %q is not the archive tree digest %s of the captured commit: take it from `tbound-run source-digest --repo <repo> --commit <freeze commit>`", pin, rec.ArchiveTreeDigest)
	}
	return nil
}

// guestExpect is what run.json pins for the guest artifacts of the runs with a config of their own: the frozen source
// (requireFrozenSource ties it to the capture), the base cell template, the image, the admin key, the Pi runtime bundle
// of their signed profiles and the runner: the service executable their signed registrations name, which the owner
// pins from the reproducible build of the frozen commit. The producers verify each run's signed registration under the
// admin key and compare its runner with the pin, so the artifact states the build the service ran; the validators
// require the artifact to carry every one of these values.
func guestExpect(commit, guestBinary string, b guestevidence.RunBinding) guestevidence.Expect {
	return guestevidence.Expect{Commit: commit, BinarySHA256: guestBinary,
		FrozenSourceDigest: b.FrozenSourceDigest, BaseTemplateDigest: b.BaseTemplateDigest, ImageDigest: b.ImageDigest, AdminPubkeyDigest: b.AdminPubkeyDigest,
		RuntimeBundleDigest: b.RuntimeBundleDigest, RunnerDigest: b.RunnerDigest}
}
