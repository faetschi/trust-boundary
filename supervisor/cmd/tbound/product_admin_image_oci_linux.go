//go:build linux

package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/piruntime"
)

const productImageMaxBytes int64 = 2 << 30
const productImageMaxFiles = 1024

type productOCIBlob struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}
type productOCIFile struct {
	digest string
	size   int64
	raw    []byte
}

// productImageProtectedPath checks every ancestor, including the test root. Live
// inputs require root ownership: the service account cannot substitute release bytes.
func productImageProtectedPath(root, name string, directory bool) error {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return errors.New("image input must be an absolute clean path")
	}
	boundary := filepath.Clean(root)
	if boundary == "." {
		boundary = "/"
	}
	if boundary != "/" && !productPathInside(name, boundary) {
		return errors.New("image input escapes the test root")
	}
	owner := uint32(0)
	if boundary != "/" {
		owner = uint32(os.Geteuid())
	}
	current := name
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != owner || info.Mode().Perm()&0022 != 0 || info.Mode()&os.ModeSymlink != 0 ||
			((current != name || directory) && !info.IsDir()) || (current == name && !directory && (!info.Mode().IsRegular() || st.Nlink != 1)) {
			return fmt.Errorf("%s must be root-protected, non-symlink and not group/world writable", current)
		}
		if current == boundary {
			break
		}
		next := filepath.Dir(current)
		if next == current {
			return errors.New("image input has no protected root")
		}
		current = next
	}
	return nil
}

func productImageRead(root, name string, limit int64) ([]byte, error) {
	if err := productImageProtectedPath(root, name, false); err != nil {
		return nil, err
	}
	file, err := readProtectedHostFile(name, limit, []uint32{0, uint32(os.Geteuid())})
	return file.Raw, err
}

func productOCIName(name string) bool {
	if name == "index.json" || name == "oci-layout" {
		return true
	}
	return strings.HasPrefix(name, "blobs/sha256/") && piruntime.ValidD09Digest("sha256:"+strings.TrimPrefix(name, "blobs/sha256/"))
}

func productOCIHash(r io.Reader, size int64, keep bool) (productOCIFile, error) {
	if size < 0 || size > productImageMaxBytes {
		return productOCIFile{}, errors.New("OCI file exceeds image bound")
	}
	h := sha256.New()
	var raw []byte
	var n int64
	var err error
	if keep {
		if size > 1<<20 {
			return productOCIFile{}, errors.New("OCI metadata exceeds 1 MiB")
		}
		raw, err = io.ReadAll(io.LimitReader(r, size+1))
		n = int64(len(raw))
		h.Write(raw)
	} else {
		n, err = io.Copy(h, io.LimitReader(r, size+1))
	}
	if err != nil || n != size {
		return productOCIFile{}, errors.Join(errors.New("OCI size mismatch"), err)
	}
	return productOCIFile{digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), size: n, raw: raw}, nil
}

// Only OCI metadata and content-addressed regular blobs are accepted. Nothing is
// extracted from an archive; load receives the same protected, measured archive.
func productOCIArchive(root, name string) (map[string]productOCIFile, string, error) {
	if err := productImageProtectedPath(root, name, false); err != nil {
		return nil, "", err
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > productImageMaxBytes {
		return nil, "", errors.New("OCI archive exceeds 2 GiB")
	}
	archiveHash := sha256.New()
	stream := io.TeeReader(io.LimitReader(f, productImageMaxBytes+1), archiveHash)
	tr := tar.NewReader(stream)
	files := map[string]productOCIFile{}
	var total int64
	for count := 0; ; count++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", err
		}
		if count >= productImageMaxFiles {
			return nil, "", errors.New("too many OCI archive entries")
		}
		name := hdr.Name
		if hdr.Typeflag == tar.TypeDir {
			if name != "blobs/" && name != "blobs/sha256/" && name != "blobs" && name != "blobs/sha256" {
				return nil, "", errors.New("unexpected OCI directory")
			}
			continue
		}
		if (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA) || !productOCIName(name) || path.Clean(name) != name {
			return nil, "", errors.New("OCI archive contains an unexpected path or non-regular entry")
		}
		if _, dup := files[name]; dup {
			return nil, "", errors.New("duplicate OCI archive entry")
		}
		total += hdr.Size
		if total > productImageMaxBytes {
			return nil, "", errors.New("OCI archive data exceeds 2 GiB")
		}
		value, err := productOCIHash(tr, hdr.Size, name == "index.json" || name == "oci-layout")
		if err != nil {
			return nil, "", err
		}
		if strings.HasPrefix(name, "blobs/") && value.digest != "sha256:"+filepath.Base(name) {
			return nil, "", errors.New("OCI archive blob digest differs from its name")
		}
		files[name] = value
	}
	// Include tar padding in the transfer checksum and refuse hidden trailing data.
	rest, err := io.ReadAll(io.LimitReader(stream, 1<<20))
	if err != nil {
		return nil, "", err
	}
	for _, b := range rest {
		if b != 0 {
			return nil, "", errors.New("unexpected bytes after OCI archive")
		}
	}
	if len(rest) >= 1<<20 {
		return nil, "", errors.New("excessive OCI archive padding")
	}
	return files, "sha256:" + hex.EncodeToString(archiveHash.Sum(nil)), nil
}

