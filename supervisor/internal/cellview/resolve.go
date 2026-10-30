// Package cellview is what a process inside the cell measures of the path to a file it is not meant to
// read, and the check that decides whether file permissions alone would already have refused it.
//
// The cell is measured, not emulated: the facts are the lstat results the process itself gets for every
// component of the path, in the mount namespace it runs in, so mounts that replace image paths are part
// of them. Walk is the one definition of which components the measurement covers, run by the cell against
// its real file system (Measure) and again by the validator against the recorded facts (View.Problems).
//
// The path must hold no symbolic link. Linux refuses to follow a link owned by another user in a sticky
// world-writable directory (fs.protected_symlinks), a rule that file permissions do not describe, so a
// walk that passes a link could not tell a permission denial from a confinement denial.
package cellview

import (
	"fmt"
	"path"
	"slices"
)

// The kinds of a node.
const (
	KindDir   = "d"
	KindFile  = "f"
	KindLink  = "l"
	KindOther = "o"
)

// Lookup returns the kind of the component at p (lstat: a final symbolic link is not followed).
type Lookup func(p string) (string, error)

// Components returns "/", every directory below it on the way to target, and target itself, in the order
// the kernel looks them up. The kernel needs search permission on each of them but the last.
func Components(target string) ([]string, error) {
	if !path.IsAbs(target) || path.Clean(target) != target || target == "/" {
		return nil, fmt.Errorf("%q is not a clean absolute path of a file", target)
	}
	parts := []string{target}
	for p := path.Dir(target); ; p = path.Dir(p) {
		parts = append(parts, p)
		if p == "/" {
			break
		}
	}
	slices.Reverse(parts)
	return parts, nil
}

// Walk looks up every component of target and fails when one is a symbolic link, when a directory above
// the target is not a directory, or when the lookup fails.
func Walk(target string, lookup Lookup) error {
	parts, err := Components(target)
	if err != nil {
		return err
	}
	for i, p := range parts {
		kind, err := lookup(p)
		switch {
		case err != nil:
			return err
		case kind == KindLink:
			return fmt.Errorf("%s is a symbolic link: the path of the target must hold none, because file permissions do not describe the kernel's refusal to follow one (fs.protected_symlinks)", p)
		case i < len(parts)-1 && kind != KindDir:
			return fmt.Errorf("%s is not a directory", p)
		}
	}
	return nil
}
