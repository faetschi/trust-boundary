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

// ApplyMode selects patch generation or writing under caller-guaranteed writer exclusion.
type ApplyMode uint8

const (
	PatchMode ApplyMode = iota
	DirectMode
	PatchFileName = "tbound-export.patch"
)

var ErrParentChanged = errors.New("projectbundle: target parent moved or changed; recovery record retained")

// ApplyOptions configure ApplyExport.
type ApplyOptions struct {
	Mode ApplyMode
	// WriterExclusionHeld is the caller's guarantee that no other writer can move or alter the tree.
	WriterExclusionHeld bool
	// StateDir is an existing directory outside the worktree that holds the recovery record.
	StateDir string
	// Exclude is the exclude list the baseline was built with. The worktree manifest ignores these
	// paths, and no change may touch them.
	Exclude  []string
	Limits   Limits
	syncFile func(*os.File) error
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
	Changed        []string       `json:"changed_actual_paths"`
}

// recoveryPath is the pre-apply state of one path; Before is nil when the path did not exist.
type recoveryPath struct {
	Path   string   `json:"path"`
	Op     ChangeOp `json:"op"`
	Before *Entry   `json:"before"`
}

// ApplyExport verifies the export, clean Git baseline and targets. By default it writes
// PatchFileName in StateDir, leaving the worktree untouched. DirectMode requires the
// caller to exclude all concurrent writers. Failures after a mutation retain recovery data.
func ApplyExport(bundle io.ReadSeeker, worktree string, opts ApplyOptions) ([]string, error) {
	if opts.Mode != PatchMode && opts.Mode != DirectMode {
		return nil, errors.New("projectbundle: unknown apply mode")
	}
	if opts.Mode == DirectMode && !opts.WriterExclusionHeld {
		return nil, errors.New("projectbundle: direct apply requires caller-guaranteed writer exclusion")
	}
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
	store, err := openApplyState(opts.StateDir, absWorktree)
	if err != nil {
		return nil, err
	}
	defer store.dir.Close()
	recordPath := filepath.Join(store.dir.Name(), recoveryRecordName)
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
	if opts.Mode == PatchMode {
		return writePatch(bundle, offsets, root, absWorktree, store, before, target, header)
	}
	record := recoveryRecord{Format: recoveryFormat, Worktree: absWorktree, ExportDigest: exportDigest, BaselineDigest: header.BaselineDigest, TargetDigest: header.TargetDigest}
	for _, c := range header.Changes {
		p := recoveryPath{Path: c.Path, Op: c.Op}
		if e, ok := states[c.Path]; ok {
			p.Before = &e
		}
		record.Paths = append(record.Paths, p)
	}
	if err := store.writeRecord(record, true); err != nil {
		return nil, err
	}

	if _, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd())); err != nil {
		return nil, fmt.Errorf("projectbundle: direct recovery needs /proc/self/fd: %w", err)
	}
	a := &applier{root: root, bundle: bundle, offsets: offsets, states: states, syncFile: opts.syncFile}
	defer a.closeLocations()
	var changed []string
	for _, i := range applyOrder(header.Changes) {
		c := header.Changes[i]
		didChange, err := a.apply(i, c)
		if didChange {
			changed = append(changed, c.Path)
		}
		if didChange || len(changed) > 0 && err != nil {
			actual, locationErr := a.actualPaths()
			record.Changed = actual
			err = errors.Join(err, locationErr, store.writeRecord(record, false))
		}
		if err != nil {
			if len(changed) == 0 && !errors.Is(err, ErrParentChanged) {
				if cleanupErr := store.removeRecord(); cleanupErr != nil {
					return nil, &ApplyError{Failed: c.Path, Record: recordPath, Err: errors.Join(err, cleanupErr)}
				}
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
		actual, locationErr := a.actualPaths()
		record.Changed = actual
		err = errors.Join(err, locationErr, store.writeRecord(record, false))
		return nil, &ApplyError{Changed: changed, Record: recordPath, Err: err}
	}
	if err := store.removeRecord(); err != nil {
		return changed, fmt.Errorf("projectbundle: applied, but the recovery record could not be removed: %w", err)
	}
	return changed, nil
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

type applyState struct{ dir *os.File }

func openApplyState(stateDir, worktree string) (*applyState, error) {
	if stateDir == "" {
		return nil, errors.New("projectbundle: a state directory is required")
	}
	state, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return nil, err
	}
	if rel, err := filepath.Rel(real, state); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return nil, errors.New("projectbundle: the state directory must be outside the worktree")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(state, "/"), "/")
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			next, e := unix.Openat(fd, parts[i], unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			unix.Close(fd)
			if e != nil {
				return nil, e
			}
			fd = next
		}
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		last := i == len(parts)-1
		valid := (st.Uid == 0 || st.Mode&0022 == 0)
		if last {
			valid = st.Uid == uint32(os.Geteuid()) && st.Mode&07777 == 0700
		}
		if err != nil || !valid {
			unix.Close(fd)
			return nil, errors.New("projectbundle: state directory must be caller-owned 0700 with protected ancestors")
		}
	}
	dir := os.NewFile(uintptr(fd), state)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		dir.Close()
		return nil, err
	}
	var st unix.Stat_t
	err = unix.Fstatat(fd, recoveryRecordName, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		dir.Close()
		return nil, fmt.Errorf("%w: %s", ErrRecoveryRecordExists, filepath.Join(state, recoveryRecordName))
	}
	if !errors.Is(err, unix.ENOENT) {
		dir.Close()
		return nil, err
	}
	return &applyState{dir: dir}, nil
}

