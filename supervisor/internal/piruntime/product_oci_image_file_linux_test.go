//go:build linux

package piruntime

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProductOCIImageFileRefusesRedirectedLayerEntries(t *testing.T) {
	link := func(name, target string) ociTestEntry {
		return ociTestEntry{name: name, typeflag: tar.TypeSymlink, link: target}
	}
	dir := func(name string) ociTestEntry {
		return ociTestEntry{name: name, typeflag: tar.TypeDir, mode: 0o755}
	}
	for _, imagePath := range []string{"/bin/bash", d09EntrypointPath} {
		t.Run(imagePath, func(t *testing.T) {
			realPath := strings.TrimPrefix(imagePath, "/")
			if imagePath == "/bin/bash" {
				realPath = "usr/bin/bash"
			}
			realDir, base := path.Dir(realPath), path.Base(realPath)
			lower := []ociTestEntry{link("bin", "usr/bin"), regEntry(realPath, "A", 0o555)}
			for _, tc := range []struct {
				name   string
				layers [][]ociTestEntry
				want   string
			}{
				{"reviewer case", [][]ociTestEntry{lower, {dir("usr/"), dir(realDir + "/"), link("alias", realDir), regEntry("alias/"+base, "B", 0o555)}}, ""},
				{"lower layer alias", [][]ociTestEntry{append(append([]ociTestEntry{}, lower...), link("alias", realDir)), {regEntry("alias/"+base, "B", 0o555)}}, ""},
				{"same layer alias", [][]ociTestEntry{append(append([]ociTestEntry{}, lower...), link("alias", realDir), regEntry("alias/"+base, "B", 0o555))}, ""},
				{"alias defined after write", [][]ociTestEntry{lower, {regEntry("alias/"+base, "B", 0o555), link("alias", realDir)}}, "A"},
				{"PAX path through alias", [][]ociTestEntry{lower, {link("alias", realDir), regEntry("alias/"+strings.Repeat("p", 300), "B", 0o555)}}, ""},
				{"regular file replaces alias", [][]ociTestEntry{append(append([]ociTestEntry{}, lower...), link("alias", realDir)), {regEntry("alias", "file", 0o555), regEntry("alias/"+base, "B", 0o555)}}, ""},
				{"normalized names", [][]ociTestEntry{lower, {link("././alias/", realDir), regEntry("././alias/"+base, "B", 0o555)}}, ""},
				{"directory replaces alias", [][]ociTestEntry{append(append([]ociTestEntry{}, lower...), link("alias", realDir)), {dir("./alias/"), regEntry("./alias/"+base, "B", 0o555)}}, "A"},
				{"same layer directory replaces alias", [][]ociTestEntry{lower, {link("alias", realDir), dir("alias/"), regEntry("alias/"+base, "B", 0o555)}}, "A"},
				{"normal merged usr", [][]ociTestEntry{lower, {dir("usr/"), dir(realDir + "/"), regEntry("./"+realPath, "B", 0o555)}}, "B"},
				{"whiteout through alias", [][]ociTestEntry{lower, {link("alias", realDir), whiteout("./alias/.wh." + base)}}, ""},
				{"opaque whiteout through alias", [][]ociTestEntry{lower, {link("alias", realDir), whiteout("alias/.wh..wh..opq")}}, ""},
				{"hardlink source through alias", [][]ociTestEntry{lower, {link("alias", realDir), {name: "copy", typeflag: tar.TypeLink, link: "./alias/" + base}}}, ""},
				{"hardlink destination through alias", [][]ociTestEntry{lower, {link("alias", realDir), {name: "alias/copy", typeflag: tar.TypeLink, link: realPath}}}, ""},
				{"hardlink copies symlink", [][]ociTestEntry{lower, {link("alias", realDir), {name: "copy", typeflag: tar.TypeLink, link: "alias"}, regEntry("copy/"+base, "B", 0o555)}}, ""},
				{"same layer hardlink metadata", [][]ociTestEntry{append(append([]ociTestEntry{}, lower...), ociTestEntry{name: "copy", typeflag: tar.TypeLink, link: realPath, mode: 0o777})}, ""},
				{"upper layer hardlink metadata", [][]ociTestEntry{lower, {{name: "copy", typeflag: tar.TypeLink, link: "./" + realPath, mode: 0o4777}}}, ""},
				{"unrelated hardlink", [][]ociTestEntry{lower, {regEntry("unrelated", "other", 0o555), {name: "copy", typeflag: tar.TypeLink, link: "unrelated", mode: 0o777}}}, "A"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var layers [][]byte
					for i, entries := range tc.layers {
						layers = append(layers, ociTestLayer(t, i%2 == 0, entries...))
					}
					layout, manifest := ociTestLayout(t, layers...)
					got, err := readProductOCIImageFile(context.Background(), layout, manifest, imagePath, false)
					if tc.want == "" {
						if err == nil {
							t.Fatalf("redirected layer entry accepted: verified %+v instead of refusing the image", got)
						}
						if !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "hardlink") {
							t.Fatalf("expected redirected-entry refusal, got %v", err)
						}
						return
					}
					if err != nil || got.digest != DigestBytes([]byte(tc.want)) || got.size != int64(len(tc.want)) || got.mode != 0o555 {
						t.Fatalf("file = %+v, %v; want bytes %q, mode 0555", got, err, tc.want)
					}
				})
			}
		})
	}
}

