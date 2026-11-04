//go:build linux

package piruntime

import (
	"archive/tar"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// readProductOCIImageFile follows at most eight ancestor symlinks in the merged
// signed image. The final component remains a regular file, matching the cell's Lstat.
func readProductOCIImageFile(ctx context.Context, layoutPath, manifestDigest, imagePath string, requireImmutable bool) (ociImageFile, error) {
	if ctx == nil || !validDigest(manifestDigest) || !path.IsAbs(imagePath) || path.Clean(imagePath) != imagePath || imagePath == "/" {
		return ociImageFile{}, ErrHostProfileMismatch
	}
	layers, err := ociManifestLayers(layoutPath, manifestDigest, requireImmutable)
	if err != nil {
		return ociImageFile{}, err
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
