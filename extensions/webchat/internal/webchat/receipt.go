package webchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// productionReceiptSchema is the only declared schema version this importer
// accepts for a production receipt.
const productionReceiptSchema = "tbound-production-pi-receipt/v1"

// runReceipt is the union of the retained Pi run-receipt shapes this importer
// projects: the production receipt, the development CLI fixture receipt, and the
// native-runtime fixture line.
//
// Only these declared fields are read. Decoding deliberately does not use
// DisallowUnknownFields because a receipt legitimately carries more fields than
// the bounded projection; unknown fields are never read and never projected.
type runReceipt struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"`
	RunID         string `json:"run_id"`

	ClaimBearing     bool   `json:"claim_bearing"`
	ProviderExchange bool   `json:"provider_exchange"`
	Containment      string `json:"containment"`

	ProfileID                string `json:"profile_id"`
	ProfileHash              string `json:"profile_hash"`
	FrozenSourceDigest       string `json:"frozen_source_digest"`
	ContainmentProfile       string `json:"containment_profile"`
	ContainmentProfileDigest string `json:"containment_profile_digest"`
	ProviderProfile          string `json:"provider_profile"`
	TCBProofDigest           string `json:"tcb_proof_digest"`
	TCBProofSequence         uint64 `json:"tcb_proof_journal_sequence"`

	WorkerReady    bool `json:"worker_ready"`
	PromptAdmitted bool `json:"prompt_admitted"`
	TaskAdmitted   bool `json:"task_admitted"`
	TurnCompleted  bool `json:"turn_completed"`

	ProposalCount   uint64 `json:"proposal_count"`
	ProviderTurns   int    `json:"provider_turns"`
	ToolCallCount   int    `json:"tool_call_count"`
	ToolResultCount int    `json:"tool_result_count"`

	InitialGeneration string   `json:"initial_generation"`
	SourceGeneration  string   `json:"source_generation"`
	FinalGeneration   string   `json:"final_generation"`
	Generations       []string `json:"generations"`

	Settlement               string `json:"settlement"`
	Publication              string `json:"publication"`
	PublicationStatus        string `json:"publication_status"`
	PublicationTransactionID string `json:"publication_transaction_id"`
	Disposition              string `json:"disposition"`
	Success                  bool   `json:"success"`

	ToolLineage    []receiptToolLineage   `json:"tool_lineage"`
	ToolSequence   []string               `json:"tool_sequence"`
	BashSettlement *receiptBashSettlement `json:"bash_settlement"`
}

type receiptToolLineage struct {
	Sequence       uint64 `json:"sequence"`
	Tool           string `json:"tool"`
	ToolCallID     string `json:"tool_call_id"`
	GenerationFrom string `json:"generation_from"`
	GenerationTo   string `json:"generation_to"`
	TransitionID   string `json:"transition_id"`
	EffectID       string `json:"effect_id"`
}

type receiptBashSettlement struct {
	ToolCallID               string `json:"tool_call_id"`
	GenerationTo             string `json:"generation_to"`
	ExitCode                 int    `json:"exit_code"`
	ExitObserved             bool   `json:"exit_observed"`
	CommandContainmentStatus string `json:"command_containment_status"`
}

// ImportReceipt reads a retained Pi run receipt and projects it into a bounded
// replay observation. It consumes the terminal receipt only: it invents no
// transcript text, no per-tool decision, and no qualification. The receipt's
// file modification time is used as the observation time because the terminal
// receipt carries no per-item timestamps.
func ImportReceipt(path string) (Snapshot, error) {
	if len(path) == 0 {
		return Snapshot{}, errors.New("receipt path must not be empty")
	}
	raw, observedAt, err := readBoundedReceipt(path)
	if err != nil {
		return Snapshot{}, err
	}
	return ProjectReceipt(raw, observedAt)
}

