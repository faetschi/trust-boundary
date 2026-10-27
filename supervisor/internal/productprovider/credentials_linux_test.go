//go:build linux

package productprovider

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/providerbridge"
)

const sentinelKey = "P7-SENTINEL-credential-never-disclose-8fd129"

func testStore(t *testing.T) *Store {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "credentials")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// WSL is a normal user: exercise actual chown to our current UID/GID.
	// Only ancestors outside this fixture are exempt; final directory is not.
	return &Store{directory: dir, exemptThrough: base}
}

func installKey(t *testing.T, s *Store, key string, replace, rotate bool) (Rotation, error) {
	t.Helper()
	return s.Install(OpenCodeGo, os.Geteuid(), os.Getegid(), []byte(key), replace, rotate)
}

func assertRedacted(t *testing.T, values ...any) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(fmt.Sprintf("%+v %#v", value, value), sentinelKey) {
			t.Fatal("credential disclosed")
		}
	}
}

func TestProviderCredentialLifecycle(t *testing.T) {
	s := testStore(t)
	status, err := s.Status(OpenCodeGo)
	if err != nil || status.Present {
		t.Fatalf("absent status: %+v %v", status, err)
	}
	source := filepath.Join(t.TempDir(), "source.key")
	if err := os.WriteFile(source, []byte(sentinelKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := ReadSource(source)
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := s.Install(OpenCodeGo, os.Geteuid(), os.Getegid(), key, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if rotation.NewFingerprint != Fingerprint([]byte(sentinelKey)) || len(rotation.NewFingerprint) != 8 || rotation.OldFingerprint != "" {
		t.Fatal(rotation)
	}
	status, err = s.Status(OpenCodeGo)
	if err != nil || !status.Present || status.UID != uint32(os.Geteuid()) || status.GID != uint32(os.Getegid()) || status.Mode != "0600" || status.Fingerprint != rotation.NewFingerprint {
		t.Fatalf("status %+v %v", status, err)
	}
	path := filepath.Join(s.directory, OpenCodeGo+".key")
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != sentinelKey {
		t.Fatal("stored content must be key only")
	}
	_, err = installKey(t, s, "replacement", false, false)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite: %v", err)
	}
	old := status.Fingerprint
	rotation, err = installKey(t, s, "replacement", true, true)
	if err != nil || rotation.OldFingerprint != old || rotation.NewFingerprint != Fingerprint([]byte("replacement")) {
		t.Fatalf("rotate: %+v %v", rotation, err)
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary file left behind")
	}
	if err := os.WriteFile(filepath.Join(s.directory, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(OpenCodeGo); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status(OpenCodeGo)
	if err != nil || status.Present {
		t.Fatal("remove did not remove key")
	}
	if _, err := os.Stat(filepath.Join(s.directory, "keep")); err != nil {
		t.Fatal("remove touched unrelated file")
	}
	if err := s.Remove(OpenCodeGo); err != nil {
		t.Fatal("remove must be idempotent")
	}
	_, err = installKey(t, s, sentinelKey, true, true)
	if !errors.Is(err, ErrAbsent) {
		t.Fatal("rotate absent accepted")
	}
	assertRedacted(t, rotation, status, err)
}

func TestProviderSourceProtection(t *testing.T) {
	for _, scenario := range []string{"world-readable", "symlink", "directory", "oversize", "invalid-key", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			base := t.TempDir()
			source := filepath.Join(base, "source")
			if err := os.WriteFile(source, []byte(sentinelKey), 0600); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "world-readable":
				if err := os.Chmod(source, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := filepath.Join(base, "link")
				if err := os.Symlink(source, link); err != nil {
					t.Fatal(err)
				}
				source = link
			case "directory":
				source = base
			case "oversize":
				if err := os.WriteFile(source, []byte(strings.Repeat("x", 4097)), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-key":
				if err := os.WriteFile(source, []byte(sentinelKey+"\x00"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				source += "missing"
			}
			_, err := ReadSource(source)
			if !errors.Is(err, ErrCredential) {
				t.Fatalf("source accepted: %v", err)
			}
			assertRedacted(t, err)
		})
	}
}

func TestProviderStatusRejectsInsecureStorage(t *testing.T) {
	for _, scenario := range []string{"directory-mode", "symlink-directory", "symlink-ancestor", "writable-ancestor", "symlink-file", "file-mode", "fifo", "invalid-key", "wrong-service-owner"} {
		t.Run(scenario, func(t *testing.T) {
			s := testStore(t)
			if _, err := installKey(t, s, sentinelKey, false, false); err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(s.directory, OpenCodeGo+".key")
			switch scenario {
			case "directory-mode":
				if err := os.Chmod(s.directory, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink-directory":
				original := s.directory
				s.directory += "-link"
				if err := os.Symlink(original, s.directory); err != nil {
					t.Fatal(err)
				}
			case "symlink-ancestor":
				link := s.exemptThrough + "-link"
				if err := os.Symlink(s.exemptThrough, link); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(link) })
				s.directory = filepath.Join(link, "credentials")
			case "writable-ancestor":
				s.exemptThrough = "" // /tmp or the normal-user fixture ancestor must fail.
			case "symlink-file":
				if err := os.Rename(keyPath, keyPath+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(keyPath+".real", keyPath); err != nil {
					t.Fatal(err)
				}
			case "file-mode":
				if err := os.Chmod(keyPath, 0640); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(keyPath); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(keyPath, 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-key":
				if err := os.WriteFile(keyPath, []byte(sentinelKey+"\ninvalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-service-owner":
				_, err := s.Install(OpenCodeGo, os.Geteuid()+1, os.Getegid(), []byte(sentinelKey), true, false)
				if !errors.Is(err, ErrCredential) {
					t.Fatalf("wrong service owner accepted: %v", err)
				}
				return
			}
			_, err := s.Status(OpenCodeGo)
			if !errors.Is(err, ErrCredential) {
				t.Fatalf("storage accepted: %v", err)
			}
			_, installErr := installKey(t, s, "replacement", true, false)
			removeErr := s.Remove(OpenCodeGo)
			if installErr == nil || removeErr == nil {
				t.Fatal("insecure storage mutated")
			}
			assertRedacted(t, err, installErr, removeErr)
		})
	}
}

func TestProviderCredentialRedaction(t *testing.T) {
	c, err := openrouter.CredentialsFromKey([]byte(sentinelKey))
	if err != nil {
		t.Fatal(err)
	}
	assertRedacted(t, c, c.String(), c.GoString(), c.Redact("wrapped: "+sentinelKey))
	encoded, err := c.MarshalJSON()
	assertRedacted(t, string(encoded), err)
	s := testStore(t)
	for _, key := range []string{"", sentinelKey + " bad", sentinelKey + "\x00"} {
		_, err := installKey(t, s, key, false, false)
		if err == nil {
			t.Fatal("invalid key accepted")
		}
		assertRedacted(t, err)
	}
}

func TestProviderFactoryPinsIdentity(t *testing.T) {
	for _, id := range []string{"", "openrouter", "../key"} {
		if _, err := ConversationFactory(id, "/etc/tbound-product/credentials/key"); err == nil {
			t.Fatal("unknown profile accepted")
		}
	}
	for _, path := range []string{"relative.key", "/etc/tbound-product/credentials/other.key", "/etc/tbound-product/../credentials/" + OpenCodeGo + ".key"} {
		if _, err := ConversationFactory(OpenCodeGo, path); err == nil {
			t.Fatal("bad path accepted")
		}
	}
	store := testStore(t)
	if _, err := installKey(t, store, sentinelKey, false, false); err != nil {
		t.Fatal(err)
	}
	factory, err := newConversationFactory(OpenCodeGo, filepath.Join(store.directory, OpenCodeGo+".key"), store.exemptThrough)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []providerbridge.Config{{ProfileID: "other"}, {ProfileID: OpenCodeGo, Route: openrouter.Route{Model: "wrong", Endpoint: "http://localhost"}}} {
		_, err := factory(cfg, nil)
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("factory pin: %v", err)
		}
		assertRedacted(t, err)
	}
}

func TestProviderFactoryBuildsServiceConversationAndEnforcesCap(t *testing.T) {
	s := testStore(t)
	if _, err := installKey(t, s, sentinelKey, false, false); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(filepath.Join(t.TempDir(), "provider.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	factory := s.conversationFactory(OpenCodeGo)
	cfg := providerbridge.Config{
		ProfileID: OpenCodeGo, ConversationID: "p7-conversation", WorkflowID: "p7-workflow",
		InitialGenerationID: "initial", InitialTreeDigest: strings.Repeat("a", 64),
		SystemPrompt: "You are a governed coding assistant.", DeveloperPrompt: "Use only authorized tools.",
		SelectCompletionTokenLimit: func(context.Context) (uint64, error) { return 16, nil },
		ReserveHostCall: func(context.Context, broker.RequestRecord, uint64) (providerbridge.HostCallBinding, error) {
			t.Fatal("no network dispatch authorized in factory test")
			return providerbridge.HostCallBinding{}, nil
		},
		CompleteHostCall: func(context.Context, providerbridge.HostCallCompletion) error { return nil },
	}
	conversation, err := factory(cfg, journal)
	if err != nil {
		t.Fatal(err)
	}
	conversation.Close()
	for _, cap := range []uint64{0, 2147483648} {
		cfg.SelectCompletionTokenLimit = func(context.Context) (uint64, error) { return cap, nil }
		conversation, err := factory(cfg, journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := conversation.AdmitPrompt("test-task", "Say OK."); err != nil {
			t.Fatal(err)
		}
		_, err = conversation.NextTurn(context.Background())
		if err == nil {
			t.Fatal("unbounded call accepted")
		}
		assertRedacted(t, err)
		conversation.Close()
	}
	if err := s.Remove(OpenCodeGo); err != nil {
		t.Fatal(err)
	}
	_, err = factory(cfg, journal)
	if !errors.Is(err, ErrCredential) {
		t.Fatal("removed credential still starts conversations")
	}
}

func TestProviderCredentialRotationAndEnvironmentIsolation(t *testing.T) {
	t.Setenv(openrouter.APIKeyEnv, "environment-key-must-not-be-used")
	t.Setenv(openrouter.APIKeyFileEnv, "/must/not/read")
	s := testStore(t)
	if _, err := s.credentials(OpenCodeGo); !errors.Is(err, ErrCredential) {
		t.Fatal("environment fallback accepted")
	}
	if _, err := installKey(t, s, sentinelKey, false, false); err != nil {
		t.Fatal(err)
	}
	first, err := s.credentials(OpenCodeGo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installKey(t, s, "replacement-key", true, true); err != nil {
		t.Fatal(err)
	}
	second, err := s.credentials(OpenCodeGo)
	if err != nil {
		t.Fatal(err)
	}
	if first.Redact(sentinelKey) != "[REDACTED]" || second.Redact("replacement-key") != "[REDACTED]" || second.Redact(sentinelKey) == "[REDACTED]" {
		t.Fatal("rotation did not select new key")
	}
	if err := s.Remove(OpenCodeGo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.credentials(OpenCodeGo); !errors.Is(err, ErrCredential) {
		t.Fatal("remove retained credential access")
	}
	assertRedacted(t, first, second)
}

func TestProviderConcurrentSetupDoesNotOverwrite(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{sentinelKey, "other-key"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := s.Install(OpenCodeGo, os.Geteuid(), os.Getegid(), []byte(key), false, false)
			results <- err
		}(key)
	}
	wg.Wait()
	close(results)
	ok, refused := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrExists) {
			refused++
		} else {
			t.Fatal(err)
		}
		assertRedacted(t, err)
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("setup race: %d success, %d refused", ok, refused)
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil || len(entries) != 1 {
		t.Fatal("setup race left temporary files")
	}
}

func TestProviderSetupCreatesServiceDirectory(t *testing.T) {
	s := testStore(t)
	if err := os.Remove(s.directory); err != nil {
		t.Fatal(err)
	}
	if _, err := installKey(t, s, sentinelKey, false, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.directory)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("setup did not create a private credential directory")
	}
	status, err := s.Status(OpenCodeGo)
	if err != nil || status.UID != uint32(os.Geteuid()) || status.GID != uint32(os.Getegid()) {
		t.Fatalf("service ownership: %+v %v", status, err)
	}
}

func TestReadSourceRefusesSymlinkAndWritableAncestors(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(real, "key")
	if err := os.WriteFile(path, []byte(sentinelKey), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSource(path); err != nil {
		t.Fatalf("secure source refused: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSource(filepath.Join(link, "key")); !errors.Is(err, ErrCredential) {
		t.Fatalf("symlink ancestor accepted: %v", err)
	}
	if err := os.Chmod(real, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSource(path); !errors.Is(err, ErrCredential) {
		t.Fatalf("group-writable ancestor accepted: %v", err)
	}
}

func TestConversationFactoryForTestIsRefusedOutsideATestBinaryAndKeepsTheCredentialRules(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "credentials")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, OpenCodeGo+".key")
	if err := os.WriteFile(key, []byte(sentinelKey), 0600); err != nil {
		t.Fatal(err)
	}
	original := testBinary
	defer func() { testBinary = original }()
	testBinary = func() bool { return false }
	if _, err := ConversationFactoryForTest(OpenCodeGo, key, base); !errors.Is(err, ErrCredential) {
		t.Fatalf("a test factory was built outside a test binary: %v", err)
	}
	testBinary = original
	for name, root := range map[string]string{"root above the key": "/nonexistent", "relative root": "x", "root equals the key": key} {
		if _, err := ConversationFactoryForTest(OpenCodeGo, key, root); !errors.Is(err, ErrCredential) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ConversationFactoryForTest(OpenCodeGo, key, base); err != nil {
		t.Fatalf("a credential below the exempt root was refused: %v", err)
	}
	// Only the ancestors are exempt: factory construction still refuses a group-readable key.
	if err := os.Chmod(key, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ConversationFactoryForTest(OpenCodeGo, key, base); !errors.Is(err, ErrCredential) {
		t.Fatalf("a key with a weak mode was used: %v", err)
	}
}