func (s *applyState) removeRecord() error {
	if err := unix.Unlinkat(int(s.dir.Fd()), recoveryRecordName, 0); err != nil {
		return err
	}
	return s.dir.Sync()
}

func (s *applyState) writeRecord(record recoveryRecord, create bool) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	name := recoveryRecordName
	if !create {
		name += ".next"
	}
	fd, err := resolveAt(int(s.dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil && !create {
		err = unix.Renameat(int(s.dir.Fd()), name, int(s.dir.Fd()), recoveryRecordName)
	}
	if err == nil {
		err = s.dir.Sync()
	}
	if !create && err != nil {
		unix.Unlinkat(int(s.dir.Fd()), name, 0)
	}
	return err
}

// checkGit requires the worktree to be the top level of a Git work tree, to have no modified tracked
// file and no untracked file at a target path, and every file to modify or delete to be tracked so
// Git still holds its original content.
func checkGit(worktree string, changes []Change) error {
	root, err := openRoot(worktree)
	if err != nil {
		return err
	}
	defer root.Close()
	gitInput := func(input io.Reader, args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", worktree, "-c", "core.fsmonitor=false"}, args...)...)
		cmd.Env = patchGitEnv()
		cmd.Stdin = input
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("projectbundle: git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	git := func(args ...string) ([]byte, error) { return gitInput(nil, args...) }
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
	listing, err := git("ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	tracked := make(map[string]bool)
	for _, p := range bytes.Split(listing, []byte{0}) {
		if len(p) < 3 {
			continue
		}
		path := string(p[2:])
		tracked[path] = true
		for _, c := range changes {
			if (path == c.Path || strings.HasPrefix(path, c.Path+"/")) && (p[0] == 'S' || p[0] >= 'a' && p[0] <= 'z') {
				return fmt.Errorf("%w: target %s has assume-unchanged or skip-worktree set", ErrDirtyWorktree, path)
			}
		}
	}
	for _, c := range changes {
		if c.Type == TypeFile && c.Op != OpCreate && tracked[c.Path] {
			tree, err := git("ls-tree", "-z", "HEAD", "--", ":(literal)"+c.Path)
			if err != nil {
				return err
			}
			fields := bytes.SplitN(tree, []byte{'\t'}, 2)
			var blob string
			if len(fields) == 2 {
				meta := strings.Fields(string(fields[0]))
				if len(meta) == 3 && meta[1] == "blob" {
					blob = meta[2]
				}
			}
			f, err := openBeneath(root, c.Path, unix.O_RDONLY|unix.O_NONBLOCK)
			if err != nil {
				return err
			}
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() {
				f.Close()
				return fmt.Errorf("%w: target %s is not a regular file", ErrDirtyWorktree, c.Path)
			}
			hash, err := gitInput(f, "hash-object", "--no-filters", "--stdin")
			f.Close()
			if err != nil {
				return err
			}
			if blob == "" || strings.TrimSpace(string(hash)) != blob {
				return fmt.Errorf("%w: target %s differs from its committed HEAD blob", ErrDirtyWorktree, c.Path)
			}
		}
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
	root      *os.File
	bundle    io.ReadSeeker
	offsets   []int64
	states    map[string]Entry
	locations []applyLocation
	syncFile  func(*os.File) error
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
	if err := a.checkParent(parent, c.Path); err != nil {
		return false, err
	}
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
		if err := a.checkParent(parent, c.Path); err != nil {
			return false, err
		}
		if err := f.Chmod(os.FileMode(withExecutable(perm, c.Executable))); err != nil {
			return false, err
		}
		if err := a.noteChange(parent, name); err != nil {
			return true, err
		}
		syncFile := a.syncFile
		if syncFile == nil {
			syncFile = (*os.File).Sync
		}
		if err := syncFile(f); err != nil {
			return true, err
		}
	case c.Op == OpDelete && c.Type == TypeDir:
		target, err := openBeneath(a.root, c.Path, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			return false, err
		}
		target.Close()
		if err := a.checkParent(parent, c.Path); err != nil {
			return false, err
		}
		if err := unix.Unlinkat(pfd, name, unix.AT_REMOVEDIR); err != nil {
			return false, err
		}
	case c.Op == OpDelete:
		f, _, err := openChecked(pfd, name, old)
		if err != nil {
			return false, err
		}
		f.Close()
		if err := a.checkParent(parent, c.Path); err != nil {
			return false, err
		}
		if err := unix.Unlinkat(pfd, name, 0); err != nil {
			return false, err
		}
	}
	if c.Op != OpChmod {
		if err := a.noteChange(parent, name); err != nil {
			return true, err
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
	fd, err := resolveAt(pfd, name, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("target changed since the baseline check: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	if err := checkMetadata(fd); err != nil {
		f.Close()
		return nil, 0, err
	}
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
	fd, err := resolveAt(pfd, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
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
	if err := a.checkParentFD(pfd, c.Path); err != nil {
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
	if err := a.checkParentFD(pfd, c.Path); err != nil {
		return err
	}
	err = unix.Renameat(pfd, tmpName, pfd, name)
	renamed = err == nil
	return err
}

func (a *applier) checkParent(parent *os.File, path string) error {
	return a.checkParentFD(int(parent.Fd()), path)
}

func (a *applier) checkParentFD(fd int, path string) error {
	parentPath := filepath.Dir(path)
	current, err := openBeneath(a.root, parentPath, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrParentChanged, err)
	}
	defer current.Close()
	var held, now unix.Stat_t
	if unix.Fstat(fd, &held) != nil || unix.Fstat(int(current.Fd()), &now) != nil || held.Dev != now.Dev || held.Ino != now.Ino {
		return ErrParentChanged
	}
	return checkMetadata(fd)
}

type applyLocation struct {
	parent   *os.File
	name     string
	fallback string
}

func (a *applier) noteChange(parent *os.File, name string) error {
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", parent.Fd()))
	if err != nil {
		return err
	}
	location := applyLocation{name: name, fallback: filepath.Join(path, name)}
	fd, err := unix.FcntlInt(parent.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		location.parent = os.NewFile(uintptr(fd), path)
	}
	a.locations = append(a.locations, location)
	return err
}

func (a *applier) actualPaths() ([]string, error) {
	var paths []string
	var errs error
	for _, l := range a.locations {
		path := l.fallback
		if l.parent != nil {
			var st unix.Stat_t
			if err := unix.Fstat(int(l.parent.Fd()), &st); err != nil {
				errs = errors.Join(errs, err)
			}
			if st.Nlink == 0 {
				paths = append(paths, path)
				continue
			}
			dir, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", l.parent.Fd()))
			if err != nil {
				errs = errors.Join(errs, err)
			} else {
				path = filepath.Join(dir, l.name)
			}
		}
		paths = append(paths, path)
	}
	return paths, errs
}

func (a *applier) closeLocations() {
	for _, l := range a.locations {
		if l.parent != nil {
			l.parent.Close()
		}
	}
}
