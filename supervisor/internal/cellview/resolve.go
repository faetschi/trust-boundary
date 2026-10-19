// Package cellview is what a process inside the cell measures of the path to a file it is not meant to
// read, and the check that decides whether file permissions alone would already have refused it.
//
// The cell is measured, not emulated: the facts are the lstat results the process itself gets for every
// component of the path, in the mount namespace it runs in, so mounts that replace image paths are part
// of them. Resolve is the one implementation of the kernel's path walk (symbolic links, "." and ".."),
// run by the cell against its real file system (Measure) and again by the validator against the recorded
// facts (View.Problems), so both agree on which directories the walk had to search.
package cellview

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// The kinds of a node.
const (
	KindDir   = "d"
	KindFile  = "f"
	KindLink  = "l"
	KindOther = "o"
)

// MaxLinks is the number of symbolic links one walk may follow (the kernel's limit).
const MaxLinks = 40

// Node is what lstat shows of a component: its kind and, for a symbolic link, its text.
type Node struct {
	Kind string
	Link string
}

// Lookup returns the node at p without following a final symbolic link. p is absolute and holds no
// symbolic link, "." or "..": the walk resolves them before it asks.
type Lookup func(p string) (Node, error)

// Walk is the outcome of resolving a path.
type Walk struct {
	// Resolved is the file the path names once every symbolic link is followed.
	Resolved string
	// ListDir is the directory that holds the last name of the path as written (the directory a listing
	// of "the directory of the path" opens).
	ListDir string
	// Searched are the directories in which the walk looked up a name, "." and ".." included, in order of
	// first use. The kernel needs search permission on each of them. A directory left by ".." is among
	// them: the kernel checks it before it steps to the parent.
	Searched []string
}

type resolver struct {
	lookup   Lookup
	nodes    map[string]Node
	searched []string
	seen     map[string]bool
	links    int
}

// Resolve walks target (a clean absolute path) the way the kernel does.
func Resolve(target string, lookup Lookup) (Walk, error) {
	var w Walk
	if !path.IsAbs(target) || path.Clean(target) != target || target == "/" {
		return w, fmt.Errorf("%q is not a clean absolute path of a file", target)
	}
	r := &resolver{lookup: lookup, nodes: map[string]Node{}, seen: map[string]bool{}}
	root, err := r.node("/")
	if err != nil {
		return w, err
	}
	if root.Kind != KindDir {
		return w, errors.New("/ is not a directory")
	}
	dir, last, err := r.walk("/", strings.Split(path.Dir(target), "/"))
	if err != nil {
		return w, err
	}
	if last.Kind != KindDir {
		return w, fmt.Errorf("%s is not a directory", dir)
	}
	resolved, _, err := r.walk(dir, []string{path.Base(target)})
	if err != nil {
		return w, err
	}
	return Walk{Resolved: resolved, ListDir: dir, Searched: r.searched}, nil
}

func (r *resolver) node(p string) (Node, error) {
	if n, ok := r.nodes[p]; ok {
		return n, nil
	}
	n, err := r.lookup(p)
	if err != nil {
		return Node{}, err
	}
	r.nodes[p] = n
	return n, nil
}

func (r *resolver) search(dir string) {
	if !r.seen[dir] {
		r.seen[dir] = true
		r.searched = append(r.searched, dir)
	}
}

func hasName(names []string) bool {
	for _, n := range names {
		if n != "" {
			return true
		}
	}
	return false
}

// walk resolves names below cur (a directory without symbolic links) and returns where it ends and the
// node there.
func (r *resolver) walk(cur string, names []string) (string, Node, error) {
	last := Node{Kind: KindDir}
	for len(names) > 0 {
		name := names[0]
		names = names[1:]
		if name == "" {
			continue
		}
		r.search(cur)
		switch name {
		case ".":
			continue
		case "..":
			cur, last = path.Dir(cur), Node{Kind: KindDir}
			continue
		}
		next := path.Join(cur, name)
		n, err := r.node(next)
		if err != nil {
			return "", Node{}, err
		}
		switch n.Kind {
		case KindLink:
			r.links++
			if r.links > MaxLinks {
				return "", Node{}, errors.New("too many levels of symbolic links")
			}
			if n.Link == "" {
				return "", Node{}, fmt.Errorf("the symbolic link %s is empty", next)
			}
			if strings.HasPrefix(n.Link, "/") {
				cur = "/"
			}
			last = Node{Kind: KindDir}
			names = append(strings.Split(n.Link, "/"), names...)
		case KindDir:
			cur, last = next, n
		default:
			if hasName(names) {
				return "", Node{}, fmt.Errorf("%s is not a directory", next)
			}
			return next, n, nil
		}
	}
	return cur, last, nil
}
