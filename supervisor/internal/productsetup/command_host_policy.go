//go:build linux

package productsetup

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
)

// Fixed cgroup paths of the product command host. The service unit owns the service path; the launcher admits only cells
// below it.
const (
	CommandServiceCgroupHostPath       = "/sys/fs/cgroup/system.slice/tbound-product.service"
	CommandCellsCgroupHostPath         = CommandServiceCgroupHostPath + "/cells"
	CommandCellsCgroupHierarchyPath    = "/system.slice/tbound-product.service/cells"
	CommandRunnerCgroupHierarchyPath   = "/system.slice/tbound-product.service/runner"
	commandRegistrationSignatureBudget = 1 << 10
)

// The functions below return plain errors. The caller owns the refusal sentinel and wraps what they return, so each message
// reads "<detail>" or "<detail>: <cause>".

// CommandCellEnvironment is the environment of every product Podman and cosign child, built fresh on each call. It
// inherits nothing. It holds the product's own roots, the C locale that the thesis unit sets, and the shared D09 PATH.
func CommandCellEnvironment() []string {
	return []string{
		"CONTAINERS_CONF=" + ContainersConfPath,
		"CONTAINERS_STORAGE_CONF=" + StorageConfPath,
		"HOME=" + PodmanHomeDir,
		"LANG=C",
		"LC_ALL=C",
		"PATH=" + CommandCellPath,
		"TMPDIR=" + PodmanTmpDir,
		"XDG_CACHE_HOME=" + PodmanCacheDir,
		"XDG_RUNTIME_DIR=" + RuntimeDir,
	}
}

// ReadCommandRegistration reads the signed registration, its signature and the public key through read, in that order, from
// the canonical paths and with the canonical bounds. The signature and the key are standard base64 files. read is the
// caller's protected reader; its errors are returned unchanged.
func ReadCommandRegistration(read func(path string, limit int64) ([]byte, error)) (raw, signature, publicKey []byte, err error) {
	raw, err = read(RegistrationPath, int64(productsession.MaxRegistrationBytes))
	if err != nil {
		return nil, nil, nil, err
	}
	if signature, err = readCommandBase64(read, RegistrationSignaturePath); err != nil {
		return nil, nil, nil, err
	}
	if publicKey, err = readCommandBase64(read, RegistrationPublicKeyPath); err != nil {
		return nil, nil, nil, err
	}
	return raw, signature, publicKey, nil
}

func readCommandBase64(read func(string, int64) ([]byte, error), path string) ([]byte, error) {
	raw, err := read(path, commandRegistrationSignatureBudget)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("input is not standard base64: %s: %w", path, err)
	}
	return decoded, nil
}

// commandServiceConfigFile is the installed service config. It adds shutdown_grace_seconds, which the product service
// config accepts and ServiceConfig does not model; the grace period is not a loader input.
type commandServiceConfigFile struct {
	ServiceConfig
	ShutdownGraceSeconds int `json:"shutdown_grace_seconds,omitempty"`
}

// DecodeCommandServiceConfig strictly decodes the fixed product service config and requires its registration and key
// fields to name the canonical product paths.
func DecodeCommandServiceConfig(raw []byte) (ServiceConfig, error) {
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return ServiceConfig{}, fmt.Errorf("service config is not strict JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file commandServiceConfigFile
	if err := decoder.Decode(&file); err != nil {
		return ServiceConfig{}, fmt.Errorf("service config does not match the product schema: %w", err)
	}
	config := file.ServiceConfig
	if config.SchemaVersion != ServiceConfigSchema || config.RegistrationPath != RegistrationPath ||
		config.RegistrationSignaturePath != RegistrationSignaturePath ||
		config.RegistrationPublicKeyPath != RegistrationPublicKeyPath {
		return ServiceConfig{}, errors.New("service config does not name the canonical registration and key")
	}
	return config, nil
}

// RequireCommandRegistration requires the service config to name the registration's git tool and exactly the registration's
// projects.
func (c ServiceConfig) RequireCommandRegistration(registration productsession.Registration) error {
	if c.GitDigest != registration.GitDigest {
		return errors.New("service git digest differs from the verified registration")
	}
	if len(c.Projects) != len(registration.Projects) {
		return errors.New("service projects differ from the verified registration")
	}
	for _, project := range registration.Projects {
		if _, ok := c.Projects[project.ProjectID]; !ok {
			return errors.New("service projects differ from the verified registration: " + project.ProjectID)
		}
	}
	return nil
}

func commandIsCleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

// ValidateCommandRoots keeps turn evidence and managed projects in the setup-owned product trees.
func (c ServiceConfig) ValidateCommandRoots() error {
	if !commandIsCleanAbsolute(c.StateRoot) || IsThesisPath(c.StateRoot) ||
		!PathInside(c.StateRoot, TurnsDir) {
		return errors.New("service state root is not a clean path inside the product state tree")
	}
	for _, project := range c.Projects {
		if !commandIsCleanAbsolute(project) || IsThesisPath(project) ||
			project == ProjectsDir || !PathInside(project, ProjectsDir) {
			return errors.New("service project root is not a clean managed product path: " + project)
		}
	}
	return nil
}

// commandEffectiveUnitProperties are the systemd properties that decide the daemon's effective unit and environment.
var commandEffectiveUnitProperties = []string{"LoadState", "NeedDaemonReload", "FragmentPath", "DropInPaths", "Environment", "EnvironmentFiles", "User", "Group"}

// ValidateCommandEffectiveUnit judges the properties systemctl reported for the product unit.
func ValidateCommandEffectiveUnit(properties map[string]string) error {
	for _, property := range commandEffectiveUnitProperties {
		// systemctl show leaves out an empty EnvironmentFiles list; absent then means empty, checked below.
		if _, ok := properties[property]; !ok && property != "EnvironmentFiles" {
			return errors.New("systemctl did not report the effective property " + property)
		}
	}
	if properties["LoadState"] != "loaded" || properties["NeedDaemonReload"] != "no" ||
		properties["FragmentPath"] != UnitPath || properties["DropInPaths"] != "" ||
		properties["EnvironmentFiles"] != "" ||
		properties["User"] != ServiceUser || properties["Group"] != ServiceGroup {
		return errors.New("effective product unit differs from the installed unit")
	}
	// systemctl separates assignments with one ASCII space; strings.Fields would also strip Unicode spaces inside values.
	environment := strings.Split(properties["Environment"], " ")
	slices.Sort(environment)
	if !slices.Equal(environment, CommandCellEnvironment()) {
		return errors.New("effective product environment differs from the fixed assignments")
	}
	return nil
}
