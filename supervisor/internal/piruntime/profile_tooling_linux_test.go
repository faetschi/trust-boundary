//go:build linux

package piruntime

import (
	"bytes"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPiWorkerCellIdentitiesAreStable(t *testing.T) {
	if got, want := DigestArguments(PiWorkerCellArguments()), DigestArguments([]string{"--experimental-strip-types", "/proc/self/fd/9/src/governed-pi-worker.ts"}); got != want {
		t.Fatalf("in-cell argv digest drifted: got %s want %s", got, want)
	}
	if got, want := DigestEnvironment(PiWorkerCellEnvironment()), DigestEnvironment([]string{"HOME=" + PiWorkerCellHome, "LANG=C.UTF-8", "PI_OFFLINE=1", "TMPDIR=" + PiWorkerCellTemp}); got != want {
		t.Fatalf("in-cell environment digest drifted: got %s want %s", got, want)
	}
	if err := DefaultLinuxHostProfileTemplate().Validate(); err != nil {
		// Digest is intentionally empty; validate the rest of the template by
		// signing it and checking the signed profile.
		t.Logf("unsigned template validate (expected missing Digest): %v", err)
	}
}

func TestLinuxHostProfileSignVerifyRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profile := DefaultLinuxHostProfileTemplate()
	signedJSON, signature, gotPublicKey, err := SignLinuxHostProfile(profile, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPublicKey, publicKey) {
		t.Fatalf("signer returned a public key that does not match the private key")
	}
	decoded, err := VerifyLinuxSignedHostProfile(signedJSON, signature, publicKey)
	if err != nil {
		t.Fatalf("signed template did not verify: %v", err)
	}
	if decoded.ContainmentProfile != D09ContainmentProfile || decoded.SettlementProfile != D09SettlementProfile {
		t.Fatalf("verified profile does not name the D09 profile: %+v", decoded)
	}
	if decoded.Digest != DigestBytes(mustSigningBytes(t, profile)) {
		t.Fatalf("signed digest is not the verifier digest: %s", decoded.Digest)
	}

	// A tampered field must invalidate the signature.
	var tampered map[string]any
	if err := json.Unmarshal(signedJSON, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["NodeVersion"] = "v99.0.0"
	tamperedJSON, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyLinuxSignedHostProfile(tamperedJSON, signature, publicKey); !errors.Is(err, ErrHostAttestationMissing) {
		t.Fatalf("tampered profile retained signed authority: %v", err)
	}

	// A different public key must not verify the signature.
	otherPublicKey, _, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyLinuxSignedHostProfile(signedJSON, signature, otherPublicKey); !errors.Is(err, ErrHostAttestationMissing) {
		t.Fatalf("signature verified under an unrelated public key: %v", err)
	}
}

func mustSigningBytes(t *testing.T, profile HostProfile) []byte {
	t.Helper()
	signingBytes, err := LinuxHostProfileSigningBytes(profile)
	if err != nil {
		t.Fatal(err)
	}
	return signingBytes
}

func TestLinuxHostProfileSignRejectsWrongKeySize(t *testing.T) {
	if _, _, _, err := SignLinuxHostProfile(DefaultLinuxHostProfileTemplate(), ed25519.PrivateKey([]byte("short"))); !errors.Is(err, ErrHostProfileMissing) {
		t.Fatalf("short private key was accepted: %v", err)
	}
}

func TestVerifyLinuxSignedHostProfileFilesRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signedJSON, signature, _, err := SignLinuxHostProfile(DefaultLinuxHostProfileTemplate(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "pi-host-profile.json")
	sigPath := filepath.Join(dir, "pi-host-profile.ed25519")
	pubPath := filepath.Join(dir, "pi-host-profile.pub")
	for path, data := range map[string][]byte{profilePath: signedJSON, sigPath: signature, pubPath: publicKey} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := VerifyLinuxSignedHostProfileFiles(profilePath, sigPath, pubPath)
	if err != nil {
		t.Fatalf("verify signed profile files: %v", err)
	}
	if loaded.ID != DefaultLinuxHostProfileTemplate().ID {
		t.Fatalf("verified profile identity differs: %+v", loaded)
	}
	if err := os.WriteFile(pubPath, bytes.Repeat([]byte{0x00}, ed25519.PublicKeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyLinuxSignedHostProfileFiles(profilePath, sigPath, pubPath); !errors.Is(err, ErrHostAttestationMissing) {
		t.Fatalf("verification accepted an unrelated public key: %v", err)
	}
}

// TestLoadLinuxSignedHostProfileRefusesUnprotectedPath proves the production
// loader never treats an operator-supplied path as its own trust root: a
// non-root-owned profile directory and the absent fixed admin key both fail
// closed.
func TestLoadLinuxSignedHostProfileRefusesUnprotectedPath(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signedJSON, signature, _, err := SignLinuxHostProfile(DefaultLinuxHostProfileTemplate(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(linuxTrustedProfilePath)), signedJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(linuxTrustedProfileSigPath)), signature, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLinuxSignedHostProfile(dir); err == nil {
		t.Fatal("loader accepted an unprotected operator-supplied profile directory")
	}
}

func TestResolveLinuxHostProfileDataSources(t *testing.T) {
	fallback := resolveLinuxHostProfileDataSources(filepath.Join(t.TempDir(), "absent.json"))
	if !fallback.rootOwned || fallback.profile != linuxTrustedProfilePath || fallback.signature != linuxTrustedProfileSigPath {
		t.Fatalf("absent path did not resolve to the root-owned trust root: %+v", fallback)
	}
	fileDir := t.TempDir()
	filePath := filepath.Join(fileDir, "pi-host-profile.json")
	if err := os.WriteFile(filePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := resolveLinuxHostProfileDataSources(filePath)
	if file.rootOwned || file.profile != filePath || file.signature != filepath.Join(fileDir, "pi-host-profile.ed25519") {
		t.Fatalf("file path did not derive the sibling signature path: %+v", file)
	}
	dir := resolveLinuxHostProfileDataSources(t.TempDir())
	if dir.rootOwned || filepath.Base(dir.profile) != filepath.Base(linuxTrustedProfilePath) ||
		filepath.Base(dir.signature) != filepath.Base(linuxTrustedProfileSigPath) {
		t.Fatalf("directory path did not use the fixed file names: %+v", dir)
	}
}
