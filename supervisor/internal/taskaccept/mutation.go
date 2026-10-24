package taskaccept

import (
	"fmt"
	"regexp"
	"slices"

	"tbound/supervisor/internal/trialfix"
)

// MutationResult records rejection separately from execution errors: a broken runner
// cannot count as evidence that an oracle rejected a mutation.
type MutationResult struct {
	Mutation string `json:"mutation"`
	Path     string `json:"path"`
	Rejected bool   `json:"rejected"`
	Reason   string `json:"reason"`
	Error    string `json:"error,omitempty"`
}

var functionDecl = regexp.MustCompile(`\bfunction\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)

// MutationControls corrupts each reference-written file independently, leaving
// the other solved files intact. It never writes a host file or runs a trial script.
// The runner has the same trusted-corpus-only contract as SelfCheckWith.
func MutationControls(fixture trialfix.Spec, accept AcceptSpec, solution SolutionSpec, runner NodeRunner) []MutationResult {
	base, err := treeOfFixture(fixture)
	if err != nil {
		return []MutationResult{{Error: err.Error()}}
	}
	solved, err := solution.apply(base)
	if err != nil {
		return []MutationResult{{Error: err.Error()}}
	}
	paths := []string{}
	for _, op := range solution.ops {
		if op.Op == SolWrite && !slices.Contains(paths, op.Path) {
			paths = append(paths, op.Path)
		}
	}
	if len(paths) == 0 {
		return []MutationResult{{Error: "no reference-written file; file mutation controls are not applicable"}}
	}
	var results []MutationResult
	for _, path := range paths {
		n, ok := solved[path]
		if !ok || !n.mode.IsRegular() {
			results = append(results, MutationResult{Path: path, Error: "reference-written file is absent from solved tree"})
			continue
		}
		wrong := []byte("incorrect-value\n")
		for _, c := range accept.checks {
			if c.Op == OpNodeTest && c.Path == path {
				wrong = []byte("null;\n")
				if match := functionDecl.FindSubmatch(n.content); match != nil {
					wrong = append(append([]byte{}, n.content...), []byte("\n"+string(match[1])+" = () => null;\n")...)
				}
			}
		}
		for _, mutation := range []string{"empty", "wrong-value", "truncated", "another-path"} {
			post := make(tree, len(solved)+1)
			for p, node := range solved {
				post[p] = node
			}
			m := n
			switch mutation {
			case "empty":
				m.content = nil
			case "wrong-value":
				m.content = wrong
			case "truncated":
				m.content = n.content[:len(n.content)/2]
			case "another-path":
				other := path + ".construct-moved"
				for i := 1; ; i++ {
					if _, exists := post[other]; !exists {
						break
					}
					other = fmt.Sprintf("%s.construct-moved-%d", path, i)
				}
				post[other] = n
				if original, exists := base[path]; exists {
					post[path] = original
				} else {
					delete(post, path)
				}
			}
			if mutation != "another-path" {
				m.size = int64(len(m.content))
				post[path] = m
			}
			doc, err := evaluateTree("mutation", accept, base, post, runner)
			r := MutationResult{Mutation: mutation, Path: path, Rejected: !doc.Passed && err == nil, Reason: doc.FirstFailure()}
			if err != nil {
				r.Error = err.Error()
			} else if doc.Passed {
				r.Reason = "acceptance witness accepted the corrupted reference"
			}
			results = append(results, r)
		}
	}
	return results
}

// WitnessValues are the exact output values used by the existing read/bash leak
// checks, plus contains markers and reference code for behavioural witnesses.
func WitnessValues(accept AcceptSpec, solution SolutionSpec) []string {
	var values []string
	for _, c := range accept.checks {
		if c.Op == OpContains {
			values = append(values, c.Text)
		}
	}
	for _, op := range solution.ops {
		if op.Op == SolWrite {
			content, _ := op.decode()
			values = append(values, string(content))
		}
	}
	return values
}
