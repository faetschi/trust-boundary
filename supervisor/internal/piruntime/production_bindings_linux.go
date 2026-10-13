//go:build linux

package piruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"tbound/supervisor/internal/sessionrepo"
)

// BindProductionPiWorkerBindings validates and assembles the fixed official Pi
// CLI RPC profile for the generic sealed consumer view. It deliberately refuses
// the D09 production profile; this binding helper does not launch or admit a
// production worker.
func BindProductionPiWorkerBindings(
	ctx context.Context,
	profile HostProfile,
	exposure VerifiedWorkerExposure,
	assets VerifiedWorkerRuntimeAssets,
	launcher ExecutableLauncher,
	settler DescendantSettler,
) (HostRuntimeBindings, *PiWorkerChannels, error) {
	if ctx == nil || isNilBinding(exposure) || isNilBinding(assets) || isNilBinding(launcher) || isNilBinding(settler) {
		return HostRuntimeBindings{}, nil, ErrHostBindingsMissing
	}
	if err := profile.Validate(); err != nil {
		return HostRuntimeBindings{}, nil, err
	}
	if profile.Mode != ModeProduction || profile.ContainmentProfile == D09ContainmentProfile {
		return HostRuntimeBindings{}, nil, ErrHostAdmissionRefused
	}
	environment := PiWorkerCellEnvironment()
	if DigestEnvironment(environment) != profile.ChildEnvironmentDigest {
		return HostRuntimeBindings{}, nil, fmt.Errorf("%w: fixed in-cell environment differs from the signed profile", ErrHostProfileMismatch)
	}
	if DigestArguments(PiWorkerCellArguments()) != profile.ChildArgumentsDigest {
		return HostRuntimeBindings{}, nil, fmt.Errorf("%w: fixed in-cell argv differs from the signed profile", ErrHostProfileMismatch)
	}
	channels, err := NewPiWorkerChannels(ctx, exposure, assets)
	if err != nil {
		return HostRuntimeBindings{}, nil, err
	}
	runtime := HostRuntimeBindings{
		ChildExecutable:        profile.ChildExecutable,
		NodeVersion:            profile.NodeVersion,
		ChildArgumentsProfile:  profile.ChildArgumentsProfile,
		WorkingDirectory:       profile.WorkingDirectory,
		WorkerExposureHandle:   channels.workingDirectory,
		WorkerExposure:         exposure,
		WorkerExposureEvidence: channels.exposureEvidence,
		PiRuntimeBundleRoot:    profile.PiRuntimeBundleRoot,
		PiRuntimeBundleDigest:  profile.PiRuntimeBundleDigest,
		PiRuntimeBundleHandle:  channels.runtimeBundle,
		WorkerRuntimeAssets:    assets,
		RuntimeAssetEvidence:   channels.assetEvidence,
		StdinProfile:           profile.StdinProfile,
		StdoutProfile:          profile.StdoutProfile,
		ChildEnvironment:       profile.ChildEnvironment,
		IPCProfile:             profile.IPCProfile,
		TerminalProfile:        profile.TerminalProfile,
		ContainmentProfile:     profile.ContainmentProfile,
		SettlementProfile:      profile.SettlementProfile,
		ObservationProfile:     profile.ObservationProfile,
		DescriptorProfile:      profile.DescriptorProfile,
		SourceProvenance:       profile.SourceProvenance,
		OfflineVerifier:        profile.OfflineVerifier,
		ProviderProfile:        profile.ProviderProfile,
		Launcher:               launcher,
		Descendant:             settler,
		Arguments:              PiWorkerCellArguments(),
		Environment:            environment,
		Stdin:                  channels.ChildStdin(),
		Terminal:               io.Discard,
		Observation:            discardObservationSink{},
		Descriptors:            channels.DescriptorBindings(),
	}
	if err := runtime.validate(profile); err != nil {
		_ = channels.Close()
		return HostRuntimeBindings{}, nil, err
	}
	return runtime, channels, nil
}

