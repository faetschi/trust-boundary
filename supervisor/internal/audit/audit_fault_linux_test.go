//go:build linux

package audit

import (
	"bytes"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

// This file adds real kernel durability-fault coverage on top of the fabricated
// faultFile tests in audit_test.go. It drives the production RunEffect and
// appendLocked write+sync path against faults the Linux kernel actually returns:
//
//   - ENOSPC from writing to /dev/full (the kernel's always-full device);
//   - EFBIG from RLIMIT_FSIZE, capped between the durable intent and the
//     outcome so the outcome append cannot extend the real journal file; and
//   - EINVAL from File.Sync on a FIFO, which is a real failed fsync(2) syscall.
//
// Only a test-only seam is added: fileJournalForTest hands an already-open
// handle to the unexported Journal. Open's regular-file, permission, ancestry,
// and locking checks are untouched.

// fileJournalForTest builds a Journal around an already-open handle so the
// production RunEffect/appendLocked path can be exercised against real kernel
// failures without weakening Open's checks. It is test-only.
func fileJournalForTest(file syncFile) *Journal {
	return &Journal{file: file, effects: make(map[string]effectState)}
}

// ignoreSIGXFSZ installs SIG_IGN so that exceeding RLIMIT_FSIZE surfaces as an
// EFBIG return from write(2) instead of terminating the test binary with the
// default SIGXFSZ action.
func ignoreSIGXFSZ() {
	signal.Ignore(syscall.SIGXFSZ)
}

func TestRealENOSPCIntentWritePreventsEffect(t *testing.T) {
	const devFull = "/dev/full"
	if _, err := os.Stat(devFull); err != nil {
		t.Skipf("%s is unavailable: %v", devFull, err)
	}
	file, err := os.OpenFile(devFull, os.O_RDWR, 0)
	if err != nil {
		t.Skipf("open %s: %v", devFull, err)
	}
	journal := fileJournalForTest(file)
	defer journal.Close()

	called := false
	result, err := journal.RunEffect("effect-enospc", []byte("intent"), func() ([]byte, error) {
		called = true
		return []byte("claim-critical-result"), nil
	})
	if called {
		t.Fatal("effect callback ran although the real ENOSPC intent write failed")
	}
	if result != nil {
		t.Fatalf("result = %q, want nil", result)
	}
	if !errors.Is(err, ErrDurability) || !errors.Is(err, ErrPoisoned) || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("RunEffect error = %v, want ErrDurability, ErrPoisoned, and ENOSPC", err)
	}
	if _, err := journal.Append(Event{Kind: "decision", ID: "after-enospc"}); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("Append after ENOSPC = %v, want ErrPoisoned", err)
	}
}

func TestRealFailedFsyncIntentPreventsEffect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// Opening a FIFO read-write never blocks and gives a real fd whose write(2)
	// succeeds into the pipe buffer while fsync(2) returns EINVAL.
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open fifo: %v", err)
	}
	journal := fileJournalForTest(file)
	defer journal.Close()

	called := false
	result, err := journal.RunEffect("effect-fsync", []byte("intent"), func() ([]byte, error) {
		called = true
		return []byte("claim-critical-result"), nil
	})
	if called {
		t.Fatal("effect callback ran although the real fsync failure was not durable")
	}
	if result != nil {
		t.Fatalf("result = %q, want nil", result)
	}
	if !errors.Is(err, ErrDurability) || !errors.Is(err, ErrPoisoned) || !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("RunEffect error = %v, want ErrDurability, ErrPoisoned, and EINVAL", err)
	}
	if _, err := journal.Append(Event{Kind: "decision", ID: "after-fsync"}); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("Append after failed fsync = %v, want ErrPoisoned", err)
	}
}

