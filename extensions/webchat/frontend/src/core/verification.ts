// verification.ts — the claim gate of the web chat, in one module. Pure: no DOM, no React, no I/O.
//
// Two decisions are made here, and they are the only place the web chat decides what a projection may claim.
//
//   1. Admission: whether an element may enter a projection at all. authorityFault and scopeFault mirror the Go
//      gateway's validateItem (internal/webchat/validate.go) and Store.AppendItem. An authoritative element is never
//      admitted. An observed element is admitted only into a live projection. An element's verification scope must
//      equal the scope its snapshot declares. validate.ts applies both to served snapshot items and to streamed
//      items, against the source and scope of the snapshot the stream is bound to.
//
//   2. Verification: whether an admitted element is verified in its projection. Only a live projection with a
//      structural audit verifies, and only an element that is itself observed and carries a structural-audit scope.
//      Either half alone is not verification.
//
// A rendered model takes the projection with the element (recordDot, statusText, railLanes, ...), so no view can
// judge a record from the record alone. isItemEvidenceOnly is the element's own declaration without the projection.
// isVerifiedIn is its only caller outside the differential suite, which compares it with the legacy isVerified.
// src/app may not name it (verification.gate.test.ts).

import type { Authority, ItemView, SourceClass, VerificationScope } from "./contract";

// The projection a stream is bound to: the source and verification scope of the snapshot it opened after.
export interface ProjectionContext {
  readonly source: SourceClass;
  readonly verification: VerificationScope;
}

// A projection as the gate reads it. The gate takes strings, because it must fail closed on values the contract does
// not admit. A ProjectionContext is a ProjectionState.
export interface ProjectionState {
  readonly source?: string;
  readonly verification?: string;
}

// The fields of an element the gate reads. Any other field is not evidence and is ignored.
export interface EvidenceFields {
  readonly authority?: string;
  readonly verification_scope?: string;
}

// Why an element may not enter a projection. The validator reports it as an AdmissionError, so a caller can tell a
// refused claim from a malformed payload.
export type AdmissionFault = "authoritative" | "observed-outside-live" | "scope-mismatch";

// Admission by authority, in the Go validator's order. authoritative is refused everywhere; observed only from a live
// source. The other authorities are admitted from any source.
export function authorityFault(source: SourceClass, authority: Authority): AdmissionFault | null {
  if (authority === "authoritative") return "authoritative";
  if (authority === "observed" && source !== "live") return "observed-outside-live";
  return null;
}

// Admission by scope: an element's verification scope must be the one its snapshot declares.
export function scopeFault(scope: VerificationScope, expected: VerificationScope): AdmissionFault | null {
  return scope === expected ? null : "scope-mismatch";
}

// Whether a projection is live and carries a structural audit. Only such a projection can verify an element.
export function isVerifiedProjection(projection: ProjectionState | null | undefined): boolean {
  return projection?.source === "live" && projection.verification === "structural-audit-only";
}

// Whether an element says of itself that it is observed and structurally audited. This is the element's own
// declaration. It does not say the element is verified: a fixture projection can carry such an element only if it
// was admitted, and admission refuses one, so the projection decides. Views must use isVerifiedIn.
export function isItemEvidenceOnly<T extends EvidenceFields>(item: T | null | undefined): boolean {
  if (!item) return false;
  return item.authority === "observed" && item.verification_scope === "structural-audit-only";
}

// The one verified predicate. An element is verified only in a live structural projection, and only when the element
// is itself observed and structurally audited. Pure and fail-closed: an absent projection or element, and any value
// outside the contract, is not verified.
export function isVerifiedIn<T extends EvidenceFields>(projection: ProjectionState | null | undefined, item: T | null | undefined): boolean {
  return isVerifiedProjection(projection) && isItemEvidenceOnly(item);
}

// The evidence a verified element carries. Its brand can be made only by verifyIn.
const evidenceBrand: unique symbol = Symbol("verified-evidence");

export type VerifiedEvidence = {
  readonly [evidenceBrand]: true;
  readonly id: string | undefined;
  readonly sequence: number | undefined;
};

// The evidence for an element that isVerifiedIn accepts, or null. This is the only way to obtain a VerifiedEvidence,
// so a success may be shown as ok only with evidence from the projection it is shown in.
export function verifyIn(projection: ProjectionState | null | undefined, item: ItemView | null | undefined): VerifiedEvidence | null {
  if (!item || !isVerifiedIn(projection, item)) return null;
  return { [evidenceBrand]: true, id: item.id, sequence: item.sequence };
}
