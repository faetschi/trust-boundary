package productsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	// ConfigSchema is the setup configuration's schema. The installed copy at SetupConfigPath carries no
	// source paths.
	ConfigSchema = "tbound-product-setup-config/v1"
	// ServiceConfigSchema is the product service configuration's schema. cmd/tbound owns its validator,
	// and a test there decodes what Setup writes.
	ServiceConfigSchema = "tbound-product-service-config/v1"

	maxConfigBytes   = 64 << 10
	maxSourceBytes   = 1 << 20
	maxBinaryBytes   = 512 << 20
	defaultSelfTest  = 120
	maxSelfTestSecs  = 3600
	defaultSelfPromt = "TBound self-test: answer in one short sentence. Do not call any tool."
)

// ErrRefused marks a configuration or a host state setup refuses to act on.
var ErrRefused = errors.New("refused")

// ErrUnsupportedHost marks failed prerequisites. Setup fails with it before any change.
var ErrUnsupportedHost = errors.New("unsupported host")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// SelfTest names the synthetic project the explicit runtime self-test runs a turn on. The project is
// registered in the signed registration for that purpose alone.
type SelfTest struct {
	ProjectID       string `json:"project_id"`
	ProviderProfile string `json:"provider_profile"`
	Prompt          string `json:"prompt,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
}

// Config is tbound-product-setup-config/v1, written by the administrator.
type Config struct {
	SchemaVersion string `json:"schema_version"`
	// Source files of the signed registration, read from any path and installed root-owned.
	Registration          string `json:"registration,omitempty"`
	RegistrationSignature string `json:"registration_signature,omitempty"`
	RegistrationPublicKey string `json:"registration_public_key,omitempty"`
	WorkerMode            string `json:"worker_mode"`
	// Projects maps each registered project ID to its managed directory below ProjectsDir.
	Projects map[string]string `json:"projects"`
	// ServiceUID and ServiceGID pin the account IDs. Zero lets the system choose.
	ServiceUID int       `json:"service_uid,omitempty"`
	ServiceGID int       `json:"service_gid,omitempty"`
	SelfTest   *SelfTest `json:"self_test,omitempty"`
}

var identity = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)

// LoadConfig reads and strictly decodes a setup configuration file.
func LoadConfig(file string) (Config, error) {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return Config{}, refuse("the setup config must be a regular non-symlink file of at most 64 KiB")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(raw)
}

// ParseConfig strictly decodes and validates a configuration.
func ParseConfig(raw []byte) (Config, error) {
	c, err := decodeConfig(raw)
	if err != nil {
		return Config{}, err
	}
	return c, c.validate()
}

func decodeConfig(raw []byte) (Config, error) {
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return Config{}, refuse("the setup config does not match its schema: %v", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Config{}, refuse("the setup config has trailing data")
	}
	return c, nil
}

func (c Config) validate() error {
	if c.SchemaVersion != ConfigSchema {
		return refuse("schema_version must be %q", ConfigSchema)
	}
	if c.WorkerMode == "" {
		return refuse("worker_mode is required")
	}
	if c.ServiceUID < 0 || c.ServiceGID < 0 || (c.ServiceUID == 0) != (c.ServiceGID == 0) {
		return refuse("service_uid and service_gid are both unset or both positive")
	}
	if len(c.Projects) == 0 {
		return refuse("projects names no project")
	}
	dirs := map[string]string{}
	for id, dir := range c.Projects {
		if !identity.MatchString(id) {
			return refuse("project ID %q is not a bounded printable identity", id)
		}
		if err := checkProductPath(fmt.Sprintf("projects[%q]", id), dir, ProjectsDir); err != nil {
			return err
		}
		if dir == ProjectsDir {
			return refuse("projects[%q] must be a directory below %s", id, ProjectsDir)
		}
		if other, dup := dirs[dir]; dup {
			return refuse("projects %q and %q share a managed directory", id, other)
		}
		dirs[dir] = id
	}
	for _, source := range []string{c.Registration, c.RegistrationSignature, c.RegistrationPublicKey} {
		if source == "" || !path.IsAbs(source) || IsThesisPath(source) {
			return refuse("registration, registration_signature and registration_public_key are required absolute paths outside every thesis path")
		}
	}
	if t := c.SelfTest; t != nil {
		if _, ok := c.Projects[t.ProjectID]; !ok || !identity.MatchString(t.ProviderProfile) {
			return refuse("self_test needs a project_id from projects and a provider_profile")
		}
		if t.TimeoutSeconds < 0 || t.TimeoutSeconds > maxSelfTestSecs {
			return refuse("self_test.timeout_seconds must be at most %d", maxSelfTestSecs)
		}
	}
	return nil
}

// checkProductPath refuses a thesis path and any path not below root.
func checkProductPath(field, p, root string) error {
	if !path.IsAbs(p) || path.Clean(p) != p {
		return refuse("%s must be an absolute clean path", field)
	}
	if IsThesisPath(p) {
		return refuse("%s is a thesis service path", field)
	}
	if !PathInside(p, root) {
		return refuse("%s must be at or below %s", field, root)
	}
	return nil
}

// Timeout is the self-test's time limit in seconds.
func (t SelfTest) Timeout() int {
	if t.TimeoutSeconds == 0 {
		return defaultSelfTest
	}
	return t.TimeoutSeconds
}

// PromptText is the self-test prompt.
func (t SelfTest) PromptText() string {
	if t.Prompt == "" {
		return defaultSelfPromt
	}
	return t.Prompt
}

// ServiceConfig mirrors cmd/tbound's tbound-product-service-config/v1, field for field.
type ServiceConfig struct {
	SchemaVersion             string            `json:"schema_version"`
	RegistrationPath          string            `json:"registration_path"`
	RegistrationSignaturePath string            `json:"registration_signature_path"`
	RegistrationPublicKeyPath string            `json:"registration_public_key_path"`
	CatalogPath               string            `json:"catalog_path"`
	StateRoot                 string            `json:"state_root"`
	SocketPath                string            `json:"socket_path"`
	WorkerMode                string            `json:"worker_mode"`
	Projects                  map[string]string `json:"projects"`
}

func (c Config) serviceConfigJSON() ([]byte, error) {
	return indented(ServiceConfig{
		SchemaVersion: ServiceConfigSchema, RegistrationPath: RegistrationPath,
		RegistrationSignaturePath: RegistrationSignaturePath, RegistrationPublicKeyPath: RegistrationPublicKeyPath,
		CatalogPath: CatalogPath, StateRoot: TurnsDir, SocketPath: SocketPath, WorkerMode: c.WorkerMode, Projects: c.Projects,
	})
}

// installedJSON is the configuration without its source paths: the record doctor and update read.
func (c Config) installedJSON() ([]byte, error) {
	c.Registration, c.RegistrationSignature, c.RegistrationPublicKey = "", "", ""
	return indented(c)
}

func indented(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ") // encoding/json sorts map keys, so the bytes are stable
	return append(data, '\n'), err
}

func (c Config) projectIDs() []string {
	ids := make([]string, 0, len(c.Projects))
	for id := range c.Projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func joinIDs(ids []string) string { return strings.Join(ids, ", ") }
