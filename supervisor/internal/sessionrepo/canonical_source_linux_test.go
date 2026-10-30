//go:build linux

package sessionrepo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/workspace"
)

// Seed normalizes what it imports, so a managed root holding a 0600 fixture is admitted, but the
// release rescan and the publication baseline scan demand the canonical form. RequireCanonicalSource
// refuses such a root right after the seed instead of after the whole run.
func TestRequireCanonicalSourceRefusesARootThatLaterScansWouldRefuse(t *testing.T) {
	for name, test := range map[string]struct {
		mode      os.FileMode
		canonical bool
	}{
		"0644 file": {0o644, true},
		"0755 file": {0o755, true},
		"0600 file": {0o600, false},
		"0640 file": {0o640, false},
		"0700 file": {0o700, false},
	} {
		store, _, _, _ := newE2EStore(t)
		sourcePath := filepath.Join(t.TempDir(), "source")
		if err := os.Mkdir(sourcePath, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, filepath.Join(sourcePath, "SMOKE-README.txt"), "fixture\n", test.mode)
		if err := os.Chmod(filepath.Join(sourcePath, "SMOKE-README.txt"), test.mode); err != nil {
			t.Fatal(err)
		}
		source := openFixtureRoot(t, sourcePath)
		g0, err := store.Seed(source, RootAttestation{Quiescent: true})
		if err != nil {
			t.Fatalf("%s: seed: %v", name, err)
		}
		err = store.RequireCanonicalSource(source, g0)
		if test.canonical && err != nil {
			t.Errorf("%s: canonical root refused: %v", name, err)
		}
		if !test.canonical && !errors.Is(err, workspace.ErrNoncanonicalMode) {
			t.Errorf("%s: non-canonical root = %v, want ErrNoncanonicalMode", name, err)
		}
	}
}

func TestRequireCanonicalSourceRefusesARootThatChangedAfterTheSeed(t *testing.T) {
	store, _, _, _ := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(sourcePath, "a.txt"), "one\n", 0o644)
	source := openFixtureRoot(t, sourcePath)
	g0, err := store.Seed(source, RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(sourcePath, "a.txt"), "two\n", 0o644)
	if err := store.RequireCanonicalSource(source, g0); err == nil {
		t.Fatal("a root whose contents changed after the seed was accepted")
	}
	if err := store.RequireCanonicalSource(nil, g0); err == nil {
		t.Fatal("a nil source was accepted")
	}
}
