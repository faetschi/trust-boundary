//go:build linux

package projectbundle

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSecurityDefaultApplyDoesNotWriteWorktree(t *testing.T) {
	f := newFixture(t, nil)
	f.edit(t, func(m string) { put(t, m, "a.txt", "changed\n", 0600) })
	raw, _ := f.export(t)
	before := treeDigest(t, f.worktree)
	_, err := ApplyExport(bytes.NewReader(raw), f.worktree, ApplyOptions{StateDir: f.state, Limits: DefaultLimits()})
	must(t, err)
	if treeDigest(t, f.worktree) != before {
		t.Fatal("default apply wrote the worktree")
	}
	patch := filepath.Join(f.state, "tbound-export.patch")
	git(t, f.worktree, "apply", "--check", patch)
	git(t, f.worktree, "apply", patch)
	if got, _ := os.ReadFile(filepath.Join(f.worktree, "a.txt")); string(got) != "changed\n" {
		t.Fatalf("applied content = %q", got)
	}
}

func TestSecurityRefusesXattrs(t *testing.T) {
	for _, path := range []string{"a.txt", "dir", "."} {
		t.Run(path, func(t *testing.T) {
			f := newFixture(t, nil)
			if err := unix.Setxattr(filepath.Join(f.worktree, path), "user.tbound-test", []byte("private"), 0); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if _, err := Build(f.worktree, BuildOptions{Limits: DefaultLimits()}, &out); err == nil {
				t.Fatal("accepted unsupported extended attributes")
			}
			if _, _, err := BuildExport(f.baseline, f.worktree, DefaultLimits(), &out); err == nil || !strings.Contains(err.Error(), "extended attribute") {
				t.Fatalf("export scan accepted extended attributes: %v", err)
			}
		})
	}
}

func TestSecurityRefusesHiddenGitFlags(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			f := newFixture(t, nil)
			f.edit(t, func(m string) { put(t, m, "a.txt", "changed\n", 0600) })
			raw, _ := f.export(t)
			git(t, f.worktree, "update-index", flag, "a.txt")
			if _, err := f.apply(bytes.NewReader(raw)); !errors.Is(err, ErrDirtyWorktree) {
				t.Fatalf("accepted hidden Git flag: %v", err)
			}
		})
	}
}

func TestSecurityGitAdministrationCaseFold(t *testing.T) {
	for _, name := range []string{".GIT", ".Git", ".gIt"} {
		t.Run(name, func(t *testing.T) {
			root := privateDir(t)
			put(t, root, name+"/config", "secret", 0600)
			var out bytes.Buffer
			result, err := Build(root, BuildOptions{Limits: DefaultLimits()}, &out)
			must(t, err)
			f := newFixture(t, nil)
			f.edit(t, func(m string) { put(t, m, "a.txt", "changed", 0600) })
			raw, _ := f.export(t)
			bad := reframe(t, raw, func(h *ExportHeader) { h.Changes[0].Path = "src/" + name + "/config" })
			if _, err := f.apply(bytes.NewReader(bad)); !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("apply accepted Git administration: %v", err)
			}
			if len(result.Manifest.Entries) != 0 {
				t.Fatal("Git administration was imported")
			}
			if err := validateManifestPath("src/" + name + "/config"); err == nil || !strings.Contains(err.Error(), "Git") {
				t.Fatalf("accepted Git administration: %v", err)
			}
			_, _, err = Import(bytes.NewReader(craft(t, []Entry{{Path: name, Type: TypeDir}}, "")), filepath.Join(privateDir(t), "import"), DefaultLimits())
			if !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("import accepted Git administration: %v", err)
			}
		})
	}
}

func TestSecurityDirectRequiresWriterExclusion(t *testing.T) {
	f := newFixture(t, nil)
	f.edit(t, func(m string) { put(t, m, "a.txt", "changed", 0600) })
	raw, _ := f.export(t)
	_, err := ApplyExport(bytes.NewReader(raw), f.worktree, ApplyOptions{Mode: DirectMode, StateDir: f.state, Limits: DefaultLimits()})
	if err == nil || !strings.Contains(err.Error(), "writer exclusion") {
		t.Fatalf("direct mode accepted without exclusion: %v", err)
	}
}

