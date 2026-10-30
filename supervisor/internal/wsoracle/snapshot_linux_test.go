package wsoracle

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// writeTree creates a file with the given content and any missing parents.
func writeTree(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// skipIfRoot skips permission-denial fixtures, which root bypasses.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-denial fixtures need a non-root user")
	}
}

// restoreModes puts modes back before t.TempDir cleanup removes the tree.
func restoreModes(t *testing.T, paths ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, p := range paths {
			_ = os.Chmod(p, 0o700)
		}
	})
}

func findEntry(m Manifest, path string) (Entry, bool) {
	for _, e := range m.Entries {
		if e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

func wantComplete(t *testing.T, m Manifest) {
	t.Helper()
	if !m.clean() {
		t.Fatalf("manifest %q is not clean: complete=%v gaps=%+v", m.Label, m.Complete, m.Gaps)
	}
}

func wantGapKind(t *testing.T, m Manifest, kind, path string) {
	t.Helper()
	if m.Complete {
		t.Fatalf("manifest %q is marked complete despite gap %s at %q", m.Label, kind, path)
	}
	for _, g := range m.Gaps {
		if g.Kind == kind && g.Path == path {
			if g.Manifest != m.Label {
				t.Fatalf("gap manifest = %q, want %q", g.Manifest, m.Label)
			}
			return
		}
	}
	t.Fatalf("no gap kind=%s path=%q in %+v", kind, path, m.Gaps)
}

func TestSnapshotRecordsFilesDirectoriesAndHashesContent(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a.txt"), "alpha")
	writeTree(t, filepath.Join(root, "sub", "b.txt"), "beta")
	m := Snapshot(root, "baseline")
	wantComplete(t, m)

	a, ok := findEntry(m, "a.txt")
	if !ok || a.Type != TypeFile || a.Size != 5 || a.SHA256 != digestOf("alpha") {
		t.Fatalf("a.txt entry = %+v (found=%v)", a, ok)
	}
	sub, ok := findEntry(m, "sub")
	if !ok || sub.Type != TypeDir {
		t.Fatalf("sub entry = %+v (found=%v)", sub, ok)
	}
	b, ok := findEntry(m, "sub/b.txt")
	if !ok || b.Type != TypeFile || b.SHA256 != digestOf("beta") {
		t.Fatalf("sub/b.txt entry = %+v (found=%v)", b, ok)
	}
	if len(m.Entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(m.Entries), m.Entries)
	}
}

func TestSnapshotRecordsSymlinksWithoutFollowingThem(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	writeTree(t, filepath.Join(outside, "secret.txt"), "SECRET")
	writeTree(t, filepath.Join(root, "sub", "b.txt"), "beta")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "dlink")); err != nil {
		t.Fatal(err)
	}
	m := Snapshot(root, "baseline")
	wantComplete(t, m)

	link, ok := findEntry(m, "link")
	if !ok || link.Type != TypeSymlink || link.Target != filepath.Join(outside, "secret.txt") || link.SHA256 != "" {
		t.Fatalf("link entry = %+v (found=%v)", link, ok)
	}
	dlink, ok := findEntry(m, "dlink")
	if !ok || dlink.Type != TypeSymlink {
		t.Fatalf("dlink entry = %+v (found=%v)", dlink, ok)
	}
	for _, e := range m.Entries {
		if e.Path == "dlink/b.txt" || e.Path == "secret.txt" || e.SHA256 == digestOf("SECRET") {
			t.Fatalf("snapshot followed a symlink: %+v", e)
		}
	}
}

func TestSnapshotRecordsAFIFOAsOtherWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a.txt"), "alpha")
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan Manifest, 1)
	go func() { done <- Snapshot(root, "baseline") }()
	select {
	case m := <-done:
		wantComplete(t, m)
		pipe, ok := findEntry(m, "pipe")
		if !ok || pipe.Type != TypeOther {
			t.Fatalf("pipe entry = %+v (found=%v)", pipe, ok)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Snapshot blocked on a FIFO")
	}
}

func TestSnapshotDigestIsReproducibleForAnUnchangedTree(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a.txt"), "alpha")
	writeTree(t, filepath.Join(root, "sub", "b.txt"), "beta")
	first := Snapshot(root, "baseline")
	wantComplete(t, first)
	if len(first.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 (a.txt, sub, sub/b.txt)", len(first.Entries))
	}
	second := Snapshot(root, "baseline")
	if first.Digest() != second.Digest() {
		t.Fatal("two snapshots of an unchanged tree have different digests")
	}
}

func TestSnapshotContentChangeChangesTheDigest(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a.txt"), "alpha")
	before := Snapshot(root, "baseline")
	writeTree(t, filepath.Join(root, "a.txt"), "ALPHA")
	after := Snapshot(root, "baseline")
	if before.Digest() == after.Digest() {
		t.Fatal("content change did not change the manifest digest")
	}
}

func TestSnapshotModeChangeIsRecorded(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	writeTree(t, path, "alpha")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := findEntry(Snapshot(root, "baseline"), "a.txt")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	after, _ := findEntry(Snapshot(root, "post"), "a.txt")
	if fingerprint(&before) == fingerprint(&after) {
		t.Fatalf("mode change not visible in fingerprint: %+v vs %+v", before, after)
	}
}

