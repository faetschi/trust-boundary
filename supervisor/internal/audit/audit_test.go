package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var errInjectedDiskFull = errors.New("ENOSPC: injected disk full")
var errInjectedSync = errors.New("injected fsync failure")

type faultFile struct {
	data            []byte
	readOffset      int64
	maxWrite        int
	writeCalls      int
	failWriteCall   int
	failWrite       error
	syncCalls       int
	failSyncCall    int
	failSync        error
	closed          bool
}

func (f *faultFile) Read(p []byte) (int, error) {
	if f.readOffset >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.readOffset:])
	f.readOffset += int64(n)
	return n, nil
}

func (f *faultFile) Write(p []byte) (int, error) {
	f.writeCalls++
	if f.failWriteCall == f.writeCalls {
		return 0, f.failWrite
	}
	n := len(p)
	if f.maxWrite > 0 && n > f.maxWrite {
		n = f.maxWrite
	}
	f.data = append(f.data, p[:n]...)
	return n, nil
}

func (f *faultFile) Sync() error {
	f.syncCalls++
	if f.failSyncCall == f.syncCalls {
		return f.failSync
	}
	return nil
}

func (f *faultFile) Close() error {
	f.closed = true
	return nil
}

func (f *faultFile) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.readOffset + offset
	case io.SeekEnd:
		next = int64(len(f.data)) + offset
	default:
		return 0, errors.New("invalid seek whence")
	}
	if next < 0 {
		return 0, errors.New("negative seek")
	}
	f.readOffset = next
	return next, nil
}

func testJournal(file *faultFile) *Journal {
	return &Journal{file: file, effects: make(map[string]effectState)}
}

func TestRunEffectCompletesShortWritesAndReconstructsTrace(t *testing.T) {
	file := &faultFile{maxWrite: 5}
	journal := testJournal(file)
	result, err := journal.RunEffect("effect-1", []byte("intent"), func() ([]byte, error) {
		return []byte("result"), nil
	})
	if err != nil {
		t.Fatalf("RunEffect: %v", err)
	}
	if !bytes.Equal(result, []byte("result")) {
		t.Fatalf("result = %q, want result", result)
	}
	trace, err := Verify(bytes.NewReader(file.data))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(trace.Records) != 2 || len(trace.Effects) != 1 {
		t.Fatalf("unexpected trace sizes: records=%d effects=%d", len(trace.Records), len(trace.Effects))
	}
	effect := trace.Effects[0]
	if effect.ID != "effect-1" || effect.Unresolved || effect.Outcome != "success" ||
		!bytes.Equal(effect.Intent, []byte("intent")) || !bytes.Equal(effect.Result, []byte("result")) {
		t.Fatalf("unexpected reconstructed effect: %+v", effect)
	}
}

func TestWriteFailureBeforeEffectPoisonsJournal(t *testing.T) {
	file := &faultFile{maxWrite: 7, failWriteCall: 2, failWrite: errInjectedDiskFull}
	journal := testJournal(file)
	called := false
	_, err := journal.RunEffect("effect-1", []byte("intent"), func() ([]byte, error) {
		called = true
		return []byte("should not run"), nil
	})
	if err == nil || called {
		t.Fatalf("RunEffect error=%v callbackCalled=%t; want write failure before callback", err, called)
	}
	if !errors.Is(err, ErrDurability) || !errors.Is(err, ErrPoisoned) ||
		!errors.Is(err, errInjectedDiskFull) || !strings.Contains(err.Error(), "ENOSPC") {
		t.Fatalf("error does not retain durability/fail-closed context: %v", err)
	}
	_, err = journal.Append(Event{Kind: "decision", ID: "later"})
	if !errors.Is(err, ErrPoisoned) {
		t.Fatalf("later append error = %v, want ErrPoisoned", err)
	}
}

