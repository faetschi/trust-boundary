// Connection status, gap disclosure and announcement text. Pure: no DOM, no timer.
// The words are the legacy ones (internal/webchat/assets/render.mjs, app.mjs), so the two
// clients speak the same way about the same state.

import { humanize } from "../core/format";
import type { ConnectionState, StoredGap, WindowGap } from "../core/store";

export interface ConnectionStatus {
  readonly state: ConnectionState;
  // The state in words. "connected" only when the stream is open.
  readonly label: string;
  // The slow-handshake note. Shown while a connection attempt is slow; it never cancels it.
  readonly warning: string;
  // The reason a stream stopped, when it stopped with one.
  readonly detail: string;
}

const LABELS: Readonly<Record<ConnectionState, string>> = {
  connecting: "connecting…",
  open: "connected",
  reconnecting: "reconnecting…",
  offline: "offline",
  error: "stream error",
};

export function connectionStatus(state: ConnectionState, warning: string, error: string | null): ConnectionStatus {
  return {
    state,
    label: LABELS[state],
    warning: state === "connecting" ? warning : "",
    detail: state === "error" ? (error ?? "") : "",
  };
}

export const WINDOW_GAP_TEXT = "Earlier history not loaded. No success is inferred for it.";

// A continuity gap that the client recovered from a snapshot. The text says so, because
// a recovered gap is not a failure, and it never implies that a missing element succeeded.
export function gapText(gap: StoredGap): string {
  return `Continuity gap (${humanize(gap.reason)}). Recovered from a snapshot; no success is inferred.`;
}

export function evictedText(count: number): string {
  return `${count} earlier ${count === 1 ? "record" : "records"} no longer retained in this window. Counts describe the retained window only.`;
}

export interface AnnouncementBasis {
  readonly status: ConnectionStatus;
  readonly gapNotice: StoredGap | null;
  readonly windowGap: WindowGap | null;
}

const CONNECTION_MESSAGES: Readonly<Partial<Record<ConnectionState, string>>> = {
  open: "Stream connected.",
  reconnecting: "Stream reconnecting.",
  offline: "Stream offline.",
};

// The messages the single announcer should speak for a change from `previous` to `next`.
// Each one is spoken once, on the change that produced it. The initial "connecting" state
// is silent, so a page load does not announce its own starting state.
export function announcementsBetween(previous: AnnouncementBasis, next: AnnouncementBasis): string[] {
  const messages: string[] = [];
  if (next.status.state !== previous.status.state) {
    if (next.status.state === "error") {
      messages.push(next.status.detail ? `Stream error: ${next.status.detail}.` : "Stream error.");
    } else {
      const message = CONNECTION_MESSAGES[next.status.state];
      if (message) messages.push(message);
    }
  }
  if (next.gapNotice !== null && next.gapNotice !== previous.gapNotice) messages.push(gapText(next.gapNotice));
  if (next.windowGap !== null && previous.windowGap === null) messages.push(WINDOW_GAP_TEXT);
  return messages;
}
