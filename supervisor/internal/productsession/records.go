//go:build linux

package productsession

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"tbound/supervisor/internal/broker/protocol"
)

const (
	catalogSchema = "tbound-product-catalog/v1"

	kindOpened                  = "product_catalog_opened"
	kindSessionCreated          = "product_session_created"
	kindTurnAdmitted            = "product_turn_admitted"
	kindTurnLinked              = "product_turn_linked"
	kindTurnTerminal            = "product_turn_terminal"
	kindTurnAwaitingReview      = "product_turn_awaiting_review"
	kindTurnAbandoned           = "product_turn_abandoned"
	kindTurnUnknown             = "product_turn_unknown"
	kindTurnRefused             = "product_turn_refused"
	kindTurnCancelRequested     = "product_turn_cancel_requested"
	kindTurnResolutionRequested = "product_turn_resolution_requested"

	opCreateSession = "create_session"
	opSubmitTurn    = "submit_turn"
	opCancelTurn    = "cancel_turn"
	opResolveTurn   = "resolve_turn"

	// MaxPromptBytes matches providerbridge.
	MaxPromptBytes = 16 << 10
	// MaxReceiptBytes bounds a product turn receipt.
	MaxReceiptBytes = 64 << 10

	maxPathBytes   = 4096
	maxReasonBytes = 256
)

type header struct {
	SchemaVersion string `json:"schema_version"`
}

func (h header) version() string { return h.SchemaVersion }

var hdr = header{SchemaVersion: catalogSchema}

type openedRecord struct {
	header
	OwnerEpoch         uint64 `json:"owner_epoch"`
	RegistrationDigest string `json:"registration_digest"`
}

type sessionCreatedRecord struct {
	header
	SessionID       string    `json:"session_id"`
	Principal       Principal `json:"principal"`
	ProjectID       string    `json:"project_id"`
	ProviderProfile string    `json:"provider_profile"`
	PublicationMode string    `json:"publication_mode"`
	Budget          Budget    `json:"budget"`
	ReadOnly        bool      `json:"read_only,omitempty"`
	BaselineDigest  string    `json:"baseline_digest"`
	RequestKey      string    `json:"request_key"`
	PayloadDigest   string    `json:"payload_digest"`
}

type turnAdmittedRecord struct {
	header
	TurnID                   string    `json:"turn_id"`
	SessionID                string    `json:"session_id"`
	Ordinal                  int       `json:"ordinal"`
	Principal                Principal `json:"principal"`
	RequestKey               string    `json:"request_key"`
	PayloadDigest            string    `json:"payload_digest"`
	Prompt                   string    `json:"prompt"`
	PromptDigest             string    `json:"prompt_digest"`
	ProjectID                string    `json:"project_id"`
	PolicyDigest             string    `json:"policy_digest"`
	BashOutputPaths          []string  `json:"bash_output_paths"`
	PublicationMode          string    `json:"publication_mode"`
	BaselineDigest           string    `json:"baseline_digest"`
	ExpectedBaselineDigest   string    `json:"expected_baseline_digest,omitempty"`
	PredecessorTurnID        string    `json:"predecessor_turn_id"`
	PredecessorReceiptDigest string    `json:"predecessor_receipt_digest"`
	Reservation              Budget    `json:"reservation"`
	SessionRevision          uint64    `json:"session_revision"`
	// Scope is the admission scope in force for the turn; absent is the exact-path rule.
	Scope *ProductScope `json:"scope,omitempty"`
}

type turnLinkedRecord struct {
	header
	TurnID string   `json:"turn_id"`
	Link   TurnLink `json:"link"`
}

type turnTerminalRecord struct {
	header
	TurnID               string `json:"turn_id"`
	Outcome              string `json:"outcome"`
	Settlement           string `json:"settlement"`
	Receipt              []byte `json:"receipt"`
	ReceiptDigest        string `json:"receipt_digest"`
	Usage                Budget `json:"usage"`
	ResultBaselineDigest string `json:"result_baseline_digest"`
	ContextEligible      bool   `json:"context_eligible"`
	Refund               Budget `json:"refund"`
	Reason               string `json:"reason,omitempty"`
}

type turnAwaitingReviewRecord struct {
	header
	TurnID               string `json:"turn_id"`
	Settlement           string `json:"settlement"`
	Receipt              []byte `json:"receipt"`
	ReceiptDigest        string `json:"receipt_digest"`
	Usage                Budget `json:"usage"`
	Refund               Budget `json:"refund"`
	CandidateDigest      string `json:"candidate_digest"`
	Removed              int    `json:"removed,omitempty"`
	ReviewTimeoutSeconds int64  `json:"review_timeout_seconds"`
}

type turnAbandonedRecord struct {
	header
	TurnID string `json:"turn_id"`
	Refund Budget `json:"refund"`
}

type turnRefusedRecord struct {
	header
	TurnID string `json:"turn_id"`
	Code   Code   `json:"code"`
	Refund Budget `json:"refund"`
}

type turnCancelRequestedRecord struct {
	header
	TurnID          string    `json:"turn_id"`
	SessionID       string    `json:"session_id"`
	Principal       Principal `json:"principal"`
	RequestKey      string    `json:"request_key"`
	PayloadDigest   string    `json:"payload_digest"`
	SessionRevision uint64    `json:"session_revision"`
}

type turnResolutionRequestedRecord struct {
	header
	TurnID          string    `json:"turn_id"`
	SessionID       string    `json:"session_id"`
	Principal       Principal `json:"principal"`
	RequestKey      string    `json:"request_key"`
	PayloadDigest   string    `json:"payload_digest"`
	CandidateDigest string    `json:"candidate_digest"`
	Decision        string    `json:"decision"`
	SessionRevision uint64    `json:"session_revision"`
}

type turnUnknownRecord struct {
	header
	TurnID string `json:"turn_id"`
	Reason string `json:"reason"`
}

// decode strictly decodes one record body: strict JSON, no unknown fields, and
// the catalog schema version.
func decode(data []byte, v interface{ version() string }) error {
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return fail(CodeInvalidRequest, "record is not strict JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil || v.version() != catalogSchema {
		return fail(CodeInvalidRequest, "record does not match its schema")
	}
	return nil
}

// payloadDigest is the request-key payload digest of one operation request.
func payloadDigest(operation string, request any) string {
	encoded, err := json.Marshal(request)
	if err != nil { // request structs hold only strings and integers
		panic("productsession: marshal request: " + err.Error())
	}
	return sha256Digest([]byte("tbound-product-request/v1\n" + operation + "\n" + string(encoded)))
}

func validPrompt(p string) bool {
	if len(p) > MaxPromptBytes || !utf8.ValidString(p) || strings.TrimSpace(p) == "" {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func validReceipt(b []byte) bool {
	return len(b) > 0 && len(b) <= MaxReceiptBytes && protocol.ValidateStrictJSON(b) == nil
}

func validPath(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > maxPathBytes || !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