// ProjectReceipt projects a receipt already held in memory. observedAt is the
// best available observation time; a zero value is replaced with the current
// time.
func ProjectReceipt(raw []byte, observedAt time.Time) (Snapshot, error) {
	if len(raw) > maxDocumentBytes {
		return Snapshot{}, errors.New("receipt exceeds 2 MiB")
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var receipt runReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return Snapshot{}, fmt.Errorf("decode receipt: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("receipt has trailing JSON")
	}

	switch receipt.Mode {
	case "production":
		if receipt.SchemaVersion != productionReceiptSchema {
			return Snapshot{}, fmt.Errorf("unsupported production receipt schema %q", receipt.SchemaVersion)
		}
		// The production receipt records tool names and top-level worker exit
		// fields; the nested development bash settlement does not belong to it, so
		// a production-shaped receipt cannot invent a per-tool outcome through it.
		receipt.BashSettlement = nil
	case "dev-fixture", "native-fixture":
	default:
		return Snapshot{}, fmt.Errorf("unsupported receipt mode %q", receipt.Mode)
	}

	// Refuse a receipt that asserts a claim this product cannot make. A
	// non-claim-bearing development receipt is the supported input.
	if receipt.ClaimBearing {
		return Snapshot{}, errors.New("receipt claims claim_bearing; the presentation gateway cannot project a claim-bearing receipt")
	}
	if receipt.Containment == "established" {
		return Snapshot{}, errors.New("receipt claims established containment; refused")
	}
	// The production receipt records a tool_sequence rather than a lineage. Fall
	// back to it so a real production receipt still projects its tool calls; the
	// synthesized ids are bounded and clearly derived.
	if len(receipt.ToolLineage) == 0 && len(receipt.ToolSequence) > 0 {
		if len(receipt.ToolSequence) > 64 {
			return Snapshot{}, errors.New("receipt tool sequence exceeds the projection bound")
		}
		lineage := make([]receiptToolLineage, 0, len(receipt.ToolSequence))
		for index, tool := range receipt.ToolSequence {
			lineage = append(lineage, receiptToolLineage{
				Sequence:   uint64(index + 1),
				Tool:       tool,
				ToolCallID: fmt.Sprintf("receipt-call-%02d", index+1),
			})
		}
		receipt.ToolLineage = lineage
	}

	for _, entry := range effectiveLineage(receipt) {
		switch Tool(entry.Tool) {
		case ToolRead, ToolWrite, ToolEdit, ToolBash:
		default:
			return Snapshot{}, fmt.Errorf("receipt records an unregistered tool %q", entry.Tool)
		}
	}

	snapshot := projectReceipt(receipt, observedAt.UTC())
	if err := ValidateSnapshot(snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("validate projected receipt: %w", err)
	}
	return cloneSnapshot(snapshot), nil
}

func effectiveLineage(receipt runReceipt) []receiptToolLineage {
	// A production receipt carries only tool names in tool_sequence. Synthesize
	// bounded call IDs so its tool calls are still projected.
	if len(receipt.ToolLineage) == 0 {
		for index, tool := range receipt.ToolSequence {
			receipt.ToolLineage = append(receipt.ToolLineage, receiptToolLineage{
				Sequence:   uint64(index + 1),
				Tool:       tool,
				ToolCallID: fmt.Sprintf("receipt-call-%02d", index+1),
			})
		}
	}
	return receipt.ToolLineage
}

func projectReceipt(receipt runReceipt, observedAt time.Time) Snapshot {
	warnings := []string{
		"Retained run receipt imported as an unverified replay observation. The gateway has not verified its provenance, policy or effects.",
		"Non-claim-bearing: G1=false; containment not-established; H1 unexecuted. A receipt is not a qualification result.",
	}
	if receipt.ProviderExchange {
		warnings = append(warnings, "The receipt reports a provider exchange. This does not qualify the governed CLI path.")
	}

	// keepReference returns a value only when it is a bounded reference; an
	// unusual or over-long value is dropped with a warning rather than projected.
	keepReference := func(name, value string) string {
		if value == "" {
			return ""
		}
		if err := validateReference(name, value, true); err != nil {
			warnings = append(warnings, name+" is not a bounded reference and is not projected")
			return ""
		}
		return value
	}

	sessionID := keepReference("receipt run id", receipt.RunID)
	if sessionID == "" {
		sessionID = "receipt-" + receipt.Mode
	}
	hasTurn := len(receipt.ToolLineage) > 0 || len(receipt.ToolSequence) > 0 || receipt.TurnCompleted || receipt.PromptAdmitted || receipt.TaskAdmitted

	items, missingOutcomes := receiptItems(receipt, observedAt)
	epoch := newEpoch()

	panels := Panels{
		Governance: GovernancePanel{
			Note: "The terminal run receipt does not record per-tool policy verdicts, so no decision counts are projected.",
		},
		Provenance: ProvenancePanel{
			Available:          false,
			ProfileID:          keepReference("receipt profile id", receipt.ProfileID),
			ProfileHash:        keepReference("receipt profile hash", receipt.ProfileHash),
			SourceDigest:       keepReference("receipt frozen source digest", receipt.FrozenSourceDigest),
			ContainmentProfile: keepReference("receipt containment profile", receipt.ContainmentProfile),
			ContainmentDigest:  keepReference("receipt containment profile digest", receipt.ContainmentProfileDigest),
			ProviderProfile:    keepReference("receipt provider profile", receipt.ProviderProfile),
			TCBProofDigest:     keepReference("receipt tcb proof digest", receipt.TCBProofDigest),
			TCBProofSequence:   receipt.TCBProofSequence,
			Note:               "Provenance references are reported by the receipt and are not independently verified here.",
		},
		Qualification: QualificationPanel{
			G1:           false,
			H1:           "unexecuted",
			Containment:  "not-established",
			ClaimBearing: false,
			Note:         "Fixed current truth. A retained receipt does not change it.",
		},
		Publication: publicationFromReceipt(receipt),
		Recovery:    recoveryFromReceipt(receipt, keepReference),
	}
	panels.Publication.TransactionID = keepReference("receipt publication transaction id", receipt.PublicationTransactionID)

	streams := []StreamStatus{
		{ID: "receipt", Label: "retained run receipt", Health: StreamComplete, Available: true, Reason: "terminal post-run receipt import"},
		{ID: "per-tool-outcome", Label: "per-tool outcome", Health: StreamObserving, Available: true, Reason: "derived from the receipt's tool lineage"},
		{ID: "transcript", Label: "transcript", Health: StreamUnavailable, Available: false, Reason: "the terminal receipt does not retain transcript text"},
	}
	if missingOutcomes > 0 {
		streams[1].Health = StreamGap
		warnings = append(warnings, fmt.Sprintf("%d tool call(s) have no terminal outcome recorded in the receipt; missing evidence is not a negative finding and is shown as UNKNOWN.", missingOutcomes))
	}
	if len(receipt.Generations) > 0 {
		generations := make([]string, 0, len(receipt.Generations))
		for _, generation := range receipt.Generations {
			if value := keepIdentifier(generation); value != "" {
				generations = append(generations, value)
			}
		}
		if len(generations) > 0 {
			warnings = append(warnings, "Generations observed: "+strings.Join(generations, ", ")+".")
		}
	}
	if settlement := keepIdentifier(receipt.Settlement); settlement != "" {
		warnings = append(warnings, "Receipt settlement: "+settlement+".")
	}

	return Snapshot{
		SchemaVersion: ContractVersion,
		Epoch:         epoch,
		Cursor:        formatCursor(epoch, uint64(len(items))),
		Source:        SourceReplay,
		Verification:  VerificationUnverifiedInput,
		GeneratedAt:   observedAt,
		Session: SessionHeader{
			SessionID: sessionID,
			Title:     receiptTitle(receipt),
			Status:    sessionStatusFromReceipt(receipt, hasTurn),
			CreatedAt: observedAt,
			UpdatedAt: observedAt,
			TurnCount: boolToInt(hasTurn),
		},
		Panels:   panels,
		Streams:  streams,
		Items:    items,
		Warnings: warnings,
	}
}

// receiptItems projects one lifecycle observation plus one activity and one
// result per recorded tool call. A result is UNKNOWN exactly when the receipt
// does not retain a terminal outcome for that call.
func receiptItems(receipt runReceipt, observedAt time.Time) ([]Item, int) {
	lineage := effectiveLineage(receipt)
	items := make([]Item, 0, 1+2*len(lineage)+1)
	base := func(id string, kind ItemType) Item {
		return Item{
			SchemaVersion: ContractVersion,
			ID:            id,
			Type:          kind,
			TurnID:        "turn-receipt-01",
			OccurredAt:    observedAt,
			Authority:     AuthorityDiagnostic,
			Verification:  VerificationUnverifiedInput,
		}
	}

	lifecycle := base("receipt-lifecycle", ItemMessage)
	lifecycle.Role = RoleSystem
	lifecycle.Text = lifecycleText(receipt)
	items = append(items, lifecycle)

	missing := 0
	for index, entry := range receipt.ToolLineage {
		label := fmt.Sprintf("%02d", index+1)

		activity := base("receipt-act-"+label, ItemActivity)
		activity.Kind = "proposal"
		activity.Tool = Tool(entry.Tool)
		activity.ToolCallID = entry.ToolCallID
		activity.State = ResultPending
		activity.Summary = fmt.Sprintf("%s call %s (%s to %s)", entry.Tool, boundedText(entry.ToolCallID), generationLabel(entry.GenerationFrom), generationLabel(entry.GenerationTo))
		items = append(items, activity)

		result := base("receipt-res-"+label, ItemResult)
		result.Tool = Tool(entry.Tool)
		result.ToolCallID = entry.ToolCallID
		result.State = ResultUnknown
		result.Summary = "no terminal outcome is retained in the receipt"
		if entry.EffectID != "" {
			result.Digest = entry.EffectID
		} else if entry.TransitionID != "" {
			result.Digest = entry.TransitionID
		}

		switch {
		case entry.Tool == string(ToolBash) && receipt.BashSettlement != nil && receipt.BashSettlement.ToolCallID == entry.ToolCallID:
			settlement := receipt.BashSettlement
			switch {
			case !settlement.ExitObserved:
				result.State = ResultUnknown
				result.Summary = "command exit was not observed"
			case settlement.ExitCode == 0:
				result.State = ResultSucceeded
				result.Summary = "command cell settled with exit code 0"
			default:
				result.State = ResultFailed
				result.Summary = fmt.Sprintf("command cell settled with exit code %d", settlement.ExitCode)
			}
			if status := keepIdentifier(settlement.CommandContainmentStatus); status != "" {
				result.Summary += "; containment " + status
			}
		default:
			// The terminal receipt records no per-tool verdict or terminal outcome
			// for any other call, so nothing is claimed as success. The recorded
			// generation transition is reported as context only.
			missing++
			if entry.GenerationTo != "" && entry.GenerationTo != entry.GenerationFrom {
				result.Summary = fmt.Sprintf("receipt records generation %s to %s for this call; no per-tool outcome is retained", generationLabel(entry.GenerationFrom), generationLabel(entry.GenerationTo))
			}
		}
		items = append(items, result)
	}

	if receipt.Settlement != "" || receipt.Success {
		tail := base("receipt-settlement", ItemMessage)
		tail.Role = RoleSystem
		tail.Text = settlementText(receipt)
		items = append(items, tail)
	}
	return items, missing
}

func lifecycleText(receipt runReceipt) string {
	parts := []string{}
	if receipt.WorkerReady {
		parts = append(parts, "worker ready")
	}
	if receipt.PromptAdmitted || receipt.TaskAdmitted {
		parts = append(parts, "prompt admitted")
	}
	if receipt.TurnCompleted {
		parts = append(parts, "turn completed")
	}
	if receipt.ProposalCount > 0 {
		parts = append(parts, fmt.Sprintf("%d proposals", receipt.ProposalCount))
	}
	if receipt.ProviderTurns > 0 {
		parts = append(parts, fmt.Sprintf("%d provider turns", receipt.ProviderTurns))
	}
	if receipt.ToolCallCount > 0 {
		parts = append(parts, fmt.Sprintf("%d tool calls / %d results", receipt.ToolCallCount, receipt.ToolResultCount))
	}
	from := receipt.SourceGeneration
	if from == "" {
		from = receipt.InitialGeneration
	}
	if from != "" || receipt.FinalGeneration != "" {
		parts = append(parts, fmt.Sprintf("generations %s to %s", generationLabel(from), generationLabel(receipt.FinalGeneration)))
	}
	if len(parts) == 0 {
		return "Retained run receipt with no lifecycle fields recorded."
	}
	return "Run receipt: " + strings.Join(parts, "; ") + "."
}

func settlementText(receipt runReceipt) string {
	settlement := keepIdentifier(receipt.Settlement)
	if settlement == "" {
		settlement = "not reported"
	}
	parts := []string{"Settlement " + settlement + "."}
	status := receipt.PublicationStatus
	if status == "" {
		status = receipt.Publication
	}
	if publication := keepIdentifier(status); publication != "" {
		parts = append(parts, "Publication "+publication+".")
	}
	parts = append(parts, "An UNKNOWN settlement is inspected and never blindly replayed.")
	return strings.Join(parts, " ")
}

func receiptTitle(receipt runReceipt) string {
	switch receipt.Mode {
	case "production":
		return "Production Pi run"
	case "native-fixture":
		return "Native runtime fixture (non-claim-bearing)"
	default:
		return "Development Pi run (non-claim-bearing)"
	}
}

func sessionStatusFromReceipt(receipt runReceipt, hasTurn bool) SessionStatus {
	switch receipt.Mode {
	case "production":
		if receipt.Success && (receipt.Settlement == "SUCCEEDED" || receipt.Settlement == "STOPPED") {
			return SessionCompleted
		}
		if receipt.Settlement == "FAILED" || receipt.Settlement == "DENIED" {
			return SessionFailed
		}
		return SessionUnknown
	default:
		// A completed turn plus an explicitly successful settlement is the only
		// combination reported as completed. Anything else stays UNKNOWN.
		if hasTurn && receipt.TurnCompleted && (receipt.Settlement == "SUCCEEDED" || receipt.Settlement == "STOPPED") {
			return SessionCompleted
		}
		if hasTurn && (receipt.Settlement == "FAILED" || receipt.Settlement == "DENIED") {
			return SessionFailed
		}
		return SessionUnknown
	}
}

func publicationFromReceipt(receipt runReceipt) PublicationPanel {
	status := receipt.PublicationStatus
	if status == "" {
		status = receipt.Publication
	}
	panel := PublicationPanel{State: PublicationUnavailable, Detail: "the receipt does not report a publication state"}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
	case "not-attempted", "not_attempted":
		panel.State = PublicationNotAttempted
		panel.Detail = "live publication was not attempted"
	case "succeeded", "published":
		panel.State = PublicationPublished
		panel.Detail = "the receipt reports publication SUCCEEDED; not independently verified"
	case "failed":
		panel.State = PublicationFailed
		panel.Detail = "the receipt reports publication FAILED"
	case "quarantined":
		panel.State = PublicationQuarantined
		panel.Detail = "publication is quarantined pending recovery inspection"
	case "pending", "staged":
		panel.State = PublicationPending
		panel.Detail = "publication is reported as pending"
	default:
		panel.State = PublicationUnknown
		panel.Detail = "unrecognized publication state"
		if value := keepIdentifier(status); value != "" {
			panel.Detail += " " + value
		}
	}
	return panel
}

