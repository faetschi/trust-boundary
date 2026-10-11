//go:build linux

package sessionlaunch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"tbound/supervisor/internal/piruntime"
)

const (
	defaultRuntimeAssetMaxFiles   = 100_000
	defaultRuntimeAssetMaxBytes   = int64(4 << 30)
	maxRuntimeAssetSingleFileSize = int64(512 << 20)
)

// ErrRuntimeAssets is the fail-closed sentinel for the verified runtime-asset
// source. It is returned when the held bundle descriptor is not a bounded,
// symlink-free, single-filesystem tree whose content digest equals the pinned
// profile digest, or when a supplied evidence record does not match this source.
var ErrRuntimeAssets = errors.New("verified runtime-asset source is unavailable or unverified")

// ErrD09RuntimeAssetsUnavailable distinguishes a production-profile runtime
// source refusal from the content-only development asset inspector below.
var ErrD09RuntimeAssetsUnavailable = errors.New("D09 runtime assets are not a protected immutable host source")
var ErrD09RuntimeAssetExportOpen = errors.New("caller-owned D09 runtime-source descriptor must close before its capability")

// RuntimeAssetSpec is the concrete measurement contract the composition root
// (owner A/C) supplies from the signed host profile. It contains no boolean
// trust switch: the bundle content digest and the namespace/profile identity
// are computed and compared, never asserted by the caller.
type RuntimeAssetSpec struct {
	// BundleDigest is the pinned digest of the complete runtime dependency tree
	// (the same scheme as piruntime.VerifyLinuxPiRuntimeBundle).
	BundleDigest string
	// NamespaceProfileDigest binds this source to the admitted read-only
	// namespace/containment profile identity.
	NamespaceProfileDigest string
	// MaxFiles and MaxBytes bound the scan; zero selects the defaults.
	MaxFiles int
	MaxBytes int64
}

// VerifiedRuntimeAssets is a content-pinned source inspector for fixtures and
// development. It deliberately does NOT implement
// piruntime.VerifiedWorkerRuntimeAssets and cannot be admitted by D09.
//
// Export returns a fresh descriptor for the held bundle source. Per the
// piruntime descriptor contract this source FD is Go-only: it is not proof that
// the child received a safe mount. The D09 launcher must reopen this source
// inside its controlled namespace and independently verify the consumer-side
// read-only mount before it creates child FD 9. This type pins bytes only; it
// does not assert a protected host mount or consumer mount.
type VerifiedRuntimeAssets struct {
	mu     sync.Mutex
	root   *os.File
	spec   RuntimeAssetSpec
	digest string
	closed bool
}

// VerifiedD09RuntimeAssetSource is the production D09 runtime-bundle source
// capability. Unlike OpenVerifiedRuntimeAssets (content-only inspection used by
// fixtures), this constructor requires a root-owned source on an actually
// read-only host mount and rechecks that condition before every export/verify.
// Its exported descriptor is still a SOURCE descriptor: only the trusted D09
// launcher/entrypoint may bind it read-only and prove the distinct consumer
// mount before Node starts.
type VerifiedD09RuntimeAssetSource struct {
	mu         sync.Mutex
	assets     *VerifiedRuntimeAssets
	exportedFD *os.File
	exported   bool
	closed     bool
}

// OpenVerifiedRuntimeAssets binds an already-open bundle root descriptor to a
// pinned content digest. It duplicates the descriptor (Go-only), recomputes the
// complete content digest, and refuses unless it equals spec.BundleDigest. The
// caller must keep the returned value alive through session teardown.
func OpenVerifiedRuntimeAssets(root *os.File, spec RuntimeAssetSpec) (*VerifiedRuntimeAssets, error) {
	if root == nil || !validDigest(spec.BundleDigest) || !validDigest(spec.NamespaceProfileDigest) {
		return nil, ErrRuntimeAssets
	}
	if spec.MaxFiles <= 0 {
		spec.MaxFiles = defaultRuntimeAssetMaxFiles
	}
	if spec.MaxBytes <= 0 {
		spec.MaxBytes = defaultRuntimeAssetMaxBytes
	}
	duplicate, err := duplicateFile(root, "sessionlaunch-runtime-asset-source")
	if err != nil {
		return nil, errors.Join(ErrRuntimeAssets, err)
	}
	digest, err := runtimeAssetContentDigest(duplicate, spec.MaxFiles, spec.MaxBytes)
	if err != nil {
		_ = duplicate.Close()
		return nil, errors.Join(ErrRuntimeAssets, err)
	}
	if digest != spec.BundleDigest {
		_ = duplicate.Close()
		return nil, fmt.Errorf("%w: runtime-asset content digest differs from the pinned profile", ErrRuntimeAssets)
	}
	// Best-effort consumer cross-check: reuse the D09 validator. On a read-only
	// mount it also proves the immutability precondition; on a writable mount it
	// refuses cheaply before walking, and the source-side content pin above still
	// holds. Any digest disagreement is fatal so the two schemes cannot drift.
	if consumerDigest, consumerErr := piruntime.VerifyLinuxPiRuntimeBundle(duplicate); consumerErr == nil && consumerDigest != digest {
		_ = duplicate.Close()
		return nil, fmt.Errorf("%w: runtime-asset digest disagrees with the D09 consumer verifier", ErrRuntimeAssets)
	}
	return &VerifiedRuntimeAssets{root: duplicate, spec: spec, digest: digest}, nil
}

