package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Component is one pinned artifact in the managed runtime bundle.
type Component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Target  string `json:"target"`
	// Extract unpacks a pinned .tar.gz archive into Target instead of writing
	// the archive bytes as a single file.
	Extract bool `json:"extract,omitempty"`
}

// BundleManifest pins the managed runtime (Node, Pi, typebox, and locked deps).
type BundleManifest struct {
	SchemaVersion string      `json:"schema_version"`
	Components    []Component `json:"components"`
}

// BundleSchemaVersion is the current manifest schema.
const BundleSchemaVersion = "tbound-runtime-bundle/v1"

var (
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// placeholder values that must never be accepted as real pins.
	placeholderValues = map[string]bool{
		"todo": true, "placeholder": true, "change-me": true, "": true,
	}
)

// LoadBundleManifest reads and validates a manifest from disk.
func LoadBundleManifest(path string) (BundleManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BundleManifest{}, err
	}
	var m BundleManifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return BundleManifest{}, fmt.Errorf("decode bundle manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return BundleManifest{}, err
	}
	return m, nil
}

// Validate fails closed on missing/placeholder identities, so a template
// manifest cannot be used as if it were provisioned.
func (m BundleManifest) Validate() error {
	if m.SchemaVersion != BundleSchemaVersion {
		return fmt.Errorf("bundle manifest schema %q != %q", m.SchemaVersion, BundleSchemaVersion)
	}
	if len(m.Components) == 0 {
		return errors.New("bundle manifest has no components")
	}
	seen := map[string]bool{}
	for _, c := range m.Components {
		if c.Name == "" || c.Version == "" || c.Target == "" {
			return errors.New("bundle component missing name/version/target")
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate bundle component %q", c.Name)
		}
		seen[c.Name] = true
		if placeholderValues[strings.ToLower(strings.TrimSpace(c.SHA256))] {
			return fmt.Errorf("component %q has no pinned sha256", c.Name)
		}
		if !sha256Hex.MatchString(c.SHA256) {
			return fmt.Errorf("component %q sha256 is not 64 lowercase hex", c.Name)
		}
		if !strings.HasPrefix(c.URL, "https://") {
			return fmt.Errorf("component %q url must be https", c.Name)
		}
		if c.Extract && !strings.HasSuffix(c.URL, ".tar.gz") {
			return fmt.Errorf("component %q extract requires a .tar.gz archive", c.Name)
		}
		if strings.Contains(c.Target, "..") || filepath.IsAbs(c.Target) {
			return fmt.Errorf("component %q target must be a relative path without ..", c.Name)
		}
	}
	return nil
}

// ComponentStatus reports one verified/present component.
type ComponentStatus struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Present bool   `json:"present"`
	Detail  string `json:"detail,omitempty"`
}

// BundleStatus reports the managed bundle state.
type BundleStatus struct {
	Root       string            `json:"root"`
	Present    bool              `json:"present"`
	Components []ComponentStatus `json:"components"`
}

// Fetcher retrieves a pinned artefact body. It is injected so tests (and
// air-gapped installs using a local mirror) never reach the network directly.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (io.ReadCloser, error)
}

type marker struct {
	SchemaVersion string `json:"schema_version"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	SHA256        string `json:"sha256"`
}

func markerPath(root, name string) string {
	return filepath.Join(root, ".tbound-pins", name+".json")
}

// VerifyBundle checks each component marker under root against the manifest.
// It never inspects or trusts a global Node/Pi installation.
func VerifyBundle(root string, m BundleManifest) (BundleStatus, error) {
	if err := m.Validate(); err != nil {
		return BundleStatus{}, err
	}
	status := BundleStatus{Root: root, Present: true}
	for _, c := range m.Components {
		cs := ComponentStatus{Name: c.Name, Version: c.Version}
		data, err := os.ReadFile(markerPath(root, c.Name))
		if err != nil {
			cs.Detail = "not installed"
			status.Present = false
			status.Components = append(status.Components, cs)
			continue
		}
		var mk marker
		if err := json.Unmarshal(data, &mk); err != nil {
			cs.Detail = "corrupt marker"
			status.Present = false
			status.Components = append(status.Components, cs)
			continue
		}
		if mk.Name != c.Name || mk.Version != c.Version || mk.SHA256 != c.SHA256 {
			cs.Detail = "pinned identity mismatch"
			status.Present = false
			status.Components = append(status.Components, cs)
			continue
		}
		if _, err := os.Stat(filepath.Join(root, c.Target)); err != nil {
			cs.Detail = "target missing"
			status.Present = false
			status.Components = append(status.Components, cs)
			continue
		}
		cs.Present = true
		cs.Detail = "pinned"
		status.Components = append(status.Components, cs)
	}
	return status, nil
}

// ApplyBundle installs the pinned components under root using the injected
// fetcher, verifying each sha256 before writing and recording a marker. It
// refuses to overwrite a destination with a different pinned identity.
func ApplyBundle(ctx context.Context, root string, m BundleManifest, fetch Fetcher) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if fetch == nil {
		return errors.New("bundle install requires a fetcher")
	}
	if err := os.MkdirAll(filepath.Join(root, ".tbound-pins"), 0o755); err != nil {
		return err
	}
	for _, c := range m.Components {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(root, c.Target)
		if existing, err := os.ReadFile(markerPath(root, c.Name)); err == nil {
			var mk marker
			if json.Unmarshal(existing, &mk) == nil && mk.SHA256 == c.SHA256 && mk.Version == c.Version {
				continue // already installed with the pinned identity
			}
			return fmt.Errorf("component %q already present with a different pinned identity", c.Name)
		}
		body, err := fetch.Fetch(ctx, c.URL)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", c.Name, err)
		}
		data, err := io.ReadAll(io.LimitReader(body, 1<<30))
		closeErr := body.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", c.Name, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", c.Name, closeErr)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != c.SHA256 {
			return fmt.Errorf("component %q sha256 mismatch", c.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if c.Extract {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			if err := extractTarGz(data, target); err != nil {
				return fmt.Errorf("extract %s: %w", c.Name, err)
			}
		} else if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		mk := marker{SchemaVersion: BundleSchemaVersion, Name: c.Name, Version: c.Version, SHA256: c.SHA256}
		enc, _ := json.Marshal(mk)
		if err := os.WriteFile(markerPath(root, c.Name), enc, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// extractTarGz unpacks a gzip-compressed tar archive into dest. It refuses
// absolute paths, parent traversal, and symlink/hardlink entries so a pinned
// archive cannot write outside its target.
func extractTarGz(data []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := path.Clean("/" + hdr.Name)
		rel := strings.TrimPrefix(clean, "/")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("unsafe archive path %q", hdr.Name)
		}
		out := filepath.Join(dest, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("refuse link entry %q in pinned archive", hdr.Name)
		}
	}
}
