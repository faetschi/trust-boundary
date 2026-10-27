//go:build linux

package projectbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/delta"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// put writes a file (or a directory when the path ends in "/") below root.
func put(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, rel)
	if strings.HasSuffix(rel, "/") {
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, mode); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestPathBoundMatchesDelta(t *testing.T) {
	if MaxPathBytes != delta.MaxPathBytes {
		t.Fatalf("MaxPathBytes = %d, delta.MaxPathBytes = %d", MaxPathBytes, delta.MaxPathBytes)
	}
}

func TestBundleRoundTrip(t *testing.T) {
	src := privateDir(t)
	put(t, src, "main.go", "package main\n", 0o644)
	put(t, src, "bin/run.sh", "#!/bin/sh\n", 0o755)
	put(t, src, "empty/", "", 0)
	put(t, src, "secret.txt", "kept: only the whole component secret is excluded", 0o644)
	put(t, src, "secret/token", "hunter2", 0o600)
	put(t, src, ".git/config", "[core]\n", 0o644)
	put(t, src, "sub/.git", "gitdir: ../elsewhere\n", 0o644)
	// Excluded subtrees are never inspected, so what they hold cannot refuse the project.
	for _, dir := range []string{".git", "secret"} {
		if err := os.Symlink("/etc/passwd", filepath.Join(src, dir, "link")); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(src, dir, "fifo"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stream bytes.Buffer
	built, err := Build(src, BuildOptions{Exclude: []string{"secret"}, Limits: DefaultLimits()}, &stream)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".git", "secret", "sub/.git"}; !reflect.DeepEqual(built.Excluded, want) {
		t.Fatalf("excluded = %v, want %v", built.Excluded, want)
	}
	if want := []string{"bin", "bin/run.sh", "empty", "main.go", "secret.txt", "sub"}; !reflect.DeepEqual(built.Included, want) {
		t.Fatalf("included = %v, want %v", built.Included, want)
	}

	parent := privateDir(t)
	dest := filepath.Join(parent, "managed")
	manifest, digest, err := Import(&stream, dest, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if digest != built.Digest || !reflect.DeepEqual(manifest, built.Manifest) {
		t.Fatalf("import returned digest %s, build returned %s", digest, built.Digest)
	}
	if got := listDir(t, parent); !reflect.DeepEqual(got, []string{"managed"}) {
		t.Fatalf("parent holds %v, want only the project", got)
	}
	for rel, mode := range map[string]os.FileMode{".": 0o700, "lost+found": 0o755, "main.go": 0o644, "bin/run.sh": 0o755, "bin": 0o755, "empty": 0o755, "secret.txt": 0o644} {
		info, err := os.Lstat(filepath.Join(dest, rel))
		if err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: mode %v err %v, want %v", rel, info, err, mode)
		}
	}
	for _, rel := range []string{".git", "secret", "sub/.git"} {
		if _, err := os.Lstat(filepath.Join(dest, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was imported (err %v)", rel, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "bin/run.sh")); string(got) != "#!/bin/sh\n" {
		t.Errorf("content = %q", got)
	}
	rescanned, _, err := scanTree(dest, scanOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := rescanned.Digest(); again != digest {
		t.Fatalf("imported tree digest %s, bundle digest %s", again, digest)
	}
}

func TestBuildRefusals(t *testing.T) {
	small := func(l *Limits) { l.MaxFileBytes = 10 }
	cases := []struct {
		name   string
		setup  func(t *testing.T, root string)
		limits func(*Limits)
		path   string
		reason string
	}{
		{"symlink", func(t *testing.T, root string) {
			if err := os.Symlink("main.go", filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
		}, nil, "link", "symlink"},
		{"hard link", func(t *testing.T, root string) {
			if err := os.Link(filepath.Join(root, "main.go"), filepath.Join(root, "second")); err != nil {
				t.Fatal(err)
			}
		}, nil, "second", "hard link"},
		{"fifo", func(t *testing.T, root string) {
			if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, nil, "pipe", "FIFO"},
		{"setuid file", func(t *testing.T, root string) {
			if err := os.Chmod(filepath.Join(root, "main.go"), os.ModeSetuid|0o755); err != nil {
				t.Fatal(err)
			}
		}, nil, "main.go", "setuid, setgid or sticky"},
		{"sticky directory", func(t *testing.T, root string) {
			put(t, root, "tmp/", "", 0)
			if err := os.Chmod(filepath.Join(root, "tmp"), os.ModeSticky|0o755); err != nil {
				t.Fatal(err)
			}
		}, nil, "tmp", "setuid, setgid or sticky"},
		{"oversized file", func(t *testing.T, root string) {
			put(t, root, "big", strings.Repeat("x", 11), 0o644)
		}, small, "big", "exceeds the limit"},
		{"too many entries", func(t *testing.T, root string) {
			for _, n := range []string{"a", "b", "c", "d"} {
				put(t, root, n, "x", 0o644)
			}
		}, func(l *Limits) { l.MaxEntries = 3 }, "", "entries"},
		{"total too large", func(t *testing.T, root string) {
			put(t, root, "x1", strings.Repeat("x", 9), 0o644)
			put(t, root, "x2", strings.Repeat("x", 9), 0o644)
		}, func(l *Limits) { l.MaxFileBytes, l.MaxTotalBytes = 10, 15 }, "x2", "total size"},
		{"path too long", func(t *testing.T, root string) {
			part := strings.Repeat("d", 250)
			put(t, root, strings.Join([]string{part, part, part, part, part}, "/")+"/", "", 0)
		}, nil, "", "unsupported"},
		{"non-canonical name", func(t *testing.T, root string) {
			put(t, root, `back\slash`, "x", 0o644)
		}, nil, `back\slash`, "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := privateDir(t)
			put(t, root, "main.go", "package main\n", 0o644)
			tc.setup(t, root)
			limits := DefaultLimits()
			if tc.limits != nil {
				tc.limits(&limits)
			}
			var out bytes.Buffer
			_, err := Build(root, BuildOptions{Limits: limits}, &out)
			var refused *RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("Build error = %v, want *RefusedError", err)
			}
			found := false
			for _, r := range refused.Refusals {
				if (tc.path == "" || r.Path == tc.path) && strings.Contains(r.Reason, tc.reason) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no refusal of %q for %q in %v", tc.path, tc.reason, refused.Refusals)
			}
			if out.Len() != 0 {
				t.Fatalf("a refused project wrote %d bytes", out.Len())
			}
		})
	}
}

func fileEntry(path, content string) Entry {
	sum := sha256.Sum256([]byte(content))
	return Entry{Path: path, Type: TypeFile, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
}

func dirEntry(path string) Entry { return Entry{Path: path, Type: TypeDir} }

// craft frames a manifest as a bundle stream without validating it.
func craft(t *testing.T, entries []Entry, content ...string) []byte {
	t.Helper()
	header, err := Manifest{Format: BundleFormat, Entries: entries}.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}
	return framed(t, header, content...)
}

func framed(t *testing.T, header []byte, content ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := writeFramed(&buf, BundleFormat, header); err != nil {
		t.Fatal(err)
	}
	for _, c := range content {
		buf.WriteString(c)
	}
	return buf.Bytes()
}

func TestImportRefusals(t *testing.T) {
	good := craft(t, []Entry{dirEntry("d"), fileEntry("d/f", "hello")}, "hello")
	indented, _ := json.MarshalIndent(Manifest{Format: BundleFormat, Entries: []Entry{dirEntry("d")}}, "", " ")
	reordered := []byte(`{"entries":[],"format":"` + BundleFormat + `"}`)
	unknownField := []byte(`{"format":"` + BundleFormat + `","entries":[],"extra":1}`)
	hugeLength := append([]byte(BundleFormat+"\n"), binary.BigEndian.AppendUint32(nil, 0xFFFFFFFF)...)
	wrongMagic := append([]byte("tbound-project-bundle/v0\n"), good[len(BundleFormat)+1:]...)
	badHex := fileEntry("f", "x")
	badHex.SHA256 = strings.ToUpper(badHex.SHA256)
	dirWithSize := dirEntry("d")
	dirWithSize.Size = 1
	execDir := dirEntry("d")
	execDir.Executable = true
	negative := fileEntry("f", "")
	negative.Size = -1
	unknownType := Entry{Path: "l", Type: "symlink"}

	cases := []struct {
		name   string
		stream []byte
		limits func(*Limits)
	}{
		{"parent traversal", craft(t, []Entry{fileEntry("../x", "x")}, "x"), nil},
		{"absolute path", craft(t, []Entry{fileEntry("/x", "x")}, "x"), nil},
		{"empty segment", craft(t, []Entry{dirEntry("a"), fileEntry("a//b", "x")}, "x"), nil},
		{"dot segment", craft(t, []Entry{dirEntry("a"), fileEntry("a/./b", "x")}, "x"), nil},
		{"backslash", craft(t, []Entry{fileEntry(`a\b`, "x")}, "x"), nil},
		{"control character", craft(t, []Entry{fileEntry("a\x01b", "x")}, "x"), nil},
		{"fence directory", craft(t, []Entry{dirEntry("lost+found")}), nil},
		{"git administration", craft(t, []Entry{dirEntry(".git")}), nil},
		{"nested git file", craft(t, []Entry{dirEntry("d"), fileEntry("d/.git", "x")}, "x"), nil},
		{"duplicate path", craft(t, []Entry{fileEntry("a", "x"), fileEntry("a", "x")}, "x", "x"), nil},
		{"unsorted", craft(t, []Entry{fileEntry("b", "x"), fileEntry("a", "x")}, "x", "x"), nil},
		{"file under a file", craft(t, []Entry{fileEntry("f", "x"), fileEntry("f/g", "x")}, "x", "x"), nil},
		{"parent not listed", craft(t, []Entry{fileEntry("d/f", "x")}, "x"), nil},
		{"digest mismatch", craft(t, []Entry{fileEntry("f", "hello")}, "hellp"), nil},
		{"truncated content", good[:len(good)-2], nil},
		{"truncated header", good[:len(BundleFormat)+5+10], nil},
		{"truncated length prefix", good[:len(BundleFormat)+3], nil},
		{"empty stream", nil, nil},
		{"trailing data", append(append([]byte(nil), good...), 'x'), nil},
		{"wrong magic", wrongMagic, nil},
		{"huge header length", hugeLength, nil},
		{"indented json", framed(t, indented), nil},
		{"unknown field", framed(t, unknownField), nil},
		{"reordered keys", framed(t, reordered), nil},
		{"uppercase sha256", craft(t, []Entry{badHex}, "x"), nil},
		{"directory with size", craft(t, []Entry{dirWithSize}), nil},
		{"executable directory", craft(t, []Entry{execDir}), nil},
		{"negative size", craft(t, []Entry{negative}), nil},
		{"symlink type", craft(t, []Entry{unknownType}), nil},
		{"oversized file", craft(t, []Entry{fileEntry("f", "12345")}, "12345"), func(l *Limits) { l.MaxFileBytes = 4 }},
		{"too many entries", craft(t, []Entry{fileEntry("a", "x"), fileEntry("b", "x")}, "x", "x"), func(l *Limits) { l.MaxEntries = 1 }},
		{"total too large", craft(t, []Entry{fileEntry("a", "xxx"), fileEntry("b", "xxx")}, "xxx", "xxx"), func(l *Limits) { l.MaxTotalBytes = 5 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := privateDir(t)
			dest := filepath.Join(parent, "managed")
			limits := DefaultLimits()
			if tc.limits != nil {
				tc.limits(&limits)
			}
			_, _, err := Import(bytes.NewReader(tc.stream), dest, limits)
			if !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("Import error = %v, want ErrInvalidBundle", err)
			}
			if got := listDir(t, parent); len(got) != 0 {
				t.Fatalf("a refused import left %v", got)
			}
		})
	}

	t.Run("control stream imports", func(t *testing.T) {
		parent := privateDir(t)
		if _, _, err := Import(bytes.NewReader(good), filepath.Join(parent, "managed"), DefaultLimits()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestImportDestinationRules(t *testing.T) {
	good := craft(t, []Entry{fileEntry("f", "hello")}, "hello")
	t.Run("destination exists", func(t *testing.T) {
		parent := privateDir(t)
		dest := filepath.Join(parent, "managed")
		put(t, dest, "keep", "mine", 0o644)
		if _, _, err := Import(bytes.NewReader(good), dest, DefaultLimits()); err == nil {
			t.Fatal("import over an existing destination succeeded")
		}
		if got := listDir(t, dest); !reflect.DeepEqual(got, []string{"keep"}) {
			t.Fatalf("destination holds %v", got)
		}
		if got := listDir(t, parent); !reflect.DeepEqual(got, []string{"managed"}) {
			t.Fatalf("parent holds %v", got)
		}
	})
	t.Run("parent missing", func(t *testing.T) {
		if _, _, err := Import(bytes.NewReader(good), filepath.Join(privateDir(t), "no", "managed"), DefaultLimits()); err == nil {
			t.Fatal("import below a missing parent succeeded")
		}
	})
	t.Run("parent not private", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Import(bytes.NewReader(good), filepath.Join(parent, "managed"), DefaultLimits()); err == nil {
			t.Fatal("import below a world-readable parent succeeded")
		}
		if got := listDir(t, parent); len(got) != 0 {
			t.Fatalf("parent holds %v", got)
		}
	})
}
