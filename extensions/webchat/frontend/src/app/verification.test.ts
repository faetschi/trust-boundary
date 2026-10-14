// The one verification gate (verification.ts). Every surface asks it, so these cases are the whole
// rule: a record is verified only in a live structural-audit projection, and only when the record
// itself is observed and structurally audited.
import { describe, expect, it } from "vitest";
import { resultItem } from "../core/testing/builders";
import type { Item } from "../core/contract";
import { confirmedIn, isVerifiedIn, judgedItem } from "./verification";

const live = { source: "live", verification: "structural-audit-only" } as const;
const fixture = { source: "fixture", verification: "unverified-input" } as const;
const replay = { source: "replay", verification: "structural-audit-only" } as const;

function succeeded(extra: Partial<Item> = {}): Item {
  return resultItem({
    id: "r1",
    sequence: 1,
    tool: "bash",
    tool_call_id: "c1",
    state: "succeeded",
    authority: "observed",
    verification_scope: "structural-audit-only",
    ...extra,
  }) as Item;
}

describe("isVerifiedIn: the one gate", () => {
  it("is true only for an observed, structurally audited record in a live structural-audit projection", () => {
    expect(isVerifiedIn(live, succeeded())).toBe(true);
  });

  it("is false for the same record in a fixture or a replay projection", () => {
    expect(isVerifiedIn(fixture, succeeded())).toBe(false);
    expect(isVerifiedIn(replay, succeeded())).toBe(false);
  });

  it("is false in a live projection when the record is not itself observed and audited", () => {
    expect(isVerifiedIn(live, succeeded({ verification_scope: "unverified-input" }))).toBe(false);
    expect(isVerifiedIn(live, succeeded({ authority: "diagnostic" }))).toBe(false);
    expect(isVerifiedIn(live, succeeded({ authority: "authoritative" }))).toBe(false);
  });

  it("fails closed on a missing projection or record", () => {
    expect(isVerifiedIn(null, succeeded())).toBe(false);
    expect(isVerifiedIn(undefined, succeeded())).toBe(false);
    expect(isVerifiedIn(live, null)).toBe(false);
    expect(isVerifiedIn(live, undefined)).toBe(false);
  });
});

describe("judgedItem: the record as the core judges it", () => {
  it("returns a record that is verified in its projection unchanged", () => {
    const item = succeeded();
    expect(judgedItem(live, item)).toBe(item);
  });

  it("demotes an observed record in a fixture projection, so no core helper can judge it verified", () => {
    const item = succeeded();
    const judged = judgedItem(fixture, item);
    expect(isVerifiedIn(fixture, judged)).toBe(false);
    expect(judged.verification_scope).toBe("unverified-input");
    expect(judged.id).toBe(item.id);
  });

  it("never edits the record it was given", () => {
    const item = succeeded();
    judgedItem(fixture, item);
    expect(item.verification_scope).toBe("structural-audit-only");
  });

  it("leaves a record that was never verified as it is", () => {
    const item = succeeded({ verification_scope: "unverified-input" });
    expect(judgedItem(fixture, item)).toBe(item);
  });
});

describe("confirmedIn: a success that is verified and not lossy", () => {
  it("confirms a verified success", () => {
    expect(confirmedIn(live, succeeded())).toBe(true);
  });

  it("does not confirm a lossy success, even when it is verified", () => {
    expect(confirmedIn(live, succeeded({ lossy: true }))).toBe(false);
  });

  it("does not confirm a success that is not verified in its projection", () => {
    expect(confirmedIn(fixture, succeeded())).toBe(false);
  });
});