func TestSnapshotUnreadableFileIsAGapAndSiblingsStayRecorded(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	locked := filepath.Join(root, "locked.txt")
	writeTree(t, locked, "secret")
	writeTree(t, filepath.Join(root, "ok.txt"), "ok")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	restoreModes(t, locked)
	m := Snapshot(root, "pre-publication")
	wantGapKind(t, m, GapUnreadable, "locked.txt")
	if e, ok := findEntry(m, "locked.txt"); !ok || e.Type != TypeUnreadable {
		t.Fatalf("locked.txt entry = %+v (found=%v), want unreadable", e, ok)
	}
	if e, ok := findEntry(m, "ok.txt"); !ok || e.SHA256 != digestOf("ok") {
		t.Fatalf("ok.txt entry = %+v (found=%v), want its digest", e, ok)
	}
}

func TestSnapshotUnreadableDirectoryIsAGapAndItsSiblingsStayRecorded(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	writeTree(t, filepath.Join(locked, "inner.txt"), "inner")
	writeTree(t, filepath.Join(root, "ok.txt"), "ok")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	restoreModes(t, locked)
	m := Snapshot(root, "pre-publication")
	wantGapKind(t, m, GapUnreadable, "locked")
	if _, ok := findEntry(m, "locked/inner.txt"); ok {
		t.Fatal("contents of an unreadable directory were recorded")
	}
	if e, ok := findEntry(m, "ok.txt"); !ok || e.SHA256 != digestOf("ok") {
		t.Fatalf("ok.txt entry = %+v (found=%v)", e, ok)
	}
}

func TestSnapshotVanishedRootIsEvidenceLost(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	writeTree(t, filepath.Join(root, "a.txt"), "alpha")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	m := Snapshot(root, "post-publication")
	wantGapKind(t, m, GapRootUnavailable, "")
	if len(m.Entries) != 0 {
		t.Fatalf("entries recorded for a vanished root: %+v", m.Entries)
	}
}

func TestSnapshotRootThatIsASymlinkIsRejected(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	writeTree(t, filepath.Join(realDir, "a.txt"), "alpha")
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	m := Snapshot(link, "baseline")
	wantGapKind(t, m, GapRootNotDirectory, "")
	if len(m.Entries) != 0 {
		t.Fatalf("a symlink root was followed: %+v", m.Entries)
	}
}

func TestSnapshotRootThatIsAFileIsRejected(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.txt")
	writeTree(t, file, "alpha")
	m := Snapshot(file, "baseline")
	wantGapKind(t, m, GapRootNotDirectory, "")
}

func TestSnapshotEmptyRootIsCompleteAndEmpty(t *testing.T) {
	m := Snapshot(t.TempDir(), "baseline")
	wantComplete(t, m)
	if len(m.Entries) != 0 {
		t.Fatalf("entries = %+v, want none", m.Entries)
	}
}

func TestSnapshotRootIdentityIsStableAcrossSnapshotsOfOneRoot(t *testing.T) {
	root := t.TempDir()
	a := Snapshot(root, "baseline")
	b := Snapshot(root, "pre-publication")
	if a.Dev != b.Dev || a.rootIno() != b.rootIno() || a.Dev == 0 {
		t.Fatalf("root identity = (%d,%d) and (%d,%d), want equal and non-zero", a.Dev, a.rootIno(), b.Dev, b.rootIno())
	}
}

// SnapshotDir is Snapshot of a held descriptor: the same manifest, no path needed, and a descriptor that is not a
// directory (or no descriptor) is a gap, never an empty complete tree.
func TestSnapshotDirIsSnapshotOfTheHeldDirectory(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a", "b.txt"), "b")
	writeTree(t, filepath.Join(root, "c.txt"), "c")
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	byPath, byFD := Snapshot(root, "x"), SnapshotDir(dir, "x")
	if !byFD.Complete || len(byFD.Entries) != 3 || byFD.Digest() != byPath.Digest() || byFD.IdentityDigest() != byPath.IdentityDigest() {
		t.Fatalf("by descriptor %+v, by path %+v", byFD, byPath)
	}
	if _, err := dir.Stat(); err != nil {
		t.Fatalf("SnapshotDir closed the descriptor: %v", err)
	}
	file, err := os.Open(filepath.Join(root, "c.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for name, m := range map[string]Manifest{"a file": SnapshotDir(file, "x"), "nil": SnapshotDir(nil, "x")} {
		if m.Complete || len(m.Gaps) == 0 || len(m.Entries) != 0 {
			t.Errorf("%s: %+v", name, m)
		}
	}
}

// A descriptor whose directory was already listed (read offset at the end) still snapshots the whole tree.
func TestSnapshotDirIgnoresTheReadOffsetOfTheCallersDescriptor(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a.txt"), "a")
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if _, err := dir.Readdirnames(-1); err != nil {
		t.Fatal(err)
	}
	if m := SnapshotDir(dir, "x"); !m.Complete || len(m.Entries) != 1 {
		t.Fatalf("%+v", m)
	}
}
