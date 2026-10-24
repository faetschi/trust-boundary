//go:build linux

package piruntime

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file retains the signing format and generates an unsigned official Pi
// CLI RPC profile scaffold. Its measured identities remain placeholders, and
// the production launcher still refuses this contract pending runtime closure
// packaging and host qualification.
//
// None of these helpers print, log, or return private key material.

const (
	// PiWorkerCellArgumentsProfile labels the exact official Pi CLI RPC argv.
	PiWorkerCellArgumentsProfile = PiCLIProductionArgumentsProfile
	// PiWorkerCellEnvironmentProfile labels the fixed minimal in-cell env.
	PiWorkerCellEnvironmentProfile = PiCLIProductionEnvironmentProfile
	// PiWorkerCellExecutable is the in-cell Node executable path recorded in a
	// signed production profile. The frozen cell supplies the pinned Node bytes.
	PiWorkerCellExecutable = PiCLIProductionExecutable
	// PiWorkerCellWorkdir is the command cell's mount point of the workspace. The
	// Pi cell has no /work: it mounts the source at PiWorkerCellDefaultWorkingDirectory.
	PiWorkerCellWorkdir = "/work"
	// PiWorkerCellHome and PiWorkerCellTemp are private in-cell tmpfs locations;
	// neither points into the sealed, read-only source view.
	PiWorkerCellHome = PiCLIProductionHome
	PiWorkerCellTemp = PiCLIProductionTemp
	// PiWorkerCellRuntimeBundleRoot is the host-side runtime bundle root a
	// generated production profile records.
	PiWorkerCellRuntimeBundleRoot = PiCLIProductionRuntimeBundleRoot
	// PiWorkerCellNodeVersion is the pinned Node version recorded in a
	// generated production profile.
	PiWorkerCellNodeVersion = PiCLIProductionNodeVersion
	// LinuxHostProfileTemplateID marks a generated, placeholder-only template.
	// Admission refuses this identity so an unedited template can never be
	// mistaken for a frozen production profile.
	LinuxHostProfileTemplateID = "pi-host-profile-template-v1"
	// LinuxHostProfileProtectedPublicKeyPath is the fixed, root-owned admin
	// public key that is the only admission trust root. The CLI never reads a
	// verification key from an operator-supplied profile directory.
	LinuxHostProfileProtectedPublicKeyPath = linuxTrustedProfileKeyPath
)

// PiWorkerCellArguments returns the exact Node argv for the pinned official Pi
// CLI entry and RPC extension. The bundle digest separately pins CLI contents.
func PiWorkerCellArguments() []string {
	return productionPiCLIArguments()
}

// PiWorkerCellEnvironment returns a copy of the fixed minimal in-cell
// environment of the governed Pi worker. The caller cannot mutate the shared
// identity.
func PiWorkerCellEnvironment() []string {
	return productionPiCLIEnvironment()
}

// LinuxHostProfileSigningBytes returns the exact domain-prefixed canonical
// bytes the Linux host-profile verifier signs and digests. The Digest field is
// cleared before canonicalization, exactly as VerifyLinuxSignedHostProfile
// recomputes it. This is the single source of truth for the signature domain
// and the canonical JSON, so the signer and the verifier cannot disagree.
func LinuxHostProfileSigningBytes(profile HostProfile) ([]byte, error) {
	canonicalProfile := profile
	canonicalProfile.Digest = ""
	canonicalJSON, err := json.Marshal(canonicalProfile)
	if err != nil {
		return nil, fmt.Errorf("canonicalize signed Pi profile: %w", err)
	}
	return append([]byte(linuxHostProfileSignatureDomain), canonicalJSON...), nil
}

