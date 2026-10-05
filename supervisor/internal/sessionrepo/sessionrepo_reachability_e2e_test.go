//go:build linux

package sessionrepo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// TestSessionRepositorySealedGenerationReachabilityE2E is the thesis todo exit
// criterion #2 fixture ("Generation visibility"). After a baseline Seed (g0)
// plus exactly one approved mutation (g1) it proves:
//
//  1. Mutating the original live source tree after Seed does not change g0's
//     tree digest, manifest, Evidence(), or EvidenceBundle().
//  2. The private repository root and its generations contain no Git
//     reachability: no .git directory/file, no GIT_COMMON_DIR/GIT_DIR
//     reference, no object store or objects/info/alternates, and no
//     linked-worktree administration.
//  3. No /proc/self/mountinfo entry binds the live source into the repository
//     or exposes a sealed generation (or a path beneath it) as a mount point,
//     and the live source path is absent from mountinfo.
//  4. Generation.ReadOnlyMountSource() carries no write capability of its own,
//     and an out-of-band mutation of the sealed tree bytes or of the durable
//     manifest is detected by Verify/EvidenceBundle/Open and fails closed.
//
// SPEC DIVERGENCE (documented, not implemented here): 02-process-model.md §6 and
// decision D06 require the session repository to be "created from an explicit
// filtered file snapshot followed by initialization of a new private Git
// repository", and §6.1 repeats "a newly initialized private Git repository".
// The prototype implementation performs no Git initialization: Seed copies the
// filtered source with workspace.Import into a private 0700 byte tree. This test
// therefore proves the *negative* Git-reachability obligations the spec lists and
// does not (cannot) prove the presence of the spec-required private Git
// repository. Initializing one is an open human decision recorded in TASK_STATE.
//
// "Sealed" here means exactly: an independent private byte copy, owner-only 0700,
// digest-verified by rescan, with no live/Git reachability. It is NOT a claim of
// kernel immutability or filesystem write-protection: ReadOnlyMountSource returns
// an O_PATH descriptor (no write/read capability of its own), but a same-UID
// process can still openat a child writable through that dirfd, which is why the
// runtime must bind it read-only and why Verify/Evidence are the fail-closed
// backstop proven below.
func TestSessionRepositorySealedGenerationReachabilityE2E(t *testing.T) {
	t.Run("live source mutation does not alter sealed generations or evidence", func(t *testing.T) {
		fx := newReachabilityFixture(t)
		g0Digest, g0Manifest := fx.g0.TreeDigest(), fx.g0.Manifest()
		g1Digest, g1Manifest := fx.g1.TreeDigest(), fx.g1.Manifest()
		artifactBefore, err := fx.store.Evidence()
		if err != nil {
			t.Fatalf("evidence before live-source mutation: %v", err)
		}
		bundleBefore, err := fx.store.EvidenceBundle()
		if err != nil {
			t.Fatalf("evidence bundle before live-source mutation: %v", err)
		}

		// Mutate the original live source tree after Seed: overwrite two files,
		// add a new one, and delete a third. None of this may reach the sealed
		// generations, which are independent byte copies.
		writeFixtureFile(t, filepath.Join(fx.sourcePath, "README.md"), "LIVE-TAMPER readme\n", 0o644)
		writeFixtureFile(t, filepath.Join(fx.sourcePath, "plain.txt"), "LIVE-TAMPER plain\n", 0o644)
		writeFixtureFile(t, filepath.Join(fx.sourcePath, "live-added.txt"), "LIVE-TAMPER added\n", 0o644)
		if err := os.Remove(filepath.Join(fx.sourcePath, "build", "result.txt")); err != nil {
			t.Fatalf("remove live source file: %v", err)
		}

		if got := fx.g0.TreeDigest(); got != g0Digest {
			t.Fatalf("live source mutation changed g0 tree digest: got %s want %s", got, g0Digest)
		}
		if !reflect.DeepEqual(fx.g0.Manifest(), g0Manifest) {
			t.Fatalf("live source mutation changed g0 manifest")
		}
		if got := fx.g1.TreeDigest(); got != g1Digest {
			t.Fatalf("live source mutation changed g1 tree digest: got %s want %s", got, g1Digest)
		}
		if !reflect.DeepEqual(fx.g1.Manifest(), g1Manifest) {
			t.Fatalf("live source mutation changed g1 manifest")
		}
		sealedReadme, err := os.ReadFile(filepath.Join(fx.g0.testPath(), "README.md"))
		if err != nil || string(sealedReadme) != "baseline text\n" {
			t.Fatalf("sealed g0 README.md = %q err=%v, want the original baseline bytes", sealedReadme, err)
		}
		if _, err := os.Stat(filepath.Join(fx.g0.testPath(), "live-added.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("live source addition appeared in sealed g0: stat err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(fx.g0.testPath(), "build", "result.txt")); err != nil {
			t.Fatalf("live source deletion removed a sealed g0 object: %v", err)
		}

		artifactAfter, err := fx.store.Evidence()
		if err != nil {
			t.Fatalf("evidence after live-source mutation: %v", err)
		}
		if !reflect.DeepEqual(artifactBefore, artifactAfter) {
			t.Fatalf("live source mutation changed Evidence():\nbefore %+v\nafter  %+v", artifactBefore, artifactAfter)
		}
		bundleAfter, err := fx.store.EvidenceBundle()
		if err != nil {
			t.Fatalf("evidence bundle after live-source mutation: %v", err)
		}
		if !reflect.DeepEqual(bundleBefore, bundleAfter) {
			t.Fatalf("live source mutation changed EvidenceBundle()")
		}
		chain, err := fx.store.Verify()
		if err != nil {
			t.Fatalf("verify sealed chain after live-source mutation: %v", err)
		}
		if chain.BaselineGeneration != "g0" || chain.SealedGeneration != "g1" {
			t.Fatalf("unexpected chain after live-source mutation: %+v", chain)
		}

		// The mutation must have really happened, so the assertions above are
		// not vacuous.
		live, err := os.ReadFile(filepath.Join(fx.sourcePath, "README.md"))
		if err != nil || string(live) != "LIVE-TAMPER readme\n" {
			t.Fatalf("live source was not mutated: content=%q err=%v", live, err)
		}
	})

	t.Run("repository tree contains no Git reachability", func(t *testing.T) {
		fx := newReachabilityFixture(t)
		assertNoGitReachability(t, fx.store.rootPath)

		// Positive control: the scan must detect a planted reference, so it
		// cannot be passing merely because it inspects nothing.
		probe := t.TempDir()
		if err := os.MkdirAll(filepath.Join(probe, "generations", "g0"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(probe, "generations", "g0", "config"), []byte("GIT_DIR=/live/workspace/.git\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := gitReachabilityError(probe); err == nil {
			t.Fatal("Git-reachability scan missed a planted GIT_DIR reference")
		}
	})

	t.Run("no live-source or sealed-generation mount is exposed", func(t *testing.T) {
		fx := newReachabilityFixture(t)
		generations := []string{fx.g0.testPath(), fx.g1.testPath()}
		entries := readMountInfo(t)
		if err := mountReachabilityError(entries, fx.sourcePath, fx.store.rootPath, generations); err != nil {
			t.Fatalf("unexpected repository mount reachability: %v", err)
		}

		// Positive controls: the mount parser unescapes octal-escaped paths, and
		// the reachability check detects a live-source bind and a generation
		// mount point.
		parsed := parseMountInfo([]byte("36 35 98:0 /mnt/with\\040space /mnt/target\\040one rw,noatime master:1 - ext3 /dev/root rw\n"))
		if len(parsed) != 1 || parsed[0].mountPoint != "/mnt/target one" || parsed[0].root != "/mnt/with space" || parsed[0].source != "/dev/root" {
			t.Fatalf("mountinfo parser produced %+v", parsed)
		}
		sourceBind := mountEntry{mountPoint: filepath.Join(fx.store.rootPath, "generations", "g0"), root: fx.sourcePath, source: "/dev/root"}
		if err := mountReachabilityError([]mountEntry{sourceBind}, fx.sourcePath, fx.store.rootPath, generations); err == nil {
			t.Fatal("mount reachability check missed a live-source bind")
		}
	})

	t.Run("read-only mount source has no write capability and tamper fails closed", func(t *testing.T) {
		fx := newReachabilityFixture(t)
		mountSource, err := fx.g0.ReadOnlyMountSource()
		if err != nil {
			t.Fatalf("open sealed generation mount source: %v", err)
		}
		defer mountSource.Close()
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, mountSource.Fd(), syscall.F_GETFL, 0)
		if errno != 0 {
			t.Fatalf("fcntl(F_GETFL) on mount source: %v", errno)
		}
		if flags&linuxOPath == 0 {
			t.Fatalf("mount source flags %#x are not O_PATH (%#x)", flags, linuxOPath)
		}
		if _, err := mountSource.Write([]byte("x")); err == nil {
			t.Fatal("sealed-generation mount source accepted a direct write")
		}

		// Out-of-band mutation of the sealed tree bytes (a same-UID actor that
		// ignores the descriptor contract) must be detected, not silently
		// accepted. Verify and EvidenceBundle fail closed; Verify quarantines.
		writeFixtureFile(t, filepath.Join(fx.g0.testPath(), "README.md"), "out-of-band tamper\n", 0o644)
		if _, err := fx.store.Verify(); !errors.Is(err, ErrGenerationModified) {
			t.Fatalf("Verify after sealed-tree tamper = %v, want %v", err, ErrGenerationModified)
		}
		if _, err := fx.store.EvidenceBundle(); !errors.Is(err, ErrGenerationModified) {
			t.Fatalf("EvidenceBundle after sealed-tree tamper = %v, want %v", err, ErrGenerationModified)
		}
	})

	t.Run("durable manifest tamper fails closed on reopen", func(t *testing.T) {
		fx := newReachabilityFixture(t)
		rootPath, options := fx.store.rootPath, fx.store.options
		if err := fx.store.Close(); err != nil {
			t.Fatalf("close store before manifest tamper: %v", err)
		}
		statePath := filepath.Join(rootPath, repositoryStateName)
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read durable repository state: %v", err)
		}
		var state durableRepositoryState
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatalf("decode durable repository state: %v", err)
		}
		if len(state.Generations) != 2 || len(state.Generations[0].Manifest.Objects) == 0 {
			t.Fatalf("durable state does not retain g0/g1: %+v", state)
		}
		// Flip one recorded content digest while leaving the journal and the
		// on-disk tree untouched: retained state must not be trusted over
		// verified audit history.
		tamperedPath := ""
		for index := range state.Generations[0].Manifest.Objects {
			object := &state.Generations[0].Manifest.Objects[index]
			if object.State.ContentDigest == "" {
				continue
			}
			object.State.ContentDigest = flipEvidenceDigest(object.State.ContentDigest)
			tamperedPath = object.Path
			break
		}
		if tamperedPath == "" {
			t.Fatal("durable g0 manifest has no content digest to tamper")
		}
		tampered, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("re-encode tampered state: %v", err)
		}
		if err := os.WriteFile(statePath, tampered, 0o600); err != nil {
			t.Fatalf("write tampered state: %v", err)
		}
		if _, err := Open(openFixtureRoot(t, rootPath), options); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("reopen with a tampered durable manifest = %v, want %v", err, ErrQuarantined)
		}
	})
}

