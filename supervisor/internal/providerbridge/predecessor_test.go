package providerbridge

import "testing"

// The expected digest was computed by the code before ImportedPrefixDigest existed, so a projection
// without a predecessor keeps its commitment.
func TestLegacyTranscriptProjectionDigestIsUnchanged(t *testing.T) {
	projection := TranscriptProjection{
		SchemaVersion: transcriptProjectionSchema, VerificationScope: ProjectionScopeStoreCrosslink,
		WorkflowID: "workflow-golden", ConversationID: "conversation-golden", ProfileID: "profile-golden",
		Model: ApprovedModel, PolicyDigest: "sha256:policy", SystemPromptDigest: "sha256:system",
		DeveloperPromptDigest: "sha256:developer", InitialGenerationID: "g0", InitialTreeDigest: "tree-0",
		ProviderGenerationID: "g1", ProviderTreeDigest: "tree-1", ProviderSequence: 2, EventCount: 7,
		ProviderJournalRecordCount: 7, ExchangeCount: 2, CapturedCallCount: 1, DecisionCount: 1, ResultCount: 1,
		ProviderJournalHead: "head", HistoryDigest: "sha256:history", Disposition: DispositionAwaitingHostPrompt,
	}
	const want = "sha256:0a9381bab56d1f4651d4e95ec6f6bbb5d631563f0b12df6822a30086502fdf5d"
	if got := transcriptProjectionDigest(projection); got != want {
		t.Fatalf("legacy projection digest=%s want %s", got, want)
	}
}