func TestRealEFBIGIntentWritePreventsEffect(t *testing.T) {
	requireLinuxJournalOpen(t)
	ignoreSIGXFSZ()
	path := filepath.Join(privateJournalTestDir(t), "journal.jsonl")
	journal, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	original := lowerFileSizeLimit(t, 0)
	defer restoreFileSizeLimit(t, original)

	called := false
	result, err := journal.RunEffect("effect-efbig-intent", []byte("intent"), func() ([]byte, error) {
		called = true
		return []byte("claim-critical-result"), nil
	})
	_ = journal.Close()
	if called {
		t.Fatal("effect callback ran although the real EFBIG intent write failed")
	}
	if result != nil {
		t.Fatalf("result = %q, want nil", result)
	}
	if !errors.Is(err, ErrDurability) || !errors.Is(err, ErrPoisoned) || !errors.Is(err, syscall.EFBIG) {
		t.Fatalf("RunEffect error = %v, want ErrDurability, ErrPoisoned, and EFBIG", err)
	}
}

func TestRealEFBIGOutcomeWriteWithholdsResultAndQuarantines(t *testing.T) {
	requireLinuxJournalOpen(t)
	ignoreSIGXFSZ()
	path := filepath.Join(privateJournalTestDir(t), "journal.jsonl")
	journal, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		_ = journal.Close()
		t.Fatalf("Getrlimit: %v", err)
	}
	defer func() { _ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original) }()

	secret := []byte("claim-critical-result-must-not-escape")
	callbackRan := false
	result, err := journal.RunEffect("effect-efbig", []byte("intent"), func() ([]byte, error) {
		callbackRan = true
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, statErr
		}
		// Cap the process file-size limit at the intent's durable extent so the
		// outcome append is refused at its very first byte: a real kernel EFBIG
		// with no partial frame and no truncation of the durable intent.
		cap := syscall.Rlimit{Cur: uint64(info.Size()), Max: original.Max}
		if limitErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &cap); limitErr != nil {
			return nil, limitErr
		}
		return secret, nil
	})
	if !callbackRan {
		t.Fatal("effect callback did not run before the outcome append")
	}
	if result != nil {
		t.Fatalf("result = %q, want withheld nil", result)
	}
	if !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, ErrDurability) || !errors.Is(err, syscall.EFBIG) {
		t.Fatalf("RunEffect error = %v, want ErrOutcomeUnknown, ErrDurability, and EFBIG", err)
	}
	if _, err := journal.Append(Event{Kind: "decision", ID: "after-efbig"}); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("Append after EFBIG = %v, want ErrPoisoned", err)
	}
	restoreFileSizeLimit(t, original)
	if err := journal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(contents, secret) {
		t.Fatal("claim-critical result bytes were written to the journal despite the failed outcome append")
	}
	trace, err := Verify(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(trace.Effects) != 1 {
		t.Fatalf("effects = %d, want 1", len(trace.Effects))
	}
	effect := trace.Effects[0]
	if effect.ID != "effect-efbig" || !effect.Unresolved || effect.Outcome != "" || effect.OutcomeSequence != 0 || len(effect.Result) != 0 {
		t.Fatalf("reconstructed effect = %+v, want unresolved intent with no durable outcome", effect)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	ran := false
	if _, err := reopened.RunEffect("effect-efbig", []byte("intent"), func() ([]byte, error) {
		ran = true
		return nil, nil
	}); !errors.Is(err, ErrUnresolvedEffect) {
		t.Fatalf("replayed ID error = %v, want ErrUnresolvedEffect", err)
	}
	if _, err := reopened.RunEffect("effect-other", nil, func() ([]byte, error) {
		ran = true
		return nil, nil
	}); !errors.Is(err, ErrQuarantined) {
		t.Fatalf("other ID error = %v, want ErrQuarantined", err)
	}
	if ran {
		t.Fatal("an effect ran while the reopened journal was quarantined")
	}
}

// lowerFileSizeLimit sets RLIMIT_FSIZE's soft limit and returns the prior value.
func lowerFileSizeLimit(t *testing.T, soft uint64) syscall.Rlimit {
	t.Helper()
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatalf("Getrlimit: %v", err)
	}
	next := syscall.Rlimit{Cur: soft, Max: original.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &next); err != nil {
		t.Fatalf("Setrlimit(soft=%d): %v", soft, err)
	}
	return original
}

// restoreFileSizeLimit restores a value captured by lowerFileSizeLimit.
func restoreFileSizeLimit(t *testing.T, original syscall.Rlimit) {
	t.Helper()
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatalf("restore RLIMIT_FSIZE: %v", err)
	}
}