func TestProductOCIImageFileResolvesOnlyMergedAncestorSymlinks(t *testing.T) {
	link := func(name, target string) ociTestEntry {
		return ociTestEntry{name: name, typeflag: tar.TypeSymlink, link: target}
	}
	dir := func(name string) ociTestEntry {
		return ociTestEntry{name: name, typeflag: tar.TypeDir, mode: 0o755}
	}
	chain := func(hops int) []ociTestEntry {
		var entries []ociTestEntry
		for i := 0; i < hops; i++ {
			entries = append(entries, link(fmt.Sprintf("link%d", i), fmt.Sprintf("link%d", i+1)))
		}
		return append(entries, regEntry(fmt.Sprintf("link%d/bash", hops), "bash", 0o555))
	}
	for _, tc := range []struct {
		name, path, want string
		layers           [][]ociTestEntry
	}{
		{"merged usr", "/bin/bash", "bash", [][]ociTestEntry{{link("bin", "usr/bin"), regEntry("usr/bin/bash", "bash", 0o555)}}},
		{"absolute target", "/bin/bash", "bash", [][]ociTestEntry{{link("bin", "/usr/bin"), regEntry("usr/bin/bash", "bash", 0o555)}}},
		{"relative to link directory", "/opt/bin/bash", "bash", [][]ociTestEntry{{link("opt/bin", "../usr/bin"), regEntry("usr/bin/bash", "bash", 0o555)}}},
		{"dot dot clamped at root", "/opt/bin/bash", "bash", [][]ociTestEntry{{link("opt/bin", "../../../../usr/bin"), regEntry("usr/bin/bash", "bash", 0o555)}}},
		{"absolute dot dot clamped at root", "/bin/bash", "bash", [][]ociTestEntry{{link("bin", "/../../usr/bin"), regEntry("usr/bin/bash", "bash", 0o555)}}},
		{"dot dot after another link", "/bin/bash", "bash", [][]ociTestEntry{{link("bin", "alias/../bin2"), link("alias", "usr/lib"), dir("usr/lib"), regEntry("usr/bin2/bash", "bash", 0o555), regEntry("bin2/bash", "wrong lexical cleanup", 0o555)}}},
		{"root target", "/bin/bash", "bash", [][]ociTestEntry{{link("bin", "/"), regEntry("bash", "bash", 0o555)}}},
		{"empty link target", "/bin/bash", "", [][]ociTestEntry{{link("bin", ""), regEntry("bash", "bash", 0o555)}}},
		{"loop", "/bin/bash", "", [][]ociTestEntry{{link("bin", "other"), link("other", "bin")}}},
		{"eight hops", "/link0/bash", "bash", [][]ociTestEntry{chain(8)}},
		{"nine hops", "/link0/bash", "", [][]ociTestEntry{chain(9)}},
		{"directory replaces link", "/bin/bash", "upper", [][]ociTestEntry{{link("bin", "usr/bin"), regEntry("usr/bin/bash", "lower", 0o555)}, {dir("bin"), regEntry("bin/bash", "upper", 0o555)}}},
		{"empty directory replaces link", "/bin/bash", "", [][]ociTestEntry{{link("bin", "usr/bin"), regEntry("usr/bin/bash", "lower", 0o555)}, {dir("bin")}}},
		{"directory recreated in whiteout layer", "/bin/bash", "upper", [][]ociTestEntry{{link("bin", "usr/bin"), regEntry("usr/bin/bash", "lower", 0o555)}, {whiteout(".wh.bin"), dir("bin"), regEntry("bin/bash", "upper", 0o555)}}},
		{"upper link target", "/bin/bash", "upper", [][]ociTestEntry{{link("bin", "old"), regEntry("old/bash", "lower", 0o555)}, {link("bin", "new"), regEntry("new/bash", "upper", 0o555)}}},
		{"whiteout hides link", "/bin/bash", "", [][]ociTestEntry{{link("bin", "usr/bin"), regEntry("usr/bin/bash", "bash", 0o555)}, {whiteout(".wh.bin")}}},
		{"opaque hides link", "/opt/bin/bash", "", [][]ociTestEntry{{link("opt/bin", "/usr/bin"), regEntry("usr/bin/bash", "bash", 0o555)}, {whiteout("opt/.wh..wh..opq")}}},
		{"recreated above whiteout", "/bin/bash", "upper", [][]ociTestEntry{{link("bin", "usr/bin"), regEntry("usr/bin/bash", "lower", 0o555)}, {whiteout(".wh.bin")}, {dir("bin"), regEntry("bin/bash", "upper", 0o555)}}},
		{"target file overridden", "/bin/bash", "upper", [][]ociTestEntry{{link("bin", "usr/bin"), regEntry("usr/bin/bash", "lower", 0o555)}, {regEntry("usr/bin/bash", "upper", 0o555)}}},
		{"target file whited out", "/bin/bash", "", [][]ociTestEntry{{link("bin", "usr/bin"), regEntry("usr/bin/bash", "lower", 0o555)}, {whiteout("usr/bin/.wh.bash")}}},
		{"final symlink", "/bin/bash", "", [][]ociTestEntry{{link("bin", "usr/bin"), link("usr/bin/bash", "real"), regEntry("usr/bin/real", "bash", 0o555)}}},
		{"final directory", "/bin/bash", "", [][]ociTestEntry{{link("bin", "usr/bin"), dir("usr/bin/bash")}}},
		{"final hardlink", "/bin/bash", "", [][]ociTestEntry{{link("bin", "usr/bin"), {name: "usr/bin/bash", typeflag: tar.TypeLink, link: "real"}, regEntry("real", "bash", 0o555)}}},
		{"final fifo", "/bin/bash", "", [][]ociTestEntry{{dir("bin"), {name: "bin/bash", typeflag: tar.TypeFifo}}}},
		{"ancestor file", "/bin/bash", "", [][]ociTestEntry{{regEntry("bin", "file", 0o555)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var layers [][]byte
			for i, entries := range tc.layers {
				layers = append(layers, ociTestLayer(t, i%2 == 0, entries...))
			}
			layout, manifest := ociTestLayout(t, layers...)
			got, err := readProductOCIImageFile(context.Background(), layout, manifest, tc.path, false)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unsafe or missing path accepted: %+v", got)
				}
				if tc.name == "loop" || tc.name == "nine hops" {
					if !strings.Contains(err.Error(), "symlink hop limit") {
						t.Fatalf("expected bounded symlink refusal, got %v", err)
					}
				}
				return
			}
			if err != nil || got.digest != DigestBytes([]byte(tc.want)) || got.size != int64(len(tc.want)) || got.mode != 0o555 {
				t.Fatalf("file = %+v, %v; want bytes %q, mode 0555", got, err, tc.want)
			}
		})
	}
}

