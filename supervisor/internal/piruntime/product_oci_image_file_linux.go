//go:build linux

package piruntime

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// productOCILayerReads counts layer blob reads by the product reader, so tests can prove the memo avoids rescans.
var productOCILayerReads atomic.Int64

var productOCIMemo struct {
	sync.Mutex
	key   string
	files map[string]ociImageFile // by image path; each was verified under key
}

// readProductOCIImageFile resolves one file; see readProductOCIImageFiles.
func readProductOCIImageFile(ctx context.Context, layoutPath, manifestDigest, imagePath string, requireImmutable bool) (ociImageFile, error) {
	files, err := readProductOCIImageFiles(ctx, layoutPath, manifestDigest, []string{imagePath}, requireImmutable)
	if err != nil {
		return ociImageFile{}, err
	}
	return files[0], nil
}

// readProductOCIImageFiles follows at most eight ancestor symlinks per path in the merged signed image. The final
// component remains a regular file, matching the cell's Lstat. All paths share one redirected-write pass and one
// per-component lookup cache. A successful result is memoized per path while every layout file identity is unchanged.
func readProductOCIImageFiles(ctx context.Context, layoutPath, manifestDigest string, imagePaths []string, requireImmutable bool) ([]ociImageFile, error) {
	files, fileErrs, err := readProductOCIImageFilesEach(ctx, layoutPath, manifestDigest, imagePaths, requireImmutable)
	if err != nil {
		return nil, err
	}
	for i, fileErr := range fileErrs {
		if fileErr != nil {
			return nil, fmt.Errorf("%s: %w", imagePaths[i], fileErr)
		}
	}
	return files, nil
}

// readProductOCIImageFilesEach is readProductOCIImageFiles with one resolution error per path (files[i] is valid where
// fileErrs[i] is nil); err fails the whole request. Only a fully successful result is memoized.
func readProductOCIImageFilesEach(ctx context.Context, layoutPath, manifestDigest string, imagePaths []string, requireImmutable bool) (files []ociImageFile, fileErrs []error, err error) {
	if ctx == nil || !validDigest(manifestDigest) || len(imagePaths) == 0 {
		return nil, nil, ErrHostProfileMismatch
	}
	for _, imagePath := range imagePaths {
		if !path.IsAbs(imagePath) || path.Clean(imagePath) != imagePath || imagePath == "/" {
			return nil, nil, ErrHostProfileMismatch
		}
	}
	layers, err := ociManifestLayers(layoutPath, manifestDigest, requireImmutable)
	if err != nil {
		return nil, nil, err
	}
	key, identified := productOCIMemoKey(layoutPath, manifestDigest, requireImmutable, layers)
	if identified {
		productOCIMemo.Lock()
		if productOCIMemo.key == key {
			cached, hit := make([]ociImageFile, len(imagePaths)), true
			for i, imagePath := range imagePaths {
				var known bool
				cached[i], known = productOCIMemo.files[imagePath]
				hit = hit && known
			}
			if hit {
				productOCIMemo.Unlock()
				return cached, make([]error, len(cached)), nil
			}
		}
		productOCIMemo.Unlock()
	}
	hardlinkTargets, err := refuseProductOCIRedirectedWrites(ctx, layoutPath, layers, requireImmutable)
	if err != nil {
		return nil, nil, err
	}
	cache := map[string]ociLayerResult{}
	lookup := func(target string) (ociLayerResult, error) {
		if result, ok := cache[target]; ok {
			return result, nil
		}
		var current ociLayerResult
		budget := PiRuntimeBundleMaxBytes
		for _, layer := range layers {
			if err := ctx.Err(); err != nil {
				return current, err
			}
			productOCILayerReads.Add(1)
			result, size, err := scanOCILayer(filepath.Join(layoutPath, "blobs", "sha256", strings.TrimPrefix(layer.Digest, "sha256:")),
				layer, target, budget, requireImmutable)
			if err != nil {
				return current, fmt.Errorf("scan OCI layer %s: %w", layer.Digest, err)
			}
			budget -= size
			if result.node != nil {
				current = result
			} else if result.deleted || result.unreadable != "" {
				current = ociLayerResult{}
			}
		}
		cache[target] = current
		return current, nil
	}
	files, fileErrs = make([]ociImageFile, len(imagePaths)), make([]error, len(imagePaths))
	failed := false
	for i, imagePath := range imagePaths {
		files[i], fileErrs[i] = resolveProductOCIImageFile(ctx, imagePath, lookup, hardlinkTargets)
		failed = failed || fileErrs[i] != nil
	}
	// Identities taken before and after the reads must agree, or the result is not memoized.
	if after, ok := productOCIMemoKey(layoutPath, manifestDigest, requireImmutable, layers); !failed && identified && ok && after == key {
		productOCIMemo.Lock()
		if productOCIMemo.key != key || productOCIMemo.files == nil {
			productOCIMemo.key, productOCIMemo.files = key, map[string]ociImageFile{}
		}
		for i, imagePath := range imagePaths {
			productOCIMemo.files[imagePath] = files[i]
		}
		productOCIMemo.Unlock()
	}
	return files, fileErrs, nil
}

