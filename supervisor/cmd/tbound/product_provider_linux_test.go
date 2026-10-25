//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/productprovider"
)

const providerTestSecret = "CLI-P7-SENTINEL-secret-never-print-ec931"

type fakeProviderTerminal struct {
	calls int
	err   error
}

func (f *fakeProviderTerminal) ReadSecret() ([]byte, error) {
	f.calls++
	return []byte(providerTestSecret), f.err
}

type fakeProviderStore struct {
	status                                     productprovider.Status
	installs, checks, removes                  int
	installErr, statusErr, checkErr, removeErr error
	lastReplace, lastRotate                    bool
}

func (f *fakeProviderStore) Install(id string, uid, gid int, key []byte, replace, rotate bool) (productprovider.Rotation, error) {
	f.installs++
	f.lastReplace, f.lastRotate = replace, rotate
	if id != productprovider.OpenCodeGo || uid != os.Geteuid() || gid != os.Getegid() || string(key) != providerTestSecret {
		return productprovider.Rotation{}, productprovider.ErrCredential
	}
	if f.installErr != nil {
		return productprovider.Rotation{}, f.installErr
	}
	old := f.status.Fingerprint
	f.status = productprovider.Status{Present: true, UID: uint32(uid), GID: uint32(gid), Mode: "0600", Fingerprint: productprovider.Fingerprint(key)}
	return productprovider.Rotation{OldFingerprint: old, NewFingerprint: f.status.Fingerprint}, nil
}
func (f *fakeProviderStore) Status(string) (productprovider.Status, error) {
	return f.status, f.statusErr
}
func (f *fakeProviderStore) Check(_ context.Context, _ string, cap uint64) (productprovider.CheckResult, error) {
	f.checks++
	return productprovider.CheckResult{Calls: 1, ModelMatch: true, UsagePresent: true, MaxTokens: cap}, f.checkErr
}
func (f *fakeProviderStore) Remove(string) error {
	f.removes++
	f.status = productprovider.Status{}
	return f.removeErr
}

func providerTestCommand(t *testing.T) (providerCommand, *fakeProviderStore, *fakeProviderTerminal) {
	t.Helper()
	s := &fakeProviderStore{}
	terminal := &fakeProviderTerminal{}
	return providerCommand{store: s, terminal: terminal, readSource: productprovider.ReadSource, lookupUser: func(string) (int, int, error) { return os.Geteuid(), os.Getegid(), nil }}, s, terminal
}
func executeProviderTest(t *testing.T, c providerCommand, args ...string) (int, string, error) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	err := c.run(context.Background(), args, &out, &diagnostic)
	code := 0
	if err != nil {
		var exit interface{ ExitCode() int }
		if !errors.As(err, &exit) {
			t.Fatalf("uncoded error: %v", err)
		}
		code = exit.ExitCode()
	}
	combined := out.String() + diagnostic.String()
	if strings.Contains(combined, providerTestSecret) || (err != nil && strings.Contains(fmt.Sprintf("%+v %#v", err, err), providerTestSecret)) {
		t.Fatal("secret disclosed")
	}
	return code, combined, err
}

func TestProviderCLILifecycleAndOffline(t *testing.T) {
	c, s, terminal := providerTestCommand(t)
	id := productprovider.OpenCodeGo
	code, out, _ := executeProviderTest(t, c, "setup", "--profile", id, "--service-user", "current-test-user")
	if code != 0 || terminal.calls != 1 || s.installs != 1 || !strings.Contains(out, "fingerprint=") || s.lastReplace {
		t.Fatalf("setup: %d %s", code, out)
	}
	code, out, _ = executeProviderTest(t, c, "status", "--profile", id)
	if code != 0 || !strings.Contains(out, `"present":true`) || !strings.Contains(out, `"mode":"0600"`) {
		t.Fatalf("status: %d %s", code, out)
	}
	code, out, _ = executeProviderTest(t, c, "check", "--profile", id, "--offline")
	if code != 0 || !strings.Contains(out, "offline configuration valid") {
		t.Fatalf("offline: %d %s", code, out)
	}
	code, out, _ = executeProviderTest(t, c, "rotate", "--profile", id, "--service-user", "current-test-user")
	if code != 0 || !s.lastRotate || !s.lastReplace || !strings.Contains(out, "old_fingerprint=") || !strings.Contains(out, "new_fingerprint=") {
		t.Fatalf("rotate: %d %s", code, out)
	}
	code, _, _ = executeProviderTest(t, c, "remove", "--profile", id)
	if code != 0 || s.removes != 1 {
		t.Fatal("remove failed")
	}
	code, _, _ = executeProviderTest(t, c, "status", "--profile", id)
	if code != 0 {
		t.Fatal("absent status failed")
	}
	code, _, _ = executeProviderTest(t, c, "check", "--profile", id, "--offline")
	if code != 3 {
		t.Fatal("absent offline check accepted")
	}
	if s.checks != 0 {
		t.Fatal("offline operation performed live check")
	}
}

func TestProviderCLIProtectedSource(t *testing.T) {
	c, s, terminal := providerTestCommand(t)
	source := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(source, []byte(providerTestSecret), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"setup", "--profile", productprovider.OpenCodeGo, "--service-user", "current-test-user", "--from-file", source}
	code, _, _ := executeProviderTest(t, c, base...)
	if code != 0 || terminal.calls != 0 || s.installs != 1 {
		t.Fatal("protected file setup failed")
	}
	if err := os.Chmod(source, 0644); err != nil {
		t.Fatal(err)
	}
	code, _, _ = executeProviderTest(t, c, base...)
	if code != 3 || s.installs != 1 {
		t.Fatal("world-readable source accepted")
	}
	if err := os.Chmod(source, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, source+"-link"); err != nil {
		t.Fatal(err)
	}
	base[len(base)-1] = source + "-link"
	code, _, _ = executeProviderTest(t, c, base...)
	if code != 3 || s.installs != 1 {
		t.Fatal("symlink source accepted")
	}
}

