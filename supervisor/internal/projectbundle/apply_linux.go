//go:build linux

package projectbundle

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	recoveryFormat     = "tbound-apply-recovery/v1"
	recoveryRecordName = "tbound-apply-recovery.json"
)

var (
	// ErrDirtyWorktree marks a worktree with a modified tracked file or an untracked file at a target path.
	ErrDirtyWorktree = errors.New("projectbundle: the worktree is not clean")
	// ErrBaselineMismatch marks a worktree whose manifest is not the bundle's baseline.
	ErrBaselineMismatch = errors.New("projectbundle: the worktree does not match the export's baseline")
	// ErrRecoveryRecordExists marks an unresolved record of an earlier failed apply in the state directory.
	ErrRecoveryRecordExists = errors.New("projectbundle: an unresolved recovery record exists")
)

// ApplyOptions configure ApplyExport.
type ApplyOptions struct {
	// StateDir is an existing directory outside the worktree that holds the recovery record.
	StateDir string
	// Exclude is the exclude list the baseline was built with. The worktree manifest ignores these
	// paths, and no change may touch them.
	Exclude []string
	Limits  Limits
}

// ApplyError reports a failure after the first write. Changed lists every path the apply changed or
// may have changed, in order. Failed is the path being applied ("" for the final verification).
// Record is the recovery record, which is left in place.
type ApplyError struct {
	Failed  string
	Changed []string
	Record  string
	Err     error
}

func (e *ApplyError) Error() string {
	failed := e.Failed
	if failed == "" {
		failed = "final verification"
	}
	return fmt.Sprintf("projectbundle: apply stopped at %s after changing %d path(s) [%s]: %v; recovery record %s",
		failed, len(e.Changed), strings.Join(e.Changed, ", "), e.Err, e.Record)
}

func (e *ApplyError) Unwrap() error { return e.Err }

type recoveryRecord struct {
	Format         string         `json:"format"`
	Worktree       string         `json:"worktree"`
	ExportDigest   string         `json:"export_digest"`
	BaselineDigest string         `json:"baseline_digest"`
	TargetDigest   string         `json:"target_digest"`
	Paths          []recoveryPath `json:"paths"`
}

// recoveryPath is the pre-apply state of one path; Before is nil when the path did not exist.
type recoveryPath struct {
	Path   string   `json:"path"`
	Op     ChangeOp `json:"op"`
	Before *Entry   `json:"before"`
}

// ApplyExport applies a tbound-project-export/v1 stream to a clean Git worktree. It runs as the
// user. Before the first write it validates the stream, the Git state, the worktree manifest against
// the baseline digest and the manifest the changes produce against the target digest, and writes a
// recovery record to opts.StateDir. Each target is rechecked immediately before it is written, files
// are replaced through a temporary file and a rename, and the result must equal the target manifest.
// It returns the changed paths. An error before the first write changes nothing. After the first
// write an *ApplyError names the changed paths and the recovery record stays.
//
// It does not exclude concurrent writers; the per-target recheck narrows the window, nothing more.
func ApplyExport(bundle io.ReadSeeker, worktree string, opts ApplyOptions) ([]string, error) {
	header, exportDigest, offsets, err := readExport(bundle, opts.Limits)
	if err != nil {
		return nil, err
	}
	for _, p := range opts.Exclude {
		if err := validatePath(p); err != nil {
			return nil, fmt.Errorf("projectbundle: exclude path %q: %v", p, err)
		}
	}
	for _, c := range header.Changes {
		if excluded(c.Path, opts.Exclude) {
			return nil, fmt.Errorf("projectbundle: change %s is inside an excluded path", c.Path)
		}
	}
	root, err := openRoot(worktree)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	absWorktree, err := filepath.Abs(worktree)
	if err != nil {
		return nil, err
	}
	recordPath, err := recoveryRecordPath(opts.StateDir, absWorktree)
	if err != nil {
		return nil, err
	}
	if err := checkGit(absWorktree, header.Changes); err != nil {
		return nil, err
	}
	before, _, err := scanTree(worktree, scanOptions{Exclude: opts.Exclude, Limits: opts.Limits})
	if err != nil {
		return nil, err
	}
	if digest, err := before.Digest(); err != nil || digest != header.BaselineDigest {
		return nil, fmt.Errorf("%w: worktree manifest is %s, export baseline is %s", ErrBaselineMismatch, digest, header.BaselineDigest)
	}
	target, err := manifestAfter(before, header, opts.Limits)
	if err != nil {
		return nil, err
	}
	if digest, err := target.Digest(); err != nil || digest != header.TargetDigest {
		return nil, fmt.Errorf("%w: the changes produce manifest %s, the export declares %s", ErrInvalidBundle, digest, header.TargetDigest)
	}
	if err := checkDeletedDirectories(root, header.Changes); err != nil {
		return nil, err
	}
	states := make(map[string]Entry, len(before.Entries))
	for _, e := range before.Entries {
		states[e.Path] = e
	}
	if err := writeRecoveryRecord(recordPath, absWorktree, exportDigest, header, states); err != nil {
		return nil, err
	}

	a := &applier{root: root, bundle: bundle, offsets: offsets, states: states}
	var changed []string
	for _, i := range applyOrder(header.Changes) {
		c := header.Changes[i]
		didChange, err := a.apply(i, c)
		if didChange {
			changed = append(changed, c.Path)
		}
		if err != nil {
			if len(changed) == 0 {
				os.Remove(recordPath)
				syncDir(opts.StateDir)
				return nil, fmt.Errorf("projectbundle: %s: %w (nothing was changed)", c.Path, err)
			}
			return nil, &ApplyError{Failed: c.Path, Changed: changed, Record: recordPath, Err: err}
		}
	}
	after, _, err := scanTree(worktree, scanOptions{Exclude: opts.Exclude, Limits: opts.Limits})
	if err == nil {
		var digest string
		if digest, err = after.Digest(); err == nil && digest != header.TargetDigest {
			err = fmt.Errorf("the result manifest %s is not the target manifest %s", digest, header.TargetDigest)
		}
	}
	if err != nil {
		return nil, &ApplyError{Changed: changed, Record: recordPath, Err: err}
	}
	if err := os.Remove(recordPath); err != nil {
		return changed, fmt.Errorf("projectbundle: applied, but the recovery record could not be removed: %w", err)
	}
	return changed, syncDir(opts.StateDir)
}

