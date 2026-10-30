package workflowrecovery

import (
	"strings"
	"testing"
	"time"
)

// The expected digest was computed by the code before the predecessor pins existed, so a legacy
// checkpoint keeps its commitment.
func TestLegacyCheckpointDigestIsUnchanged(t *testing.T) {
	d := func(c string) string { return strings.Repeat(c, 64) }
	checkpoint := Checkpoint{SchemaVersion: checkpointSchema, Pins: Pins{
		WorkflowID: "workflow-golden", SessionID: "session-golden", RecoveryGeneration: 1, OwnerEpoch: 1,
		OwnerIncarnationDigest: d("1"), RootIdentityDigest: d("2"), StoreIdentityDigest: d("3"),
		AdmissionIdentityDigest: d("4"), CommandIdentityDigest: d("5"), AdminIdentityDigest: d("6"),
		TaskDigest: d("7"), PromptDigest: d("8"), PolicyDigest: d("9"), ProfileDigest: d("a"),
		ImageDigest: d("b"), RuntimeDigest: d("c"), D06ProvenanceDigest: d("d"), PrivateGitIdentityDigest: d("e"),
		StoreBaselineGenerationID: "g0", StoreBaselineTreeDigest: d("f"), StoreTipGenerationID: "g0", StoreTipTreeDigest: d("f"),
		ProviderTranscripts: []TranscriptPin{}, ProviderProjectionDigest: d("0"), ProviderDisposition: DispositionNone,
		RecoveryLineageDigest: d("1"), PendingCallDisposition: PendingCallNone,
	}, Limits: Limits{MaxPreEffectRetries: 2, MaxCleanRestarts: 2, MaxHostCalls: 8, MaxTokens: 512,
		Deadline: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}}
	const want = "4ea1a444f73d332039e40e8db442b69f6433e17ea16374945ccf8b2e74c79e3b"
	if got := CheckpointDigest(checkpoint); got != want {
		t.Fatalf("legacy checkpoint digest=%s want %s", got, want)
	}
}