func TestIntentSyncFailurePreventsEffect(t *testing.T) {
	file := &faultFile{failSyncCall: 1, failSync: errInjectedSync}
	journal := testJournal(file)
	called := false
	_, err := journal.RunEffect("effect-1", []byte("intent"), func() ([]byte, error) {
		called = true
		return []byte("should not run"), nil
	})
	if err == nil || called || !errors.Is(err, ErrDurability) || !errors.Is(err, ErrPoisoned) || !errors.Is(err, errInjectedSync) {
		t.Fatalf("RunEffect error=%v callbackCalled=%t; want fail-closed sync error before effect", err, called)
	}
}

func TestOutcomeSyncFailureWithholdsResultAfterEffect(t *testing.T) {
	file := &faultFile{failSyncCall: 2, failSync: errInjectedSync}
	journal := testJournal(file)
	called := 0
	result, err := journal.RunEffect("effect-1", []byte("intent"), func() ([]byte, error) {
		called++
		return []byte("private result"), nil
	})
	if called != 1 || result != nil || !errors.Is(err, ErrOutcomeUnknown) ||
		!errors.Is(err, ErrDurability) || !errors.Is(err, errInjectedSync) {
		t.Fatalf("got callback calls=%d result=%q error=%v; want one effect, withheld result, unknown outcome", called, result, err)
	}
	if _, err := journal.RunEffect("effect-2", nil, func() ([]byte, error) {
		t.Fatal("poisoned journal invoked a later effect")
		return nil, nil
	}); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("later effect error = %v, want ErrPoisoned", err)
	}
}

func TestPanicQuarantinesAllLaterEffects(t *testing.T) {
	journal := testJournal(&faultFile{})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("effect callback panic was not propagated")
			}
		}()
		_, _ = journal.RunEffect("effect-1", []byte("intent"), func() ([]byte, error) {
			panic("simulated process-local callback failure")
		})
	}()

	called := false
	_, err := journal.RunEffect("effect-2", nil, func() ([]byte, error) {
		called = true
		return nil, nil
	})
	if !errors.Is(err, ErrQuarantined) || called {
		t.Fatalf("later effect error=%v callbackCalled=%t; want global quarantine", err, called)
	}
}

func TestOversizedOutcomeQuarantinesAllLaterEffects(t *testing.T) {
	journal := testJournal(&faultFile{})
	calls := 0
	_, err := journal.RunEffect("effect-1", []byte("intent"), func() ([]byte, error) {
		calls++
		return make([]byte, MaxRecordBytes), nil
	})
	if calls != 1 || !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("calls=%d error=%v; want effect run once and oversized outcome marked unknown", calls, err)
	}
	_, err = journal.RunEffect("effect-2", nil, func() ([]byte, error) {
		calls++
		return nil, nil
	})
	if !errors.Is(err, ErrQuarantined) || calls != 1 {
		t.Fatalf("later effect error=%v calls=%d; want global quarantine", err, calls)
	}
}

