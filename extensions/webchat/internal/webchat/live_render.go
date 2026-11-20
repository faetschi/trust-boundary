package webchat

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// This file renders the product service's session view and events into the
// snapshot and item shapes the existing client validates. It is pure: it sets no
// authority, invents no verdict, and never builds markup. Text stays data.
//
// Mapping, one event to exactly one item so sequence = seq + 1:
//
//	turn_admitted             message (user), the prompt
//	assistant_message         message (assistant)
//	tool_call                 decision (tool, tool_call_id, verdict, reason_code, summary = hint)
//	tool_result               result (tool, tool_call_id, state, summary = text, detail = reason and hint)
//	turn_linked               message (system)
//	turn_cancel_requested     message (system)
//	turn_awaiting_review      message (system), changed-path count and candidate digest
//	turn_resolution_requested message (system)
//	turn_terminal             message (system), outcome and reason
//
// A tool outside the closed read, write, edit and bash set, or a kind the gateway
// does not know, becomes a system message that names it, so no event disappears.

// cleanText makes s valid display text: valid UTF-8, no control character other
// than newline and tab (a carriage return is dropped), cut at limit bytes on a rune
// boundary. The flag reports a cut.
func cleanText(s string, limit int) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "�") {
		switch {
		case r == '\r':
			continue
		case (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f):
			r = '�'
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) <= limit {
		return out, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut], true
}

