//go:build linux

package piruntime

import (
	"archive/tar"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
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
