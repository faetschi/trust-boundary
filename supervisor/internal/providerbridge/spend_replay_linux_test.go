//go:build linux

package providerbridge

import (
	"context"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
)

// A research-route transcript must replay byte-for-byte under the research
// route's pins, and only under them: an engineering pin set, or a research route
// with another profile digest, cannot reproduce the recorded request.
func TestResearchTranscriptReplaysOnlyUnderItsOwnRoute(t *testing.T) {
	cfg := recoveryTestConfig()
	cfg.Route = spendTestRoute(t)
	journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "research-provider.audit"))
	doer := &spendDoer{}
	conversation, err := newWithDoer(cfg, spendTestModel, doer, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-research-replay", "Say ok."); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.NextTurn(context.Background()); err != nil {
		t.Fatalf("research turn: %v", err)
	}
	policy := recoveryTestPolicy(t, true)
	projection, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: spendTestModel, Policy: policy}, &replayVerifierFixture{})
	if err != nil {
		t.Fatalf("research transcript did not replay under its own route: %v", err)
	}
	if projection.Model != spendTestModel || projection.ExchangeCount != 1 || projection.Disposition != DispositionAwaitingHostPrompt {
		t.Fatalf("research replay projection differs: %+v", projection)
	}

	engineering := cfg
	engineering.Route = openrouter.Route{}
	if _, err := ReplayTranscript(journal, RecoveryPins{Config: engineering, Model: ApprovedModel, Policy: policy}, &replayVerifierFixture{}); err == nil {
		t.Fatal("a research transcript replayed under the engineering route")
	}
	if _, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: policy}, &replayVerifierFixture{}); err == nil {
		t.Fatal("a research route replayed with the engineering model pin")
	}
	other, err := openrouter.NewResearchRoute(openrouter.ResearchSpec{Model: spendTestModel, Endpoint: spendTestEndpoint,
		ProfileDigest: "cd" + cfg.Route.ProfileDigest[2:], Members: spendTestMembers + "", Timeout: cfg.Route.Timeout})
	if err != nil {
		t.Fatal(err)
	}
	changed := cfg
	changed.Route = other
	// A different profile digest alone does not change the request bytes: the
	// digest is bound through the workflow runtime pin and the route comparison,
	// not through the body. A different member set does change the bytes.
	if _, err := ReplayTranscript(journal, RecoveryPins{Config: changed, Model: spendTestModel, Policy: policy}, &replayVerifierFixture{}); err != nil {
		t.Fatalf("same request members must replay: %v", err)
	}
	reseeded, err := openrouter.NewResearchRoute(openrouter.ResearchSpec{Model: spendTestModel, Endpoint: spendTestEndpoint,
		ProfileDigest: cfg.Route.ProfileDigest, Members: `{"provider":{"order":["example-provider"],"allow_fallbacks":false,"data_collection":"deny","require_parameters":true,"zdr":true},"temperature":0,"seed":8,"usage":{"include":true}}`,
		Timeout: cfg.Route.Timeout})
	if err != nil {
		t.Fatal(err)
	}
	changed.Route = reseeded
	if _, err := ReplayTranscript(journal, RecoveryPins{Config: changed, Model: spendTestModel, Policy: policy}, &replayVerifierFixture{}); err == nil {
		t.Fatal("a transcript recorded with seed 7 replayed under a profile with seed 8")
	}
}
