package governanceview

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/webview"
)

// FromVerifiedAuditTrace maps a trace supplied by a caller that claims it was
// verified by audit.Verify. The trace type itself carries no non-forgeable
// provenance, so this adapter deliberately keeps every mapped record
// diagnostic and labels the scope structural-audit-only. It does not verify the
// journal and never infers policy, effect, provider, containment, or
// publication authority.
func FromVerifiedAuditTrace(trace audit.Trace, source Source) (Snapshot, error) {
	if source.Mode == "" {
		source.Mode = ModeLive
	}
	if source.Mode == ModeFixture || source.Mode == ModeUnavailable {
		return Snapshot{}, fmt.Errorf("verified audit trace requires live or replay source mode")
	}
	source.Available = true
	if source.Health == "" {
		source.Health = SourceComplete
	}
	if err := validateSource(source); err != nil {
		return Snapshot{}, err
	}
	epoch := newEpoch()
	snapshot := baseSnapshot(source.Mode, epoch)
	snapshot.Verification = VerificationStructuralAudit
	snapshot.Sources = []Source{source}
	snapshot.Warnings = []string{
		"the supplied audit trace is structural input only; records remain diagnostic",
		"structural audit projection does not establish policy-decision authenticity, effect authority, containment, or provider exchange",
		"audit records do not carry original event timestamps; occurred_at is observation time",
	}
	start := 0
	if len(trace.Records) > MaxRecords {
		start = len(trace.Records) - MaxRecords
		snapshot.Warnings = append(snapshot.Warnings, "structural audit input projection was bounded to the newest records")
	}
	effectStatuses := auditEffectStatuses(trace.Records, source.ID)
	for index, auditRecord := range trace.Records[start:] {
		record := mapAuditRecord(auditRecord, source.ID, source.Mode, effectStatuses[start+index])
		snapshot.Records = append(snapshot.Records, record)
	}
	snapshot.Cursor = formatCursor(epoch, uint64(len(snapshot.Records)))
	if err := ValidateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

type auditEffectKey struct {
	sourceID string
	effectID string
}

type auditEffectLineage struct {
	intentSequence uint64
	eventIndexes   []int
	invalid        bool
	outcomeSeen    bool
}

// auditEffectStatuses derives only a conservative diagnostic projection from
// the ordered input records. The plain audit.Trace has no verifier provenance,
// so even a success remains diagnostic and structural-audit-only.
func auditEffectStatuses(records []audit.Record, sourceID string) []State {
	statuses := make([]State, len(records))
	for index := range statuses {
		statuses[index] = StateUnknown
	}
	ordered := true
	var previousSequence uint64
	for _, record := range records {
		if record.Sequence == 0 || (previousSequence != 0 && record.Sequence <= previousSequence) {
			ordered = false
		}
		if record.Sequence > previousSequence {
			previousSequence = record.Sequence
		}
	}
	if !ordered {
		return statuses
	}

	lineages := make(map[auditEffectKey]*auditEffectLineage)
	for index, record := range records {
		event := record.Event
		if event.Kind != "effect_intent" && event.Kind != "effect_outcome" {
			continue
		}
		key := auditEffectKey{sourceID: sourceID, effectID: event.ID}
		lineage := lineages[key]
		if lineage == nil {
			lineage = &auditEffectLineage{}
			lineages[key] = lineage
		}
		lineage.eventIndexes = append(lineage.eventIndexes, index)
		switch event.Kind {
		case "effect_intent":
			if strings.TrimSpace(event.ID) == "" || event.Outcome != "" || lineage.intentSequence != 0 || lineage.outcomeSeen {
				lineage.invalid = true
				for _, eventIndex := range lineage.eventIndexes {
					statuses[eventIndex] = StateUnknown
				}
				continue
			}
			lineage.intentSequence = record.Sequence
			statuses[index] = StatePending
		case "effect_outcome":
			// An outcome consumes the unique preceding intent even when its
			// payload is malformed. A later success must not repair that lineage.
			if lineage.intentSequence == 0 || record.Sequence <= lineage.intentSequence || lineage.invalid || lineage.outcomeSeen {
				if lineage.outcomeSeen {
					lineage.invalid = true
					for _, eventIndex := range lineage.eventIndexes {
						statuses[eventIndex] = StateUnknown
					}
				}
				lineage.outcomeSeen = true
				continue
			}
			lineage.outcomeSeen = true
			switch event.Outcome {
			case "success":
				statuses[index] = StateCompleted
			case "failed":
				if len(event.Data) == 0 {
					statuses[index] = StateFailed
				} else {
					lineage.invalid = true
					for _, eventIndex := range lineage.eventIndexes {
						statuses[eventIndex] = StateUnknown
					}
				}
			case "unknown":
				if len(event.Data) == 0 {
					statuses[index] = StateUnknown
				} else {
					lineage.invalid = true
					for _, eventIndex := range lineage.eventIndexes {
						statuses[eventIndex] = StateUnknown
					}
				}
			default:
				lineage.invalid = true
				for _, eventIndex := range lineage.eventIndexes {
					statuses[eventIndex] = StateUnknown
				}
				statuses[index] = StateUnknown
			}
		}
	}
	return statuses
}

func mapAuditRecord(auditRecord audit.Record, sourceID string, mode Mode, effectStatus State) Record {
	namespace := namespaceFor(safeKind(auditRecord.Event.Kind))
	status := StateUnknown
	switch auditRecord.Event.Kind {
	case "effect_intent":
		status = effectStatus
	case "effect_outcome":
		status = effectStatus
	}
	links := Links{}
	if namespace == NamespaceEffect && identifierPattern.MatchString(auditRecord.Event.ID) {
		links.EffectID = auditRecord.Event.ID
	}
	return Record{
		SchemaVersion: ContractVersion,
		ID:            fmt.Sprintf("audit-%d", auditRecord.Sequence),
		SourceID:      sourceID,
		Mode:          mode,
		Namespace:     namespace,
		Kind:          safeKind(auditRecord.Event.Kind),
		Status:        status,
		Authority:     AuthorityDiagnostic,
		Verification:  VerificationStructuralAudit,
		OccurredAt:    time.Now().UTC(),
		Summary:       fmt.Sprintf("structural audit input record: %s", safeKind(auditRecord.Event.Kind)),
		Links:         links,
		Evidence:      []EvidenceReference{{ID: fmt.Sprintf("audit-record-%d", auditRecord.Sequence), Kind: "audit-record", Scope: "structural input record; verification provenance unavailable"}},
	}
}

// SourceRegistration binds a private webview source token to a non-path
// presentation identity. Token is used only for matching records; it is never
// emitted in a snapshot. This keeps filesystem/environment strings out of the
// browser even when the underlying webview was configured from a path.
type SourceRegistration struct {
	Token string
	ID    string
	Label string
}

// FromWebviewBuffer maps the existing public webview observation buffer into
// this contract. It intentionally consumes only Snapshot/Record data; it does
// not copy webview's internal store, rerun verification, or turn diagnostics
// into authoritative records.
func FromWebviewBuffer(buffer *webview.Buffer) Snapshot {
	if buffer == nil {
		return NewUnavailableSnapshot()
	}
	return FromWebviewRecords(buffer.Snapshot())
}

// FromWebviewRecords is the structural adapter used by the optional live
// command path. The source is explicitly live, but every mapped record stays
// diagnostic because a presentation buffer alone does not prove durable
// policy, effect settlement, containment, or a real Pi provider exchange.
func FromWebviewRecords(records []webview.Record) Snapshot {
	return FromWebviewRecordsWithRegistrations(records, nil)
}

// FromWebviewBufferWithRegistrations preserves only registered source IDs and
// labels while projecting public-buffer source epoch/gap markers.
func FromWebviewBufferWithRegistrations(buffer *webview.Buffer, registrations []SourceRegistration) Snapshot {
	if buffer == nil {
		return NewUnavailableSnapshot()
	}
	return FromWebviewRecordsWithRegistrations(buffer.Snapshot(), registrations)
}

// FromWebviewRecordsWithRegistrations keeps unobserved registered sources
// unavailable, rather than pretending that Buffer.Snapshot contains source
// health metadata it does not expose.
func FromWebviewRecordsWithRegistrations(records []webview.Record, registrations []SourceRegistration) Snapshot {
	epoch := newEpoch()
	snapshot := baseSnapshot(ModeLive, epoch)
	byToken := make(map[string]SourceRegistration, len(registrations))
	for _, registration := range registrations {
		if validRegistration(registration) {
			byToken[registration.Token] = registration
			snapshot.Sources = append(snapshot.Sources, unavailableRegisteredSource(registration))
		}
	}
	snapshot.Warnings = []string{
		"live records are diagnostic projections from the registered webview buffer",
		"durable authority, provider exchange, containment, and publication are not inferred from browser observations",
		"unobserved registered sources remain unavailable; source gaps and epochs come only from public record markers",
	}
	for _, sourceRecord := range records {
		registration, registered := byToken[sourceRecord.Source]
		mapped, source, ok := mapWebviewRecord(sourceRecord, registration, registered)
		if !ok {
			continue
		}
		found := false
		for index := range snapshot.Sources {
			if snapshot.Sources[index].ID == source.ID {
				snapshot.Sources[index] = source
				found = true
				break
			}
		}
		if !found && len(snapshot.Sources) < MaxSources {
			snapshot.Sources = append(snapshot.Sources, source)
		}
		if len(snapshot.Records) < MaxRecords {
			snapshot.Records = append(snapshot.Records, mapped)
		}
	}
	if len(snapshot.Sources) == 0 {
		snapshot.Sources = []Source{{ID: "webview-live", Label: "registered webview observation buffer", Mode: ModeLive, Health: SourceUnavailable, Available: false, Continuity: "no-source-health-metadata", Reason: "public Buffer.Snapshot has no source-health metadata"}}
	}
	snapshot.Cursor = formatCursor(epoch, uint64(len(snapshot.Records)))
	snapshot.GeneratedAt = time.Now().UTC()
	return snapshot
}

// FromWebviewRecord maps one newly observed public-buffer record for a live
// subscriber. It returns false only for an unidentifiable record.
func FromWebviewRecord(record webview.Record) (Record, bool) {
	mapped, _, ok := mapWebviewRecord(record, SourceRegistration{}, false)
	return mapped, ok
}

// FromWebviewRecordForRegistration maps a live record and returns the
// registered source-health update that belongs with it.
func FromWebviewRecordForRegistration(record webview.Record, registration SourceRegistration) (Record, Source, bool) {
	return mapWebviewRecord(record, registration, validRegistration(registration))
}

func baseSnapshot(mode Mode, epoch string) Snapshot {
	return Snapshot{
		SchemaVersion: ContractVersion,
		Epoch:         epoch,
		Cursor:        formatCursor(epoch, 0),
		Mode:          mode,
		Verification:  VerificationUnverifiedInput,
		GeneratedAt:   time.Now().UTC(),
		Session:       SessionHeader{Status: StateUnknown},
		Preflight:     []PreflightCheck{{Name: "Pi/runtime governance proof", State: PreflightUnavailable, Detail: "the companion observes registered records only"}},
		Publication:   PublicationView{State: PublicationUnavailable, Detail: "no publication authority is exposed by this client"},
		Recovery:      RecoveryView{State: RecoveryUnavailable, Detail: "recovery classification is unavailable in the presentation adapter"},
		Generation:    GenerationView{MetadataState: "unavailable", ContentsState: "unavailable", DiffState: "not-comparable", Note: "generation metadata is not present in this adapter"},
		ProjectTests:  []ProjectTestOutput{{Name: "Pi project tests", State: StateUnknown, Detail: "project-test output is unavailable"}},
	}
}

func mapWebviewRecord(sourceRecord webview.Record, registration SourceRegistration, registered bool) (Record, Source, bool) {
	if sourceRecord.ID == 0 {
		return Record{}, Source{}, false
	}
	source := Source{ID: "webview-unregistered", Label: "unregistered observation source", Mode: ModeLive, Health: SourceUnavailable, Available: false, Continuity: "unregistered", Reason: "record source is outside the registered observation set"}
	if registered {
		source = observedRegisteredSource(registration, sourceRecord)
	}
	kind := "observation"
	if len(sourceRecord.Types) != 0 && strings.TrimSpace(sourceRecord.Types[0]) != "" {
		kind = safeKind(sourceRecord.Types[0])
	}
	namespace := namespaceFor(kind)
	links := Links{}
	status := StateUnknown
	detail := ""
	if len(sourceRecord.Record) != 0 {
		var object map[string]json.RawMessage
		if json.Unmarshal(sourceRecord.Record, &object) == nil && object != nil {
			if nested, ok := rawObject(object["event"]); ok {
				for key, value := range nested {
					object[key] = value
				}
			}
			links = linksFromObject(object, namespace)
			status = stateFromObject(object, kind)
		}
	}
	if !registered {
		detail = "observation source is not registered; status and source health remain unknown"
		status = StateUnknown
	} else if sourceRecord.ParseError != "" {
		detail = truncate(sourceRecord.ParseError, MaxTextBytes)
		status = StateUnknown
	}
	if sourceRecord.ReceivedAt.IsZero() {
		sourceRecord.ReceivedAt = time.Now().UTC()
	}
	return Record{
		SchemaVersion: ContractVersion,
		ID:            fmt.Sprintf("webview-%d", sourceRecord.ID),
		SourceID:      source.ID,
		Mode:          ModeLive,
		Verification:  VerificationUnverifiedInput,
		Namespace:     namespace,
		Kind:          kind,
		Status:        status,
		Authority:     AuthorityDiagnostic,
		OccurredAt:    sourceRecord.ReceivedAt,
		Summary:       fmt.Sprintf("%s observation from %s", kind, source.Label),
		Detail:        detail,
		Links:         links,
	}, source, true
}

func validRegistration(registration SourceRegistration) bool {
	return registration.Token != "" && identifierPattern.MatchString(registration.ID) && strings.TrimSpace(registration.Label) != "" && len(registration.Label) <= MaxTextBytes && !strings.ContainsRune(registration.Label, '\x00') && !strings.ContainsAny(registration.Label, `/\\:`)
}

func unavailableRegisteredSource(registration SourceRegistration) Source {
	return Source{ID: registration.ID, Label: registration.Label, Mode: ModeLive, Health: SourceUnavailable, Available: false, Continuity: "not-observed", Reason: "registered source has no public observation record"}
}

func observedRegisteredSource(registration SourceRegistration, record webview.Record) Source {
	source := Source{ID: registration.ID, Label: registration.Label, Mode: ModeLive, Health: SourceObserving, Available: true, Epoch: record.SourceEpoch, Continuity: "observing"}
	for _, kind := range record.Types {
		switch kind {
		case "source_gap":
			source.Health, source.Continuity, source.Reason = SourceGap, "gap", "public source-gap marker observed"
		case "source_epoch":
			source.Health, source.Continuity, source.Reason = SourceGap, "epoch-changed", "public source-epoch marker observed"
		case "malformed":
			source.Health, source.Continuity, source.Reason = SourceGap, "malformed-record", "public malformed-record marker observed"
		}
	}
	if record.ParseError != "" {
		source.Health, source.Continuity, source.Reason = SourceGap, "malformed-record", "public parse-error marker observed"
	}
	return source
}

func safeKind(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "observation"
	}
	if len(value) > 128 {
		value = value[:128]
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.') {
			return "observation"
		}
	}
	return value
}

