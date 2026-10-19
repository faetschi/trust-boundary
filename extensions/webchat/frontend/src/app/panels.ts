// Governance panel readings. Pure. Each function turns one panel of the snapshot into the
// words and tones the inspector shows, under the honesty rules of the legacy client:
//
//   - an unverified success is "reported", never "published" or "succeeded";
//   - UNKNOWN stays UNKNOWN, and a state the client does not know is UNKNOWN;
//   - an absent value is "unavailable" with a reason, never zero, never empty.
//
// Verification is judged by the shared gate (verification.ts). A panel reading uses its projection half
// (core format.ts isVerifiedProjection): a live source with a structural audit scope. Anything else is
// not verified, and a value the panel does not carry is unavailable, never no.

import { SUCCESS_STATES, humanize } from "../core/format";
import type {
  GovernancePanel,
  ItemView,
  PublicationPanel,
  QualificationPanel,
  ProvenancePanel,
  RecoveryPanel,
  SessionHeader,
} from "../core/contract";
import type { ChipTone } from "../ui/Chip";

export type Reading =
  | { readonly kind: "value"; readonly text: string }
  | { readonly kind: "unavailable"; readonly reason: string };

export interface StateReading {
  // The state in words. "reported" stands for an unverified success.
  readonly label: string;
  readonly tone: ChipTone;
  // A plain sentence about how the state was established. Empty when there is nothing to add.
  readonly trust: string;
}

// Why a value has no reading before the first snapshot. Shown as unavailable, never as zero or none.
export const NO_PROJECTION = "no projection loaded";

// The words of an unavailable value, for text that is not a reading (a summary line, a list item).
export function unavailableWords(reason: string): string {
  return `unavailable (${reason})`;
}

// A count the projection states: a non-negative integer. Anything else is not a count.
export function countValue(value: unknown): number | null {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 ? value : null;
}

export function countReading(value: unknown, reason: string): Reading {
  const count = countValue(value);
  return count === null ? { kind: "unavailable", reason } : { kind: "value", text: String(count) };
}

export function textReading(value: unknown, reason: string): Reading {
  if (typeof value === "string" && value !== "") return { kind: "value", text: value };
  return { kind: "unavailable", reason };
}

export function flagReading(value: unknown, reason: string): Reading {
  if (typeof value === "boolean") return { kind: "value", text: value ? "true" : "false" };
  return { kind: "unavailable", reason };
}

const NO_GOVERNANCE = "the snapshot carries no governance panel";
const NO_PUBLICATION = "the snapshot carries no publication panel";
const NO_RECOVERY = "the snapshot carries no recovery panel";
const NO_PROVENANCE = "the snapshot carries no provenance panel";
export const NO_QUALIFICATION = "the snapshot carries no qualification panel";
const NOT_PROJECTED = "not projected by the source";

export function governanceRows(panel: GovernancePanel | null | undefined): Array<readonly [string, Reading]> {
  return [["Policy digest", textReading(panel?.policy_digest, panel ? "not projected" : NO_GOVERNANCE)]];
}

const NO_DECISION_COUNTS = "the snapshot carries no decision counts";
const CONTRADICTED_COUNTS = "the counts contradict the loaded decision records";

// Whether the panel states its decisions, by presence alone. A count the source did not state is absent (undefined or null),
// and a stated count, zero included, is a statement. "none" is three stated zeros with no loaded verdict. "contradicted" is
// three stated zeros beside a loaded verdict, which the records refute, so the zeros are not shown. A panel with some counts
// stated is "counted": it reads part by part, and an absent count reads unavailable. The policy digest plays no part.
export type DecisionsState = "counted" | "none" | "absent" | "contradicted";

export function decisionsState(panel: GovernancePanel | null | undefined, verdicts: number): DecisionsState {
  if (!panel) return "absent";
  const counts = [panel.allow_count, panel.deny_count, panel.unknown_count];
  if (counts.every(isAbsent)) return "absent";
  if (counts.every((count) => countValue(count) === 0)) return verdicts === 0 ? "none" : "contradicted";
  return "counted";
}

function isAbsent(value: unknown): boolean {
  return value === undefined || value === null;
}

// The loaded records that carry a verdict. A zero count beside one of them contradicts the zero.
export function verdictCount(items: readonly ItemView[]): number {
  return items.filter((item) => item.verdict !== undefined).length;
}

// The policy verdicts in words, in the footer's form: "policy verdicts 1 allow / 1 deny / 0 unknown". Absent counts read
// unavailable, and an absent panel says why. A count the panel does not carry reads "unavailable allow", never zero.
export function decisionSummary(panel: GovernancePanel | null | undefined, verdicts = 0): string {
  if (!panel) return `policy verdicts ${unavailableWords(NO_GOVERNANCE)}`;
  const state = decisionsState(panel, verdicts);
  if (state === "absent") return `policy verdicts ${unavailableWords(NO_DECISION_COUNTS)}`;
  if (state === "contradicted") return `policy verdicts ${unavailableWords(CONTRADICTED_COUNTS)}`;
  const word = (value: unknown): string => {
    const count = countValue(value);
    return count === null ? "unavailable" : String(count);
  };
  return `policy verdicts ${word(panel.allow_count)} allow / ${word(panel.deny_count)} deny / ${word(panel.unknown_count)} unknown`;
}

