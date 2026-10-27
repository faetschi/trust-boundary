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
	"slices"
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
	// ClientUID is the registered client the self-test turn runs as. The service authorizes a caller by its
	// UID, and root is not a registered principal, so the self-test never runs as root.
	ClientUID      int    `json:"client_uid"`
	Prompt         string `json:"prompt,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
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
	// The installed inputs of the service's host profile. Setup records their paths in the service config
	// and does not install them: the administrator places them first, and the service verifies them at
	// start. GatePolicyPaths names a policy file under ConfigDir for every project, GitPath the private Git
	// executable under BinaryDir with its pinned GitDigest, and XattrCalibrationPath the signed calibration
	// under ConfigDir (its signature sits at the same path plus ".sig").
	GatePolicyPaths      map[string]string `json:"gate_policy_paths"`
	GitPath              string            `json:"git_path"`
	GitDigest            string            `json:"git_digest"`
	XattrCalibrationPath string            `json:"xattr_calibration_path"`
	// ServiceUID and ServiceGID pin the account IDs. Zero lets the system choose.
	ServiceUID int       `json:"service_uid,omitempty"`
	ServiceGID int       `json:"service_gid,omitempty"`
	SelfTest   *SelfTest `json:"self_test,omitempty"`
}

var (
	identity     = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)
	sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

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
	if err := c.validateHostInputs(); err != nil {
		return err
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
		if err := t.validateClient(); err != nil {
			return err
		}
	}
	return nil
}

// validateHostInputs requires the host profile's inputs, so setup never installs a service that
// refuses to start for want of them. The policy map is checked against the projects in both directions.
func (c Config) validateHostInputs() error {
	if c.GitPath == "" || c.GitDigest == "" || c.XattrCalibrationPath == "" {
		return refuse("git_path, git_digest and xattr_calibration_path are required")
	}
	if err := checkProductPath("git_path", c.GitPath, BinaryDir); err != nil {
		return err
	}
	if err := checkNotWritten("git_path", c.GitPath); err != nil {
		return err
	}
	if err := checkProductPath("xattr_calibration_path", c.XattrCalibrationPath, ConfigDir); err != nil {
		return err
	}
	for _, p := range []string{c.XattrCalibrationPath, c.XattrCalibrationPath + ".sig"} {
		if err := checkNotWritten("xattr_calibration_path", p); err != nil {
			return err
		}
	}
	if !sha256Digest.MatchString(c.GitDigest) {
		return refuse("git_digest must be a canonical SHA-256 digest")
	}
	if len(c.GatePolicyPaths) != len(c.Projects) {
		return refuse("gate_policy_paths needs exactly one policy file for every project")
	}
	for id := range c.Projects {
		file, ok := c.GatePolicyPaths[id]
		if !ok {
			return refuse("gate_policy_paths has no policy file for project %q", id)
		}
		if err := checkProductPath(fmt.Sprintf("gate_policy_paths[%q]", id), file, ConfigDir); err != nil {
			return err
		}
		if err := checkNotWritten(fmt.Sprintf("gate_policy_paths[%q]", id), file); err != nil {
			return err
		}
	}
	return nil
}

// checkNotWritten refuses a host input on a path the product writes: the input would be overwritten by a
// setup or an update, or deleted by an uninstall, and the check that pinned it would pass against the new
// content.
func checkNotWritten(field, p string) error {
	if slices.Contains(writtenPaths(), p) || slices.ContainsFunc(writtenTrees, func(tree string) bool { return PathInside(p, tree) }) {
		return refuse("%s %s collides with a path setup, update, rollback or uninstall writes", field, p)
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

// validateClient requires an explicit client UID. Zero is root, which is not a registered principal, and is
// also what an unset field decodes to.
func (t SelfTest) validateClient() error {
	if t.ClientUID <= 0 {
		return refuse("self_test.client_uid is required: the registered client UID the self-test runs as (not 0, which is root)")
	}
	return nil
}

// checkRegistered also requires the UID to be an allowed principal of the registration.
func (t SelfTest) checkRegistered(principals []int64) error {
	if err := t.validateClient(); err != nil {
		return err
	}
	if !slices.Contains(principals, int64(t.ClientUID)) {
		return refuse("self_test.client_uid %d is not an allowed principal of the registration (allowed: %v)", t.ClientUID, principals)
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
	GatePolicyPaths           map[string]string `json:"gate_policy_paths"`
	GitPath                   string            `json:"git_path"`
	GitDigest                 string            `json:"git_digest"`
	XattrCalibrationPath      string            `json:"xattr_calibration_path"`
}

func (c Config) serviceConfigJSON() ([]byte, error) {
	return indented(ServiceConfig{
		SchemaVersion: ServiceConfigSchema, RegistrationPath: RegistrationPath,
		RegistrationSignaturePath: RegistrationSignaturePath, RegistrationPublicKeyPath: RegistrationPublicKeyPath,
		CatalogPath: CatalogPath, StateRoot: TurnsDir, SocketPath: SocketPath, WorkerMode: c.WorkerMode, Projects: c.Projects,
		GatePolicyPaths: c.GatePolicyPaths, GitPath: c.GitPath, GitDigest: c.GitDigest, XattrCalibrationPath: c.XattrCalibrationPath,
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
