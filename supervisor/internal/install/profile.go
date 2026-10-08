package install

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HostProfileOptions configures host-profile verification. The default targets
// the root-owned /etc/tbound location documented for the Linux host profile.
type HostProfileOptions struct {
	Dir              string
	RequireRootOwner bool
	OwnerUID         func(path string) (int, bool)
	ReadFile         func(string) ([]byte, error)
	Stat             func(string) (os.FileInfo, error)
}

// DefaultHostProfileOptions returns the documented /etc/tbound layout. It uses
// Lstat so the symlink guard actually observes a symlink instead of following
// it.
func DefaultHostProfileOptions() HostProfileOptions {
	p := DefaultProbes()
	return HostProfileOptions{
		Dir:              "/etc/tbound",
		RequireRootOwner: true,
		OwnerUID:         p.OwnerUID,
		ReadFile:         os.ReadFile,
		Stat:             os.Lstat,
	}
}

// HostProfileVerification is the outcome of verifying the admitted profile.
type HostProfileVerification struct {
	Valid         bool   `json:"valid"`
	ProfileDigest string `json:"profile_digest,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// ProfileFileNames are the fixed file names for the admitted host profile.
const (
	ProfileJSONName = "pi-host-profile.json"
	ProfileSigName  = "pi-host-profile.ed25519"
	ProfilePubName  = "pi-host-profile.pub"
)

// VerifyHostProfile validates the signed host profile: the Ed25519 signature
// over the exact profile bytes, an optional root-ownership requirement, and
// non-group/world-writable permissions. It fails closed on any missing or
// malformed input and never treats a boolean or fixture as authority.
func VerifyHostProfile(opts HostProfileOptions) (HostProfileVerification, error) {
	if opts.ReadFile == nil || opts.Stat == nil {
		return HostProfileVerification{}, errors.New("host profile verification requires file access")
	}
	profilePath := filepath.Join(opts.Dir, ProfileJSONName)
	sigPath := filepath.Join(opts.Dir, ProfileSigName)
	pubPath := filepath.Join(opts.Dir, ProfilePubName)

	for _, p := range []string{profilePath, sigPath, pubPath} {
		info, err := opts.Stat(p)
		if err != nil {
			return HostProfileVerification{}, fmt.Errorf("host profile %s: %w", filepath.Base(p), err)
		}
		if info.IsDir() {
			return HostProfileVerification{}, fmt.Errorf("host profile %s is a directory", filepath.Base(p))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return HostProfileVerification{}, fmt.Errorf("host profile %s is a symlink", filepath.Base(p))
		}
		if info.Mode().Perm()&0o022 != 0 {
			return HostProfileVerification{}, fmt.Errorf("host profile %s is group/world writable", filepath.Base(p))
		}
		if opts.RequireRootOwner && opts.OwnerUID != nil {
			if uid, ok := opts.OwnerUID(p); ok && uid != 0 {
				return HostProfileVerification{}, fmt.Errorf("host profile %s is not root-owned (uid=%d)", filepath.Base(p), uid)
			}
		}
	}

	profile, err := opts.ReadFile(profilePath)
	if err != nil {
		return HostProfileVerification{}, err
	}
	pub, err := opts.ReadFile(pubPath)
	if err != nil {
		return HostProfileVerification{}, err
	}
	sig, err := opts.ReadFile(sigPath)
	if err != nil {
		return HostProfileVerification{}, err
	}
	if len(pub) != ed25519.PublicKeySize {
		return HostProfileVerification{}, fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	sig, err = normalizeSignature(sig)
	if err != nil {
		return HostProfileVerification{}, err
	}
	if len(sig) != ed25519.SignatureSize {
		return HostProfileVerification{}, fmt.Errorf("signature must be %d bytes, got %d", ed25519.SignatureSize, len(sig))
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), profile, sig) {
		return HostProfileVerification{}, errors.New("host profile signature verification failed")
	}
	sum := sha256.Sum256(profile)
	return HostProfileVerification{
		Valid:         true,
		ProfileDigest: "sha256:" + hex.EncodeToString(sum[:]),
		Detail:        "signature verified",
	}, nil
}

func normalizeSignature(raw []byte) ([]byte, error) {
	if len(raw) == ed25519.SignatureSize {
		return raw, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == ed25519.SignatureSize*2 {
		if decoded, err := hex.DecodeString(trimmed); err == nil {
			return decoded, nil
		}
	}
	return raw, nil
}
