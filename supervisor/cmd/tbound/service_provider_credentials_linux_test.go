//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
)

const serviceProviderTestKey = "service-file-opencode-secret-123"

func serviceProviderKeyFixture(t *testing.T, mode os.FileMode, content string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "provider")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "opencode-go.key")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireServiceProviderRefusal(t *testing.T, path string) {
	t.Helper()
	credentials, err := loadServiceProviderCredentials(path)
	if err == nil || !errors.Is(err, ErrServiceProviderCredentials) {
		t.Fatalf("credential file %q was not refused with the service credential error: %v", path, err)
	}
	if strings.Contains(err.Error(), serviceProviderTestKey) || credentials.Redact(serviceProviderTestKey) != serviceProviderTestKey {
		t.Fatal("a refused credential load leaked or retained key material")
	}
}

func TestServiceProviderCredentialPathIsTheFixedManagedPath(t *testing.T) {
	if piruntime.D09ManagedPiProviderCredentialPath != "/etc/tbound/provider/opencode-go.key" {
		t.Fatalf("provider credential path drifted from the runbook: %q", piruntime.D09ManagedPiProviderCredentialPath)
	}
}

func TestServiceProviderCredentialsLoadFromValidKeyFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o400, 0o600} {
		path := serviceProviderKeyFixture(t, mode, serviceProviderTestKey+"\n")
		credentials, err := loadServiceProviderCredentials(path)
		if err != nil {
			t.Fatalf("mode %o valid key file was refused: %v", mode, err)
		}
		if credentials.Redact(serviceProviderTestKey) != "[REDACTED]" {
			t.Fatalf("mode %o key was not loaded from the file", mode)
		}
	}
}

func TestServiceProviderCredentialsRefuseUnsafeKeyFiles(t *testing.T) {
	directory := t.TempDir()
	valid := serviceProviderKeyFixture(t, 0o400, serviceProviderTestKey)

	t.Run("missing directory", func(t *testing.T) {
		requireServiceProviderRefusal(t, filepath.Join(directory, "absent", "opencode-go.key"))
	})
	t.Run("missing file", func(t *testing.T) {
		requireServiceProviderRefusal(t, filepath.Join(filepath.Dir(valid), "other.key"))
	})
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(directory, "link.key")
		if err := os.Symlink(valid, link); err != nil {
			t.Fatal(err)
		}
		requireServiceProviderRefusal(t, link)
	})
	t.Run("directory", func(t *testing.T) {
		requireServiceProviderRefusal(t, filepath.Dir(valid))
	})
	for _, mode := range []os.FileMode{0o644, 0o640, 0o440, 0o604, 0o700, 0o000} {
		mode := mode
		t.Run("mode "+strings.TrimPrefix(mode.String(), "-"), func(t *testing.T) {
			requireServiceProviderRefusal(t, serviceProviderKeyFixture(t, mode, serviceProviderTestKey))
		})
	}
	t.Run("empty", func(t *testing.T) {
		requireServiceProviderRefusal(t, serviceProviderKeyFixture(t, 0o400, ""))
	})
	t.Run("whitespace only", func(t *testing.T) {
		requireServiceProviderRefusal(t, serviceProviderKeyFixture(t, 0o400, " \n\t\n"))
	})
	t.Run("oversized", func(t *testing.T) {
		requireServiceProviderRefusal(t, serviceProviderKeyFixture(t, 0o400, strings.Repeat("a", 4097)))
	})
	t.Run("embedded whitespace", func(t *testing.T) {
		requireServiceProviderRefusal(t, serviceProviderKeyFixture(t, 0o400, "two words"))
	})
}

func TestServiceProviderCredentialsRefuseForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("creating a file owned by another uid requires root; the owner rule is openrouter.readSecureCredentialFile uid == euid")
	}
	path := serviceProviderKeyFixture(t, 0o400, serviceProviderTestKey)
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	// Relax the parent so only the file owner differs from the process euid.
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if stat, ok := mustStat(t, path).Sys().(*syscall.Stat_t); !ok || stat.Uid == 0 {
		t.Fatal("fixture was not made foreign-owned")
	}
	requireServiceProviderRefusal(t, path)
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestServiceProviderCredentialsIgnoreEnvironment(t *testing.T) {
	t.Setenv(openrouter.APIKeyEnv, "environment-key-must-be-ignored")
	missing := filepath.Join(t.TempDir(), "opencode-go.key")
	t.Setenv(openrouter.APIKeyFileEnv, serviceProviderKeyFixture(t, 0o400, serviceProviderTestKey))
	requireServiceProviderRefusal(t, missing)

	// With a valid fixed file, the environment key and environment file path
	// are both ignored: only the fixed file's key is loaded.
	t.Setenv(openrouter.APIKeyEnv, "environment-key-must-be-ignored")
	t.Setenv(openrouter.APIKeyFileEnv, missing)
	path := serviceProviderKeyFixture(t, 0o400, serviceProviderTestKey)
	credentials, err := loadServiceProviderCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Redact(serviceProviderTestKey) != "[REDACTED]" ||
		credentials.Redact("environment-key-must-be-ignored") != "environment-key-must-be-ignored" {
		t.Fatal("service credentials did not come exclusively from the fixed key file")
	}
}

func TestServiceProviderConversationFactoryRefusesBeforeAnyFallback(t *testing.T) {
	t.Setenv(openrouter.APIKeyEnv, "environment-key-must-be-ignored")
	t.Setenv(openrouter.APIKeyFileEnv, serviceProviderKeyFixture(t, 0o400, serviceProviderTestKey))
	factory := serviceProviderConversationFactory(filepath.Join(t.TempDir(), "opencode-go.key"))
	conversation, err := factory(providerbridge.Config{}, nil)
	if conversation != nil || !errors.Is(err, ErrServiceProviderCredentials) {
		t.Fatalf("missing service key file fell back or did not refuse: conversation=%v err=%v", conversation, err)
	}

	// A valid fixed file gets past the credential gate; the nil journal then
	// refuses for its own reason, proving the factory really loaded the file.
	factory = serviceProviderConversationFactory(serviceProviderKeyFixture(t, 0o400, serviceProviderTestKey))
	_, err = factory(providerbridge.Config{}, nil)
	if err == nil || errors.Is(err, ErrServiceProviderCredentials) {
		t.Fatalf("valid service key file did not reach the conversation constructor: %v", err)
	}
}
