package taskaccept

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"tbound/supervisor/internal/trialfix"
)

// Schemas of the documents of this package.
const (
	AcceptSchema   = "tbound-acceptance/v1"
	SolutionSchema = "tbound-solution/v1"
	ResultSchema   = "tbound-accept-result/v1"
)

// RouteTrialAcceptCell is the Route of a result captured by `tbound trial accept`: the case acceptance
// spec evaluated on the live root, node_test checks in the isolated acceptance cell.
const RouteTrialAcceptCell = "trial-accept-cell"

// Operations of a check.
const (
	OpFileSHA256      = "file_sha256"
	OpContains        = "contains"
	OpAbsent          = "absent"
	OpUnchangedExcept = "unchanged_except"
)

// Bounds of an acceptance or solution spec.
const (
	MaxSpecBytes   = 1 << 20
	MaxChecks      = 256
	MaxTextBytes   = 64 << 10
	MaxExceptPaths = trialfix.MaxEntries
	MaxOps         = 1024
)

// Check is one declarative check. Each operation uses only its own members: file_sha256
// uses Path and Digest, contains Path and Text, absent Path, unchanged_except Paths. Paths
// are relative and canonical (see validPath); Digest is "sha256:" and 64 lowercase hex
// digits; Text is non-empty UTF-8.
//
// unchanged_except names the paths that may differ from the fixture. The root directory is
// always compared (type and mode, never content) and cannot be listed. Every other path of the
// post-run tree must exist in the fixture with the same type, mode and content, and every
// other fixture path must still exist. An added file or directory is a change unless its own
// path is listed; a listed directory does not exempt what is under it. An empty Paths means
// nothing may change.
type Check struct {
	Op     string   `json:"op"`
	Path   string   `json:"path,omitempty"`
	Digest string   `json:"digest,omitempty"`
	Text   string   `json:"text,omitempty"`
	Paths  []string `json:"paths,omitempty"`
}

// acceptWire is the JSON form of an acceptance spec.
type acceptWire struct {
	Schema string  `json:"schema"`
	Checks []Check `json:"checks"`
}

// AcceptSpec is a parsed acceptance spec. Obtain one with ParseAccept, which records the
// digest of the bytes it came from. Its members are unexported so that what Evaluate runs is
// exactly what the digest names; the zero value is refused by Evaluate.
type AcceptSpec struct {
	checks []Check
	digest string
}

// Digest is "sha256:" and the SHA-256 of the bytes ParseAccept read.
func (s AcceptSpec) Digest() string { return s.digest }

// ValidateResult binds a retained result to every check of this parsed spec.
func (s AcceptSpec) ValidateResult(doc AcceptDoc, caseID string) error {
	if s.digest == "" || doc.Schema != ResultSchema || doc.CaseID != caseID || doc.SpecDigest != s.digest || len(doc.Results) != len(s.checks) {
		return errors.New("acceptance schema/case_id/spec_digest/result count mismatch")
	}
	all := true
	for i, check := range s.checks {
		path := check.Path
		if check.Op == OpUnchangedExcept {
			path = "."
		}
		result := doc.Results[i]
		if result.Op != check.Op || result.Path != path {
			return fmt.Errorf("acceptance result %d operation/path mismatch", i)
		}
		all = all && result.Passed
	}
	if doc.Passed != all {
		return errors.New("acceptance passed disagrees with results")
	}
	return nil
}

// DigestOf is the digest an artifact of these bytes carries: "sha256:" and the SHA-256 of raw.
func DigestOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseAccept reads one acceptance spec. It refuses input over MaxSpecBytes, unknown fields,
// data after the document, a wrong schema, no check or more than MaxChecks, an unknown
// operation, a member that the operation does not use, and an invalid path, digest or text.
func ParseAccept(raw []byte) (AcceptSpec, error) {
	var s acceptWire
	if err := strictDecode(raw, &s, "acceptance spec"); err != nil {
		return AcceptSpec{}, err
	}
	if s.Schema != AcceptSchema {
		return AcceptSpec{}, fmt.Errorf("acceptance spec: schema %q, want %q", s.Schema, AcceptSchema)
	}
	if len(s.Checks) == 0 || len(s.Checks) > MaxChecks {
		return AcceptSpec{}, fmt.Errorf("acceptance spec: %d checks, want 1 to %d", len(s.Checks), MaxChecks)
	}
	for i, c := range s.Checks {
		if err := c.validate(); err != nil {
			return AcceptSpec{}, fmt.Errorf("acceptance spec: check %d (%s): %w", i, c.Op, err)
		}
	}
	return AcceptSpec{checks: s.Checks, digest: DigestOf(raw)}, nil
}

func (c Check) validate() error {
	uses := map[string]bool{}
	switch c.Op {
	case OpFileSHA256:
		uses["path"], uses["digest"] = true, true
		if !digestRE.MatchString(c.Digest) {
			return fmt.Errorf("digest %q is not sha256 followed by 64 lowercase hex digits", c.Digest)
		}
	case OpContains:
		uses["path"], uses["text"] = true, true
		if c.Text == "" || len(c.Text) > MaxTextBytes || !utf8.ValidString(c.Text) {
			return fmt.Errorf("text must be 1 to %d bytes of UTF-8", MaxTextBytes)
		}
	case OpAbsent:
		uses["path"] = true
	case OpNodeTest:
		uses["path"], uses["text"] = true, true
		if c.Text == "" || len(c.Text) > MaxTextBytes || !utf8.ValidString(c.Text) {
			return fmt.Errorf("text must be 1 to %d bytes of UTF-8", MaxTextBytes)
		}
	case OpUnchangedExcept:
		uses["paths"] = true
		if len(c.Paths) > MaxExceptPaths {
			return fmt.Errorf("%d paths, at most %d", len(c.Paths), MaxExceptPaths)
		}
		seen := make(map[string]bool, len(c.Paths))
		for _, p := range c.Paths {
			if err := validPath(p); err != nil {
				return err
			}
			if seen[p] {
				return fmt.Errorf("path %q appears twice", p)
			}
			seen[p] = true
		}
	default:
		return fmt.Errorf("unknown operation %q", c.Op)
	}
	for member, set := range map[string]bool{"path": c.Path != "", "digest": c.Digest != "", "text": c.Text != "", "paths": len(c.Paths) != 0} {
		if set && !uses[member] {
			return fmt.Errorf("member %q is not used by this operation", member)
		}
	}
	if uses["path"] {
		return validPath(c.Path)
	}
	return nil
}

// strictDecode decodes the one JSON value in raw into v.
func strictDecode(raw []byte, v any, what string) error {
	if len(raw) > MaxSpecBytes {
		return fmt.Errorf("%s: more than %d bytes", what, MaxSpecBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: data after the document", what)
	}
	return nil
}
