package piruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"unicode/utf8"

	"tbound/supervisor/internal/piinstall"
)

var (
	ErrHostProfileMissing            = errors.New("native Pi host profile is missing")
	ErrHostAttestationMissing        = errors.New("native Pi host attestation is missing")
	ErrHostProfileMismatch           = errors.New("native Pi host profile and attestation do not match")
	ErrHostAdmissionRefused          = errors.New("native Pi host admission is not authorized")
	ErrHostBindingsMissing           = errors.New("native Pi host runtime bindings are missing")
	ErrChildCapabilityMissing        = errors.New("native child lacks an admitted host capability")
	ErrProductionLauncherUnavailable = errors.New("no production Pi launcher is bound to the frozen Podman/crun host profile")
)

const (
	ModeProduction = "production"
	ModeFixture    = "fixture"

	developerPiCLIProfileID          = "developer-official-pi-cli-uncontained"
	developerPiCLIArgumentsProfile   = "developer-pi-cli-rpc-v1"
	developerPiCLIEnvironmentProfile = "developer-pi-cli-isolated/v1"
	developerPiCLIContainmentProfile = "developer-uncontained"
	developerPiCLISettlementProfile  = "direct-child-only-unknown"
	developerPiCLIObservationProfile = "worker-events/fd8/v1"
	developerPiCLISourceProvenance   = "DEVELOPMENT/UNKNOWN"
	developerPiCLIOfflineVerifier    = "none-developer-only"
	developerPiCLIProviderProfile    = "go-providerbridge-synthetic-peer-nonclaiming"
)

// The production profile pins the official Pi CLI RPC contract. The CLI and
// extension are read from the verified runtime bundle mounted at /runtime;
// fd 9 remains the separately held host-side bundle descriptor.
const (
	PiCLIProductionArgumentsProfile   = "governed-pi-cli-rpc-argv/0.87.1"
	PiCLIProductionEnvironmentProfile = "governed-pi-cli-cell-env/v1"
	PiCLIProductionStdinProfile       = "pi-rpc-jsonl-stdin/v0.87.1"
	PiCLIProductionStdoutProfile      = "pi-rpc-jsonl-stdout/v0.87.1"
	PiCLIProductionTerminalProfile    = "pi-rpc-no-terminal/v1"
	PiCLIProductionIPCProfile         = "tbound-ipc/v1/fd6"
	PiCLIProductionNodeVersion        = "v24.21.0"
	PiCLIProductionExecutable         = "/usr/local/bin/node"
	PiCLIProductionRuntimeBundleRoot  = "/var/lib/tbound/runtime"
	PiCLIProductionEntryPath          = "/runtime/cli/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js"
	PiCLIProductionExtensionPath      = "/runtime/extension/src/tbound-cli-extension.ts"
	PiCLIProductionHome               = "/tmp/home"
	PiCLIProductionTemp               = "/tmp/tbound-tmp"
)

// productionPiCLIArguments is the single signed argv vector for Node running
// the official Pi 0.87.1 CLI in RPC mode. The CLI entry and extension path are
// inside the read-only runtime bundle mounted at /runtime.
func productionPiCLIArguments() []string {
	flags := []string{
		"--mode", "rpc", "--no-session", "--no-extensions", "--no-builtin-tools",
		"--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
		"--no-approve", "--offline", "--extension", PiCLIProductionExtensionPath,
		"--provider", piinstall.PinnedPiProvider, "--model", piinstall.PinnedPiProvider + "/" + piinstall.PinnedPiModel,
	}
	return append([]string{PiCLIProductionEntryPath}, flags...)
}

func productionPiCLIEnvironment() []string {
	return []string{
		"HOME=" + PiCLIProductionHome,
		"LANG=C.UTF-8",
		"PI_OFFLINE=1",
		"TMPDIR=" + PiCLIProductionTemp,
	}
}

