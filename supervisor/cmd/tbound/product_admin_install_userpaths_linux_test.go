//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductAdminInstallScriptAsRootNeverFollowsUserControlledCompletionAndConfigPaths(t *testing.T) {
	skipUnlessInstallScriptRuns(t)
	f := newReleaseFixture(t)
	f.extra = map[string]string{"share/completions/tbound.bash": "# completion\n", "share/completions/_tbound": "#compdef tbound\n", "share/completions/tbound.fish": "# fish\n"}
	f.writeArchive("genuine")
	f.resign(f.keys)
	f.asRoot, f.shellSetup, f.completions = true, everythingIsRootOwned, true

	// The user redirects the XDG config directory and the bash completion leaf at files root must not touch.
	home := filepath.Join(f.dir, "home")
	victimConfig, victimFile := filepath.Join(f.dir, "victim-config"), filepath.Join(f.dir, "victim-completion")
	writeFileOrFatal(t, victimFile, "precious\n")
	if err := os.MkdirAll(victimConfig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimConfig, filepath.Join(home, "cfg")); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(home, ".local", "share", "bash-completion", "completions", "tbound")
	if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimFile, leaf); err != nil {
		t.Fatal(err)
	}

	out, prefix, err := f.install("--public-key", f.pub, "--verifier", f.verifier())
	if err != nil {
		t.Fatalf("a root install must succeed without the per-user files: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(victimFile); string(got) != "precious\n" {
		t.Fatalf("a root install wrote through a user-controlled completion link: %q", got)
	}
	if entries, _ := os.ReadDir(victimConfig); len(entries) != 0 {
		t.Fatalf("a root install wrote below a user-controlled config directory: %v", entries)
	}
	if _, statErr := os.Lstat(filepath.Join(home, ".local", "share", "zsh")); statErr == nil {
		t.Fatal("a root install created per-user completion directories")
	}
	if !strings.Contains(out, "as that user") || !strings.Contains(out, filepath.Join(prefix, "share", "completions")) {
		t.Fatalf("a root install must say what the user runs unprivileged:\n%s", out)
	}
}

func TestProductAdminInstallScriptAsAUserStillInstallsCompletionsAndConfig(t *testing.T) {
	skipUnlessInstallScriptRuns(t)
	f := newReleaseFixture(t)
	f.extra = map[string]string{"share/completions/tbound.bash": "# completion\n", "share/completions/_tbound": "#compdef tbound\n", "share/completions/tbound.fish": "# fish\n"}
	f.writeArchive("genuine")
	f.publishSums()
	f.completions = true

	out, _, err := f.install()
	if err != nil {
		t.Fatalf("user install: %v\n%s", err, out)
	}
	home := filepath.Join(f.dir, "home")
	for _, want := range []string{
		filepath.Join(home, ".local", "share", "bash-completion", "completions", "tbound"),
		filepath.Join(home, ".local", "share", "zsh", "site-functions", "_tbound"),
		filepath.Join(home, "cfg", "fish", "completions", "tbound.fish"),
		filepath.Join(home, "cfg", "tbound", "config.json"),
	} {
		if _, statErr := os.Stat(want); statErr != nil {
			t.Errorf("a user install did not write %s: %v", want, statErr)
		}
	}
}

func TestProductAdminUninstallScriptAsRootNeverRemovesThroughUserControlledCompletionPaths(t *testing.T) {
	base := t.TempDir()
	prefix := ownedPrefix(t, base)
	victim := filepath.Join(base, "victim-data")
	writeFileOrFatal(t, filepath.Join(victim, "completions", "tbound"), "precious\n")
	data := filepath.Join(base, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(data, "bash-completion")); err != nil {
		t.Fatal(err)
	}
	out, err := uninstallAsRoot(t, prefix, everythingIsRootOwned, "XDG_DATA_HOME="+data)
	if err != nil || exists(prefix) {
		t.Fatalf("a root uninstall of a trusted prefix failed: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(victim, "completions", "tbound")); string(got) != "precious\n" {
		t.Fatalf("a root uninstall removed a file through a user-controlled completion path: %q\n%s", got, out)
	}
	if !strings.Contains(out, "as that user") {
		t.Fatalf("a root uninstall must say what the user removes unprivileged:\n%s", out)
	}
}