// BindProductionPiWorkerBindingsWithSource assembles the D09 source-stage
// binding for the signed official Pi CLI RPC profile. It remains only a
// contract scaffold: production launch still refuses the current profile.
// The source wrapper is never passed through VerifiedWorkerExposure, and
// the strict runtime source is never passed through VerifiedWorkerRuntimeAssets.
// The returned D09PiWorkerChannels owns the one exported fd4 and runtime-bundle
// descriptor; it does not own source/capability lifetimes. PreparedSession must
// keep those alive until the launcher has settled and removed the exact cell.
func BindProductionPiWorkerBindingsWithSource(
	ctx context.Context,
	profile HostProfile,
	source VerifiedD09WorkerSource,
	sourceCapability *sessionrepo.VerifiedSealedSource,
	assets VerifiedD09RuntimeAssetSource,
	launcher ExecutableLauncher,
	settler DescendantSettler,
) (HostRuntimeBindings, *D09PiWorkerChannels, error) {
	if ctx == nil || isNilBinding(source) || sourceCapability == nil || isNilBinding(assets) ||
		isNilBinding(launcher) || isNilBinding(settler) || source.SourceCapability() != sourceCapability {
		return HostRuntimeBindings{}, nil, ErrHostBindingsMissing
	}
	if err := profile.Validate(); err != nil {
		return HostRuntimeBindings{}, nil, err
	}
	if profile.Mode != ModeProduction || profile.ContainmentProfile != D09ContainmentProfile {
		return HostRuntimeBindings{}, nil, ErrHostAdmissionRefused
	}
	environment := PiWorkerCellEnvironment()
	if DigestEnvironment(environment) != profile.ChildEnvironmentDigest ||
		DigestArguments(PiWorkerCellArguments()) != profile.ChildArgumentsDigest {
		return HostRuntimeBindings{}, nil, fmt.Errorf("%w: fixed D09 in-cell argv/environment differs from signed profile", ErrHostProfileMismatch)
	}
	channels, err := NewD09PiWorkerChannels(ctx, source, assets)
	if err != nil {
		return HostRuntimeBindings{}, nil, err
	}
	if channels.sourceCapability != sourceCapability {
		_ = channels.Close()
		return HostRuntimeBindings{}, nil, ErrHostBindingsMissing
	}
	runtime := HostRuntimeBindings{
		ChildExecutable: profile.ChildExecutable, NodeVersion: profile.NodeVersion,
		ChildArgumentsProfile: profile.ChildArgumentsProfile, WorkingDirectory: profile.WorkingDirectory,
		D09Source: source, D09SourceCapability: sourceCapability,
		D09SourceEvidence:   d09SourceEvidenceForBindings(channels.sourceEvidence),
		D09SourceHandle:     channels.channels.workingDirectory,
		PiRuntimeBundleRoot: profile.PiRuntimeBundleRoot, PiRuntimeBundleDigest: profile.PiRuntimeBundleDigest,
		PiRuntimeBundleHandle: channels.channels.runtimeBundle,
		D09RuntimeAssetSource: assets, D09RuntimeAssetEvidence: channels.assetEvidence,
		StdinProfile: profile.StdinProfile, StdoutProfile: profile.StdoutProfile, ChildEnvironment: profile.ChildEnvironment,
		IPCProfile: profile.IPCProfile, TerminalProfile: profile.TerminalProfile,
		ContainmentProfile: profile.ContainmentProfile, SettlementProfile: profile.SettlementProfile,
		ObservationProfile: profile.ObservationProfile, DescriptorProfile: profile.DescriptorProfile,
		SourceProvenance: profile.SourceProvenance, OfflineVerifier: profile.OfflineVerifier,
		ProviderProfile: profile.ProviderProfile, Launcher: launcher, Descendant: settler,
		Arguments: PiWorkerCellArguments(), Environment: environment, Stdin: channels.channels.ChildStdin(),
		Terminal: io.Discard, Observation: discardObservationSink{},
		Descriptors: channels.channels.DescriptorBindings(),
	}
	if err := runtime.validate(profile); err != nil {
		_ = channels.Close()
		return HostRuntimeBindings{}, nil, err
	}
	return runtime, channels, nil
}

// validateD09CellEnvironment checks the exact four-field in-cell worker
// environment without requiring the in-cell paths to exist on the host: the
// signed profile binds their digest, and the cell image provides the private
// 0700 directories at the authorized UID.
func validateD09CellEnvironment(environment []string) error {
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		name, value, ok := strings.Cut(item, "=")
		if !ok || (name != "HOME" && name != "TMPDIR" && name != "LANG" && name != "PI_OFFLINE") {
			return errors.New("D09 worker environment contains a non-allowlisted variable")
		}
		if _, duplicate := values[name]; duplicate {
			return fmt.Errorf("D09 worker environment repeats %s", name)
		}
		values[name] = value
	}
	if len(values) != 4 || values["PI_OFFLINE"] != "1" || values["LANG"] != "C.UTF-8" ||
		values["HOME"] != PiWorkerCellHome || values["TMPDIR"] != PiWorkerCellTemp {
		return fmt.Errorf("D09 worker environment must be exactly HOME=%s/TMPDIR=%s/LANG/PI_OFFLINE", PiWorkerCellHome, PiWorkerCellTemp)
	}
	return nil
}
