// Package releasetrust signs and verifies the release manifest, tbound-release-manifest/v1. The
// manifest lists each release artifact with its size and SHA-256, and an Ed25519 signature covers
// its canonical bytes. A checksum file that arrives with the artifacts is never authentication:
// only a signature that verifies under a publisher key obtained separately is.
//
// The package has no platform-specific code, so a Windows or macOS user can verify a download.
package releasetrust

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

const (
	// Schema is the manifest schema and the signature domain.
	Schema = "tbound-release-manifest/v1"

	maxManifestBytes = 1 << 20
	maxKeyFileBytes  = 1024
	maxArtifacts     = 1024
)

// ErrRefused marks every verification refusal.
var ErrRefused = errors.New("release verification refused")

var artifactName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// Artifact is one released file. Name is a bare file name and SHA256 is 64 lowercase hex characters.
type Artifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is the signed release manifest.
type Manifest struct {
	SchemaVersion string     `json:"schema_version"`
	Artifacts     []Artifact `json:"artifacts"`
}

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

func (m Manifest) validate() error {
	if m.SchemaVersion != Schema {
		return refuse("schema_version must be %q", Schema)
	}
	if len(m.Artifacts) == 0 || len(m.Artifacts) > maxArtifacts {
		return refuse("a manifest lists between 1 and %d artifacts", maxArtifacts)
	}
	for i, a := range m.Artifacts {
		if !artifactName.MatchString(a.Name) {
			return refuse("artifact name %q is not a bare file name", a.Name)
		}
		if i > 0 && m.Artifacts[i-1].Name >= a.Name {
			return refuse("artifacts must be sorted by name without duplicates")
		}
		if a.Size < 0 || len(a.SHA256) != 64 || strings.Trim(a.SHA256, "0123456789abcdef") != "" {
			return refuse("artifact %q needs a size and a lowercase hex SHA-256", a.Name)
		}
	}
	return nil
}

// Canonical returns the manifest's canonical bytes: the compact JSON encoding with the artifacts sorted
// by name. The bytes are the manifest file's content, without a trailing newline.
func Canonical(m Manifest) ([]byte, error) {
	m.Artifacts = slices.SortedFunc(slices.Values(m.Artifacts), func(a, b Artifact) int { return strings.Compare(a.Name, b.Name) })
	if err := m.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// signingBytes is the signed message: the schema domain, a newline, and the canonical manifest.
func signingBytes(canonical []byte) []byte { return append([]byte(Schema+"\n"), canonical...) }

// Parse strictly decodes a manifest file. The file must be exactly the canonical encoding (one
// trailing newline is allowed), so there is one byte string per manifest and a signature cannot be
// replayed over a differently spelled copy.
func Parse(data []byte) (Manifest, []byte, error) {
	var m Manifest
	if len(data) == 0 || len(data) > maxManifestBytes {
		return m, nil, refuse("manifest size is out of bounds")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return Manifest{}, nil, refuse("manifest does not match its schema")
	}
	canonical, err := Canonical(m)
	if err != nil {
		return Manifest{}, nil, err
	}
	if !bytes.Equal(canonical, bytes.TrimSuffix(data, []byte("\n"))) {
		return Manifest{}, nil, refuse("manifest is not in canonical form")
	}
	return m, canonical, nil
}

// BuildManifest hashes the files and returns the unsigned canonical manifest. Names are the files'
// base names.
func BuildManifest(files []string) ([]byte, error) {
	m := Manifest{SchemaVersion: Schema}
	for _, file := range files {
		size, sum, err := hashFile(file)
		if err != nil {
			return nil, err
		}
		m.Artifacts = append(m.Artifacts, Artifact{Name: filepath.Base(file), Size: size, SHA256: sum})
	}
	return Canonical(m)
}

// Sign signs a canonical manifest file's bytes.
func Sign(manifest []byte, key ed25519.PrivateKey) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, refuse("Ed25519 private key has the wrong size")
	}
	_, canonical, err := Parse(manifest)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(key, signingBytes(canonical)), nil
}

