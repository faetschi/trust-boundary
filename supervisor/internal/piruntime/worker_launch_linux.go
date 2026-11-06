//go:build linux

// worker_launch_linux.go: Pi worker launch: development fixture evidence, launch bindings, environment and launchPiCLIWorker.

package piruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"tbound/supervisor/internal/piinstall"
)

func developmentWorkerSourceEvidence(directory *os.File) (PiWorkerBootstrap, error) {
	if directory == nil {
		return PiWorkerBootstrap{}, errors.New("development worker source evidence requires the selected fixture directory")
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return PiWorkerBootstrap{}, errors.New("development worker source handle is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return PiWorkerBootstrap{}, errors.New("development worker source directory identity is unavailable")
	}
	device := uint64(stat.Dev)
	if device == 0 {
		return PiWorkerBootstrap{}, errors.New("development worker source directory device is unavailable")
	}
	mountID, err := descriptorMountID(directory.Fd())
	if err != nil {
		return PiWorkerBootstrap{}, fmt.Errorf("read development fixture directory mount identity: %w", err)
	}
	manifestDigest, err := digestDevelopmentFixtureDirectory(directory, info)
	if err != nil {
		return PiWorkerBootstrap{}, err
	}
	treeDigest := DigestBytes([]byte("DEVELOPMENT/UNKNOWN/fixture-tree/v1\x00" + manifestDigest))
	viewIdentity := DigestBytes([]byte(fmt.Sprintf("DEVELOPMENT/UNKNOWN/fixture-view/v1\x00%d:%d:%d", device, stat.Ino, mountID)))
	generationID := fmt.Sprintf("DEVELOPMENT/UNKNOWN/fixture-%d-%d", device, stat.Ino)
	return PiWorkerBootstrap{
		SourceBindingDigest:    DigestBytes([]byte("DEVELOPMENT/UNKNOWN/source-binding/v1\x00" + treeDigest)),
		SourceProvenanceDigest: DigestBytes([]byte("DEVELOPMENT/UNKNOWN/source-provenance/v1\x00" + treeDigest)),
		SourceGenerationID:     generationID,
		SourceTreeDigest:       treeDigest,
		SourceManifestDigest:   manifestDigest,
		WorkerViewIdentity:     viewIdentity,
		WorkerMountID:          mountID,
	}, nil
}

func digestDevelopmentFixtureDirectory(directory *os.File, rootInfo os.FileInfo) (string, error) {
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", directory.Fd()))
	if err != nil || !filepath.IsAbs(rootPath) || strings.HasSuffix(rootPath, " (deleted)") {
		return "", errors.New("development worker fixture directory path is unavailable")
	}
	pathInfo, err := os.Lstat(rootPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(rootInfo, pathInfo) {
		return "", errors.New("development worker fixture directory path differs from its held descriptor")
	}
	var manifest strings.Builder
	var entries uint64
	var contentBytes int64
	err = filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 4096 {
			return errors.New("development worker fixture entry budget exhausted")
		}
		relative, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if name == "." {
			name = ""
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			_, err = fmt.Fprintf(&manifest, "d\x00%s\x00%o\n", name, info.Mode().Perm())
		case info.Mode().IsRegular():
			remaining := (16 << 20) - contentBytes
			if remaining < 0 {
				return errors.New("development worker fixture byte budget exhausted")
			}
			data, readErr := readRegularBounded(path, remaining)
			if readErr != nil {
				return readErr
			}
			contentBytes += int64(len(data))
			_, err = fmt.Fprintf(&manifest, "f\x00%s\x00%o\x00%d\x00%s\n", name, info.Mode().Perm(), len(data), DigestBytes(data))
		case info.Mode()&os.ModeSymlink != 0:
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			if len(target) > 4096 {
				return errors.New("development worker fixture symlink target exceeds bound")
			}
			_, err = fmt.Fprintf(&manifest, "l\x00%s\x00%s\n", name, target)
		default:
			return errors.New("development worker fixture contains an unsupported filesystem object")
		}
		if err != nil {
			return err
		}
		if manifest.Len() > 16<<20 {
			return errors.New("development worker fixture manifest exceeds bound")
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest development worker fixture directory: %w", err)
	}
	return DigestBytes([]byte("DEVELOPMENT/UNKNOWN/fixture-manifest/v1\x00" + manifest.String())), nil
}

type PiCLIWorker struct {
	Session     *Session
	Channels    *PiWorkerChannels
	development bool
	rpc         *PiRPCClient

	// launchProof is the captured, validated per-launch D09 TCB proof, bound to
	// this worker's exact process. It is nil for development workers.
	launchProof any

	mu                sync.Mutex
	leases            map[string]*workerPromptLease
	closed            bool
	events            chan PiWorkerEvent
	readerDone        chan struct{}
	rpcReader         io.Reader
	eventsErr         error
	eventsErrReturned bool
	eventsDrop        uint64
	eventsBytes       uint64
	gapReturned       bool

	stdoutCloseMu  sync.Mutex
	stdoutCloser   io.Closer
	stdoutCloseErr error
	stdoutClosed   bool
}

type workerPromptLease struct {
	lease *Lease
	stop  func() bool
}

// Admission returns the session's close-and-drain gate. Effect supervisors
// should acquire a lease before executing a proposal and release it on result.
func (w *PiCLIWorker) Admission() *Admission {
	if w == nil || w.Session == nil {
		return nil
	}
	return w.Session.Admission()
}

// PID is the actual direct Node worker pid; it is diagnostic/process-scope
// binding only and does not establish descendant settlement.
func (w *PiCLIWorker) PID() int {
	if w == nil || w.Session == nil || w.Session.child == nil {
		return 0
	}
	return w.Session.child.PID()
}

// D09LaunchProof returns the captured, validated D09 TCB proof for this worker's
// exact launch, bound to the launched command identity. Production callers MUST
// journal it before releasing any prompt or effect; a missing proof returns
// false and no prompt may be released. Development workers always return false.
func (w *PiCLIWorker) D09LaunchProof() (D09TCBProof, bool) {
	if w == nil || w.launchProof == nil {
		return D09TCBProof{}, false
	}
	proof, ok := w.launchProof.(D09TCBProof)
	if !ok || proof.LeaderPID <= 0 || proof.LauncherProcessPID <= 0 || proof.LauncherProcessPID != w.PID() || proof.ContainerName == "" ||
		!validDigest(proof.ImageDigest) || proof.TemplateDigest == "" ||
		proof.Schema != "tbound-pi-entrypoint/v1" || !proof.ConsumerMountsVerified ||
		!proof.RawSourceFDsClosed || proof.WorkerMountID == 0 || proof.RuntimeMountID == 0 ||
		proof.ConsumerWorkerMountID != proof.WorkerMountID || proof.ConsumerRuntimeMountID != proof.RuntimeMountID ||
		!validIdentity(proof.SourceGenerationID) || !validDigest(proof.SourceTreeDigest) ||
		!validDigest(proof.SourceManifestDigest) || !validDigest(proof.SourceBindingDigest) ||
		!validDigest(proof.SourceProvenanceDigest) || !validDigest(proof.SourceIdentityDigest) ||
		!validBareAuditHead(proof.SourceStoreAuditHead) || proof.SourceStoreAuditCount == 0 ||
		proof.SourceDevice == 0 || proof.SourceInode == 0 || proof.SourceMountID == 0 {
		return D09TCBProof{}, false
	}
	return proof, true
}

func LaunchPiCLIWorker(ctx context.Context, capability *AdmissionCapability, exposure VerifiedWorkerExposure, assets VerifiedWorkerRuntimeAssets, channels *PiWorkerChannels, bootstrap PiWorkerBootstrap) (*PiCLIWorker, error) {
	return nil, errors.New("production Pi launch refused: admitted profiles must attest the official Pi CLI executable, RPC arguments, extension and transitive closure")

}

// LaunchD09PiCLIWorker launches from a source-stage binding. The Store-issued
// capability and source evidence are revalidated without treating the host O_PATH
// descriptor as a consumer view; StartVerified returns only after the trusted
// entrypoint has proved the distinct read-only consumer mount and replaced the
// raw source FD. Bootstrap/readiness is released only after that proof exists.
func LaunchD09PiCLIWorker(ctx context.Context, capability *AdmissionCapability,
	channels *D09PiWorkerChannels, bootstrap PiWorkerBootstrap) (*PiCLIWorker, error) {
	reject := func(err error) (*PiCLIWorker, error) {
		if channels == nil {
			return nil, err
		}
		if retainD09LaunchOwners(err) {
			return nil, err
		}
		return nil, errors.Join(err, channels.Close())
	}
	if channels == nil || channels.channels == nil {
		return reject(errors.New("D09 Pi launch refused: source-stage channels are required"))
	}
	if ctx == nil {
		return reject(errors.New("D09 Pi launch refused: context is required"))
	}
	if err := ctx.Err(); err != nil {
		return reject(fmt.Errorf("D09 Pi launch refused: %w", err))
	}
	if capability == nil || !capability.valid() ||
		capability.profile.Mode != ModeProduction ||
		capability.profile.ContainmentProfile != D09ContainmentProfile ||
		capability.profile.SettlementProfile != D09SettlementProfile {
		return reject(fmt.Errorf("D09 Pi launch refused: %w", ErrChildCapabilityMissing))
	}
	profile, runtime := capability.profile, capability.runtime
	if err := profile.Validate(); err != nil {
		return reject(fmt.Errorf("D09 Pi launch refused: %w", err))
	}
	if _, ok := runtime.Launcher.(*D09PodmanCrunLauncher); !ok {
		return reject(fmt.Errorf("D09 Pi launch refused: %w", ErrProductionLauncherUnavailable))
	}
	if err := runtime.validate(profile); err != nil {
		return reject(fmt.Errorf("D09 Pi launch refused: %w", err))
	}
	if err := validateD09PiChannelBindings(channels, runtime); err != nil {
		return reject(fmt.Errorf("D09 Pi launch refused: %w", err))
	}
	if err := verifyD09HostRuntimeSources(ctx, runtime); err != nil {
		return reject(fmt.Errorf("D09 Pi launch refused: %w", err))
	}
	if err := bindD09BootstrapSourceEvidence(&bootstrap, runtime.D09SourceEvidence); err != nil {
		return reject(fmt.Errorf("D09 Pi launch refused: %w", err))
	}

	return launchPiCLIWorker(ctx, profile, runtime, capability, channels.channels, bootstrap, false, "")
}

func validateD09PiChannelBindings(channels *D09PiWorkerChannels, runtime HostRuntimeBindings) error {
	if channels == nil || channels.channels == nil {
		return fmt.Errorf("%w: D09 source-stage channels are missing", ErrHostBindingsMissing)
	}
	channels.channels.mu.Lock()
	defer channels.channels.mu.Unlock()
	if channels.channels.closed || channels.channels.childClosed ||
		!channels.channels.ownedSourceFDs ||
		isNilBinding(channels.source) ||
		isNilBinding(channels.sourceCapability) ||
		isNilBinding(channels.assets) ||
		!sameBinding(channels.source, runtime.D09Source) ||
		!sameBinding(channels.sourceCapability, runtime.D09SourceCapability) ||
		!sameBinding(channels.assets, runtime.D09RuntimeAssetSource) ||
		!sameD09SourceEvidence(runtime.D09SourceEvidence, channels.sourceEvidence) ||
		channels.assetEvidence != runtime.D09RuntimeAssetEvidence ||
		channels.channels.workerExposure != nil ||
		channels.channels.exposureEvidence != (WorkerExposureEvidence{}) ||
		channels.channels.workerAssets != nil ||
		channels.channels.assetEvidence != (WorkerRuntimeAssetEvidence{}) ||
		!sameBinding(channels.channels.workingDirectory, runtime.D09SourceHandle) ||
		!sameBinding(channels.channels.runtimeBundle, runtime.PiRuntimeBundleHandle) ||
		!sameBinding(channels.channels.ChildStdin(), runtime.Stdin) ||
		!sameInheritedDescriptors(channels.channels.DescriptorBindings(), runtime.Descriptors) {
		return fmt.Errorf("%w: source-stage channels do not match the admitted D09 handles, evidence, and descriptors", ErrHostBindingsMissing)
	}
	return nil
}

// Bind only Store-issued source evidence here. Consumer view identity and mount
// ID become available after the entrypoint proves its distinct read-only mount.
func bindD09BootstrapSourceEvidence(bootstrap *PiWorkerBootstrap, evidence D09WorkerSourceEvidence) error {
	if bootstrap == nil || evidence.validateShape() != nil ||
		(bootstrap.SourceBindingDigest != "" && bootstrap.SourceBindingDigest != evidence.SourceBindingDigest) ||
		(bootstrap.SourceProvenanceDigest != "" && bootstrap.SourceProvenanceDigest != evidence.ProvenanceDigest) ||
		(bootstrap.SourceGenerationID != "" && bootstrap.SourceGenerationID != evidence.GenerationID) ||
		(bootstrap.SourceTreeDigest != "" && bootstrap.SourceTreeDigest != evidence.TreeDigest) ||
		(bootstrap.SourceManifestDigest != "" && bootstrap.SourceManifestDigest != evidence.ManifestDigest) ||
		bootstrap.WorkerViewIdentity != "" || bootstrap.WorkerMountID != 0 {
		return fmt.Errorf("%w: D09 bootstrap source or consumer-view identity is inconsistent", ErrHostBindingsMissing)
	}
	bootstrap.SourceBindingDigest = evidence.SourceBindingDigest
	bootstrap.SourceProvenanceDigest = evidence.ProvenanceDigest
	bootstrap.SourceGenerationID = evidence.GenerationID
	bootstrap.SourceTreeDigest = evidence.TreeDigest
	bootstrap.SourceManifestDigest = evidence.ManifestDigest
	return nil
}

// bindD09ConsumerViewProof accepts consumer identity only when it matches the
// launcher-captured entrypoint proof and the Store/runtime evidence already
// bound to the admitted runtime. The production caller supplies the proof
// returned by the exact launched Process; this helper does not create authority.
func bindD09ConsumerViewProof(bootstrap *PiWorkerBootstrap, runtime HostRuntimeBindings,
	launcherPID int, proof D09TCBProof) error {
	source, assets := runtime.D09SourceEvidence, runtime.D09RuntimeAssetEvidence
	if bootstrap == nil || source.validateShape() != nil || assets.validateShape() != nil ||
		launcherPID <= 0 || proof.LauncherProcessPID != launcherPID ||
		proof.Schema != "tbound-pi-entrypoint/v1" || proof.ContainerName == "" ||
		proof.LeaderPID <= 0 || proof.ContainerLeaderPID != 1 ||
		!validDigest(proof.ImageDigest) || !strings.HasSuffix(proof.ImageRef, "@"+proof.ImageDigest) ||
		proof.TemplateDigest != assets.NamespaceProfileDigest ||
		!proof.ConsumerMountsVerified || !proof.RawSourceFDsClosed ||
		proof.WorkerMountID == 0 || proof.RuntimeMountID == 0 ||
		proof.ConsumerWorkerMountID != proof.WorkerMountID ||
		proof.ConsumerRuntimeMountID != proof.RuntimeMountID ||
		proof.WorkerDevice != source.SourceDevice || proof.WorkerInode != source.SourceInode ||
		proof.SourceGenerationID != source.GenerationID || proof.SourceTreeDigest != source.TreeDigest ||
		proof.SourceManifestDigest != source.ManifestDigest ||
		proof.SourceBindingDigest != source.SourceBindingDigest ||
		proof.SourceProvenanceDigest != source.ProvenanceDigest ||
		proof.SourceIdentityDigest != source.SourceIdentityDigest ||
		proof.SourceStoreAuditHead != source.StoreAuditHead ||
		proof.SourceStoreAuditCount != source.StoreAuditRecordCount ||
		proof.SourceDevice != source.SourceDevice || proof.SourceInode != source.SourceInode ||
		proof.SourceMountID != source.SourceMountID ||
		proof.RuntimeDevice != assets.Device || proof.RuntimeInode != assets.Inode ||
		bootstrap.SourceBindingDigest != source.SourceBindingDigest ||
		bootstrap.SourceProvenanceDigest != source.ProvenanceDigest ||
		bootstrap.SourceGenerationID != source.GenerationID ||
		bootstrap.SourceTreeDigest != source.TreeDigest ||
		bootstrap.SourceManifestDigest != source.ManifestDigest ||
		bootstrap.WorkerViewIdentity != "" || bootstrap.WorkerMountID != 0 {
		return fmt.Errorf("%w: D09 entrypoint consumer proof does not match the launched process and admitted source/runtime evidence", ErrHostBindingsMissing)
	}
	bootstrap.WorkerViewIdentity = DigestBytes([]byte(fmt.Sprintf("tbound-d09-consumer-view/v1\x00%d:%d:%d",
		proof.WorkerDevice, proof.WorkerInode, proof.ConsumerWorkerMountID)))
	bootstrap.WorkerMountID = proof.ConsumerWorkerMountID
	return nil
}

// DeveloperPiCLIPlan describes a real official Pi CLI subprocess and an
// isolated extension source snapshot. No adapter node_modules are accepted.
type DeveloperPiCLIPlan struct {
	PiPath, PiSHA256, NodePath, NodeVersion string
	BundleRoot, BundleDigest, ExtensionPath string
	BundleHandle, WorkingHandle             *os.File
	WorkingDir, Home, TempDir               string
	SessionID, WorkflowID, ConversationID   string
}

func StartDevelopmentPiCLIWorker(ctx context.Context, plan DeveloperPiCLIPlan) (*PiCLIWorker, error) {
	if ctx == nil || !filepath.IsAbs(plan.PiPath) || !validDigest(plan.PiSHA256) || !filepath.IsAbs(plan.NodePath) ||
		!filepath.IsAbs(plan.BundleRoot) || !filepath.IsAbs(plan.ExtensionPath) || !filepath.IsAbs(plan.WorkingDir) ||
		!filepath.IsAbs(plan.Home) || !filepath.IsAbs(plan.TempDir) || plan.BundleHandle == nil || plan.WorkingHandle == nil {
		return nil, errors.New("Pi CLI fixture requires explicit executable, extension, private directory paths, and held handles")
	}
	if plan.ExtensionPath != filepath.Join(plan.BundleRoot, "src", "tbound-cli-extension.ts") ||
		!validIdentity(plan.NodeVersion) || !validIdentity(plan.SessionID) || !validIdentity(plan.WorkflowID) || !validIdentity(plan.ConversationID) {
		return nil, errors.New("Pi CLI fixture plan is incomplete")
	}
	if err := validatePiCLIEnvironment(piCLIEnvironment(plan)); err != nil {
		return nil, err
	}
	channels, err := newPiWorkerChannels(plan.WorkingHandle, plan.BundleHandle)
	if err != nil {
		return nil, err
	}
	bundleDigest, err := DigestDeveloperPiCLIBundle(plan.BundleHandle)
	if err != nil {
		_ = channels.Close()
		return nil, err
	}
	if plan.BundleDigest != "" && plan.BundleDigest != bundleDigest {
		_ = channels.Close()
		return nil, errors.New("Pi CLI extension bundle digest differs from explicit plan")
	}
	profile := developmentPiCLIProfile(plan, bundleDigest, channels)
	bootstrap := PiWorkerBootstrap{SessionID: plan.SessionID, WorkflowID: plan.WorkflowID, ConversationID: plan.ConversationID}
	sourceEvidence, err := developmentWorkerSourceEvidence(plan.WorkingHandle)
	if err != nil {
		_ = channels.Close()
		return nil, err
	}
	bootstrap.SourceBindingDigest = sourceEvidence.SourceBindingDigest
	bootstrap.SourceProvenanceDigest = sourceEvidence.SourceProvenanceDigest
	bootstrap.SourceGenerationID = sourceEvidence.SourceGenerationID
	bootstrap.SourceTreeDigest = sourceEvidence.SourceTreeDigest
	bootstrap.SourceManifestDigest = sourceEvidence.SourceManifestDigest
	bootstrap.WorkerViewIdentity = sourceEvidence.WorkerViewIdentity
	bootstrap.WorkerMountID = sourceEvidence.WorkerMountID
	runtime := developmentPiCLIWorkerBindings(profile, plan, channels)
	return launchPiCLIWorker(ctx, profile, runtime, nil, channels, bootstrap, true, plan.PiPath)
}

func developmentPiCLIProfile(plan DeveloperPiCLIPlan, bundleDigest string, channels *PiWorkerChannels) HostProfile {
	env := piCLIEnvironment(plan)
	args, _ := piinstall.GovernedRPCArgs("/proc/self/fd/9/src/tbound-cli-extension.ts")
	profile := HostProfile{
		ID: developerPiCLIProfileID, Digest: DigestBytes([]byte(developerPiCLIProfileID)),
		Mode: ModeFixture, ChildExecutable: plan.PiPath, ExecutableDigest: plan.PiSHA256, NodeVersion: plan.NodeVersion,
		ChildArgumentsProfile: developerPiCLIArgumentsProfile, ChildArgumentsDigest: DigestArguments(args),
		WorkingDirectory: plan.WorkingDir, WorkingDirectoryDigest: DigestBytes([]byte(plan.WorkingDir)),
		StdinProfile: "pi-rpc-jsonl-stdin/v0.87.1", StdinDigest: DigestBytes([]byte("pi-rpc-jsonl-stdin/v0.87.1")),
		ChildEnvironment: developerPiCLIEnvironmentProfile, ChildEnvironmentDigest: DigestEnvironment(env),
		IPCProfile: "tbound-ipc/v1/fd6", IPCProfileDigest: DigestBytes([]byte("tbound-ipc/v1/fd6")),
		TerminalProfile: "pi-rpc-no-terminal/v1", TerminalProfileDigest: DigestBytes([]byte("pi-rpc-no-terminal/v1")),
		ContainmentProfile: developerPiCLIContainmentProfile, ContainmentProfileDigest: DigestBytes([]byte(developerPiCLIContainmentProfile)),
		SettlementProfile: developerPiCLISettlementProfile, SettlementProfileDigest: DigestBytes([]byte(developerPiCLISettlementProfile)),
		ObservationProfile: developerPiCLIObservationProfile, ObservationProfileDigest: DigestBytes([]byte(developerPiCLIObservationProfile)),
		DescriptorProfile: PiWorkerDescriptorProfile, DescriptorProfileDigest: DigestBytes([]byte(PiWorkerDescriptorProfile)),
		PiRuntimeBundleRoot: plan.BundleRoot, PiRuntimeBundleDigest: bundleDigest,
		SourceProvenance: developerPiCLISourceProvenance, SourceProvenanceDigest: DigestBytes([]byte(developerPiCLISourceProvenance)),
		OfflineVerifier: developerPiCLIOfflineVerifier, OfflineVerifierDigest: DigestBytes([]byte(developerPiCLIOfflineVerifier)),
		ProviderProfile: developerPiCLIProviderProfile, ProviderProfileDigest: DigestBytes([]byte(developerPiCLIProviderProfile)),
	}
	_ = channels
	return profile
}

func developmentPiCLIWorkerBindings(profile HostProfile, plan DeveloperPiCLIPlan, channels *PiWorkerChannels) HostRuntimeBindings {
	args, _ := piinstall.GovernedRPCArgs("/proc/self/fd/9/src/tbound-cli-extension.ts")
	return HostRuntimeBindings{ChildExecutable: profile.ChildExecutable, NodeVersion: profile.NodeVersion,
		ChildArgumentsProfile: profile.ChildArgumentsProfile, WorkingDirectory: profile.WorkingDirectory,
		WorkerExposureHandle: plan.WorkingHandle, PiRuntimeBundleRoot: profile.PiRuntimeBundleRoot,
		PiRuntimeBundleDigest: profile.PiRuntimeBundleDigest, PiRuntimeBundleHandle: plan.BundleHandle,
		StdinProfile: profile.StdinProfile, ChildEnvironment: profile.ChildEnvironment, IPCProfile: profile.IPCProfile,
		TerminalProfile: profile.TerminalProfile, ContainmentProfile: profile.ContainmentProfile,
		SettlementProfile: profile.SettlementProfile, ObservationProfile: profile.ObservationProfile,
		DescriptorProfile: profile.DescriptorProfile, SourceProvenance: profile.SourceProvenance,
		OfflineVerifier: profile.OfflineVerifier, ProviderProfile: profile.ProviderProfile,
		Arguments: args, Environment: piCLIEnvironment(plan), Stdin: channels.ChildStdin(), Terminal: io.Discard,
		Observation: discardObservationSink{}, Descriptors: channels.DescriptorBindings()}
}

func piCLIEnvironment(plan DeveloperPiCLIPlan) []string {
	return []string{"HOME=" + plan.Home, "TMPDIR=" + plan.TempDir, "LANG=C.UTF-8", "PI_OFFLINE=1",
		"PATH=" + filepath.Dir(plan.NodePath), "TBOUND_PI_CLI_ENTRY=" + plan.PiPath}
}

func validatePiCLIEnvironment(environment []string) error {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return errors.New("Pi CLI environment entry is malformed")
		}
		values[key] = value
	}
	if len(values) != 6 || values["PI_OFFLINE"] != "1" || values["LANG"] != "C.UTF-8" ||
		!filepath.IsAbs(values["HOME"]) || !filepath.IsAbs(values["TMPDIR"]) || !filepath.IsAbs(values["PATH"]) || !filepath.IsAbs(values["TBOUND_PI_CLI_ENTRY"]) {
		return errors.New("Pi CLI environment must be exactly private HOME/TMPDIR, fixed PATH, entry path, LANG and PI_OFFLINE")
	}
	for _, name := range []string{"HOME", "TMPDIR"} {
		info, err := os.Lstat(values[name])
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("Pi CLI %s is not an existing private mode-0700 directory", name)
		}
	}
	return nil
}