// SignLinuxHostProfile signs profile with the Linux host-profile signature
// domain. It returns the strict canonical JSON with Digest set exactly as
// VerifyLinuxSignedHostProfile recomputes it, the domain-prefixed Ed25519
// signature, and the matching public key. The private key is used in process
// only and is never returned or printed.
func SignLinuxHostProfile(profile HostProfile, privateKey ed25519.PrivateKey) (profileJSON, signature []byte, publicKey ed25519.PublicKey, err error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, nil, nil, fmt.Errorf("%w: Ed25519 private key must be %d bytes", ErrHostProfileMissing, ed25519.PrivateKeySize)
	}
	signingBytes, err := LinuxHostProfileSigningBytes(profile)
	if err != nil {
		return nil, nil, nil, err
	}
	signed := profile
	signed.Digest = DigestBytes(signingBytes)
	profileJSON, err = json.Marshal(signed)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode signed Pi profile: %w", err)
	}
	publicKey = append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	return profileJSON, ed25519.Sign(privateKey, signingBytes), publicKey, nil
}

// VerifyLinuxSignedHostProfileFiles reads and verifies a signed profile from
// explicit files. Unlike the fixed /etc/tbound loader it does not require
// root-owned inputs: callers that supply their own path are responsible for
// that path's custody. It performs the same cryptographic verification.
func VerifyLinuxSignedHostProfileFiles(profilePath, signaturePath, publicKeyPath string) (HostProfile, error) {
	if strings.TrimSpace(profilePath) == "" || strings.TrimSpace(signaturePath) == "" || strings.TrimSpace(publicKeyPath) == "" {
		return HostProfile{}, ErrHostProfileMissing
	}
	profileBytes, err := os.ReadFile(profilePath)
	if err != nil {
		return HostProfile{}, fmt.Errorf("read signed Pi host profile: %w", err)
	}
	signature, err := os.ReadFile(signaturePath)
	if err != nil {
		return HostProfile{}, fmt.Errorf("read Ed25519 host profile signature: %w", err)
	}
	publicKey, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return HostProfile{}, fmt.Errorf("read Ed25519 host profile public key: %w", err)
	}
	if len(signature) != ed25519.SignatureSize || len(publicKey) != ed25519.PublicKeySize {
		return HostProfile{}, ErrHostProfileMissing
	}
	return VerifyLinuxSignedHostProfile(profileBytes, signature, ed25519.PublicKey(publicKey))
}

type linuxHostProfileDataSources struct {
	profile   string
	signature string
	rootOwned bool
}

func defaultLinuxHostProfileDataSources() linuxHostProfileDataSources {
	return linuxHostProfileDataSources{
		profile:   linuxTrustedProfilePath,
		signature: linuxTrustedProfileSigPath,
		rootOwned: true,
	}
}

// resolveLinuxHostProfileDataSources resolves the --native-host-profile path to
// the profile/signature data sources only. The public key is deliberately never
// derived from the operator path: the CLI profile path cannot choose its own
// trust root. An explicit directory uses the fixed profile/signature file names
// inside it; an explicit file derives the sibling .ed25519 signature; an absent
// or empty path falls back to the root-owned /etc/tbound trust root.
func resolveLinuxHostProfileDataSources(path string) linuxHostProfileDataSources {
	if strings.TrimSpace(path) == "" {
		return defaultLinuxHostProfileDataSources()
	}
	info, err := os.Lstat(path)
	if err != nil {
		return defaultLinuxHostProfileDataSources()
	}
	if info.IsDir() {
		return linuxHostProfileDataSources{
			profile:   filepath.Join(path, filepath.Base(linuxTrustedProfilePath)),
			signature: filepath.Join(path, filepath.Base(linuxTrustedProfileSigPath)),
		}
	}
	base := path
	if ext := filepath.Ext(base); ext == ".json" {
		base = strings.TrimSuffix(base, ext)
	}
	return linuxHostProfileDataSources{profile: path, signature: base + ".ed25519"}
}

