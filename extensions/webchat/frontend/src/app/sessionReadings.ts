// The words and tones about the session that the details card (HelperBar.tsx) draws: the source binding, the observation streams, and the
// read-only notes. Pure; no DOM.

import type { StreamHealth, StreamStatus } from "../core/contract";
import type { ChipTone } from "../ui/Chip";

// A stream that completed is not a verified outcome: the source did not claim completeness. So "complete" is
// neutral in its dot and its words, and never the success tone.
export const HEALTH_TONES: Readonly<Record<StreamHealth, ChipTone>> = {
  complete: "idle",
  observing: "pending",
  gap: "unknown",
  unavailable: "idle",
};

const HEALTH_WORDS: Readonly<Record<StreamHealth, string>> = {
  complete: "complete (source did not claim completeness)",
  observing: "observing",
  gap: "gap",
  unavailable: "unavailable",
};

// A stream's state in words. The dot is decorative; this text carries the meaning.
export function streamText(stream: StreamStatus): string {
  const reason = stream.reason ? ` (${stream.reason})` : "";
  const availability = stream.available ? "" : ", not available";
  return `${stream.label}: ${HEALTH_WORDS[stream.health] ?? stream.health}${reason}${availability}`;
}

// The name of the source the page is bound to. The fixture is named as such; any other source is named by its class.
export function bindingName(source: string): string {
  return source === "fixture" ? "Fixture workspace" : `Workspace (${source})`;
}

// The one session this page receives. Other sessions need a list route that does not exist (the legacy client
// says the same, render.mjs). The note states it, and it offers no control for it.
export const OTHER_SESSIONS_NOTE =
  "Other Pi sessions are not listed. This page receives one session from the supervisor. A session list needs the supervisor session service, which is not available in this build.";

export const READ_ONLY_NOTE =
  "Read-only projection. No launch, cancel, approve, fork or execute control exists in this client; those are supervisor authority.";

// In live mode the notes say what the controls are. They are requests, and the product service decides.
export const LIVE_FOOTNOTE =
  "Live session. Cancel, Publish and Discard are requests to the product service, which decides. This page holds no policy, effect or publication authority.";

// The one standing line of the live panel: the full sentence is in the details card, this one stays where the controls are.
export const LIVE_AUTHORITY_LINE = "Cancel, Publish and Discard are requests; the product service decides.";
