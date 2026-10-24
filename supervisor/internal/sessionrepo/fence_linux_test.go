//go:build linux

package sessionrepo

import (
	"errors"
	"testing"
)

// The fence directory of a managed live root is the effect oracle's, not workspace content: the
// session view must not read or write it, and a proposal under it is not a workspace path.
func TestSessionPathsRefuseTheFenceDirectory(t *testing.T) {
	for _, path := range []string{"lost+found", "lost+found/exfil", "lost+found/a/b"} {
		if err := validateRelativePath(path); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("validateRelativePath(%q) = %v, want ErrInvalidPath", path, err)
		}
	}
	for _, path := range []string{"src/lost+found", "lost+found.txt", "a/lost+found/b"} {
		if err := validateRelativePath(path); err != nil {
			t.Errorf("validateRelativePath(%q) = %v, want a normal path", path, err)
		}
	}
}
