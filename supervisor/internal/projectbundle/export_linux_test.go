//go:build linux

package projectbundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed: export apply tests need a real Git worktree")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

type fixture struct {
	worktree, managed, state string
	baseline                 Manifest
	exclude                  []string
}

// newFixture commits a small project to a Git worktree, then bundles it (after prepare runs) and
// imports it as the managed copy.
func newFixture(t *testing.T, prepare func(worktree string), exclude ...string) fixture {
	t.Helper()
	requireGit(t)
	f := fixture{worktree: privateDir(t), managed: filepath.Join(privateDir(t), "managed"), state: privateDir(t), exclude: exclude}
	put(t, f.worktree, "a.txt", "alpha\n", 0o644)
	put(t, f.worktree, "keep.txt", "keep\n", 0o644)
	put(t, f.worktree, "old.txt", "old\n", 0o644)
	put(t, f.worktree, "dir/b.sh", "#!/bin/sh\n", 0o644)
	put(t, f.worktree, "tool.sh", "#!/bin/sh\n", 0o755)
	put(t, f.worktree, "del/c.txt", "c\n", 0o644)
	git(t, f.worktree, "init", "-q")
	git(t, f.worktree, "add", "-A")
	git(t, f.worktree, "commit", "-q", "-m", "init")
	if prepare != nil {
		prepare(f.worktree)
	}
	var stream bytes.Buffer
	if _, err := Build(f.worktree, BuildOptions{Exclude: exclude, Limits: DefaultLimits()}, &stream); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := Import(&stream, f.managed, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	f.baseline = manifest
	return f
}

func (f fixture) export(t *testing.T) ([]byte, ExportHeader) {
	t.Helper()
	var out bytes.Buffer
	header, _, err := BuildExport(f.baseline, f.managed, DefaultLimits(), &out)
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), header
}

func (f fixture) apply(bundle io.ReadSeeker) ([]string, error) {
	return ApplyExport(bundle, f.worktree, ApplyOptions{Mode: DirectMode, WriterExclusionHeld: true, StateDir: f.state, Exclude: f.exclude, Limits: DefaultLimits()})
}

// edit changes the managed copy the way a published agent turn would.
func (f fixture) edit(t *testing.T, mutate func(managed string)) {
	t.Helper()
	mutate(f.managed)
}

