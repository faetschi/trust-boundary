package webchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// identifierPattern bounds an identifier. Unlike a digest reference it admits no
// slash, so a relative path cannot masquerade as a session or generation ID.
var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// referencePattern bounds a digest-like reference, which may be base64-shaped.
// Values are never treated as paths and never reach the filesystem.
var referencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+=/-]{0,191}$`)

// reasonCodePattern bounds a supervisor reason code such as policy_rule_deny.
var reasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// epochPattern bounds a stream epoch. The epoch is interpolated into the SSE
// `id:` line, so it must not contain CR, LF, spaces or colons.
var epochPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validateSourceClass(source SourceClass) error {
	switch source {
	case SourceFixture, SourceLive, SourceReplay, SourceUnavailable:
		return nil
	default:
		return fmt.Errorf("unsupported source class %q", source)
	}
}

func validateAuthority(authority Authority) error {
	switch authority {
	case AuthorityAuthoritative, AuthorityObserved, AuthorityDiagnostic, AuthorityUnknown:
		return nil
	default:
		return fmt.Errorf("unsupported authority %q", authority)
	}
}

func validateVerification(scope Verification) error {
	switch scope {
	case VerificationUnavailable, VerificationUnverifiedInput, VerificationStructuralAudit:
		return nil
	default:
		return fmt.Errorf("unsupported verification scope %q", scope)
	}
}

func validateItemType(kind ItemType) error {
	switch kind {
	case ItemMessage, ItemActivity, ItemDecision, ItemResult:
		return nil
	default:
		return fmt.Errorf("unsupported item type %q", kind)
	}
}

func validateRole(role Role) error {
	switch role {
	case RoleUser, RoleAssistant, RoleSystem:
		return nil
	default:
		return fmt.Errorf("unsupported transcript role %q", role)
	}
}

func validateVerdict(verdict Verdict) error {
	switch verdict {
	case VerdictAllow, VerdictDeny, VerdictUnknown:
		return nil
	default:
		return fmt.Errorf("unsupported verdict %q", verdict)
	}
}

func validateTool(tool Tool) error {
	switch tool {
	case ToolRead, ToolWrite, ToolEdit, ToolBash:
		return nil
	default:
		return fmt.Errorf("unsupported tool %q", tool)
	}
}

func validateResultState(state ResultState) error {
	switch state {
	case ResultPending, ResultSucceeded, ResultDenied, ResultFailed, ResultUnknown, ResultLost:
		return nil
	default:
		return fmt.Errorf("unsupported result state %q", state)
	}
}

func validateSessionStatus(status SessionStatus) error {
	switch status {
	case SessionPending, SessionRunning, SessionCompleted, SessionFailed, SessionCancelled, SessionUnknown, SessionUnavailable:
		return nil
	default:
		return fmt.Errorf("unsupported session status %q", status)
	}
}

func validatePublicationState(state PublicationState) error {
	switch state {
	case PublicationUnavailable, PublicationNotAttempted, PublicationPending, PublicationPublished, PublicationFailed, PublicationQuarantined, PublicationUnknown:
		return nil
	default:
		return fmt.Errorf("unsupported publication state %q", state)
	}
}

func validateRecoveryState(state RecoveryState) error {
	switch state {
	case RecoveryUnavailable, RecoveryNotRequired, RecoveryReady, RecoveryRequired, RecoveryQuarantined, RecoveryUnknown:
		return nil
	default:
		return fmt.Errorf("unsupported recovery state %q", state)
	}
}

func validateStreamHealth(health StreamHealth) error {
	switch health {
	case StreamObserving, StreamUnavailable, StreamGap, StreamComplete:
		return nil
	default:
		return fmt.Errorf("unsupported stream health %q", health)
	}
}

func defaultVerification(source SourceClass) Verification {
	if source == SourceUnavailable {
		return VerificationUnavailable
	}
	return VerificationUnverifiedInput
}

func validateText(name, value string, limit int, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > limit {
		return fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	// Display text is single-line: reject control characters so a value cannot
	// smuggle terminal escapes or line structure into the projection. Newline and
	// tab are the only permitted whitespace controls; the C1 range is rejected too.
	for _, r := range value {
		if r < 0x20 && r != '\n' && r != '\t' {
			return fmt.Errorf("%s contains a control character", name)
		}
		if r >= 0x7f && r <= 0x9f {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

// validateIdentifier bounds an identifier-like value: no path separators.
func validateIdentifier(name, value string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s is required", name)
	}
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s is not a bounded identifier", name)
	}
	return nil
}

func validateReference(name, value string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s is required", name)
	}
	if !referencePattern.MatchString(value) {
		return fmt.Errorf("%s is not a bounded reference", name)
	}
	return nil
}

func validateReasonCode(name, value string) error {
	if !reasonCodePattern.MatchString(value) {
		return fmt.Errorf("%s is not a registered reason code", name)
	}
	return nil
}

// forbiddenField reports a field that does not belong to the item's type.
func forbiddenField(item Item, fields ...string) error {
	for _, field := range fields {
		present := false
		switch field {
		case "role":
			present = item.Role != ""
		case "text":
			present = item.Text != "" || item.TextTruncated
		case "kind":
			present = item.Kind != ""
		case "summary":
			present = item.Summary != ""
		case "detail":
			present = item.Detail != ""
		case "state":
			present = item.State != ""
		case "tool":
			present = item.Tool != ""
		case "tool_call_id":
			present = item.ToolCallID != ""
		case "verdict":
			present = item.Verdict != ""
		case "reason_code":
			present = item.ReasonCode != ""
		case "policy_digest":
			present = item.PolicyDigest != ""
		case "lossy":
			present = item.Lossy
		case "digest":
			present = item.Digest != ""
		}
		if present {
			return fmt.Errorf("item %q of type %q must not carry field %q", item.ID, item.Type, field)
		}
	}
	return nil
}

func validateItem(item Item, expectedSource SourceClass, expectedScope Verification) error {
	if item.SchemaVersion != ContractVersion {
		return fmt.Errorf("item %q has unsupported schema version", item.ID)
	}
	if err := validateIdentifier("item id", item.ID, false); err != nil {
		return err
	}
	if err := validateItemType(item.Type); err != nil {
		return err
	}
	if err := validateIdentifier("turn id", item.TurnID, true); err != nil {
		return err
	}
	if err := validateAuthority(item.Authority); err != nil {
		return err
	}
	if item.Authority == AuthorityAuthoritative {
		return fmt.Errorf("item %q authoritative authority is unavailable to this presentation contract", item.ID)
	}
	// Only a live projection may carry the observed authority. A fixture, replay
	// or receipt that claims it would be able to pass the client's verified gate.
	if item.Authority == AuthorityObserved && expectedSource != SourceLive {
		return fmt.Errorf("item %q cannot claim observed authority outside a live projection", item.ID)
	}
	// The scope is required rather than defaulted: defaulting here would let an
	// item be stored with an empty scope and later read as verified.
	if item.Verification == "" {
		return fmt.Errorf("item %q verification scope is required", item.ID)
	}
	scope := item.Verification
	if err := validateVerification(scope); err != nil {
		return err
	}
	if expectedScope != "" && scope != expectedScope {
		return fmt.Errorf("item %q verification scope does not match snapshot", item.ID)
	}
	if item.OccurredAt.IsZero() || item.OccurredAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return fmt.Errorf("item %q has invalid occurred_at", item.ID)
	}
	switch item.Type {
	case ItemMessage:
		if err := validateRole(item.Role); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if strings.TrimSpace(item.Text) == "" && !item.TextTruncated {
			return fmt.Errorf("item %q transcript text is required", item.ID)
		}
		if err := validateText("item text", item.Text, MaxTextBytes, true); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := forbiddenField(item, "kind", "summary", "detail", "state", "tool", "tool_call_id", "verdict", "reason_code", "policy_digest", "lossy", "digest"); err != nil {
			return err
		}
	case ItemActivity:
		if err := validateTool(item.Tool); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateResultState(item.State); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateText("activity summary", item.Summary, MaxTextBytes, false); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateText("activity detail", item.Detail, MaxDetailBytes, true); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		// A tool activity must be correlatable so a filter can always retain it
		// beside its decision and result.
		if err := validateIdentifier("activity tool call id", item.ToolCallID, false); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := forbiddenField(item, "role", "text", "verdict", "reason_code", "policy_digest", "lossy", "digest"); err != nil {
			return err
		}
	case ItemDecision:
		if err := validateTool(item.Tool); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateIdentifier("tool call id", item.ToolCallID, false); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateVerdict(item.Verdict); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateReasonCode("reason code", item.ReasonCode); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateReference("policy digest", item.PolicyDigest, true); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateText("decision summary", item.Summary, MaxTextBytes, true); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := forbiddenField(item, "role", "text", "kind", "detail", "state", "lossy", "digest"); err != nil {
			return err
		}
	case ItemResult:
		if err := validateTool(item.Tool); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateIdentifier("tool call id", item.ToolCallID, false); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateResultState(item.State); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateText("result summary", item.Summary, MaxTextBytes, true); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateText("result detail", item.Detail, MaxDetailBytes, true); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := validateReference("result digest", item.Digest, true); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if err := forbiddenField(item, "role", "text", "kind", "verdict", "reason_code", "policy_digest"); err != nil {
			return err
		}
	}
	return nil
}

func validateStream(stream StreamStatus) error {
	if err := validateIdentifier("stream id", stream.ID, false); err != nil {
		return err
	}
	if err := validateText("stream label", stream.Label, MaxLabelBytes, false); err != nil {
		return err
	}
	if err := validateStreamHealth(stream.Health); err != nil {
		return err
	}
	if err := validateText("stream reason", stream.Reason, MaxTextBytes, true); err != nil {
		return err
	}
	if stream.Health == StreamUnavailable && stream.Available {
		return fmt.Errorf("unavailable stream %q cannot be available", stream.ID)
	}
	return nil
}

func validateGap(gap GapRecord) error {
	if err := validateIdentifier("gap id", gap.ID, false); err != nil {
		return err
	}
	if err := validateReasonCode("gap reason", gap.Reason); err != nil {
		return err
	}
	if gap.LastMissing < gap.FirstMissing {
		return fmt.Errorf("gap %q has an inverted range", gap.ID)
	}
	if gap.RecordedAt.IsZero() {
		return fmt.Errorf("gap %q requires recorded_at", gap.ID)
	}
	return validateText("gap detail", gap.Detail, MaxTextBytes, true)
}

func validatePanels(panels Panels) error {
	if err := validateReference("governance policy digest", panels.Governance.PolicyDigest, true); err != nil {
		return err
	}
	if panels.Governance.AllowCount < 0 || panels.Governance.DenyCount < 0 || panels.Governance.UnknownCount < 0 {
		return errors.New("governance decision counts must not be negative")
	}
	if err := validateText("governance note", panels.Governance.Note, MaxTextBytes, true); err != nil {
		return err
	}

	for name, value := range map[string]string{
		"provenance profile hash":     panels.Provenance.ProfileHash,
		"provenance source digest":    panels.Provenance.SourceDigest,
		"provenance containment hash": panels.Provenance.ContainmentDigest,
		"provenance tcb digest":       panels.Provenance.TCBProofDigest,
	} {
		if err := validateReference(name, value, true); err != nil {
			return err
		}
	}
	for name, value := range map[string]string{
		"provenance profile id":      panels.Provenance.ProfileID,
		"provenance containment":     panels.Provenance.ContainmentProfile,
		"provenance provider":        panels.Provenance.ProviderProfile,
		"provenance runtime closure": panels.Provenance.RuntimeClosure,
	} {
		if err := validateIdentifier(name, value, true); err != nil {
			return err
		}
	}
	if err := validateText("provenance note", panels.Provenance.Note, MaxTextBytes, true); err != nil {
		return err
	}

	if err := validatePublicationState(panels.Publication.State); err != nil {
		return err
	}
	if err := validateReference("publication transaction id", panels.Publication.TransactionID, true); err != nil {
		return err
	}
	if err := validateText("publication detail", panels.Publication.Detail, MaxTextBytes, true); err != nil {
		return err
	}

	if err := validateRecoveryState(panels.Recovery.State); err != nil {
		return err
	}
	if err := validateReference("recovery generation", panels.Recovery.Generation, true); err != nil {
		return err
	}
	if err := validateText("recovery detail", panels.Recovery.Detail, MaxTextBytes, true); err != nil {
		return err
	}

	if err := validateText("qualification h1", panels.Qualification.H1, MaxLabelBytes, false); err != nil {
		return err
	}
	if err := validateText("qualification containment", panels.Qualification.Containment, MaxLabelBytes, false); err != nil {
		return err
	}
	if err := validateText("qualification note", panels.Qualification.Note, MaxTextBytes, true); err != nil {
		return err
	}
	return nil
}

// ValidateSnapshot verifies the bounded contract before an observation is
// accepted. It does not verify policy, evidence, or external authenticity;
// those remain owned by their source components.
func ValidateSnapshot(snapshot Snapshot) error {
	if snapshot.SchemaVersion != ContractVersion {
		return fmt.Errorf("unsupported snapshot schema version %q", snapshot.SchemaVersion)
	}
	if err := validateSourceClass(snapshot.Source); err != nil {
		return err
	}
	scope := snapshot.Verification
	if scope == "" {
		scope = defaultVerification(snapshot.Source)
	}
	if err := validateVerification(scope); err != nil {
		return err
	}
	if snapshot.Source == SourceUnavailable && scope != VerificationUnavailable {
		return errors.New("unavailable snapshot must use unavailable verification scope")
	}
	if !epochPattern.MatchString(snapshot.Epoch) {
		return errors.New("snapshot epoch is invalid")
	}
	if err := validateCursor(snapshot.Cursor, snapshot.Epoch); err != nil {
		return err
	}
	if cursorEpoch, cursorSequence, ok := parseCursor(snapshot.Cursor); ok && cursorEpoch == snapshot.Epoch {
		// When sequences are provided they must agree with the cursor, otherwise
		// the store would silently normalize away a real discontinuity.
		if len(snapshot.Items) > 0 {
			if last := snapshot.Items[len(snapshot.Items)-1].Sequence; last != 0 && cursorSequence != last {
				return errors.New("snapshot cursor does not match the item sequence")
			}
		}
	}
	if snapshot.GeneratedAt.IsZero() {
		return errors.New("snapshot generated_at is required")
	}
	if len(snapshot.Items) > MaxItems || len(snapshot.Streams) > MaxStreams || len(snapshot.Warnings) > MaxWarnings || len(snapshot.Gaps) > MaxGaps {
		return errors.New("snapshot exceeds a configured collection bound")
	}
	if err := validateSession(snapshot.Session); err != nil {
		return err
	}
	if err := validatePanels(snapshot.Panels); err != nil {
		return err
	}
	seenStreams := make(map[string]struct{}, len(snapshot.Streams))
	for _, stream := range snapshot.Streams {
		if err := validateStream(stream); err != nil {
			return err
		}
		if _, ok := seenStreams[stream.ID]; ok {
			return fmt.Errorf("duplicate stream %q", stream.ID)
		}
		seenStreams[stream.ID] = struct{}{}
	}
	seenItems := make(map[string]struct{}, len(snapshot.Items))
	for _, item := range snapshot.Items {
		if err := validateItem(item, snapshot.Source, scope); err != nil {
			return err
		}
		if _, ok := seenItems[item.ID]; ok {
			return fmt.Errorf("duplicate item %q", item.ID)
		}
		seenItems[item.ID] = struct{}{}
	}
	// A provided sequence must be complete and contiguous from 1. The store
	// renumbers input items, so a partial or gapped sequence would otherwise be
	// silently "repaired" and an input discontinuity erased.
	provided := 0
	for _, item := range snapshot.Items {
		if item.Sequence != 0 {
			provided++
		}
	}
	if provided > 0 {
		if provided != len(snapshot.Items) {
			return errors.New("item sequence must be either unset for every element or set for every element")
		}
		for index, item := range snapshot.Items {
			if item.Sequence != uint64(index+1) {
				return errors.New("item sequence must be contiguous from 1")
			}
		}
	}
	for _, gap := range snapshot.Gaps {
		if err := validateGap(gap); err != nil {
			return err
		}
	}
	for _, warning := range snapshot.Warnings {
		if err := validateText("warning", warning, MaxTextBytes, false); err != nil {
			return err
		}
	}
	return nil
}

func validateSession(session SessionHeader) error {
	if err := validateIdentifier("session id", session.SessionID, true); err != nil {
		return err
	}
	if err := validateIdentifier("workflow id", session.WorkflowID, true); err != nil {
		return err
	}
	if err := validateText("session title", session.Title, MaxLabelBytes, true); err != nil {
		return err
	}
	if err := validateSessionStatus(session.Status); err != nil {
		return err
	}
	if err := validateIdentifier("session profile", session.Profile, true); err != nil {
		return err
	}
	if err := validateIdentifier("session pi version", session.PiVersion, true); err != nil {
		return err
	}
	if session.TurnCount < 0 {
		return errors.New("session turn count must not be negative")
	}
	return nil
}

func validateCursor(cursor, epoch string) error {
	parsedEpoch, _, ok := parseCursor(cursor)
	if !ok || parsedEpoch != epoch {
		return errors.New("snapshot cursor is invalid")
	}
	return nil
}

func parseCursor(cursor string) (epoch string, sequence uint64, ok bool) {
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", 0, false
	}
	var value uint64
	for _, char := range parts[1] {
		if char < '0' || char > '9' {
			return "", 0, false
		}
		if value > (^uint64(0)-uint64(char-'0'))/10 {
			return "", 0, false
		}
		value = value*10 + uint64(char-'0')
	}
	return parts[0], value, true
}

func formatCursor(epoch string, sequence uint64) string {
	return fmt.Sprintf("%s:%d", epoch, sequence)
}

// downgradeDocument rewrites caller-supplied input so it can never present
// itself as verified or authoritative. It is the boundary between an untrusted
// file and the bounded projection.
func downgradeDocument(snapshot *Snapshot) {
	snapshot.Verification = VerificationUnverifiedInput
	for index := range snapshot.Items {
		snapshot.Items[index].Authority = AuthorityDiagnostic
		snapshot.Items[index].Verification = VerificationUnverifiedInput
	}
	// A fixture or replay may illustrate provenance, provenance availability
	// and qualification are supervisor facts that a document cannot assert.
	snapshot.Panels.Provenance.Available = false
	snapshot.Panels.Qualification.G1 = false
	snapshot.Panels.Qualification.ClaimBearing = false
}

// maxDocumentBytes bounds an observation document read from disk.
const maxDocumentBytes = 2 << 20

// decodeDocument parses, refuses, downgrades, and validates a candidate
// observation. Every document is untrusted: it is always forced to unverified
// diagnostic input and its qualification panel is normalized before it is
// accepted.
func decodeDocument(raw []byte, expected SourceClass) (Snapshot, error) {
	if len(raw) > maxDocumentBytes {
		return Snapshot{}, errors.New("observation document exceeds 2 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Snapshot{}, fmt.Errorf("decode observation document: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("observation document has trailing JSON")
	}
	if document.SchemaVersion != DocumentVersion {
		return Snapshot{}, fmt.Errorf("unsupported observation document schema %q", document.SchemaVersion)
	}
	if expected == SourceLive {
		return Snapshot{}, errors.New("a live projection cannot be loaded from a document")
	}
	if document.Snapshot.Source != expected {
		return Snapshot{}, fmt.Errorf("observation document source %q does not match requested %q", document.Snapshot.Source, expected)
	}
	if document.Snapshot.Panels.Qualification.Containment != "not-established" && document.Snapshot.Panels.Qualification.Containment != "unavailable" {
		return Snapshot{}, errors.New("a document cannot claim containment qualification")
	}
	if document.Snapshot.Panels.Qualification.H1 != "unexecuted" && document.Snapshot.Panels.Qualification.H1 != "unavailable" {
		return Snapshot{}, errors.New("a document cannot claim an executed research protocol")
	}
	downgradeDocument(&document.Snapshot)
	if err := ValidateSnapshot(document.Snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("validate observation document: %w", err)
	}
	return cloneSnapshot(document.Snapshot), nil
}

func readDocument(path string, expected SourceClass) (Snapshot, error) {
	if strings.TrimSpace(path) == "" {
		return Snapshot{}, errors.New("observation document path must not be empty")
	}
	file, err := openRegularNoFollow(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open observation document: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect opened observation document: %w", err)
	}
	if info.Size() > maxDocumentBytes {
		return Snapshot{}, errors.New("observation document exceeds 2 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read observation document: %w", err)
	}
	return decodeDocument(raw, expected)
}

// LoadFixture loads a non-claim-bearing development fixture. It is deliberately
// a separate API from replay so callers must opt in explicitly.
func LoadFixture(path string) (Snapshot, error) { return readDocument(path, SourceFixture) }

// LoadFixtureBytes loads an embedded development fixture. It applies the same
// downgrade and validation as LoadFixture.
func LoadFixtureBytes(raw []byte) (Snapshot, error) { return decodeDocument(raw, SourceFixture) }

// LoadReplay loads retained observations without contacting a provider or
// executing tools. A replay document is still caller-supplied and unverified.
func LoadReplay(path string) (Snapshot, error) { return readDocument(path, SourceReplay) }
