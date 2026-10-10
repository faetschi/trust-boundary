//go:build linux

package piruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// BindProductionPiWorkerBindings assembles the exact production Pi worker
// runtime bindings and channels from the verified sealed exposure/assets and the
// verified D09 launcher. It is the stable API the CLI composition (worker C) and
// the session-launch owner (worker B) consume; it exposes no fictional seam.
//
// The returned bindings carry the same owned descriptors the channels hold (not
// re-exported copies), the fixed in-cell argv/environment, the fixed descriptor
// role map (D09ChildRoleMap), and the supplied verified launcher and cgroup
// settler. The caller then calls AdmitProduction and LaunchPiSDKWorker.
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
	if profile.Mode != ModeProduction {
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