// HostProfile is trusted host configuration. It is not decoded from Pi IPC or
// browser JSON. Digest is the reviewed identity of the complete runtime
// profile, including every component digest below.
type HostProfile struct {
	ID                       string
	Digest                   string
	Mode                     string
	ChildExecutable          string
	ExecutableDigest         string
	NodeVersion              string
	ChildArgumentsProfile    string
	ChildArgumentsDigest     string
	WorkingDirectory         string
	WorkingDirectoryDigest   string
	StdinProfile             string
	StdinDigest              string
	StdoutProfile            string
	StdoutProfileDigest      string
	ChildEnvironment         string
	ChildEnvironmentDigest   string
	IPCProfile               string
	IPCProfileDigest         string
	TerminalProfile          string
	TerminalProfileDigest    string
	ContainmentProfile       string
	ContainmentProfileDigest string
	CgroupRootDigest         string
	SettlementProfile        string
	SettlementProfileDigest  string
	ObservationProfile       string
	ObservationProfileDigest string
	DescriptorProfile        string
	DescriptorProfileDigest  string
	PiRuntimeBundleRoot      string
	PiRuntimeBundleDigest    string
	SourceProvenance         string
	SourceProvenanceDigest   string
	OfflineVerifier          string
	OfflineVerifierDigest    string
	ProviderProfile          string
	ProviderProfileDigest    string
}

// HostAttestation is a receipt returned by the trusted host/profile verifier.
// It deliberately has no caller-controlled Trusted boolean: production code
// must obtain it through TrustedHostVerifier and AdmitProduction.
type HostAttestation struct {
	HostID                 string
	ProfileID              string
	ProfileDigest          string
	ExecutableDigest       string
	NodeVersion            string
	ChildArgumentsDigest   string
	WorkingDirectoryDigest string
	StdinDigest            string
	StdoutDigest           string
	ChildEnvironmentDigest string
	IPCProfileDigest       string
	TerminalDigest         string
	ContainmentDigest      string
	CgroupRootDigest       string
	SettlementDigest       string
	ObservationDigest      string
	DescriptorDigest       string
	PiRuntimeBundleDigest  string
	SourceDigest           string
	OfflineVerifierDigest  string
	ProviderProfileDigest  string
}

// HostAttestor is implemented by the trusted host/profile loader. It is not a
// browser, Pi worker, or model callback. A test implementation is component
// evidence only; it does not make production externally authentic.
type HostAttestor interface {
	Attest(context.Context, HostProfile) (HostAttestation, error)
}

// TrustedHostVerifier extends the receipt with the concrete runtime bindings
// that the verifier admitted. A syntactic receipt or a callback that does not
// supply these bindings cannot produce a child-start capability.
type TrustedHostVerifier interface {
	HostAttestor
	BindRuntime(context.Context, HostProfile) (HostRuntimeBindings, error)
	trustedHostProfileVerifier()
}

// HostRuntimeBindings are supplied by the trusted host composition root. The
// identity strings bind every non-child component to the same profile; the
// handles/callbacks are retained privately by AdmissionCapability so callers
// cannot swap an unreviewed launcher or settlement observer after admission.
type HostRuntimeBindings struct {
	ChildExecutable        string
	NodeVersion            string
	ChildArgumentsProfile  string
	WorkingDirectory       string
	WorkerExposureHandle   *os.File
	WorkerExposure         VerifiedWorkerExposure
	WorkerExposureEvidence WorkerExposureEvidence
	// D09 source staging is intentionally separate from the generic consumer
	// exposure interface. The concrete Linux composition asserts these opaque
	// fields to the sessionrepo capability and D09-only source interfaces.
	D09Source               any
	D09SourceCapability     any
	D09SourceEvidence       D09WorkerSourceEvidence
	D09SourceHandle         *os.File
	PiRuntimeBundleRoot     string
	PiRuntimeBundleDigest   string
	PiRuntimeBundleHandle   *os.File
	WorkerRuntimeAssets     VerifiedWorkerRuntimeAssets
	RuntimeAssetEvidence    WorkerRuntimeAssetEvidence
	D09RuntimeAssetSource   any
	D09RuntimeAssetEvidence WorkerRuntimeAssetEvidence
	StdinProfile            string
	StdoutProfile           string
	ChildEnvironment        string
	IPCProfile              string
	TerminalProfile         string
	ContainmentProfile      string
	SettlementProfile       string
	ObservationProfile      string
	DescriptorProfile       string
	SourceProvenance        string
	OfflineVerifier         string
	ProviderProfile         string

	Launcher    ExecutableLauncher
	Descendant  DescendantSettler
	Arguments   []string
	Environment []string
	Stdin       io.Reader
	Terminal    io.Writer
	Observation ObservationSink
	Descriptors []InheritedDescriptor
}

