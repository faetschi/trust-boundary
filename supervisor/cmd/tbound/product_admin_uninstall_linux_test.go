//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// uninstallAsRoot runs install.sh's `uninstall --prefix PREFIX` through main with `id -u` answering 0.
// setup is shell code that runs after the script was sourced: stubs, or a simulated race.
func uninstallAsRoot(t *testing.T, prefix, setup string, env ...string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	if setup == "" {
		setup = ":"
	}
	home := t.TempDir()
	script := `. "$1"; id() { if [ "${1:-}" = -u ]; then echo 0; else command id "$@"; fi; }; ` + setup + `; main uninstall --prefix "$2"`
	cmd := exec.Command(bash, "-c", script, "tbound-test", "../../../install/install.sh", prefix)
	cmd.Env = append([]string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_CONFIG_HOME=" + filepath.Join(home, "cfg")}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ownedPrefix creates a prefix directory that carries the install marker and one file.
func ownedPrefix(t *testing.T, dir string) string {
	t.Helper()
	prefix := filepath.Join(dir, "prefix")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".tbound-install", "bin/tbound"} {
		if err := os.WriteFile(filepath.Join(prefix, name), []byte("name=tbound\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return prefix
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// everythingIsRootOwned makes the root-run checks see a root-owned, non-writable tree, so a test that is
// not root can reach the checks that come after the ownership one.
const everythingIsRootOwned = `stat() { echo "0 755"; }`

func TestProductAdminUninstallScriptAsRootRefusesAPrefixBelowADirectoryAnotherUserOwns(t *testing.T) {
	prefix := ownedPrefix(t, t.TempDir())
	if os.Geteuid() == 0 {
		t.Skip("the prefix below this test's directory would be root-owned")
	}
	out, err := uninstallAsRoot(t, prefix, "")
	if err == nil || !strings.Contains(out, "refusing to act as root") || !strings.Contains(out, "owned by uid") {
		t.Fatalf("a root uninstall accepted a prefix whose ancestors another user owns: %v\n%s", err, out)
	}
	if !exists(filepath.Join(prefix, "bin", "tbound")) {
		t.Fatal("a refused root uninstall removed the prefix")
	}
}

func TestProductAdminUninstallScriptAsRootTakesTheMarkerAsATrustAnchor(t *testing.T) {
	prefix := ownedPrefix(t, t.TempDir())
	marker := filepath.Join(prefix, ".tbound-install")
	if err := os.Rename(marker, marker+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(marker+".real", marker); err != nil {
		t.Fatal(err)
	}
	out, err := uninstallAsRoot(t, prefix, everythingIsRootOwned)
	if err == nil || !strings.Contains(out, "refusing to act as root") || !strings.Contains(out, "is a symlink") {
		t.Fatalf("a root uninstall accepted a symlinked marker: %v\n%s", err, out)
	}
	if !exists(filepath.Join(prefix, "bin", "tbound")) {
		t.Fatal("a refused root uninstall removed the prefix")
	}

	good := ownedPrefix(t, t.TempDir())
	if out, err := uninstallAsRoot(t, good, everythingIsRootOwned); err != nil || exists(good) {
		t.Fatalf("a root uninstall of a trusted prefix failed: %v\n%s", err, out)
	}
}

func TestProductAdminUninstallScriptAsRootChecksAgainImmediatelyBeforeRemoving(t *testing.T) {
	base := t.TempDir()
	prefix := ownedPrefix(t, filepath.Join(base, "a"))
	victim := ownedPrefix(t, filepath.Join(base, "victim"))
	// Once the marker was accepted, the directory above the prefix becomes a symlink to the victim's.
	swap := `is_owned_prefix() { [ -f "$1/$MARKER" ] || return 1; [ -L "$SWAP" ] || { mv "$SWAP" "$SWAP.moved" && ln -s "$VICTIM" "$SWAP"; }; }`
	out, err := uninstallAsRoot(t, prefix, everythingIsRootOwned+"; "+swap, "SWAP="+filepath.Join(base, "a"), "VICTIM="+filepath.Join(base, "victim"))
	if err == nil || !strings.Contains(out, "refusing") {
		t.Fatalf("a root uninstall went on after an ancestor of the prefix became a symlink: %v\n%s", err, out)
	}
	if !exists(filepath.Join(victim, "bin", "tbound")) {
		t.Fatal("the root uninstall followed the swapped ancestor and deleted outside the prefix")
	}
}