func TestCallbackErrorRecordsUnknownAndQuarantinesAfterReopen(t *testing.T) {
	requireLinuxJournalOpen(t)
	path := filepath.Join(privateJournalTestDir(t), "journal.jsonl")
	journal, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	callbackErr := errors.New("effect may have partially completed")
	result, err := journal.RunEffect("effect-1", []byte("intent"), func() ([]byte, error) {
		return []byte("must not be released"), callbackErr
	})
	if result != nil || !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, callbackErr) {
		t.Fatalf("result=%q error=%v; want withheld result and ambiguous callback error", result, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	journal, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer journal.Close()
	called := false
	if _, err := journal.RunEffect("effect-2", nil, func() ([]byte, error) {
		called = true
		return nil, nil
	}); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("later effect error = %v, want ErrQuarantined", err)
	}
	if called {
		t.Fatal("later effect ran after callback error")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := Verify(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(trace.Effects) != 1 || trace.Effects[0].Outcome != outcomeUnknown || !trace.Effects[0].Unresolved {
		t.Fatalf("callback error was not reconstructed as unresolved unknown: %+v", trace.Effects)
	}
}

func TestDuplicateEffectIDDoesNotRunAgain(t *testing.T) {
	journal := testJournal(&faultFile{})
	calls := 0
	perform := func() ([]byte, error) {
		calls++
		return []byte("ok"), nil
	}
	if _, err := journal.RunEffect("effect-1", nil, perform); err != nil {
		t.Fatalf("first RunEffect: %v", err)
	}
	if _, err := journal.RunEffect("effect-1", nil, perform); !errors.Is(err, ErrDuplicateEffect) {
		t.Fatalf("second RunEffect error = %v, want ErrDuplicateEffect", err)
	}
	if calls != 1 {
		t.Fatalf("effect callback ran %d times, want once", calls)
	}
}

func TestOpenRejectsCorruptAndTruncatedJournal(t *testing.T) {
	requireLinuxJournalOpen(t)
	base := filepath.Join(privateJournalTestDir(t), "base.jsonl")
	journal, err := Open(base)
	if err != nil {
		t.Fatalf("Open new journal: %v", err)
	}
	if _, err := journal.Append(Event{Kind: "decision", ID: "decision-1", Data: []byte("allow")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	valid, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), valid...)
	corrupt[len(corrupt)-3] ^= 1
	truncated := append([]byte(nil), valid[:len(valid)-1]...)

	for name, contents := range map[string][]byte{"corrupt": corrupt, "truncated": truncated} {
		name, contents := name, contents
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(privateJournalTestDir(t), "journal.jsonl")
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(path)
			if err == nil {
				_ = opened.Close()
				t.Fatal("Open accepted corrupt or truncated journal")
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Open error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestOpenReconstructsUnresolvedAttemptAndBlocksReplay(t *testing.T) {
	requireLinuxJournalOpen(t)
	path := filepath.Join(privateJournalTestDir(t), "journal.jsonl")
	frame := testFrame(t, 1, "", Event{Kind: intentKind, ID: "ambiguous", Data: []byte("intent")})
	if err := os.WriteFile(path, frame, 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(path)
	if err != nil {
		t.Fatalf("Open unresolved journal: %v", err)
	}
	defer journal.Close()
	called := false
	if _, err := journal.RunEffect("ambiguous", []byte("intent"), func() ([]byte, error) {
		called = true
		return nil, nil
	}); !errors.Is(err, ErrUnresolvedEffect) {
		t.Fatalf("RunEffect error = %v, want ErrUnresolvedEffect", err)
	}
	if called {
		t.Fatal("unresolved effect was replayed")
	}
	called = false
	if _, err := journal.RunEffect("different-id", nil, func() ([]byte, error) {
		called = true
		return nil, nil
	}); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("different effect ID error = %v, want ErrQuarantined", err)
	}
	if called {
		t.Fatal("different effect ran while journal was quarantined")
	}
}

func TestOpenHoldsExclusiveLockUntilClose(t *testing.T) {
	requireLinuxJournalOpen(t)
	path := filepath.Join(privateJournalTestDir(t), "journal.jsonl")
	alias := path + ".hardlink"
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := os.Link(path, alias); err != nil {
		_ = first.Close()
		t.Fatalf("create hard link: %v", err)
	}
	if _, err := Open(path); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("concurrent Open error = %v, want ErrAlreadyOpen", err)
	}
	if _, err := Open(alias); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("hard-link Open error = %v, want ErrAlreadyOpen", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first handle: %v", err)
	}
	second, err := Open(alias)
	if err != nil {
		t.Fatalf("Open hard link after Close: %v", err)
	}
	if _, err := Open(path); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("original path Open error = %v, want ErrAlreadyOpen while alias is held", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close second handle: %v", err)
	}
}

func TestOpenRejectsBroadPermissionsAndLeafSymlink(t *testing.T) {
	requireLinuxJournalOpen(t)
	root := privateJournalTestDir(t)
	parent := filepath.Join(root, "broad")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(parent, "journal.jsonl")); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("Open broad parent error = %v, want ErrInsecurePermissions", err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "journal.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created journal in broad directory: stat error=%v", err)
	}

	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(private, "broad.jsonl")
	if err := os.WriteFile(filePath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filePath); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("Open broad file error = %v, want ErrInsecurePermissions", err)
	}

	target := filepath.Join(private, "target.jsonl")
	if err := os.WriteFile(target, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(private, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); !errors.Is(err, ErrJournalSymlink) {
		t.Fatalf("Open leaf symlink error = %v, want ErrJournalSymlink", err)
	}
	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(private, parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(parentLink, "journal.jsonl")); !errors.Is(err, ErrJournalSymlink) {
		t.Fatalf("Open parent symlink error = %v, want ErrJournalSymlink", err)
	}
	regularComponent := filepath.Join(root, "regular-component")
	if err := os.WriteFile(regularComponent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(regularComponent, "journal.jsonl")); err == nil || errors.Is(err, ErrJournalSymlink) || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Open non-directory component error = %v, want a non-symlink not-directory error", err)
	}
}

func TestProcessExitReleasesLockButIntentStillQuarantines(t *testing.T) {
	requireLinuxJournalOpen(t)
	path := filepath.Join(privateJournalTestDir(t), "journal.jsonl")
	command := exec.Command(os.Args[0], "-test.run=^TestJournalCrashHelper$")
	command.Env = append(os.Environ(), "TB_AUDIT_CRASH_HELPER_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v\n%s", err, output)
	}

	journal, err := Open(path)
	if err != nil {
		t.Fatalf("Open after child exit (flock should be released): %v", err)
	}
	defer journal.Close()
	called := false
	if _, err := journal.RunEffect("later-effect", nil, func() ([]byte, error) {
		called = true
		return nil, nil
	}); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("later effect error = %v, want ErrQuarantined", err)
	}
	if called {
		t.Fatal("later effect ran after crash left an unresolved intent")
	}
}

