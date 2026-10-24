//go:build linux

package productsession

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/sessionrepo"
)

const (
	// RegistrationSchema is the schema and signature domain of a registration.
	RegistrationSchema = "tbound-product-registration/v1"
	// MaxRegistrationBytes bounds the registration file.
	MaxRegistrationBytes = 1 << 20

	PublicationReview = "review"
	PublicationAuto   = "auto"

	maxIdentityBytes   = 128
	maxBudgetField     = 1 << 40 // keeps Budget sums far from int64 overflow
	maxKeyFileBytes    = 1024
	maxReviewTimeout   = 86400
	maxTurnsPerSession = 10000
)

// ErrRegistration marks a refused product registration.
var ErrRegistration = errors.New("invalid product registration")

// Principal is the authenticated peer UID. Requests never carry it.
type Principal int64

// Budget is a bundle of the three metered resources.
type Budget struct {
	HostCalls       int64 `json:"host_calls"`
	Tokens          int64 `json:"tokens"`
	DurationSeconds int64 `json:"duration_seconds"`
}

// Project is one registered managed project.
type Project struct {
	ProjectID          string   `json:"project_id"`
	RootIdentityDigest string   `json:"root_identity_digest"`
	PolicyDigest       string   `json:"policy_digest"`
	BashOutputPaths    []string `json:"bash_output_paths"`
	PublicationModes   []string `json:"publication_modes"`
}

// Limits are the registration-wide ceilings.
type Limits struct {
	Turn                 Budget `json:"turn"`
	Session              Budget `json:"session"`
	MaxTurnsPerSession   int    `json:"max_turns_per_session"`
	ReviewTimeoutSeconds int    `json:"review_timeout_seconds"`
}

// Registration is the signed product registration document.
type Registration struct {
	SchemaVersion     string      `json:"schema_version"`
	RegistrationID    string      `json:"registration_id"`
	ServiceIdentity   string      `json:"service_identity"`
	AllowedPrincipals []Principal `json:"allowed_principals"`
	Projects          []Project   `json:"projects"`
	ProviderProfiles  []string    `json:"provider_profiles"`
	Limits            Limits      `json:"limits"`
	RuntimeDigest     string      `json:"runtime_digest"`
	ImageDigest       string      `json:"image_digest"`
	ExtensionDigest   string      `json:"extension_digest"`
	Digest            string      `json:"digest"`
}

func (r Registration) project(id string) (Project, bool) {
	for _, p := range r.Projects {
		if p.ProjectID == id {
			return p, true
		}
	}
	return Project{}, false
}

// RegistrationSigningBytes returns the exact signed bytes: the schema domain, a
// newline, and the JSON encoding of r with Digest empty.
func RegistrationSigningBytes(r Registration) ([]byte, error) {
	r.Digest = ""
	encoded, err := json.Marshal(r)
	if err != nil {
		return nil, regErr("encode")
	}
	return append([]byte(RegistrationSchema+"\n"), encoded...), nil
}

// SignRegistration validates r, sets its digest, and signs it. It is a setup
// and test helper; the private key never leaves the call.
func SignRegistration(r Registration, key ed25519.PrivateKey) (data, signature []byte, err error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, regErr("Ed25519 private key has the wrong size")
	}
	if err := r.validateFields(); err != nil {
		return nil, nil, err
	}
	signing, err := RegistrationSigningBytes(r)
	if err != nil {
		return nil, nil, err
	}
	r.Digest = sha256Digest(signing)
	data, err = json.Marshal(r)
	if err != nil {
		return nil, nil, regErr("encode signed registration")
	}
	return data, ed25519.Sign(key, signing), nil
}

// VerifyRegistration strictly decodes data and checks its digest, its
// signature, and every field.
func VerifyRegistration(data, signature []byte, key ed25519.PublicKey) (Registration, error) {
	var r Registration
	if len(data) == 0 || len(data) > MaxRegistrationBytes || len(signature) != ed25519.SignatureSize || len(key) != ed25519.PublicKeySize {
		return r, regErr("registration, signature, or key has an invalid size")
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return r, regErr("not strict JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return Registration{}, regErr("does not match the schema")
	}
	signing, err := RegistrationSigningBytes(r)
	if err != nil {
		return Registration{}, err
	}
	if r.Digest != sha256Digest(signing) || !ed25519.Verify(key, signing, signature) {
		return Registration{}, regErr("digest or signature mismatch")
	}
	if err := r.validateFields(); err != nil {
		return Registration{}, err
	}
	return r, nil
}

// LoadRegistration reads the registration, its base64 signature, and the
// base64 Ed25519 public key from regular non-symlink files and verifies them.
func LoadRegistration(registrationPath, signaturePath, publicKeyPath string) (Registration, error) {
	data, err := readRegularFile(registrationPath, MaxRegistrationBytes)
	if err != nil {
		return Registration{}, err
	}
	signature, err := readBase64File(signaturePath)
	if err != nil {
		return Registration{}, err
	}
	key, err := readBase64File(publicKeyPath)
	if err != nil {
		return Registration{}, err
	}
	return VerifyRegistration(data, signature, ed25519.PublicKey(key))
}

func readBase64File(path string) ([]byte, error) {
	raw, err := readRegularFile(path, maxKeyFileBytes)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(string(raw), "\n"))
	if err != nil {
		return nil, regErr("key or signature file is not standard base64")
	}
	return decoded, nil
}

func readRegularFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, regErr("input must be a regular non-symlink file within its size bound")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, regErr("open input file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, regErr("input file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, regErr("read input file")
	}
	return data, nil
}

func regErr(detail string) error { return fmt.Errorf("%w: %s", ErrRegistration, detail) }

// validate is validateFields plus a check that Digest matches the signed bytes.
func (r Registration) validate() error {
	if err := r.validateFields(); err != nil {
		return err
	}
	signing, err := RegistrationSigningBytes(r)
	if err != nil {
		return err
	}
	if r.Digest != sha256Digest(signing) {
		return regErr("digest mismatch")
	}
	return nil
}

func (r Registration) validateFields() error {
	if r.SchemaVersion != RegistrationSchema {
		return regErr("wrong schema_version")
	}
	if !validIdentity(r.RegistrationID) || !validIdentity(r.ServiceIdentity) {
		return regErr("registration_id and service_identity must be bounded printable identities")
	}
	if len(r.AllowedPrincipals) == 0 {
		return regErr("allowed_principals is empty")
	}
	for i, p := range r.AllowedPrincipals {
		if p < 0 || slices.Contains(r.AllowedPrincipals[:i], p) {
			return regErr("allowed_principals must be unique UIDs >= 0")
		}
	}
	if len(r.Projects) == 0 {
		return regErr("no projects")
	}
	seen := map[string]bool{}
	for _, p := range r.Projects {
		if !validIdentity(p.ProjectID) || seen[p.ProjectID] {
			return regErr("project_id must be a unique bounded identity")
		}
		seen[p.ProjectID] = true
		if !validDigest(p.RootIdentityDigest) || !validDigest(p.PolicyDigest) {
			return regErr("project digests must be sha256: plus 64 lowercase hex")
		}
		if err := sessionrepo.ValidateOutputScopePaths(p.BashOutputPaths); err != nil {
			return regErr("bash_output_paths is not a canonical exact scope")
		}
		if len(p.PublicationModes) == 0 {
			return regErr("publication_modes is empty")
		}
		for i, m := range p.PublicationModes {
			if !validMode(m) || slices.Contains(p.PublicationModes[:i], m) {
				return regErr("publication_modes must be unique values of review, auto")
			}
		}
	}
	if len(r.ProviderProfiles) == 0 {
		return regErr("provider_profiles is empty")
	}
	for i, p := range r.ProviderProfiles {
		if !validIdentity(p) || slices.Contains(r.ProviderProfiles[:i], p) {
			return regErr("provider_profiles must be unique bounded identities")
		}
	}
	l := r.Limits
	if !l.Turn.valid() || !l.Session.valid() || !l.Turn.fits(l.Session) {
		return regErr("limits must be positive and the session limit must cover the turn limit")
	}
	if l.MaxTurnsPerSession < 1 || l.MaxTurnsPerSession > maxTurnsPerSession {
		return regErr("max_turns_per_session out of range")
	}
	if l.ReviewTimeoutSeconds < 1 || l.ReviewTimeoutSeconds > maxReviewTimeout {
		return regErr("review_timeout_seconds out of range")
	}
	if !validDigest(r.RuntimeDigest) || !validDigest(r.ImageDigest) || !validDigest(r.ExtensionDigest) {
		return regErr("runtime, image, and extension digests must be sha256: plus 64 lowercase hex")
	}
	return nil
}

func validMode(m string) bool { return m == PublicationReview || m == PublicationAuto }

// validIdentity accepts 1 to 128 bytes of printable ASCII without spaces.
func validIdentity(s string) bool {
	if s == "" || len(s) > maxIdentityBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func validDigest(s string) bool {
	hexPart, ok := strings.CutPrefix(s, "sha256:")
	return ok && len(hexPart) == 64 && isLowerHex(hexPart)
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ReceiptDigest is "sha256:" plus the lowercase hex SHA-256 of the receipt bytes.
func ReceiptDigest(b []byte) string { return sha256Digest(b) }

func (b Budget) valid() bool {
	return b.HostCalls >= 1 && b.Tokens >= 1 && b.DurationSeconds >= 1 &&
		b.HostCalls <= maxBudgetField && b.Tokens <= maxBudgetField && b.DurationSeconds <= maxBudgetField
}

func (b Budget) nonNegative() bool {
	return b.HostCalls >= 0 && b.Tokens >= 0 && b.DurationSeconds >= 0
}

// fits reports whether every field of b is at most the matching field of limit.
func (b Budget) fits(limit Budget) bool {
	return b.HostCalls <= limit.HostCalls && b.Tokens <= limit.Tokens && b.DurationSeconds <= limit.DurationSeconds
}

func (b Budget) plus(o Budget) Budget {
	return Budget{b.HostCalls + o.HostCalls, b.Tokens + o.Tokens, b.DurationSeconds + o.DurationSeconds}
}

func (b Budget) minus(o Budget) Budget {
	return Budget{b.HostCalls - o.HostCalls, b.Tokens - o.Tokens, b.DurationSeconds - o.DurationSeconds}
}

func minBudget(a, b Budget) Budget {
	return Budget{min(a.HostCalls, b.HostCalls), min(a.Tokens, b.Tokens), min(a.DurationSeconds, b.DurationSeconds)}
}