func productOCILayout(root, dir string) (map[string]productOCIFile, error) {
	if err := productImageProtectedPath(root, dir, true); err != nil {
		return nil, err
	}
	files := map[string]productOCIFile{}
	var total int64
	count := 0
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == dir {
			return nil
		}
		count++
		if count > productImageMaxFiles {
			return errors.New("too many signed OCI files")
		}
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "blobs" && rel != "blobs/sha256" {
				return errors.New("unexpected signed OCI directory")
			}
			return productImageProtectedPath(root, name, true)
		}
		if !productOCIName(rel) {
			return errors.New("unexpected signed OCI file")
		}
		if err := productImageProtectedPath(root, name, false); err != nil {
			return err
		}
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > productImageMaxBytes {
			return errors.New("signed OCI input exceeds 2 GiB")
		}
		// Metadata blobs can also be layer blobs, so keep only small blobs in memory.
		value, err := productOCIHash(f, info.Size(), info.Size() <= 1<<20)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "blobs/") && value.digest != "sha256:"+filepath.Base(rel) {
			return errors.New("signed OCI blob hash differs from its name")
		}
		files[rel] = value
		return nil
	})
	return files, err
}

func productOCIIdentity(files map[string]productOCIFile, expected string) (string, error) {
	var layout struct {
		Version string `json:"imageLayoutVersion"`
	}
	if err := json.Unmarshal(files["oci-layout"].raw, &layout); err != nil || layout.Version != "1.0.0" {
		return "", errors.New("missing OCI layout version")
	}
	index := files["index.json"].raw
	if err := protocol.ValidateStrictJSON(index); err != nil {
		return "", err
	}
	var idx struct {
		SchemaVersion int              `json:"schemaVersion"`
		Manifests     []productOCIBlob `json:"manifests"`
	}
	if err := json.Unmarshal(index, &idx); err != nil || idx.SchemaVersion != 2 {
		return "", errors.New("invalid OCI index")
	}
	var descriptor *productOCIBlob
	for i := range idx.Manifests {
		if idx.Manifests[i].Digest == expected {
			if descriptor != nil {
				return "", errors.New("duplicate image in OCI index")
			}
			descriptor = &idx.Manifests[i]
		}
	}
	if descriptor == nil || descriptor.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		return "", errors.New("OCI index does not name the signed image manifest")
	}
	blob, ok := files["blobs/sha256/"+strings.TrimPrefix(expected, "sha256:")]
	if !ok || blob.digest != expected || blob.size != descriptor.Size || len(blob.raw) == 0 {
		return "", errors.New("OCI manifest bytes differ from the signed digest")
	}
	if err := protocol.ValidateStrictJSON(blob.raw); err != nil {
		return "", err
	}
	var manifest struct {
		SchemaVersion int              `json:"schemaVersion"`
		Config        productOCIBlob   `json:"config"`
		Layers        []productOCIBlob `json:"layers"`
	}
	if err := json.Unmarshal(blob.raw, &manifest); err != nil || manifest.SchemaVersion != 2 || manifest.Config.MediaType != "application/vnd.oci.image.config.v1+json" {
		return "", errors.New("invalid OCI image manifest")
	}
	for _, d := range append([]productOCIBlob{manifest.Config}, manifest.Layers...) {
		if !piruntime.ValidD09Digest(d.Digest) || d.Size < 0 {
			return "", errors.New("invalid OCI descriptor")
		}
		f, ok := files["blobs/sha256/"+strings.TrimPrefix(d.Digest, "sha256:")]
		if !ok || f.digest != d.Digest || f.size != d.Size {
			return "", errors.New("OCI descriptor does not match the measured blob")
		}
	}
	return manifest.Config.Digest, nil
}

// Archive manifests are read through a separate bounded pass after hashing every
// member; large layers never need to be retained in memory.
func productOCIArchiveMetadata(name string, files map[string]productOCIFile, expected string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	wanted := "blobs/sha256/" + strings.TrimPrefix(expected, "sha256:")
	for {
		hdr, err := tr.Next()
		if err != nil {
			return err
		}
		if hdr.Name == wanted {
			value, err := productOCIHash(tr, hdr.Size, true)
			if err != nil {
				return err
			}
			if value.digest != expected {
				return errors.New("archive manifest changed")
			}
			files[wanted] = value
			return nil
		}
	}
}
