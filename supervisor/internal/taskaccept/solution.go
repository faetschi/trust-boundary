package taskaccept

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"tbound/supervisor/internal/trialfix"
)

// Operations of a reference solution.
const (
	SolWrite  = "write"
	SolRemove = "remove"
	SolMkdir  = "mkdir"
)

// Op is one step of a reference solution. write creates or replaces a regular file (Path,
// Mode, ContentB64, which may be empty); mkdir creates a directory that does not exist yet
// (Path, Mode, owner rwx required); remove deletes a file or a directory with everything
// under it (Path). Every parent must exist as a directory when the step runs. There is no
// way to name a symbolic link or a path outside the tree.
type Op struct {
	Op         string `json:"op"`
	Path       string `json:"path"`
	ContentB64 string `json:"content_b64,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

// solutionWire is the JSON form of a solution spec.
type solutionWire struct {
	Schema string `json:"schema"`
	Ops    []Op   `json:"ops"`
}

// SolutionSpec is a reference solution: ops applied in order to a copy of the fixture. Its
// members are unexported; obtain one with ParseSolution.
type SolutionSpec struct {
	ops []Op
}

// ParseSolution reads one solution spec with the strict rules of ParseAccept: unknown
// fields, trailing data, oversize input, a wrong schema, no op or more than MaxOps, an
// unknown op, a member the op does not use, an invalid path or mode, content that is not
// canonical base64 or is over trialfix.MaxFileBytes.
func ParseSolution(raw []byte) (SolutionSpec, error) {
	var s solutionWire
	if err := strictDecode(raw, &s, "solution spec"); err != nil {
		return SolutionSpec{}, err
	}
	if s.Schema != SolutionSchema {
		return SolutionSpec{}, fmt.Errorf("solution spec: schema %q, want %q", s.Schema, SolutionSchema)
	}
	if len(s.Ops) == 0 || len(s.Ops) > MaxOps {
		return SolutionSpec{}, fmt.Errorf("solution spec: %d ops, want 1 to %d", len(s.Ops), MaxOps)
	}
	for i, o := range s.Ops {
		if _, err := o.decode(); err != nil {
			return SolutionSpec{}, fmt.Errorf("solution spec: op %d (%s): %w", i, o.Op, err)
		}
	}
	return SolutionSpec{ops: s.Ops}, nil
}

// decode validates the op on its own and returns its content.
func (o Op) decode() (content []byte, err error) {
	if err := validPath(o.Path); err != nil {
		return nil, err
	}
	switch o.Op {
	case SolWrite, SolMkdir:
		mode, err := parseMode(o.Mode)
		if err != nil {
			return nil, err
		}
		if o.Op == SolMkdir {
			if o.ContentB64 != "" {
				return nil, fmt.Errorf("a directory has no content")
			}
			if mode&0o700 != 0o700 {
				return nil, fmt.Errorf("directory mode %s lacks owner rwx", o.Mode)
			}
			return nil, nil
		}
		content, err = base64.StdEncoding.DecodeString(o.ContentB64)
		if err != nil || base64.StdEncoding.EncodeToString(content) != o.ContentB64 {
			return nil, fmt.Errorf("content is not canonical base64")
		}
		if len(content) > trialfix.MaxFileBytes {
			return nil, fmt.Errorf("%d bytes, at most %d", len(content), trialfix.MaxFileBytes)
		}
		return content, nil
	case SolRemove:
		if o.Mode != "" || o.ContentB64 != "" {
			return nil, fmt.Errorf("remove takes only a path")
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown operation %q", o.Op)
}

// apply returns a copy of base with the ops applied, or the first step that cannot apply.
func (s SolutionSpec) apply(base tree) (tree, error) {
	t := make(tree, len(base))
	for p, n := range base {
		t[p] = n
	}
	for i, o := range s.ops {
		content, err := o.decode()
		if err != nil {
			return nil, fmt.Errorf("op %d (%s %s): %w", i, o.Op, o.Path, err)
		}
		fail := func(format string, a ...any) error {
			return fmt.Errorf("op %d (%s %s): %s", i, o.Op, o.Path, fmt.Sprintf(format, a...))
		}
		if o.Op != SolRemove {
			if k := strings.LastIndexByte(o.Path, '/'); k >= 0 && !t[o.Path[:k]].mode.IsDir() {
				return nil, fail("the parent is not a directory")
			}
		}
		existing, exists := t[o.Path]
		switch o.Op {
		case SolMkdir:
			if exists {
				return nil, fail("the path exists")
			}
			mode, _ := parseMode(o.Mode)
			t[o.Path] = node{mode: fs.ModeDir | fs.FileMode(mode)}
		case SolWrite:
			if exists && !existing.mode.IsRegular() {
				return nil, fail("the path is not a regular file")
			}
			mode, _ := parseMode(o.Mode)
			t[o.Path] = node{mode: fs.FileMode(mode), size: int64(len(content)), content: content}
		case SolRemove:
			if !exists {
				return nil, fail("the path does not exist")
			}
			delete(t, o.Path)
			for p := range t {
				if strings.HasPrefix(p, o.Path+"/") {
					delete(t, p)
				}
			}
		}
	}
	if len(t)-1 > trialfix.MaxEntries { // the root is not an entry
		return nil, fmt.Errorf("the result has %d entries, at most %d", len(t)-1, trialfix.MaxEntries)
	}
	var total int64
	for _, n := range t {
		total += n.size
	}
	if total > trialfix.MaxTotalBytes {
		return nil, fmt.Errorf("the result has %d bytes of content, at most %d", total, trialfix.MaxTotalBytes)
	}
	return t, nil
}

// Solved is the fixture with the solution applied, as a fixture spec: trialfix.Digest of it is the
// digest of the live tree that holds exactly the reference solution's result (owner, modes and
// content as the solution states them), so a retained result can be tied to that tree.
func (s SolutionSpec) Solved(fixture trialfix.Spec) (trialfix.Spec, error) {
	base, err := treeOfFixture(fixture)
	if err != nil {
		return trialfix.Spec{}, err
	}
	t, err := s.apply(base)
	if err != nil {
		return trialfix.Spec{}, fmt.Errorf("the reference solution does not apply to the fixture: %w", err)
	}
	out := trialfix.Spec{Schema: fixture.Schema}
	for p, n := range t {
		if p == rootPath {
			continue
		}
		e := trialfix.Entry{Path: p, Type: trialfix.TypeFile, Mode: fmt.Sprintf("%04o", n.mode.Perm()),
			ContentB64: base64.StdEncoding.EncodeToString(n.content)}
		if n.mode.IsDir() {
			e.Type, e.ContentB64 = trialfix.TypeDir, ""
		}
		out.Entries = append(out.Entries, e)
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Path < out.Entries[j].Path })
	if _, err := out.Files(); err != nil {
		return trialfix.Spec{}, err
	}
	return out, nil
}
