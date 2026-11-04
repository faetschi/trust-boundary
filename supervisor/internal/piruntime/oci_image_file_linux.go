//go:build linux

package piruntime

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
)

// ociLayerMaxDecompressed bounds the decompressed size of one layer; a variable so tests can use a
// small bound.
var ociLayerMaxDecompressed int64 = 8 << 30

const (
	ociImageFileMaxBytes = 128 << 20
	ociManifestMaxBytes  = 4 << 20
	ociMaxLayers         = 4096
	ociWhiteoutPrefix    = ".wh."
	ociOpaqueWhiteout    = ".wh..wh..opq"
)

// ociImageFile is the final content identity of one regular file in a layered
// OCI image.
type ociImageFile struct {
	digest string
	size   int64
	mode   int64
}

type ociLayerDescriptor struct {
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType"`
}

// ociLayerResult is what one layer does to the target path.
type ociLayerResult struct {
	file       *ociImageFile // a regular file at the target
	deleted    bool          // a whiteout hides what lower layers hold at the target
	unreadable string        // the layer makes the target unresolvable without following a link
	node       *ociImageNode // exact or implied node, for the product's component-by-component lookup
}

type ociImageNode struct {
	typeflag byte
	link     string
}

// readOCIImageFile resolves imagePath through the layers of the manifest named
// manifestDigest in the OCI layout at layoutPath, applying later-layer
// overrides and OCI whiteouts, and returns the digest of the regular file the
// merged image holds there. Every blob is verified against the digest its
// descriptor names while it is read; nothing is extracted to disk and no link
// in the image is followed. The layout must already be cosign-verified: this
// binds file bytes to that verified manifest, it does not authenticate it.
func readOCIImageFile(ctx context.Context, layoutPath, manifestDigest, imagePath string, requireImmutable bool) (ociImageFile, error) {
	if ctx == nil || !validDigest(manifestDigest) || !filepath.IsAbs(imagePath) || path.Clean(imagePath) != imagePath || imagePath == "/" {
		return ociImageFile{}, ErrHostProfileMismatch
	}
	target := strings.TrimPrefix(imagePath, "/")
	layers, err := ociManifestLayers(layoutPath, manifestDigest, requireImmutable)
	if err != nil {
		return ociImageFile{}, err
	}
	var current *ociImageFile
	reason := ""
	budget := PiRuntimeBundleMaxBytes
	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			return ociImageFile{}, err
		}
		result, size, err := scanOCILayer(filepath.Join(layoutPath, "blobs", "sha256", strings.TrimPrefix(layer.Digest, "sha256:")),
			layer, target, budget, requireImmutable)
		if err != nil {
			return ociImageFile{}, fmt.Errorf("scan OCI layer %s: %w", layer.Digest, err)
		}
		budget -= size
		switch {
		case result.file != nil:
			current, reason = result.file, ""
		case result.unreadable != "":
			current, reason = nil, result.unreadable
		case result.deleted:
			current, reason = nil, ""
		}
	}
	if reason != "" {
		return ociImageFile{}, errors.New(reason)
	}
	if current == nil {
		return ociImageFile{}, fmt.Errorf("%s is not present in the signed image", imagePath)
	}
	return *current, nil
}

func ociManifestLayers(layoutPath, manifestDigest string, requireImmutable bool) ([]ociLayerDescriptor, error) {
	indexRaw, err := readBoundedFile(filepath.Join(layoutPath, "index.json"), 1<<20, requireImmutable)
	if err != nil {
		return nil, fmt.Errorf("read OCI index: %w", err)
	}
	if err := protocol.ValidateStrictJSON(indexRaw); err != nil {
		return nil, fmt.Errorf("OCI index is not strict JSON: %w", err)
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		return nil, fmt.Errorf("decode OCI index: %w", err)
	}
	listed := false
	for _, manifest := range index.Manifests {
		if strings.EqualFold(strings.TrimSpace(manifest.Digest), manifestDigest) {
			listed = true
		}
	}
	if !listed {
		return nil, fmt.Errorf("local OCI layout does not contain the frozen manifest %s", manifestDigest)
	}
	blob, err := readBoundedFile(filepath.Join(layoutPath, "blobs", "sha256", strings.TrimPrefix(manifestDigest, "sha256:")),
		ociManifestMaxBytes, requireImmutable)
	if err != nil {
		return nil, fmt.Errorf("read OCI manifest blob: %w", err)
	}
	if DigestBytes(blob) != manifestDigest {
		return nil, fmt.Errorf("local OCI manifest blob does not hash to %s", manifestDigest)
	}
	if err := protocol.ValidateStrictJSON(blob); err != nil {
		return nil, fmt.Errorf("OCI image manifest is not strict JSON: %w", err)
	}
	var manifest struct {
		Layers []ociLayerDescriptor `json:"layers"`
	}
	if err := json.Unmarshal(blob, &manifest); err != nil || len(manifest.Layers) == 0 || len(manifest.Layers) > ociMaxLayers {
		return nil, errors.Join(errors.New("OCI image manifest is invalid or outside the layer bound"), err)
	}
	for _, layer := range manifest.Layers {
		if !validDigest(layer.Digest) {
			return nil, errors.New("OCI layer descriptor has an invalid digest")
		}
	}
	return manifest.Layers, nil
}

