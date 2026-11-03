//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
	"tbound/supervisor/internal/workspace"
)

// productCommandInputs contains prospective public artifacts, not installed host authority.
// Signatures and the registration public key retain their installed base64 wrappers.
type productCommandInputs struct {
	Registration, RegistrationSignature, PublicKey []byte
	Profile, ProfileSignature, Template            []byte
	Unit, Seccomp, CosignPublicKey                 []byte
}

// validateProductCommandInputs checks prospective signed inputs without an account,
// unit installation or Podman store. It cannot construct a command host or claim readiness.
func validateProductCommandInputs(cfg productServiceConfig, registration productsession.Registration, in productCommandInputs, extraRoots ...string) (piruntime.HostProfile, piruntime.D09CellTemplate, error) {
	refuse := func(err error) (piruntime.HostProfile, piruntime.D09CellTemplate, error) {
		return piruntime.HostProfile{}, piruntime.D09CellTemplate{}, fmt.Errorf("%w: static product command inputs: %v", piruntime.ErrProductD09HostRefused, err)
	}
	// The live root is not a test override: admitting / would permit every non-thesis host path.
	var testRoots []string
	for _, root := range extraRoots {
		if root != "" && root != "/" {
			testRoots = append(testRoots, root)
		}
	}
	if err := cfg.validate(testRoots); err != nil {
		return refuse(err)
	}
	if len(in.PublicKey) > 1024 || len(in.ProfileSignature) > 1024 || len(in.RegistrationSignature) > 1024 || len(in.Template) > 1<<20 || len(in.Unit) > productServiceMaxConfigBytes {
		return refuse(errors.New("public command artifact exceeds its installed size bound"))
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(in.PublicKey)))
	if err != nil {
		return refuse(err)
	}
	registrationSignature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(in.RegistrationSignature)))
	if err != nil {
		return refuse(err)
	}
	verified, err := productsession.VerifyRegistration(in.Registration, registrationSignature, ed25519.PublicKey(key))
	if err != nil {
		return refuse(err)
	}
	if !reflect.DeepEqual(verified, registration) {
		return refuse(errors.New("candidate registration differs from the proposed registration"))
	}
	if err := validateProductCommandConfig(cfg, verified); err != nil {
		return refuse(err)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(in.ProfileSignature)))
	if err != nil {
		return refuse(err)
	}
	profile, err := piruntime.VerifyLinuxSignedHostProfile(in.Profile, signature, ed25519.PublicKey(key))
	if err != nil {
		return refuse(err)
	}
	template, err := piruntime.DecodeProductD09CellTemplate(in.Template)
	if err != nil {
		return refuse(err)
	}
	templateDigest, err := template.Digest()
	if err != nil {
		return refuse(err)
	}
	if profile.Mode != piruntime.ModeProduction || profile.ContainmentProfile != piruntime.D09ContainmentProfile ||
		profile.SettlementProfile != piruntime.D09SettlementProfile || len(profile.PlaceholderFields()) != 0 ||
		profile.ContainmentProfileDigest != templateDigest || template.ProductRegistrationDigest != verified.Digest ||
		template.ExpectedImageDigest != verified.ImageDigest ||
		profile.OfflineVerifierDigest != piruntime.OfflineVerifierDigest(template.CosignDigest, template.CosignPublicKeyDigest) {
		return refuse(errors.New("profile, product template and registration bindings differ"))
	}
	if template.CosignPublicKeyPath != productsetup.CommandCosignPublicKeyPath || template.OCILayoutPath != productsetup.CommandOCILayoutPath ||
		template.SeccompProfilePath != productsetup.CommandSeccompPath || template.RouteProbe != nil {
		return refuse(errors.New("command artifacts do not use the fixed product paths"))
	}
	if template.CellUID == template.XattrScanUID || template.CellGID == template.XattrScanGID ||
		template.UserNamespaceMappingDigest != piruntime.DigestD09UserNamespaceMapping(template.CellUID, template.CellGID, template.XattrScanUID, template.XattrScanGID) {
		return refuse(errors.New("command template does not bind the single own-ID mapping"))
	}
	if err := productsetup.ValidateUnit(in.Unit); err != nil {
		return refuse(err)
	}
	if len(in.Seccomp) == 0 || len(in.Seccomp) > 1<<20 || piruntime.DigestBytes(in.Seccomp) != template.SeccompProfileDigest ||
		len(in.CosignPublicKey) == 0 || len(in.CosignPublicKey) > 1<<20 || piruntime.DigestBytes(in.CosignPublicKey) != template.CosignPublicKeyDigest {
		return refuse(errors.New("seccomp or cosign public key differs from the signed template"))
	}
	return profile, template, nil
}

func validateProductCommandConfig(cfg productServiceConfig, registration productsession.Registration) error {
	if cfg.SchemaVersion != productServiceConfigSchema {
		return errors.New("command configuration has an unknown schema")
	}
	identity, err := productHostIdentity(registration.ProviderProfiles)
	if err != nil {
		return err
	}
	signing, err := productsession.RegistrationSigningBytes(registration)
	if err != nil {
		return err
	}
	if piruntime.DigestBytes(signing) != registration.Digest || cfg.GitDigest != registration.GitDigest ||
		identity != registration.HostIdentityDigest || cfg.WorkerMode != productsession.WorkerSupervisorLoop ||
		len(cfg.Projects) != len(registration.Projects) {
		return errors.New("command configuration differs from the verified product identity")
	}
	for _, project := range registration.Projects {
		if cfg.Projects[project.ProjectID] == "" {
			return errors.New("command configuration is missing a registered project")
		}
	}
	return nil
}

