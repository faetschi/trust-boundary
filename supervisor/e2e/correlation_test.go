package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"tbound/supervisor/internal/broker/correlation"
)

type transcript struct {
	FixtureID               string     `json:"fixture_id"`
	EvidenceClass           string     `json:"evidence_class"`
	RealProviderExchange    bool       `json:"real_provider_exchange"`
	ClaimBearing            bool       `json:"claim_bearing"`
	DigestValuesAreSynthetic bool       `json:"digest_values_are_synthetic"`
	Scenarios               []scenario `json:"scenarios"`
}

type scenario struct {
	Name  string `json:"name"`
	Steps []step `json:"steps"`
}

type step struct {
	Action             string                  `json:"action"`
	Capture            *correlation.BrokerCall `json:"capture,omitempty"`
	Proposal           *correlation.Proposal   `json:"proposal,omitempty"`
	WantAccepted       bool                    `json:"want_accepted"`
	WantReason         string                  `json:"want_reason"`
	WantStreamClosed   bool                    `json:"want_stream_closed"`
	WantCaptureOrdinal uint64                  `json:"want_capture_ordinal"`
}

type artifact struct {
	FixtureID               string             `json:"fixture_id"`
	EvidenceClass           string             `json:"evidence_class"`
	RealExchange            bool               `json:"real_provider_exchange"`
	ClaimBearing            bool               `json:"claim_bearing"`
	DigestValuesAreSynthetic bool               `json:"digest_values_are_synthetic"`
	Decisions               []artifactDecision `json:"decisions"`
}

type artifactDecision struct {
	Scenario       string `json:"scenario"`
	Step           int    `json:"step"`
	Action         string `json:"action"`
	Accepted       bool   `json:"accepted"`
	ReasonCode     string `json:"reason_code"`
	StreamClosed   bool   `json:"stream_closed"`
	CaptureOrdinal uint64 `json:"capture_ordinal"`
}

func TestCorrelationTranscriptEndToEnd(t *testing.T) {
	input, err := os.ReadFile(filepath.Join("testdata", "correlation_e2e.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture transcript
	if err := json.Unmarshal(input, &fixture); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if fixture.FixtureID == "" || fixture.EvidenceClass != "synthetic_protocol_fixture" ||
		fixture.RealProviderExchange || fixture.ClaimBearing || !fixture.DigestValuesAreSynthetic {
		t.Fatalf("fixture must remain explicitly synthetic and non-claim-bearing: %+v", fixture)
	}

	report := artifact{
		FixtureID: fixture.FixtureID, EvidenceClass: fixture.EvidenceClass,
		RealExchange: fixture.RealProviderExchange, ClaimBearing: fixture.ClaimBearing,
		DigestValuesAreSynthetic: fixture.DigestValuesAreSynthetic,
	}
	for _, scenario := range fixture.Scenarios {
		stream := correlation.New()
		for i, item := range scenario.Steps {
			var decision correlation.Decision
			switch item.Action {
			case "capture":
				if item.Capture == nil || item.Proposal != nil {
					t.Fatalf("%s step %d: malformed fixture action", scenario.Name, i+1)
				}
				decision = stream.Capture(*item.Capture)
				if !reflect.DeepEqual(decision.BrokerCapture, item.Capture) {
					t.Errorf("%s step %d: broker evidence changed: got %+v want %+v", scenario.Name, i+1, decision.BrokerCapture, item.Capture)
				}
			case "proposal":
				if item.Proposal == nil || item.Capture != nil {
					t.Fatalf("%s step %d: malformed fixture action", scenario.Name, i+1)
				}
				decision = stream.Validate(*item.Proposal)
				if !reflect.DeepEqual(decision.Proposal, item.Proposal) {
					t.Errorf("%s step %d: proposal evidence changed: got %+v want %+v", scenario.Name, i+1, decision.Proposal, item.Proposal)
				}

				var opaqueMatches []*correlation.BrokerCall
				var exactMatch *correlation.BrokerCall
				for _, prior := range scenario.Steps {
					if prior.Action != "capture" || !prior.WantAccepted || prior.Capture == nil {
						continue
					}
					if prior.Capture.ToolCallID.Opaque == item.Proposal.ToolCallID.Opaque {
						opaqueMatches = append(opaqueMatches, prior.Capture)
						if prior.Capture.ToolCallID == item.Proposal.ToolCallID {
							exactMatch = prior.Capture
						}
					}
				}
				var expectedCapture *correlation.BrokerCall
				switch {
				case exactMatch != nil:
					expectedCapture = exactMatch
				case len(opaqueMatches) == 1:
					expectedCapture = opaqueMatches[0]
				}
				if !reflect.DeepEqual(decision.BrokerCapture, expectedCapture) {
					t.Errorf("%s step %d: captured evidence changed: got %+v want %+v", scenario.Name, i+1, decision.BrokerCapture, expectedCapture)
				}
			default:
				t.Fatalf("%s step %d: unknown action %q", scenario.Name, i+1, item.Action)
			}
			if decision.Accepted != item.WantAccepted || string(decision.ReasonCode) != item.WantReason {
				t.Errorf("%s step %d: got accepted=%t reason=%q; want accepted=%t reason=%q", scenario.Name, i+1, decision.Accepted, decision.ReasonCode, item.WantAccepted, item.WantReason)
			}
			if decision.StreamClosed != item.WantStreamClosed || decision.CaptureOrdinal != item.WantCaptureOrdinal {
				t.Errorf("%s step %d: got closed=%t ordinal=%d; want closed=%t ordinal=%d", scenario.Name, i+1, decision.StreamClosed, decision.CaptureOrdinal, item.WantStreamClosed, item.WantCaptureOrdinal)
			}
			report.Decisions = append(report.Decisions, artifactDecision{
				Scenario: scenario.Name, Step: i + 1, Action: item.Action,
				Accepted: decision.Accepted, ReasonCode: string(decision.ReasonCode),
				StreamClosed: decision.StreamClosed, CaptureOrdinal: decision.CaptureOrdinal,
			})
		}
	}

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	artifactDir := "artifacts"
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(artifactDir, "correlation-e2e-report.json")
	if err := os.WriteFile(artifactPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	digestText := hex.EncodeToString(digest[:])
	t.Logf("synthetic E2E artifact: %s (sha256:%s)", artifactPath, digestText)
	fmt.Printf("CORRELATION_E2E_ARTIFACT=%s sha256:%s\n", artifactPath, digestText)
}