func TestProductOCIImageFileStillVerifiesSignedBlobsAndThesisRefusesLinks(t *testing.T) {
	layer := ociTestLayer(t, false,
		ociTestEntry{name: "bin", typeflag: tar.TypeSymlink, link: "usr/bin"},
		regEntry("usr/bin/bash", "bash", 0o555))
	layout, manifest := ociTestLayout(t, layer)
	if _, err := readOCIImageFile(context.Background(), layout, manifest, "/bin/bash", false); err == nil {
		t.Fatal("thesis reader followed an ancestor symlink")
	}
	blob := filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(DigestBytes(layer), "sha256:"))
	if err := os.WriteFile(blob, append(layer, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProductOCIImageFile(context.Background(), layout, manifest, "/bin/bash", false); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered layer was not refused by digest: %v", err)
	}
}

func productOCIMemoTestLayout(t *testing.T) (layout, manifest string, paths []string) {
	t.Helper()
	productOCIMemo.Lock()
	productOCIMemo.key, productOCIMemo.files = "", nil
	productOCIMemo.Unlock()
	lower := ociTestLayer(t, true, ociTestEntry{name: "bin", typeflag: tar.TypeSymlink, link: "usr/bin"},
		regEntry("usr/bin/bash", "bash", 0o555), regEntry("usr/bin/node", "node", 0o555))
	upper := ociTestLayer(t, false, regEntry("entrypoint", "entry", 0o555))
	layout, manifest = ociTestLayout(t, lower, upper)
	waitUntilSettled()
	return layout, manifest, []string{"/entrypoint", "/bin/bash", "/usr/bin/node"}
}