func (b HostRuntimeBindings) validate(profile HostProfile) error {
	if b.ChildExecutable != profile.ChildExecutable || b.NodeVersion != profile.NodeVersion || b.ChildArgumentsProfile != profile.ChildArgumentsProfile ||
		DigestArguments(b.Arguments) != profile.ChildArgumentsDigest {
		return fmt.Errorf("%w: executable, Node version, argv, or argument digest differs from signed profile", ErrHostBindingsMissing)
	}
	workerSourceHandle := b.WorkerExposureHandle
	if b.D09Source != nil {
		workerSourceHandle = b.D09SourceHandle
	}
	if b.WorkingDirectory != profile.WorkingDirectory || workerSourceHandle == nil ||
		DigestBytes([]byte(b.WorkingDirectory)) != profile.WorkingDirectoryDigest {
		return fmt.Errorf("%w: namespace CWD or worker source handle differs from signed profile", ErrHostBindingsMissing)
	}
	if b.D09Source != nil || b.D09SourceCapability != nil || b.D09SourceEvidence != (D09WorkerSourceEvidence{}) ||
		b.D09SourceHandle != nil || b.D09RuntimeAssetSource != nil {
		if isNilBinding(b.D09Source) || isNilBinding(b.D09SourceCapability) ||
			b.D09SourceHandle == nil || b.D09SourceEvidence.validateShape() != nil || isNilBinding(b.D09RuntimeAssetSource) ||
			b.WorkerExposureHandle != nil || b.WorkerExposure != nil || b.WorkerExposureEvidence != (WorkerExposureEvidence{}) ||
			b.WorkerRuntimeAssets != nil || b.RuntimeAssetEvidence != (WorkerRuntimeAssetEvidence{}) ||
			b.D09RuntimeAssetEvidence.validateShape() != nil {
			return fmt.Errorf("%w: D09 source-stage binding is incomplete or aliases consumer-view semantics", ErrHostBindingsMissing)
		}
		if b.D09RuntimeAssetEvidence.BundleDigest != profile.PiRuntimeBundleDigest ||
			b.D09RuntimeAssetEvidence.NamespaceProfileDigest != profile.ContainmentProfileDigest {
			return fmt.Errorf("%w: D09 runtime source evidence differs from signed profile", ErrHostBindingsMissing)
		}
	} else if isNilBinding(b.WorkerExposure) || b.WorkerExposureEvidence.validateShape() != nil {
		return fmt.Errorf("%w: verified sealed worker consumer exposure differs from signed profile", ErrHostBindingsMissing)
	}
	if b.PiRuntimeBundleRoot != profile.PiRuntimeBundleRoot || b.PiRuntimeBundleDigest != profile.PiRuntimeBundleDigest ||
		b.PiRuntimeBundleHandle == nil || (b.D09Source == nil && (isNilBinding(b.WorkerRuntimeAssets) ||
		b.RuntimeAssetEvidence.validateShape() != nil || b.RuntimeAssetEvidence.BundleDigest != profile.PiRuntimeBundleDigest ||
		b.RuntimeAssetEvidence.NamespaceProfileDigest != profile.ContainmentProfileDigest)) {
		return fmt.Errorf("%w: read-only namespace runtime asset evidence differs from signed profile", ErrHostBindingsMissing)
	}
	if b.StdinProfile != profile.StdinProfile || isNilBinding(b.Stdin) ||
		DigestBytes([]byte(b.StdinProfile)) != profile.StdinDigest || b.ChildEnvironment != profile.ChildEnvironment ||
		DigestEnvironment(b.Environment) != profile.ChildEnvironmentDigest {
		return fmt.Errorf("%w: stdin or exact child environment differs from signed profile", ErrHostBindingsMissing)
	}
	if profile.Mode == ModeProduction && b.StdoutProfile != profile.StdoutProfile {
		return fmt.Errorf("%w: Pi RPC stdout profile differs from signed profile", ErrHostBindingsMissing)
	}
	if b.IPCProfile != profile.IPCProfile || b.TerminalProfile != profile.TerminalProfile ||
		b.ContainmentProfile != profile.ContainmentProfile || b.SettlementProfile != profile.SettlementProfile ||
		b.ObservationProfile != profile.ObservationProfile || b.DescriptorProfile != profile.DescriptorProfile ||
		b.SourceProvenance != profile.SourceProvenance || b.OfflineVerifier != profile.OfflineVerifier || b.ProviderProfile != profile.ProviderProfile {
		return fmt.Errorf("%w: host channel/profile identity differs from signed profile", ErrHostBindingsMissing)
	}
	if isNilBinding(b.Launcher) || isNilBinding(b.Descendant) || isNilBinding(b.Terminal) || isNilBinding(b.Observation) {
		return fmt.Errorf("%w: launcher, settlement, terminal, or observation binding is absent", ErrHostBindingsMissing)
	}
	if err := validatePiWorkerDescriptors(b.DescriptorProfile, b.Descriptors, workerSourceHandle, b.PiRuntimeBundleHandle); err != nil {
		return fmt.Errorf("%w: %v", ErrHostBindingsMissing, err)
	}
	workingDirectoryInfo, err := workerSourceHandle.Stat()
	if err != nil || !workingDirectoryInfo.IsDir() {
		return ErrHostBindingsMissing
	}
	bundleInfo, err := b.PiRuntimeBundleHandle.Stat()
	if err != nil || !bundleInfo.IsDir() {
		return ErrHostBindingsMissing
	}
	switch b.Descendant.(type) {
	case NoDescendants, *NoDescendants:
		return ErrHostBindingsMissing
	}
	return nil
}

