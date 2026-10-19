import { describe, expect, it } from "vitest";
import { activityItem, messageItem } from "../../../core/testing/builders";
import type { Item } from "../../../core/contract";
import { markerFor, provenanceOf } from "./provenance";

// Items the builders make are unverified (authority diagnostic). These two make a verified one.
function observed(item: Item): Item {
  return { ...item, authority: "observed", verification_scope: "structural-audit-only" } as Item;
}

const unverified = (): boolean => false;
const verified = (): boolean => true;

const two = (): Item[] => [
  messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" }),
  activityItem({ id: "a1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "pending" }),
];

describe("provenanceOf: one transcript-level line when the visible items agree", () => {
  it("is empty for no visible items, so no line is shown", () => {
    expect(provenanceOf([], unverified, "fixture")).toEqual({ kind: "empty" });
  });

  it("states one unverified line for uniformly unverified items, with the source", () => {
    expect(provenanceOf(two(), unverified, "fixture")).toEqual({
      kind: "uniform",
      verified: false,
      text: "All 2 records in this view are unverified · source: fixture",
    });
  });

  it("states the singular for one record", () => {
    expect(provenanceOf([messageItem({ id: "m1", sequence: 1 })], unverified, "fixture")).toEqual({
      kind: "uniform",
      verified: false,
      text: "The one record in this view is unverified · source: fixture",
    });
  });

  it("states one verified line when every visible item is verified", () => {
    const items = two().map(observed);
    expect(provenanceOf(items, verified, "live")).toEqual({
      kind: "uniform",
      verified: true,
      text: "All 2 records in this view are verified · source: live",
    });
  });

  it("is mixed when some visible items are verified and some are not", () => {
    const [first, second] = two();
    const items = [first as Item, observed(second as Item)];
    const verifiedOf = (item: Item) => item.id === "a1";
    expect(provenanceOf(items, verifiedOf, "live")).toEqual({ kind: "mixed" });
  });

  it("is mixed when the items are unverified but do not share an authority", () => {
    const [first, second] = two() as [Item, Item];
    const other = { ...second, authority: "unknown" } as Item;
    expect(provenanceOf([first, other], unverified, "fixture")).toEqual({ kind: "mixed" });
  });

  it("is mixed when the items are verified but do not share a verification scope", () => {
    const [first, second] = two().map(observed) as [Item, Item];
    const other = { ...second, verification_scope: "unverified-input" } as Item;
    expect(provenanceOf([first, other], verified, "live")).toEqual({ kind: "mixed" });
  });
});

describe("markerFor: item-level markers exist only in a mixed projection", () => {
  it("gives no item marker when the visible items are uniformly unverified or uniformly verified", () => {
    expect(markerFor(provenanceOf(two(), unverified, "fixture"), false)).toBeNull();
    expect(markerFor(provenanceOf(two().map(observed), verified, "live"), true)).toBeNull();
    expect(markerFor({ kind: "empty" }, false)).toBeNull();
  });

  it("marks each item of a mixed projection by its own verification", () => {
    const mixed = { kind: "mixed" } as const;
    expect(markerFor(mixed, true)).toBe("verified");
    expect(markerFor(mixed, false)).toBe("unverified");
  });
});