func TestProductOCIImageFilesMatchSeparateCalls(t *testing.T) {
	layout, manifest, paths := productOCIMemoTestLayout(t)
	got, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths, false)
	if err != nil || len(got) != 3 {
		t.Fatalf("batch = %+v, %v", got, err)
	}
	// The oracle is the known content of the layers, not the batch code under test.
	for i, content := range []string{"entry", "bash", "node"} {
		if got[i].digest != DigestBytes([]byte(content)) || got[i].size != int64(len(content)) || got[i].mode != 0o555 {
			t.Fatalf("%s: batch = %+v, want %q mode 0555", paths[i], got[i], content)
		}
	}
	for i, imagePath := range paths {
		productOCIMemo.Lock()
		productOCIMemo.key, productOCIMemo.files = "", nil
		productOCIMemo.Unlock()
		want, err := readProductOCIImageFile(context.Background(), layout, manifest, imagePath, false)
		if err != nil || want != got[i] {
			t.Fatalf("%s: separate = %+v, %v; batch = %+v", imagePath, want, err, got[i])
		}
	}
	// A missing path is refused with its own error and does not hide the others.
	files, fileErrs, err := readProductOCIImageFilesEach(context.Background(), layout, manifest, []string{"/entrypoint", "/missing", "/bin/bash"}, false)
	if err != nil || fileErrs[0] != nil || fileErrs[1] == nil || fileErrs[2] != nil || files[2] != got[1] {
		t.Fatalf("per-file results = %+v, %v, %v", files, fileErrs, err)
	}
	if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, []string{"/entrypoint", "/missing"}, false); err == nil || !strings.Contains(err.Error(), "/missing") {
		t.Fatalf("a missing path in the batch was accepted or unnamed: %v", err)
	}
}