func TestSecurityDirectRefusesMovedParent(t *testing.T) {
	for _, earlier := range []bool{false, true} {
		t.Run(fmt.Sprint(earlier), func(t *testing.T) {
			f := newFixture(t, nil)
			f.edit(t, func(m string) {
				if earlier {
					put(t, m, "a.txt", "first change", 0600)
				}
				put(t, m, "dir/b.sh", "changed script", 0600)
			})
			raw, _ := f.export(t)
			moved := filepath.Join(privateDir(t), "outside")
			at := 2
			if earlier {
				at = 3
			}
			hook := &seekHook{Reader: bytes.NewReader(raw), at: at, fn: func() { must(t, os.Rename(filepath.Join(f.worktree, "dir"), moved)) }}
			_, err := f.apply(hook)
			if !errors.Is(err, ErrParentChanged) {
				t.Fatalf("moved parent accepted: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(moved, "b.sh"))
			must(t, err)
			if string(got) != "#!/bin/sh\n" {
				t.Fatalf("outside target overwritten: %q", got)
			}
			data, err := os.ReadFile(filepath.Join(f.state, recoveryRecordName))
			must(t, err)
			var record recoveryRecord
			must(t, json.Unmarshal(data, &record))
			if earlier && !reflect.DeepEqual(record.Changed, []string{filepath.Join(f.worktree, "a.txt")}) {
				t.Fatalf("actual paths = %v", record.Changed)
			}
			for _, name := range listDir(t, moved) {
				if strings.HasPrefix(name, ".tbound-apply-") {
					t.Fatal("temporary file left outside")
				}
			}
		})
	}
}

func TestSecurityPatchBinaryModesCreatesDeletes(t *testing.T) {
	f := newFixture(t, func(w string) {
		put(t, w, "binary.dat", "old\x00data\xff", 0644)
		git(t, w, "add", "-A")
		git(t, w, "commit", "-qm", "binary")
	})
	f.edit(t, func(m string) {
		put(t, m, "binary.dat", "new\x00binary\x80\xff", 0600)
		put(t, m, "nested/new 'quoted'.dat", "created\x00\xff", 0700)
		put(t, m, "a.txt", "text changed\n", 0600)
		must(t, os.Chmod(filepath.Join(m, "tool.sh"), 0600))
		must(t, os.Chmod(filepath.Join(m, "dir/b.sh"), 0700))
		must(t, os.Remove(filepath.Join(m, "old.txt")))
	})
	raw, header := f.export(t)
	before := treeDigest(t, f.worktree)
	_, err := ApplyExport(bytes.NewReader(raw), f.worktree, ApplyOptions{StateDir: f.state, Limits: DefaultLimits()})
	must(t, err)
	if treeDigest(t, f.worktree) != before {
		t.Fatal("patch mode mutated worktree")
	}
	patch := filepath.Join(f.state, PatchFileName)
	git(t, f.worktree, "apply", "--check", patch)
	git(t, f.worktree, "apply", patch)
	if treeDigest(t, f.worktree) != header.TargetDigest {
		t.Fatal("git apply did not produce target content and modes")
	}
}

func TestSecurityRefusesTargetNotAtHEAD(t *testing.T) {
	f := newFixture(t, nil)
	put(t, f.worktree, "a.txt", "committed original", 0644)
	git(t, f.worktree, "add", "a.txt")
	git(t, f.worktree, "commit", "-qm", "new HEAD")
	if err := checkGit(f.worktree, []Change{{Path: "a.txt", Type: TypeFile, Op: OpModify}}); err != nil {
		t.Fatal(err)
	}
	// A matching stat cache can suppress status without either hidden-file flag.
	put(t, f.worktree, "a.txt", "alpha\n", 0644)
	// Substitute a successful status result only; real ls-tree and hash-object still run.
	realGit, err := exec.LookPath("git")
	must(t, err)
	bin := privateDir(t)
	script := "#!/bin/sh\nfor arg do if [ \"$arg\" = status ]; then exit 0; fi; done\nexec '" + realGit + "' \"$@\"\n"
	must(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700))
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := checkGit(f.worktree, []Change{{Path: "a.txt", Type: TypeFile, Op: OpModify}}); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("HEAD byte mismatch accepted: %v", err)
	}
}

