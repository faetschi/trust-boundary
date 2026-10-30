import { describe, expect, it } from "vitest";
import type { GapRecord } from "../core/contract";
import {
  announcementsBetween,
  connectionStatus,
  evictedText,
  gapText,
  WINDOW_GAP_TEXT,
  type AnnouncementBasis,
} from "./status";

function basis(state: Parameters<typeof connectionStatus>[0], extra: Partial<AnnouncementBasis> = {}): AnnouncementBasis {
  return {
    status: connectionStatus(state, "", null),
    gapNotice: null,
    windowGap: null,
    ...extra,
  };
}

describe("connection status", () => {
  it("names connected only for an open stream", () => {
    expect(connectionStatus("open", "", null).label).toBe("connected");
    for (const state of ["connecting", "reconnecting", "offline", "error"] as const) {
      expect(connectionStatus(state, "", null).label).not.toBe("connected");
    }
  });

  it("shows the slow-handshake note only while connecting, and never as a state", () => {
    const connecting = connectionStatus("connecting", "slow handshake: still connecting", null);
    expect(connecting).toMatchObject({ label: "connecting…", warning: "slow handshake: still connecting" });
    expect(connectionStatus("open", "slow handshake: still connecting", null).warning).toBe("");
  });

  it("carries the reason for an error stop", () => {
    expect(connectionStatus("error", "", "the gateway is at its subscriber limit")).toMatchObject({
      label: "stream error",
      detail: "the gateway is at its subscriber limit",
    });
  });
});

const gap: GapRecord = {
  id: "g1",
  reason: "sequence_gap",
  first_missing: 3,
  last_missing: 3,
  recorded_at: "2026-10-13T09:12:04Z",
};

describe("gap disclosure text", () => {
  it("states that a recovered gap was recovered from a snapshot, and infers no success", () => {
    expect(gapText(gap)).toBe("Continuity gap (sequence gap). Recovered from a snapshot; no success is inferred.");
  });

  it("says that earlier history is not loaded, and infers no success for it", () => {
    expect(WINDOW_GAP_TEXT).toBe("Earlier history not loaded. No success is inferred for it.");
  });

  it("counts evicted records in the singular and the plural", () => {
    expect(evictedText(1)).toMatch(/^1 earlier record no longer retained/);
    expect(evictedText(3)).toMatch(/^3 earlier records no longer retained/);
  });
});

describe("announcements: each change is spoken once, on the change that produced it", () => {
  it("announces the connection changes that matter, and not the starting state", () => {
    expect(announcementsBetween(basis("connecting"), basis("open"))).toEqual(["Stream connected."]);
    expect(announcementsBetween(basis("open"), basis("reconnecting"))).toEqual(["Stream reconnecting."]);
    expect(announcementsBetween(basis("reconnecting"), basis("offline"))).toEqual(["Stream offline."]);
    expect(announcementsBetween(basis("open"), basis("connecting"))).toEqual([]);
  });

  it("announces an error stop with its reason", () => {
    const next = { ...basis("error"), status: connectionStatus("error", "", "subscriber limit") };
    expect(announcementsBetween(basis("open"), next)).toEqual(["Stream error: subscriber limit."]);
  });

  it("does not repeat a connection message when nothing changed", () => {
    expect(announcementsBetween(basis("open"), basis("open"))).toEqual([]);
  });

  it("announces a new gap once, and a new window gap once", () => {
    const withGap = basis("open", { gapNotice: gap });
    expect(announcementsBetween(basis("open"), withGap)).toEqual([gapText(gap)]);
    expect(announcementsBetween(withGap, withGap)).toEqual([]);
    const windowed = basis("open", { windowGap: { kind: "client", reason: "retained_window", since: 1 } });
    expect(announcementsBetween(basis("open"), windowed)).toEqual([WINDOW_GAP_TEXT]);
    expect(announcementsBetween(windowed, windowed)).toEqual([]);
  });
});
