package governanceview

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

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func validateMode(mode Mode) error {
	switch mode {
	case ModeFixture, ModeLive, ModeReplay, ModeUnavailable:
		return nil
	default:
		return fmt.Errorf("unsupported observation mode %q", mode)
	}
}

func validateState(state State) error {
	switch state {
	case StatePending, StateDenied, StateFailed, StateWithheld, StateUnknown, StateCompleted:
		return nil
	default:
		return fmt.Errorf("unsupported observation state %q", state)
	}
}

func validateAuthority(authority Authority) error {
	switch authority {
	case AuthorityAuthoritative, AuthorityDiagnostic, AuthorityUnknown:
		return nil
	default:
		return fmt.Errorf("unsupported observation authority %q", authority)
	}
}

func defaultVerificationScope(mode Mode) VerificationScope {
	if mode == ModeUnavailable {
		return VerificationUnavailable
	}
	return VerificationUnverifiedInput
}

func validateVerificationScope(scope VerificationScope) error {
	switch scope {
	case VerificationUnavailable, VerificationUnverifiedInput, VerificationStructuralAudit:
		return nil
	default:
		return fmt.Errorf("unsupported verification scope %q", scope)
	}
}

func validateText(name, value string, limit int, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > limit {
		return fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s contains NUL", name)
	}
	return nil
}

func validateIdentifier(name, value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s is not a registered identifier", name)
	}
	return nil
}

func validateLinks(links Links) error {
	values := []struct {
		name, value string
	}{
		{"provider_id", links.ProviderID}, {"pi_id", links.PiID},
		{"proposal_id", links.ProposalID}, {"decision_id", links.DecisionID},
		{"effect_id", links.EffectID}, {"generation_id", links.GenerationID},
	}
	count := 0
	for _, item := range values {
		if item.value != "" {
			count++
		}
		if err := validateIdentifier(item.name, item.value, true); err != nil {
			return err
		}
	}
	if count > MaxLinks {
		return errors.New("record link count exceeds limit")
	}
	return nil
}

func validateRecord(record Record, expectedMode Mode, expectedScope VerificationScope) error {
	if record.SchemaVersion != ContractVersion {
		return fmt.Errorf("record %q has unsupported schema version", record.ID)
	}
	if err := validateIdentifier("record id", record.ID, false); err != nil {
		return err
	}
	if err := validateIdentifier("source id", record.SourceID, false); err != nil {
		return err
	}
	if err := validateMode(record.Mode); err != nil {
		return err
	}
	if expectedMode != "" && record.Mode != expectedMode {
		return fmt.Errorf("record %q mode does not match snapshot mode", record.ID)
	}
	recordScope := record.Verification
	if recordScope == "" {
		recordScope = defaultVerificationScope(record.Mode)
	}
	if err := validateVerificationScope(recordScope); err != nil {
		return err
	}
	if expectedScope != "" && recordScope != expectedScope {
		return fmt.Errorf("record %q verification scope does not match snapshot", record.ID)
	}
	if record.Namespace == "" {
		return fmt.Errorf("record %q namespace is required", record.ID)
	}
	switch record.Namespace {
	case NamespaceProvider, NamespacePi, NamespaceProposal, NamespaceDecision, NamespaceEffect, NamespaceGeneration, NamespaceSession, NamespaceSource:
	default:
		return fmt.Errorf("record %q has unsupported namespace %q", record.ID, record.Namespace)
	}
	if err := validateText("record kind", record.Kind, 128, false); err != nil {
		return err
	}
	if err := validateState(record.Status); err != nil {
		return err
	}
	if err := validateAuthority(record.Authority); err != nil {
		return err
	}
	if record.Authority == AuthorityAuthoritative {
		return fmt.Errorf("record %q authoritative authority is unavailable to this presentation contract", record.ID)
	}
	if record.OccurredAt.IsZero() || record.OccurredAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return fmt.Errorf("record %q has invalid occurred_at", record.ID)
	}
	if err := validateText("record summary", record.Summary, MaxTextBytes, false); err != nil {
		return err
	}
	if err := validateText("record detail", record.Detail, MaxTextBytes, true); err != nil {
		return err
	}
	if err := validateLinks(record.Links); err != nil {
		return err
	}
	if len(record.Evidence) > MaxEvidenceRefs {
		return fmt.Errorf("record %q evidence reference count exceeds limit", record.ID)
	}
	for _, ref := range record.Evidence {
		if err := validateIdentifier("evidence id", ref.ID, false); err != nil {
			return err
		}
		if err := validateText("evidence kind", ref.Kind, 128, false); err != nil {
			return err
		}
		if err := validateText("evidence scope", ref.Scope, 256, false); err != nil {
			return err
		}
	}
	if expectedMode == ModeFixture && record.Authority == AuthorityAuthoritative {
		return fmt.Errorf("fixture record %q cannot claim authoritative authority", record.ID)
	}
	return nil
}

