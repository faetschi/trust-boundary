//go:build linux

package trialfix

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"tbound/supervisor/internal/wsoracle"
)

// testRoot makes a mode-0700 root and returns it with the owner its files will have: the
// uid and gid of the root itself, which the test user may chown to.
func testRoot(t *testing.T) (string, Owner) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "live")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	return root, Owner{UID: st.Uid, GID: st.Gid}
}

func rootIdentity(t *testing.T, root string) syscall.Stat_t {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func treeDigestOf(t *testing.T, root string) string {
	t.Helper()
	got, err := TreeDigest(wsoracle.Snapshot(root, "reset"))
	if err != nil {
		t.Fatalf("TreeDigest: %v", err)
	}
	return got
}

func TestPopulateBuildsATreeThatDigestsEqualToItsSpec(t *testing.T) {
	root, owner := testRoot(t)
	if err := Populate(root, sampleSpec(), owner); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	want, err := DigestOwned(sampleSpec(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if got := treeDigestOf(t, root); got != want {
		t.Fatalf("tree digest %s, want the spec's %s", got, want)
	}
	// The modes are the spec's even under a restrictive umask.
	if info, err := os.Stat(filepath.Join(root, "run.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("run.sh: %v, %v", info, err)
	}
}

func TestPopulateRefusesAnExistingEntry(t *testing.T) {
	root, owner := testRoot(t)
	if err := os.WriteFile(filepath.Join(root, "empty"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Populate(root, sampleSpec(), owner); err == nil {
		t.Fatal("Populate overwrote an existing entry")
	}
}

func TestPopulateRefusesAnInvalidSpecBeforeTouchingTheRoot(t *testing.T) {
	root, owner := testRoot(t)
	if err := Populate(root, Spec{Schema: "x"}, owner); err == nil {
		t.Fatal("Populate accepted an invalid spec")
	}
	if names, _ := os.ReadDir(root); len(names) != 0 {
		t.Fatalf("root holds %d entries", len(names))
	}
}

func TestPopulateAndWipeRefuseASymlinkRoot(t *testing.T) {
	root, owner := testRoot(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := Populate(link, sampleSpec(), owner); err == nil {
		t.Fatal("Populate followed a symlink root")
	}
	if err := Wipe(link, owner); err == nil {
		t.Fatal("Wipe followed a symlink root")
	}
}

// dirty fills root with what a cell may leave behind: nested directories, a symlink out of
// the tree, a hard link, a fifo, a read-only file, and a lost+found of the filesystem.
func dirty(t *testing.T, root, outside string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "a", "b", "c"), 0o700))
	must(os.WriteFile(filepath.Join(root, "a", "b", "c", "f"), []byte("x"), 0o600))
	must(os.WriteFile(filepath.Join(root, "ro"), []byte("x"), 0o400))
	must(os.Link(filepath.Join(root, "ro"), filepath.Join(root, "a", "hard")))
	must(os.Symlink(outside, filepath.Join(root, "out")))
	must(os.Symlink(filepath.Dir(outside), filepath.Join(root, "a", "outdir")))
	must(syscall.Mkfifo(filepath.Join(root, "fifo"), 0o600))
	must(os.Mkdir(filepath.Join(root, "lost+found"), 0o700))
	must(os.WriteFile(filepath.Join(root, "lost+found", "keep"), []byte("keep"), 0o600))
}

// fenceState reports the type, mode, owner and names of the top-level fence directory.
func fenceState(t *testing.T, root string) (mode os.FileMode, uid, gid uint32, names []string) {
	t.Helper()
	info, err := os.Lstat(filepath.Join(root, LostFound))
	if err != nil {
		t.Fatalf("stat %s: %v", LostFound, err)
	}
	if !info.IsDir() {
		return info.Mode(), 0, 0, nil
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("the fence directory has no owner")
	}
	entries, err := os.ReadDir(filepath.Join(root, LostFound))
	if err != nil {
		t.Fatalf("read %s: %v", LostFound, err)
	}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return info.Mode(), st.Uid, st.Gid, names
}

// requireCanonicalFence requires the fence directory a reset leaves: a real directory, owned by
// the fixture owner, mode 0755, and empty. The effect oracle opens it and the cell may write it,
// so what a run leaves there is not carried into the next trial.
func requireCanonicalFence(t *testing.T, root string, owner Owner) {
	t.Helper()
	mode, uid, gid, names := fenceState(t, root)
	if !mode.IsDir() || mode&os.ModeSymlink != 0 || mode.Perm() != 0o755 || uid != owner.UID || gid != owner.GID || len(names) != 0 {
		t.Fatalf("%s is %v %d:%d with %v, want an empty directory 0755 %d:%d", LostFound, mode, uid, gid, names, owner.UID, owner.GID)
	}
}

func TestWipeRemovesEverythingInPlaceAndKeepsOnlyACanonicalFence(t *testing.T) {
	root, owner := testRoot(t)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "precious")
	if err := os.WriteFile(outside, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty(t, root, outside)
	before := rootIdentity(t, root)

	if err := Wipe(root, owner); err != nil {
		t.Fatalf("Wipe: %v", err)
	}
	after := rootIdentity(t, root)
	if after.Ino != before.Ino || after.Dev != before.Dev || after.Mode != before.Mode || after.Uid != before.Uid || after.Gid != before.Gid {
		t.Fatalf("root changed: %+v -> %+v", before, after)
	}
	names, err := os.ReadDir(root)
	if err != nil || len(names) != 1 || names[0].Name() != LostFound {
		t.Fatalf("root holds %v, %v; want only %s", names, err, LostFound)
	}
	requireCanonicalFence(t, root, owner)
	if got, err := os.ReadFile(outside); err != nil || string(got) != "precious" {
		t.Fatalf("a symlink target outside the root was touched: %q, %v", got, err)
	}
}

// TestResetRemovesAPayloadLeftInTheFence is the fence payload: the fence directory is inside
// the live root and owned by the cell, so a run can leave content in it.
func TestResetRemovesAPayloadLeftInTheFence(t *testing.T) {
	root, owner := testRoot(t)
	outside := filepath.Join(t.TempDir(), "precious")
	if err := os.WriteFile(outside, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty(t, root, outside)
	if err := Reset(root, sampleSpec(), owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	requireCanonicalFence(t, root, owner)
}

func TestResetRecreatesAMissingFence(t *testing.T) {
	root, owner := testRoot(t)
	if err := Reset(root, sampleSpec(), owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	requireCanonicalFence(t, root, owner)
}

func TestResetReplacesAFenceThatIsAFile(t *testing.T) {
	root, owner := testRoot(t)
	if err := os.WriteFile(filepath.Join(root, LostFound), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Reset(root, sampleSpec(), owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	requireCanonicalFence(t, root, owner)
}

func TestResetReplacesAFenceThatIsASymlink(t *testing.T) {
	root, owner := testRoot(t)
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, LostFound)); err != nil {
		t.Fatal(err)
	}
	if err := Reset(root, sampleSpec(), owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	requireCanonicalFence(t, root, owner)
	if got, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(got) != "outside" {
		t.Fatalf("the symlink target was touched: %q, %v", got, err)
	}
}

func TestResetReplacesADirtyTreeWithTheSpecAndIsRepeatable(t *testing.T) {
	root, owner := testRoot(t)
	outside := filepath.Join(t.TempDir(), "precious")
	if err := os.WriteFile(outside, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty(t, root, outside)
	want, _ := DigestOwned(sampleSpec(), owner)
	for i := 0; i < 2; i++ {
		if err := Reset(root, sampleSpec(), owner); err != nil {
			t.Fatalf("Reset %d: %v", i, err)
		}
		if got := treeDigestOf(t, root); got != want {
			t.Fatalf("Reset %d: tree digest %s, want %s", i, got, want)
		}
	}
	requireCanonicalFence(t, root, owner)
}

func TestResetPinnedRefusesARenamedRootAndADecoy(t *testing.T) {
	root, owner := testRoot(t)
	var st syscall.Stat_t
	if err := syscall.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	want := RootIdentity{Device: uint64(st.Dev), Inode: st.Ino}

	decoy := filepath.Join(filepath.Dir(root), "decoy")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "keep"), []byte("decoy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(decoy, root); err != nil {
		t.Fatal(err)
	}

	if err := ResetPinned(root, sampleSpec(), owner, want); err == nil {
		t.Fatal("ResetPinned wiped a decoy at the registered root's path")
	}
	if got, err := os.ReadFile(filepath.Join(root, "keep")); err != nil || string(got) != "decoy" {
		t.Fatalf("the decoy was touched: %q, %v", got, err)
	}
	// The tree that really is the registered inode is still resettable under its new name.
	if err := ResetPinned(root+"-old", sampleSpec(), owner, want); err != nil {
		t.Fatalf("ResetPinned of the real root under its new name: %v", err)
	}
}

// The next test needs root: it chowns to the canonical owner and wipes a directory the
// owner cannot enter.
func TestResetAsRootChownsToTheCanonicalOwnerAndWipesClosedDirectories(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown to 995:986 and to wipe a directory with mode 000")
	}
	root, _ := testRoot(t)
	if err := os.Chown(root, int(OwnerUID), int(OwnerGID)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "closed", "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "closed"), 0); err != nil {
		t.Fatal(err)
	}
	if err := Reset(root, sampleSpec(), Canonical); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	want, _ := Digest(sampleSpec())
	if got := treeDigestOf(t, root); got != want {
		t.Fatalf("tree digest %s, want %s", got, want)
	}
}
