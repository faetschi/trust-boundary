// The provenance of the transcript: whether the records in view agree on their verification. When they
// do, the view says so once, in one line, and draws no item marker. Only a mixed view draws a marker on
// each item, so the disclosure is never hidden. Pure: no DOM, no React.

import type { ItemView } from "../../../core/contract";

export type Provenance =
  | { readonly kind: "empty" }
  | { readonly kind: "uniform"; readonly verified: boolean; readonly text: string }
  | { readonly kind: "mixed" };

export type Marker = "verified" | "unverified";

// Uniform when every item shares the same authority, verification scope and verdict of verification.
// `source` is the projection's source, named as the source badge names it.
export function provenanceOf<T extends ItemView>(
  items: readonly T[],
  verifiedOf: (item: T) => boolean,
  source: string,
): Provenance {
  const first = items[0];
  if (!first) return { kind: "empty" };
  const verified = verifiedOf(first);
  const shared = items.every(
    (item) =>
      item.authority === first.authority &&
      item.verification_scope === first.verification_scope &&
      verifiedOf(item) === verified,
  );
  if (!shared) return { kind: "mixed" };
  const word = verified ? "verified" : "unverified";
  const subject = items.length === 1 ? "The one record in this view is" : `All ${items.length} records in this view are`;
  return { kind: "uniform", verified, text: `${subject} ${word} · source: ${source}` };
}

// The item marker for one record. Only a mixed view draws one: a uniform view has its line.
export function markerFor(provenance: Provenance, verified: boolean): Marker | null {
  if (provenance.kind !== "mixed") return null;
  return verified ? "verified" : "unverified";
}