func namespaceFor(kind string) Namespace {
	switch {
	case strings.Contains(kind, "provider"):
		return NamespaceProvider
	case strings.Contains(kind, "pi"), strings.Contains(kind, "chat"), kind == "go_test":
		return NamespacePi
	case strings.Contains(kind, "proposal"):
		return NamespaceProposal
	case strings.Contains(kind, "decision"), kind == "audit":
		return NamespaceDecision
	case strings.Contains(kind, "effect"), strings.Contains(kind, "result"):
		return NamespaceEffect
	case strings.Contains(kind, "generation"):
		return NamespaceGeneration
	case strings.Contains(kind, "source"):
		return NamespaceSource
	default:
		return NamespaceSession
	}
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var object map[string]json.RawMessage
	return object, json.Unmarshal(raw, &object) == nil && object != nil
}

func linksFromObject(object map[string]json.RawMessage, namespace Namespace) Links {
	links := Links{
		ProviderID:   objectIdentifier(object, "provider_id", "provider"),
		PiID:         objectIdentifier(object, "pi_id", "pi"),
		ProposalID:   objectIdentifier(object, "proposal_id", "proposal"),
		DecisionID:   objectIdentifier(object, "decision_id", "decision"),
		EffectID:     objectIdentifier(object, "effect_id", "effect"),
		GenerationID: objectIdentifier(object, "generation_id", "generation"),
	}
	if namespace == NamespaceEffect && links.EffectID == "" {
		links.EffectID = objectIdentifier(object, "id")
	}
	return links
}

func objectIdentifier(object map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := object[key]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil && identifierPattern.MatchString(value) {
			return value
		}
	}
	return ""
}

func stateFromObject(object map[string]json.RawMessage, kind string) State {
	for _, key := range []string{"status", "outcome", "state"} {
		var value string
		if json.Unmarshal(object[key], &value) != nil {
			continue
		}
		switch strings.ToLower(value) {
		case "pending", "running":
			return StatePending
		case "denied", "deny":
			return StateDenied
		case "failed", "error":
			return StateFailed
		case "withheld":
			return StateWithheld
		case "unknown":
			return StateUnknown
		case "success", "succeeded", "completed", "passed", "allow", "allowed":
			return StateCompleted
		}
	}
	if strings.Contains(kind, "intent") || strings.Contains(kind, "proposal") {
		return StatePending
	}
	return StateUnknown
}