func TestSecurityMetadataRecheckAndACLs(t *testing.T) {
	for _, attr := range []string{"user.tbound-test", "system.posix_acl_access", "system.posix_acl_default"} {
		t.Run(attr, func(t *testing.T) {
			f := newFixture(t, nil)
			target := "a.txt"
			original := "alpha\n"
			if attr == "system.posix_acl_default" {
				target = "dir/b.sh"
				original = "#!/bin/sh\n"
			}
			f.edit(t, func(m string) { put(t, m, target, "changed", 0600) })
			raw, _ := f.export(t)
			path := filepath.Join(f.worktree, target)
			if attr == "system.posix_acl_default" {
				path = filepath.Join(f.worktree, "dir")
			}
			value := []byte("private")
			if strings.HasPrefix(attr, "system.") {
				// Linux POSIX ACL xattr version 2 with a named user and restrictive mask.
				value = make([]byte, 4+5*8)
				binary.LittleEndian.PutUint32(value, 2)
				for i, e := range []struct {
					tag, perm uint16
					id        uint32
				}{{1, 6, 0xffffffff}, {2, 4, uint32(os.Geteuid() + 1)}, {4, 0, 0xffffffff}, {16, 4, 0xffffffff}, {32, 0, 0xffffffff}} {
					b := value[4+i*8:]
					binary.LittleEndian.PutUint16(b, e.tag)
					binary.LittleEndian.PutUint16(b[2:], e.perm)
					binary.LittleEndian.PutUint32(b[4:], e.id)
				}
			}
			must(t, unix.Setxattr(path, attr, value, 0))
			var out bytes.Buffer
			if _, err := Build(f.worktree, BuildOptions{Limits: DefaultLimits()}, &out); err == nil {
				t.Fatal("Build accepted ACL/xattr")
			}
			if _, err := f.apply(bytes.NewReader(raw)); err == nil {
				t.Fatal("apply accepted ACL/xattr")
			}
			must(t, unix.Removexattr(path, attr))
			// Introduce metadata after the scan, during content copying before rename.
			hook := &seekHook{Reader: bytes.NewReader(raw), at: 2, fn: func() { must(t, unix.Setxattr(path, attr, value, 0)) }}
			if _, err := f.apply(hook); err == nil {
				t.Fatal("apply target recheck accepted late ACL/xattr")
			}
			got, _ := os.ReadFile(filepath.Join(f.worktree, target))
			if string(got) != original {
				t.Fatal("metadata refusal changed target")
			}
		})
	}
}

func TestSecurityOpenat2MountRefusalWithoutRoot(t *testing.T) {
	_, err := resolveWith(func(_ int, _ string, how *unix.OpenHow) (int, error) {
		want := uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV)
		if how.Resolve != want {
			t.Fatalf("resolve flags = %x", how.Resolve)
		}
		return -1, unix.EXDEV
	}, -1, "bind-mounted-file", unix.O_RDONLY, 0)
	if !errors.Is(err, unix.EXDEV) || !strings.Contains(err.Error(), "mount point") {
		t.Fatalf("mount refusal = %v", err)
	}
}