// OpenVerifiedD09RuntimeAssetSource is the production constructor for the
// immutable Pi/runtime bundle. It requires the complete expected content digest,
// root ownership, and a read-only host mount as checked by the same D09 bundle
// verifier the trusted launcher uses. No mount is created here. The returned
// opaque capability holds the source FD privately and may export it once for
// the trusted D09 mount setup.
func OpenVerifiedD09RuntimeAssetSource(root *os.File, spec RuntimeAssetSpec) (*VerifiedD09RuntimeAssetSource, error) {
	assets, err := OpenVerifiedRuntimeAssets(root, spec)
	if err != nil {
		return nil, errors.Join(ErrD09RuntimeAssetsUnavailable, err)
	}
	assets.mu.Lock()
	err = assets.verifyD09RuntimeSourceLocked()
	assets.mu.Unlock()
	if err != nil {
		_ = assets.Close()
		return nil, errors.Join(ErrD09RuntimeAssetsUnavailable, err)
	}
	return &VerifiedD09RuntimeAssetSource{assets: assets}, nil
}

// ExportD09RuntimeAssetSource returns one caller-owned descriptor for the host
// source. The caller (trusted D09 channel/launcher) must close the returned FD;
// this capability retains its own private descriptor for revalidation. The
// source evidence is not a consumer-mount proof.
func (s *VerifiedD09RuntimeAssetSource) ExportD09RuntimeAssetSource(ctx context.Context) (*os.File, piruntime.WorkerRuntimeAssetEvidence, error) {
	if ctx == nil || s == nil {
		return nil, piruntime.WorkerRuntimeAssetEvidence{}, ErrD09RuntimeAssetsUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.exported || s.assets == nil {
		return nil, piruntime.WorkerRuntimeAssetEvidence{}, ErrD09RuntimeAssetsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, piruntime.WorkerRuntimeAssetEvidence{}, errors.Join(ErrD09RuntimeAssetsUnavailable, err)
	}
	s.assets.mu.Lock()
	verifyErr := s.assets.verifyD09RuntimeSourceLocked()
	s.assets.mu.Unlock()
	if verifyErr != nil {
		return nil, piruntime.WorkerRuntimeAssetEvidence{}, errors.Join(ErrD09RuntimeAssetsUnavailable, verifyErr)
	}
	file, evidence, err := s.assets.ExportPinnedRuntimeAssets(ctx)
	if err != nil {
		return nil, piruntime.WorkerRuntimeAssetEvidence{}, errors.Join(ErrD09RuntimeAssetsUnavailable, err)
	}
	s.exported = true
	s.exportedFD = file
	return file, evidence, nil
}

// VerifyD09RuntimeAssetSource revalidates host source immutability/content. The
// in-cell entrypoint still has to separately prove its consumer-side read-only
// mount and record a distinct consumer mount ID in the D09 TCB proof.
func (s *VerifiedD09RuntimeAssetSource) VerifyD09RuntimeAssetSource(ctx context.Context, evidence piruntime.WorkerRuntimeAssetEvidence) error {
	if ctx == nil || s == nil {
		return ErrD09RuntimeAssetsUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.assets == nil {
		return ErrD09RuntimeAssetsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrD09RuntimeAssetsUnavailable, err)
	}
	s.assets.mu.Lock()
	verifyErr := s.assets.verifyD09RuntimeSourceLocked()
	s.assets.mu.Unlock()
	if verifyErr != nil {
		return errors.Join(ErrD09RuntimeAssetsUnavailable, verifyErr)
	}
	if err := s.assets.VerifyPinnedRuntimeAssets(ctx, evidence); err != nil {
		return errors.Join(ErrD09RuntimeAssetsUnavailable, err)
	}
	return nil
}

// Close releases the retained source capability, not any D09 consumer mount.
// The launcher/cgroup owner must settle and remove that separate cell view.
func (s *VerifiedD09RuntimeAssetSource) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if s.exportedFD != nil && s.exportedFD.Fd() != ^uintptr(0) {
		s.mu.Unlock()
		return ErrD09RuntimeAssetExportOpen
	}
	s.closed = true
	assets := s.assets
	s.assets, s.exportedFD = nil, nil
	s.mu.Unlock()
	if assets != nil {
		return assets.Close()
	}
	return nil
}