func recoveryFromReceipt(receipt runReceipt, keepReference func(name, value string) string) RecoveryPanel {
	detail := ""
	// A disposition is a bounded label, not free text: it is dropped if it is not
	// a plain identifier rather than echoed.
	if disposition := keepIdentifier(receipt.Disposition); disposition != "" {
		detail = "Disposition " + disposition + ". "
	}
	panel := RecoveryPanel{
		Generation: keepIdentifier(receipt.FinalGeneration),
		Detail:     detail,
	}
	switch receipt.Settlement {
	case "SUCCEEDED", "STOPPED":
		panel.State = RecoveryNotRequired
		panel.Detail += "Settlement " + boundedText(receipt.Settlement) + " as reported by the receipt."
	case "FAILED", "DENIED":
		panel.State = RecoveryRequired
		panel.Detail += "Settlement " + boundedText(receipt.Settlement) + "; recovery must inspect state and must not replay an unsettled effect."
	case "UNKNOWN":
		panel.State = RecoveryUnknown
		panel.Detail += "Settlement UNKNOWN; recovery inspects workspace, process and commit-token state and must never blindly replay."
	case "":
		panel.State = RecoveryUnavailable
		panel.Detail += "The receipt does not report a settlement."
	default:
		panel.State = RecoveryUnknown
		panel.Detail += "Unrecognized settlement."
		if value := keepIdentifier(receipt.Settlement); value != "" {
			panel.Detail = strings.TrimSuffix(panel.Detail, ".") + " (" + value + ")."
		}
	}
	return panel
}

