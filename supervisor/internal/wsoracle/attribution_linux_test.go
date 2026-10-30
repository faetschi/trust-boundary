package wsoracle

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests run the real Snapshot over a real tree and apply one operation
// between the pre-publication and post-publication snapshots.

func TestSnapshotRecordsTheInodeAndRenameKeepsIt(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a.txt"), "X")
	before := mustEntry(t, Snapshot(root, "before"), "a.txt")
	if before.Ino == 0 {
		t.Fatal("inode not recorded")
	}
	if err := os.Rename(filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	after := mustEntry(t, Snapshot(root, "after"), "b.txt")
	if after.Ino != before.Ino {
		t.Fatalf("inode %d became %d across a rename", before.Ino, after.Ino)
	}
	if fingerprint(&before) != fingerprint(&after) {
		t.Fatal("the inode must not take part in the equality key")
	}
}

func TestEndToEndAttributionPerPredicate(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, live string)
		apply func(t *testing.T, live string)
		want  map[string]string
	}{
		{
			name:  "overwrite is content",
			apply: func(t *testing.T, live string) { writeTree(t, filepath.Join(live, "keep.txt"), "CHANGED") },
			want:  map[string]string{"keep.txt": PredicateWS01},
		},
		{
			name:  "create is content",
			apply: func(t *testing.T, live string) { writeTree(t, filepath.Join(live, "new.txt"), "N") },
			want:  map[string]string{"new.txt": PredicateWS01},
		},
		{
			name: "delete",
			apply: func(t *testing.T, live string) {
				if err := os.Remove(filepath.Join(live, "keep.txt")); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]string{"keep.txt": PredicateWS02},
		},
		{
			name: "rename",
			apply: func(t *testing.T, live string) {
				if err := os.Rename(filepath.Join(live, "keep.txt"), filepath.Join(live, "moved.txt")); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]string{"keep.txt": PredicateWS03, "moved.txt": PredicateWS03},
		},
		{
			name:  "rename replacement",
			setup: func(t *testing.T, live string) { writeTree(t, filepath.Join(live, "other.txt"), "OTHER") },
			apply: func(t *testing.T, live string) {
				if err := os.Rename(filepath.Join(live, "other.txt"), filepath.Join(live, "keep.txt")); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]string{"keep.txt": PredicateWS03, "other.txt": PredicateWS03},
		},
		{
			name: "mode change",
			apply: func(t *testing.T, live string) {
				if err := os.Chmod(filepath.Join(live, "keep.txt"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]string{"keep.txt": PredicateWS04},
		},
		{
			name: "symlink created",
			apply: func(t *testing.T, live string) {
				if err := os.Symlink("keep.txt", filepath.Join(live, "link")); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]string{"link": PredicateWS04},
		},
		{
			name: "hard link created",
			apply: func(t *testing.T, live string) {
				if err := os.Link(filepath.Join(live, "keep.txt"), filepath.Join(live, "hard")); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]string{"hard": PredicateWS04, "keep.txt": PredicateWS04}, // the new path, and the link count of the object it links
		},
		{
			name: "directory created is not attributed",
			apply: func(t *testing.T, live string) {
				if err := os.Mkdir(filepath.Join(live, "d"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]string{"d": ""},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.setup != nil {
				c.setup(t, f.live)
			}
			r := ws05Observed(t, f, nil, func(live string) { c.apply(t, live) })
			wantVerdict(t, r, VerdictEffectPresent)
			if len(r.Findings) != len(c.want) {
				t.Fatalf("findings = %+v, want paths %v", r.Findings, c.want)
			}
			for p, pred := range c.want {
				wantPredicate(t, r, p, pred)
			}
		})
	}
}
