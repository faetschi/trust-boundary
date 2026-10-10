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

// VerifiedRuntimeAssets is the sessionlaunch-owned implementation of
// piruntime.VerifiedWorkerRuntimeAssets for one content-pinned runtime bundle.
//
// Export returns a fresh descriptor for the held bundle source. Per the
// piruntime descriptor contract this source FD is Go-only: it is not proof that
// the child received a safe mount. The D09 launcher must reopen this source
// inside its controlled namespace and independently verify the consumer-side
// read-only mount (piruntime.VerifyLinuxPiRuntimeBundle) before it creates child
// FD 9. This type pins the bytes; it does not fabricate the consumer mount.
type VerifiedRuntimeAssets struct {
	mu     sync.Mutex
	root   *os.File
	spec   RuntimeAssetSpec
	digest string
	closed bool
}

// VerifiedRuntimeAssets must satisfy the launcher-owned descriptor interface
// exactly; the D09 consumer reopens and re-verifies this source before FD 9.
var _ piruntime.VerifiedWorkerRuntimeAssets = (*VerifiedRuntimeAssets)(nil)

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

// ExportVerifiedWorkerRuntimeAssets returns a fresh descriptor for the pinned
// bundle source and evidence bound to that descriptor.
func (a *VerifiedRuntimeAssets) ExportVerifiedWorkerRuntimeAssets(ctx context.Context) (*os.File, piruntime.WorkerRuntimeAssetEvidence, error) {
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

// VerifyWorkerRuntimeAssets re-pins the complete bundle content and re-checks
// the descriptor identity against the supplied evidence.
func (a *VerifiedRuntimeAssets) VerifyWorkerRuntimeAssets(ctx context.Context, evidence piruntime.WorkerRuntimeAssetEvidence) error {
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