// loadProductCommandHost is installed admission only. The runtime's fixed-path
// verifier issues the opaque value; caller config and a factory cannot issue one.
func loadProductCommandHost(ctx context.Context, cfg productServiceConfig, env *productTurnEnv) (*piruntime.D09ProductCommandHost, error) {
	if ctx == nil || env == nil || cfg.RegistrationPath != productsetup.RegistrationPath ||
		cfg.RegistrationSignaturePath != productsetup.RegistrationSignaturePath || cfg.RegistrationPublicKeyPath != productsetup.RegistrationPublicKeyPath {
		return nil, fmt.Errorf("%w: Bash requires the canonical installed product registration and key", piruntime.ErrProductD09HostRefused)
	}
	if err := cfg.validate(nil); err != nil {
		return nil, err
	}
	host, err := piruntime.LoadProductD09CommandHost(ctx)
	if err != nil {
		return nil, err
	}
	installed, err := loadProductServiceConfig(productsetup.ServiceConfigPath)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(installed, cfg) || cfg.StateRoot != env.StateRoot || !reflect.DeepEqual(cfg.Projects, env.ProjectRoots) {
		return nil, errors.New("product command config differs from the protected installed config or turn roots")
	}
	if err := validateProductCommandConfig(cfg, env.Registration); err != nil {
		return nil, err
	}
	template := host.Template()
	if template.ProductRegistrationDigest != env.Registration.Digest || template.ExpectedImageDigest != env.Registration.ImageDigest {
		return nil, errors.New("product command host differs from the turn registration")
	}
	// Reuse the product managed-rw scanner check; this grants no thesis managed-root lease.
	attestation, err := productHostXattr(cfg, env)
	if err != nil {
		return nil, err
	}
	key, err := readProductHostBase64(cfg.RegistrationPublicKeyPath, []uint32{0})
	if err != nil {
		return nil, err
	}
	calibration, _, _, err := verifyProductCalibration(cfg.XattrCalibrationPath, ed25519.PublicKey(key), []uint32{0})
	if err != nil {
		return nil, err
	}
	if !productCommandCalibrationMatches(attestation, calibration, template) {
		return nil, errors.New("product command template differs from the signed managed-rw calibration")
	}
	if err := host.Revalidate(ctx); err != nil {
		return nil, err
	}
	return host, nil
}

// prepareProductCommandRuntime creates the Podman directories under the unit's runtime directory: tmp, cache and the
// containers runroot. systemd creates the runtime directory at start, so callers run this after that and before any
// Podman child starts. root is "/" on a host. Each directory must be a real directory owned by the service account with mode 0700.
// A symlink, or a directory with another owner or mode, is refused rather than repaired.
func prepareProductCommandRuntime(root string, serviceUID, serviceGID int) error {
	if err := requireCommandRuntimeDirectory(filepath.Join(root, productsetup.RuntimeDir), 0o750, serviceUID, serviceGID); err != nil {
		return err
	}
	for _, dir := range []string{productsetup.PodmanTmpDir, productsetup.PodmanCacheDir, productsetup.PodmanRunrootDir} {
		logical := filepath.Join(root, dir)
		if _, err := os.Lstat(logical); errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(logical, 0o700); err != nil {
				return err
			}
		}
		if err := requireCommandRuntimeDirectory(logical, 0o700, serviceUID, serviceGID); err != nil {
			return err
		}
	}
	return nil
}

// requireCommandRuntimeDirectory refuses anything but a real directory of the wanted mode and owner. Lstat does not
// follow a symlink, so a link is refused as not a directory.
func requireCommandRuntimeDirectory(path string, mode fs.FileMode, uid, gid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory: symlinks and files are refused", path)
	}
	st := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != mode || int(st.Uid) != uid || int(st.Gid) != gid {
		return fmt.Errorf("%s must be mode %04o owned by %d:%d, found mode %04o owned by %d:%d",
			path, mode, uid, gid, info.Mode().Perm(), st.Uid, st.Gid)
	}
	return nil
}

func productCommandCalibrationMatches(attestation workspace.XattrVisibilityAttestation, calibration piruntime.D09XattrEnumerationCalibration, template piruntime.D09CellTemplate) bool {
	return attestation.ProfileDigest == calibration.Digest && string(attestation.Authority) == calibration.Authority &&
		template.XattrFilesystemMagic == calibration.FilesystemMagic && template.XattrMountOptionsDigest == calibration.MountOptionsDigest &&
		template.XattrScanUID == calibration.ScanUID && template.XattrScanGID == calibration.ScanGID &&
		template.XattrScanPrivilegeProfile == calibration.ScanPrivilegeProfile && template.XattrVisibilityAuthority == calibration.Authority
}