func fullEdit(t *testing.T) func(string) {
	return func(m string) {
		put(t, m, "a.txt", "alpha changed\n", 0o600)
		put(t, m, "new/n.txt", "new file\n", 0o600)
		put(t, m, "new/sub/", "", 0)
		put(t, m, "run.sh", "#!/bin/sh\necho new\n", 0o700)
		must(t, os.Remove(filepath.Join(m, "old.txt")))
		must(t, os.RemoveAll(filepath.Join(m, "del")))
		must(t, os.Chmod(filepath.Join(m, "dir/b.sh"), 0o700))
		must(t, os.Chmod(filepath.Join(m, "tool.sh"), 0o600))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func treeDigest(t *testing.T, dir string, exclude ...string) string {
	t.Helper()
	m, _, err := scanTree(dir, scanOptions{Exclude: exclude, Limits: DefaultLimits()})
	must(t, err)
	d, err := m.Digest()
	must(t, err)
	return d
}

func TestExportApplyRoundTrip(t *testing.T) {
	f := newFixture(t, func(w string) { put(t, w, "scratch.txt", "untracked, elsewhere\n", 0o644) })
	f.edit(t, fullEdit(t))
	bundle, header := f.export(t)

	got := map[string]ChangeOp{}
	for _, c := range header.Changes {
		got[c.Path] = c.Op
	}
	want := map[string]ChangeOp{"a.txt": OpModify, "new": OpCreate, "new/n.txt": OpCreate, "new/sub": OpCreate, "run.sh": OpCreate,
		"old.txt": OpDelete, "del": OpDelete, "del/c.txt": OpDelete, "dir/b.sh": OpChmod, "tool.sh": OpChmod}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	if want := treeDigest(t, f.managed); header.TargetDigest != want {
		t.Fatalf("target digest %s, managed tree %s", header.TargetDigest, want)
	}

	changed, err := f.apply(bytes.NewReader(bundle))
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != len(want) {
		t.Fatalf("changed %v", changed)
	}
	if got := treeDigest(t, f.worktree); got != header.TargetDigest {
		t.Fatalf("worktree digest %s, target %s", got, header.TargetDigest)
	}
	for rel, mode := range map[string]os.FileMode{"a.txt": 0o644, "dir/b.sh": 0o755, "tool.sh": 0o644, "run.sh": 0o755, "new/n.txt": 0o644} {
		if info, err := os.Stat(filepath.Join(f.worktree, rel)); err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want mode %v", rel, info, err, mode)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(f.worktree, "scratch.txt")); string(got) != "untracked, elsewhere\n" {
		t.Errorf("an untracked file outside the change set was touched: %q", got)
	}
	if got := listDir(t, f.state); len(got) != 0 {
		t.Errorf("recovery record left after a verified apply: %v", got)
	}
	for _, rel := range []string{"old.txt", "del"} {
		if _, err := os.Lstat(filepath.Join(f.worktree, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists (%v)", rel, err)
		}
	}
	for _, name := range listDir(t, f.worktree) {
		if strings.HasPrefix(name, ".tbound-apply-") {
			t.Errorf("temporary file %s left behind", name)
		}
	}
}

func TestExportBuildRefusals(t *testing.T) {
	t.Run("no changes", func(t *testing.T) {
		f := newFixture(t, nil)
		_, _, err := BuildExport(f.baseline, f.managed, DefaultLimits(), io.Discard)
		if !errors.Is(err, ErrNoChanges) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("type change", func(t *testing.T) {
		f := newFixture(t, nil)
		f.edit(t, func(m string) {
			must(t, os.Remove(filepath.Join(m, "old.txt")))
			put(t, m, "old.txt/inner", "x", 0o644)
		})
		var out bytes.Buffer
		if _, _, err := BuildExport(f.baseline, f.managed, DefaultLimits(), &out); err == nil || !strings.Contains(err.Error(), "type") || out.Len() != 0 {
			t.Fatalf("err = %v, wrote %d bytes", err, out.Len())
		}
	})
	t.Run("symlink in managed project", func(t *testing.T) {
		f := newFixture(t, nil)
		f.edit(t, func(m string) { must(t, os.Symlink("a.txt", filepath.Join(m, "link"))) })
		var refused *RefusedError
		if _, _, err := BuildExport(f.baseline, f.managed, DefaultLimits(), io.Discard); !errors.As(err, &refused) {
			t.Fatalf("err = %v", err)
		}
	})
}

// snapshot describes every path outside .git: type, permissions, content and symlink target.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	must(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.Name() == ".git" {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %v", path, info.Mode())
		switch {
		case info.Mode().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, " %x", content)
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			b.WriteString(" -> " + target)
		}
		b.WriteString("\n")
		return nil
	}))
	return b.String()
}

// refusedApply asserts the apply failed before any write: no ApplyError, an unchanged worktree and no record.
func refusedApply(t *testing.T, f fixture, bundle io.ReadSeeker, want error) error {
	t.Helper()
	before := snapshot(t, f.worktree)
	_, err := f.apply(bundle)
	var applyErr *ApplyError
	if err == nil || errors.As(err, &applyErr) || (want != nil && !errors.Is(err, want)) {
		t.Fatalf("apply error = %v, want a refusal matching %v", err, want)
	}
	if after := snapshot(t, f.worktree); after != before {
		t.Fatal("a refused apply changed the worktree")
	}
	if got := listDir(t, f.state); len(got) != 0 {
		t.Fatalf("a refused apply left %v in the state directory", got)
	}
	return err
}

