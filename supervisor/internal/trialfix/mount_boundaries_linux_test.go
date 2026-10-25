//go:build linux

package trialfix_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/wsoracle"
)

func TestTreeWalkMountBoundaries(t *testing.T) {
	const childEnv = "TBOUND_TREE_WALK_MOUNT_CHILD"
	if os.Getenv(childEnv) != "1" {
		cmd := exec.Command("/proc/self/exe", "-test.run=^TestTreeWalkMountBoundaries$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("user/mount namespace regression child failed: %v\n%s", err, out)
		}
		t.Logf("user/mount namespace regression child:\n%s", out)
		return
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"bind-directory", "bind-file", "tmpfs", "fence-bind", "fence-child-bind"} {
		t.Run(kind, func(t *testing.T) {
			root, target, _ := mountBoundaryTree(t, kind)
			rel, err := filepath.Rel(root, target)
			if err != nil {
				t.Fatal(err)
			}
			held, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			for _, snapshot := range []struct {
				name string
				read func() wsoracle.Manifest
			}{
				{"Snapshot", func() wsoracle.Manifest { return wsoracle.Snapshot(root, "mount") }},
				{"SnapshotDir", func() wsoracle.Manifest { return wsoracle.SnapshotDir(held, "mount") }},
			} {
				t.Run(snapshot.name, func(t *testing.T) {
					manifest := snapshot.read()
					if manifest.Complete {
						t.Error("mount crossing reported a complete snapshot")
					}
					found := false
					for _, gap := range manifest.Gaps {
						found = found || gap.Kind == wsoracle.GapMountBoundary && gap.Path == rel
					}
					if !found {
						t.Errorf("missing mount_boundary gap at %q: %+v", rel, manifest.Gaps)
					}
					for _, entry := range manifest.Entries {
						if strings.HasPrefix(entry.Path, rel+"/") || entry.Path == rel && entry.Type != wsoracle.TypeUnreadable {
							t.Errorf("snapshot read mounted entry %+v", entry)
						}
					}
					if _, err := trialfix.TreeDigest(manifest); err == nil {
						t.Error("TreeDigest accepted a tree containing a mount")
					}
				})
			}
			t.Run("census", func(t *testing.T) {
				observation, err := piruntime.MeasureD09XattrEnumeration(held)
				if !errors.Is(err, syscall.EXDEV) || observation != (piruntime.D09XattrEnumerationObservation{}) {
					t.Fatalf("census must refuse the mount with no observation: got %+v, %v", observation, err)
				}
			})
			for _, operation := range []string{"Wipe", "Reset", "ResetPinned"} {
				t.Run(operation, func(t *testing.T) {
					root, _, outsideSentinel := mountBoundaryTree(t, kind)
					before := mountTreeContents(t, root)
					owner := trialfix.Owner{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
					spec := trialfix.Spec{Schema: trialfix.Schema, Entries: []trialfix.Entry{{Path: "replacement", Type: trialfix.TypeFile, Mode: "0600"}}}
					var err error
					switch operation {
					case "Wipe":
						err = trialfix.Wipe(root, owner)
					case "Reset":
						err = trialfix.Reset(root, spec, owner)
					case "ResetPinned":
						var st syscall.Stat_t
						if err := syscall.Stat(root, &st); err != nil {
							t.Fatal(err)
						}
						err = trialfix.ResetPinned(root, spec, owner, trialfix.RootIdentity{Device: uint64(st.Dev), Inode: st.Ino})
					}
					if err == nil {
						t.Error("operation accepted a mounted tree")
					}
					if got, err := os.ReadFile(outsideSentinel); err != nil || string(got) != "outside" {
						t.Errorf("bind source sentinel did not survive: %q, %v", got, err)
					}
					if after := mountTreeContents(t, root); !reflect.DeepEqual(before, after) {
						t.Errorf("refused operation changed the tree: before=%v after=%v", before, after)
					}
				})
			}
		})
	}
}

func mountBoundaryTree(t *testing.T, kind string) (root, target, outsideSentinel string) {
	t.Helper()
	root = t.TempDir()
	outside := t.TempDir()
	outsideSentinel = filepath.Join(outside, "sentinel")
	for _, file := range []struct{ path, content string }{
		{filepath.Join(root, "first"), "keep-first"},
		{filepath.Join(root, "last"), "keep-last"},
		{outsideSentinel, "outside"},
	} {
		if err := os.WriteFile(file.path, []byte(file.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target = filepath.Join(root, "nested", "mount")
	if kind == "fence-bind" {
		target = filepath.Join(root, trialfix.LostFound)
	} else if kind == "fence-child-bind" {
		target = filepath.Join(root, trialfix.LostFound, "mount")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	source, filesystem, flags := outside, "", uintptr(syscall.MS_BIND)
	if kind == "bind-file" {
		source = outsideSentinel
		if err := os.WriteFile(target, []byte("covered"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if kind == "tmpfs" {
		source, filesystem, flags = "tmpfs", "tmpfs", 0
	}
	if err := syscall.Mount(source, target, filesystem, flags, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Unmount(target, 0); err != nil {
			t.Error(err)
		}
	})
	if kind == "tmpfs" {
		if err := os.WriteFile(filepath.Join(target, "sentinel"), []byte("mounted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var rootStat, mountStat syscall.Stat_t
	if err := syscall.Stat(root, &rootStat); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat(target, &mountStat); err != nil {
		t.Fatal(err)
	}
	if (rootStat.Dev == mountStat.Dev) != (kind != "tmpfs") {
		t.Fatal("fixture did not establish the intended device boundary")
	}
	return root, target, outsideSentinel
}

func mountTreeContents(t *testing.T, root string) map[string]string {
	t.Helper()
	contents := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents[rel] = fmt.Sprintf("%v", info.Mode())
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			contents[rel] += ":" + string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