// readProtectedLinuxHostProfilePublicKey reads the authorized admin public key
// from the fixed, root-owned trust root. It is never read from an
// operator-supplied directory, so an operator cannot substitute a self-signed
// trust root by pointing --native-host-profile at their own key.
func readProtectedLinuxHostProfilePublicKey() ([]byte, error) {
	if err := validateRootOwnedProfileDirectory(linuxTrustedProfileDirectory); err != nil {
		return nil, fmt.Errorf("protected host-profile trust root directory: %w", err)
	}
	publicKey, err := readRootOwnedProfileFile(linuxTrustedProfileKeyPath, ed25519.PublicKeySize)
	if err != nil {
		return nil, fmt.Errorf("read protected Ed25519 host profile public key: %w", err)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, ErrHostProfileMissing
	}
	return publicKey, nil
}

// LoadLinuxSignedHostProfile loads and verifies a signed host profile. The
// profile and signature may come from an operator-supplied path only when that
// path's directory is protected (root-owned, not group/world-writable) or from
// the fixed /etc/tbound trust root when the path is empty or absent. The
// Ed25519 public key ALWAYS comes from the fixed, root-owned trust root, so the
// CLI cannot choose its own trust root. This loader verifies exactly as the
// production verifier does but makes no admission decision.
func LoadLinuxSignedHostProfile(path string) (HostProfile, error) {
	sources := resolveLinuxHostProfileDataSources(path)
	directory := linuxTrustedProfileDirectory
	if !sources.rootOwned {
		directory = filepath.Dir(sources.profile)
	}
	if err := validateRootOwnedProfileDirectory(directory); err != nil {
		return HostProfile{}, fmt.Errorf("protected host-profile directory: %w", err)
	}
	profileBytes, err := readRootOwnedProfileFile(sources.profile, maxHostProfileBytes)
	if err != nil {
		return HostProfile{}, fmt.Errorf("read signed Pi host profile: %w", err)
	}
	signature, err := readRootOwnedProfileFile(sources.signature, ed25519.SignatureSize)
	if err != nil {
		return HostProfile{}, fmt.Errorf("read Ed25519 host profile signature: %w", err)
	}
	publicKey, err := readProtectedLinuxHostProfilePublicKey()
	if err != nil {
		return HostProfile{}, err
	}
	if len(signature) != ed25519.SignatureSize {
		return HostProfile{}, ErrHostProfileMissing
	}
	return VerifyLinuxSignedHostProfile(profileBytes, signature, ed25519.PublicKey(publicKey))
}

// placeholderDigest derives a valid, clearly-labelled placeholder sha256 for a
// generated template field the operator must replace with a measured value.
func placeholderDigest(label string) string {
	return DigestBytes([]byte("PLACEHOLDER/UNVERIFIED/" + label))
}

