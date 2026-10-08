//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"syscall"

	"tbound/supervisor/internal/broker/protocol"
)

const (
	linuxTrustedProfileDirectory    = "/etc/tbound"
	linuxTrustedProfilePath         = "/etc/tbound/pi-host-profile.json"
	linuxTrustedProfileSigPath      = "/etc/tbound/pi-host-profile.ed25519"
	linuxTrustedProfileKeyPath      = "/etc/tbound/pi-host-profile.pub"
	linuxHostProfileSignatureDomain = "tbound-linux-host-profile/v1\x00"
	maxHostProfileBytes             = 1 << 20
)

// LinuxHostProfileVerifier is the only production TrustedHostVerifier in this
// package. Its profile and public key are read from fixed root-owned paths; a
// caller-supplied attestation callback cannot create an AdmissionCapability.
// Runtime handles are provided by the trusted composition root and are checked
// against the signed profile and executable/runtime bundle before attestations
// are returned.
type LinuxHostProfileVerifier struct {
	profile HostProfile
	runtime HostRuntimeBindings
}

func OpenLinuxHostProfileVerifier(runtime HostRuntimeBindings) (*LinuxHostProfileVerifier, error) {
	if err := validateRootOwnedProfileDirectory(linuxTrustedProfileDirectory); err != nil {
		return nil, err
	}
	profileBytes, err := readRootOwnedProfileFile(linuxTrustedProfilePath, maxHostProfileBytes)
	if err != nil {
		return nil, fmt.Errorf("read signed Pi host profile: %w", err)
	}
	signature, err := readRootOwnedProfileFile(linuxTrustedProfileSigPath, ed25519.SignatureSize)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, errors.Join(ErrHostProfileMissing, errors.New("root-owned Ed25519 host profile signature is unavailable"), err)
	}
	publicKey, err := readRootOwnedProfileFile(linuxTrustedProfileKeyPath, ed25519.PublicKeySize)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.Join(ErrHostProfileMissing, errors.New("root-owned Ed25519 host profile public key is unavailable"), err)
	}
	profile, err := VerifyLinuxSignedHostProfile(profileBytes, signature, ed25519.PublicKey(publicKey))
	if err != nil {
		return nil, err
	}
	verifier := &LinuxHostProfileVerifier{
		profile: profile,
		runtime: cloneHostRuntimeBindings(runtime),
	}
	if err := verifier.verifyRuntime(context.Background(), profile); err != nil {
		return nil, err
	}
	return verifier, nil
}

func (v *LinuxHostProfileVerifier) trustedHostProfileVerifier() {}

func (v *LinuxHostProfileVerifier) admitsFrozenLinuxProfile(profile HostProfile) error {
	if v == nil || profile != v.profile {
		return ErrHostProfileMismatch
	}
	return ErrProductionLauncherUnavailable
}

