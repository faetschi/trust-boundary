//go:build linux

package piruntime

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// corpusManifestTree builds <base>/etc/corpus/manifest.json under a private base
// and returns the policy that exempts only the base (and its ancestors) from the
// owner and mode rule, as the credential-file tests do.
func corpusManifestTree(t *testing.T, content []byte) (string, corpusAncestorPolicy) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "etc", "corpus")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(base, "etc"), dir} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, corpusAncestorPolicy{trustedUID: uint32(os.Geteuid()), exemptThrough: base}
}

func TestCorpusManifestReadsARegularRootOwnedFile(t *testing.T) {
	want := []byte(`{"schema_version":"x"}`)
	path, policy := corpusManifestTree(t, want)
	got, err := readCorpusManifestFile(path, 1<<20, policy)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read = %q, %v", got, err)
	}
}

func TestCorpusManifestRefusesASymlinkedFinalComponent(t *testing.T) {
	path, policy := corpusManifestTree(t, []byte("{}"))
	link := filepath.Join(filepath.Dir(path), "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readCorpusManifestFile(link, 1<<20, policy); err == nil {
		t.Fatal("a symlinked manifest was read")
	}
}

func TestCorpusManifestRefusesASymlinkedAncestor(t *testing.T) {
	path, policy := corpusManifestTree(t, []byte("{}"))
	base := policy.exemptThrough
	if err := os.Symlink(filepath.Join(base, "etc", "corpus"), filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := readCorpusManifestFile(filepath.Join(base, "alias", "manifest.json"), 1<<20, policy); err == nil {
		t.Fatal("a manifest reached through a symlinked directory was read")
	}
	if _, err := readCorpusManifestFile(path, 1<<20, policy); err != nil {
		t.Fatalf("the real path stopped working: %v", err)
	}
}

func TestCorpusManifestRefusesWritableAncestorsAndFiles(t *testing.T) {
	for name, mutate := range map[string]func(path string){
		"group-writable directory": func(p string) { _ = os.Chmod(filepath.Dir(p), 0o775) },
		"world-writable parent":    func(p string) { _ = os.Chmod(filepath.Dir(filepath.Dir(p)), 0o757) },
		"group-writable file":      func(p string) { _ = os.Chmod(p, 0o664) },
		"world-writable file":      func(p string) { _ = os.Chmod(p, 0o646) },
		"setuid file":              func(p string) { _ = os.Chmod(p, os.ModeSetuid|0o644) },
	} {
		path, policy := corpusManifestTree(t, []byte("{}"))
		mutate(path)
		if _, err := readCorpusManifestFile(path, 1<<20, policy); err == nil {
			t.Errorf("%s: manifest was read", name)
		}
	}
}

func TestCorpusManifestRefusesAnUntrustedOwner(t *testing.T) {
	path, policy := corpusManifestTree(t, []byte("{}"))
	policy.trustedUID = uint32(os.Geteuid()) + 1
	if _, err := readCorpusManifestFile(path, 1<<20, policy); err == nil {
		t.Fatal("a file or directory owned by another uid was trusted")
	}
}

func TestCorpusManifestRefusesBadSizesAndSpellings(t *testing.T) {
	path, policy := corpusManifestTree(t, []byte("{}"))
	if _, err := readCorpusManifestFile(path, 1, policy); err == nil {
		t.Error("an oversize manifest was read")
	}
	empty, emptyPolicy := corpusManifestTree(t, nil)
	if _, err := readCorpusManifestFile(empty, 1<<20, emptyPolicy); err == nil {
		t.Error("an empty manifest was read")
	}
	for _, spelling := range []string{"", "relative/manifest.json", path + "/", filepath.Dir(path) + "/../corpus/manifest.json", "/"} {
		if _, err := readCorpusManifestFile(spelling, 1<<20, policy); err == nil {
			t.Errorf("non-canonical spelling %q was read", spelling)
		}
	}
}

func TestProductionCorpusManifestPolicyExemptsNothing(t *testing.T) {
	if corpusManifestPolicy.trustedUID != 0 || corpusManifestPolicy.exemptThrough != "" {
		t.Fatalf("production corpus policy must be root-owned with no exemption: %+v", corpusManifestPolicy)
	}
	if _, err := ReadD09ManagedPiCorpusManifest(); err == nil && os.Geteuid() != 0 {
		t.Fatal("the fixed production path was readable without the root-owned tree")
	}
}
