//go:build linux

package openrouter

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const ancestorTestKey = "ancestor-test-secret-key-123"

// useServicePolicy routes path through the strict service-credential walk for
// the duration of a test, as the two fixed production paths always are.
func useServicePolicy(t *testing.T, path string, policy credentialAncestorPolicy) {
	t.Helper()
	oldPaths, oldPolicy := serviceCredentialPaths, serviceCredentialPolicy
	serviceCredentialPaths, serviceCredentialPolicy = []string{path}, policy
	t.Cleanup(func() { serviceCredentialPaths, serviceCredentialPolicy = oldPaths, oldPolicy })
}

// ancestorFixture builds <base>/etc/tbound/provider/key.key. Every directory
// is 0755 and owned by the test user. The returned policy trusts the test
// user's uid and exempts only the temp base and everything above it, so the
// directories this test controls are walked exactly as root-owned ones are in
// production.
func ancestorFixture(t *testing.T) (base, directory, file string, relaxed credentialAncestorPolicy) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base = resolved
	directory = filepath.Join(base, "etc", "tbound", "provider")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(base, "etc"), filepath.Join(base, "etc", "tbound"), directory} {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file = filepath.Join(directory, "key.key")
	if err := os.WriteFile(file, []byte(ancestorTestKey+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	return base, directory, file, credentialAncestorPolicy{trustedUID: uint32(os.Geteuid()), exemptThrough: base}
}

func loadFixedPath(t *testing.T, path string) error {
	t.Helper()
	credentials, err := LoadCredentialsFrom(func(name string) (string, bool) {
		if name == APIKeyFileEnv {
			return path, true
		}
		return "", false
	})
	if err != nil {
		if strings.Contains(err.Error(), ancestorTestKey) {
			t.Fatal("refusal leaked the key")
		}
		return err
	}
	if credentials.Redact(ancestorTestKey) != "[REDACTED]" {
		t.Fatal("accepted credential did not carry the file's key")
	}
	return nil
}

func TestServiceCredentialPathsAreExactlyTheTwoFixedKeys(t *testing.T) {
	if got := ServiceCredentialPaths(); !reflect.DeepEqual(got, []string{
		"/etc/tbound/provider/opencode-go.key", "/etc/tbound/provider/openrouter.key"}) {
		t.Fatalf("service credential paths = %v", got)
	}
	if serviceCredentialPolicy != (credentialAncestorPolicy{trustedUID: 0}) {
		t.Fatalf("production ancestor policy = %+v; must trust only root and exempt nothing", serviceCredentialPolicy)
	}
	got := ServiceCredentialPaths()
	got[0] = "/tmp/x"
	if ServiceCredentialPaths()[0] == "/tmp/x" {
		t.Fatal("ServiceCredentialPaths exposes the internal slice")
	}
}

func TestServiceCredentialAncestorWalkAcceptsTrustedChain(t *testing.T) {
	_, _, file, relaxed := ancestorFixture(t)
	useServicePolicy(t, file, relaxed)
	if err := loadFixedPath(t, file); err != nil {
		t.Fatalf("trusted real-directory chain was refused: %v", err)
	}
}

func TestServiceCredentialAncestorWalkRefusesSymlinkedAncestors(t *testing.T) {
	base, directory, file, relaxed := ancestorFixture(t)
	// A symlinked parent, grandparent and great-grandparent each reach the
	// same real file; none may be followed.
	cases := map[string]string{}
	parentLink := filepath.Join(base, "parent-link")
	if err := os.Symlink(directory, parentLink); err != nil {
		t.Fatal(err)
	}
	cases["parent"] = filepath.Join(parentLink, "key.key")
	grandLink := filepath.Join(base, "grand-link")
	if err := os.Symlink(filepath.Join(base, "etc", "tbound"), grandLink); err != nil {
		t.Fatal(err)
	}
	cases["grandparent"] = filepath.Join(grandLink, "provider", "key.key")
	topLink := filepath.Join(base, "top-link")
	if err := os.Symlink(filepath.Join(base, "etc"), topLink); err != nil {
		t.Fatal(err)
	}
	cases["top"] = filepath.Join(topLink, "tbound", "provider", "key.key")
	cases["final component"] = filepath.Join(directory, "final-link.key")
	if err := os.Symlink(file, cases["final component"]); err != nil {
		t.Fatal(err)
	}
	for name, path := range cases {
		useServicePolicy(t, path, relaxed)
		if err := loadFixedPath(t, path); err == nil {
			t.Fatalf("a symlinked %s was followed and the key accepted", name)
		}
	}
}

func TestServiceCredentialAncestorWalkRefusesGroupOrOtherWritableAncestors(t *testing.T) {
	base, directory, file, relaxed := ancestorFixture(t)
	for _, target := range []string{directory, filepath.Join(base, "etc", "tbound"), filepath.Join(base, "etc")} {
		for _, mode := range []os.FileMode{0o775, 0o757, 0o777, 0o755 | os.ModeSticky | 0o002} {
			if err := os.Chmod(target, mode); err != nil {
				t.Fatal(err)
			}
			useServicePolicy(t, file, relaxed)
			if err := loadFixedPath(t, file); err == nil {
				t.Fatalf("ancestor %s with mode %v was accepted", target, mode)
			}
			if err := os.Chmod(target, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	useServicePolicy(t, file, relaxed)
	if err := loadFixedPath(t, file); err != nil {
		t.Fatalf("restored chain was refused: %v", err)
	}
}

func TestServiceCredentialAncestorWalkRefusesAncestorsNotOwnedByTheTrustedUID(t *testing.T) {
	_, _, file, relaxed := ancestorFixture(t)
	// The fixture is owned by the test user; trusting a different uid models
	// "an ancestor owned by someone other than root" without needing root.
	relaxed.trustedUID = uint32(os.Geteuid()) + 1
	useServicePolicy(t, file, relaxed)
	if err := loadFixedPath(t, file); err == nil {
		t.Fatal("ancestors owned by an untrusted uid were accepted")
	}
}

func TestServiceCredentialWalkRefusesNonCanonicalAliasOfAFixedPath(t *testing.T) {
	base, _, file, relaxed := ancestorFixture(t)
	useServicePolicy(t, file, relaxed)
	if _, err := readSecureCredentialFile(file); err != nil {
		t.Fatalf("canonical fixed path was refused: %v", err)
	}
	for _, alias := range []string{
		filepath.Join(base, "etc") + "/../etc/tbound/provider/key.key",
		strings.Replace(file, "/etc/", "//etc/", 1),
		strings.Replace(file, "/etc/", "/./etc/", 1),
	} {
		if alias == file {
			t.Fatalf("alias %q is not an alias", alias)
		}
		if _, err := readSecureCredentialFile(alias); err == nil {
			t.Fatalf("non-canonical alias %q of a fixed service path was accepted", alias)
		}
	}
}

func TestOtherPathsKeepTheFinalComponentRule(t *testing.T) {
	// Not a fixed service path: the dev/env behavior is unchanged, so a file
	// below a group-writable parent is still read by the existing loader.
	_, directory, file, _ := ancestorFixture(t)
	if err := os.Chmod(directory, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := loadFixedPath(t, file); err != nil {
		t.Fatalf("dev/env path behavior changed: %v", err)
	}
}

// Root-only: with the real production policy (root-owned, not group/other
// writable ancestors, nothing exempt) a chain of root-owned directories is
// accepted, and each defect is refused. Skipped when not root.
func TestServiceCredentialProductionPolicyAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("production ancestor policy needs root-owned directories; run as root")
	}
	base, directory, file, _ := ancestorFixture(t)
	production := credentialAncestorPolicy{trustedUID: 0}
	useServicePolicy(t, file, production)
	if err := loadFixedPath(t, file); err != nil {
		t.Fatalf("the temp base %s has an ancestor that is not root-owned/non-writable; use a root-private TMPDIR: %v", base, err)
	}

	for _, target := range []string{directory, filepath.Join(base, "etc")} {
		if err := os.Chown(target, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		if err := loadFixedPath(t, file); err == nil {
			t.Fatalf("ancestor %s owned by uid 65534 was accepted", target)
		}
		if err := os.Chown(target, 0, 0); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o775); err != nil {
			t.Fatal(err)
		}
		if err := loadFixedPath(t, file); err == nil {
			t.Fatalf("group-writable root-owned ancestor %s was accepted", target)
		}
		if err := os.Chmod(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	useServicePolicy(t, filepath.Join(link, "key.key"), production)
	if err := loadFixedPath(t, filepath.Join(link, "key.key")); err == nil {
		t.Fatal("a symlinked ancestor was followed under the production policy")
	}
	useServicePolicy(t, file, production)
	if err := os.Chown(file, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if err := loadFixedPath(t, file); err == nil {
		t.Fatal("a key file not owned by the process euid was accepted")
	}
}