func TestProductOCIImageFilesMemoRescansOnlyAfterIdentityChange(t *testing.T) {
	layout, manifest, paths := productOCIMemoTestLayout(t)
	read := func() ([]ociImageFile, error) {
		return readProductOCIImageFiles(context.Background(), layout, manifest, paths, false)
	}
	reads := func() int64 { return productOCILayerReads.Load() }
	first, err := read()
	if err != nil {
		t.Fatal(err)
	}
	before := reads()
	again, err := read()
	if err != nil || reads() != before || fmt.Sprint(again) != fmt.Sprint(first) {
		t.Fatalf("second verification rescanned layers (%d reads) or differed: %v", reads()-before, err)
	}
	// The expected manifest digest is part of the key: another digest is refused, never answered from the memo.
	if _, err := readProductOCIImageFiles(context.Background(), layout, DigestBytes([]byte("other")), paths, false); err == nil {
		t.Fatal("unlisted manifest digest accepted")
	}
	// A subset of memoized paths is answered from the memo; a path never verified is scanned.
	if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths[:1], false); err != nil || reads() != before {
		t.Fatalf("a memoized subset rescanned layers: %v", err)
	}
	if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, []string{"/usr/bin/bash"}, false); err != nil || reads() == before {
		t.Fatalf("an unverified path was answered from the memo: %v", err)
	}
	if _, err := read(); err != nil {
		t.Fatal(err)
	}
	// A layout file change (index.json gains whitespace) forces a full re-verification that still succeeds.
	indexPath := filepath.Join(layout, "index.json")
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, append(index, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	before = reads()
	if _, err := read(); err != nil || reads() == before {
		t.Fatalf("index.json change was not re-verified: %v", err)
	}
	// The rewritten index.json is fresh: settle, re-prime, and require a memo hit before tampering.
	waitUntilSettled()
	if _, err := read(); err != nil {
		t.Fatal(err)
	}
	before = reads()
	if _, err := read(); err != nil || reads() != before {
		t.Fatalf("settled layout was not answered from the memo: %v", err)
	}
	// A replaced blob is re-verified and refused.
	blobs := filepath.Join(layout, "blobs", "sha256")
	entries, err := os.ReadDir(blobs)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == strings.TrimPrefix(manifest, "sha256:") {
			continue
		}
		name := filepath.Join(blobs, entry.Name())
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 100 { // the image config, which verification never reads
			continue
		}
		data[len(data)/2] ^= 0xff
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
		break
	}
	if got, err := read(); err == nil || reads() == before {
		t.Fatalf("tampered blob accepted from the memo or not rescanned: %+v, %v", got, err)
	}
}

// A file fresh at pre-snapshot time is never memoized, even if verification stalls past the threshold after the snapshot.
func TestProductOCIImageFilesStalledScanIsNotMemoized(t *testing.T) {
	layout, manifest, paths := productOCIMemoTestLayout(t)
	now := time.Now()
	if err := os.Chtimes(filepath.Join(layout, "index.json"), now, now); err != nil {
		t.Fatal(err)
	}
	productAfterSnapshot = waitUntilSettled
	defer func() { productAfterSnapshot = nil }()
	for i := 0; i < 2; i++ {
		before := productOCILayerReads.Load()
		if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths, false); err != nil {
			t.Fatal(err)
		}
		if productOCILayerReads.Load() == before {
			t.Fatalf("call %d answered from the memo although index.json was fresh at snapshot time", i+1)
		}
	}
}

func TestProductOCIImageFilesFreshLayoutIsNotMemoized(t *testing.T) {
	layout, manifest, paths := productOCIMemoTestLayout(t)
	defer func(old time.Duration) { productMemoMinAge = old }(productMemoMinAge)
	productMemoMinAge = time.Hour
	for i := 0; i < 2; i++ {
		before := productOCILayerReads.Load()
		if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths, false); err != nil {
			t.Fatal(err)
		}
		if productOCILayerReads.Load() == before {
			t.Fatalf("call %d answered from the memo although the layout is newer than the threshold", i+1)
		}
	}
}

