//go:build linux

package publication

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/effectoracle"
)

// The fence directory is the effect oracle's window marker on the live root. Publication opens
// and reads it just before it applies and just after, so every rename it makes is between the two
// fences in the observer's queue. Without the opening fence the apply cannot be placed, so
// publication must not apply: a partial apply would be unexplained.
func TestPublishRefusesToApplyWithoutTheFenceDirectory(t *testing.T) {
	base := map[string]string{"a.txt": "a"}
	middle := map[string]string{"a.txt": "b"}
	final := map[string]string{"a.txt": "c"}
	f := newFixture(t, base, base, middle, final, nil, nil, nil)
	if _, err := os.Lstat(filepath.Join(f.root, "live", effectoracle.FenceDir)); !os.IsNotExist(err) {
		t.Fatalf("the fixture has a fence directory: %v", err)
	}
	repo := f.acquire(1)
	defer repo.Close()
	result, err := repo.Publish(context.Background(), f.request, func(b CommitBinding) (Authorization, error) {
		return Authorization{DecisionID: "d", BindingDigest: b.BindingDigest}, nil
	}, nil)
	if err == nil {
		t.Fatalf("Publish applied without a fence directory: %+v", result)
	}
	if result.Evidence != nil && result.Evidence.FenceBursts != 0 {
		t.Fatalf("publication claims a fence it did not make: %+v", result.Evidence)
	}
	got, readErr := os.ReadFile(filepath.Join(f.root, "live", "a.txt"))
	if readErr != nil || string(got) != "a" {
		t.Fatalf("the live root was changed by a refused publication: %q, %v", got, readErr)
	}
}

// A proposal may not name the fence directory or anything below it: the directory is the effect
// oracle's, and a published change inside it would be invisible to the fixture digest.
func TestPublishRefusesAChangeUnderTheFenceDirectory(t *testing.T) {
	base := map[string]string{"a.txt": "a"}
	middle := map[string]string{"a.txt": "a", "lost+found/exfil": "secret"}
	final := map[string]string{"a.txt": "a", "lost+found/exfil": "secret"}
	f := newFixture(t, base, base, middle, final, []string{effectoracle.FenceDir}, []string{effectoracle.FenceDir}, []string{effectoracle.FenceDir})
	repo := f.acquire(1)
	defer repo.Close()
	_, err := repo.Publish(context.Background(), f.request, func(b CommitBinding) (Authorization, error) {
		return Authorization{DecisionID: "d", BindingDigest: b.BindingDigest}, nil
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "lost+found") {
		t.Fatalf("Publish accepted a change under %s: %v", effectoracle.FenceDir, err)
	}
	if _, statErr := os.Lstat(filepath.Join(f.root, "live", "lost+found", "exfil")); !os.IsNotExist(statErr) {
		t.Fatalf("a change under %s reached the live root: %v", effectoracle.FenceDir, statErr)
	}
}

func TestValidateRelativePathRefusesTheFenceDirectory(t *testing.T) {
	for _, path := range []string{"lost+found", "lost+found/exfil", "lost+found/a/b"} {
		if err := validateRelativePath(path); err == nil {
			t.Errorf("validateRelativePath(%q) accepted the fence directory", path)
		}
	}
	// A nested name is not the live root's fence and stays a normal path.
	for _, path := range []string{"src/lost+found", "lost+found.txt", "a/lost+found/b"} {
		if err := validateRelativePath(path); err != nil {
			t.Errorf("validateRelativePath(%q) = %v, want a normal path", path, err)
		}
	}
}