func validateSource(source Source) error {
	if err := validateIdentifier("source id", source.ID, false); err != nil {
		return err
	}
	if err := validateText("source label", source.Label, MaxTextBytes, false); err != nil {
		return err
	}
	if err := validateMode(source.Mode); err != nil {
		return err
	}
	switch source.Health {
	case SourceObserving, SourceUnavailable, SourceGap, SourceComplete:
	default:
		return fmt.Errorf("source %q has invalid health", source.ID)
	}
	if err := validateText("source continuity", source.Continuity, 256, true); err != nil {
		return err
	}
	if err := validateText("source epoch", source.Epoch, 128, true); err != nil {
		return err
	}
	if err := validateText("source reason", source.Reason, MaxTextBytes, true); err != nil {
		return err
	}
	if source.Mode == ModeUnavailable && source.Available {
		return fmt.Errorf("unavailable source %q cannot be available", source.ID)
	}
	return nil
}

// ValidateSnapshot verifies the bounded wire contract before it is accepted
// into the presentation store. It does not verify policy, evidence, or
// external authenticity; those remain owned by their source components.
func ValidateSnapshot(snapshot Snapshot) error {
	if snapshot.SchemaVersion != ContractVersion {
		return fmt.Errorf("unsupported snapshot schema version %q", snapshot.SchemaVersion)
	}
	if err := validateMode(snapshot.Mode); err != nil {
		return err
	}
	snapshotScope := snapshot.Verification
	if snapshotScope == "" {
		snapshotScope = defaultVerificationScope(snapshot.Mode)
	}
	if err := validateVerificationScope(snapshotScope); err != nil {
		return err
	}
	if snapshot.Mode == ModeUnavailable && snapshotScope != VerificationUnavailable {
		return errors.New("unavailable snapshot must use unavailable verification scope")
	}
	if snapshot.Epoch == "" || len(snapshot.Epoch) > 128 || strings.Contains(snapshot.Epoch, ":") {
		return errors.New("snapshot epoch is invalid")
	}
	if snapshot.Cursor == "" {
		return errors.New("snapshot cursor is required")
	}
	if err := validateCursor(snapshot.Cursor, snapshot.Epoch); err != nil {
		return err
	}
	if snapshot.GeneratedAt.IsZero() {
		return errors.New("snapshot generated_at is required")
	}
	if len(snapshot.Sources) > MaxSources || len(snapshot.Records) > MaxRecords || len(snapshot.Warnings) > MaxWarnings || len(snapshot.Preflight) > MaxPreflightChecks || len(snapshot.ProjectTests) > MaxProjectTests || len(snapshot.Actions) > MaxActionOutputs {
		return errors.New("snapshot exceeds a configured collection bound")
	}
	seenSources := make(map[string]struct{}, len(snapshot.Sources))
	for _, source := range snapshot.Sources {
		if err := validateSource(source); err != nil {
			return err
		}
		if (snapshot.Mode == ModeFixture || snapshot.Mode == ModeReplay) && source.Mode != snapshot.Mode {
			return fmt.Errorf("%s source %q must use matching mode", snapshot.Mode, source.ID)
		}
		if _, ok := seenSources[source.ID]; ok {
			return fmt.Errorf("duplicate source %q", source.ID)
		}
		seenSources[source.ID] = struct{}{}
	}
	seenRecords := make(map[string]struct{}, len(snapshot.Records))
	for _, record := range snapshot.Records {
		if err := validateRecord(record, snapshot.Mode, snapshotScope); err != nil {
			return err
		}
		if _, ok := seenRecords[record.ID]; ok {
			return fmt.Errorf("duplicate record %q", record.ID)
		}
		seenRecords[record.ID] = struct{}{}
		if _, ok := seenSources[record.SourceID]; !ok {
			return fmt.Errorf("record %q references an unregistered source", record.ID)
		}
		if snapshot.Mode == ModeFixture && (record.Authority != AuthorityDiagnostic || record.Verification != VerificationUnverifiedInput) {
			return fmt.Errorf("fixture record %q must remain diagnostic unverified input", record.ID)
		}
	}
	for _, check := range snapshot.Preflight {
		if err := validateText("preflight name", check.Name, 128, false); err != nil {
			return err
		}
		switch check.State {
		case PreflightPassed, PreflightFailed, PreflightUnavailable, PreflightPending:
		default:
			return fmt.Errorf("preflight %q has invalid state", check.Name)
		}
		if err := validateText("preflight detail", check.Detail, MaxTextBytes, true); err != nil {
			return err
		}
	}
	if err := validateText("session id", snapshot.Session.SessionID, 256, true); err != nil {
		return err
	}
	if err := validateText("profile", snapshot.Session.Profile, 256, true); err != nil {
		return err
	}
	if err := validateText("Pi identity", snapshot.Session.Pi, 256, true); err != nil {
		return err
	}
	if err := validateState(snapshot.Session.Status); err != nil {
		return err
	}
	if err := validatePublication(snapshot.Publication); err != nil {
		return err
	}
	if snapshot.Mode == ModeFixture && snapshot.Publication.State == PublicationPublished {
		return errors.New("fixture cannot claim published authority")
	}
	if err := validateRecovery(snapshot.Recovery); err != nil {
		return err
	}
	if snapshot.Mode == ModeFixture && snapshot.Recovery.State == RecoveryReady {
		return errors.New("fixture cannot claim recovery readiness")
	}
	if err := validateGeneration(snapshot.Generation); err != nil {
		return err
	}
	for _, action := range snapshot.Actions {
		if err := validateText("action name", action.Name, 256, false); err != nil {
			return err
		}
		if err := validateState(action.State); err != nil {
			return err
		}
		if err := validateAuthority(action.Authority); err != nil {
			return err
		}
		if action.Authority == AuthorityAuthoritative {
			return fmt.Errorf("action %q authoritative authority is unavailable to this presentation contract", action.Name)
		}
		if err := validateMode(action.Mode); err != nil {
			return err
		}
		if action.Mode != snapshot.Mode {
			return fmt.Errorf("action %q mode does not match snapshot mode", action.Name)
		}
		actionScope := action.Verification
		if actionScope == "" {
			actionScope = defaultVerificationScope(action.Mode)
		}
		if err := validateVerificationScope(actionScope); err != nil {
			return err
		}
		if actionScope != snapshotScope {
			return fmt.Errorf("action %q verification scope does not match snapshot", action.Name)
		}
		if snapshot.Mode == ModeFixture && (action.Authority != AuthorityDiagnostic || actionScope != VerificationUnverifiedInput) {
			return fmt.Errorf("fixture action %q must remain diagnostic unverified input", action.Name)
		}
		if err := validateText("action output", action.Output, MaxOutputBytes, true); err != nil {
			return err
		}
		if err := validateText("action detail", action.Detail, MaxTextBytes, true); err != nil {
			return err
		}
	}
	for _, output := range snapshot.ProjectTests {
		if err := validateText("project test name", output.Name, 256, false); err != nil {
			return err
		}
		if err := validateState(output.State); err != nil {
			return err
		}
		if err := validateText("project test output", output.Output, MaxOutputBytes, true); err != nil {
			return err
		}
		if err := validateText("project test detail", output.Detail, MaxTextBytes, true); err != nil {
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

func validatePublication(value PublicationView) error {
	switch value.State {
	case PublicationUnavailable, PublicationStaged, PublicationPublished, PublicationQuarantined:
	default:
		return fmt.Errorf("invalid publication state %q", value.State)
	}
	return validateText("publication detail", value.Detail, MaxTextBytes, true)
}

func validateRecovery(value RecoveryView) error {
	switch value.State {
	case RecoveryUnavailable, RecoveryReady, RecoveryRequired, RecoveryQuarantined:
	default:
		return fmt.Errorf("invalid recovery state %q", value.State)
	}
	return validateText("recovery detail", value.Detail, MaxTextBytes, true)
}

func validateGeneration(value GenerationView) error {
	for name, item := range map[string]string{"generation id": value.ID, "baseline": value.Baseline, "tree digest": value.TreeDigest, "generation note": value.Note} {
		if err := validateText(name, item, MaxTextBytes, true); err != nil {
			return err
		}
	}
	for name, item := range map[string]string{"metadata state": value.MetadataState, "contents state": value.ContentsState, "diff state": value.DiffState} {
		if err := validateText(name, item, 64, false); err != nil {
			return err
		}
	}
	return nil
}

func validateCursor(cursor, epoch string) error {
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 || parts[0] != epoch || parts[1] == "" {
		return errors.New("snapshot cursor is invalid")
	}
	for _, char := range parts[1] {
		if char < '0' || char > '9' {
			return errors.New("snapshot cursor sequence is invalid")
		}
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

func readDocument(path string, expectedMode Mode) (Snapshot, error) {
	if strings.TrimSpace(path) == "" {
		return Snapshot{}, errors.New("observation document path must not be empty")
	}
	const maxDocumentBytes = 2 << 20
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
	if document.Snapshot.Mode != expectedMode {
		return Snapshot{}, fmt.Errorf("observation document mode %q does not match requested %q", document.Snapshot.Mode, expectedMode)
	}
	// JSON documents are caller-controlled input. A field claiming structural
	// verification is not provenance, so loaded documents are always explicitly
	// unverified regardless of what the document says.
	document.Snapshot.Verification = VerificationUnverifiedInput
	for index := range document.Snapshot.Records {
		document.Snapshot.Records[index].Authority = AuthorityDiagnostic
		document.Snapshot.Records[index].Verification = VerificationUnverifiedInput
	}
	for index := range document.Snapshot.Actions {
		document.Snapshot.Actions[index].Authority = AuthorityDiagnostic
		document.Snapshot.Actions[index].Verification = VerificationUnverifiedInput
	}
	if err := ValidateSnapshot(document.Snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("validate observation document: %w", err)
	}
	return cloneSnapshot(document.Snapshot), nil
}

// LoadFixture loads a sanitized fixture document. It is intentionally a
// separate API from replay so callers must opt into fixture mode explicitly.
func LoadFixture(path string) (Snapshot, error) { return readDocument(path, ModeFixture) }

// LoadReplay loads retained observations without contacting a provider or
// executing tools. The document must identify itself as replay.
func LoadReplay(path string) (Snapshot, error) { return readDocument(path, ModeReplay) }