func TestSecurityBindMounts(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("bind-mount regression requires root and CAP_SYS_ADMIN; normal-user WSL cannot mount")
	}
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			runtime.LockOSThread()
			// The locked thread exits with this goroutine, discarding its private namespace.
			if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
				t.Skipf("private mount namespace unavailable: %v", err)
			}
			must(t, unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""))
			f := newFixture(t, nil)
			f.edit(t, func(m string) { put(t, m, "a.txt", "changed", 0600) })
			raw, _ := f.export(t)
			source := filepath.Join(privateDir(t), "source")
			target := filepath.Join(f.worktree, "a.txt")
			if directory {
				must(t, os.Mkdir(source, 0700))
				put(t, source, "b.sh", "#!/bin/sh\n", 0644)
				target = filepath.Join(f.worktree, "dir")
			} else {
				must(t, os.WriteFile(source, []byte("alpha\n"), 0644))
			}
			if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
				t.Skipf("bind mount unavailable: %v", err)
			}
			defer unix.Unmount(target, 0)
			var out bytes.Buffer
			if _, err := Build(f.worktree, BuildOptions{Limits: DefaultLimits()}, &out); err == nil || !strings.Contains(err.Error(), "mount") {
				t.Fatalf("Build accepted bind mount: %v", err)
			}
			if _, _, err := BuildExport(f.baseline, f.worktree, DefaultLimits(), &out); err == nil || !strings.Contains(err.Error(), "mount") {
				t.Fatalf("export scan accepted bind mount: %v", err)
			}
			if _, err := f.apply(bytes.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "mount") {
				t.Fatalf("apply accepted bind mount: %v", err)
			}
			root, err := openRoot(f.worktree)
			must(t, err)
			defer root.Close()
			if _, err := openBeneath(root, filepath.Base(target), unix.O_RDONLY); !errors.Is(err, unix.EXDEV) {
				t.Fatalf("apply resolver accepted bind mount: %v", err)
			}
		})
	}
}

func TestSecurityChmodSyncFailureKeepsRecovery(t *testing.T) {
	f := newFixture(t, nil)
	f.edit(t, func(m string) { must(t, os.Chmod(filepath.Join(m, "tool.sh"), 0600)) })
	raw, _ := f.export(t)
	synced := false
	_, err := ApplyExport(bytes.NewReader(raw), f.worktree, ApplyOptions{Mode: DirectMode, WriterExclusionHeld: true, StateDir: f.state, Limits: DefaultLimits(), syncFile: func(file *os.File) error { synced = true; return unix.EIO }})
	if !synced || !errors.Is(err, unix.EIO) {
		t.Fatalf("chmod did not propagate file Sync failure: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(f.state, recoveryRecordName))
	must(t, err)
	var record recoveryRecord
	must(t, json.Unmarshal(data, &record))
	if !reflect.DeepEqual(record.Changed, []string{filepath.Join(f.worktree, "tool.sh")}) {
		t.Fatalf("recovery paths = %v", record.Changed)
	}
}

func TestSecurityApplyStatePermissionsAndPinnedFD(t *testing.T) {
	for _, mode := range []os.FileMode{0755, 0777} {
		t.Run(mode.String(), func(t *testing.T) {
			f := newFixture(t, nil)
			must(t, os.Chmod(f.state, mode))
			if store, err := openApplyState(f.state, f.worktree); err == nil {
				store.dir.Close()
				t.Fatal("insecure state directory accepted")
			}
		})
	}
	t.Run("symlink ancestor", func(t *testing.T) {
		f := newFixture(t, nil)
		link := filepath.Join(privateDir(t), "link")
		must(t, os.Symlink(filepath.Dir(f.state), link))
		if store, err := openApplyState(filepath.Join(link, filepath.Base(f.state)), f.worktree); err == nil {
			store.dir.Close()
			t.Fatal("symlink ancestor accepted")
		}
	})
	t.Run("writable non-root ancestor", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("non-root ancestor owner fixture requires normal user")
		}
		f := newFixture(t, nil)
		ancestor := privateDir(t)
		state := filepath.Join(ancestor, "state")
		must(t, os.Mkdir(state, 0700))
		must(t, os.Chmod(ancestor, 0777))
		if store, err := openApplyState(state, f.worktree); err == nil {
			store.dir.Close()
			t.Fatal("writable non-root ancestor accepted")
		}
	})
	t.Run("pinned fd", func(t *testing.T) {
		f := newFixture(t, nil)
		f.edit(t, func(m string) { put(t, m, "a.txt", "changed", 0600) })
		raw, _ := f.export(t)
		moved := f.state + "-moved"
		t.Cleanup(func() { os.RemoveAll(moved) })
		hook := &seekHook{Reader: bytes.NewReader(raw), at: 2, fn: func() {
			must(t, os.Rename(f.state, moved))
			must(t, os.Mkdir(f.state, 0700))
			must(t, os.WriteFile(filepath.Join(f.state, recoveryRecordName), []byte("sentinel"), 0600))
		}}
		_, err := f.apply(hook)
		must(t, err)
		data, err := os.ReadFile(filepath.Join(f.state, recoveryRecordName))
		must(t, err)
		if string(data) != "sentinel" {
			t.Fatal("recovery operation followed replaced directory path")
		}
		if _, err := os.Stat(filepath.Join(moved, recoveryRecordName)); !os.IsNotExist(err) {
			t.Fatalf("held record not removed: %v", err)
		}
	})
}

