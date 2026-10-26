package releasetrust

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func signedRelease(t *testing.T) (dir string, artifact string, manifest, sig []byte, pub ed25519.PublicKey) {
	t.Helper()
	dir = t.TempDir()
	artifact = filepath.Join(dir, "tbound-1.0-linux-amd64.tar.gz")
	if err := os.WriteFile(artifact, []byte("genuine release bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if manifest, err = BuildManifest([]string{artifact}); err != nil {
		t.Fatal(err)
	}
	if sig, err = Sign(manifest, key); err != nil {
		t.Fatal(err)
	}
	return dir, artifact, manifest, sig, pub
}

func TestManifestSignVerifyRoundTrip(t *testing.T) {
	_, artifact, manifest, sig, pub := signedRelease(t)
	m, err := Verify(manifest, sig, pub)
	if err != nil {
		t.Fatalf("a genuine signed manifest was refused: %v", err)
	}
	if err := VerifyArtifacts(m, []string{artifact}); err != nil {
		t.Fatalf("the genuine artifact was refused: %v", err)
	}
	if _, err := Verify(append(manifest, '\n'), sig, pub); err != nil {
		t.Fatalf("a manifest file with its trailing newline was refused: %v", err)
	}
}

func TestTamperedArtifactWithReplacedChecksumFileIsRefused(t *testing.T) {
	dir, artifact, manifest, sig, pub := signedRelease(t)
	if err := os.WriteFile(artifact, []byte("malicious bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The attacker also replaces the same-origin checksum file. It is not an input to verification.
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte("deadbeef  tbound-1.0-linux-amd64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Verify(manifest, sig, pub)
	if err != nil {
		t.Fatalf("the signed manifest itself is untouched: %v", err)
	}
	if err := VerifyArtifacts(m, []string{artifact}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a tampered artifact was accepted: %v", err)
	}
}

func TestWrongKeyAndTamperedManifestAreRefused(t *testing.T) {
	_, _, manifest, sig, pub := signedRelease(t)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Verify(manifest, sig, other); !errors.Is(err, ErrRefused) {
		t.Fatalf("a signature under another key was accepted: %v", err)
	}
	tampered := []byte(strings.Replace(string(manifest), `"size":21`, `"size":22`, 1))
	if string(tampered) == string(manifest) {
		t.Fatal("test setup did not change the manifest")
	}
	if _, err := Verify(tampered, sig, pub); !errors.Is(err, ErrRefused) {
		t.Fatalf("a tampered manifest was accepted: %v", err)
	}
}

func TestManifestMustBeCanonicalAndListEveryArtifact(t *testing.T) {
	dir, artifact, manifest, sig, pub := signedRelease(t)
	spaced := strings.Replace(string(manifest), `,"artifacts"`, `, "artifacts"`, 1)
	if _, err := Verify([]byte(spaced), sig, pub); !errors.Is(err, ErrRefused) {
		t.Fatalf("a differently spelled copy of the manifest was accepted: %v", err)
	}
	m, err := Verify(manifest, sig, pub)
	if err != nil {
		t.Fatal(err)
	}
	stranger := filepath.Join(dir, "extra.tar.gz")
	if err := os.WriteFile(stranger, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(m, []string{artifact, stranger}); !errors.Is(err, ErrRefused) {
		t.Fatalf("an artifact the manifest does not list was accepted: %v", err)
	}
}

func TestFingerprintIsStable(t *testing.T) {
	// SHA-256 of 32 zero bytes begins 66687aadf862bd77.
	if got := Fingerprint(make([]byte, ed25519.PublicKeySize)); got != "66687aadf862bd77" {
		t.Fatalf("fingerprint = %s", got)
	}
}

func TestWriteKeyPairModesAndNoOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	pub, err := WriteKeyPair(dir, "release")
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadPublicKey(filepath.Join(dir, "release.pub"))
	if err != nil || !pub.Equal(read) {
		t.Fatalf("public key did not round trip: %v", err)
	}
	if _, err := ReadPrivateKey(filepath.Join(dir, "release.key")); err != nil {
		t.Fatalf("the generated private key is unreadable: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "release.key"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private key mode = %v, %v", info.Mode().Perm(), err)
		}
		if err := os.Chmod(filepath.Join(dir, "release.key"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPrivateKey(filepath.Join(dir, "release.key")); !errors.Is(err, ErrRefused) {
			t.Fatalf("a world-readable private key was accepted: %v", err)
		}
	}
	if _, err := WriteKeyPair(dir, "release"); err == nil {
		t.Fatal("an existing key pair was overwritten")
	}
}
