//go:build linux

package piruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"tbound/supervisor/internal/sessionrepo"
)

// VerifiedD09WorkerSource is deliberately disjoint from VerifiedWorkerExposure:
// that older interface represents an already-registered read-only consumer
// view, while this interface exports only the Store-owned host source.
type VerifiedD09WorkerSource interface {
	SourceCapability() *sessionrepo.VerifiedSealedSource
	SourceEvidence() (sessionrepo.SealedSourceEvidence, error)
	ExportD09WorkerSource(context.Context) (*os.File, sessionrepo.SealedSourceEvidence, error)
	VerifyD09WorkerSource(context.Context, sessionrepo.SealedSourceEvidence) error
	Close() error
}

// VerifiedD09RuntimeAssetSource is the strict D09 host-source API. Content-only
// fixture assets and the generic consumer-view interface do not satisfy it.
type VerifiedD09RuntimeAssetSource interface {
	ExportD09RuntimeAssetSource(context.Context) (*os.File, WorkerRuntimeAssetEvidence, error)
	VerifyD09RuntimeAssetSource(context.Context, WorkerRuntimeAssetEvidence) error
	Close() error
}

// D09PiWorkerChannels owns the one exported source FD and runtime-bundle FD held
// by PiWorkerChannels. It does not own or close the sessionlaunch source wrapper,
// its Store capability, or the runtime asset source capability. Those remain in
// PreparedSession's cleanup stack and are released only after cell settlement.
type D09PiWorkerChannels struct {
	channels         *PiWorkerChannels
	source           VerifiedD09WorkerSource
	sourceCapability *sessionrepo.VerifiedSealedSource
	sourceEvidence   sessionrepo.SealedSourceEvidence
	assets           VerifiedD09RuntimeAssetSource
	assetEvidence    WorkerRuntimeAssetEvidence
}

// PiWorkerChannels returns the exact channel object whose fd4 is the one source
// descriptor exported by source.ExportD09WorkerSource. The caller passes this to
// LaunchD09PiSDKWorker; it must not export or duplicate another source FD.
func (c *D09PiWorkerChannels) PiWorkerChannels() *PiWorkerChannels {
	if c == nil {
		return nil
	}
	return c.channels
}

func (c *D09PiWorkerChannels) SourceCapability() *sessionrepo.VerifiedSealedSource {
	if c == nil {
		return nil
	}
	return c.sourceCapability
}

func (c *D09PiWorkerChannels) SourceEvidence() (sessionrepo.SealedSourceEvidence, bool) {
	if c == nil || c.channels == nil {
		return sessionrepo.SealedSourceEvidence{}, false
	}
	return c.sourceEvidence, true
}

// Close releases the channel-owned exported FDs and channel endpoints only. It
// intentionally leaves source/capability/asset lifetimes to PreparedSession.
func (c *D09PiWorkerChannels) Close() error {
	if c == nil || c.channels == nil {
		return nil
	}
	return c.channels.Close()
}

// NewD09PiWorkerChannels exports the source exactly once and binds that returned
// O_PATH descriptor directly to fd4. The entrypoint later replaces the raw
// source FD with its separately verified read-only consumer mount before Node.
func NewD09PiWorkerChannels(ctx context.Context, source VerifiedD09WorkerSource,
	assets VerifiedD09RuntimeAssetSource) (*D09PiWorkerChannels, error) {
	if ctx == nil || isNilBinding(source) || isNilBinding(assets) {
		return nil, ErrHostBindingsMissing
	}
	capability := source.SourceCapability()
	if capability == nil {
		return nil, ErrHostBindingsMissing
	}
	evidence, err := source.SourceEvidence()
	if err != nil {
		return nil, errors.Join(ErrHostBindingsMissing, err)
	}
	capEvidence, err := capability.Evidence()
	if err != nil || capEvidence != evidence {
		return nil, errors.Join(ErrHostBindingsMissing, err)
	}
	if err := capability.Revalidate(ctx, evidence); err != nil {
		return nil, fmt.Errorf("revalidate Store-issued sealed source before export: %w", err)
	}
	workerSource, exportedEvidence, err := source.ExportD09WorkerSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("export D09 host source once: %w", err)
	}
	closeWorkerSource := true
	defer func() {
		if closeWorkerSource && workerSource != nil {
			_ = workerSource.Close()
		}
	}()
	if workerSource == nil || exportedEvidence != evidence {
		return nil, ErrHostBindingsMissing
	}
	if err := capability.Revalidate(ctx, exportedEvidence); err != nil {
		return nil, fmt.Errorf("revalidate Store-issued sealed source after export: %w", err)
	}
	if err := source.VerifyD09WorkerSource(ctx, exportedEvidence); err != nil {
		return nil, fmt.Errorf("verify D09 host source provenance: %w", err)
	}
	if err := validateD09WorkerSourceDescriptor(workerSource, exportedEvidence); err != nil {
		return nil, err
	}
	runtimeSource, assetEvidence, err := assets.ExportD09RuntimeAssetSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("export strict D09 runtime asset source: %w", err)
	}
	closeRuntimeSource := true
	defer func() {
		if closeRuntimeSource && runtimeSource != nil {
			_ = runtimeSource.Close()
		}
	}()
	if runtimeSource == nil || assetEvidence.validateShape() != nil {
		return nil, ErrHostBindingsMissing
	}
	if err := assets.VerifyD09RuntimeAssetSource(ctx, assetEvidence); err != nil {
		return nil, fmt.Errorf("verify strict D09 runtime asset source: %w", err)
	}
	if err := validateRuntimeAssetDescriptor(runtimeSource, assetEvidence); err != nil {
		return nil, err
	}
	channels, err := newPiWorkerChannels(workerSource, runtimeSource)
	if err != nil {
		return nil, err
	}
	channels.ownedSourceFDs = true
	closeWorkerSource = false
	closeRuntimeSource = false
	return &D09PiWorkerChannels{channels: channels, source: source, sourceCapability: capability,
		sourceEvidence: exportedEvidence, assets: assets, assetEvidence: assetEvidence}, nil
}

