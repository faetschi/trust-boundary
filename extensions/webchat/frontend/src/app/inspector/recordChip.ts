// The chip and status-word rules the ledger and the record inspector share. Pure: no DOM.
//
// The core names outcome states as chip variants (succeeded, denied, failed). The chip's tone
// names are the closed set the status dot shares, so one record cannot read one way as a dot
// and another as a chip. This file maps a variant to a tone, and the tone is never more than
// the core's own reading: an unknown variant takes the conservative unknown tone.

import { TONES, type ItemView, type Tone } from "../../core/contract";
import { SUCCESS_STATES, slug } from "../../core/format";
import type { RecordSource } from "../../core/trajectory";
import type { ChipTone, SourceRole } from "../../ui/Chip";

const VARIANT_TONES: ReadonlyMap<string, ChipTone> = new Map<string, ChipTone>([
  ["succeeded", "ok"],
  ["denied", "deny"],
  ["failed", "deny"],
]);

// A core chip variant as a chip tone. A Map, not an object, so that a key such as "constructor"
// reads as absent rather than as a prototype property.
export function chipToneOf(variant: string): ChipTone {
  const mapped = VARIANT_TONES.get(variant);
  if (mapped) return mapped;
  return (TONES as readonly string[]).includes(variant) ? (variant as ChipTone) : "unknown";
}

// The source role a chip is styled by. The core's "unknown" source has no role of its own, so it
// is styled as a message and still labelled Unknown.
export function sourceRole(source: RecordSource): SourceRole {
  return source === "unknown" ? "message" : source;
}

// The state chip's word. An unverified success reads "reported", and a lossy success reads "not
// confirmed", so no chip shows the confirmed word for either. Other states read as the
// projection names them.
export function stateWord(item: ItemView, dot: Tone): string {
  const state = item.state ?? "unknown";
  if (dot === "reported") return "reported";
  if (dot === "unknown" && item.lossy && SUCCESS_STATES.has(slug(state))) return "not confirmed";
  return state;
}
