// Governance panel readings. Pure. Each function turns one panel of the snapshot into the
// words and tones the inspector shows, under the honesty rules of the legacy client:
//
//   - an unverified success is "reported", never "published" or "succeeded";
//   - UNKNOWN stays UNKNOWN, and a state the client does not know is UNKNOWN;
//   - an absent value is "unavailable" with a reason, never zero, never empty.
//
// Verification is a property of the projection (core format.ts isVerifiedProjection): a
// live source with a structural audit scope. Anything else is not verified.

import { SUCCESS_STATES, humanize } from "../core/format";
import type {
  GovernancePanel,
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

export function countReading(value: unknown, reason: string): Reading {
  if (typeof value === "number" && Number.isInteger(value) && value >= 0) {
    return { kind: "value", text: String(value) };
  }
  return { kind: "unavailable", reason };
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
const NO_QUALIFICATION = "the snapshot carries no qualification panel";

export function governanceRows(panel: GovernancePanel | null | undefined): Array<readonly [string, Reading]> {
  return [
    ["Allowed (ALLOW)", countReading(panel?.allow_count, NO_GOVERNANCE)],
    ["Denied (DENY)", countReading(panel?.deny_count, NO_GOVERNANCE)],
    ["Unknown", countReading(panel?.unknown_count, NO_GOVERNANCE)],
    ["Policy digest", textReading(panel?.policy_digest, panel ? "not projected" : NO_GOVERNANCE)],
  ];
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
  if (state === undefined) {
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



export function provenanceRows(panel: ProvenancePanel | null | undefined): Array<readonly [string, Reading]> {
  const unverified = panel?.note ?? "the gateway has not verified provenance";
  const field = (value: unknown): Reading => textReading(value, panel ? unverified : NO_PROVENANCE);
  return [
    ["Independently verified", { kind: "value", text: panel?.available === true ? "yes" : "no" }],
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


export function qualificationRows(panel: QualificationPanel | null | undefined): Array<readonly [string, Reading]> {
  return [
    ["G1", flagReading(panel?.g1, NO_QUALIFICATION)],
    ["H1", textReading(panel?.h1, NO_QUALIFICATION)],
    ["Containment", textReading(panel?.containment, NO_QUALIFICATION)],
    ["Claim-bearing", flagReading(panel?.claim_bearing, NO_QUALIFICATION)],
  ];
}

export const NO_WARNINGS = "No warnings projected.";

export function warningLines(warnings: readonly string[]): string[] {
  return warnings.length > 0 ? [...warnings] : [NO_WARNINGS];
}