// digestingReader hashes everything read through it.
type digestingReader struct {
	reader io.Reader
	hash   hash.Hash
	count  int64
}

func (d *digestingReader) Read(p []byte) (int, error) {
	n, err := d.reader.Read(p)
	d.hash.Write(p[:n])
	d.count += int64(n)
	return n, err
}

// countingReader counts the bytes read through it.
type countingReader struct {
	reader io.Reader
	count  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.count += int64(n)
	return n, err
}

// scanOCILayer reads a whole layer blob, verifies its digest, and reports what
// the layer does to target (a path relative to the image root).
func scanOCILayer(blobPath string, layer ociLayerDescriptor, target string, maximum int64, requireImmutable bool) (ociLayerResult, int64, error) {
	var result ociLayerResult
	size, err := readOCILayer(blobPath, layer, maximum, requireImmutable, func(content io.Reader) error {
		return scanTarForTarget(content, target, &result)
	})
	if err != nil {
		return ociLayerResult{}, 0, err
	}
	return result, size, nil
}

// readOCILayer bounds and verifies the blob independently of its tar consumer.
func readOCILayer(blobPath string, layer ociLayerDescriptor, maximum int64, requireImmutable bool, scan func(io.Reader) error) (int64, error) {
	info, err := os.Lstat(blobPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return 0, errors.Join(ErrD09TemplateInvalid, err)
	}
	if requireImmutable {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return 0, errors.New("OCI blob must be root-owned and non-writable")
		}
	}
	fd, err := syscall.Open(blobPath, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	file := os.NewFile(uintptr(fd), blobPath)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return 0, errors.New("OCI blob changed while opening")
	}
	raw := &digestingReader{reader: io.LimitReader(file, info.Size()+1), hash: sha256.New()}
	buffered := bufio.NewReader(raw)
	magic, _ := buffered.Peek(4)
	var content io.Reader = buffered
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		zr, err := gzip.NewReader(buffered)
		if err != nil {
			return 0, err
		}
		defer zr.Close()
		content = zr
	case len(magic) == 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		return 0, errors.New("zstd-compressed OCI layers are not supported")
	}
	// Read through the decompressor with one byte of slack past the bound. io.LimitReader at the bound
	// would report EOF there, and a tar parser that stops at an entry boundary cannot tell that from
	// the end of the archive: entries after the bound (an override, a whiteout) would go unseen while
	// the compressed blob still hashes correctly.
	counted := &countingReader{reader: io.LimitReader(content, ociLayerMaxDecompressed+1)}
	if err := scan(counted); err != nil {
		return 0, err
	}
	// The archive ended (end blocks or EOF). Consume the rest of the decompressed stream, which also
	// makes the gzip reader verify its checksum, and refuse a layer that is larger than the bound.
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return 0, err
	}
	if counted.count > ociLayerMaxDecompressed {
		return 0, fmt.Errorf("OCI layer decompresses to more than %d bytes", ociLayerMaxDecompressed)
	}
	if _, err := io.Copy(io.Discard, buffered); err != nil {
		return 0, err
	}
	if raw.count != info.Size() || "sha256:"+hex.EncodeToString(raw.hash.Sum(nil)) != layer.Digest {
		return 0, errors.New("OCI layer content digest mismatch")
	}
	return info.Size(), nil
}

func scanTarForTarget(content io.Reader, target string, result *ociLayerResult) error {
	reader := tar.NewReader(content)
	for {
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
		if name == target {
			result.node = &ociImageNode{typeflag: header.Typeflag, link: header.Linkname}
		} else if result.node == nil && strings.HasPrefix(name, target+"/") {
			// Tar layers may omit directory headers for the parents of their entries.
			result.node = &ociImageNode{typeflag: tar.TypeDir}
		}
		directory, base := path.Dir(name), path.Base(name)
		if directory == "." {
			directory = ""
		}
		switch {
		case base == ociOpaqueWhiteout:
			if directory == "" || strings.HasPrefix(target, directory+"/") {
				result.deleted = true
			}
		case strings.HasPrefix(base, ociWhiteoutPrefix):
			victim := path.Join(directory, strings.TrimPrefix(base, ociWhiteoutPrefix))
			if target == victim || strings.HasPrefix(target, victim+"/") {
				result.deleted = true
			}
		case name == target:
			if header.Typeflag != tar.TypeReg {
				result.file = nil
				result.unreadable = fmt.Sprintf("/%s is not a regular file in the signed image (tar type %q)", target, header.Typeflag)
				continue
			}
			if header.Size < 0 || header.Size > ociImageFileMaxBytes {
				return errors.New("in-image file is outside the size bound")
			}
			digest := sha256.New()
			n, err := io.Copy(digest, io.LimitReader(reader, header.Size+1))
			if err != nil || n != header.Size {
				return errors.Join(errors.New("in-image file read was incomplete"), err)
			}
			result.file = &ociImageFile{digest: "sha256:" + hex.EncodeToString(digest.Sum(nil)), size: header.Size, mode: header.Mode}
			result.unreadable = ""
		case strings.HasPrefix(target, name+"/") && header.Typeflag != tar.TypeDir:
			result.file = nil
			result.unreadable = fmt.Sprintf("/%s is a link or file where a directory is needed to reach the path", name)
		}
	}
}
