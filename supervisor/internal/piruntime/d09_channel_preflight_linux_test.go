//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/sessionrepo"
)

type d09ChannelSourceFixture struct{ identity byte }

func (*d09ChannelSourceFixture) SourceCapability() *sessionrepo.VerifiedSealedSource {
	return nil
}
func (*d09ChannelSourceFixture) SourceEvidence() (sessionrepo.SealedSourceEvidence, error) {
	return sessionrepo.SealedSourceEvidence{}, nil
}
func (*d09ChannelSourceFixture) ExportD09WorkerSource(context.Context) (*os.File, sessionrepo.SealedSourceEvidence, error) {
	return nil, sessionrepo.SealedSourceEvidence{}, nil
}
func (*d09ChannelSourceFixture) VerifyD09WorkerSource(context.Context, sessionrepo.SealedSourceEvidence) error {
	return nil
}
func (*d09ChannelSourceFixture) Close() error { return nil }

type d09ChannelAssetsFixture struct{ identity byte }

func (*d09ChannelAssetsFixture) ExportD09RuntimeAssetSource(context.Context) (*os.File, WorkerRuntimeAssetEvidence, error) {
	return nil, WorkerRuntimeAssetEvidence{}, nil
}
func (*d09ChannelAssetsFixture) VerifyD09RuntimeAssetSource(context.Context, WorkerRuntimeAssetEvidence) error {
	return nil
}
func (*d09ChannelAssetsFixture) Close() error { return nil }

func d09ChannelBindingFixture(t *testing.T) (*D09PiWorkerChannels, HostRuntimeBindings) {
	t.Helper()
	sourceHandle, err := os.Open(makePrivateTestDirectory(t, "d09-source"))
	if err != nil {
		t.Fatal(err)
	}
	bundleHandle, err := os.Open(makePrivateTestDirectory(t, "d09-bundle"))
	if err != nil {
		_ = sourceHandle.Close()
		t.Fatal(err)
	}
	channels, err := newPiWorkerChannels(sourceHandle, bundleHandle)
	if err != nil {
		_ = sourceHandle.Close()
		_ = bundleHandle.Close()
		t.Fatal(err)
	}
	channels.ownedSourceFDs = true
	t.Cleanup(func() {
		_ = channels.Close()
		_ = sourceHandle.Close()
		_ = bundleHandle.Close()
	})

	digest := func(value string) string { return DigestBytes([]byte(value)) }
	sourceEvidence := sessionrepo.SealedSourceEvidence{
		GenerationID: "generation-1", TreeDigest: digest("tree"),
		ManifestDigest: digest("manifest"), SourceBindingDigest: digest("binding"),
		ProvenanceDigest: digest("provenance"), StoreAuditHead: d09AuditHeadFromJournal(t),
		StoreAuditRecordCount: 1, SourceIdentityDigest: digest("identity"),
		SourceDevice: 1, SourceInode: 2, SourceMountID: 3,
	}
	source := &d09ChannelSourceFixture{}
	sourceCapability := &sessionrepo.VerifiedSealedSource{}
	assets := &d09ChannelAssetsFixture{}
	assetEvidence := WorkerRuntimeAssetEvidence{
		BundleDigest: digest("bundle"), ViewIdentity: digest("asset-view"),
		NamespaceProfileDigest: digest("namespace"), Device: 4, Inode: 5, MountID: 6,
	}
	d09Channels := &D09PiWorkerChannels{
		channels: channels, source: source, sourceCapability: sourceCapability,
		sourceEvidence: sourceEvidence, assets: assets, assetEvidence: assetEvidence,
	}
	runtime := HostRuntimeBindings{
		D09Source: source, D09SourceCapability: sourceCapability,
		D09SourceEvidence:     d09SourceEvidenceForBindings(sourceEvidence),
		D09SourceHandle:       sourceHandle,
		D09RuntimeAssetSource: assets, D09RuntimeAssetEvidence: assetEvidence,
		PiRuntimeBundleHandle: bundleHandle, Stdin: channels.ChildStdin(),
		Descriptors: channels.DescriptorBindings(),
	}
	return d09Channels, runtime
}

