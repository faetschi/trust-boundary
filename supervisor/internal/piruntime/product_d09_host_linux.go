//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

// The product D09 command host is the only fixed-path input to the product Bash cell. It is one chain: a registration
// signed by the fixed product key, a host profile signed by that key and bound to the measured product template, and a
// template bound back to the registration and to the scanner account. Loading grants no launch authority by itself.

// ErrProductD09HostRefused reports missing, unprotected, mis-signed, or unbound product command-host inputs. Every
// refusal of the product loader and its helpers wraps it.
var ErrProductD09HostRefused = errors.New("product D09 command host is refused")

const (
	productD09MaxSignatureFileBytes = 1 << 10
	productD09MaxConfigBytes        = 64 << 10
	productD09MaxIdentityBytes      = 1 << 20
	productD09MaxArtifactBytes      = 1 << 20
	productD09SystemctlPath         = "/usr/bin/systemctl"
)

// The product Podman configs are pinned byte for byte. Each names the product's own graph and run roots, so a root-protected
// file cannot point the cell at the thesis or at Podman's default store.
const productD09ContainersConf = `[engine]
cgroup_manager = "cgroupfs"
events_logger = "file"
runtime = "crun"
`

const productD09StorageConf = `[storage]
driver = "overlay"
graphroot = "/var/lib/tbound-product/podman-storage/graphroot"
rootless_storage_path = "/var/lib/tbound-product/podman-storage/graphroot"
runroot = "/run/tbound-product/containers"

[storage.options]
ignore_chown_errors = "true"
`

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

// productD09EffectiveUnitProperties are the systemd properties that decide the daemon's effective unit and environment.
var productD09EffectiveUnitProperties = []string{"LoadState", "NeedDaemonReload", "FragmentPath", "DropInPaths", "Environment", "EnvironmentFiles", "User", "Group"}

// productD09UnitOverrideRoots are the systemd directories whose drop-ins apply to the product unit or to every service.
var productD09UnitOverrideRoots = []string{
	"/etc/systemd/system",
	"/run/systemd/system",
	"/usr/local/lib/systemd/system",
	"/usr/lib/systemd/system",
	"/lib/systemd/system",
	"/etc/systemd/system.control",
	"/run/systemd/system.control",
	"/run/systemd/transient",
}

// productD09UnitShadowRoots are the directories where a unit file of the same name would replace the product unit.
var productD09UnitShadowRoots = []string{
	"/run/systemd/system",
	"/run/systemd/system.control",
	"/run/systemd/transient",
	"/run/systemd/generator",
	"/run/systemd/generator.early",
	"/run/systemd/generator.late",
}

// D09ProductCommandHost is the verified product command-host input set. Its state is private, and only
// LoadProductD09CommandHost makes a non-zero one. Every method refuses or returns zero values for a zero or nil host.
type D09ProductCommandHost struct {
	snapshot productD09Snapshot
	loaded   bool
}

// productD09Snapshot is everything one load read, pinned, and measured. Revalidate loads the same fixed inputs again and
// compares the result with this value, so a changed file, identity, root, or effective unit fails closed.
type productD09Snapshot struct {
	profile        HostProfile
	template       D09CellTemplate
	serviceUID     int
	serviceGID     int
	inputs         productD09Inputs
	roots          map[string]productD09DirIdentity
	rootScanners   map[string]D09XattrVisibilityMeasurements
	unitProperties map[string]string
}

// productD09FileIdentity is the descriptor metadata of one protected file, plus the digest of the bytes it returned. A
// same-bytes replacement changes the inode or the change time, so it fails the snapshot comparison too.
type productD09FileIdentity struct {
	Device    uint64
	Inode     uint64
	Size      int64
	Mode      uint32
	UID       uint32
	GID       uint32
	MTimeSec  int64
	MTimeNsec int64
	CTimeSec  int64
	CTimeNsec int64
	Digest    string
}

// productD09DirIdentity identifies a measured directory by its descriptor, mode, and owner. Times are left out because a
// live state directory's mtime changes with every turn.
type productD09DirIdentity struct {
	Device uint64
	Inode  uint64
	Mode   uint32
	UID    uint32
	GID    uint32
}

// productD09Inputs records the identity of every protected input one load read or pinned, keyed by path.
type productD09Inputs map[string]productD09FileIdentity

