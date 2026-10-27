//go:build linux

package sessionrepo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckProductPreflightRefusesOnlyProvablyUnexecutedCalls(t *testing.T) {
	store, journal, _, _ := newE2EStore(t)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureDir(t, filepath.Join(source, "build"), 0o755)
	writeFixtureFile(t, filepath.Join(source, "task.txt"), "protected content\n", 0o644)
	writeFixtureFile(t, filepath.Join(source, "build", "result.txt"), "build result\n", 0o644)
	g0, err := store.Seed(openFixtureRoot(t, source), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatal(err)
	}
	records := func() int {
		trace, err := journal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		return len(trace.Records)
	}
	before := records()
	edit := func(path string, edits ...Replacement) ProductPreflight {
		return ProductPreflight{Tool: "edit", Path: path, Edits: edits}
	}
	for _, c := range []struct {
		name    string
		input   ProductPreflight
		refusal string
		err     error
	}{
		{"missing read", ProductPreflight{Tool: "read", Path: "missing.txt", MaxBytes: 1024}, "read_missing", nil},
		{"existing read", ProductPreflight{Tool: "read", Path: "task.txt", MaxBytes: 1024}, "", nil},
		{"directory read stays out of scope", ProductPreflight{Tool: "read", Path: "build", MaxBytes: 1024}, "", nil},
		{"read below a file", ProductPreflight{Tool: "read", Path: "task.txt/child", MaxBytes: 1024}, "", ErrUnsupportedTarget},
		{"read outside the grammar", ProductPreflight{Tool: "read", Path: "../outside", MaxBytes: 1024}, "", ErrInvalidPath},
		{"read without a bound", ProductPreflight{Tool: "read", Path: "missing.txt"}, "", ErrOperationOutputLimit},
		{"write to a directory", ProductPreflight{Tool: "write", Path: "build", Content: []byte("x")}, "target_not_regular", nil},
		{"write to a file", ProductPreflight{Tool: "write", Path: "task.txt", Content: []byte("x")}, "", nil},
		{"write a new file", ProductPreflight{Tool: "write", Path: "new.txt", Content: []byte("x")}, "", nil},
		{"write below a missing parent stays out of scope", ProductPreflight{Tool: "write", Path: "missing/new.txt", Content: []byte("x")}, "", nil},
		{"edit a directory", edit("build", Replacement{OldText: "a", NewText: "b"}), "target_not_regular", nil},
		{"edit a missing file stays out of scope", edit("missing.txt", Replacement{OldText: "a", NewText: "b"}), "", nil},
		{"absent old text", edit("task.txt", Replacement{OldText: "nothing", NewText: "b"}), "edit_old_text_absent", nil},
		{"ambiguous old text", edit("task.txt", Replacement{OldText: "t", NewText: "b"}), "edit_old_text_ambiguous", nil},
		{"unique old text", edit("task.txt", Replacement{OldText: "protected", NewText: "b"}), "", nil},
		{"second edit absent", edit("task.txt", Replacement{OldText: "protected", NewText: "b"}, Replacement{OldText: "nothing", NewText: "c"}), "edit_old_text_absent", nil},
		{"overlapping edits stay out of scope", edit("task.txt", Replacement{OldText: "protected", NewText: "b"}, Replacement{OldText: "tected content", NewText: "c"}), "", ErrInvalidEdit},
		{"empty old text", edit("task.txt", Replacement{OldText: "", NewText: "b"}), "", ErrInvalidEdit},
		{"no edits", edit("task.txt"), "", ErrInvalidEdit},
		{"bash has no preflight", ProductPreflight{Tool: "bash", Path: "task.txt"}, "", ErrInvalidOptions},
	} {
		t.Run(c.name, func(t *testing.T) {
			refusal, err := store.CheckProductPreflight(g0.ID(), g0.TreeDigest(), c.input)
			switch {
			case c.err != nil:
				if !errors.Is(err, c.err) || refusal != nil {
					t.Fatalf("want error %v, got %+v %v", c.err, refusal, err)
				}
			case c.refusal == "":
				if err != nil || refusal != nil {
					t.Fatalf("want no refusal, got %+v %v", refusal, err)
				}
			default:
				if err != nil || refusal == nil || refusal.Code != c.refusal || refusal.Hint == "" || refusal.ArgumentDigest == "" {
					t.Fatalf("want refusal %s, got %+v %v", c.refusal, refusal, err)
				}
			}
		})
	}
	refusal, err := store.CheckProductPreflight(g0.ID(), g0.TreeDigest(), ProductPreflight{Tool: "read", Path: "missing.txt", MaxBytes: 1024})
	if err != nil || refusal == nil || refusal.ArgumentDigest != readArgumentsDigest("missing.txt", 1024) {
		t.Fatalf("refusal must bind the digest the effect path would use: %+v %v", refusal, err)
	}
	for _, tree := range []string{"sha256:" + strings.Repeat("0", 64), ""} {
		if refusal, err := store.CheckProductPreflight(g0.ID(), tree, ProductPreflight{Tool: "read", Path: "missing.txt", MaxBytes: 1024}); !errors.Is(err, ErrGenerationModified) || refusal != nil {
			t.Fatalf("wrong tree %q produced evidence: %+v %v", tree, refusal, err)
		}
	}
	if refusal, err := store.CheckProductPreflight("g404", g0.TreeDigest(), ProductPreflight{Tool: "read", Path: "missing.txt", MaxBytes: 1024}); !errors.Is(err, ErrGenerationModified) || refusal != nil {
		t.Fatalf("unknown generation produced evidence: %+v %v", refusal, err)
	}
	if after := records(); after != before {
		t.Fatalf("preflight changed the Store journal: %d -> %d records", before, after)
	}
	if err := os.Symlink(source, filepath.Join(g0.testPath(), "escape")); err != nil {
		t.Fatal(err)
	}
	if refusal, err := store.CheckProductPreflight(g0.ID(), g0.TreeDigest(), ProductPreflight{Tool: "read", Path: "missing.txt", MaxBytes: 1024}); err == nil || refusal != nil {
		t.Fatalf("modified sealed generation produced evidence: %+v %v", refusal, err)
	}
}