func excluded(path string, exclude []string) bool {
	if isGitPath(path) || path == fenceDir || strings.HasPrefix(path, fenceDir+"/") {
		return true
	}
	for _, e := range exclude {
		if path == e || strings.HasPrefix(path, e+"/") {
			return true
		}
	}
	return false
}

// applyOrder lists creates, modifies and chmods by ascending path (parents first), then deletes by
// descending path (children first).
func applyOrder(changes []Change) []int {
	var order, deletes []int
	for i, c := range changes {
		if c.Op == OpDelete {
			deletes = append(deletes, i)
		} else {
			order = append(order, i)
		}
	}
	for i := len(deletes) - 1; i >= 0; i-- {
		order = append(order, deletes[i])
	}
	return order
}

func recoveryRecordPath(stateDir, worktree string) (string, error) {
	if stateDir == "" {
		return "", errors.New("projectbundle: a state directory is required")
	}
	state, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return "", fmt.Errorf("projectbundle: state directory: %w", err)
	}
	if info, err := os.Stat(state); err != nil || !info.IsDir() {
		return "", fmt.Errorf("projectbundle: state directory %s is not a directory", stateDir)
	}
	real, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(real, state); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return "", errors.New("projectbundle: the state directory must be outside the worktree")
	}
	path := filepath.Join(state, recoveryRecordName)
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("%w: %s", ErrRecoveryRecordExists, path)
	}
	return path, nil
}

func writeRecoveryRecord(path, worktree, exportDigest string, header ExportHeader, states map[string]Entry) error {
	record := recoveryRecord{Format: recoveryFormat, Worktree: worktree, ExportDigest: exportDigest,
		BaselineDigest: header.BaselineDigest, TargetDigest: header.TargetDigest}
	for _, c := range header.Changes {
		p := recoveryPath{Path: c.Path, Op: c.Op}
		if e, ok := states[c.Path]; ok {
			p.Before = &e
		}
		record.Paths = append(record.Paths, p)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrRecoveryRecordExists, path)
		}
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = syncDir(filepath.Dir(path))
	}
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("projectbundle: write recovery record: %w", err)
	}
	return nil
}

// checkGit requires the worktree to be the top level of a Git work tree, to have no modified tracked
// file and no untracked file at a target path, and every file to modify or delete to be tracked so
// Git still holds its original content.
func checkGit(worktree string, changes []Change) error {
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", worktree, "-c", "core.fsmonitor=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("projectbundle: git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	top, err := git("rev-parse", "--is-inside-work-tree", "--show-prefix")
	if err != nil {
		return err
	}
	if string(top) != "true\n\n" {
		return errors.New("projectbundle: the worktree must be the top level of a Git work tree")
	}
	status, err := git("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	for _, record := range bytes.Split(status, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if len(record) < 4 || string(record[:2]) != "??" {
			return fmt.Errorf("%w: git reports a modified tracked file: %q", ErrDirtyWorktree, record)
		}
		for _, c := range changes {
			p := string(record[3:])
			if p == c.Path || strings.HasPrefix(p, c.Path+"/") || strings.HasPrefix(c.Path, p+"/") {
				return fmt.Errorf("%w: untracked file %s is at target path %s", ErrDirtyWorktree, p, c.Path)
			}
		}
	}
	listing, err := git("ls-files", "-z")
	if err != nil {
		return err
	}
	tracked := make(map[string]bool)
	for _, p := range bytes.Split(listing, []byte{0}) {
		tracked[string(p)] = true
	}
	for _, c := range changes {
		if c.Type == TypeFile && c.Op != OpCreate && !tracked[c.Path] {
			return fmt.Errorf("%w: %s is not tracked by Git, so its original content could not be restored", ErrDirtyWorktree, c.Path)
		}
	}
	return nil
}