// AdmissionCapability is intentionally opaque. Its zero value is invalid and
// it can only be produced by AdmitProduction after the trusted host verifier
// supplied a receipt matching every profile component.
type AdmissionCapability struct {
	profile HostProfile
	receipt HostAttestation
	runtime HostRuntimeBindings
	nonce   [32]byte
}

func (c *AdmissionCapability) ProfileDigest() string {
	if c == nil {
		return ""
	}
	return c.profile.Digest
}

func (c *AdmissionCapability) valid() bool {
	if c == nil || c.profile.Mode != ModeProduction || c.profile.Digest == "" ||
		isNilBinding(c.runtime.Launcher) || isNilBinding(c.runtime.Descendant) {
		return false
	}
	for _, value := range c.nonce {
		if value != 0 {
			return true
		}
	}
	return false
}

func (p HostProfile) Validate() error {
	if !validIdentity(p.ID) || !validDigest(p.Digest) || p.Mode == "" {
		return ErrHostProfileMissing
	}
	switch p.Mode {
	case ModeFixture:
		return p.validateFixture()
	case ModeProduction:
		// Continue through the production-only profile checks below.
	default:
		return ErrHostProfileMissing
	}
	if !validIdentity(p.ChildExecutable) || !filepath.IsAbs(p.ChildExecutable) ||
		!validDigest(p.ExecutableDigest) || !validIdentity(p.NodeVersion) || !validIdentity(p.ChildArgumentsProfile) ||
		!validDigest(p.ChildArgumentsDigest) || !validIdentity(p.WorkingDirectory) ||
		!filepath.IsAbs(p.WorkingDirectory) || !validDigest(p.WorkingDirectoryDigest) ||
		!validIdentity(p.StdinProfile) || !validDigest(p.StdinDigest) || !validIdentity(p.StdoutProfile) ||
		!validDigest(p.StdoutProfileDigest) || !validIdentity(p.ChildEnvironment) ||
		!validDigest(p.ChildEnvironmentDigest) || !validIdentity(p.IPCProfile) ||
		!validDigest(p.IPCProfileDigest) || !validIdentity(p.TerminalProfile) ||
		!validDigest(p.TerminalProfileDigest) || !validIdentity(p.ContainmentProfile) ||
		!validDigest(p.ContainmentProfileDigest) || !validDigest(p.CgroupRootDigest) || !validIdentity(p.SettlementProfile) ||
		!validDigest(p.SettlementProfileDigest) || !validIdentity(p.ObservationProfile) ||
		!validDigest(p.ObservationProfileDigest) || !validIdentity(p.DescriptorProfile) ||
		p.DescriptorProfile != PiWorkerDescriptorProfile ||
		!validDigest(p.DescriptorProfileDigest) || p.DescriptorProfileDigest != DigestBytes([]byte(PiWorkerDescriptorProfile)) ||
		!validIdentity(p.PiRuntimeBundleRoot) || !filepath.IsAbs(p.PiRuntimeBundleRoot) || !validDigest(p.PiRuntimeBundleDigest) ||
		!validIdentity(p.SourceProvenance) ||
		!validDigest(p.SourceProvenanceDigest) || !validIdentity(p.OfflineVerifier) ||
		!validDigest(p.OfflineVerifierDigest) || !validIdentity(p.ProviderProfile) ||
		!validDigest(p.ProviderProfileDigest) {
		return ErrHostProfileMissing
	}
	if runtime.GOOS == "linux" {
		if p.ChildExecutable != PiCLIProductionExecutable || p.NodeVersion != PiCLIProductionNodeVersion ||
			p.ChildArgumentsProfile != PiCLIProductionArgumentsProfile ||
			p.ChildArgumentsDigest != DigestArguments(productionPiCLIArguments()) ||
			p.StdinProfile != PiCLIProductionStdinProfile || p.StdinDigest != DigestBytes([]byte(PiCLIProductionStdinProfile)) ||
			p.StdoutProfile != PiCLIProductionStdoutProfile || p.StdoutProfileDigest != DigestBytes([]byte(PiCLIProductionStdoutProfile)) ||
			p.ChildEnvironment != PiCLIProductionEnvironmentProfile ||
			p.ChildEnvironmentDigest != DigestEnvironment(productionPiCLIEnvironment()) ||
			p.IPCProfile != PiCLIProductionIPCProfile || p.IPCProfileDigest != DigestBytes([]byte(PiCLIProductionIPCProfile)) ||
			p.TerminalProfile != PiCLIProductionTerminalProfile ||
			p.TerminalProfileDigest != DigestBytes([]byte(PiCLIProductionTerminalProfile)) ||
			p.PiRuntimeBundleRoot != PiCLIProductionRuntimeBundleRoot {
			return ErrHostProfileMissing
		}
	}
	return nil
}