func developmentPiCLIRuntimeLauncher(ctx context.Context, plan DeveloperPiCLIPlan, profile HostProfile, runtime HostRuntimeBindings) (*Process, error) {
	if ctx == nil || plan.PiPath != runtime.ChildExecutable || !filepath.IsAbs(plan.PiPath) {
		return nil, errors.New("Pi CLI path differs from fixture plan")
	}
	if err := validatePiCLIEnvironment(runtime.Environment); err != nil {
		return nil, err
	}
	bundleDigest, err := DigestDeveloperPiCLIBundle(runtime.PiRuntimeBundleHandle)
	if err != nil || bundleDigest != profile.PiRuntimeBundleDigest {
		return nil, errors.Join(ErrRuntimeBundleRefused, err)
	}
	cli, err := openVerifiedPiCLIEntry(plan.PiPath, plan.PiSHA256)
	if err != nil {
		return nil, err
	}
	if !tryAcquireSettlementWorker() {
		_ = cli.Close()
		return nil, ErrSettlementCapacity
	}
	command := exec.CommandContext(ctx, "/proc/self/fd/3", runtime.Arguments...)
	command.Dir, command.Env, command.Stdin = runtime.WorkingDirectory, append([]string{}, runtime.Environment...), runtime.Stdin
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = cli.Close()
		releaseSettlementWorker()
		return nil, err
	}
	command.Stderr = io.Discard
	command.ExtraFiles = []*os.File{cli}
	for _, descriptor := range cloneInheritedDescriptors(runtime.Descriptors) {
		command.ExtraFiles = append(command.ExtraFiles, descriptor.File)
	}
	if err := command.Start(); err != nil {
		_ = cli.Close()
		releaseSettlementWorker()
		return nil, err
	}
	_ = cli.Close()
	process := newProcess(command, developmentUnknownSettler{}, nil, true)
	process.stdout = stdout
	return process, nil
}