// cleanIdentifier maps a value to a bounded identifier. A valid one is unchanged,
// so a decision and its result keep matching ids; any other is rewritten the same
// way every time.
func cleanIdentifier(value, fallback string) string {
	if identifierPattern.MatchString(value) {
		return value
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == ':', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 128 {
		out = out[:128]
	}
	if out == "" {
		return fallback
	}
	if !identifierPattern.MatchString(out) {
		out = "x" + out
		if len(out) > 128 {
			out = out[:128]
		}
	}
	return out
}

func validToolName(tool string) bool {
	switch Tool(tool) {
	case ToolRead, ToolWrite, ToolEdit, ToolBash:
		return true
	}
	return false
}

// shortTurnID is the turn part of a turn ID: turn IDs are session/turn-N and an
// item's turn_id carries no slash.
func shortTurnID(sessionID, turnID string) string {
	short := strings.TrimPrefix(turnID, sessionID+"/")
	if short == "" {
		return ""
	}
	return cleanIdentifier(short, "turn")
}

func eventItems(sessionID string, events []ProductEvent, now time.Time) []Item {
	items := make([]Item, 0, len(events))
	for _, event := range events {
		items = append(items, eventItem(sessionID, event, now))
	}
	return items
}

func eventItem(sessionID string, event ProductEvent, now time.Time) Item {
	item := Item{
		SchemaVersion: ContractVersion,
		Sequence:      event.Seq + 1,
		ID:            fmt.Sprintf("ev-%d", event.Seq),
		TurnID:        shortTurnID(sessionID, event.TurnID),
		OccurredAt:    now,
		Authority:     AuthorityObserved,
		Verification:  VerificationStructuralAudit,
	}
	system := func(text string) Item {
		item.Type, item.Role = ItemMessage, RoleSystem
		item.Text, _ = cleanText(text, MaxTextBytes)
		return item
	}
	switch event.Kind {
	case EventTurnAdmitted, EventAssistantMessage:
		item.Type, item.Role = ItemMessage, RoleAssistant
		if event.Kind == EventTurnAdmitted {
			item.Role = RoleUser
		}
		item.Text, item.TextTruncated = cleanText(event.Text, MaxTextBytes)
		if strings.TrimSpace(item.Text) == "" {
			item.Text = "(empty)"
		}
		return item
	case EventToolCall:
		if !validToolName(event.Tool) {
			break
		}
		item.Type, item.Tool = ItemDecision, Tool(event.Tool)
		item.ToolCallID = cleanIdentifier(event.ToolCallID, fmt.Sprintf("call-%d", event.Seq))
		item.Verdict = decisionVerdict(event.Verdict)
		item.ReasonCode = reasonCode(event.ReasonCode)
		item.Summary, _ = cleanText(event.Hint, MaxTextBytes)
		return item
	case EventToolResult:
		if !validToolName(event.Tool) {
			break
		}
		item.Type, item.Tool = ItemResult, Tool(event.Tool)
		item.ToolCallID = cleanIdentifier(event.ToolCallID, fmt.Sprintf("call-%d", event.Seq))
		item.State = resultState(event)
		item.Summary, _ = cleanText(event.Text, MaxTextBytes)
		var detail []string
		if event.ReasonCode != "" {
			detail = append(detail, "reason_code: "+event.ReasonCode)
		}
		if event.Hint != "" {
			detail = append(detail, event.Hint)
		}
		item.Detail, _ = cleanText(strings.Join(detail, "\n"), MaxDetailBytes)
		return item
	case EventTurnLinked:
		return system("Turn started.")
	case EventTurnCancelRequested:
		return system("Cancel requested.")
	case EventTurnAwaitingReview:
		text := "Awaiting review."
		if event.Text != "" {
			text += " " + event.Text + "."
		}
		if event.CandidateDigest != "" {
			text += " Candidate " + event.CandidateDigest + "."
		}
		return system(text)
	case EventTurnResolution:
		return system("Resolution requested: " + event.Reason + ".")
	case EventTurnTerminal:
		text := "Turn finished: " + event.Outcome
		if event.Reason != "" {
			text += " (" + event.Reason + ")"
		}
		return system(text + ".")
	}
	return system(fmt.Sprintf("Unmapped event %q, tool %q, verdict %q, reason %q.", event.Kind, event.Tool, event.Verdict, event.ReasonCode))
}

func decisionVerdict(verdict string) Verdict {
	switch Verdict(verdict) {
	case VerdictAllow, VerdictDeny:
		return Verdict(verdict)
	}
	return VerdictUnknown
}

func reasonCode(code string) string {
	if reasonCodePattern.MatchString(code) {
		return code
	}
	return "unspecified"
}

// resultState reads the released result: a DENY is denied, an ALLOW is succeeded
// unless the fixed summary says the command failed, anything else is unknown.
func resultState(event ProductEvent) ResultState {
	switch Verdict(event.Verdict) {
	case VerdictDeny:
		return ResultDenied
	case VerdictAllow:
		if event.Text == "command failed" {
			return ResultFailed
		}
		return ResultSucceeded
	}
	return ResultUnknown
}

// renderSnapshot builds the live snapshot. truncated says events is only the start
// of the session's list, so the decision counts, which would be partial, are left
// unstated.
func renderSnapshot(view *ProductSession, events []ProductEvent, truncated bool, now time.Time) (Snapshot, error) {
	items := eventItems(view.ID, events, now)
	turnCount := len(view.Turns)
	title, _ := cleanText(fmt.Sprintf("%s / %s / %s", view.ProjectID, view.ProviderProfile, view.PublicationMode), MaxLabelBytes)
	profile := ""
	if identifierPattern.MatchString(view.ProviderProfile) {
		profile = view.ProviderProfile
	}
	governance := GovernancePanel{Note: "counts are the tool decisions in this session's events; the product protocol carries no policy digest"}
	stream := StreamStatus{ID: "product", Label: "product service observations", Health: StreamObserving, Available: true}
	warnings := []string{"event times are not recorded by the product protocol; occurred_at is the time the gateway rendered the item"}
	if truncated {
		stream.Health = StreamGap
		stream.Reason = fmt.Sprintf("only the first %d events are shown", len(events))
		warnings = append(warnings, fmt.Sprintf("the session has more events than one snapshot holds; the first %d are shown and the decision counts are not stated", len(events)))
	} else {
		var allow, deny, unknown int
		for _, item := range items {
			if item.Type != ItemDecision {
				continue
			}
			switch item.Verdict {
			case VerdictAllow:
				allow++
			case VerdictDeny:
				deny++
			default:
				unknown++
			}
		}
		governance.AllowCount, governance.DenyCount, governance.UnknownCount = &allow, &deny, &unknown
	}
	snapshot := Snapshot{
		SchemaVersion: ContractVersion,
		Epoch:         view.ID,
		Cursor:        formatCursor(view.ID, uint64(len(items))),
		Source:        SourceLive,
		Verification:  VerificationStructuralAudit,
		GeneratedAt:   now,
		Session: SessionHeader{
			SessionID: view.ID, Title: title, Status: liveStatus(view), Profile: profile,
			CreatedAt: now, UpdatedAt: now, TurnCount: &turnCount,
		},
		Panels: Panels{
			Governance:  governance,
			Provenance:  ProvenancePanel{Available: false, ProviderProfile: profile, Note: "the product protocol projects no provenance"},
			Publication: publicationPanel(view),
			Recovery:    recoveryPanel(view),
			Qualification: QualificationPanel{G1: false, H1: "unexecuted", Containment: "not-established", ClaimBearing: false,
				Note: "G1=false; H1 unexecuted; containment not-established"},
		},
		Streams:  []StreamStatus{stream},
		Items:    items,
		Warnings: warnings,
		Live:     view,
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// liveStatus derives the session status from its latest turn. The turn states in
// Snapshot.Live stay the precise record.
func liveStatus(view *ProductSession) SessionStatus {
	if len(view.Turns) == 0 {
		return SessionPending
	}
	last := view.Turns[len(view.Turns)-1]
	switch last.State {
	case "admitted", "linked":
		return SessionRunning
	case "awaiting_review":
		return SessionPending
	case "terminal":
		switch last.Outcome {
		case "published", "no_diff", "discarded":
			return SessionCompleted
		case "failed":
			return SessionFailed
		case "cancelled":
			return SessionCancelled
		}
	}
	return SessionUnknown
}

func publicationPanel(view *ProductSession) PublicationPanel {
	if len(view.Turns) == 0 {
		return PublicationPanel{State: PublicationUnavailable, Detail: "no turn has run in this session"}
	}
	last := view.Turns[len(view.Turns)-1]
	switch {
	case last.Outcome == "published":
		return PublicationPanel{State: PublicationPublished, Detail: "the latest turn's outcome is published; the product protocol carries no transaction id"}
	case last.Outcome == "no_diff" || last.Outcome == "discarded" || last.Outcome == "cancelled":
		return PublicationPanel{State: PublicationNotAttempted, Detail: "the latest turn's outcome is " + last.Outcome}
	case last.Outcome == "" && last.Resolution == "publish":
		return PublicationPanel{State: PublicationPending, Detail: "publication was requested for the latest turn"}
	}
	return PublicationPanel{State: PublicationUnavailable, Detail: "the latest turn has no publication outcome"}
}

func recoveryPanel(view *ProductSession) RecoveryPanel {
	if view.Blocked {
		return RecoveryPanel{State: RecoveryRequired, Detail: "the session is blocked; the turn outcomes say why"}
	}
	return RecoveryPanel{State: RecoveryUnavailable, Detail: "the product protocol projects no recovery state"}
}