func (a *VerifiedRuntimeAssets) verifyD09RuntimeSourceLocked() error {
	if a == nil || a.closed || a.root == nil {
		return ErrRuntimeAssets
	}
	info, err := a.root.Stat()
	if err != nil || !info.IsDir() {
		return errors.Join(ErrRuntimeAssets, err)
	}
	rootStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || rootStat.Uid != 0 {
		return errors.New("D09 runtime bundle source must be root-owned on the protected host filesystem")
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", a.root.Fd()))
	if err != nil || !filepath.IsAbs(path) || strings.HasSuffix(path, " (deleted)") {
		return errors.Join(ErrRuntimeAssets, err)
	}
	if err := verifyRootOwnedRuntimeTree(path); err != nil {
		return err
	}
	digest, err := piruntime.VerifyLinuxPiRuntimeBundle(a.root)
	if err != nil {
		return fmt.Errorf("runtime bundle source is not on a verified read-only host mount: %w", err)
	}
	if digest != a.digest || digest != a.spec.BundleDigest {
		return errors.New("runtime bundle host-source digest differs from the signed profile")
	}
	return nil
}

func verifyRootOwnedRuntimeTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.Join(ErrRuntimeAssets, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return errors.New("every D09 runtime bundle object must be root-owned")
		}
		if info.Mode().Perm()&0o022 != 0 {
			return errors.New("D09 runtime bundle object is group/other writable")
		}
		if !entry.IsDir() && !info.Mode().IsRegular() {
			return errors.New("D09 runtime bundle contains a non-regular object")
		}
		return nil
	})
}