func TestProviderCLIRefusalsAndAuthorization(t *testing.T) {
	id := productprovider.OpenCodeGo
	for _, args := range [][]string{
		{}, {"unknown-" + providerTestSecret}, {"list", "--key", providerTestSecret},
		{"status", "--profile", "unknown"}, {"setup", "--profile", id},
		{"setup", "--profile", id, "--service-user", "current", "--key", providerTestSecret},
		{"setup", "--profile", id, "--service-user", "current", "--url", "https://elsewhere"},
		{"check", "--profile", id}, {"check", "--profile", id, "--yes"}, {"check", "--profile", id, "--live"},
		{"check", "--profile", id, "--live", "--yes"}, {"check", "--profile", id, "--live", "--max-tokens", "16"},
		{"check", "--profile", id, "--live", "--yes", "--max-tokens", "2147483648"},
		{"check", "--profile", id, "--live", "--yes", "--max-tokens", "-1"},
		{"check", "--profile", id, "--live", "--offline", "--yes", "--max-tokens", "16"},
		{"check", "--profile", id, "--offline", "--yes"}, {"check", "--profile", id, "--offline", "--max-tokens", "16"},
		{"status", "--profile", id, "--from-file", "a"}, {"remove", "--profile", id, "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			c, s, terminal := providerTestCommand(t)
			code, _, _ := executeProviderTest(t, c, args...)
			if code != 2 || s.checks != 0 || s.installs != 0 || s.removes != 0 || terminal.calls != 0 {
				t.Fatalf("invalid arguments had effects: code %d", code)
			}
		})
	}
	c, s, _ := providerTestCommand(t)
	code, out, _ := executeProviderTest(t, c, "check", "--profile", id, "--live", "--max-tokens", "16", "--yes")
	if code != 0 || s.checks != 1 || !strings.Contains(out, `"model_identity_match":true`) || !strings.Contains(out, `"usage_present":true`) {
		t.Fatal("authorized live check not run once")
	}
}

func TestProviderCLIExitCodesAndRedaction(t *testing.T) {
	for _, scenario := range []struct {
		name string
		code int
	}{
		{"exists", 2}, {"absent", 2}, {"credential", 3}, {"storage", 1}, {"terminal", 3}, {"user", 2}, {"source", 3}, {"check", 3}, {"status", 3}, {"remove", 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c, s, terminal := providerTestCommand(t)
			args := []string{"setup", "--profile", productprovider.OpenCodeGo, "--service-user", "current"}
			secretErr := fmt.Errorf("wrapped: %w", errors.New(providerTestSecret))
			switch scenario.name {
			case "exists":
				s.installErr = productprovider.ErrExists
			case "absent":
				s.installErr = productprovider.ErrAbsent
			case "credential":
				s.installErr = fmt.Errorf("%s: %w", providerTestSecret, productprovider.ErrCredential)
			case "storage":
				s.installErr = secretErr
			case "terminal":
				terminal.err = secretErr
			case "source":
				c.readSource = func(string) ([]byte, error) { return nil, secretErr }
				args = append(args, "--from-file", "protected")
			case "user":
				c.lookupUser = func(string) (int, int, error) { return 0, 0, secretErr }
			case "check":
				s.checkErr = errors.New("provider authentication failed; key may be expired or revoked: rotate the credential")
				args = []string{"check", "--profile", productprovider.OpenCodeGo, "--live", "--yes", "--max-tokens", "16"}
			case "status":
				s.statusErr = productprovider.ErrCredential
				args = []string{"status", "--profile", productprovider.OpenCodeGo}
			case "remove":
				s.removeErr = secretErr
				args = []string{"remove", "--profile", productprovider.OpenCodeGo}
			}
			code, _, _ := executeProviderTest(t, c, args...)
			if code != scenario.code {
				t.Fatalf("want %d got %d", scenario.code, code)
			}
		})
	}
}

func TestProviderCLIListHelpAndDispatch(t *testing.T) {
	t.Setenv("TBOUND_OPENCODE_GO_API_KEY", providerTestSecret)
	t.Setenv("TBOUND_OPENCODE_GO_API_KEY_FILE", "/must/not/read")
	for _, args := range [][]string{{"provider", "list"}, {"provider", "--help"}, {"provider", "check", "--help"}} {
		var out, stderr bytes.Buffer
		if err := run(args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 || strings.Contains(out.String()+stderr.String(), providerTestSecret) {
			t.Fatal("bad dispatch output")
		}
	}
	c, _, _ := providerTestCommand(t)
	if err := c.run(context.Background(), []string{"list"}, providerFailWriter{}, io.Discard); err == nil {
		t.Fatal("output failure accepted")
	}
	uid, gid, err := providerServiceUser(currentProviderUsername(t))
	if err != nil || uid != os.Geteuid() || gid != os.Getegid() {
		t.Fatalf("current service account: %d %d %v", uid, gid, err)
	}
}

type providerFailWriter struct{}

func (providerFailWriter) Write([]byte) (int, error) { return 0, errors.New(providerTestSecret) }
func currentProviderUsername(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.Username
}