func TestD09ChannelBindingsRequireExactAdmittedHandlesAndEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*D09PiWorkerChannels, *HostRuntimeBindings)
	}{
		{"source wrapper", func(ch *D09PiWorkerChannels, _ *HostRuntimeBindings) {
			ch.source = &d09ChannelSourceFixture{}
		}},
		{"source capability", func(ch *D09PiWorkerChannels, _ *HostRuntimeBindings) {
			ch.sourceCapability = &sessionrepo.VerifiedSealedSource{}
		}},
		{"runtime asset source", func(ch *D09PiWorkerChannels, _ *HostRuntimeBindings) {
			ch.assets = &d09ChannelAssetsFixture{}
		}},
		{"asset evidence", func(ch *D09PiWorkerChannels, _ *HostRuntimeBindings) {
			ch.assetEvidence.BundleDigest = DigestBytes([]byte("different bundle"))
		}},
		{"source evidence", func(ch *D09PiWorkerChannels, _ *HostRuntimeBindings) {
			ch.sourceEvidence.TreeDigest = DigestBytes([]byte("different tree"))
		}},
		{"source descriptor", func(_ *D09PiWorkerChannels, runtime *HostRuntimeBindings) {
			runtime.D09SourceHandle = runtime.PiRuntimeBundleHandle
		}},
		{"runtime bundle descriptor", func(ch *D09PiWorkerChannels, runtime *HostRuntimeBindings) {
			runtime.PiRuntimeBundleHandle = ch.channels.workingDirectory
		}},
		{"stdin handle", func(_ *D09PiWorkerChannels, runtime *HostRuntimeBindings) {
			runtime.Stdin = nil
		}},
		{"closed channels", func(ch *D09PiWorkerChannels, _ *HostRuntimeBindings) { _ = ch.channels.Close() }},
		{"closed child ends", func(ch *D09PiWorkerChannels, _ *HostRuntimeBindings) { _ = ch.channels.CloseChildEnds() }},
		{"descriptor file", func(_ *D09PiWorkerChannels, runtime *HostRuntimeBindings) { runtime.Descriptors[0].File = nil }},
		{"descriptor role", func(_ *D09PiWorkerChannels, runtime *HostRuntimeBindings) {
			runtime.Descriptors[0].Role = DescriptorRuntimeBundle
		}},
		{"borrowed source descriptors", func(ch *D09PiWorkerChannels, _ *HostRuntimeBindings) {
			ch.channels.ownedSourceFDs = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channels, runtime := d09ChannelBindingFixture(t)
			if err := validateD09PiChannelBindings(channels, runtime); err != nil {
				t.Fatalf("valid source-stage bindings rejected: %v", err)
			}
			test.mutate(channels, &runtime)
			if err := validateD09PiChannelBindings(channels, runtime); !errors.Is(err, ErrHostBindingsMissing) {
				t.Fatalf("mismatched source-stage binding accepted: %v", err)
			}
		})
	}
}

func TestD09BootstrapBindsStoreSourceAndRejectsForgedConsumerView(t *testing.T) {
	evidence := D09WorkerSourceEvidence{
		GenerationID:          "generation-1",
		TreeDigest:            DigestBytes([]byte("tree")),
		ManifestDigest:        DigestBytes([]byte("manifest")),
		SourceBindingDigest:   DigestBytes([]byte("binding")),
		ProvenanceDigest:      DigestBytes([]byte("provenance")),
		StoreAuditHead:        d09AuditHeadFromJournal(t),
		StoreAuditRecordCount: 1,
		SourceIdentityDigest:  DigestBytes([]byte("identity")),
		SourceDevice:          1, SourceInode: 2, SourceMountID: 3,
	}
	base := PiWorkerBootstrap{
		SessionID: "session-1", WorkflowID: "workflow-1", ConversationID: "conversation-1",
	}
	if err := bindD09BootstrapSourceEvidence(&base, evidence); err != nil {
		t.Fatalf("bind Store source evidence: %v", err)
	}
	if base.SourceBindingDigest != evidence.SourceBindingDigest ||
		base.SourceProvenanceDigest != evidence.ProvenanceDigest ||
		base.SourceGenerationID != evidence.GenerationID ||
		base.SourceTreeDigest != evidence.TreeDigest ||
		base.SourceManifestDigest != evidence.ManifestDigest ||
		base.WorkerViewIdentity != "" || base.WorkerMountID != 0 {
		t.Fatalf("bootstrap fields cross the source/consumer-view boundary: %+v", base)
	}

	tests := []struct {
		name   string
		mutate func(*PiWorkerBootstrap)
	}{
		{"forged source digest", func(b *PiWorkerBootstrap) {
			b.SourceBindingDigest = DigestBytes([]byte("forged"))
		}},
		{"forged worker view", func(b *PiWorkerBootstrap) {
			b.WorkerViewIdentity = DigestBytes([]byte("unproved view"))
		}},
		{"forged worker mount", func(b *PiWorkerBootstrap) { b.WorkerMountID = 99 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := PiWorkerBootstrap{
				SessionID: "session-1", WorkflowID: "workflow-1", ConversationID: "conversation-1",
			}
			test.mutate(&candidate)
			before := candidate
			if err := bindD09BootstrapSourceEvidence(&candidate, evidence); !errors.Is(err, ErrHostBindingsMissing) {
				t.Fatalf("inconsistent bootstrap accepted: %v", err)
			}
			if candidate != before {
				t.Fatalf("rejected bootstrap was partially modified: before=%+v after=%+v", before, candidate)
			}
		})
	}
}

func TestD09LaunchClosesAvailableChannelsWhenContextIsNil(t *testing.T) {
	sourceHandle, err := os.Open(makePrivateTestDirectory(t, "d09-nil-context-source"))
	if err != nil {
		t.Fatal(err)
	}
	bundleHandle, err := os.Open(makePrivateTestDirectory(t, "d09-nil-context-bundle"))
	if err != nil {
		_ = sourceHandle.Close()
		t.Fatal(err)
	}
	channels, err := newPiWorkerChannels(sourceHandle, bundleHandle)
	if err != nil {
		_ = sourceHandle.Close()
		_ = bundleHandle.Close()
		t.Fatal(err)
	}
	channels.ownedSourceFDs = true
	_, launchErr := LaunchD09PiCLIWorker(nil, nil, &D09PiWorkerChannels{channels: channels}, PiWorkerBootstrap{})
	if launchErr == nil || !strings.Contains(launchErr.Error(), "D09 Pi launch refused") {
		t.Fatalf("nil-context launch was not refused: %v", launchErr)
	}
	if _, err := sourceHandle.Stat(); err == nil {
		t.Fatal("owned source descriptor remained open after nil-context refusal")
	}
	if _, err := bundleHandle.Stat(); err == nil {
		t.Fatal("owned runtime descriptor remained open after nil-context refusal")
	}
}