// productOCIMemoKey names everything the verification read: the layout, digest and mode plus the identity (device, inode, size, times,
// mode, owner) of the layout directories, index.json, the manifest blob and every layer blob. ok is false if any is unreadable.
func productOCIMemoKey(layoutPath, manifestDigest string, requireImmutable bool, layers []ociLayerDescriptor) (string, bool) {
	var key strings.Builder
	fmt.Fprintf(&key, "%s\x00%s\x00%v", layoutPath, manifestDigest, requireImmutable)
	blobs := filepath.Join(layoutPath, "blobs", "sha256")
	names := []string{layoutPath, filepath.Join(layoutPath, "blobs"), blobs, filepath.Join(layoutPath, "index.json"),
		filepath.Join(blobs, strings.TrimPrefix(manifestDigest, "sha256:"))}
	for _, layer := range layers {
		names = append(names, filepath.Join(blobs, strings.TrimPrefix(layer.Digest, "sha256:")))
	}
	for _, name := range names {
		identity, ok := productFileIdentity(name)
		if !ok {
			return "", false
		}
		key.WriteString("\x00" + identity)
	}
	return key.String(), true
}

// productFileIdentity names a file by device, inode, size, mtime, ctime, mode and owner, without following a final symlink.
func productFileIdentity(name string) (string, bool) {
	info, err := os.Lstat(name)
	if err != nil {
		return "", false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s:%d:%d:%d:%d:%d:%d:%d:%d:%d", name, stat.Dev, stat.Ino, stat.Size, stat.Mtim.Sec, stat.Mtim.Nsec,
		stat.Ctim.Sec, stat.Ctim.Nsec, stat.Mode, stat.Uid), true
}

func resolveProductOCIImageFile(ctx context.Context, imagePath string, lookup func(string) (ociLayerResult, error), hardlinkTargets map[string]bool) (ociImageFile, error) {
	var resolved []string
	pending := strings.Split(strings.TrimPrefix(imagePath, "/"), "/")
	hops := 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return ociImageFile{}, err
		}
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			continue
		}
		target := path.Join(strings.Join(resolved, "/"), component)
		result, err := lookup(target)
		if err != nil {
			return ociImageFile{}, err
		}
		if result.node == nil {
			return ociImageFile{}, fmt.Errorf("/%s is not present in the signed image", target)
		}
		if len(pending) == 0 {
			if result.node.typeflag != tar.TypeReg || result.file == nil {
				return ociImageFile{}, fmt.Errorf("/%s is not a regular file in the signed image", target)
			}
			// Hardlink headers can chmod/chown the shared inode during extraction.
			if hardlinkTargets[target] {
				return ociImageFile{}, fmt.Errorf("/%s is a hardlink source in the signed image", target)
			}
			return *result.file, nil
		}
		switch result.node.typeflag {
		case tar.TypeSymlink:
			if result.node.link == "" {
				return ociImageFile{}, fmt.Errorf("/%s has an empty symlink target in the signed image", target)
			}
			if hops == 8 {
				return ociImageFile{}, fmt.Errorf("%s exceeds the in-image symlink hop limit (8)", imagePath)
			}
			hops++
			if path.IsAbs(result.node.link) {
				resolved = nil
			}
			// Resolve components before '..': a preceding component may itself be a link.
			pending = append(strings.Split(result.node.link, "/"), pending...)
		case tar.TypeDir:
			resolved = append(resolved, component)
		default:
			return ociImageFile{}, fmt.Errorf("/%s is not a directory in the signed image", target)
		}
	}
	return ociImageFile{}, fmt.Errorf("%s is not a regular file in the signed image", imagePath)
}

func refuseProductOCIRedirectedWrites(ctx context.Context, layoutPath string, layers []ociLayerDescriptor, requireImmutable bool) (map[string]bool, error) {
	// shortcut: retain whited-out symlinks until a directory header replaces them; refine if valid images need it.
	symlinks := map[string]bool{}
	hardlinkTargets := map[string]bool{}
	checkAncestors := func(name string) error {
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			if symlinks[directory] {
				return fmt.Errorf("OCI layer path /%s has symlink ancestor /%s", name, directory)
			}
		}
		return nil
	}
	scan := func(content io.Reader) error {
		reader := tar.NewReader(content)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			name := strings.TrimPrefix(path.Clean("/"+header.Name), "/")
			if name == "" {
				continue
			}
			if err := checkAncestors(name); err != nil {
				return err
			}
			if header.Typeflag == tar.TypeLink {
				target := strings.TrimPrefix(path.Clean("/"+header.Linkname), "/")
				if err := checkAncestors(target); err != nil {
					return err
				}
				if symlinks[target] {
					return fmt.Errorf("OCI hardlink source /%s is a symlink", target)
				}
				hardlinkTargets[target] = true
			}
			switch header.Typeflag {
			case tar.TypeSymlink:
				symlinks[name] = true
			case tar.TypeDir:
				delete(symlinks, name)
			}
		}
	}
	budget := PiRuntimeBundleMaxBytes
	for _, layer := range layers {
		productOCILayerReads.Add(1)
		size, err := readOCILayer(filepath.Join(layoutPath, "blobs", "sha256", strings.TrimPrefix(layer.Digest, "sha256:")),
			layer, budget, requireImmutable, scan)
		if err != nil {
			return nil, fmt.Errorf("validate OCI layer %s: %w", layer.Digest, err)
		}
		budget -= size
	}
	return hardlinkTargets, nil
}