func TestJournalCrashHelper(t *testing.T) {
	path := os.Getenv("TB_AUDIT_CRASH_HELPER_PATH")
	if path == "" {
		return
	}
	journal, err := Open(path)
	if err != nil {
		t.Fatalf("Open in crash helper: %v", err)
	}
	_, err = journal.RunEffect("crashed-effect", []byte("durable intent"), func() ([]byte, error) {
		os.Exit(0) // Simulate process death after intent Sync and before outcome.
		return nil, nil
	})
	t.Fatalf("RunEffect returned after os.Exit callback: %v", err)
}

func requireLinuxJournalOpen(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Open requires the Linux flock implementation")
	}
}

func privateJournalTestDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Open requires the Linux flock implementation")
	}

	home, homeErr := os.UserHomeDir()
	for _, base := range []string{home, os.TempDir()} {
		if base == "" {
			continue
		}
		anchor, err := openJournalDirectory(base)
		if err != nil {
			continue
		}
		_ = anchor.Close()
		directory, err := os.MkdirTemp(base, ".audit-test-")
		if err != nil {
			continue
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			_ = os.RemoveAll(directory)
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(directory) })
		return directory
	}
	if homeErr != nil {
		t.Skipf("no safe private test directory (home lookup failed: %v)", homeErr)
	}
	t.Skip("no writable test location has root or supervisor-owned, non-writable ancestry")
	return ""
}

