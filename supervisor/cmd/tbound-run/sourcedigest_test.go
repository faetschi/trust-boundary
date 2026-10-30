package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// source-digest prints the digest the host profile pins as SourceProvenanceDigest and the run pins as
// frozen_source_digest: the archive tree digest evq-capture records for the same commit (one definition).
func TestSourceDigestIsTheArchiveTreeDigestOfTheCommit(t *testing.T) {
	requireCaptureTools(t)
	files := baseMini()
	repo, c := makeMiniRepo(t, files, nil)
	var out bytes.Buffer
	if err := runSourceDigest(context.Background(), repo, c, "git", &out); err != nil {
		t.Fatal(err)
	}
	want := expectedTreeDigest(files)
	if !strings.Contains(out.String(), "source_digest "+want+"\n") || !strings.Contains(out.String(), "commit "+c+"\n") {
		t.Fatalf("output = %q, want source_digest %s", out.String(), want)
	}
	// The same commit gives the same digest, and another commit another.
	files["docs/note.md"] = miniFile{"changed\n", 0o644}
	repo2, c2 := makeMiniRepo(t, files, nil)
	var out2 bytes.Buffer
	if err := runSourceDigest(context.Background(), repo2, c2, "git", &out2); err != nil {
		t.Fatal(err)
	}
	if out2.String() == out.String() || !strings.Contains(out2.String(), expectedTreeDigest(files)) {
		t.Fatalf("a changed file gave %q", out2.String())
	}
}

func TestSourceDigestRefusesWhatIsNotAnExactCommit(t *testing.T) {
	requireCaptureTools(t)
	repo, c := makeMiniRepo(t, baseMini(), nil)
	for name, commit := range map[string]string{"an abbreviation": c[:12], "a branch": "HEAD", "empty": "", "a missing commit": strings.Repeat("1", 40)} {
		var out bytes.Buffer
		if err := runSourceDigest(context.Background(), repo, commit, "git", &out); err == nil || out.Len() != 0 {
			t.Errorf("%s: accepted (%v, %q)", name, err, out.String())
		}
	}
	var out bytes.Buffer
	if err := runSourceDigest(context.Background(), filepath.Join(repo, "missing"), c, "git", &out); err == nil {
		t.Error("a repository that does not exist was read")
	}
}

// The pin of run.json is the capture's: a profile pinned to any other source does not belong to the commit the
// tests ran on.
func TestRequireFrozenSourceMatchesTheCapture(t *testing.T) {
	rec := evqCapture{ArchiveTreeDigest: fixtureDigest("tree of F")}
	if err := requireFrozenSource(fixtureDigest("tree of F"), rec); err != nil {
		t.Fatal(err)
	}
	for name, pin := range map[string]string{"another source": fixtureDigest("tree of another commit"), "none": ""} {
		if err := requireFrozenSource(pin, rec); err == nil || !strings.Contains(err.Error(), "source-digest") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
