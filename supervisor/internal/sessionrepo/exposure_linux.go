//go:build linux

package sessionrepo

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// ViewMode selects how an exposed generation is refreshed. Recreated views
// replace the bind at one stable target. Indirection views bind each selected
// generation separately and atomically switch a supervisor-owned symlink.
type ViewMode string

const (
	ViewRecreated   ViewMode = "recreated"
	ViewIndirection ViewMode = "indirection"
)

var (
	ErrExposureBinderUnavailable = errors.New("session repository has no exposure binder")
	ErrExposureClosed            = errors.New("exposed generation view is closed")
	ErrInvalidViewMode           = errors.New("invalid exposed generation view mode")
)

// Binder installs and removes read-only bind mounts. The source is an O_PATH
// descriptor pinned to the verified generation directory. Implementations
// must either complete a read-only bind or leave the target unmounted.
//
// The interface is injected through Options so selection, refresh, and
// generation-identity checks can be exercised without mount privileges.
type Binder interface {
	BindReadOnly(source *os.File, target string) error
	Unmount(target string) error
}

// LinuxReadOnlyBinder implements a bind mount followed by a read-only bind
// remount. It must run in the mount namespace that owns the intended view.
// The caller still must expose the resulting view to its consumer safely.
type LinuxReadOnlyBinder struct{}

// BindReadOnly mounts the pinned source directory at target and makes that
// mount read-only. A failed remount is cleaned up before the error is returned.
func (LinuxReadOnlyBinder) BindReadOnly(source *os.File, target string) error {
	if source == nil || target == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return ErrInvalidOptions
	}
	sourcePath := fmt.Sprintf("/proc/self/fd/%d", source.Fd())
	if err := syscall.Mount(sourcePath, target, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind generation view: %w", err)
	}
	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV)
	if err := syscall.Mount("", target, "", flags, ""); err != nil {
		unmountErr := syscall.Unmount(target, syscall.MNT_DETACH)
		return errors.Join(fmt.Errorf("remount generation view read-only: %w", err), unmountErr)
	}
	return nil
}

// Unmount detaches the selected bind from the private exposure tree. Open
// consumer references may continue to refer to the detached old generation;
// this method does not claim that any consumer process has settled.
func (LinuxReadOnlyBinder) Unmount(target string) error {
	if target == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return ErrInvalidOptions
	}
	if err := syscall.Unmount(target, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("detach generation view: %w", err)
	}
	return nil
}

// ExposedView is a supervisor-selected read-only view of one retained
// generation. Observed reports the selected, re-proven generation evidence;
// it is not evidence that Pi or another consumer actually observed the view.
type ExposedView struct {
	store        *Store
	name         string
	mode         ViewMode
	root         *os.File
	rootPath     string
	selected     *Generation
	observed     GenerationEvidence
	activeTarget string
	mounts       map[string]bool
	nextTarget   uint64
	closed       bool
}

// Expose creates a private view of g. The selected generation identity is
// verified before and after the injected binder installs the read-only view.
func (g *Generation) Expose(mode ViewMode) (*ExposedView, error) {
	if g == nil || g.store == nil {
		return nil, ErrNoBaseline
	}
	if mode != ViewRecreated && mode != ViewIndirection {
		return nil, ErrInvalidViewMode
	}
	s := g.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUsableLocked(); err != nil {
		return nil, err
	}
	if s.options.ExposureBinder == nil {
		return nil, ErrExposureBinderUnavailable
	}
	if !s.containsGenerationLocked(g) {
		return nil, ErrStaleGeneration
	}
	if err := s.verifyGenerationLocked(g); err != nil {
		return nil, err
	}
	name, err := newExposureName()
	if err != nil {
		return nil, err
	}
	if err := syscall.Mkdirat(int(s.exposedDir.Fd()), name, 0o700); err != nil {
		return nil, fmt.Errorf("create private exposed view: %w", err)
	}
	root, err := openDirectoryAt(s.exposedDir, name)
	if err != nil {
		_ = removeTreeAt(int(s.exposedDir.Fd()), name)
		return nil, fmt.Errorf("open private exposed view: %w", err)
	}
	if err := validatePrivateDirectory(root); err != nil {
		_ = root.Close()
		_ = removeTreeAt(int(s.exposedDir.Fd()), name)
		return nil, err
	}
	view := &ExposedView{
		store: s, name: name, mode: mode, root: root,
		rootPath: filepath.Join(s.rootPath, "exposed", name),
		mounts:   make(map[string]bool),
	}
	s.exposedViews[name] = view

	target := view.rootPath
	if mode == ViewIndirection {
		target, err = view.makeTargetLocked()
		if err != nil {
			return nil, view.failCreateLocked(err)
		}
	}
	if err := view.bindAndVerifyLocked(g, target); err != nil {
		return nil, view.failCreateLocked(err)
	}
	if mode == ViewIndirection {
		if err := view.switchIndirectionLocked(filepath.Base(target)); err != nil {
			return nil, view.failCreateLocked(err)
		}
	}
	view.activeTarget = target
	view.selectLocked(g)
	if err := syncDirectory(s.exposedDir); err != nil {
		return nil, view.failCreateLocked(fmt.Errorf("sync exposed-view directory: %w", err))
	}
	return view, nil
}

