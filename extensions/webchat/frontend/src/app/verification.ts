// The one verification gate of the web chat. Every surface asks this module, so a record reads as
// verified in the same way in the transcript, the trajectory rows, the sequence rail, the record
// inspector, the posture badges and the announcements. Pure: no DOM, no React.
//
// A record is verified only when both hold: its projection is a live source with a structural audit
// (core format.ts isVerifiedProjection), and the record itself is observed and structurally audited
// (core format.ts isVerified). Either one alone is not verification. A fixture or replay projection
// never verifies a record, whatever the record says of itself: the stream does not refuse an observed
// record against the snapshot's source, so a fixture projection can carry one.

import type { ItemView } from "../core/contract";
import { isVerified, isVerifiedProjection } from "../core/format";

// The projection a record is shown in: its source and its projection-level verification.
export interface ProjectionState {
  readonly source?: string;
  readonly verification?: string;
}

// Whether a record is verified in its projection. This is the only verified predicate the app uses.
export function isVerifiedIn(projection: ProjectionState | null | undefined, item: ItemView | null | undefined): boolean {
  return isVerifiedProjection(projection) && isVerified(item);
}

// A success the projection confirms: verified in its projection, and not lossy. Lossy output is not
// terminal evidence, so a lossy success is never confirmed, whatever its verification.
export function confirmedIn(projection: ProjectionState | null | undefined, item: ItemView | null | undefined): boolean {
  return isVerifiedIn(projection, item) && item?.lossy !== true;
}

// The record as the core judges it. The core's record model (core trajectory.ts) reads verification
// from the record alone, so a record that is not verified in its projection is handed to the core with
// its scope demoted to unverified-input. The record itself is never edited, and the inspector shows the
// record's own evidence (see RecordInspector.tsx), not this judgement.
export function judgedItem<T extends ItemView>(projection: ProjectionState | null | undefined, item: T): T {
  if (!isVerified(item) || isVerifiedIn(projection, item)) return item;
  return { ...item, verification_scope: "unverified-input" } as T;
}
