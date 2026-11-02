//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"

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
	if err := cfg.validate(extraRoots); err != nil {
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

func productCommandCalibrationMatches(attestation workspace.XattrVisibilityAttestation, calibration piruntime.D09XattrEnumerationCalibration, template piruntime.D09CellTemplate) bool {
	return attestation.ProfileDigest == calibration.Digest && string(attestation.Authority) == calibration.Authority &&
		template.XattrFilesystemMagic == calibration.FilesystemMagic && template.XattrMountOptionsDigest == calibration.MountOptionsDigest &&
		template.XattrScanUID == calibration.ScanUID && template.XattrScanGID == calibration.ScanGID &&
		template.XattrScanPrivilegeProfile == calibration.ScanPrivilegeProfile && template.XattrVisibilityAuthority == calibration.Authority
}