// reachabilityFixture is one seeded repository (g0) with exactly one approved
// mutation (g1), plus its live source path.
type reachabilityFixture struct {
	store      *Store
	g0, g1     *Generation
	sourcePath string
}

func newReachabilityFixture(t *testing.T) reachabilityFixture {
	t.Helper()
	store, _, _, authority := newE2EStore(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	buildWriteFixtureTree(t, sourcePath, writeFixtureFiles())
	g0, err := store.Seed(openFixtureRoot(t, sourcePath), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed g0: %v", err)
	}
	created := append(cloneWriteFixtureFiles(writeFixtureFiles()), writeFixtureFileSpec{path: "created.txt", content: "created\n", mode: 0o644})
	g1 := writeAndAssertMode(t, store, g0, authority, "reachability-g1", "created.txt", []byte("created\n"), expectedWriteSnapshot(t, "g1", created), 1, 0o644)
	return reachabilityFixture{store: store, g0: g0, g1: g1, sourcePath: sourcePath}
}

type mountEntry struct {
	mountPoint string
	root       string
	source     string
}

// readMountInfo returns the decoded /proc/self/mountinfo entries for this
// process. A missing or unreadable file is a hard failure: this test is the
// evidence for the mount-reachability obligation.
func readMountInfo(t *testing.T) []mountEntry {
	t.Helper()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatalf("read /proc/self/mountinfo: %v", err)
	}
	return parseMountInfo(data)
}

