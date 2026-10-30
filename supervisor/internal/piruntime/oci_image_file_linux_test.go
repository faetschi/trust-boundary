//go:build linux

package piruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type ociTestEntry struct {
	name     string
	typeflag byte
	mode     int64
	content  string
	link     string
}

func regEntry(name, content string, mode int64) ociTestEntry {
	return ociTestEntry{name: name, typeflag: tar.TypeReg, mode: mode, content: content}
}

func whiteout(name string) ociTestEntry { return ociTestEntry{name: name, typeflag: tar.TypeReg} }

func ociTestLayer(t *testing.T, compress bool, entries ...ociTestEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Mode: entry.mode, Size: int64(len(entry.content)), Linkname: entry.link}
		if entry.typeflag == tar.TypeDir || entry.typeflag == tar.TypeSymlink {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := writer.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !compress {
		return buffer.Bytes()
	}
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	if _, err := zw.Write(buffer.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return zipped.Bytes()
}

// ociTestLayout writes a signed-layout stand-in (index, manifest, config, layers)
// and returns its path and the manifest digest.
func ociTestLayout(t *testing.T, layers ...[]byte) (string, string) {
	t.Helper()
	layout := t.TempDir()
	blobs := filepath.Join(layout, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o700); err != nil {
		t.Fatal(err)
	}
	put := func(data []byte) string {
		digest := DigestBytes(data)
		if err := os.WriteFile(filepath.Join(blobs, strings.TrimPrefix(digest, "sha256:")), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return digest
	}
	type descriptor struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int    `json:"size"`
	}
	var manifest struct {
		SchemaVersion int          `json:"schemaVersion"`
		Config        descriptor   `json:"config"`
		Layers        []descriptor `json:"layers"`
	}
	manifest.SchemaVersion = 2
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	manifest.Config = descriptor{"application/vnd.oci.image.config.v1+json", put(config), len(config)}
	for _, layer := range layers {
		manifest.Layers = append(manifest.Layers, descriptor{"application/vnd.oci.image.layer.v1.tar", put(layer), len(layer)})
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := put(encoded)
	if err := os.WriteFile(filepath.Join(layout, "index.json"), []byte(`{"manifests":[{"digest":"`+digest+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return layout, digest
}

func TestReadOCIImageFileAppliesLayerOverridesAndWhiteouts(t *testing.T) {
	const node = "usr/local/bin/node"
	base := ociTestLayer(t, false,
		ociTestEntry{name: "usr/", typeflag: tar.TypeDir, mode: 0o755},
		ociTestEntry{name: "usr/local/bin/", typeflag: tar.TypeDir, mode: 0o755},
		regEntry(node, "node v1", 0o755), regEntry("usr/local/bin/old", "old", 0o755),
		regEntry("usr/local/lib/keep", "keep", 0o644), regEntry("opt/gone/x", "x", 0o755))
	upper := ociTestLayer(t, true,
		regEntry("./"+node, "node v2", 0o755),
		whiteout("usr/local/bin/.wh.old"),
		whiteout("opt/.wh..wh..opq"))
	layout, manifest := ociTestLayout(t, base, upper)
	ctx := context.Background()

	got, err := readOCIImageFile(ctx, layout, manifest, "/"+node, false)
	if err != nil || got.digest != DigestBytes([]byte("node v2")) || got.mode != 0o755 {
		t.Fatalf("overridden file = %+v, %v; want the upper layer's bytes", got, err)
	}
	if got, err := readOCIImageFile(ctx, layout, manifest, "/usr/local/lib/keep", false); err != nil || got.digest != DigestBytes([]byte("keep")) {
		t.Fatalf("file only in the lower layer = %+v, %v", got, err)
	}
	for _, hidden := range []string{"/usr/local/bin/old", "/opt/gone/x", "/usr/local/bin/never"} {
		if got, err := readOCIImageFile(ctx, layout, manifest, hidden, false); err == nil {
			t.Fatalf("%s resolved to %+v although a whiteout hides it or it never existed", hidden, got)
		}
	}
}

func TestReadOCIImageFileKeepsAFileRecreatedAboveAWhiteout(t *testing.T) {
	base := ociTestLayer(t, false, regEntry("bin/tool", "v1", 0o755))
	deleted := ociTestLayer(t, false, whiteout(".wh.bin"))
	recreated := ociTestLayer(t, true, regEntry("bin/tool", "v3", 0o755))
	layout, manifest := ociTestLayout(t, base, deleted, recreated)
	got, err := readOCIImageFile(context.Background(), layout, manifest, "/bin/tool", false)
	if err != nil || got.digest != DigestBytes([]byte("v3")) {
		t.Fatalf("file recreated above a whiteout = %+v, %v", got, err)
	}
	layout, manifest = ociTestLayout(t, base, deleted)
	if got, err := readOCIImageFile(context.Background(), layout, manifest, "/bin/tool", false); err == nil {
		t.Fatalf("whited-out directory still resolved: %+v", got)
	}
}

func TestReadOCIImageFileRefusesLinksAndTamperedBlobs(t *testing.T) {
	ctx := context.Background()
	symlink := ociTestLayer(t, false, ociTestEntry{name: "usr/local/bin/node", typeflag: tar.TypeSymlink, link: "/elsewhere"})
	layout, manifest := ociTestLayout(t, symlink)
	if _, err := readOCIImageFile(ctx, layout, manifest, "/usr/local/bin/node", false); err == nil {
		t.Fatal("a symlink was accepted as the in-image file")
	}
	ancestor := ociTestLayer(t, false, ociTestEntry{name: "usr/local", typeflag: tar.TypeSymlink, link: "/elsewhere"}, regEntry("elsewhere/bin/node", "x", 0o755))
	layout, manifest = ociTestLayout(t, ancestor)
	if _, err := readOCIImageFile(ctx, layout, manifest, "/usr/local/bin/node", false); err == nil {
		t.Fatal("a path reached through a symlinked ancestor was accepted")
	}
	overridden := ociTestLayer(t, false, regEntry("usr/local/bin/node", "x", 0o755))
	layout, manifest = ociTestLayout(t, symlink, overridden)
	if got, err := readOCIImageFile(ctx, layout, manifest, "/usr/local/bin/node", false); err != nil || got.digest != DigestBytes([]byte("x")) {
		t.Fatalf("a regular file in a later layer must replace an earlier symlink: %+v, %v", got, err)
	}

	layer := ociTestLayer(t, false, regEntry("usr/local/bin/node", "x", 0o755))
	layout, manifest = ociTestLayout(t, layer)
	blob := filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(DigestBytes(layer), "sha256:"))
	tampered := append(append([]byte{}, layer...), 0)
	if err := os.WriteFile(blob, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOCIImageFile(ctx, layout, manifest, "/usr/local/bin/node", false); err == nil {
		t.Fatal("a layer blob that does not hash to its descriptor was accepted")
	}
	if _, err := readOCIImageFile(ctx, layout, "sha256:"+strings.Repeat("b", 64), "/usr/local/bin/node", false); err == nil {
		t.Fatal("a manifest the index does not list was accepted")
	}
}

func TestLauncherVerifiesInImageFilesAgainstTheSignedLayoutWithoutPodman(t *testing.T) {
	layer := ociTestLayer(t, true,
		regEntry("usr/local/bin/node", "immutable Node ELF fixture", 0o555),
		regEntry("usr/local/bin/tbound-pi-entrypoint", "entrypoint", 0o755),
		regEntry("usr/local/bin/writable", "w", 0o777), regEntry("usr/local/bin/data", "d", 0o644))
	layout, manifest := ociTestLayout(t, layer)
	runner := &fakeD09Runner{}
	launcher := &D09PodmanCrunLauncher{runner: runner, config: D09Config{OCILayoutPath: layout, ExpectedImageDigest: manifest,
		EntrypointPath: d09EntrypointPath, EntrypointDigest: DigestBytes([]byte("entrypoint"))}}
	ctx := context.Background()
	if err := launcher.verifyImageWorkerExecutable(ctx, PiWorkerCellExecutable, DigestBytes([]byte("immutable Node ELF fixture"))); err != nil {
		t.Fatalf("matching image/host fd3 executable was refused: %v", err)
	}
	if err := launcher.verifyImageWorkerExecutable(ctx, PiWorkerCellExecutable, DigestBytes([]byte("different host node"))); err == nil {
		t.Fatal("different host fd3 executable digest was accepted for the signed image")
	}
	if err := launcher.verifyEntrypointInImage(ctx, launcher.config); err != nil {
		t.Fatalf("signed entrypoint was refused: %v", err)
	}
	launcher.config.EntrypointDigest = DigestBytes([]byte("another entrypoint"))
	if err := launcher.verifyEntrypointInImage(ctx, launcher.config); err == nil {
		t.Fatal("a different entrypoint digest was accepted")
	}
	for path, content := range map[string]string{"/usr/local/bin/writable": "w", "/usr/local/bin/data": "d"} {
		if err := launcher.verifyImageWorkerExecutable(ctx, path, DigestBytes([]byte(content))); err == nil {
			t.Fatalf("%s is not a non-writable executable but was accepted", path)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("podman was invoked: %v", runner.calls)
	}
}

// A tar parser that stops at an entry boundary cannot tell io.LimitReader's EOF from the end of the
// archive. With the bound exactly at the end of the first entry, an override after it was never read
// and the earlier digest was returned while the compressed blob still hashed correctly.
func TestOversizedLayerAlignedToAnEntryBoundaryIsRefusedNotTruncated(t *testing.T) {
	original := ociLayerMaxDecompressed
	t.Cleanup(func() { ociLayerMaxDecompressed = original })
	for _, compress := range []bool{false, true} {
		layer := ociTestLayer(t, compress,
			regEntry("usr/local/bin/node", "node v1", 0o755), // 512-byte header + 512 bytes of data
			regEntry("usr/local/bin/node", "node v2", 0o755))
		layout, manifest := ociTestLayout(t, layer)

		ociLayerMaxDecompressed = 8 << 30
		if got, err := readOCIImageFile(context.Background(), layout, manifest, "/usr/local/bin/node", false); err != nil ||
			got.digest != DigestBytes([]byte("node v2")) {
			t.Fatalf("compress=%v: without the bound the later entry must win: %+v, %v", compress, got, err)
		}
		ociLayerMaxDecompressed = 1024 // exactly the first entry
		if got, err := readOCIImageFile(context.Background(), layout, manifest, "/usr/local/bin/node", false); err == nil {
			t.Fatalf("compress=%v: a layer larger than the bound resolved to %+v instead of being refused", compress, got)
		}
		ociLayerMaxDecompressed = 1024 + 1024 + 1024 // both entries and the end blocks fit
		if got, err := readOCIImageFile(context.Background(), layout, manifest, "/usr/local/bin/node", false); err != nil ||
			got.digest != DigestBytes([]byte("node v2")) {
			t.Fatalf("compress=%v: a layer within the bound was refused or misread: %+v, %v", compress, got, err)
		}
	}
}

// Trailing bytes after the end-of-archive blocks count against the bound too.
func TestTrailingBytesAfterTheArchiveEndCountAgainstTheBound(t *testing.T) {
	original := ociLayerMaxDecompressed
	t.Cleanup(func() { ociLayerMaxDecompressed = original })
	layer := append(ociTestLayer(t, false, regEntry("usr/local/bin/node", "node", 0o755)), make([]byte, 4096)...)
	layout, manifest := ociTestLayout(t, layer)
	ociLayerMaxDecompressed = 2048
	if _, err := readOCIImageFile(context.Background(), layout, manifest, "/usr/local/bin/node", false); err == nil {
		t.Fatal("padding beyond the bound was accepted")
	}
	ociLayerMaxDecompressed = 1 << 20
	if _, err := readOCIImageFile(context.Background(), layout, manifest, "/usr/local/bin/node", false); err != nil {
		t.Fatalf("padding within the bound was refused: %v", err)
	}
}
