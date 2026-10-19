//go:build linux

package sessionrepo

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOutsideWorkspaceReadRecordsNoSuccessfulReadDeclaredRead is the E06
// SuccessfulRead evidence for the declared-read route and the
// outside-workspace-path class. A real file outside the workspace holds a fresh
// canary; the read is attempted by absolute path, by ".." traversal, and through
// a symlink substituted into the sealed tree. Each attempt must return an error
// and no bytes, the canary must appear nowhere in the returned data, the error,
// the in-memory operation evidence, or the durable audit journal, and no
// successful read of the target may be recorded. A positive control reads an
// inside file through the same call and requires the same detectors to see a
// successful read and its content, so the assertions cannot pass vacuously.
func TestOutsideWorkspaceReadRecordsNoSuccessfulReadDeclaredRead(t *testing.T) {
	canary := e06DeclaredReadCanary(t)
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte(canary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The target is real and readable by the host, so a denial below is the
	// repository's, not a missing file.
	if host, err := os.ReadFile(outsideFile); err != nil || !strings.Contains(string(host), canary) {
		t.Fatalf("host control could not read the outside canary: %v", err)
	}

	t.Run("absolute path", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		data, err := grantedReadCall(t, g0, authority, "e06-sr-absolute", outsideFile, 1024)
		assertNoReleasedCanary(t, data, err, canary)
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("absolute outside read returned %v, want %v", err, ErrInvalidPath)
		}
		assertNoSuccessfulRead(t, store, outsideFile, canary)
		assertNoSuccessfulReadAfterReopen(t, store, outsideFile, canary)
	})

	t.Run("parent traversal to the real outside file", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		relative, err := filepath.Rel(g0.testPath(), outsideFile)
		if err != nil || !strings.HasPrefix(relative, "..") {
			t.Fatalf("relative path to the outside canary = %q, %v", relative, err)
		}
		data, err := grantedReadCall(t, g0, authority, "e06-sr-dotdot", relative, 1024)
		assertNoReleasedCanary(t, data, err, canary)
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("traversal outside read returned %v, want %v", err, ErrInvalidPath)
		}
		assertNoSuccessfulRead(t, store, relative, canary)
		assertNoSuccessfulReadAfterReopen(t, store, relative, canary)
	})

	t.Run("symlink escape to the real outside file", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		if err := os.Symlink(outsideDir, filepath.Join(g0.testPath(), "escape")); err != nil {
			t.Fatal(err)
		}
		const path = "escape/secret.txt"
		data, err := grantedReadCall(t, g0, authority, "e06-sr-symlink", path, 1024)
		assertNoReleasedCanary(t, data, err, canary)
		assertNoSuccessfulRead(t, store, path, canary)
		if _, err := store.Verify(); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("store after symlink escape = %v, want quarantine", err)
		}
		rootPath, options := store.rootPath, store.options
		if err := store.Close(); err != nil {
			t.Fatalf("close store before reopen: %v", err)
		}
		if _, err := Open(openFixtureRoot(t, rootPath), options); !errors.Is(err, ErrQuarantined) {
			t.Fatalf("reopen after symlink escape = %v, want quarantine", err)
		}
		// The durable journal, re-verified from disk after the store closed,
		// still holds no successful read and no canary bytes.
		assertJournalHasNoSuccessfulRead(t, options, path, canary)
	})

	t.Run("control: an inside read is recorded as successful", func(t *testing.T) {
		store, g0, authority := newE06ReadStore(t)
		const path, content = "task.txt", "protected content\n"
		data, err := grantedReadCall(t, g0, authority, "e06-sr-control", path, 1024)
		if err != nil || string(data) != content {
			t.Fatalf("inside control read = %q, %v", data, err)
		}
		if got := successfulReadOperations(t, store, path); got != 1 {
			t.Fatalf("detector saw %d successful operation records for the inside read, want 1", got)
		}
		effects, carried := journalReadEffects(t, store.options, path, content)
		if effects != 1 || !carried {
			t.Fatalf("detector saw %d successful journal effects (content carried=%t) for the inside read, want 1 with content", effects, carried)
		}
	})
}

func e06DeclaredReadCanary(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return "tbound-e06-declared-read-canary-" + hex.EncodeToString(raw[:])
}