// parseMountInfo parses the proc_pid_mountinfo(5) line format into mountpoint,
// root, and source paths, decoding the kernel's octal escapes.
func parseMountInfo(data []byte) []mountEntry {
	var entries []mountEntry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		halves := strings.SplitN(line, " - ", 2)
		fields := strings.Fields(halves[0])
		if len(fields) < 5 {
			continue
		}
		entry := mountEntry{root: unescapeMountPath(fields[3]), mountPoint: unescapeMountPath(fields[4])}
		if len(halves) == 2 {
			rest := strings.Fields(halves[1])
			if len(rest) >= 2 {
				entry.source = unescapeMountPath(rest[1])
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func unescapeMountPath(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}

// mountReachabilityError reports the first mountinfo entry that exposes the
// live source path, makes the repository (or a path beneath it) a mount point,
// or makes a sealed generation (or a path beneath it) a mount point.
func mountReachabilityError(entries []mountEntry, liveSource, repositoryRoot string, generations []string) error {
	liveSource = filepath.Clean(liveSource)
	repositoryRoot = filepath.Clean(repositoryRoot)
	under := func(path, root string) bool {
		return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
	}
	for _, entry := range entries {
		for _, exposed := range []string{entry.mountPoint, entry.root, entry.source} {
			if exposed == liveSource || strings.HasPrefix(exposed, liveSource+string(filepath.Separator)) {
				return fmt.Errorf("live source %q is exposed by mount point %q", liveSource, entry.mountPoint)
			}
		}
		if under(entry.mountPoint, repositoryRoot) {
			return fmt.Errorf("repository is or contains mount point %q", entry.mountPoint)
		}
		for _, generation := range generations {
			if under(entry.mountPoint, filepath.Clean(generation)) {
				return fmt.Errorf("sealed generation %q is or contains mount point %q", generation, entry.mountPoint)
			}
		}
	}
	return nil
}

// assertNoGitReachability fails the test if any Git administration, object
// store, alternates, worktree administration, or GIT_DIR/GIT_COMMON_DIR
// reference is present in the repository tree.
func assertNoGitReachability(t *testing.T, root string) {
	t.Helper()
	if err := gitReachabilityError(root); err != nil {
		t.Fatalf("repository tree has Git reachability: %v", err)
	}
}

func gitReachabilityError(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch entry.Name() {
		case ".git":
			return fmt.Errorf("Git administration entry %q", relative)
		case "commondir", "worktrees":
			return fmt.Errorf("linked-worktree administration %q", relative)
		case "objects":
			return fmt.Errorf("Git object store %q", relative)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, token := range []string{"GIT_COMMON_DIR", "GIT_DIR", "gitdir:", "objects/info/alternates"} {
			if bytes.Contains(data, []byte(token)) {
				return fmt.Errorf("repository file %q references %s", relative, token)
			}
		}
		return nil
	})
}
