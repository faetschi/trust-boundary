package protocol

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/correlation"
)

func allowedBashResult() Result {
	return Result{SchemaVersion: ResultSchemaVersion, ResponseID: &correlation.Identifier{Issuer: "tbound-provider", Opaque: "response-1"},
		ToolCallID: "call-1", Tool: "bash", Sequence: 1, CanonicalArgumentsDigest: CanonicalizationProfile + ":sha256:" + strings.Repeat("a", 64),
		Verdict: "ALLOW", ReasonCode: "policy_rule_allow", PolicyDigest: "sha256:" + strings.Repeat("b", 64)}
}

// An ALLOWed command that ran and failed keeps the ALLOW verdict and its gate reason code; the failure is the
// separate stable public status member, so the verdict list of the receipt does not change.
func TestResultStatusMarksAFailedCommandWithoutChangingTheVerdict(t *testing.T) {
	result := allowedBashResult()
	result.Status = ResultStatusCommandFailed
	encoded, err := MarshalResult(result)
	if err != nil {
		t.Fatalf("a failed ALLOWed bash result must be encodable: %v", err)
	}
	decoded, err := DecodeResult(encoded)
	if err != nil || decoded.Status != ResultStatusCommandFailed || decoded.Verdict != "ALLOW" || decoded.ReasonCode != "policy_rule_allow" {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
	plain, err := MarshalResult(allowedBashResult())
	if err != nil || strings.Contains(string(plain), "status") {
		t.Fatalf("a Result without a failure must not carry a status member: %s %v", plain, err)
	}
}

func TestResultStatusIsRefusedWhereItHasNoMeaning(t *testing.T) {
	denied := allowedBashResult()
	denied.Verdict, denied.ReasonCode, denied.Status = "DENY", "policy_rule_deny", ResultStatusCommandFailed
	read := allowedBashResult()
	read.Tool, read.Status = "read", ResultStatusCommandFailed
	unknown := allowedBashResult()
	unknown.Status = "it failed with exit 3"
	for name, result := range map[string]Result{"deny": denied, "non-bash tool": read, "unregistered value": unknown} {
		if _, err := MarshalResult(result); err == nil {
			t.Errorf("%s: a status was accepted", name)
		}
	}
}
