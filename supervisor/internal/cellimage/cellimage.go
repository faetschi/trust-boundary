// Package cellimage verifies the signed cell image TBound uses for isolated
// command execution. Verification is digest- and signature-based: a missing
// cosign, digest, or public key fails closed, and a caller boolean is never
// accepted as evidence.
package cellimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Runner executes an external command and returns its stdout. It is injected so
// tests do not execute cosign.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Options configure verification.
type Options struct {
	ImageRef       string
	ExpectedDigest string // "sha256:<64 hex>"
	PubKeyPath     string
	CosignPath     string // defaults to "cosign"
	Runner         Runner
}

// Verified records the verified image identity for the installer.
type Verified struct {
	ImageRef string `json:"image_ref"`
	Digest   string `json:"digest"`
	Signer   string `json:"signer,omitempty"`
}

// cosignRecord is the minimal shape of `cosign verify --output json`.
type cosignRecord struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
	} `json:"critical"`
	Optional map[string]any `json:"optional"`
}

// ValidDigest reports whether s is a lowercase sha256 digest.
func ValidDigest(s string) bool {
	if !strings.HasPrefix(s, "sha256:") {
		return false
	}
	hex := strings.TrimPrefix(s, "sha256:")
	if len(hex) != 64 {
		return false
	}
	for _, r := range hex {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Verify runs cosign against the image and requires the manifest digest to
// match ExpectedDigest. It fails closed on any missing input or mismatch.
func Verify(ctx context.Context, opts Options) (Verified, error) {
	if opts.ImageRef == "" || !strings.Contains(opts.ImageRef, "/") {
		return Verified{}, errors.New("cell image reference is required and must be a full reference")
	}
	if !ValidDigest(opts.ExpectedDigest) {
		return Verified{}, errors.New("expected cell image digest must be a sha256 digest")
	}
	if opts.PubKeyPath == "" {
		return Verified{}, errors.New("cosign public key path is required")
	}
	if opts.Runner == nil {
		return Verified{}, errors.New("cell image verification requires a runner")
	}
	cosign := opts.CosignPath
	if cosign == "" {
		cosign = "cosign"
	}
	out, err := opts.Runner.Run(ctx, cosign, "verify", "--key", opts.PubKeyPath, "--output", "json", opts.ImageRef)
	if err != nil {
		return Verified{}, fmt.Errorf("cosign verify failed: %w", err)
	}
	var records []cosignRecord
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&records); err != nil {
		// cosign output carries optional fields; fall back to lenient decode of
		// only the fields we need.
		records = nil
		if err2 := json.Unmarshal(out, &records); err2 != nil {
			return Verified{}, fmt.Errorf("parse cosign output: %w", err2)
		}
	}
	if len(records) == 0 {
		return Verified{}, errors.New("cosign produced no signatures")
	}
	for _, r := range records {
		if r.Critical.Image.DockerManifestDigest == opts.ExpectedDigest {
			return Verified{
				ImageRef: opts.ImageRef,
				Digest:   opts.ExpectedDigest,
				Signer:   r.Critical.Identity.DockerReference,
			}, nil
		}
	}
	return Verified{}, fmt.Errorf("no signature matches expected digest %s", opts.ExpectedDigest)
}