// A same-inode, same-size, same-mtime rewrite differs only in ctime; the memo key must still notice it.
func TestProductOCIImageFilesMemoNoticesCtimeOnlyChange(t *testing.T) {
	layout, manifest, paths := productOCIMemoTestLayout(t)
	read := func() ([]ociImageFile, error) {
		return readProductOCIImageFiles(context.Background(), layout, manifest, paths, false)
	}
	rewriteKeepingMtime := func(name string, mutate func([]byte)) {
		t.Helper()
		before, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		mutate(data)
		file, err := os.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, time.Now(), before.ModTime()); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(name)
		if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			t.Fatalf("rewrite changed more than ctime: %v", err)
		}
	}
	if _, err := read(); err != nil {
		t.Fatal(err)
	}
	// index.json: same size, same mtime, different ctime -> rescan, still accepted.
	before := productOCILayerReads.Load()
	rewriteKeepingMtime(filepath.Join(layout, "index.json"), func([]byte) {})
	if _, err := read(); err != nil || productOCILayerReads.Load() == before {
		t.Fatalf("ctime-only index.json change was not re-verified: %v", err)
	}
	// The rewritten index.json is fresh: settle, re-prime, and require a memo hit before tampering.
	waitUntilSettled()
	if _, err := read(); err != nil {
		t.Fatal(err)
	}
	before = productOCILayerReads.Load()
	if _, err := read(); err != nil || productOCILayerReads.Load() != before {
		t.Fatalf("settled layout was not answered from the memo: %v", err)
	}
	// A layer blob: tampered in place -> rescan and refusal.
	layers, err := ociManifestLayers(layout, manifest, false)
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(layers[0].Digest, "sha256:"))
	before = productOCILayerReads.Load()
	rewriteKeepingMtime(blob, func(data []byte) { data[len(data)/2] ^= 0xff })
	if got, err := read(); err == nil || productOCILayerReads.Load() == before {
		t.Fatalf("ctime-only blob tamper accepted from the memo: %+v, %v", got, err)
	}
}

// A layout that is settled before the first call is memoized even if verification stalls after the snapshot.
func TestProductOCIImageFilesSettledLayoutIsMemoizedAfterStall(t *testing.T) {
	layout, manifest, paths := productOCIMemoTestLayout(t)
	productAfterSnapshot = waitUntilSettled
	defer func() { productAfterSnapshot = nil }()
	if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths, false); err != nil {
		t.Fatal(err)
	}
	before := productOCILayerReads.Load()
	if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths, false); err != nil || productOCILayerReads.Load() != before {
		t.Fatalf("settled layout was not answered from the memo: %v", err)
	}
}

func TestProductOCIImageFilesRecheckReadOnlyMount(t *testing.T) {
	layout, manifest, paths := productOCIMemoTestLayout(t)
	if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths, false); err != nil {
		t.Fatal(err)
	}
	defer func(old func(string) error) { productLayoutMountCheck = old }(productLayoutMountCheck)
	checks := 0
	productLayoutMountCheck = func(string) error { checks++; return errors.New("mount is writable") }
	// requireImmutable=false never asks (the test layout is not root-owned); true is refused before any memo or layer read.
	before := productOCILayerReads.Load()
	if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths, false); err != nil || checks != 0 {
		t.Fatalf("mutable read: err = %v, mount checks = %d", err, checks)
	}
	if _, err := readProductOCIImageFiles(context.Background(), layout, manifest, paths, true); err == nil || !strings.Contains(err.Error(), "read-only mount") {
		t.Fatalf("writable layout mount: err = %v, want refusal", err)
	}
	if checks != 1 || productOCILayerReads.Load() != before {
		t.Fatalf("mount checks = %d, layer reads = %d", checks, productOCILayerReads.Load()-before)
	}
}
