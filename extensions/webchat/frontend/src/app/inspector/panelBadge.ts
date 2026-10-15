import type { ItemView, Panels } from "../../core/contract";
import { isVerifiedProjection, type ProjectionState } from "../../core/verification";
import type { ChipTone } from "../../ui/Chip";
import { countValue, decisionsState, publicationReading, recoveryReading, verdictCount, type StateReading } from "../panels";
import type { PanelKey } from "../uiPrefs";

// The words on each governance tab, from the projection only. Pure. The words carry the meaning; the tone is one
// of three, and none of them is the success tone. A success is never a badge tone: an allow is a permission, and
// a success is reported unless the gate verifies it (the verified word keeps the neutral tone).

export type BadgeTone = "neutral" | "warn" | "danger";

export interface PanelBadge {
  readonly text: string;
  readonly tone: BadgeTone;
}

export interface BadgeSource extends ProjectionState {
  readonly loaded: boolean;
  readonly panels: Panels | null;
  readonly warnings: readonly string[];
  // The loaded records. A decision among them with a verdict is evidence against all-zero counts (see decisionsState).
  readonly items?: readonly ItemView[];
}

const UNAVAILABLE: PanelBadge = { text: "unavailable", tone: "neutral" };

// A state's chip tone, as a badge tone. A deny is danger; unknown, pending and lost are warnings; the rest, the
// success tone included, are neutral.
function toneOf(tone: ChipTone): BadgeTone {
  if (tone === "deny") return "danger";
  if (tone === "unknown" || tone === "pending" || tone === "lost") return "warn";
  return "neutral";
}

// The chip tone that draws a badge: the same pairs the rest of the page uses, never the success pair.
export function chipToneOf(tone: BadgeTone): ChipTone {
  if (tone === "danger") return "deny";
  if (tone === "warn") return "unknown";
  return "idle";
}

// The most severe tone of a set of badges, for a collapsed panel's dot.
export function worstToneOf(badges: readonly PanelBadge[]): BadgeTone {
  if (badges.some((badge) => badge.tone === "danger")) return "danger";
  if (badges.some((badge) => badge.tone === "warn")) return "warn";
  return "neutral";
}

// A count of verdicts, named as verdicts, so the tab's word and the footer's count cannot be read as the same thing.
function verdictsText(count: number, word: string): string {
  return `${count} ${word} verdict${count === 1 ? "" : "s"}`;
}

// The Decisions tab. Zero counts read "no decisions" only when a policy digest and no verdict back them up (decisionsState);
// otherwise they are the gateway's zero for an absent count, and read unavailable.
function decisionsBadge(source: BadgeSource): PanelBadge {
  const governance = source.loaded ? source.panels?.governance : undefined;
  if (!governance) return UNAVAILABLE;
  const allow = countValue(governance.allow_count);
  const deny = countValue(governance.deny_count);
  const unknown = countValue(governance.unknown_count);
  if (deny !== null && deny > 0) return { text: verdictsText(deny, "deny"), tone: "danger" };
  if (unknown !== null && unknown > 0) return { text: verdictsText(unknown, "unknown"), tone: "warn" };
  if (allow === null || deny === null || unknown === null) return UNAVAILABLE;
  if (allow > 0) return { text: "allow only", tone: "neutral" };
  return decisionsState(governance, verdictCount(source.items ?? [])) === "none"
    ? { text: "no decisions", tone: "neutral" }
    : UNAVAILABLE;
}

function provenanceBadge(source: BadgeSource): PanelBadge {
  const provenance = source.loaded ? source.panels?.provenance : undefined;
  return provenance?.available === true ? { text: "available", tone: "neutral" } : UNAVAILABLE;
}

function stateBadge(reading: StateReading): PanelBadge {
  return { text: reading.label, tone: toneOf(reading.tone) };
}

// The boundary is claim-bearing only when it claims and containment is established. Every other present panel reads
// not established; unavailable is for an absent panel alone. The contract has no established containment today
// (CONTAINMENT_VALUES), so the widening below keeps the comparison compiling until it does.
function qualificationBadge(source: BadgeSource): PanelBadge {
  const qualification = source.loaded ? source.panels?.qualification : undefined;
  if (!qualification) return UNAVAILABLE;
  const containment: string = qualification.containment;
  if (qualification.claim_bearing === true && containment === "established") {
    return { text: "claim-bearing", tone: "warn" };
  }
  return { text: "not established", tone: "neutral" };
}

export function panelBadge(key: PanelKey, source: BadgeSource): PanelBadge {
  const verified = isVerifiedProjection(source);
  switch (key) {
    case "decisions":
      return decisionsBadge(source);
    case "provenance":
      return provenanceBadge(source);
    case "publication":
      return stateBadge(publicationReading(source.loaded ? source.panels?.publication : undefined, verified));
    case "recovery":
      return stateBadge(recoveryReading(source.loaded ? source.panels?.recovery : undefined, verified));
    case "qualification":
      return qualificationBadge(source);
  }
}

// The warnings count, shown on the qualification tab beside its word. Nothing before a projection loads, and
// nothing when there are no warnings.
export function warningBadge(source: Pick<BadgeSource, "loaded" | "warnings">): PanelBadge | null {
  const count = source.loaded && Array.isArray(source.warnings) ? source.warnings.length : 0;
  if (count === 0) return null;
  return { text: `${count} warning${count === 1 ? "" : "s"}`, tone: "warn" };
}