func (p HostProfile) validateFixture() error {
	arguments, err := piinstall.GovernedRPCArgs("/proc/self/fd/9/src/tbound-cli-extension.ts")
	if runtime.GOOS != "linux" || err != nil ||
		p.ID != developerPiCLIProfileID || p.Digest != DigestBytes([]byte(developerPiCLIProfileID)) ||
		!validIdentity(p.ChildExecutable) || !filepath.IsAbs(p.ChildExecutable) || !validDigest(p.ExecutableDigest) ||
		p.NodeVersion != PiCLIProductionNodeVersion ||
		p.ChildArgumentsProfile != developerPiCLIArgumentsProfile || p.ChildArgumentsDigest != DigestArguments(arguments) ||
		!validIdentity(p.WorkingDirectory) || !filepath.IsAbs(p.WorkingDirectory) ||
		p.WorkingDirectoryDigest != DigestBytes([]byte(p.WorkingDirectory)) ||
		p.StdinProfile != PiCLIProductionStdinProfile || p.StdinDigest != DigestBytes([]byte(PiCLIProductionStdinProfile)) ||
		p.StdoutProfile != "" || p.StdoutProfileDigest != "" ||
		p.ChildEnvironment != developerPiCLIEnvironmentProfile || !validDigest(p.ChildEnvironmentDigest) ||
		p.IPCProfile != PiCLIProductionIPCProfile || p.IPCProfileDigest != DigestBytes([]byte(PiCLIProductionIPCProfile)) ||
		p.TerminalProfile != PiCLIProductionTerminalProfile || p.TerminalProfileDigest != DigestBytes([]byte(PiCLIProductionTerminalProfile)) ||
		p.ContainmentProfile != developerPiCLIContainmentProfile || p.ContainmentProfileDigest != DigestBytes([]byte(developerPiCLIContainmentProfile)) ||
		p.CgroupRootDigest != "" ||
		p.SettlementProfile != developerPiCLISettlementProfile || p.SettlementProfileDigest != DigestBytes([]byte(developerPiCLISettlementProfile)) ||
		p.ObservationProfile != developerPiCLIObservationProfile || p.ObservationProfileDigest != DigestBytes([]byte(developerPiCLIObservationProfile)) ||
		p.DescriptorProfile != PiWorkerDescriptorProfile || p.DescriptorProfileDigest != DigestBytes([]byte(PiWorkerDescriptorProfile)) ||
		!validIdentity(p.PiRuntimeBundleRoot) || !filepath.IsAbs(p.PiRuntimeBundleRoot) || !validDigest(p.PiRuntimeBundleDigest) ||
		p.SourceProvenance != developerPiCLISourceProvenance || p.SourceProvenanceDigest != DigestBytes([]byte(developerPiCLISourceProvenance)) ||
		p.OfflineVerifier != developerPiCLIOfflineVerifier || p.OfflineVerifierDigest != DigestBytes([]byte(developerPiCLIOfflineVerifier)) ||
		p.ProviderProfile != developerPiCLIProviderProfile || p.ProviderProfileDigest != DigestBytes([]byte(developerPiCLIProviderProfile)) {
		return ErrHostProfileMissing
	}
	return nil
}