// productD09ServiceConfigFile is the installed service config. It adds shutdown_grace_seconds, which the product service
// config accepts and productsetup.ServiceConfig does not model; the grace period is not a loader input.
type productD09ServiceConfigFile struct {
	productsetup.ServiceConfig
	ShutdownGraceSeconds int `json:"shutdown_grace_seconds,omitempty"`
}

// LoadProductD09CommandHost verifies the installed product command-host inputs at their fixed paths. It takes no path
// argument: the trust roots are the product constants in productsetup.
func LoadProductD09CommandHost(ctx context.Context) (*D09ProductCommandHost, error) {
	snapshot, err := loadProductD09Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &D09ProductCommandHost{snapshot: snapshot, loaded: true}, nil
}

// Profile returns the verified product host profile, or the zero profile for a zero or nil host.
func (h *D09ProductCommandHost) Profile() HostProfile {
	if !h.verified() {
		return HostProfile{}
	}
	return h.snapshot.profile
}

// Template returns the verified product cell template, or the zero template for a zero or nil host.
func (h *D09ProductCommandHost) Template() D09CellTemplate {
	if !h.verified() {
		return D09CellTemplate{}
	}
	return h.snapshot.template
}

// Revalidate reloads every fixed input, identity, root, and effective unit property, and refuses unless they equal the
// snapshot taken at load.
func (h *D09ProductCommandHost) Revalidate(ctx context.Context) error {
	if !h.verified() || ctx == nil {
		return productD09Refused(nil, "product command host was not loaded")
	}
	fresh, err := loadProductD09Snapshot(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(fresh, h.snapshot) {
		return productD09Refused(nil, "product command-host inputs changed since they were loaded")
	}
	return nil
}

func (h *D09ProductCommandHost) verified() bool {
	return h != nil && h.loaded
}

// validateProductD09Artifacts verifies the signed registration, the signed host profile, and the product template as one
// chain, against the service account IDs the loader derived. It reads no file and proves no launch authority.
func validateProductD09Artifacts(profileRaw, profileSig, registrationRaw, registrationSig, publicKey []byte, templateRaw []byte,
	serviceUID, serviceGID int) (HostProfile, D09CellTemplate, error) {
	registration, err := productsession.VerifyRegistration(registrationRaw, registrationSig, ed25519.PublicKey(publicKey))
	if err != nil {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(err, "product registration does not verify under the fixed product key")
	}
	profile, err := VerifyLinuxSignedHostProfile(profileRaw, profileSig, ed25519.PublicKey(publicKey))
	if err != nil {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(err, "product host profile does not verify under the fixed product key")
	}
	if profile.ContainmentProfile != D09ContainmentProfile || profile.SettlementProfile != D09SettlementProfile {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(nil, "product host profile is not the D09 containment and settlement profile")
	}
	if placeholders := profile.PlaceholderFields(); len(placeholders) != 0 {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(nil, "product host profile has placeholder fields "+strings.Join(placeholders, ","))
	}
	template, err := DecodeProductD09CellTemplate(templateRaw)
	if err != nil {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(err, "product D09 cell template is not admissible")
	}
	templateDigest, err := template.Digest()
	if err != nil || profile.ContainmentProfileDigest != templateDigest {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(err, "product host profile is not bound to the product cell template")
	}
	if template.ProductRegistrationDigest != registration.Digest || template.ExpectedImageDigest != registration.ImageDigest {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(nil, "product cell template is not bound to the product registration and image")
	}
	if profile.OfflineVerifierDigest != OfflineVerifierDigest(template.CosignDigest, template.CosignPublicKeyDigest) {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(nil, "product host profile does not bind the template's offline verifier")
	}
	if serviceUID <= 0 || serviceGID <= 0 || template.XattrScanUID != serviceUID || template.XattrScanGID != serviceGID {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(nil, "scanner account IDs differ from the registered service account")
	}
	if serviceUID == template.CellUID || serviceGID == template.CellGID {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(nil, "service account shares an ID with the in-cell account")
	}
	if template.UserNamespaceMappingDigest != DigestD09UserNamespaceMapping(template.CellUID, template.CellGID, serviceUID, serviceGID) {
		return HostProfile{}, D09CellTemplate{}, productD09Refused(nil, "user namespace mapping digest does not match the service and cell IDs")
	}
	return profile, template, nil
}

// loadProductD09Snapshot reads, verifies, pins, and measures every fixed product input in one order: service config, unit,
// effective unit, registration, host profile, template, service account, artifacts, and the managed-RW roots.
func loadProductD09Snapshot(ctx context.Context) (productD09Snapshot, error) {
	if ctx == nil {
		return productD09Snapshot{}, productD09Refused(nil, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return productD09Snapshot{}, err
	}
	inputs := productD09Inputs{}
	serviceConfigRaw, err := inputs.read(productsetup.ServiceConfigPath, productD09MaxConfigBytes)
	if err != nil {
		return productD09Snapshot{}, err
	}
	service, err := productD09DecodeServiceConfig(serviceConfigRaw)
	if err != nil {
		return productD09Snapshot{}, err
	}
	unitRaw, err := inputs.read(productsetup.UnitPath, productD09MaxConfigBytes)
	if err != nil {
		return productD09Snapshot{}, err
	}
	if err := productD09RequireNoUnitOverrides(); err != nil {
		return productD09Snapshot{}, err
	}
	if err := productD09ValidateUnit(unitRaw); err != nil {
		return productD09Snapshot{}, err
	}
	unitProperties, err := productD09RequireEffectiveUnit(ctx)
	if err != nil {
		return productD09Snapshot{}, err
	}
	registrationRaw, err := inputs.read(productsetup.RegistrationPath, int64(productsession.MaxRegistrationBytes))
	if err != nil {
		return productD09Snapshot{}, err
	}
	registrationSig, err := productD09ReadBase64(inputs, productsetup.RegistrationSignaturePath)
	if err != nil {
		return productD09Snapshot{}, err
	}
	publicKey, err := productD09ReadBase64(inputs, productsetup.RegistrationPublicKeyPath)
	if err != nil {
		return productD09Snapshot{}, err
	}
	profileRaw, err := inputs.read(productsetup.CommandHostProfilePath, maxHostProfileBytes)
	if err != nil {
		return productD09Snapshot{}, err
	}
	profileSig, err := productD09ReadBase64(inputs, productsetup.CommandHostProfileSignaturePath)
	if err != nil {
		return productD09Snapshot{}, err
	}
	templateRaw, err := inputs.read(productsetup.CommandCellTemplatePath, maxD09TemplateBytes)
	if err != nil {
		return productD09Snapshot{}, err
	}
	serviceUID, serviceGID, err := productD09ReadServiceIdentity(inputs)
	if err != nil {
		return productD09Snapshot{}, err
	}
	profile, template, err := validateProductD09Artifacts(profileRaw, profileSig, registrationRaw, registrationSig, publicKey,
		templateRaw, serviceUID, serviceGID)
	if err != nil {
		return productD09Snapshot{}, err
	}
	registration, err := productsession.VerifyRegistration(registrationRaw, registrationSig, ed25519.PublicKey(publicKey))
	if err != nil {
		return productD09Snapshot{}, productD09Refused(err, "product registration does not verify under the fixed product key")
	}
	if err := productD09RequireServiceMatchesRegistration(service, registration); err != nil {
		return productD09Snapshot{}, err
	}
	if err := productD09VerifyArtifacts(ctx, inputs, template, profile); err != nil {
		return productD09Snapshot{}, err
	}
	roots, rootScanners, err := productD09VerifyStateRoots(inputs, service, template, publicKey, serviceUID, serviceGID)
	if err != nil {
		return productD09Snapshot{}, err
	}
	return productD09Snapshot{
		profile:        profile,
		template:       template,
		serviceUID:     serviceUID,
		serviceGID:     serviceGID,
		inputs:         inputs,
		roots:          roots,
		rootScanners:   rootScanners,
		unitProperties: unitProperties,
	}, nil
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

// productD09ReadBase64 reads a standard base64 file, the product convention for keys and signatures.
func productD09ReadBase64(inputs productD09Inputs, path string) ([]byte, error) {
	raw, err := inputs.read(path, productD09MaxSignatureFileBytes)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, productD09Refused(err, "input is not standard base64: "+path)
	}
	return decoded, nil
}

// productD09ReadServiceIdentity derives the non-root service account from the installed passwd and group files. The
// account must be a nologin shell with a single-member group, and it must hold no subordinate ID range.
func productD09ReadServiceIdentity(inputs productD09Inputs) (int, int, error) {
	passwd, err := inputs.read("/etc/passwd", productD09MaxIdentityBytes)
	if err != nil {
		return 0, 0, err
	}
	group, err := inputs.read("/etc/group", productD09MaxIdentityBytes)
	if err != nil {
		return 0, 0, err
	}
	uid, gid, shell, found := lookupLocalServiceAccount(passwd, group, productsetup.ServiceUser, productsetup.ServiceGroup)
	if !found || uid <= 0 || gid <= 0 || !productD09IsNologinShell(shell) {
		return 0, 0, productD09Refused(nil, "service account is not a non-root nologin account with a single-member group")
	}
	ids := []string{productsetup.ServiceUser, productsetup.ServiceGroup, strconv.Itoa(uid), strconv.Itoa(gid)}
	for _, subIDFile := range []string{"/etc/subuid", "/etc/subgid"} {
		if _, err := os.Lstat(subIDFile); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		raw, err := inputs.read(subIDFile, productD09MaxIdentityBytes)
		if err != nil {
			return 0, 0, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			owner, _, _ := strings.Cut(line, ":")
			for _, id := range ids {
				if owner == id {
					return 0, 0, productD09Refused(nil, "service account has a subordinate ID range in "+subIDFile)
				}
			}
		}
	}
	return uid, gid, nil
}

func productD09IsNologinShell(shell string) bool {
	return shell == "/usr/sbin/nologin"
}

// productD09VerifyArtifacts checks the fixed product artifacts. The seccomp profile and cosign key are checked by digest on
// a read-only mount, the Podman configs are pinned byte for byte, the pinned host tools by digest, and the OCI image by its
// signature and by the digests of the in-image entrypoint, Bash, and Node.
func productD09VerifyArtifacts(ctx context.Context, inputs productD09Inputs, template D09CellTemplate, profile HostProfile) error {
	for _, artifact := range []struct{ path, digest, what string }{
		{productsetup.CommandSeccompPath, template.SeccompProfileDigest, "seccomp profile"},
		{productsetup.CommandCosignPublicKeyPath, template.CosignPublicKeyDigest, "cosign public key"},
	} {
		raw, err := inputs.read(artifact.path, productD09MaxArtifactBytes)
		if err != nil {
			return err
		}
		if err := requireReadOnlyMount(artifact.path); err != nil {
			return productD09Refused(err, artifact.what+" is not on a read-only mount")
		}
		if DigestBytes(raw) != artifact.digest {
			return productD09Refused(nil, artifact.what+" is not the digest the product template names")
		}
	}
	if raw, err := inputs.read(productsetup.ContainersConfPath, productD09MaxConfigBytes); err != nil || string(raw) != productD09ContainersConf {
		return productD09Refused(err, "containers.conf is not the pinned product configuration")
	}
	if raw, err := inputs.read(productsetup.StorageConfPath, productD09MaxConfigBytes); err != nil || string(raw) != productD09StorageConf {
		return productD09Refused(err, "storage.conf is not the pinned product configuration")
	}
	for _, tool := range []struct{ path, digest, what string }{
		{template.PodmanPath, template.PodmanDigest, "podman"},
		{template.CrunPath, template.CrunDigest, "crun"},
		{template.CosignPath, template.CosignDigest, "cosign"},
	} {
		if err := productD09ValidateAncestors(tool.path); err != nil {
			return err
		}
		if err := inputs.pin(tool.path, tool.digest); err != nil {
			return err
		}
		if err := verifyPinnedToolBinary(tool.path, tool.digest); err != nil {
			return productD09Refused(err, "pinned host tool "+tool.what+" does not match the template")
		}
	}
	layout := productsetup.CommandOCILayoutPath
	if err := productD09ValidateAncestors(filepath.Join(layout, "blobs", "sha256", "blob")); err != nil {
		return err
	}
	for _, name := range []string{"oci-layout", "index.json"} {
		if _, err := inputs.read(filepath.Join(layout, name), productD09MaxConfigBytes); err != nil {
			return err
		}
	}
	launcher := &D09PodmanCrunLauncher{runner: execD09CommandRunner{environment: productD09CommandEnvironment()}}
	if err := launcher.verifyOfflineSignature(ctx, template.Config(), template.CosignPath, true); err != nil {
		return productD09Refused(err, "offline cosign verification of the product image failed")
	}
	for _, image := range []struct{ path, digest, what string }{
		{template.EntrypointPath, template.EntrypointDigest, "in-image entrypoint"},
		{template.CommandExecutable, template.CommandExecutableDigest, "in-image Bash"},
		{profile.ChildExecutable, profile.ExecutableDigest, "in-image Node"},
	} {
		if err := productD09VerifyImageFile(ctx, template, image.path, image.digest, image.what); err != nil {
			return err
		}
	}
	return nil
}

// productD09VerifyImageFile resolves imagePath through the signed OCI layout without extracting it, and compares the digest
// of the regular file it names with the expected one.
func productD09VerifyImageFile(ctx context.Context, template D09CellTemplate, imagePath, expectedDigest, what string) error {
	file, err := readOCIImageFile(ctx, template.OCILayoutPath, template.ExpectedImageDigest, filepath.Clean(imagePath), true)
	if err != nil {
		return productD09Refused(err, what+" is not in the pinned product image")
	}
	if file.mode&0o111 == 0 || file.mode&0o022 != 0 {
		return productD09Refused(nil, what+" is not a non-writable executable regular file")
	}
	if file.digest != expectedDigest {
		return productD09Refused(nil, what+" digest does not match the template")
	}
	return nil
}

// productD09VerifyStateRoots binds the managed-RW roots to the signed xattr calibration under the service scanner. The state
// directory and the state root must be writable; every project root is measured against the same template and calibration.
func productD09VerifyStateRoots(inputs productD09Inputs, service productsetup.ServiceConfig, template D09CellTemplate,
	publicKey []byte, serviceUID, serviceGID int) (map[string]productD09DirIdentity, map[string]D09XattrVisibilityMeasurements, error) {
	if err := productD09ValidateServiceRoots(service); err != nil {
		return nil, nil, err
	}
	if !productD09IsCleanAbsolute(service.XattrCalibrationPath) || productsetup.IsThesisPath(service.XattrCalibrationPath) {
		return nil, nil, productD09Refused(nil, "xattr calibration path is not a clean product path")
	}
	encoded, err := inputs.read(service.XattrCalibrationPath, productD09MaxConfigBytes)
	if err != nil {
		return nil, nil, err
	}
	signature, err := productD09ReadBase64(inputs, service.XattrCalibrationPath+".sig")
	if err != nil {
		return nil, nil, err
	}
	calibration, err := verifyD09XattrCalibrationForSource(encoded, signature, ed25519.PublicKey(publicKey), D09XattrManagedRWSourceProfile)
	if err != nil {
		return nil, nil, productD09Refused(err, "xattr calibration does not verify under the fixed product key")
	}
	if err := productD09ValidateAncestors(productsetup.StateDir); err != nil {
		return nil, nil, err
	}
	roots := map[string]productD09DirIdentity{}
	scanners := map[string]D09XattrVisibilityMeasurements{}
	measure := func(path string, requireWritable bool) error {
		observed, identity, err := productD09MeasureRoot(path)
		if err != nil {
			return err
		}
		if requireWritable && observed.ReadOnly {
			return productD09Refused(nil, "managed-RW root is mounted read-only: "+path)
		}
		if err := productD09ValidateStateScanner(observed, calibration, template, serviceUID, serviceGID); err != nil {
			return err
		}
		roots[path] = identity
		scanners[path] = observed
		return nil
	}
	if err := measure(productsetup.StateDir, true); err != nil {
		return nil, nil, err
	}
	if err := measure(service.StateRoot, true); err != nil {
		return nil, nil, err
	}
	for _, project := range service.Projects {
		if err := measure(project, false); err != nil {
			return nil, nil, err
		}
	}
	return roots, scanners, nil
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

// productD09MeasureRoot measures one root directory as the current scanner sees it. The root is opened without following
// links, and the descriptor must be the directory its metadata named.
func productD09MeasureRoot(path string) (D09XattrVisibilityMeasurements, productD09DirIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return D09XattrVisibilityMeasurements{}, productD09DirIdentity{}, productD09Refused(err, "root is missing, symlinked, or not a directory: "+path)
	}
	dir, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return D09XattrVisibilityMeasurements{}, productD09DirIdentity{}, productD09Refused(err, "root cannot be opened without following links: "+path)
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return D09XattrVisibilityMeasurements{}, productD09DirIdentity{}, productD09Refused(err, "root changed while it was opened: "+path)
	}
	identity, err := productD09DirIdentityOf(opened)
	if err != nil {
		return D09XattrVisibilityMeasurements{}, productD09DirIdentity{}, err
	}
	observed, err := MeasureD09XattrScannerIdentity(dir)
	if err != nil {
		return D09XattrVisibilityMeasurements{}, productD09DirIdentity{}, productD09Refused(err, "root xattr scanner cannot be measured: "+path)
	}
	return observed, identity, nil
}

// productD09ValidateStateScanner requires one measured root to be scanned by the service account under the frozen privilege
// profile, and the template and the signed calibration to describe that same filesystem, mount, and scanner.
func productD09ValidateStateScanner(observed D09XattrVisibilityMeasurements, calibration D09XattrEnumerationCalibration,
	template D09CellTemplate, serviceUID, serviceGID int) error {
	if !observed.NoNewPrivs || !observed.InitialUserNamespace || observed.EffectiveCapabilities != d09EmptyCapabilitySet ||
		observed.ScanUID != serviceUID || observed.ScanGID != serviceGID || observed.ScanPrivilegeProfile != D09XattrScanPrivilegeProfile {
		return productD09Refused(nil, "root is not scanned by the service account under the frozen privilege profile")
	}
	if template.XattrScanUID != serviceUID || template.XattrScanGID != serviceGID || template.XattrScanPrivilegeProfile != D09XattrScanPrivilegeProfile {
		return productD09Refused(nil, "template scanner identity differs from the service scanner")
	}
	if template.XattrFilesystemMagic != observed.FilesystemMagic || template.XattrMountOptionsDigest != observed.MountOptionsDigest {
		return productD09Refused(nil, "template filesystem or mount options differ from the measured root")
	}
	expectedProfile := DigestD09XattrVisibilityProfile(observed.FilesystemMagic, observed.MountOptionsDigest, serviceUID, serviceGID,
		D09XattrScanPrivilegeProfile)
	if template.XattrVisibilityProfileDigest != expectedProfile || calibration.VisibilityProfileDigest != expectedProfile ||
		calibration.Authority != template.XattrVisibilityAuthority || calibration.FilesystemMagic != observed.FilesystemMagic ||
		calibration.MountOptionsDigest != observed.MountOptionsDigest || calibration.ScanUID != serviceUID ||
		calibration.ScanGID != serviceGID || calibration.ScanPrivilegeProfile != D09XattrScanPrivilegeProfile {
		return productD09Refused(nil, "template or managed-RW calibration differs from the measured root")
	}
	return nil
}

// productD09RequireEffectiveUnit asks systemd for the product unit's effective fragment, drop-ins, environment, and identity.
// It refuses unless the unit is loaded from the installed path, has no drop-in or environment file, no environment
// beyond the fixed product assignments, and runs as the product service account.
func productD09RequireEffectiveUnit(ctx context.Context) (map[string]string, error) {
	if err := productD09ValidateAncestors(productD09SystemctlPath); err != nil {
		return nil, err
	}
	info, err := os.Lstat(productD09SystemctlPath)
	if err != nil {
		return nil, productD09Refused(err, "systemctl is missing")
	}
	if err := productD09ValidateFileInfo(info, info.Size()); err != nil {
		return nil, err
	}
	args := []string{"show", "--no-pager"}
	for _, property := range productD09EffectiveUnitProperties {
		args = append(args, "--property="+property)
	}
	args = append(args, productsetup.UnitName)
	command := exec.CommandContext(ctx, productD09SystemctlPath, args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	output, err := command.Output()
	if err != nil {
		return nil, productD09Refused(err, "systemctl cannot show the product unit")
	}
	properties := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			properties[key] = value
		}
	}
	if err := productD09ValidateEffectiveUnit(properties); err != nil {
		return nil, err
	}
	return properties, nil
}

