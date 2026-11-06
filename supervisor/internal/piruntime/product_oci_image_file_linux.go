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
	files []ociImageFile
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
// per-component lookup cache. A successful result is memoized while every layout file identity is unchanged.
func readProductOCIImageFiles(ctx context.Context, layoutPath, manifestDigest string, imagePaths []string, requireImmutable bool) ([]ociImageFile, error) {
	if ctx == nil || !validDigest(manifestDigest) || len(imagePaths) == 0 {
		return nil, ErrHostProfileMismatch
	}
	for _, imagePath := range imagePaths {
		if !path.IsAbs(imagePath) || path.Clean(imagePath) != imagePath || imagePath == "/" {
			return nil, ErrHostProfileMismatch
		}
	}
	layers, err := ociManifestLayers(layoutPath, manifestDigest, requireImmutable)
	if err != nil {
		return nil, err
	}
	key, identified := productOCIMemoKey(layoutPath, manifestDigest, imagePaths, requireImmutable, layers)
	if identified {
		productOCIMemo.Lock()
		if productOCIMemo.key == key && productOCIMemo.files != nil {
			files := append([]ociImageFile(nil), productOCIMemo.files...)
			productOCIMemo.Unlock()
			return files, nil
		}
		productOCIMemo.Unlock()
	}
	hardlinkTargets, err := refuseProductOCIRedirectedWrites(ctx, layoutPath, layers, requireImmutable)
	if err != nil {
		return nil, err
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
	files := make([]ociImageFile, 0, len(imagePaths))
	for _, imagePath := range imagePaths {
		file, err := resolveProductOCIImageFile(ctx, imagePath, lookup, hardlinkTargets)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	// Identities taken before and after the reads must agree, or the result is not memoized.
	if after, ok := productOCIMemoKey(layoutPath, manifestDigest, imagePaths, requireImmutable, layers); identified && ok && after == key {
		productOCIMemo.Lock()
		productOCIMemo.key, productOCIMemo.files = key, append([]ociImageFile(nil), files...)
		productOCIMemo.Unlock()
	}
	return files, nil
}

// productOCIMemoKey names everything the verification read: the request plus the identity (device, inode, size, times,
// mode, owner) of the layout directories, index.json, the manifest blob and every layer blob. ok is false if any is unreadable.
func productOCIMemoKey(layoutPath, manifestDigest string, imagePaths []string, requireImmutable bool, layers []ociLayerDescriptor) (string, bool) {
	var key strings.Builder
	fmt.Fprintf(&key, "%s\x00%s\x00%v\x00%q", layoutPath, manifestDigest, requireImmutable, imagePaths)
	blobs := filepath.Join(layoutPath, "blobs", "sha256")
	names := []string{layoutPath, filepath.Join(layoutPath, "blobs"), blobs, filepath.Join(layoutPath, "index.json"),
		filepath.Join(blobs, strings.TrimPrefix(manifestDigest, "sha256:"))}
	for _, layer := range layers {
		names = append(names, filepath.Join(blobs, strings.TrimPrefix(layer.Digest, "sha256:")))
	}
	for _, name := range names {
		info, err := os.Lstat(name)
		if err != nil {
			return "", false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return "", false
		}
		fmt.Fprintf(&key, "\x00%s:%d:%d:%d:%d:%d:%d:%d:%d:%d", name, stat.Dev, stat.Ino, stat.Size, stat.Mtim.Sec, stat.Mtim.Nsec,
			stat.Ctim.Sec, stat.Ctim.Nsec, stat.Mode, stat.Uid)
	}
	return key.String(), true
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