// Refresh explicitly reselects next. For ViewRecreated this detaches and
// recreates the bind at the same target; for ViewIndirection it mounts the new
// generation at a fresh target and atomically repoints the private selector.
// The new generation is rescanned and its TreeDigest re-proven before it is
// recorded as Observed. Replacing files beneath an existing bind is not a
// refresh operation.
func (v *ExposedView) Refresh(next *Generation) error {
	if v == nil || v.store == nil {
		return ErrExposureClosed
	}
	s := v.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.closed || s.exposedViews[v.name] != v {
		return ErrExposureClosed
	}
	if err := s.checkUsableLocked(); err != nil {
		return err
	}
	if next == nil || next.store != s || !s.containsGenerationLocked(next) {
		return ErrStaleGeneration
	}
	if err := s.verifyGenerationLocked(next); err != nil {
		return err
	}
	if v.mode == ViewRecreated {
		if v.activeTarget != "" && v.mounts[v.activeTarget] {
			if err := s.options.ExposureBinder.Unmount(v.activeTarget); err != nil {
				return err
			}
			delete(v.mounts, v.activeTarget)
		}
		v.activeTarget = ""
		v.observed = GenerationEvidence{}
		if err := v.bindAndVerifyLocked(next, v.rootPath); err != nil {
			return err
		}
		v.activeTarget = v.rootPath
		v.selectLocked(next)
		return nil
	}

	newTarget, err := v.makeTargetLocked()
	if err != nil {
		return err
	}
	if err := v.bindAndVerifyLocked(next, newTarget); err != nil {
		_ = removeTreeAt(int(v.root.Fd()), filepath.Base(newTarget))
		return err
	}
	if err := v.switchIndirectionLocked(filepath.Base(newTarget)); err != nil {
		_ = s.options.ExposureBinder.Unmount(newTarget)
		delete(v.mounts, newTarget)
		_ = removeTreeAt(int(v.root.Fd()), filepath.Base(newTarget))
		return err
	}
	oldTarget := v.activeTarget
	v.activeTarget = newTarget
	v.selectLocked(next)
	if oldTarget != "" && oldTarget != newTarget && v.mounts[oldTarget] {
		if err := s.options.ExposureBinder.Unmount(oldTarget); err != nil {
			return fmt.Errorf("new generation selected but prior view detach failed: %w", err)
		}
		delete(v.mounts, oldTarget)
		if err := removeTreeAt(int(v.root.Fd()), filepath.Base(oldTarget)); err != nil {
			return fmt.Errorf("new generation selected but prior view cleanup failed: %w", err)
		}
	}
	return nil
}

// Observed returns the last generation identity that the supervisor selected
// and successfully re-proved after binding. It is not a consumer observation.
func (v *ExposedView) Observed() GenerationEvidence {
	if v == nil || v.store == nil {
		return GenerationEvidence{}
	}
	v.store.mu.Lock()
	defer v.store.mu.Unlock()
	return cloneGenerationEvidence(v.observed)
}

