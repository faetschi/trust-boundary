//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/vrun"
)

func newVForwarderTestDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "v-fwd")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, vrun.TokenName), []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mknod(filepath.Join(dir, vrun.SocketName), syscall.S_IFSOCK|0o600, 0); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCheckVForwarderDirAcceptsOnlyASocketAndATokenOfTheServiceUser(t *testing.T) {
	uid := uint32(os.Getuid())
	if err := checkVForwarderDir(newVForwarderTestDir(t), uid); err != nil {
		t.Fatalf("a well-formed unit directory: %v", err)
	}
	if err := checkVForwarderDir(newVForwarderTestDir(t), uid+1); err == nil {
		t.Error("a directory of another user was accepted")
	}
	replace := func(name string, build func(path string) error) func(dir string) error {
		return func(dir string) error {
			path := filepath.Join(dir, name)
			if err := os.Remove(path); err != nil {
				return err
			}
			return build(path)
		}
	}
	for name, mutate := range map[string]func(dir string) error{
		"socket is a regular file": replace(vrun.SocketName, func(p string) error { return os.WriteFile(p, nil, 0o600) }),
		"socket is a symlink": replace(vrun.SocketName, func(p string) error {
			return os.Symlink(filepath.Join(filepath.Dir(p), vrun.TokenName), p)
		}),
		"socket open to others": func(dir string) error { return os.Chmod(filepath.Join(dir, vrun.SocketName), 0o666) },
		"token is a socket": replace(vrun.TokenName, func(p string) error {
			return syscall.Mknod(p, syscall.S_IFSOCK|0o600, 0)
		}),
		"token is a symlink": replace(vrun.TokenName, func(p string) error {
			target := filepath.Join(filepath.Dir(filepath.Dir(p)), "elsewhere")
			if err := os.WriteFile(target, []byte("credential"), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, p)
		}),
		"token is a directory": replace(vrun.TokenName, func(p string) error { return os.Mkdir(p, 0o600) }),
		"token readable by others": func(dir string) error {
			return os.Chmod(filepath.Join(dir, vrun.TokenName), 0o644)
		},
	} {
		dir := newVForwarderTestDir(t)
		if err := mutate(dir); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := checkVForwarderDir(dir, uid); err == nil {
			t.Errorf("%s: the directory was accepted", name)
		}
	}
}

func TestVForwarderClientRunsUnderNoNewPrivs(t *testing.T) {
	cmd := vForwarderCommand("/usr/local/bin/tbound", "case-1", "req-1")
	want := []string{"/usr/bin/setpriv", "--no-new-privs", "--", "/usr/local/bin/tbound", "serve", "--pi", "--task-id", "case-1",
		"--attempt", "1", "--role", corpusRoleV, "--task-request-id", "req-1"}
	if !slices.Equal(cmd.Args, want) || cmd.Path != want[0] {
		t.Fatalf("command %q (path %q)", cmd.Args, cmd.Path)
	}
	if _, err := os.Stat(noNewPrivsWrap[0]); err != nil {
		t.Skip("setpriv is not installed here")
	}
	out, err := exec.Command(noNewPrivsWrap[0], append(noNewPrivsWrap[1:], "/bin/sh", "-c", "grep NoNewPrivs /proc/self/status")...).Output()
	if err != nil || !strings.Contains(string(out), "NoNewPrivs:\t1") {
		t.Fatalf("a process started through the wrap: %q, %v", out, err)
	}
}