func (a HostAttestation) Validate() error {
	if !validIdentity(a.HostID) || !validIdentity(a.ProfileID) ||
		!validDigest(a.ProfileDigest) || !validDigest(a.ExecutableDigest) || !validIdentity(a.NodeVersion) ||
		!validDigest(a.ChildArgumentsDigest) || !validDigest(a.WorkingDirectoryDigest) ||
		!validDigest(a.StdinDigest) || !validDigest(a.StdoutDigest) ||
		!validDigest(a.ChildEnvironmentDigest) || !validDigest(a.IPCProfileDigest) ||
		!validDigest(a.TerminalDigest) || !validDigest(a.ContainmentDigest) || !validDigest(a.CgroupRootDigest) ||
		!validDigest(a.SettlementDigest) || !validDigest(a.ObservationDigest) ||
		!validDigest(a.DescriptorDigest) || !validDigest(a.PiRuntimeBundleDigest) ||
		!validDigest(a.SourceDigest) ||
		!validDigest(a.OfflineVerifierDigest) || !validDigest(a.ProviderProfileDigest) {
		return ErrHostAttestationMissing
	}
	return nil
}

// ValidateHostAdmission checks only structural binding. It is not an
// authorization operation; production callers must use AdmitProduction.
func ValidateHostAdmission(profile HostProfile, attestation HostAttestation) error {
	if profile.Mode == ModeFixture {
		return ErrHostAdmissionRefused
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	if profile.Mode != ModeProduction {
		return ErrHostAdmissionRefused
	}
	if err := attestation.Validate(); err != nil {
		return err
	}
	if attestation.ProfileID != profile.ID || attestation.ProfileDigest != profile.Digest ||
		attestation.ExecutableDigest != profile.ExecutableDigest || attestation.NodeVersion != profile.NodeVersion ||
		attestation.ChildArgumentsDigest != profile.ChildArgumentsDigest ||
		attestation.WorkingDirectoryDigest != profile.WorkingDirectoryDigest ||
		attestation.StdinDigest != profile.StdinDigest ||
		attestation.StdoutDigest != profile.StdoutProfileDigest ||
		attestation.ChildEnvironmentDigest != profile.ChildEnvironmentDigest ||
		attestation.IPCProfileDigest != profile.IPCProfileDigest ||
		attestation.TerminalDigest != profile.TerminalProfileDigest ||
		attestation.ContainmentDigest != profile.ContainmentProfileDigest ||
		attestation.CgroupRootDigest != profile.CgroupRootDigest ||
		attestation.SettlementDigest != profile.SettlementProfileDigest ||
		attestation.ObservationDigest != profile.ObservationProfileDigest ||
		attestation.DescriptorDigest != profile.DescriptorProfileDigest ||
		attestation.PiRuntimeBundleDigest != profile.PiRuntimeBundleDigest ||
		attestation.SourceDigest != profile.SourceProvenanceDigest ||
		attestation.OfflineVerifierDigest != profile.OfflineVerifierDigest ||
		attestation.ProviderProfileDigest != profile.ProviderProfileDigest {
		return ErrHostProfileMismatch
	}
	return nil
}

// AdmitProduction obtains the receipt from the trusted host loader and then
// creates an opaque capability. A syntactically valid receipt passed directly
// to ValidateHostAdmission cannot create a child capability.
func AdmitProduction(ctx context.Context, profile HostProfile, attestor HostAttestor) (*AdmissionCapability, error) {
	if ctx == nil || isNilBinding(attestor) {
		return nil, ErrHostAttestationMissing
	}
	if profile.Mode == ModeFixture {
		return nil, ErrHostAdmissionRefused
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if profile.Mode != ModeProduction {
		return nil, ErrHostAdmissionRefused
	}
	verifier, ok := attestor.(TrustedHostVerifier)
	if !ok || isNilBinding(verifier) {
		return nil, ErrHostBindingsMissing
	}
	receipt, err := attestor.Attest(ctx, profile)
	if err != nil {
		return nil, fmt.Errorf("obtain native Pi host attestation: %w", err)
	}
	if err := ValidateHostAdmission(profile, receipt); err != nil {
		return nil, err
	}
	runtime, err := verifier.BindRuntime(ctx, profile)
	if err != nil {
		return nil, fmt.Errorf("obtain native Pi host runtime bindings: %w", err)
	}
	// Own a private copy of mutable vector data before validation/capability
	// publication; the verifier's retained slice cannot rewrite the plan later.
	runtime.Arguments = append([]string(nil), runtime.Arguments...)
	runtime.Environment = append([]string{}, runtime.Environment...)
	runtime.Descriptors = cloneInheritedDescriptors(runtime.Descriptors)
	if err := runtime.validate(profile); err != nil {
		return nil, err
	}
	capability := &AdmissionCapability{profile: profile, receipt: receipt, runtime: runtime}
	if _, err := rand.Read(capability.nonce[:]); err != nil {
		return nil, fmt.Errorf("create opaque native host admission capability: %w", err)
	}
	return capability, nil
}

func sameBinding(left, right any) bool {
	if isNilBinding(left) || isNilBinding(right) {
		return false
	}
	leftValue, rightValue := reflect.ValueOf(left), reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() {
		return false
	}
	if leftValue.Type().Comparable() {
		return leftValue.Interface() == rightValue.Interface()
	}
	switch leftValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Pointer, reflect.UnsafePointer:
		return leftValue.Pointer() == rightValue.Pointer()
	default:
		return false
	}
}

func isNilBinding(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func DigestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func DigestArguments(arguments []string) string {
	encoded, err := json.Marshal(append([]string{}, arguments...))
	if err != nil {
		return ""
	}
	return DigestBytes(encoded)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	encoded := value[len("sha256:"):]
	if encoded != strings.ToLower(encoded) {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}