// ReadOnlyMountSource returns an O_PATH descriptor for the currently selected
// view. O_PATH itself does not enforce read-only access; a runtime must still
// install and verify a read-only bind in the consumer's mount namespace.
func (v *ExposedView) ReadOnlyMountSource() (*os.File, error) {
	if v == nil || v.store == nil {
		return nil, ErrExposureClosed
	}
	s := v.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.closed || s.exposedViews[v.name] != v || v.selected == nil || v.activeTarget == "" {
		return nil, ErrExposureClosed
	}
	if err := s.checkUsableLocked(); err != nil {
		return nil, err
	}
	if err := s.verifyGenerationLocked(v.selected); err != nil {
		return nil, err
	}
	path := v.activeTarget
	if v.mode == ViewIndirection {
		path = filepath.Join(v.rootPath, "current")
	}
	fd, err := syscall.Open(path, linuxOPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open selected exposed view: %w", err)
	}
	if err := s.verifyGenerationLocked(v.selected); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// Close detaches the view and removes its private exposure entry. If a mount
// cannot be detached, cleanup fails closed and the residue makes Open reject
// the repository rather than silently accepting an unfinished exposure.
func (v *ExposedView) Close() error {
	if v == nil || v.store == nil {
		return nil
	}
	s := v.store
	s.mu.Lock()
	defer s.mu.Unlock()
	return v.closeLocked()
}

func (v *ExposedView) closeLocked() error {
	if v.closed {
		return nil
	}
	var detachErrors []error
	for target, mounted := range v.mounts {
		if !mounted {
			continue
		}
		if err := v.store.options.ExposureBinder.Unmount(target); err != nil {
			detachErrors = append(detachErrors, err)
			continue
		}
		delete(v.mounts, target)
	}
	if len(detachErrors) != 0 {
		return errors.Join(detachErrors...)
	}
	if err := removeTreeAt(int(v.store.exposedDir.Fd()), v.name); err != nil {
		return fmt.Errorf("remove private exposed view: %w", err)
	}
	if err := syncDirectory(v.store.exposedDir); err != nil {
		return fmt.Errorf("sync exposed-view cleanup: %w", err)
	}
	var closeErr error
	if v.root != nil {
		closeErr = v.root.Close()
		v.root = nil
	}
	v.closed = true
	v.selected = nil
	v.observed = GenerationEvidence{}
	delete(v.store.exposedViews, v.name)
	return closeErr
}

func (v *ExposedView) failCreateLocked(cause error) error {
	if cleanupErr := v.closeLocked(); cleanupErr != nil {
		return errors.Join(cause, fmt.Errorf("clean failed exposed view: %w", cleanupErr))
	}
	return cause
}

func (v *ExposedView) bindAndVerifyLocked(generation *Generation, target string) error {
	if err := v.store.verifyGenerationLocked(generation); err != nil {
		return err
	}
	source, err := openGenerationSourceLocked(generation)
	if err != nil {
		return err
	}
	bindErr := v.store.options.ExposureBinder.BindReadOnly(source, target)
	closeErr := source.Close()
	if bindErr != nil {
		return errors.Join(fmt.Errorf("bind generation %s read-only: %w", generation.id, bindErr), closeErr)
	}
	v.mounts[target] = true
	if closeErr != nil {
		return fmt.Errorf("close pinned generation source: %w", closeErr)
	}
	if err := v.store.verifyGenerationLocked(generation); err != nil {
		if unmountErr := v.store.options.ExposureBinder.Unmount(target); unmountErr == nil {
			delete(v.mounts, target)
		}
		return fmt.Errorf("re-prove bound generation %s: %w", generation.id, err)
	}
	return nil
}

func (v *ExposedView) makeTargetLocked() (string, error) {
	name := fmt.Sprintf("target-%d", v.nextTarget)
	v.nextTarget++
	if err := syscall.Mkdirat(int(v.root.Fd()), name, 0o700); err != nil {
		return "", fmt.Errorf("create private generation mount target: %w", err)
	}
	return filepath.Join(v.rootPath, name), nil
}

func (v *ExposedView) switchIndirectionLocked(target string) error {
	if target == "" || filepath.Base(target) != target || strings.ContainsAny(target, "/\\\x00") {
		return ErrInvalidOptions
	}
	temporary := fmt.Sprintf(".current-%d", v.nextTarget)
	if err := symlinkAt(target, temporary, int(v.root.Fd())); err != nil {
		return fmt.Errorf("create exposed-view selector: %w", err)
	}
	if err := syscall.Renameat(int(v.root.Fd()), temporary, int(v.root.Fd()), "current"); err != nil {
		_ = unlinkAt(int(v.root.Fd()), temporary, 0)
		return fmt.Errorf("switch exposed-view selector: %w", err)
	}
	return nil
}

func (v *ExposedView) selectLocked(generation *Generation) {
	v.selected = generation
	v.observed = GenerationEvidence{
		ID: generation.id, TreeDigest: generation.snapshot.TreeDigest,
		Manifest: cloneManifest(generation.snapshot.Manifest),
	}
}

func (s *Store) containsGenerationLocked(generation *Generation) bool {
	for _, retained := range s.generations {
		if retained == generation {
			return true
		}
	}
	return false
}

func openGenerationSourceLocked(generation *Generation) (*os.File, error) {
	fd, err := syscall.Openat(int(generation.store.genDir.Fd()), generation.id,
		linuxOPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open pinned generation source: %w", err)
	}
	var held, opened syscall.Stat_t
	if err := syscall.Fstat(int(generation.root.Fd()), &held); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	if err := syscall.Fstat(fd, &opened); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	if !sameNode(held, opened) {
		_ = syscall.Close(fd)
		return nil, ErrGenerationModified
	}
	name := filepath.Join(generation.store.rootPath, "generations", generation.id)
	return os.NewFile(uintptr(fd), name), nil
}

func newExposureName() (string, error) {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("mint exposed-view ID: %w", err)
	}
	return fmt.Sprintf("exposure-%x", token[:]), nil
}

func cloneGenerationEvidence(evidence GenerationEvidence) GenerationEvidence {
	evidence.Manifest = cloneManifest(evidence.Manifest)
	return evidence
}

func symlinkAt(target, link string, directoryFD int) error {
	targetPointer, err := syscall.BytePtrFromString(target)
	if err != nil {
		return err
	}
	linkPointer, err := syscall.BytePtrFromString(link)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_SYMLINKAT,
		uintptr(unsafe.Pointer(targetPointer)), uintptr(directoryFD), uintptr(unsafe.Pointer(linkPointer)))
	if errno != 0 {
		return errno
	}
	return nil
}
