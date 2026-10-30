// The one verification gate of the web chat, as the views ask it. The gate is core verification.ts: this module
// re-exports it, so every surface judges a record the same way: in the transcript, the trajectory rows, the sequence
// rail, the record inspector, the posture badges and the announcements. Pure: no DOM, no React.
//
// A record is verified only in a live structural projection, and only when the record is itself observed and
// structurally audited. Either half alone is not verification. A fixture or replay projection never verifies a record,
// whatever the record says of itself: the stream refuses such a record before it reaches a view (core validate.ts).

import type { ItemView } from "../core/contract";
import { isVerifiedIn, type ProjectionState } from "../core/verification";

export { isVerifiedIn, type ProjectionState };

// A success the projection confirms: verified in its projection, and not lossy. Lossy output is not terminal
// evidence, so a lossy success is never confirmed, whatever its verification.
export function confirmedIn(projection: ProjectionState | null | undefined, item: ItemView | null | undefined): boolean {
  return isVerifiedIn(projection, item) && item?.lossy !== true;
}
