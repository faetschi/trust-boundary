package taskaccept

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/trialfix"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fixtureSpec is src/main.go, src/util.go, notes.txt and run.sh.
func fixtureSpec() trialfix.Spec {
	return trialfix.Spec{Schema: trialfix.Schema, Entries: []trialfix.Entry{
		{Path: "src", Type: trialfix.TypeDir, Mode: "0755"},
		{Path: "src/main.go", Type: trialfix.TypeFile, Mode: "0644", ContentB64: b64("package main\n")},
		{Path: "src/util.go", Type: trialfix.TypeFile, Mode: "0644", ContentB64: b64("package main\n// util\n")},
		{Path: "notes.txt", Type: trialfix.TypeFile, Mode: "0600", ContentB64: b64("todo\n")},
		{Path: "run.sh", Type: trialfix.TypeFile, Mode: "0755", ContentB64: b64("#!/bin/sh\n")},
	}}
}

// fixtureFS is the file system view of fixtureSpec.
func fixtureFS(t *testing.T) fstest.MapFS {
	t.Helper()
	files, err := fixtureSpec().Files()
	if err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{".": &fstest.MapFile{Mode: fs.ModeDir | fs.FileMode(trialfix.RootMode)}}
	for _, f := range files {
		if f.Dir {
			m[f.Path] = &fstest.MapFile{Mode: fs.ModeDir | fs.FileMode(f.Mode)}
		} else {
			m[f.Path] = &fstest.MapFile{Data: f.Content, Mode: fs.FileMode(f.Mode)}
		}
	}
	return m
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// accept parses an acceptance spec made of checks.
func accept(t *testing.T, checks ...Check) AcceptSpec {
	t.Helper()
	s, err := ParseAccept(marshal(t, acceptWire{Schema: AcceptSchema, Checks: checks}))
	if err != nil {
		t.Fatalf("ParseAccept: %v", err)
	}
	return s
}

func solution(t *testing.T, ops ...Op) SolutionSpec {
	t.Helper()
	s, err := ParseSolution(marshal(t, solutionWire{Schema: SolutionSchema, Ops: ops}))
	if err != nil {
		t.Fatalf("ParseSolution: %v", err)
	}
	return s
}

// evaluate runs one check against post and returns its result.
func evaluate(t *testing.T, post fs.FS, c Check) Result {
	t.Helper()
	doc, err := EvaluateWith("case-1", accept(t, c), fixtureSpec(), post, nil)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(doc.Results) != 1 || doc.Passed != doc.Results[0].Passed {
		t.Fatalf("doc = %+v", doc)
	}
	return doc.Results[0]
}