func productD09ValidateEffectiveUnit(properties map[string]string) error {
	for _, property := range productD09EffectiveUnitProperties {
		if _, ok := properties[property]; !ok {
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

// productD09ReadFile reads one protected product input. It refuses a missing, symlinked, non-regular, group- or
// world-writable, non-root-owned, or oversized file, and any ancestor that is not a protected directory.
func productD09ReadFile(path string, limit int64) ([]byte, error) {
	data, _, err := productD09ReadIdentifiedFile(path, limit)
	return data, err
}

// productD09ReadIdentifiedFile is productD09ReadFile that also returns the descriptor identity of the bytes it measured. The
// metadata is taken before and after the read, and the read fails if the file changed in between.
// productD09ReadPolicy keeps the production root policy separate from unprivileged filesystem fixtures.
// Tests can validate a descriptor and replace its pathname before the next lookup.
type productD09ReadPolicy struct {
	checkDirectory func(string, fs.FileInfo) error
	checkFile      func(fs.FileInfo, int64) error
}

var productD09ProtectedReadPolicy = productD09ReadPolicy{
	checkDirectory: func(_ string, info fs.FileInfo) error { return productD09ValidateDirectoryInfo(info) },
	checkFile:      productD09ValidateFileInfo,
}

func productD09ReadIdentifiedFile(path string, limit int64) ([]byte, productD09FileIdentity, error) {
	return productD09ReadFileWithPolicy(path, limit, productD09ProtectedReadPolicy)
}

func productD09ReadFileWithPolicy(path string, limit int64, policy productD09ReadPolicy) ([]byte, productD09FileIdentity, error) {
	parent, err := productD09OpenParent(path, policy)
	if err != nil {
		return nil, productD09FileIdentity{}, err
	}
	defer parent.Close()
	fd, err := syscall.Openat(int(parent.Fd()), filepath.Base(path), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, productD09FileIdentity{}, productD09Refused(err, "protected input cannot be opened without following links: "+path)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, productD09FileIdentity{}, productD09Refused(err, "protected input cannot be measured: "+path)
	}
	if err := policy.checkFile(opened, limit); err != nil {
		return nil, productD09FileIdentity{}, err
	}
	before, err := productD09FileIdentityOf(opened, "")
	if err != nil {
		return nil, productD09FileIdentity{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, productD09FileIdentity{}, productD09Refused(err, "read protected input: "+path)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, productD09FileIdentity{}, productD09Refused(err, "protected input cannot be measured after it was read: "+path)
	}
	afterIdentity, err := productD09FileIdentityOf(after, "")
	if err != nil || afterIdentity != before || int64(len(data)) != before.Size {
		return nil, productD09FileIdentity{}, productD09Refused(err, "protected input changed while it was read: "+path)
	}
	before.Digest = DigestBytes(data)
	return data, before, nil
}

// read returns the protected bytes at path and records their identity under that path.
func (inputs productD09Inputs) read(path string, limit int64) ([]byte, error) {
	data, identity, err := productD09ReadIdentifiedFile(path, limit)
	if err != nil {
		return nil, err
	}
	inputs[path] = identity
	return data, nil
}

// pin records the metadata of a file whose content a digest check verifies separately, such as a pinned host tool.
func (inputs productD09Inputs) pin(path, digest string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return productD09Refused(err, "pinned input is missing: "+path)
	}
	identity, err := productD09FileIdentityOf(info, digest)
	if err != nil {
		return err
	}
	inputs[path] = identity
	return nil
}

func productD09StatOf(info fs.FileInfo) (*syscall.Stat_t, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, productD09Refused(nil, "protected input has no Linux stat identity")
	}
	return stat, nil
}

func productD09FileIdentityOf(info fs.FileInfo, digest string) (productD09FileIdentity, error) {
	stat, err := productD09StatOf(info)
	if err != nil {
		return productD09FileIdentity{}, err
	}
	return productD09FileIdentity{
		Device:    uint64(stat.Dev),
		Inode:     uint64(stat.Ino),
		Size:      info.Size(),
		Mode:      uint32(info.Mode()),
		UID:       stat.Uid,
		GID:       stat.Gid,
		MTimeSec:  int64(stat.Mtim.Sec),
		MTimeNsec: int64(stat.Mtim.Nsec),
		CTimeSec:  int64(stat.Ctim.Sec),
		CTimeNsec: int64(stat.Ctim.Nsec),
		Digest:    digest,
	}, nil
}

func productD09DirIdentityOf(info fs.FileInfo) (productD09DirIdentity, error) {
	stat, err := productD09StatOf(info)
	if err != nil {
		return productD09DirIdentity{}, err
	}
	return productD09DirIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Mode: uint32(info.Mode()), UID: stat.Uid, GID: stat.Gid}, nil
}

// productD09ValidateFileInfo accepts a root-owned regular file that only root can write, within its size bound. It judges
// metadata alone, so the accept path needs no root privilege to test.
func productD09ValidateFileInfo(info fs.FileInfo, maximum int64) error {
	if info == nil {
		return productD09Refused(nil, "input has no metadata")
	}
	owner, ownerKnown := productD09OwnerUID(info)
	if !ownerKnown || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum ||
		owner != 0 || info.Mode().Perm()&0o022 != 0 {
		return productD09Refused(nil, "input must be a root-owned regular file that group and others cannot write, within its size bound")
	}
	return nil
}

// productD09ValidateDirectoryInfo accepts a root-owned, non-symlink directory that group and others cannot write.
func productD09ValidateDirectoryInfo(info fs.FileInfo) error {
	if info == nil {
		return productD09Refused(nil, "ancestor has no metadata")
	}
	owner, ownerKnown := productD09OwnerUID(info)
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || !ownerKnown || owner != 0 || info.Mode().Perm()&0o022 != 0 {
		return productD09Refused(nil, "ancestor must be a root-owned directory that group and others cannot write")
	}
	return nil
}

func productD09OwnerUID(info fs.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}

// productD09ValidateAncestors walks the protected chain descriptor-relatively, refusing symlinks at every component.
func productD09ValidateAncestors(path string) error {
	parent, err := productD09OpenParent(path, productD09ProtectedReadPolicy)
	if err == nil {
		parent.Close()
	}
	return err
}

// productD09OpenParent retains the verified directory for the leaf lookup. Replacing any pathname already walked cannot
// redirect subsequent opens: each component is opened relative to its verified parent, then checked by descriptor.
func productD09OpenParent(path string, policy productD09ReadPolicy) (*os.File, error) {
	if !productD09IsCleanAbsolute(path) {
		return nil, productD09Refused(nil, "protected path must be absolute and clean: "+path)
	}
	var current *os.File
	for _, dir := range productD09AncestorDirectories(path) {
		var fd int
		var err error
		if current == nil {
			fd, err = syscall.Open("/", corpusOPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		} else {
			fd, err = syscall.Openat(int(current.Fd()), filepath.Base(dir), corpusOPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			current.Close()
		}
		if err != nil {
			return nil, productD09Refused(err, "protected ancestor cannot be opened without following links: "+dir)
		}
		current = os.NewFile(uintptr(fd), dir)
		info, err := current.Stat()
		if err == nil {
			err = policy.checkDirectory(dir, info)
		}
		if err != nil {
			current.Close()
			return nil, productD09Refused(err, "protected ancestor "+dir)
		}
	}
	return current, nil
}

// productD09AncestorDirectories lists the directories above path, from / down to the parent of path.
func productD09AncestorDirectories(path string) []string {
	var dirs []string
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		dirs = append([]string{dir}, dirs...)
		if dir == string(filepath.Separator) {
			return dirs
		}
	}
}

func productD09IsCleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

// productD09RequireNoUnitOverrides refuses any drop-in that applies to the product unit or to every service, and any unit
// file of the same name that would shadow the product unit.
func productD09RequireNoUnitOverrides() error {
	unitName := filepath.Base(productsetup.UnitPath)
	for _, root := range productD09UnitOverrideRoots {
		for _, dropInDir := range []string{filepath.Join(root, unitName+".d"), filepath.Join(root, "service.d")} {
			if err := productD09RefuseDropInDirectory(dropInDir); err != nil {
				return err
			}
		}
	}
	for _, root := range productD09UnitShadowRoots {
		if _, err := os.Lstat(filepath.Join(root, unitName)); !errors.Is(err, fs.ErrNotExist) {
			return productD09Refused(err, "a unit file in "+root+" shadows the product unit")
		}
	}
	return nil
}

// productD09RefuseDropInDirectory allows a missing or empty drop-in directory and refuses anything else at that path.
func productD09RefuseDropInDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return productD09Refused(err, "a systemd drop-in location is present or unreadable at "+dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		return productD09Refused(err, "a systemd drop-in for the product unit is present at "+dir)
	}
	return nil
}

// productD09ValidateUnit accepts only the rendered product unit, with no drop-in or environment override.
func productD09ValidateUnit(unit []byte, dropIns ...[]byte) error {
	if len(dropIns) != 0 {
		return productD09Refused(nil, "product unit has a drop-in or environment override")
	}
	if err := productsetup.ValidateUnit(unit); err != nil {
		return productD09Refused(err, "product unit does not match the pinned unit grammar")
	}
	return nil
}

// productD09Refused wraps a cause in ErrProductD09HostRefused, without re-wrapping a cause that is already a refusal.
func productD09Refused(cause error, detail string) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrProductD09HostRefused, detail)
	}
	if errors.Is(cause, ErrProductD09HostRefused) {
		return fmt.Errorf("%s: %w", detail, cause)
	}
	return fmt.Errorf("%w: %s: %w", ErrProductD09HostRefused, detail, cause)
}
