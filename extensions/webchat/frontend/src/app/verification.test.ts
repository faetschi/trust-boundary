// The one verification gate (verification.ts). Every surface asks it, so these cases are the whole
// rule: a record is verified only in a live structural-audit projection, and only when the record
// itself is observed and structurally audited.
import { describe, expect, it } from "vitest";
import { resultItem } from "../core/testing/builders";
import type { Item } from "../core/contract";
import { statusText } from "../core/trajectory";
import { confirmedIn, isVerifiedIn } from "./verification";

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

// judgedItem is retired. Its guarantee, that the core cannot judge a fixture record verified, now holds in the
// model itself: each model function takes the projection, so the record is judged where it is shown.
describe("the model judges a record in the projection it is shown in", () => {
  it("reads a record verified in its projection as verified", () => {
    expect(statusText(succeeded(), live)).toBe("Result: succeeded, verified");
  });

  it("does not read the same observed record as verified in a fixture projection", () => {
    const text = statusText(succeeded(), fixture);
    expect(text).toBe("Result: succeeded, reported, not independently verified");
    expect(isVerifiedIn(fixture, succeeded())).toBe(false);
  });

  it("never edits the record it was given", () => {
    const item = succeeded();
    statusText(item, fixture);
    expect(item.verification_scope).toBe("structural-audit-only");
  });

  it("reads a record that was never verified as reported, in every projection", () => {
    const item = succeeded({ verification_scope: "unverified-input" });
    expect(statusText(item, live)).toBe("Result: succeeded, reported, not independently verified");
    expect(statusText(item, fixture)).toBe("Result: succeeded, reported, not independently verified");
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