// VerifyLinuxSignedHostProfileBytes verifies canonicalized HostProfile bytes.
// This is a structural test helper, not an admission operation; callers still
// need OpenLinuxHostProfileVerifier and its fixed root-owned trust root.
func VerifyLinuxSignedHostProfile(profileBytes, signature []byte, publicKey ed25519.PublicKey) (HostProfile, error) {
	var profile HostProfile
	if len(profileBytes) == 0 || len(profileBytes) > maxHostProfileBytes || len(signature) != ed25519.SignatureSize || len(publicKey) != ed25519.PublicKeySize {
		return profile, ErrHostProfileMissing
	}
	if err := protocol.ValidateStrictJSON(profileBytes); err != nil {
		return profile, fmt.Errorf("strictly validate signed Pi profile JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(profileBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return HostProfile{}, fmt.Errorf("decode signed Pi profile: %w", err)
	}
	canonicalProfile := profile
	canonicalProfile.Digest = ""
	canonicalJSON, err := json.Marshal(canonicalProfile)
	if err != nil {
		return HostProfile{}, fmt.Errorf("canonicalize signed Pi profile: %w", err)
	}
	canonicalBytes := append([]byte(linuxHostProfileSignatureDomain), canonicalJSON...)
	if profile.Digest != DigestBytes(canonicalBytes) || !ed25519.Verify(publicKey, canonicalBytes, signature) {
		return HostProfile{}, ErrHostAttestationMissing
	}
	if err := profile.Validate(); err != nil {
		return HostProfile{}, err
	}
	if profile.Mode != ModeProduction {
		return HostProfile{}, ErrHostAdmissionRefused
	}
	return profile, nil
}

func (v *LinuxHostProfileVerifier) Attest(ctx context.Context, requested HostProfile) (HostAttestation, error) {
	if v == nil || ctx == nil {
		return HostAttestation{}, ErrHostAttestationMissing
	}
	if err := ctx.Err(); err != nil {
		return HostAttestation{}, err
	}
	if !reflect.DeepEqual(requested, v.profile) {
		return HostAttestation{}, ErrHostProfileMismatch
	}
	if err := v.verifyRuntime(ctx, requested); err != nil {
		return HostAttestation{}, err
	}
	p := v.profile
	return HostAttestation{
		HostID: p.ID, ProfileID: p.ID, ProfileDigest: p.Digest,
		ExecutableDigest: p.ExecutableDigest, NodeVersion: p.NodeVersion,
		ChildArgumentsDigest:   p.ChildArgumentsDigest,
		WorkingDirectoryDigest: p.WorkingDirectoryDigest, StdinDigest: p.StdinDigest,
		ChildEnvironmentDigest: p.ChildEnvironmentDigest, IPCProfileDigest: p.IPCProfileDigest,
		TerminalDigest: p.TerminalProfileDigest, ContainmentDigest: p.ContainmentProfileDigest,
		CgroupRootDigest: p.CgroupRootDigest,
		SettlementDigest: p.SettlementProfileDigest, ObservationDigest: p.ObservationProfileDigest,
		DescriptorDigest: p.DescriptorProfileDigest, PiRuntimeBundleDigest: p.PiRuntimeBundleDigest,
		SourceDigest: p.SourceProvenanceDigest, OfflineVerifierDigest: p.OfflineVerifierDigest,
		ProviderProfileDigest: p.ProviderProfileDigest,
	}, nil
}

func (v *LinuxHostProfileVerifier) BindRuntime(ctx context.Context, requested HostProfile) (HostRuntimeBindings, error) {
	if v == nil || ctx == nil {
		return HostRuntimeBindings{}, ErrHostBindingsMissing
	}
	if err := ctx.Err(); err != nil {
		return HostRuntimeBindings{}, err
	}
	if !reflect.DeepEqual(requested, v.profile) {
		return HostRuntimeBindings{}, ErrHostProfileMismatch
	}
	if err := v.verifyRuntime(ctx, requested); err != nil {
		return HostRuntimeBindings{}, err
	}
	return cloneHostRuntimeBindings(v.runtime), nil
}

func (v *LinuxHostProfileVerifier) verifyRuntime(ctx context.Context, profile HostProfile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	if profile.Mode != ModeProduction {
		return ErrHostAdmissionRefused
	}
	if err := v.runtime.validate(profile); err != nil {
		return err
	}
	if err := v.runtime.WorkerExposure.VerifyWorkerExposure(ctx, v.runtime.WorkerExposureEvidence); err != nil {
		return fmt.Errorf("reverify sealed generation and D06 provenance: %w", err)
	}
	if err := validateWorkerExposureDescriptor(v.runtime.WorkerExposureHandle, v.runtime.WorkerExposureEvidence); err != nil {
		return err
	}
	if err := v.runtime.WorkerRuntimeAssets.VerifyWorkerRuntimeAssets(ctx, v.runtime.RuntimeAssetEvidence); err != nil {
		return fmt.Errorf("reverify read-only runtime asset source: %w", err)
	}
	if err := validateRuntimeAssetDescriptor(v.runtime.PiRuntimeBundleHandle, v.runtime.RuntimeAssetEvidence); err != nil {
		return err
	}
	launcher, ok := v.runtime.Launcher.(interface{ admitsFrozenLinuxProfile(HostProfile) error })
	if !ok {
		return ErrProductionLauncherUnavailable
	}
	if err := launcher.admitsFrozenLinuxProfile(profile); err != nil {
		return err
	}
	if err := validateMinimalWorkerEnvironment(v.runtime.Environment); err != nil {
		return fmt.Errorf("signed Pi profile environment is not minimal: %w", err)
	}
	if err := validatePrivateDirectory(v.runtime.Environment); err != nil {
		return err
	}
	if err := verifyImmutableLinuxExecutable(profile.ChildExecutable); err != nil {
		return err
	}
	executable, err := openVerifiedExecutable(profile.ChildExecutable, profile.ExecutableDigest)
	if err != nil {
		return err
	}
	_ = executable.Close()
	runtimeDigest, err := VerifyLinuxPiRuntimeBundle(v.runtime.PiRuntimeBundleHandle)
	if err != nil || runtimeDigest != profile.PiRuntimeBundleDigest {
		return errors.Join(ErrRuntimeBundleRefused, err)
	}
	if err := validatePiWorkerDescriptors(profile.DescriptorProfile, v.runtime.Descriptors, v.runtime.WorkerExposureHandle, v.runtime.PiRuntimeBundleHandle); err != nil {
		return err
	}
	linuxLauncher, ok := v.runtime.Launcher.(*LinuxExecutableLauncher)
	if !ok || linuxLauncher.scope == nil || !sameBinding(linuxLauncher.scope, v.runtime.Descendant) ||
		linuxLauncher.scope.containment != profile.ContainmentProfile || linuxLauncher.scope.settlement != profile.SettlementProfile ||
		linuxLauncher.scope.RootDigest() != profile.CgroupRootDigest {
		return errors.New("signed Pi profile has no matching concrete Linux cgroup launcher/settler")
	}
	if err := validateCgroupDirectory(linuxLauncher.scope.file); err != nil {
		return errors.Join(ErrCgroupScopeRefused, err)
	}
	return nil
}

func cloneHostRuntimeBindings(runtime HostRuntimeBindings) HostRuntimeBindings {
	runtime.Arguments = append([]string(nil), runtime.Arguments...)
	runtime.Environment = append([]string(nil), runtime.Environment...)
	runtime.Descriptors = cloneInheritedDescriptors(runtime.Descriptors)
	return runtime
}

func validateRootOwnedProfileDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return ErrHostProfileMissing
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("Pi host profile directory must be root-owned and not group/world writable")
	}
	return nil
}

func readRootOwnedProfileFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || info.Size() < 0 || info.Size() > maximum {
		return nil, ErrHostProfileMissing
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return nil, errors.New("Pi host profile inputs must be root-owned and non-writable by group/world")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, ErrHostProfileMismatch
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errors.Join(err, ErrHostProfileMismatch)
	}
	return data, nil
}

var _ TrustedHostVerifier = (*LinuxHostProfileVerifier)(nil)
