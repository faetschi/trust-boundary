package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func tarGz(t *testing.T, entries map[string]string, symlink string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if symlink != "" {
		if err := tw.WriteHeader(&tar.Header{Name: symlink, Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func manifestForArchive(name string, archive []byte) BundleManifest {
	sum := sha256.Sum256(archive)
	return BundleManifest{SchemaVersion: BundleSchemaVersion, Components: []Component{{
		Name: "node", Version: "24.21.0",
		URL: "https://example.invalid/node.tar.gz", SHA256: hex.EncodeToString(sum[:]),
		Target: "toolchains/node", Extract: true,
	}}}
}

func TestApplyBundleExtractsArchive(t *testing.T) {
	archive := tarGz(t, map[string]string{"bin/node": "elf-bytes", "LICENSE": "mit"}, "")
	m := manifestForArchive("node", archive)
	root := t.TempDir()
	if err := ApplyBundle(context.Background(), root, m, fakeFetcher{bodies: map[string][]byte{m.Components[0].URL: archive}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "toolchains", "node", "bin", "node")); err != nil {
		t.Fatalf("extracted file missing: %v", err)
	}
}

func TestApplyBundleRefusesSymlinkArchive(t *testing.T) {
	archive := tarGz(t, map[string]string{"ok": "x"}, "escape")
	m := manifestForArchive("node", archive)
	root := t.TempDir()
	if err := ApplyBundle(context.Background(), root, m, fakeFetcher{bodies: map[string][]byte{m.Components[0].URL: archive}}); err == nil {
		t.Fatal("symlink entry in pinned archive must be refused")
	}
}