// Verify checks the signature of a manifest file under key and returns the parsed manifest.
func Verify(manifest, signature []byte, key ed25519.PublicKey) (Manifest, error) {
	if len(key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return Manifest{}, refuse("public key or signature has the wrong size")
	}
	m, canonical, err := Parse(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if !ed25519.Verify(key, signingBytes(canonical), signature) {
		return Manifest{}, refuse("signature does not verify under this public key")
	}
	return m, nil
}

// VerifyArtifacts checks each file against the manifest by base name, size and SHA-256. A file the
// manifest does not list is refused.
func VerifyArtifacts(m Manifest, files []string) error {
	for _, file := range files {
		name := filepath.Base(file)
		i := slices.IndexFunc(m.Artifacts, func(a Artifact) bool { return a.Name == name })
		if i < 0 {
			return refuse("%s is not listed in the signed manifest", name)
		}
		size, sum, err := hashFile(file)
		if err != nil {
			return err
		}
		if want := m.Artifacts[i]; size != want.Size || sum != want.SHA256 {
			return refuse("%s does not match the signed manifest (size or SHA-256 differs)", name)
		}
	}
	return nil
}

// Fingerprint is the first 16 hex characters of the SHA-256 of the raw public key. Users compare it
// with the value distributed separately from the artifacts.
func Fingerprint(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])[:16]
}

func hashFile(file string) (int64, string, error) {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return 0, "", refuse("%s is not a regular file", filepath.Base(file))
	}
	f, err := os.Open(file)
	if err != nil {
		return 0, "", refuse("cannot open %s", filepath.Base(file))
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", refuse("cannot read %s", filepath.Base(file))
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// ReadSignature reads a base64 signature file.
func ReadSignature(file string) ([]byte, error) { return readBase64(file) }

// ReadPublicKey reads a base64 Ed25519 public key file, the format productsession registrations use.
func ReadPublicKey(file string) (ed25519.PublicKey, error) {
	raw, err := readBase64(file)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, refuse("%s is not a base64 Ed25519 public key", filepath.Base(file))
	}
	return raw, nil
}

// ReadPrivateKey reads a base64 Ed25519 private key file. Outside Windows it refuses a key that
// group or others can read.
func ReadPrivateKey(file string) (ed25519.PrivateKey, error) {
	if info, err := os.Stat(file); err == nil && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, refuse("private key %s must not be readable by group or others (chmod 600)", filepath.Base(file))
	}
	raw, err := readBase64(file)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, refuse("%s is not a base64 Ed25519 private key", filepath.Base(file))
	}
	return raw, nil
}

func readBase64(file string) ([]byte, error) {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxKeyFileBytes {
		return nil, refuse("%s must be a regular file of at most %d bytes", filepath.Base(file), maxKeyFileBytes)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, refuse("cannot read %s", filepath.Base(file))
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(string(raw), "\n"))
	if err != nil {
		return nil, refuse("%s is not standard base64", filepath.Base(file))
	}
	return decoded, nil
}

// WriteNew writes data to a file that must not exist yet, as base64 when asBase64 is set.
func WriteNew(file string, data []byte, mode os.FileMode, asBase64 bool) error {
	if asBase64 {
		data = []byte(base64.StdEncoding.EncodeToString(data) + "\n")
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("refusing to write %s: %w", file, err)
	}
	if err = f.Chmod(mode); err == nil { // the umask must not loosen or tighten the documented mode
		if _, err = f.Write(data); err == nil {
			err = f.Sync()
		}
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(file)
		return fmt.Errorf("write %s: %w", file, err)
	}
	return nil
}

// WriteKeyPair creates <dir>/<name>.key (mode 0600) and <dir>/<name>.pub (0644) with a fresh Ed25519
// key pair. It refuses to touch an existing file, so it never replaces a key.
func WriteKeyPair(dir, name string) (ed25519.PublicKey, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	private, public := filepath.Join(dir, name+".key"), filepath.Join(dir, name+".pub")
	for _, f := range []string{private, public} {
		if _, err := os.Lstat(f); err == nil {
			return nil, fmt.Errorf("refusing to overwrite %s", f)
		}
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := WriteNew(private, key, 0o600, true); err != nil {
		return nil, err
	}
	if err := WriteNew(public, pub, 0o644, true); err != nil {
		_ = os.Remove(private)
		return nil, err
	}
	return pub, nil
}
