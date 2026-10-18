package taskaccept

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"tbound/supervisor/internal/trialfix"
)

// Result is the outcome of one check. Path is "." for unchanged_except, which covers the
// whole tree.
type Result struct {
	Op     string `json:"op"`
	Path   string `json:"path"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// AcceptDoc is the result document of evaluating one acceptance spec: tbound-accept-result/v1.
// Passed is true only if every check passed.
type AcceptDoc struct {
	Schema     string   `json:"schema"`
	CaseID     string   `json:"case_id"`
	SpecDigest string   `json:"spec_digest"`
	Results    []Result `json:"results"`
	Passed     bool     `json:"passed"`
}

// FirstFailure describes the first failed check, or is empty when none failed.
func (d AcceptDoc) FirstFailure() string {
	for _, r := range d.Results {
		if !r.Passed {
			return fmt.Sprintf("%s %s: %s", r.Op, r.Path, r.Detail)
		}
	}
	return ""
}

// maxViolations bounds the paths that an unchanged_except detail names.
const maxViolations = 8

// Evaluate runs spec against post, the read-only view of the tree a run left behind, and
// fixture, the tree the run started from. A symbolic link in post is never followed: it is a
// non-regular file at its own path. It returns an error only when the spec was not produced
// by ParseAccept, the fixture is invalid, or post cannot be read; a failed check is a result.
func Evaluate(caseID string, spec AcceptSpec, fixture trialfix.Spec, post fs.FS) (AcceptDoc, error) {
	before, err := treeOfFixture(fixture)
	if err != nil {
		return AcceptDoc{}, err
	}
	after, err := treeOfFS(post)
	if err != nil {
		return AcceptDoc{}, err
	}
	return evaluateTree(caseID, spec, before, after)
}

func evaluateTree(caseID string, spec AcceptSpec, before, after tree) (AcceptDoc, error) {
	if spec.digest == "" {
		return AcceptDoc{}, errors.New("the acceptance spec was not produced by ParseAccept")
	}
	doc := AcceptDoc{Schema: ResultSchema, CaseID: caseID, SpecDigest: spec.digest, Passed: true,
		Results: make([]Result, 0, len(spec.checks))}
	for _, c := range spec.checks {
		r := Result{Op: c.Op, Path: c.Path}
		switch c.Op {
		case OpFileSHA256:
			r.Passed, r.Detail = checkSHA256(after, c)
		case OpContains:
			r.Passed, r.Detail = checkContains(after, c)
		case OpAbsent:
			r.Passed, r.Detail = checkAbsent(after, c)
		case OpUnchangedExcept:
			r.Path = "."
			r.Passed, r.Detail = checkUnchanged(before, after, c)
		default:
			return AcceptDoc{}, fmt.Errorf("unknown operation %q", c.Op)
		}
		doc.Passed = doc.Passed && r.Passed
		doc.Results = append(doc.Results, r)
	}
	return doc, nil
}

func checkSHA256(after tree, c Check) (bool, string) {
	n, ok := after[c.Path]
	if !ok {
		return false, "the file is missing"
	}
	if !n.mode.IsRegular() {
		return false, fmt.Sprintf("not a regular file (%s)", n.mode)
	}
	if n.size > MaxReadBytes {
		return false, fmt.Sprintf("%d bytes, more than the %d a check reads", n.size, MaxReadBytes)
	}
	f, err := n.open()
	if err != nil {
		return false, err.Error()
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, MaxReadBytes+1)); err != nil {
		return false, err.Error()
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != c.Digest {
		return false, fmt.Sprintf("digest is %s, want %s", got, c.Digest)
	}
	return true, "digest matches"
}

func checkContains(after tree, c Check) (bool, string) {
	n, ok := after[c.Path]
	if !ok {
		return false, "the file is missing"
	}
	data, err := n.read(MaxReadBytes)
	if err != nil {
		return false, err.Error()
	}
	if !bytes.Contains(data, []byte(c.Text)) {
		return false, "the text is not in the file"
	}
	return true, "the text is in the file"
}

func checkAbsent(after tree, c Check) (bool, string) {
	if n, ok := after[c.Path]; ok {
		return false, fmt.Sprintf("the path is present (%s)", n.mode)
	}
	return true, "the path is absent"
}

func checkUnchanged(before, after tree, c Check) (bool, string) {
	allowed := make(map[string]bool, len(c.Paths))
	for _, p := range c.Paths {
		allowed[p] = true
	}
	var changes []string
	for p, n := range after {
		if allowed[p] {
			continue
		}
		f, ok := before[p]
		switch {
		case !ok:
			changes = append(changes, p+" was added")
		case f.mode != n.mode:
			changes = append(changes, fmt.Sprintf("%s changed from %s to %s", p, f.mode, n.mode))
		case n.mode.IsRegular() && !sameContent(f, n):
			changes = append(changes, p+" has other content")
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok && !allowed[p] {
			changes = append(changes, p+" was deleted")
		}
	}
	if len(changes) == 0 {
		return true, "nothing else changed"
	}
	sort.Strings(changes)
	if extra := len(changes) - maxViolations; extra > 0 {
		changes = append(changes[:maxViolations], fmt.Sprintf("and %d more", extra))
	}
	return false, strings.Join(changes, "; ")
}

// sameContent reports whether post (a regular file) holds the bytes of the fixture file f.
// A file that cannot be read differs.
func sameContent(f, post node) bool {
	if f.size != post.size {
		return false
	}
	data, err := post.read(MaxReadBytes)
	return err == nil && bytes.Equal(data, f.content)
}
