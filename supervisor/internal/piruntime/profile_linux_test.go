//go:build linux

package piruntime

import (
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"testing"
)

func TestLinuxSignedHostProfileVerifiesCanonicalPinnedProfile(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := testHostProfile()
	unsigned := profile
	unsigned.Digest = ""
	canonical, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	signedBytes := append([]byte(linuxHostProfileSignatureDomain), canonical...)
	profile.Digest = DigestBytes(signedBytes)
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, signedBytes)
	decoded, err := VerifyLinuxSignedHostProfile(profileJSON, signature, publicKey)
	if err != nil || decoded != profile {
		t.Fatalf("signed profile verify: decoded=%+v err=%v", decoded, err)
	}

	mutated := profile
	mutated.NodeVersion = "v99.0.0"
	mutatedJSON, err := json.Marshal(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyLinuxSignedHostProfile(mutatedJSON, signature, publicKey); !errors.Is(err, ErrHostAttestationMissing) {
		t.Fatalf("mutated profile retained signed authority: %v", err)
	}
}
