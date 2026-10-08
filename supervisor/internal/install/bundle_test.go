package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeFetcher struct{ bodies map[string][]byte }

func (f fakeFetcher) Fetch(_ context.Context, url string) (io.ReadCloser, error) {
	body, ok := f.bodies[url]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(string(body))), nil
}

func manifestFor(body []byte) BundleManifest {
	sum := sha256.Sum256(body)
	return BundleManifest{
		SchemaVersion: BundleSchemaVersion,
		Components: []Component{{
			Name: "node", Version: "24.21.0",
			URL: "https://example.invalid/node.tar.xz", SHA256: hex.EncodeToString(sum[:]), Target: "toolchains/node/bin/node",
		}},
	}
}

func TestBundleManifestRejectsPlaceholders(t *testing.T) {
	m := BundleManifest{SchemaVersion: BundleSchemaVersion, Components: []Component{{
		Name: "node", Version: "24.21.0", URL: "https://example.invalid/x", SHA256: "todo", Target: "x",
	}}}
	if err := m.Validate(); err == nil {
		t.Fatal("placeholder sha256 must be rejected")
	}
}

func TestApplyAndVerifyBundle(t *testing.T) {
	body := []byte("node-binary-bytes")
	m := manifestFor(body)
	root := t.TempDir()
	fetch := fakeFetcher{bodies: map[string][]byte{m.Components[0].URL: body}}
	if err := ApplyBundle(context.Background(), root, m, fetch); err != nil {
		t.Fatal(err)
	}
	status, err := VerifyBundle(root, m)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Present {
		t.Fatalf("expected present, got %+v", status)
	}
	if _, err := os.Stat(filepath.Join(root, m.Components[0].Target)); err != nil {
		t.Fatalf("target not written: %v", err)
	}
}

func TestApplyBundleRejectsChecksumMismatch(t *testing.T) {
	m := manifestFor([]byte("real-bytes"))
	root := t.TempDir()
	fetch := fakeFetcher{bodies: map[string][]byte{m.Components[0].URL: []byte("tampered-bytes")}}
	if err := ApplyBundle(context.Background(), root, m, fetch); err == nil {
		t.Fatal("checksum mismatch must fail closed")
	}
}
