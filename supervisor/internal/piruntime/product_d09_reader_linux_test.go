//go:build linux

package piruntime

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The fixture trusts its actual account below base; production still trusts only root from /.
// Reuse the production mode/type/size checks with only that account translated to root.
type productD09ReaderTestInfo struct {
	fs.FileInfo
	stat syscall.Stat_t
}

func (i productD09ReaderTestInfo) Sys() any { return &i.stat }

func productD09ReaderTestPolicy(base string, uid uint32) productD09ReadPolicy {
	translate := func(info fs.FileInfo) fs.FileInfo {
		stat := *info.Sys().(*syscall.Stat_t)
		if stat.Uid == uid {
			stat.Uid = 0
		} else {
			stat.Uid = 65534
		}
		return productD09ReaderTestInfo{info, stat}
	}
	return productD09ReadPolicy{
		checkDirectory: func(path string, info fs.FileInfo) error {
			if corpusAncestorExempt(path, base) {
				return nil
			}
			return productD09ValidateDirectoryInfo(translate(info))
		},
		checkFile: func(info fs.FileInfo, limit int64) error {
			return productD09ValidateFileInfo(translate(info), limit)
		},
	}
}

func TestProductD09ReaderKeepsVerifiedParentDuringReplacement(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "trusted")
	nested := filepath.Join(parent, "nested")
	attacker := filepath.Join(base, "attacker")
	for _, dir := range []string{nested, filepath.Join(attacker, "nested")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(nested, "input")
	if err := os.WriteFile(path, []byte("trusted"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attacker, "nested", "input"), []byte("substituted"), 0644); err != nil {
		t.Fatal(err)
	}
	policy := productD09ReaderTestPolicy(base, uint32(os.Geteuid()))
	data, _, err := productD09ReadFileWithPolicy(path, 100, policy)
	if err != nil || string(data) != "trusted" {
		t.Fatalf("control read = %q, %v", data, err)
	}
	check := policy.checkDirectory
	replaced := false
	policy.checkDirectory = func(dir string, info fs.FileInfo) error {
		if err := check(dir, info); err != nil {
			return err
		}
		if dir == nested {
			if err := os.Rename(parent, filepath.Join(base, "retained")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(attacker, parent); err != nil {
				t.Fatal(err)
			}
			replaced = true
		}
		return nil
	}
	data, _, err = productD09ReadFileWithPolicy(path, 100, policy)
	if !replaced || err != nil || string(data) != "trusted" {
		t.Fatalf("read lost its verified parent: replaced=%t data=%q err=%v", replaced, data, err)
	}
	// A fresh read must refuse the replacement link, even with the fixture's ownership policy.
	policy.checkDirectory = check
	if _, _, err := productD09ReadFileWithPolicy(path, 100, policy); !errors.Is(err, ErrProductD09HostRefused) {
		t.Fatalf("fresh lookup followed replaced ancestor: %v", err)
	}
}

func TestProductD09ReaderRefusesUnsafeLeaves(t *testing.T) {
	for _, name := range []string{"symlink", "world writable", "group writable", "foreign owner", "missing", "oversized", "directory", "fifo"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "protected")
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "input")
			if err := os.WriteFile(path, []byte("trusted"), 0644); err != nil {
				t.Fatal(err)
			}
			policy := productD09ReaderTestPolicy(base, uint32(os.Geteuid()))
			data, _, err := productD09ReadFileWithPolicy(path, 7, policy)
			if err != nil || string(data) != "trusted" {
				t.Fatalf("control read = %q, %v", data, err)
			}
			switch name {
			case "world writable", "group writable":
				mode := fs.FileMode(0666)
				if name == "group writable" {
					mode = 0664
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			case "foreign owner":
				// Change only the trusted account to exercise the owner predicate without chown privileges.
				policy.checkFile = productD09ReaderTestPolicy(base, uint32(os.Geteuid())+1).checkFile
			case "oversized":
				if err := os.WriteFile(path, []byte("too large"), 0644); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				switch name {
				case "symlink":
					if err := os.Symlink(path+".original", path); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(path, 0755); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, _, err := productD09ReadFileWithPolicy(path, 7, policy); !errors.Is(err, ErrProductD09HostRefused) {
				t.Fatalf("unsafe leaf accepted: %v", err)
			}
		})
	}
}

func TestProductD09ReaderRefusesUnsafeAncestors(t *testing.T) {
	for _, name := range []string{"symlink", "world writable", "group writable", "foreign owner"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "protected")
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "input")
			if err := os.WriteFile(path, []byte("trusted"), 0644); err != nil {
				t.Fatal(err)
			}
			policy := productD09ReaderTestPolicy(base, uint32(os.Geteuid()))
			data, _, err := productD09ReadFileWithPolicy(path, 7, policy)
			if err != nil || string(data) != "trusted" {
				t.Fatalf("control read = %q, %v", data, err)
			}
			switch name {
			case "world writable", "group writable":
				mode := fs.FileMode(0777)
				if name == "group writable" {
					mode = 0775
				}
				if err := os.Chmod(dir, mode); err != nil {
					t.Fatal(err)
				}
			case "foreign owner":
				policy.checkDirectory = productD09ReaderTestPolicy(base, uint32(os.Geteuid())+1).checkDirectory
			case "symlink":
				if err := os.Rename(dir, dir+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+".original", dir); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := productD09ReadFileWithPolicy(path, 7, policy); !errors.Is(err, ErrProductD09HostRefused) || !strings.Contains(err.Error(), "protected ancestor") {
				t.Fatalf("unsafe ancestor was not refused at the ancestor: %v", err)
			}
		})
	}
}