// DefaultLinuxHostProfileTemplate returns an unsigned official Pi CLI RPC
// profile scaffold. All host-measured identities remain placeholders, so
// signing this template alone cannot satisfy production qualification or
// bypass the refused CLI launcher.
//
// The executable, runtime-bundle, cgroup, source-provenance, offline-verifier,
// and provider digests are labelled placeholders and MUST be replaced with the
// measured experiment values before the profile is treated as authoritative.
func DefaultLinuxHostProfileTemplate() HostProfile {
	return HostProfile{
		ID:                       LinuxHostProfileTemplateID,
		Mode:                     ModeProduction,
		ChildExecutable:          PiWorkerCellExecutable,
		ExecutableDigest:         placeholderDigest("node-executable"),
		NodeVersion:              PiWorkerCellNodeVersion,
		ChildArgumentsProfile:    PiWorkerCellArgumentsProfile,
		ChildArgumentsDigest:     DigestArguments(PiWorkerCellArguments()),
		WorkingDirectory:         PiWorkerCellDefaultWorkingDirectory,
		WorkingDirectoryDigest:   DigestBytes([]byte(PiWorkerCellDefaultWorkingDirectory)),
		StdinProfile:             PiCLIProductionStdinProfile,
		StdinDigest:              DigestBytes([]byte(PiCLIProductionStdinProfile)),
		StdoutProfile:            PiCLIProductionStdoutProfile,
		StdoutProfileDigest:      DigestBytes([]byte(PiCLIProductionStdoutProfile)),
		ChildEnvironment:         PiWorkerCellEnvironmentProfile,
		ChildEnvironmentDigest:   DigestEnvironment(PiWorkerCellEnvironment()),
		IPCProfile:               PiCLIProductionIPCProfile,
		IPCProfileDigest:         DigestBytes([]byte(PiCLIProductionIPCProfile)),
		TerminalProfile:          PiCLIProductionTerminalProfile,
		TerminalProfileDigest:    DigestBytes([]byte(PiCLIProductionTerminalProfile)),
		ContainmentProfile:       D09ContainmentProfile,
		ContainmentProfileDigest: DigestBytes([]byte(D09ContainmentProfile)),
		CgroupRootDigest:         placeholderDigest("cgroup-root"),
		SettlementProfile:        D09SettlementProfile,
		SettlementProfileDigest:  DigestBytes([]byte(D09SettlementProfile)),
		ObservationProfile:       "worker-events/fd8/v1",
		ObservationProfileDigest: DigestBytes([]byte("worker-events/fd8/v1")),
		DescriptorProfile:        PiWorkerDescriptorProfile,
		DescriptorProfileDigest:  DigestBytes([]byte(PiWorkerDescriptorProfile)),
		PiRuntimeBundleRoot:      PiWorkerCellRuntimeBundleRoot,
		PiRuntimeBundleDigest:    placeholderDigest("pi-runtime-bundle"),
		SourceProvenance:         "source-provenance/d06/v1",
		SourceProvenanceDigest:   placeholderDigest("source-provenance"),
		OfflineVerifier:          "cosign-offline/v1",
		OfflineVerifierDigest:    placeholderDigest("cosign-offline"),
		ProviderProfile:          "go-providerbridge/v1",
		ProviderProfileDigest:    placeholderDigest("go-providerbridge"),
	}
}

// MarshalUnsignedLinuxHostProfileTemplate returns the canonical JSON of the
// unsigned template. It is the exact byte form the signer consumes.
func MarshalUnsignedLinuxHostProfileTemplate() ([]byte, error) {
	return json.Marshal(DefaultLinuxHostProfileTemplate())
}

// PlaceholderFields names the fields of a profile that still hold the value the generated template put there for
// the operator to replace: the template id, and the digests of the Node executable, the cgroup root, the runtime
// bundle, the source provenance, the offline verifier and the provider profile. A profile with one is not a
// profile of anything measured, and neither the signing tool nor the launcher accepts it.
func (p HostProfile) PlaceholderFields() []string {
	var fields []string
	if p.ID == LinuxHostProfileTemplateID {
		fields = append(fields, "ID")
	}
	for _, f := range []struct{ name, value, label string }{
		{"ExecutableDigest", p.ExecutableDigest, "node-executable"},
		{"CgroupRootDigest", p.CgroupRootDigest, "cgroup-root"},
		{"PiRuntimeBundleDigest", p.PiRuntimeBundleDigest, "pi-runtime-bundle"},
		{"SourceProvenanceDigest", p.SourceProvenanceDigest, "source-provenance"},
		{"OfflineVerifierDigest", p.OfflineVerifierDigest, "cosign-offline"},
		{"ProviderProfileDigest", p.ProviderProfileDigest, "go-providerbridge"},
	} {
		if f.value == placeholderDigest(f.label) {
			fields = append(fields, f.name)
		}
	}
	return fields
}

// OfflineVerifierDigest is the OfflineVerifierDigest a profile pins for the offline image verifier: the digest of
// the pinned cosign executable and of the cosign public key it verifies under, in that order.
func OfflineVerifierDigest(cosignDigest, cosignPublicKeyDigest string) string {
	return DigestBytes([]byte("tbound-offline-verifier/v1\x00" + cosignDigest + "\x00" + cosignPublicKeyDigest))
}