func TestApplyRefusals(t *testing.T) {
	t.Run("modified tracked file", func(t *testing.T) {
		f := newFixture(t, nil)
		f.edit(t, fullEdit(t))
		bundle, _ := f.export(t)
		put(t, f.worktree, "keep.txt", "dirty\n", 0o644)
		refusedApply(t, f, bytes.NewReader(bundle), ErrDirtyWorktree)
	})
	t.Run("conflicting committed change", func(t *testing.T) {
		f := newFixture(t, nil)
		f.edit(t, fullEdit(t))
		bundle, _ := f.export(t)
		put(t, f.worktree, "a.txt", "somebody else\n", 0o644)
		git(t, f.worktree, "commit", "-q", "-am", "conflict")
		refusedApply(t, f, bytes.NewReader(bundle), ErrBaselineMismatch)
	})
	t.Run("untracked file at a create target", func(t *testing.T) {
		f := newFixture(t, nil)
		f.edit(t, fullEdit(t))
		bundle, _ := f.export(t)
		put(t, f.worktree, "new/n.txt", "mine\n", 0o644)
		err := refusedApply(t, f, bytes.NewReader(bundle), ErrDirtyWorktree)
		if !strings.Contains(err.Error(), "untracked") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("untracked file at a modify target", func(t *testing.T) {
		f := newFixture(t, func(w string) { put(t, w, "u.txt", "untracked\n", 0o644) })
		f.edit(t, func(m string) { put(t, m, "u.txt", "changed\n", 0o600) })
		bundle, _ := f.export(t)
		err := refusedApply(t, f, bytes.NewReader(bundle), ErrDirtyWorktree)
		if !strings.Contains(err.Error(), "untracked") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("ignored file at a modify target", func(t *testing.T) {
		f := newFixture(t, func(w string) {
			put(t, w, ".gitignore", "ignored.txt\n", 0o644)
			git(t, w, "add", ".gitignore")
			git(t, w, "commit", "-q", "-m", "ignore")
			put(t, w, "ignored.txt", "not in Git\n", 0o644)
		})
		f.edit(t, func(m string) { put(t, m, "ignored.txt", "changed\n", 0o600) })
		bundle, _ := f.export(t)
		err := refusedApply(t, f, bytes.NewReader(bundle), ErrDirtyWorktree)
		if !strings.Contains(err.Error(), "not tracked") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("symlink in the worktree", func(t *testing.T) {
		f := newFixture(t, nil)
		f.edit(t, fullEdit(t))
		bundle, _ := f.export(t)
		must(t, os.Symlink("a.txt", filepath.Join(f.worktree, "link")))
		var refused *RefusedError
		if err := refusedApply(t, f, bytes.NewReader(bundle), nil); !errors.As(err, &refused) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("deleted directory holds an excluded path", func(t *testing.T) {
		f := newFixture(t, func(w string) {
			put(t, w, "del/private/key", "k\n", 0o644)
			git(t, w, "add", "-A")
			git(t, w, "commit", "-q", "-m", "private")
		}, "del/private")
		f.edit(t, fullEdit(t))
		bundle, _ := f.export(t)
		err := refusedApply(t, f, bytes.NewReader(bundle), nil)
		if !strings.Contains(err.Error(), "does not delete") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("state directory inside the worktree", func(t *testing.T) {
		f := newFixture(t, nil)
		f.edit(t, fullEdit(t))
		bundle, _ := f.export(t)
		f.state = filepath.Join(f.worktree, "dir")
		before := treeDigest(t, f.worktree)
		if _, err := f.apply(bytes.NewReader(bundle)); err == nil || treeDigest(t, f.worktree) != before {
			t.Fatalf("err = %v", err)
		}
	})
}

// reframe rewrites an export's header and keeps its content.
func reframe(t *testing.T, raw []byte, mutate func(*ExportHeader)) []byte {
	t.Helper()
	data, err := readFramed(bytes.NewReader(raw), ExportFormat)
	must(t, err)
	var header ExportHeader
	must(t, decodeStrict(data, &header))
	mutate(&header)
	rewritten, err := header.marshal()
	must(t, err)
	var out bytes.Buffer
	must(t, writeFramed(&out, ExportFormat, rewritten))
	out.Write(raw[len(ExportFormat)+5+len(data):])
	return out.Bytes()
}

func TestApplyRefusesBadBundles(t *testing.T) {
	f := newFixture(t, nil, "secret")
	f.edit(t, fullEdit(t))
	raw, _ := f.export(t)

	t.Run("tampered content", func(t *testing.T) {
		bad := append([]byte(nil), raw...)
		bad[len(bad)-1] ^= 1
		refusedApply(t, f, bytes.NewReader(bad), ErrInvalidBundle)
	})
	t.Run("truncated", func(t *testing.T) {
		refusedApply(t, f, bytes.NewReader(raw[:len(raw)-1]), ErrInvalidBundle)
	})
	t.Run("trailing data", func(t *testing.T) {
		refusedApply(t, f, bytes.NewReader(append(append([]byte(nil), raw...), 'x')), ErrInvalidBundle)
	})
	t.Run("target digest that the changes do not produce", func(t *testing.T) {
		bad := reframe(t, raw, func(h *ExportHeader) { h.TargetDigest = h.BaselineDigest })
		refusedApply(t, f, bytes.NewReader(bad), ErrInvalidBundle)
	})
	t.Run("path escaping the worktree", func(t *testing.T) {
		bad := reframe(t, raw, func(h *ExportHeader) { h.Changes[0].Path = "../escape" })
		refusedApply(t, f, bytes.NewReader(bad), ErrInvalidBundle)
	})
	t.Run("git administration", func(t *testing.T) {
		bad := reframe(t, raw, func(h *ExportHeader) { h.Changes[0].Path = ".git/hooks/pre-commit" })
		refusedApply(t, f, bytes.NewReader(bad), ErrInvalidBundle)
	})
	t.Run("excluded path", func(t *testing.T) {
		bad := reframe(t, raw, func(h *ExportHeader) {
			h.Changes = append(h.Changes, Change{Path: "secret", Op: OpCreate, Type: TypeDir})
			sort.Slice(h.Changes, func(i, j int) bool { return h.Changes[i].Path < h.Changes[j].Path })
		})
		refusedApply(t, f, bytes.NewReader(bad), nil)
	})
	t.Run("unknown operation", func(t *testing.T) {
		bad := reframe(t, raw, func(h *ExportHeader) { h.Changes[0].Op = "symlink" })
		refusedApply(t, f, bytes.NewReader(bad), ErrInvalidBundle)
	})
	t.Run("export format is not a bundle", func(t *testing.T) {
		refusedApply(t, f, bytes.NewReader(craft(t, []Entry{fileEntry("f", "x")}, "x")), ErrInvalidBundle)
	})
	t.Run("valid bundle still applies afterwards", func(t *testing.T) {
		if _, err := f.apply(bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
	})
}

// seekHook runs fn on the nth Seek, which ApplyExport issues once to validate and once before each content copy.
type seekHook struct {
	*bytes.Reader
	seeks, at int
	fn        func()
}

func (s *seekHook) Seek(offset int64, whence int) (int64, error) {
	if s.seeks++; s.seeks == s.at {
		s.fn()
	}
	return s.Reader.Seek(offset, whence)
}

func TestApplyFailureAfterFirstWriteKeepsRecovery(t *testing.T) {
	f := newFixture(t, nil)
	f.edit(t, func(m string) {
		put(t, m, "a.txt", "alpha changed\n", 0o600)
		put(t, m, "keep.txt", "keep changed\n", 0o600)
	})
	raw, header := f.export(t)
	baselineA := f.baseline.Entries[0]
	if baselineA.Path != "a.txt" {
		t.Fatalf("fixture order changed: %v", baselineA)
	}

	// Seek 1 validates, seek 2 precedes a.txt, seek 3 precedes keep.txt. The concurrent write has the same size, so only the content digest can catch it.
	hook := &seekHook{Reader: bytes.NewReader(raw), at: 3, fn: func() {
		must(t, os.WriteFile(filepath.Join(f.worktree, "keep.txt"), []byte("kEEp\n"), 0o644))
	}}
	changed, err := f.apply(hook)
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) || changed != nil {
		t.Fatalf("err = %v, changed = %v, want *ApplyError", err, changed)
	}
	if applyErr.Failed != "keep.txt" || !reflect.DeepEqual(applyErr.Changed, []string{"a.txt"}) {
		t.Fatalf("report = failed %q changed %v", applyErr.Failed, applyErr.Changed)
	}
	if got, _ := os.ReadFile(filepath.Join(f.worktree, "a.txt")); string(got) != "alpha changed\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(f.worktree, "keep.txt")); string(got) != "kEEp\n" {
		t.Errorf("the concurrent write to keep.txt was overwritten: %q", got)
	}
	for _, name := range listDir(t, f.worktree) {
		if strings.HasPrefix(name, ".tbound-apply-") {
			t.Errorf("temporary file %s left behind", name)
		}
	}

	raw2, err := os.ReadFile(applyErr.Record)
	must(t, err)
	var record recoveryRecord
	must(t, json.Unmarshal(raw2, &record))
	if record.BaselineDigest != header.BaselineDigest || record.TargetDigest != header.TargetDigest || len(record.Paths) != 2 {
		t.Fatalf("record = %+v", record)
	}
	if p := record.Paths[0]; p.Path != "a.txt" || p.Op != OpModify || p.Before == nil || *p.Before != baselineA {
		t.Fatalf("record path = %+v, want the pre-apply state of a.txt", p)
	}
	if _, err := f.apply(bytes.NewReader(raw)); !errors.Is(err, ErrRecoveryRecordExists) {
		t.Fatalf("second apply error = %v, want ErrRecoveryRecordExists", err)
	}
}

func TestApplyRecheckBeforeFirstWriteLeavesNothing(t *testing.T) {
	f := newFixture(t, nil)
	f.edit(t, func(m string) { put(t, m, "a.txt", "alpha changed\n", 0o600) })
	raw, _ := f.export(t)
	hook := &seekHook{Reader: bytes.NewReader(raw), at: 2, fn: func() {
		must(t, os.WriteFile(filepath.Join(f.worktree, "a.txt"), []byte("alph0\n"), 0o644))
	}}
	_, err := f.apply(hook)
	var applyErr *ApplyError
	if err == nil || errors.As(err, &applyErr) {
		t.Fatalf("err = %v, want a plain refusal: nothing was written", err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.worktree, "a.txt")); string(got) != "alph0\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got := listDir(t, f.state); len(got) != 0 {
		t.Errorf("record left although nothing was written: %v", got)
	}
}
