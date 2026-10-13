//go:build linux

package piruntime

import (
	"errors"
	"testing"
)

// These tests exercise the proof-to-bootstrap binding contract only. They do
// not create an AdmissionCapability, qualify a host, or launch a process.
func TestD09ConsumerProofBindsViewOnlyFromMatchingLaunchEvidence(t *testing.T) {
	channels, runtime := d09ChannelBindingFixture(t)
	_ = channels
	source, assets := runtime.D09SourceEvidence, runtime.D09RuntimeAssetEvidence
	digest := func(value string) string { return DigestBytes([]byte(value)) }
	const launcherPID = 123
	proof := D09TCBProof{
		Schema: "tbound-pi-entrypoint/v1", ContainerName: "cell-test",
		LeaderPID: 124, ContainerLeaderPID: 1, LauncherProcessPID: launcherPID,
		ImageDigest: digest("image"), ImageRef: "cell@" + digest("image"),
		TemplateDigest:         assets.NamespaceProfileDigest,
		ConsumerMountsVerified: true, RawSourceFDsClosed: true,
		WorkerMountID: 31, RuntimeMountID: 32,
		ConsumerWorkerMountID: 31, ConsumerRuntimeMountID: 32,
		WorkerDevice: source.SourceDevice, WorkerInode: source.SourceInode,
		RuntimeDevice: assets.Device, RuntimeInode: assets.Inode,
		SourceGenerationID: source.GenerationID, SourceTreeDigest: source.TreeDigest,
		SourceManifestDigest: source.ManifestDigest, SourceBindingDigest: source.SourceBindingDigest,
		SourceProvenanceDigest: source.ProvenanceDigest, SourceIdentityDigest: source.SourceIdentityDigest,
		SourceStoreAuditHead: source.StoreAuditHead, SourceStoreAuditCount: source.StoreAuditRecordCount,
		SourceDevice: source.SourceDevice, SourceInode: source.SourceInode, SourceMountID: source.SourceMountID,
	}
	newBootstrap := func() PiWorkerBootstrap {
		bootstrap := PiWorkerBootstrap{
			SessionID: "session-1", WorkflowID: "workflow-1", ConversationID: "conversation-1",
		}
		if err := bindD09BootstrapSourceEvidence(&bootstrap, source); err != nil {
			t.Fatal(err)
		}
		return bootstrap
	}

	bootstrap := newBootstrap()
	if err := bindD09ConsumerViewProof(&bootstrap, runtime, launcherPID, proof); err != nil {
		t.Fatalf("matching proof rejected: %v", err)
	}
	if bootstrap.WorkerViewIdentity == "" || bootstrap.WorkerMountID != proof.ConsumerWorkerMountID {
		t.Fatalf("consumer view was not bound from proof: %+v", bootstrap)
	}

	tests := []struct {
		name   string
		mutate func(*D09TCBProof, *PiWorkerBootstrap, *int)
	}{
		{"wrong process", func(_ *D09TCBProof, _ *PiWorkerBootstrap, pid *int) { *pid++ }},
		{"wrong source identity", func(p *D09TCBProof, _ *PiWorkerBootstrap, _ *int) { p.SourceIdentityDigest = digest("other") }},
		{"wrong source mount evidence", func(p *D09TCBProof, _ *PiWorkerBootstrap, _ *int) { p.SourceMountID++ }},
		{"wrong consumer mount", func(p *D09TCBProof, _ *PiWorkerBootstrap, _ *int) { p.ConsumerWorkerMountID++ }},
		{"wrong runtime mount", func(p *D09TCBProof, _ *PiWorkerBootstrap, _ *int) { p.ConsumerRuntimeMountID++ }},
		{"raw source remains open", func(p *D09TCBProof, _ *PiWorkerBootstrap, _ *int) { p.RawSourceFDsClosed = false }},
		{"prefilled consumer view", func(_ *D09TCBProof, b *PiWorkerBootstrap, _ *int) { b.WorkerMountID = 99 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate, candidateProof, pid := newBootstrap(), proof, launcherPID
			test.mutate(&candidateProof, &candidate, &pid)
			before := candidate
			if err := bindD09ConsumerViewProof(&candidate, runtime, pid, candidateProof); !errors.Is(err, ErrHostBindingsMissing) {
				t.Fatalf("mismatched proof accepted: %v", err)
			}
			if candidate != before {
				t.Fatalf("rejected proof partially changed bootstrap: before=%+v after=%+v", before, candidate)
			}
		})
	}
}