// openVerifiedPiCLIEntry verifies the exact executable JavaScript entrypoint
// selected by the CLI installation. Node itself remains independently pinned
// and validated as a native ELF binary by DevelopmentNodeVersion.
func openVerifiedPiCLIEntry(path, expectedDigest string) (*os.File, error) {
	if !filepath.IsAbs(path) || !validDigest(expectedDigest) {
		return nil, ErrLinuxLauncherRefused
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open Pi CLI entrypoint without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || info.Size() < 20 {
		_ = file.Close()
		return nil, errors.New("Pi CLI entrypoint is not a regular executable script")
	}
	var shebang [20]byte
	if _, err := file.ReadAt(shebang[:], 0); err != nil || !bytes.HasPrefix(shebang[:], []byte("#!/usr/bin/env node")) {
		_ = file.Close()
		return nil, errors.New("Pi CLI entrypoint does not use the pinned Node interpreter")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		_ = file.Close()
		return nil, errors.New("held Pi CLI entrypoint digest differs from the admitted profile")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validatePiCLIWorkerRPCProfile(profile HostProfile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	switch profile.ChildArgumentsProfile {
	case PiCLIProductionArgumentsProfile, developerPiCLIArgumentsProfile:
		return nil
	default:
		return errors.New("Pi worker requires the fixed official CLI RPC profile")
	}
}

func newPiCLIWorkerRPC(profile HostProfile, stdout io.Reader, stdin io.Writer) (*PiRPCClient, error) {
	if err := validatePiCLIWorkerRPCProfile(profile); err != nil {
		return nil, err
	}
	return NewPiRPCClient(stdout, stdin)
}

func launchPiCLIWorker(ctx context.Context, profile HostProfile, runtime HostRuntimeBindings, capability *AdmissionCapability, channels *PiWorkerChannels, bootstrap PiWorkerBootstrap, development bool, nodePath string) (resultWorker *PiCLIWorker, resultErr error) {
	if ctx == nil || channels == nil {
		return nil, errors.New("governed Pi worker requires context and channels")
	}
	keepChannels := false
	defer func() {
		if !keepChannels {
			if runtime.D09Source != nil && retainD09LaunchOwners(resultErr) {
				// The caller retains channels/source/capability with its cleanup
				// stack until a later exact scope settlement.
				return
			}
			resultErr = errors.Join(resultErr, channels.Close())
		}
	}()
	if err := validatePiCLIWorkerRPCProfile(profile); err != nil {
		return nil, err
	}
	if !validDigest(bootstrap.SourceBindingDigest) || !validDigest(bootstrap.SourceProvenanceDigest) || !validIdentity(bootstrap.SessionID) || !validIdentity(bootstrap.WorkflowID) || !validIdentity(bootstrap.ConversationID) {
		return nil, errors.New("Pi worker bootstrap identity/source binding is invalid")
	}
	if profile.DescriptorProfile != PiWorkerDescriptorProfile || profile.DescriptorProfileDigest != DigestBytes([]byte(PiWorkerDescriptorProfile)) ||
		profile.PiRuntimeBundleRoot == "" || !validDigest(profile.PiRuntimeBundleDigest) ||
		profile.ChildArgumentsDigest != DigestArguments(runtime.Arguments) || len(runtime.Arguments) == 0 ||
		!sameInheritedDescriptors(channels.DescriptorBindings(), runtime.Descriptors) || channels.ChildStdin() != runtime.Stdin ||
		(runtime.D09Source != nil && (!sameBinding(channels.workingDirectory, runtime.D09SourceHandle) ||
			!sameBinding(channels.runtimeBundle, runtime.PiRuntimeBundleHandle))) {
		return nil, ErrHostBindingsMissing
	}
	if runtime.D09Source == nil {
		if err := channels.writeBootstrap(profile, bootstrap); err != nil {
			_ = channels.Close()
			return nil, err
		}
	}
	var process *Process
	var err error
	if development {
		if profile.ChildArgumentsProfile == "developer-pi-cli-rpc-v1" {
			process, err = developmentPiCLIRuntimeLauncher(ctx, DeveloperPiCLIPlan{PiPath: nodePath, PiSHA256: profile.ExecutableDigest}, profile, runtime)
		} else {
			_ = channels.Close()
			return nil, errors.New("embedded Pi SDK worker is retired; development launches require the official CLI RPC profile")
		}
	} else {
		if capability == nil || !capability.valid() || capability.profile.Mode != ModeProduction {
			_ = channels.Close()
			return nil, ErrChildCapabilityMissing
		}
		bound, bindErr := capability.BindChild(ChildSpec{
			Executable: profile.ChildExecutable, Args: append([]string(nil), runtime.Arguments...),
			Dir: runtime.WorkingDirectory, Env: append([]string{}, runtime.Environment...), Stdin: runtime.Stdin,
			Mode:       IOModeSilent,
			Descendant: runtime.Descendant, Descriptors: cloneInheritedDescriptors(runtime.Descriptors),
		}, runtime.Launcher)
		if bindErr != nil {
			_ = channels.Close()
			return nil, bindErr
		}
		process, err = StartChild(ctx, bound)
	}
	if err != nil {
		return nil, err
	}
	if runtime.D09Source != nil {
		proof, ok := process.LaunchProof().(D09TCBProof)
		if !ok {
			return nil, stopFailedPiLaunch(process, channels, fmt.Errorf("D09 entrypoint did not provide separate source and consumer proof"))
		}
		if err := bindD09ConsumerViewProof(&bootstrap, runtime, process.PID(), proof); err != nil {
			return nil, stopFailedPiLaunch(process, channels, err)
		}
		if proof.RouteProbe != nil {
			failed := stopFailedPiLaunch(process, channels, ErrD09RouteProbeLaunched)
			var launchErr *PiWorkerLaunchError
			if errors.As(failed, &launchErr) {
				launchErr.RouteProbe = &D09RouteProbeLaunch{LauncherPID: process.PID(), Proof: proof}
			}
			return nil, failed
		}
		if err := channels.writeBootstrap(profile, bootstrap); err != nil {
			return nil, stopFailedPiLaunch(process, channels, err)
		}
	}
	if err := closePiWorkerChildEndsForReadiness(channels); err != nil {
		return nil, stopFailedPiLaunch(process, channels, err)
	}
	startupCtx, cancel := boundedStartupContext(ctx)
	defer cancel()
	for {
		event, eventErr := channels.NextEvent(startupCtx)
		if eventErr != nil {
			return nil, stopFailedPiLaunch(process, channels, fmt.Errorf("wait for Pi worker readiness: %w", eventErr))
		}
		if event.Type == "worker_error" {
			return nil, stopFailedPiLaunch(process, channels, fmt.Errorf("Pi worker startup failed: %s", event.Code))
		}
		if event.Type != "ready" {
			continue
		}
		if err := validateReadyEvent(event, profile, bootstrap); err != nil {
			return nil, stopFailedPiLaunch(process, channels, err)
		}
		break
	}
	session, err := NewSession(process)
	if err != nil {
		return nil, stopFailedPiLaunch(process, channels, err)
	}
	worker := &PiCLIWorker{Session: session, Channels: channels, development: development, launchProof: process.LaunchProof(), leases: make(map[string]*workerPromptLease), events: make(chan PiWorkerEvent, PiWorkerEventQueue), readerDone: make(chan struct{})}
	worker.rpc, err = newPiCLIWorkerRPC(profile, process.stdout, channels.RPCWriter())
	if err != nil {
		return nil, stopFailedPiLaunch(process, channels, err)
	}
	if closer, ok := process.stdout.(io.Closer); ok {
		worker.stdoutCloser = closer
	}
	go worker.pumpEvents()
	keepChannels = true
	return worker, nil
}