func TestOpenRejectsWritableAncestorsAndTmpStyleAncestry(t *testing.T) {
	requireLinuxJournalOpen(t)
	root := privateJournalTestDir(t)
	for name, mode := range map[string]os.FileMode{"group-writable": 0o770, "other-writable": 0o707} {
		name, mode := name, mode
		t.Run(name, func(t *testing.T) {
			ancestor := filepath.Join(root, name)
			if err := os.Mkdir(ancestor, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(ancestor, mode); err != nil {
				t.Fatal(err)
			}
			privateChild := filepath.Join(ancestor, "private")
			if err := os.Mkdir(privateChild, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(filepath.Join(privateChild, "journal.jsonl")); !errors.Is(err, ErrInsecurePermissions) {
				t.Fatalf("Open through %s ancestor error = %v, want ErrInsecurePermissions", name, err)
			}
		})
	}

	if _, err := Open(filepath.Join(string(os.PathSeparator), "tmp", "audit-no-create", "journal.jsonl")); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("Open through /tmp error = %v, want ErrInsecurePermissions", err)
	}
}

func TestOpenRejectsUntrustedOwnership(t *testing.T) {
	requireLinuxJournalOpen(t)
	if currentEffectiveUID() != 0 {
		t.Skip("changing file ownership requires root")
	}
	root := privateJournalTestDir(t)
	foreignDirectory := filepath.Join(root, "foreign-directory")
	if err := os.Mkdir(foreignDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(foreignDirectory, 65534, 65534); err != nil {
		t.Skipf("cannot assign test directory to an untrusted owner: %v", err)
	}
	if _, err := Open(filepath.Join(foreignDirectory, "journal.jsonl")); !errors.Is(err, ErrInsecureOwnership) {
		t.Fatalf("Open through foreign-owned ancestor error = %v, want ErrInsecureOwnership", err)
	}

	path := filepath.Join(root, "foreign-owner.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Skipf("cannot assign test file to an untrusted owner: %v", err)
	}
	if _, err := Open(path); !errors.Is(err, ErrInsecureOwnership) {
		t.Fatalf("Open foreign-owned file error = %v, want ErrInsecureOwnership", err)
	}
}

func TestOpenKeepsDirectoryIdentityAfterPathReplacement(t *testing.T) {
	requireLinuxJournalOpen(t)
	root := privateJournalTestDir(t)
	active := filepath.Join(root, "active")
	if err := os.Mkdir(active, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(active, "journal.jsonl")
	journal, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	archived := filepath.Join(root, "archived")
	if err := os.Rename(active, archived); err != nil {
		_ = journal.Close()
		t.Fatalf("rename opened parent: %v", err)
	}
	if err := os.Mkdir(active, 0o700); err != nil {
		_ = journal.Close()
		t.Fatalf("replace parent path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(active, "journal.jsonl"), nil, 0o600); err != nil {
		_ = journal.Close()
		t.Fatalf("create replacement journal: %v", err)
	}
	if _, err := journal.Append(Event{Kind: "decision", ID: "anchored"}); err != nil {
		_ = journal.Close()
		t.Fatalf("Append through anchored handle: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	originalContents, err := os.ReadFile(filepath.Join(archived, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	replacementContents, err := os.ReadFile(filepath.Join(active, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(originalContents) == 0 || len(replacementContents) != 0 {
		t.Fatalf("append followed replacement path: original bytes=%d replacement bytes=%d", len(originalContents), len(replacementContents))
	}
}

func TestVerifyRejectsSequenceAndPredecessorMismatch(t *testing.T) {
	for name, frame := range map[string][]byte{
		"sequence":    testFrame(t, 2, "", Event{Kind: "decision", ID: "bad-sequence"}),
		"predecessor": testFrame(t, 1, "orphan-hash", Event{Kind: "decision", ID: "bad-predecessor"}),
	} {
		name, frame := name, frame
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(bytes.NewReader(frame)); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Verify error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func testFrame(t *testing.T, sequence uint64, previousHash string, event Event) []byte {
	t.Helper()
	unsigned := unsignedRecord{Version: recordVersion, Sequence: sequence, PreviousHash: previousHash, Event: event}
	hash, err := hashRecord(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Version: unsigned.Version, Sequence: unsigned.Sequence, PreviousHash: unsigned.PreviousHash, Event: event, Hash: hash}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}
