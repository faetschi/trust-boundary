//go:build linux

package publication

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/effectoracle"
)

const liveRootMountChildEnv = "TBOUND_PUBLICATION_MOUNT_CHILD"

// The managed live root is the "root" child of its own ext4 mount, so the mount
// root is the same-mount staging parent. A live root that is itself the mount
// point has its parent on another mount and must refuse before staging.
func TestPublicationLiveRootMountLayout(t *testing.T) {
	if os.Getenv(liveRootMountChildEnv) == "" {
		runInUserMountNamespace(t, "^TestPublicationLiveRootMountLayout$")
		return
	}
	enterPrivateTmpfsRoot(t)
	base := map[string]string{"file.txt": "before"}
	middle := map[string]string{"file.txt": "intermediate"}
	final := map[string]string{"file.txt": "after"}
	allow := func(binding CommitBinding) (Authorization, error) {
		return Authorization{DecisionID: "mount-layout-decision", BindingDigest: binding.BindingDigest}, nil
	}

	t.Run("mount root as live root refuses before staging", func(t *testing.T) {
		f := newFixture(t, base, base, middle, final, nil, nil, nil)
		livePath := filepath.Join(f.root, "live")
		mountPrivateTmpfs(t, livePath)
		reopenLiveRoot(t, f, livePath)
		before := directoryNames(t, f.root)
		repo := f.acquire(1)
		_, err := repo.Publish(context.Background(), f.request, allow, nil)
		_ = repo.Close()
		if err == nil || !strings.Contains(err.Error(), "one verified mount") {
			t.Fatalf("live root whose parent is another mount was not refused at the mount check: %v", err)
		}
		if after := directoryNames(t, f.root); !slices.Equal(before, after) {
			t.Fatalf("refused publication staged in the parent: before=%v after=%v", before, after)
		}
	})

	t.Run("fixed child of the mount root publishes", func(t *testing.T) {
		f := newFixture(t, base, base, middle, final, nil, nil, nil)
		mountRoot := filepath.Join(f.root, "image")
		if err := os.Mkdir(mountRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		mountPrivateTmpfs(t, mountRoot)
		livePath := filepath.Join(mountRoot, "root")
		makeTree(t, livePath, base, nil)
		reopenLiveRoot(t, f, livePath)
		repo := f.acquire(1)
		result, err := repo.Publish(context.Background(), f.request, allow, nil)
		if closeErr := repo.Close(); err == nil {
			err = closeErr
		}
		if err != nil || result.Status != StatusSucceeded {
			t.Fatalf("publication into the fixed child of the mount root failed: result=%+v err=%v", result, err)
		}
		if got, err := os.ReadFile(filepath.Join(livePath, "file.txt")); err != nil || string(got) != "after" {
			t.Fatalf("published live file=%q err=%v", got, err)
		}
		// The seeded live root holds the fence directory (effectoracle.FenceDir, the one the
		// operator's live_dir creates beside the fixture content), so both fences are made and
		// the EFF-TRANSIENT oracle can place the transaction's events between them.
		if result.Evidence == nil || result.Evidence.FenceDir != effectoracle.FenceDir || result.Evidence.FenceBursts != 2 {
			t.Fatalf("publication into the fixed child of the mount root recorded evidence %+v, want fence directory %q and 2 bursts", result.Evidence, effectoracle.FenceDir)
		}
	})
}

func runInUserMountNamespace(t *testing.T, pattern string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run="+pattern, "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), liveRootMountChildEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Skipf("unprivileged user and mount namespaces are unavailable: %v", err)
	}
	if err != nil {
		t.Fatalf("mount-namespace child failed: %v\n%s", err, out)
	}
	if bytes.Contains(out, []byte("--- SKIP")) {
		t.Skipf("mount-namespace child skipped:\n%s", out)
	}
	t.Logf("mount-namespace child:\n%s", out)
}

// enterPrivateTmpfsRoot chroots the namespace child into a fresh tmpfs, so every
// ancestor of the fixture is owned by the mapped root the audit journal trusts.
// The host's real root appears as the overflow UID inside the user namespace.
func enterPrivateTmpfsRoot(t *testing.T) {
	t.Helper()
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Skipf("cannot make the mount namespace private: %v", err)
	}
	newRoot, err := os.MkdirTemp("", "publication-mount-root-")
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount("tbound-publication-root", newRoot, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0755"); err != nil {
		t.Skipf("cannot mount tmpfs in the test namespace: %v", err)
	}
	for _, dir := range []string{"proc", "tmp"} {
		if err := os.Mkdir(filepath.Join(newRoot, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mount("/proc", filepath.Join(newRoot, "proc"), "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		t.Skipf("cannot bind /proc into the test root: %v", err)
	}
	if err := syscall.Chroot(newRoot); err != nil {
		t.Skipf("cannot chroot into the test root: %v", err)
	}
	if err := os.Chdir("/"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", "/tmp")
}

func mountPrivateTmpfs(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mount("tbound-publication-test", path, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0700"); err != nil {
		t.Skipf("cannot mount tmpfs in the test namespace: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(path, syscall.MNT_DETACH) })
}

func reopenLiveRoot(t *testing.T, f *fixture, path string) {
	t.Helper()
	live, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.live.Close()
	f.live = live
	f.request.LiveRoot = live
}

func directoryNames(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