func generationLabel(value string) string {
	// A generation is an identifier such as g2; free text is never echoed.
	if value == "" {
		return "unknown"
	}
	if identifier := keepIdentifier(value); identifier != "" {
		return identifier
	}
	return "unknown"
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// boundedText truncates display text to the bounded label size and keeps the
// result valid UTF-8.
func boundedText(value string) string {
	if len(value) > MaxLabelBytes {
		value = value[:MaxLabelBytes]
	}
	return strings.ToValidUTF8(value, "")
}

// keepIdentifier returns a value only when it is a bounded identifier. Unlike
// boundedText it never echoes free text, so a receipt field cannot smuggle a
// path, credential or wrapper text into the projection.
func keepIdentifier(value string) string {
	if value == "" || !identifierPattern.MatchString(value) {
		return ""
	}
	return value
}

func readBoundedReceipt(path string) ([]byte, time.Time, error) {
	file, err := openRegularNoFollow(path)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("open receipt: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("inspect receipt: %w", err)
	}
	if info.Size() > maxDocumentBytes {
		return nil, time.Time{}, errors.New("receipt exceeds 2 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read receipt: %w", err)
	}
	if len(raw) > maxDocumentBytes {
		return nil, time.Time{}, errors.New("receipt exceeds 2 MiB")
	}
	return raw, info.ModTime().UTC(), nil
}