// A policy ALLOW is a permission, not an outcome. The panel says so in words.
export const POLICY_NOTE = "A policy ALLOW is a permission, not an outcome.";

const PUBLICATION_TONES: Readonly<Record<string, ChipTone>> = {
  unavailable: "idle",
  "not-attempted": "idle",
  pending: "pending",
  failed: "deny",
  quarantined: "deny",
  unknown: "unknown",
};

const RECOVERY_TONES: Readonly<Record<string, ChipTone>> = {
  unavailable: "idle",
  "not-required": "idle",
  "recovery-required": "pending",
  quarantined: "deny",
  unknown: "unknown",
};

const SESSION_TONES: Readonly<Record<string, ChipTone>> = {
  pending: "pending",
  running: "pending",
  failed: "deny",
  cancelled: "idle",
  unknown: "unknown",
  unavailable: "idle",
};

export const UNREPORTED_TRUST = "reported by the source, not independently verified";

// The reading for one state value. A success is "ok" only when the projection is
// verified; otherwise it is "reported". A value outside the tone table is UNKNOWN.
export function stateReading(
  state: string | undefined,
  verified: boolean,
  tones: Readonly<Record<string, ChipTone>>,
): StateReading {
  if (state === undefined || state === "") {
    return { label: "unavailable", tone: "idle", trust: "not projected by the source" };
  }
  if (SUCCESS_STATES.has(state)) {
    if (verified) return { label: humanize(state), tone: "ok", trust: "verified by the gateway" };
    return { label: "reported", tone: "reported", trust: UNREPORTED_TRUST };
  }
  const tone = Object.hasOwn(tones, state) ? tones[state] : undefined;
  return { label: humanize(state), tone: tone ?? "unknown", trust: "" };
}

export function publicationReading(panel: PublicationPanel | null | undefined, verified: boolean): StateReading {
  if (!panel) return { label: "unavailable", tone: "idle", trust: NO_PUBLICATION };
  return stateReading(panel.state, verified, PUBLICATION_TONES);
}

export function recoveryReading(panel: RecoveryPanel | null | undefined, verified: boolean): StateReading {
  if (!panel) return { label: "unavailable", tone: "idle", trust: NO_RECOVERY };
  return stateReading(panel.state, verified, RECOVERY_TONES);
}

export function sessionReading(session: SessionHeader | null, verified: boolean): StateReading {
  if (!session) return { label: "unavailable", tone: "idle", trust: "no session projection" };
  return stateReading(session.status, verified, SESSION_TONES);
}



// "Independently verified" is yes only when the panel says it is available and the projection is
// verified. An absent panel, or one that does not state availability, is unavailable, never no.
function independentlyVerified(panel: ProvenancePanel | null | undefined, verified: boolean): Reading {
  if (!panel) return { kind: "unavailable", reason: NO_PROVENANCE };
  if (typeof panel.available !== "boolean") {
    return { kind: "unavailable", reason: "the provenance panel does not state availability" };
  }
  return { kind: "value", text: panel.available && verified ? "yes" : "no" };
}

export function provenanceRows(
  panel: ProvenancePanel | null | undefined,
  verified: boolean,
): Array<readonly [string, Reading]> {
  const unverified = panel?.note ?? "the gateway has not verified provenance";
  const field = (value: unknown): Reading => textReading(value, panel ? unverified : NO_PROVENANCE);
  return [
    ["Independently verified", independentlyVerified(panel, verified)],
    ["Profile", field(panel?.profile_id)],
    ["Profile hash", field(panel?.profile_hash)],
    ["Source digest", field(panel?.source_digest)],
    ["Containment profile", field(panel?.containment_profile)],
    ["Containment digest", field(panel?.containment_digest)],
    ["Provider profile", field(panel?.provider_profile)],
    ["Runtime closure", field(panel?.runtime_closure)],
    ["Launch proof digest", field(panel?.tcb_proof_digest)],
  ];
}


// The qualification values as the projection states them. A panel that is absent gives `absentReason`; a
// present panel that does not carry a value gives that value as not projected. Nothing here is a default.
export function qualificationRows(
  panel: QualificationPanel | null | undefined,
  absentReason: string = NO_QUALIFICATION,
): Array<readonly [string, Reading]> {
  const missing = panel ? NOT_PROJECTED : absentReason;
  return [
    ["G1", flagReading(panel?.g1, missing)],
    ["H1", textReading(panel?.h1, missing)],
    ["Containment", textReading(panel?.containment, missing)],
    ["Claim-bearing", flagReading(panel?.claim_bearing, missing)],
  ];
}

export const NO_WARNINGS = "No warnings projected.";

// The warnings of the snapshot. Before the first snapshot there are none to show, and that is unavailable,
// not "none": the empty list is a statement only once a projection has loaded.
export function warningLines(warnings: readonly string[] | null | undefined, loaded: boolean): string[] {
  if (!loaded) return [unavailableWords(NO_PROJECTION)];
  if (!Array.isArray(warnings)) return [unavailableWords("the snapshot carries no warnings")];
  return warnings.length > 0 ? [...warnings] : [NO_WARNINGS];
}