func assertNoReleasedCanary(t *testing.T, data []byte, err error, canary string) {
	t.Helper()
	if err == nil {
		t.Fatalf("outside-workspace read succeeded and returned %d bytes", len(data))
	}
	if len(data) != 0 {
		t.Fatalf("outside-workspace read released %d bytes", len(data))
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("outside-workspace read error carries the canary: %v", err)
	}
}

// assertNoSuccessfulRead requires that neither the store's operation evidence
// nor its durable audit journal records a successful read of path, and that the
// canary appears in no retained record.
func assertNoSuccessfulRead(t *testing.T, store *Store, path, canary string) {
	t.Helper()
	if got := successfulReadOperations(t, store, path); got != 0 {
		t.Fatalf("store recorded %d successful read operations for %q", got, path)
	}
	store.mu.Lock()
	for _, operation := range store.operations {
		if encoded, err := json.Marshal(operation); err != nil || bytes.Contains(encoded, []byte(canary)) {
			store.mu.Unlock()
			t.Fatalf("operation evidence carries the canary or cannot be encoded: %v", err)
		}
	}
	store.mu.Unlock()
	assertJournalHasNoSuccessfulRead(t, store.options, path, canary)
}

func assertJournalHasNoSuccessfulRead(t *testing.T, options Options, path, canary string) {
	t.Helper()
	if effects, carried := journalReadEffects(t, options, path, canary); effects != 0 || carried {
		t.Fatalf("durable journal records %d successful reads of %q (canary carried=%t)", effects, path, carried)
	}
}

// assertNoSuccessfulReadAfterReopen reconstructs the store from disk and
// requires the reconstructed evidence to hold no successful read of path.
func assertNoSuccessfulReadAfterReopen(t *testing.T, store *Store, path, canary string) {
	t.Helper()
	rootPath, options := store.rootPath, store.options
	if err := store.Close(); err != nil {
		t.Fatalf("close store before reopen: %v", err)
	}
	recovered, err := Open(openFixtureRoot(t, rootPath), options)
	if err != nil {
		t.Fatalf("reopen after outside read: %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	artifact, err := recovered.Evidence()
	if err != nil {
		t.Fatalf("evidence after reopen: %v", err)
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(canary)) {
		t.Fatal("reconstructed evidence carries the canary")
	}
	if got := successfulReadOperations(t, recovered, path); got != 0 {
		t.Fatalf("reconstructed store holds %d successful reads of %q", got, path)
	}
	assertJournalHasNoSuccessfulRead(t, options, path, canary)
}

// successfulReadOperations counts operation evidence records of a successful
// read whose argument digest binds path.
func successfulReadOperations(t *testing.T, store *Store, path string) int {
	t.Helper()
	digest := readArgumentsDigest(path, 1024)
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for _, operation := range store.operations {
		if operation.Tool == "read" && operation.Outcome == "success" && operation.ArgumentDigest == digest {
			count++
		}
	}
	return count
}

// journalReadEffects verifies the durable audit journal from disk and returns
// the number of successful read effects for path, and whether any record's
// payload contains needle.
func journalReadEffects(t *testing.T, options Options, path, needle string) (int, bool) {
	t.Helper()
	trace, err := options.Journal.Trace()
	if err != nil {
		t.Fatalf("verify durable audit journal: %v", err)
	}
	carried := false
	for _, record := range trace.Records {
		if bytes.Contains(record.Event.Data, []byte(needle)) {
			carried = true
		}
	}
	successes := 0
	for _, effect := range trace.Effects {
		if bytes.Contains(effect.Result, []byte(needle)) || bytes.Contains(effect.Intent, []byte(needle)) {
			carried = true
		}
		var intent readIntent
		if err := json.Unmarshal(effect.Intent, &intent); err != nil || intent.Request.Tool != "read" || intent.Path != path {
			continue
		}
		t.Logf("journal effect for read of %q: outcome=%q unresolved=%t result_bytes=%d",
			path, effect.Outcome, effect.Unresolved, len(effect.Result))
		if effect.Outcome == "success" && !effect.Unresolved {
			successes++
		}
	}
	return successes, carried
}