func TestSecurityRecoveryTracksMovedEarlierParent(t *testing.T) {
	f := newFixture(t, nil)
	f.edit(t, func(m string) { put(t, m, "dir/b.sh", "changed", 0600); put(t, m, "keep.txt", "changed too", 0600) })
	raw, _ := f.export(t)
	outside := filepath.Join(privateDir(t), "moved")
	hook := &seekHook{Reader: bytes.NewReader(raw), at: 3, fn: func() {
		must(t, os.Rename(filepath.Join(f.worktree, "dir"), outside))
		must(t, os.WriteFile(filepath.Join(f.worktree, "keep.txt"), []byte("concurrent"), 0644))
	}}
	_, err := f.apply(hook)
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("expected recovery error: %v", err)
	}
	data, err := os.ReadFile(applyErr.Record)
	must(t, err)
	var record recoveryRecord
	must(t, json.Unmarshal(data, &record))
	if !reflect.DeepEqual(record.Changed, []string{filepath.Join(outside, "b.sh")}) {
		t.Fatalf("recovery names stale path: %v", record.Changed)
	}
}

func TestSecurityPatchRefusesEmptyDirectoryChanges(t *testing.T) {
	for _, op := range []string{"create", "delete", "retain"} {
		t.Run(op, func(t *testing.T) {
			f := newFixture(t, func(w string) {
				if op == "delete" {
					put(t, w, "empty/", "", 0)
				}
			})
			f.edit(t, func(m string) {
				switch op {
				case "create":
					put(t, m, "empty/", "", 0)
				case "delete":
					must(t, os.Remove(filepath.Join(m, "empty")))
				case "retain":
					must(t, os.Remove(filepath.Join(m, "dir/b.sh")))
				}
				put(t, m, "a.txt", "changed", 0600)
			})
			raw, _ := f.export(t)
			before := treeDigest(t, f.worktree)
			_, err := ApplyExport(bytes.NewReader(raw), f.worktree, ApplyOptions{StateDir: f.state, Limits: DefaultLimits()})
			if err == nil || !strings.Contains(err.Error(), "empty directory") {
				t.Fatalf("unrepresentable patch accepted: %v", err)
			}
			if treeDigest(t, f.worktree) != before {
				t.Fatal("refusal changed worktree")
			}
			if entries := listDir(t, f.state); len(entries) != 0 {
				t.Fatalf("refusal left output: %v", entries)
			}
		})
	}
}

func TestSecurityApplyRechecksAncestorMetadata(t *testing.T) {
	for _, ancestor := range []string{".", "dir"} {
		t.Run(ancestor, func(t *testing.T) {
			f := newFixture(t, func(w string) {
				put(t, w, "dir/sub/f", "original", 0644)
				git(t, w, "add", "-A")
				git(t, w, "commit", "-qm", "nested")
			})
			f.edit(t, func(m string) { put(t, m, "dir/sub/f", "changed", 0600) })
			raw, _ := f.export(t)
			hook := &seekHook{Reader: bytes.NewReader(raw), at: 2, fn: func() {
				must(t, unix.Setxattr(filepath.Join(f.worktree, ancestor), "user.private", []byte("metadata"), 0))
			}}
			if _, err := f.apply(hook); err == nil {
				t.Fatal("late ancestor metadata accepted")
			}
			if data, _ := os.ReadFile(filepath.Join(f.worktree, "dir/sub/f")); string(data) != "original" {
				t.Fatal("refusal changed target")
			}
		})
	}
}
