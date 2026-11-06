// The sequence rail's lanes and their words. Pure: no DOM, no React.
//
// Core sequenceLanes folds a LOST outcome into DENY. LOST is uncertain, not a refusal, so the rail keeps
// the record's own tone (core recordDot) and states each tone in its own words. Each record is judged in the
// projection the rail is drawn from (core verification.ts), so a fixture projection never draws a success as verified.

import type { ItemView } from "../../../core/contract";
import { recordDot, type RailTone } from "../../../core/trajectory";
import type { ProjectionState } from "../../../core/verification";

interface RailSegment {
  readonly tone: RailTone;
  weight: number;
}

interface RailLane {
  readonly id: string;
  readonly label: string;
  readonly segments: RailSegment[];
}

export interface RailModel {
  readonly lanes: RailLane[];
  readonly records: number;
  readonly axis: string;
  readonly ordinal: true;
  readonly note: string;
}

interface LaneDef {
  readonly id: string;
  readonly label: string;
  readonly match: (item: ItemView) => boolean;
}

const LANES: readonly LaneDef[] = [
  { id: "model", label: "Model", match: (item) => item.type === "message" },
  { id: "policy", label: "Policy", match: (item) => item.type === "decision" },
  { id: "tools", label: "Tools", match: (item) => item.type === "activity" || item.type === "result" },
];

// The rail's accessible words for each tone. A policy ALLOW is a permission, an unverified success is
// reported, LOST is not success and not a refusal, and a DENY is a refusal or a failure.
const RAIL_WORDS: Readonly<Record<string, string>> = {
  allow: "ALLOW, a permission not an outcome",
  ok: "succeeded, verified",
  reported: "succeeded, reported, not independently verified",
  deny: "DENY, denied or failed, not success",
  lost: "LOST, not success",
  pending: "pending, no outcome yet",
  unknown: "UNKNOWN, not success",
  idle: "message, no verdict or outcome",
  muted: "no record in this lane",
};

export function railToneText(tone: string): string {
  return RAIL_WORDS[tone] ?? RAIL_WORDS.muted ?? "no record in this lane";
}

// The lanes over every retained record, in record order. Every lane walks the same order, so the three
// share one axis, and a lane's muted spans show what it did not touch at that position. No span is a
// duration.
export function railLanes(items: readonly ItemView[], projection: ProjectionState | null | undefined): RailModel {
  const lanes: RailLane[] = [];
  for (const lane of LANES) {
    const segments: RailSegment[] = [];
    for (const item of items) {
      const tone: RailTone = lane.match(item) ? recordDot(item, projection) : "muted";
      const last = segments[segments.length - 1];
      if (last && last.tone === tone) last.weight += 1;
      else segments.push({ tone, weight: 1 });
    }
    lanes.push({ id: lane.id, label: lane.label, segments });
  }
  return {
    lanes,
    records: items.length,
    axis: "record sequence",
    ordinal: true,
    note: "The axis is record sequence, not time: the projection records no duration, so no span here is drawn as one.",
  };
}

// The key under the lanes, in words. Colour is the kind of record a lane draws; shape is the state of a record in it. Only the states
// that the retained records actually show are listed, so the key is as short as the picture needs.
const LANE_WORDS: Readonly<Record<string, string>> = {
  model: "assistant messages",
  policy: "policy decisions",
  tools: "tool requests and results",
};

const LEGEND_STATE_WORDS: Readonly<Record<string, string>> = {
  allow: "ALLOW, a permission",
  ok: "succeeded, verified",
  reported: "reported, not verified",
  pending: "pending",
  unknown: "UNKNOWN",
  deny: "DENY or denied",
  lost: "LOST",
  idle: "a message",
};

interface RailLegend {
  readonly kinds: ReadonlyArray<{ readonly lane: string; readonly label: string; readonly words: string }>;
  readonly states: ReadonlyArray<{ readonly tone: string; readonly words: string }>;
}

export function railLegend(rail: RailModel): RailLegend {
  const shown = new Set<string>();
  for (const lane of rail.lanes) for (const segment of lane.segments) shown.add(segment.tone);
  const order = ["allow", "ok", "reported", "pending", "unknown", "deny", "lost", "idle"];
  const states = order.filter((tone) => shown.has(tone)).map((tone) => ({ tone, words: LEGEND_STATE_WORDS[tone] ?? tone }));
  if (shown.has("muted")) states.push({ tone: "muted", words: "no record of this kind here" });
  return { kinds: rail.lanes.map((lane) => ({ lane: lane.id, label: lane.label, words: LANE_WORDS[lane.id] ?? lane.label })), states };
}