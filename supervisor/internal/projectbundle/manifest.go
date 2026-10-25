// Package projectbundle builds, validates, imports, exports and applies TBound project bundles.
//
// A bundle ("tbound-project-bundle/v1") carries a project's regular files and directories from the
// user's own client to the service, which never reads the original repository. An export
// ("tbound-project-export/v1") carries the service's changes back to a clean Git worktree.
//
// Both are one byte stream: the format name and a newline, a 4-byte big-endian header length, a
// canonical JSON header, then the raw content of each file in header order. The JSON is compact,
// uses the field order of the Go types below and no HTML escaping. A reader decodes it with unknown
// fields refused, re-encodes it, and refuses any byte difference, so there is exactly one accepted
// encoding of each header.
package projectbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	BundleFormat = "tbound-project-bundle/v1"
	ExportFormat = "tbound-project-export/v1"

	// MaxPathBytes mirrors delta.MaxPathBytes, the path bound of every other TBound manifest.
	MaxPathBytes = 1024
	// MaxHeaderBytes bounds the JSON header a reader will buffer.
	MaxHeaderBytes = 32 << 20

	// fenceDir is the effect oracle's directory of the managed live root. It is not project content.
	fenceDir = "lost+found"
	gitDir   = ".git"
)

// ErrInvalidBundle marks a stream or header that violates the format, a limit or a path rule.
var ErrInvalidBundle = errors.New("invalid project bundle")

type EntryType string

const (
	TypeFile EntryType = "file"
	TypeDir  EntryType = "dir"
)