// checkDeletedDirectories refuses to delete a directory that holds anything the export does not
// delete, such as an excluded path, so the later rmdir cannot fail after earlier writes.
func checkDeletedDirectories(root *os.File, changes []Change) error {
	deleted := make(map[string]bool)
	for _, c := range changes {
		if c.Op == OpDelete {
			deleted[c.Path] = true
		}
	}
	for _, c := range changes {
		if c.Op != OpDelete || c.Type != TypeDir {
			continue
		}
		dir, err := openBeneath(root, c.Path, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			return err
		}
		names, err := dir.Readdirnames(-1)
		dir.Close()
		if err != nil {
			return err
		}
		for _, name := range names {
			if !deleted[c.Path+"/"+name] {
				return fmt.Errorf("projectbundle: directory %s holds %s, which the export does not delete", c.Path, name)
			}
		}
	}
	return nil
}

type applier struct {
	root    *os.File
	bundle  io.ReadSeeker
	offsets []int64
	states  map[string]Entry
}

// apply performs one change. changed reports that the target may differ from before, even on error.
func (a *applier) apply(i int, c Change) (bool, error) {
	dir, name := filepath.Split(c.Path)
	parent, err := openBeneath(a.root, strings.TrimSuffix(dir, "/"), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	pfd := int(parent.Fd())
	old := a.states[c.Path]
	switch {
	case c.Op == OpCreate && c.Type == TypeDir:
		if err := unix.Mkdirat(pfd, name, 0o755); err != nil {
			return false, err
		}
	case c.Op == OpCreate:
		mode := uint32(0o644)
		if c.Executable {
			mode = 0o755
		}
		if err := a.install(i, c, pfd, name, mode, false, old); err != nil {
			return false, err
		}
	case c.Op == OpModify:
		f, perm, err := openChecked(pfd, name, old)
		if err != nil {
			return false, err
		}
		f.Close()
		if err := a.install(i, c, pfd, name, withExecutable(perm, c.Executable), true, old); err != nil {
			return false, err
		}
	case c.Op == OpChmod:
		f, perm, err := openChecked(pfd, name, old)
		if err != nil {
			return false, err
		}
		defer f.Close()
		if err := f.Chmod(os.FileMode(withExecutable(perm, c.Executable))); err != nil {
			return false, err
		}
	case c.Op == OpDelete && c.Type == TypeDir:
		if err := unix.Unlinkat(pfd, name, unix.AT_REMOVEDIR); err != nil {
			return false, err
		}
	case c.Op == OpDelete:
		f, _, err := openChecked(pfd, name, old)
		if err != nil {
			return false, err
		}
		f.Close()
		if err := unix.Unlinkat(pfd, name, 0); err != nil {
			return false, err
		}
	}
	return true, parent.Sync()
}

func withExecutable(perm uint32, executable bool) uint32 {
	if executable {
		return perm | (perm&0o444)>>2
	}
	return perm &^ 0o111
}

// openChecked opens the regular file at name below parent and verifies it still has the state the
// baseline manifest recorded. It returns the open file and its permission bits.
func openChecked(pfd int, name string, want Entry) (*os.File, uint32, error) {
	fd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("target changed since the baseline check: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, 0, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink > 1 || st.Size != want.Size || (st.Mode&0o111 != 0) != want.Executable {
		f.Close()
		return nil, 0, errors.New("target changed since the baseline check")
	}
	if sum, err := hashFile(f, want.Size); err != nil || sum != want.SHA256 {
		f.Close()
		return nil, 0, errors.New("target content changed since the baseline check")
	}
	return f, st.Mode & 0o777, nil
}

// install writes the content of change i to a temporary file beside the target, rechecks the target
// and renames the temporary file over it. A create refuses to replace anything.
func (a *applier) install(i int, c Change, pfd int, name string, mode uint32, replace bool, old Entry) error {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmpName := ".tbound-apply-" + hex.EncodeToString(suffix[:])
	fd, err := unix.Openat(pfd, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	tmp := os.NewFile(uintptr(fd), tmpName)
	renamed := false
	defer func() {
		tmp.Close()
		if !renamed {
			unix.Unlinkat(pfd, tmpName, 0)
		}
	}()
	if _, err := a.bundle.Seek(a.offsets[i], io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(a.bundle, c.Size))
	if err != nil {
		return err
	}
	if n != c.Size || hex.EncodeToString(h.Sum(nil)) != c.SHA256 {
		return fmt.Errorf("content of %s changed after validation", c.Path)
	}
	if err := tmp.Chmod(os.FileMode(mode)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if !replace {
		err = unix.Renameat2(pfd, tmpName, pfd, name, unix.RENAME_NOREPLACE)
		renamed = err == nil
		return err
	}
	current, _, err := openChecked(pfd, name, old)
	if err != nil {
		return err
	}
	current.Close()
	err = unix.Renameat(pfd, tmpName, pfd, name)
	renamed = err == nil
	return err
}