func validateD09WorkerSourceDescriptor(file *os.File, evidence sessionrepo.SealedSourceEvidence) error {
	if file == nil || evidence.GenerationID == "" || !validDigest(evidence.TreeDigest) ||
		!validDigest(evidence.ManifestDigest) || !validDigest(evidence.SourceBindingDigest) ||
		!validDigest(evidence.ProvenanceDigest) || !validDigest(evidence.StoreAuditHead) ||
		!validDigest(evidence.SourceIdentityDigest) || evidence.StoreAuditRecordCount == 0 ||
		evidence.SourceDevice == 0 || evidence.SourceInode == 0 || evidence.SourceMountID == 0 {
		return ErrHostBindingsMissing
	}
	info, err := file.Stat()
	stat, ok := (*syscall.Stat_t)(nil), false
	if info != nil {
		stat, ok = info.Sys().(*syscall.Stat_t)
	}
	if err != nil || !info.IsDir() || !ok || uint64(stat.Dev) != evidence.SourceDevice || stat.Ino != evidence.SourceInode {
		return errors.Join(ErrHostBindingsMissing, err)
	}
	mountID, err := descriptorMountID(file.Fd())
	if err != nil || mountID != evidence.SourceMountID {
		return errors.Join(ErrHostBindingsMissing, err)
	}
	return nil
}

func d09SourceEvidenceForBindings(e sessionrepo.SealedSourceEvidence) D09WorkerSourceEvidence {
	return D09WorkerSourceEvidence{GenerationID: e.GenerationID, TreeDigest: e.TreeDigest,
		ManifestDigest: e.ManifestDigest, SourceBindingDigest: e.SourceBindingDigest,
		ProvenanceDigest: e.ProvenanceDigest, StoreAuditHead: e.StoreAuditHead,
		StoreAuditRecordCount: e.StoreAuditRecordCount, SourceIdentityDigest: e.SourceIdentityDigest,
		SourceDevice: e.SourceDevice, SourceInode: e.SourceInode, SourceMountID: e.SourceMountID}
}

func sameD09SourceEvidence(a D09WorkerSourceEvidence, b sessionrepo.SealedSourceEvidence) bool {
	return a == d09SourceEvidenceForBindings(b)
}

func d09WorkerStagingHandle(runtime HostRuntimeBindings) *os.File {
	if runtime.D09Source != nil {
		return runtime.D09SourceHandle
	}
	return runtime.WorkerExposureHandle
}

func verifyD09HostRuntimeSources(ctx context.Context, runtime HostRuntimeBindings) error {
	source, sourceOK := runtime.D09Source.(VerifiedD09WorkerSource)
	capability, capabilityOK := runtime.D09SourceCapability.(*sessionrepo.VerifiedSealedSource)
	assets, assetsOK := runtime.D09RuntimeAssetSource.(VerifiedD09RuntimeAssetSource)
	if ctx == nil || !sourceOK || !capabilityOK || !assetsOK || capability == nil ||
		source.SourceCapability() != capability || runtime.D09SourceHandle == nil ||
		runtime.WorkerExposure != nil || runtime.WorkerRuntimeAssets != nil {
		return ErrHostBindingsMissing
	}
	evidence, err := source.SourceEvidence()
	if err != nil || !sameD09SourceEvidence(runtime.D09SourceEvidence, evidence) {
		return errors.Join(ErrHostBindingsMissing, err)
	}
	capEvidence, err := capability.Evidence()
	if err != nil || capEvidence != evidence {
		return errors.Join(ErrHostBindingsMissing, err)
	}
	if err := capability.Revalidate(ctx, evidence); err != nil {
		return fmt.Errorf("revalidate retained Store source capability: %w", err)
	}
	if err := source.VerifyD09WorkerSource(ctx, evidence); err != nil {
		return fmt.Errorf("reverify D09 source wrapper and D06 prefix: %w", err)
	}
	if err := validateD09WorkerSourceDescriptor(runtime.D09SourceHandle, evidence); err != nil {
		return err
	}
	if err := assets.VerifyD09RuntimeAssetSource(ctx, runtime.D09RuntimeAssetEvidence); err != nil {
		return fmt.Errorf("reverify strict D09 runtime asset source: %w", err)
	}
	if err := validateRuntimeAssetDescriptor(runtime.PiRuntimeBundleHandle, runtime.D09RuntimeAssetEvidence); err != nil {
		return err
	}
	return nil
}