// Entry describes one path. Directories carry Executable=false, Size=0 and an empty SHA256.
type Entry struct {
	Path       string    `json:"path"`
	Type       EntryType `json:"type"`
	Executable bool      `json:"executable"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
}

// Manifest lists every entry sorted by byte order of its path. Every parent directory is listed.
type Manifest struct {
	Format  string  `json:"format"`
	Entries []Entry `json:"entries"`
}

// Limits bound a bundle. Every field must be positive.
type Limits struct {
	MaxEntries    int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

func DefaultLimits() Limits {
	return Limits{MaxEntries: 20000, MaxFileBytes: 64 << 20, MaxTotalBytes: 512 << 20}
}

func (l Limits) validate() error {
	if l.MaxEntries <= 0 || l.MaxFileBytes <= 0 || l.MaxTotalBytes <= 0 {
		return errors.New("projectbundle: limits must be positive")
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidBundle, fmt.Sprintf(format, args...))
}

// validatePath mirrors the component rules of sessionrepo's validateRelativePath.
func validatePath(value string) error {
	switch {
	case value == "":
		return errors.New("path is empty")
	case len(value) > MaxPathBytes:
		return fmt.Errorf("path is longer than %d bytes", MaxPathBytes)
	case !utf8.ValidString(value):
		return errors.New("path is not valid UTF-8")
	case strings.HasPrefix(value, "/"):
		return errors.New("path is absolute")
	case strings.ContainsRune(value, '\\'):
		return errors.New("path contains a backslash")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("path has an empty, . or .. component")
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return errors.New("path contains a control character")
			}
		}
	}
	return nil
}

// validateManifestPath is validatePath plus the two names that are never project content.
func validateManifestPath(value string) error {
	if err := validatePath(value); err != nil {
		return err
	}
	if value == fenceDir || strings.HasPrefix(value, fenceDir+"/") {
		return errors.New("names the managed root fence directory")
	}
	if isGitPath(value) {
		return errors.New("names Git administration")
	}
	return nil
}

func isGitPath(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		if segment == gitDir {
			return true
		}
	}
	return false
}

func validHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validHex(strings.TrimPrefix(value, "sha256:"))
}

// Validate checks the manifest against the format rules and limits, without touching any file.
func (m Manifest) Validate(l Limits) error {
	if err := l.validate(); err != nil {
		return err
	}
	if m.Format != BundleFormat {
		return invalid("manifest format %q is not %q", m.Format, BundleFormat)
	}
	if len(m.Entries) > l.MaxEntries {
		return invalid("%d entries exceed the limit of %d", len(m.Entries), l.MaxEntries)
	}
	kinds := make(map[string]EntryType, len(m.Entries))
	var total int64
	for i, e := range m.Entries {
		if err := validateManifestPath(e.Path); err != nil {
			return invalid("entry %d path %q: %v", i, e.Path, err)
		}
		if i > 0 && e.Path <= m.Entries[i-1].Path {
			return invalid("entry %q is out of order or duplicated", e.Path)
		}
		if slash := strings.LastIndexByte(e.Path, '/'); slash >= 0 {
			switch kinds[e.Path[:slash]] {
			case TypeDir:
			case TypeFile:
				return invalid("%q lies under a file", e.Path)
			default:
				return invalid("%q has no listed parent directory", e.Path)
			}
		}
		switch e.Type {
		case TypeDir:
			if e.Executable || e.Size != 0 || e.SHA256 != "" {
				return invalid("directory %q carries file fields", e.Path)
			}
		case TypeFile:
			if e.Size < 0 || e.Size > l.MaxFileBytes {
				return invalid("file %q size %d exceeds the limit of %d", e.Path, e.Size, l.MaxFileBytes)
			}
			if total += e.Size; total > l.MaxTotalBytes {
				return invalid("total size exceeds the limit of %d at %q", l.MaxTotalBytes, e.Path)
			}
			if !validHex(e.SHA256) {
				return invalid("file %q has a malformed sha256", e.Path)
			}
		default:
			return invalid("entry %q has unknown type %q", e.Path, e.Type)
		}
		kinds[e.Path] = e.Type
	}
	return nil
}

func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// decodeStrict fills v from data and refuses unknown fields and any encoding other than canonicalJSON.
func decodeStrict(data []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return invalid("header: %v", err)
	}
	again, err := canonicalJSON(v)
	if err != nil || !bytes.Equal(again, data) {
		return invalid("header is not in canonical form")
	}
	return nil
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// MarshalCanonical is the manifest encoding that Digest hashes and a bundle carries.
func (m Manifest) MarshalCanonical() ([]byte, error) {
	if m.Entries == nil {
		m.Entries = []Entry{}
	}
	return canonicalJSON(m)
}

// Digest is "sha256:" plus the hex SHA-256 of MarshalCanonical.
func (m Manifest) Digest() (string, error) {
	data, err := m.MarshalCanonical()
	if err != nil {
		return "", err
	}
	return digestOf(data), nil
}

// ParseManifest strictly decodes and validates a canonical manifest.
func ParseManifest(data []byte, l Limits) (Manifest, error) {
	var m Manifest
	if err := decodeStrict(data, &m); err != nil {
		return Manifest{}, err
	}
	return m, m.Validate(l)
}

func writeFramed(w io.Writer, format string, header []byte) error {
	if len(header) > MaxHeaderBytes {
		return fmt.Errorf("projectbundle: header of %d bytes exceeds %d", len(header), MaxHeaderBytes)
	}
	prefix := make([]byte, 0, len(format)+5)
	prefix = append(prefix, format...)
	prefix = append(prefix, '\n')
	prefix = binary.BigEndian.AppendUint32(prefix, uint32(len(header)))
	if _, err := w.Write(prefix); err != nil {
		return err
	}
	_, err := w.Write(header)
	return err
}

func readFramed(r io.Reader, format string) ([]byte, error) {
	prefix := make([]byte, len(format)+5)
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, truncated(err)
	}
	if string(prefix[:len(format)+1]) != format+"\n" {
		return nil, invalid("stream does not start with %q", format)
	}
	n := binary.BigEndian.Uint32(prefix[len(format)+1:])
	if n == 0 || n > MaxHeaderBytes {
		return nil, invalid("header length %d is outside 1..%d", n, MaxHeaderBytes)
	}
	header, err := io.ReadAll(io.LimitReader(r, int64(n)))
	if err != nil {
		return nil, err
	}
	if len(header) != int(n) {
		return nil, invalid("truncated stream: header ends after %d of %d bytes", len(header), n)
	}
	return header, nil
}

func truncated(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return invalid("truncated stream")
	}
	return err
}

// requireEOF refuses bytes after the last declared content.
func requireEOF(r io.Reader) error {
	var one [1]byte
	n, err := r.Read(one[:])
	if n > 0 {
		return invalid("unexpected data after the last declared content")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
