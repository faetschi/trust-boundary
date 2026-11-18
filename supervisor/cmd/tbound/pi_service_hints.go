package main

import "fmt"

// piServiceErrorHints maps each error_code the local Pi service answers to a short
// cause and the owner's next action. The wire response stays {"status","error_code"}: it
// is a pinned, strictly decoded schema (tbound-pi-service-response/v1) that the journal
// digests, so the client adds the hint from this table when it prints a refusal.
var piServiceErrorHints = map[string]string{
	"peer-not-authorized": "the calling uid/gid is not the registered client; run the client as the account named in the installed registration (AllowedClientUIDs, AllowedClientPrimaryGID)",
	"invalid-request":     "the request frame, or the workflow envelope it selects, is malformed or does not match the installed service config; fix the client arguments (--task-id, --task, --attempt, --role) to equal the installed config",
	"request-id-conflict": "this request id was already used with a different payload or by another client; use a fresh request id, or resend the identical request to replay it",
	"one-task-at-a-time":  "another task holds the service; wait for it to finish, then send the request again with a fresh request id",

	"maintenance-lock-unavailable": "the service could not take the maintenance lock; check the service log, and retry once the cause is fixed",
	"maintenance-in-progress":      "a maintenance operation holds the service; wait until it ends, then send the request again",
	"request-journal-unavailable":  "the request journal could not be read or written; check the service log and the journal directory; the request may or may not be recorded, so read the journal before sending the next request",
	"socket-authority-changed":     "the service socket no longer matches its registration; the request is recorded but this attempt did not start a run; restore the registered socket unit, restart the service, and reconcile the recorded request",
	"terminal-journal-unavailable": "the run ended; its outcome was not recorded (its result could not be journaled); check the service log and journal; the next request is blocked until the request is settled or abandoned",

	"task-outcome-unknown":                  "the run's outcome could not be established; the next request is blocked until it is abandoned: reconcile it with `tbound abandon-unknown` (guest: `tbound-op abandon <rid>`) while its registration is installed",
	"provider-outage":                       "the last provider attempt of a turn got no model output (no HTTP response, or status 403, 429 or 5xx after the bounded retries); the outcome is unknown and the next request is blocked until it is abandoned: reconcile it with `tbound abandon-unknown` (guest: `tbound-op abandon <rid>`); outage_evidence_digest names the provider journal record",
	"retained-workflow-recovery-unresolved": "recovery of the retained workflow failed and the request stays unresolved; check the service log, then settle it with `tbound abandon-unknown` (guest: `tbound-op abandon <rid>`); retrying cannot turn it into a new run",
	"prior-workflow-unsettled":              "an earlier request has no settled terminal; check the service log line 'settlement check <name>' and, if the prior ended unknown, reconcile it with `tbound abandon-unknown` (guest: `tbound-op abandon <rid>`) while its registration is installed",

	"attempt-not-accepted": "the installed service config does not use corpus admission, so --attempt and --role are not accepted; drop them or install a corpus-mode config",
	"prompt-not-accepted":  "corpus admission selects the task itself; drop the prompt from the request",
	"attempt-required":     "corpus admission needs --attempt (1 or 2); add it",
	"invalid-attempt":      "--attempt must be 1 or 2",
	"role-required":        "corpus admission needs --role; add one of the corpus roles",
	"invalid-role":         "--role is not a corpus role; use one of trajectory, static-replay, variance-1, variance-2, compliant, ambiguous",
	"corpus-unavailable":   "the service has no loaded corpus; check the service log and the installed corpus manifest, then restart the service",
	"unknown-case":         "the task id is not a case in the installed corpus manifest; use a case id from the manifest",
	"role-kind-mismatch":   "this role is not allowed for the case; use a role its case kind allows",
	"role-unavailable":     "the case has no servable artifact for this role in the installed corpus; pick another role or install the missing artifact",

	"admission-already-used":  "this case, role and attempt were already admitted under another request id (single use); replay the original request id to read it back; use the next attempt or role only after the earlier run's outcome is settled (completed, or abandoned via tbound-op abandon)",
	"replay-binding-mismatch": "the stored result belongs to a different manifest, service config or registration than the active one; the request id is consumed, so install the original binding to read it back, or use a fresh admission only after the earlier run's outcome is settled (completed, or abandoned via tbound-op abandon)",
}

// piServiceRefusalMessage is the client's one-line answer for a non-completed response,
// with a second hint line when the code is known. The status and code come from the wire, so
// one outside the known set is printed quoted (%q): a newline in it cannot start a line of its
// own, such as a forged "hint:" line. A known code keeps the documented
// "local Pi service <status>: <error_code>" first line.
func piServiceRefusalMessage(status, errorCode string) string {
	hint := piServiceErrorHints[errorCode]
	if !knownPiServiceStatus[status] {
		status = fmt.Sprintf("%q", status)
	}
	if hint == "" && errorCode != "" {
		errorCode = fmt.Sprintf("%q", errorCode)
	}
	message := fmt.Sprintf("local Pi service %s: %s", status, errorCode)
	if hint != "" {
		message += "\nhint: " + hint
	}
	return message
}

var knownPiServiceStatus = map[string]bool{"refused": true, "busy": true, "unknown": true}