// ExportPinnedRuntimeAssets returns a fresh descriptor for content-only
// fixture/development inspection. This name intentionally prevents accidental
// satisfaction of piruntime.VerifiedWorkerRuntimeAssets.
func (a *VerifiedRuntimeAssets) ExportPinnedRuntimeAssets(ctx context.Context) (*os.File, piruntime.WorkerRuntimeAssetEvidence, error) {
	if ctx == nil {
		return nil, piruntime.WorkerRuntimeAssetEvidence{}, ErrRuntimeAssets
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.root == nil {
		return nil, piruntime.WorkerRuntimeAssetEvidence{}, ErrRuntimeAssets
	}
	descriptor, err := duplicateFile(a.root, "sessionlaunch-runtime-asset-export")
	if err != nil {
		return nil, piruntime.WorkerRuntimeAssetEvidence{}, errors.Join(ErrRuntimeAssets, err)
	}
	evidence, err := a.evidenceFor(descriptor)
	if err != nil {
		_ = descriptor.Close()
		return nil, piruntime.WorkerRuntimeAssetEvidence{}, err
	}
	return descriptor, evidence, nil
}

// VerifyPinnedRuntimeAssets re-pins content and checks descriptor identity for
// fixtures/development; it proves neither an immutable host mount nor consumer
// mount state.
func (a *VerifiedRuntimeAssets) VerifyPinnedRuntimeAssets(ctx context.Context, evidence piruntime.WorkerRuntimeAssetEvidence) error {
	if ctx == nil {
		return ErrRuntimeAssets
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.root == nil {
		return ErrRuntimeAssets
	}
	if evidence.BundleDigest != a.digest || evidence.NamespaceProfileDigest != a.spec.NamespaceProfileDigest ||
		evidence.Device == 0 || evidence.Inode == 0 || evidence.MountID == 0 {
		return ErrRuntimeAssets
	}
	digest, err := runtimeAssetContentDigest(a.root, a.spec.MaxFiles, a.spec.MaxBytes)
	if err != nil || digest != a.digest {
		return errors.Join(ErrRuntimeAssets, err)
	}
	descriptor, err := duplicateFile(a.root, "sessionlaunch-runtime-asset-reverify")
	if err != nil {
		return errors.Join(ErrRuntimeAssets, err)
	}
	defer descriptor.Close()
	expected, err := a.evidenceFor(descriptor)
	if err != nil {
		return err
	}
	if expected.ViewIdentity != evidence.ViewIdentity || expected.Device != evidence.Device ||
		expected.Inode != evidence.Inode || expected.MountID != evidence.MountID {
		return ErrRuntimeAssets
	}
	return nil
}

// Close releases the held bundle descriptor. It does not unmount the consumer
// view; the launcher/settlement owner remains responsible for that.
func (a *VerifiedRuntimeAssets) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.root != nil {
		err := a.root.Close()
		a.root = nil
		return err
	}
	return nil
}

func (a *VerifiedRuntimeAssets) evidenceFor(descriptor *os.File) (piruntime.WorkerRuntimeAssetEvidence, error) {
	stat, err := statNode(descriptor)
	if err != nil || stat.mode&syscall.S_IFMT != syscall.S_IFDIR {
		return piruntime.WorkerRuntimeAssetEvidence{}, errors.Join(ErrRuntimeAssets, err)
	}
	mountID, err := descriptorMountID(descriptor.Fd())
	if err != nil {
		return piruntime.WorkerRuntimeAssetEvidence{}, errors.Join(ErrRuntimeAssets, err)
	}
	viewIdentity := DigestBytes([]byte(fmt.Sprintf("tbound/sessionlaunch/runtime-asset-view/v1\x00%s\x00%d:%d:%d",
		a.digest, stat.device, stat.inode, mountID)))
	return piruntime.WorkerRuntimeAssetEvidence{
		BundleDigest:           a.digest,
		ViewIdentity:           viewIdentity,
		NamespaceProfileDigest: a.spec.NamespaceProfileDigest,
		Device:                 stat.device,
		Inode:                  stat.inode,
		MountID:                mountID,
	}, nil
}

// runtimeAssetContentDigest computes the immutable content commitment of a
// bundle tree. The field encoding is byte-for-byte the same scheme as
// piruntime.VerifyLinuxPiRuntimeBundle so a digest produced here matches the
// digest the D09 consumer verifier recomputes. It additionally refuses
// group/other-writable files and any non-regular, symlinked, or cross-device
// (nested-mount) object.
func runtimeAssetContentDigest(root *os.File, maxFiles int, maxBytes int64) (string, error) {
	info, err := root.Stat()
	if err != nil || !info.IsDir() {
		return "", ErrRuntimeAssets
	}
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd()))
	if err != nil || !filepath.IsAbs(rootPath) || strings.HasSuffix(rootPath, " (deleted)") {
		return "", ErrRuntimeAssets
	}
	pathInfo, err := os.Lstat(rootPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) {
		return "", ErrRuntimeAssets
	}
	device, ok := statDevice(info)
	if !ok || device == 0 {
		return "", ErrRuntimeAssets
	}
	hash := sha256.New()
	count := 0
	total := int64(0)
	err = filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > maxFiles {
			return fmt.Errorf("%w: runtime-asset file budget exceeded", ErrRuntimeAssets)
		}
		entryInfo, err := os.Lstat(path)
		if err != nil || entryInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: runtime-asset tree contains a symlink or unreadable object", ErrRuntimeAssets)
		}
		current, ok := statDevice(entryInfo)
		if !ok || current != device {
			return fmt.Errorf("%w: runtime-asset tree contains a nested filesystem mount", ErrRuntimeAssets)
		}
		relative, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			_, _ = fmt.Fprintf(hash, "d\x00%s\x00%o\n", filepath.ToSlash(relative), entryInfo.Mode().Perm())
			return nil
		}
		if !entryInfo.Mode().IsRegular() || entryInfo.Size() < 0 ||
			entryInfo.Size() > maxRuntimeAssetSingleFileSize || total > maxBytes-entryInfo.Size() {
			return fmt.Errorf("%w: runtime-asset object is not a bounded regular file", ErrRuntimeAssets)
		}
		if entryInfo.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%w: runtime-asset file is group/other writable", ErrRuntimeAssets)
		}
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		before, err := file.Stat()
		if err != nil || !os.SameFile(entryInfo, before) {
			_ = file.Close()
			return fmt.Errorf("%w: runtime-asset file changed while opening", ErrRuntimeAssets)
		}
		contentHash := sha256.New()
		written, copyErr := io.Copy(contentHash, io.LimitReader(file, entryInfo.Size()+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || written != entryInfo.Size() {
			return errors.Join(copyErr, closeErr, ErrRuntimeAssets)
		}
		total += written
		_, _ = fmt.Fprintf(hash, "f\x00%s\x00%o\x00%d\x00%s\n",
			filepath.ToSlash(relative), entryInfo.Mode().Perm(), entryInfo.Size(), hex.EncodeToString(contentHash.Sum(nil)))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRuntimeAssets, err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
