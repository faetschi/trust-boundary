//go:build linux

package piruntime

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

const (
	productD09ServiceHostPath     = "/sys/fs/cgroup/system.slice/tbound-product.service"
	productD09CellsHostPath       = productD09ServiceHostPath + "/cells"
	productD09CellsHierarchyPath  = "/system.slice/tbound-product.service/cells"
	productD09RunnerHierarchyPath = "/system.slice/tbound-product.service/runner"
)

// productD09CommandEnvironment is the environment of every product Podman and cosign child, built fresh on each call. It
// inherits nothing. It holds the product's own roots, the C locale that the thesis unit sets, and the shared D09 PATH.
func productD09CommandEnvironment() []string {
	return []string{
		"CONTAINERS_CONF=" + productsetup.ContainersConfPath,
		"CONTAINERS_STORAGE_CONF=" + productsetup.StorageConfPath,
		"HOME=" + productsetup.PodmanHomeDir,
		"LANG=C",
		"LC_ALL=C",
		"PATH=" + d09FixedPath,
		"TMPDIR=" + productsetup.PodmanTmpDir,
		"XDG_CACHE_HOME=" + productsetup.PodmanCacheDir,
		"XDG_RUNTIME_DIR=" + productsetup.RuntimeDir,
	}
}

// productD09ServiceConfigFile is the installed service config. It adds shutdown_grace_seconds, which the product service
// config accepts and productsetup.ServiceConfig does not model; the grace period is not a loader input.
type productD09ServiceConfigFile struct {
	productsetup.ServiceConfig
	ShutdownGraceSeconds int `json:"shutdown_grace_seconds,omitempty"`
}

// productD09DecodeServiceConfig strictly decodes the fixed product service config and requires its registration and key
// fields to name the canonical product paths.
func productD09DecodeServiceConfig(raw []byte) (productsetup.ServiceConfig, error) {
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return productsetup.ServiceConfig{}, productD09Refused(err, "service config is not strict JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file productD09ServiceConfigFile
	if err := decoder.Decode(&file); err != nil {
		return productsetup.ServiceConfig{}, productD09Refused(err, "service config does not match the product schema")
	}
	config := file.ServiceConfig
	if config.SchemaVersion != productsetup.ServiceConfigSchema || config.RegistrationPath != productsetup.RegistrationPath ||
		config.RegistrationSignaturePath != productsetup.RegistrationSignaturePath ||
		config.RegistrationPublicKeyPath != productsetup.RegistrationPublicKeyPath {
		return productsetup.ServiceConfig{}, productD09Refused(nil, "service config does not name the canonical registration and key")
	}
	return config, nil
}

// productD09RequireServiceMatchesRegistration requires the service config to name the registration's git tool and exactly
// the registration's projects.
func productD09RequireServiceMatchesRegistration(service productsetup.ServiceConfig, registration productsession.Registration) error {
	if service.GitDigest != registration.GitDigest {
		return productD09Refused(nil, "service git digest differs from the verified registration")
	}
	if len(service.Projects) != len(registration.Projects) {
		return productD09Refused(nil, "service projects differ from the verified registration")
	}
	for _, project := range registration.Projects {
		if _, ok := service.Projects[project.ProjectID]; !ok {
			return productD09Refused(nil, "service projects differ from the verified registration: "+project.ProjectID)
		}
	}
	return nil
}

// productD09ValidateServiceRoots keeps turn evidence and managed projects in the setup-owned product trees.
func productD09ValidateServiceRoots(service productsetup.ServiceConfig) error {
	if !productD09IsCleanAbsolute(service.StateRoot) || productsetup.IsThesisPath(service.StateRoot) ||
		!productsetup.PathInside(service.StateRoot, productsetup.TurnsDir) {
		return productD09Refused(nil, "service state root is not a clean path inside the product state tree")
	}
	for _, project := range service.Projects {
		if !productD09IsCleanAbsolute(project) || productsetup.IsThesisPath(project) ||
			project == productsetup.ProjectsDir || !productsetup.PathInside(project, productsetup.ProjectsDir) {
			return productD09Refused(nil, "service project root is not a clean managed product path: "+project)
		}
	}
	return nil
}

func productD09ValidateEffectiveUnit(properties map[string]string) error {
	for _, property := range productD09EffectiveUnitProperties {
		// systemctl show leaves out an empty EnvironmentFiles list; absent then means empty, checked below.
		if _, ok := properties[property]; !ok && property != "EnvironmentFiles" {
			return productD09Refused(nil, "systemctl did not report the effective property "+property)
		}
	}
	if properties["LoadState"] != "loaded" || properties["NeedDaemonReload"] != "no" ||
		properties["FragmentPath"] != productsetup.UnitPath || properties["DropInPaths"] != "" ||
		properties["EnvironmentFiles"] != "" ||
		properties["User"] != productsetup.ServiceUser || properties["Group"] != productsetup.ServiceGroup {
		return productD09Refused(nil, "effective product unit differs from the installed unit")
	}
	// systemctl separates assignments with one ASCII space; strings.Fields would also strip Unicode spaces inside values.
	environment := strings.Split(properties["Environment"], " ")
	slices.Sort(environment)
	if !slices.Equal(environment, productD09CommandEnvironment()) {
		return productD09Refused(nil, "effective product environment differs from the fixed assignments")
	}
	return nil
}
