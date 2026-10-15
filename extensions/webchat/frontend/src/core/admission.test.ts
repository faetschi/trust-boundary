// Admission at the typed boundary. A served snapshot and a streamed item must be judged by the same rule: the
// Go gateway's validateItem (internal/webchat/validate.go) and Store.AppendItem, which checks each streamed item
// against the base snapshot's source and scope. The table is fixture/replay/live x authority x scope, and every
// row is run through both paths, so the two cannot drift apart.
import { describe, expect, test } from "vitest";
import { AUTHORITIES, CONTRACT_VERSION, type Authority, type Item, type SourceClass, type VerificationScope } from "./contract";
import { messageItem, snapshotOf } from "./testing/builders";
import { AdmissionError, ContractError, parseEvent, parseSnapshot } from "./validate";
import type { ProjectionContext } from "./verification";

const NOW = Date.parse("2026-10-14T00:00:00Z");
const options = { nowMs: NOW };

function wire(value: unknown): unknown {
  return JSON.parse(JSON.stringify(value));
}

function itemWith(authority: Authority, scope: VerificationScope): Item {
  return messageItem({ id: "m1", sequence: 1, text: "hello", authority, verification_scope: scope });
}

function eventOf(item: Item): unknown {
  return wire({ schema_version: CONTRACT_VERSION, cursor: `e1:${item.sequence}`, kind: "item", item });
}

// The contract's own rule, written out: authoritative is never admitted; observed only from a live source.
function admitted(source: SourceClass, authority: Authority): boolean {
  if (authority === "authoritative") return false;
  return authority !== "observed" || source === "live";
}

const SOURCES: readonly SourceClass[] = ["fixture", "replay", "live"];
const SCOPES: readonly VerificationScope[] = ["unverified-input", "structural-audit-only"];

describe("admission table: served snapshot and streamed item agree", () => {
  for (const source of SOURCES) {
    for (const authority of AUTHORITIES) {
      for (const scope of SCOPES) {
        const label = `${source} x ${authority} x ${scope}`;

        test(`served snapshot: ${label}`, () => {
          const snapshot = wire(snapshotOf({ source, verification_scope: scope, items: [itemWith(authority, scope)] }));
          if (admitted(source, authority)) {
            expect(() => parseSnapshot(snapshot, options)).not.toThrow();
          } else {
            expect(() => parseSnapshot(snapshot, options)).toThrow(ContractError);
          }
        });

        test(`streamed item in the same projection: ${label}`, () => {
          const projection: ProjectionContext = { source, verification: scope };
          const event = eventOf(itemWith(authority, scope));
          if (admitted(source, authority)) {
            expect(parseEvent(event, { ...options, projection }).kind).toBe("item");
          } else {
            expect(() => parseEvent(event, { ...options, projection })).toThrow(ContractError);
          }
        });
      }
    }
  }
});

describe("streamed items are judged against the snapshot they arrived under", () => {
  test("an observed structural item is refused in a fixture or replay projection, even though it passes the item-only rule", () => {
    const event = eventOf(itemWith("observed", "structural-audit-only"));
    expect(() => parseEvent(event, { ...options, projection: { source: "fixture", verification: "structural-audit-only" } })).toThrow(AdmissionError);
    expect(() => parseEvent(event, { ...options, projection: { source: "replay", verification: "structural-audit-only" } })).toThrow(AdmissionError);
  });

  test("the same item is admitted in a live structural projection", () => {
    const event = eventOf(itemWith("observed", "structural-audit-only"));
    expect(parseEvent(event, { ...options, projection: { source: "live", verification: "structural-audit-only" } }).kind).toBe("item");
  });

  test("a streamed item whose scope differs from its projection's is refused, whatever its authority", () => {
    const event = eventOf(itemWith("diagnostic", "structural-audit-only"));
    expect(() => parseEvent(event, { ...options, projection: { source: "fixture", verification: "unverified-input" } })).toThrow(AdmissionError);
  });

  test("the refusal carries its typed fault, so a caller can tell a refused claim from a malformed payload", () => {
    const faultOf = (fn: () => unknown) => {
      try {
        fn();
      } catch (error) {
        return error instanceof AdmissionError ? error.fault : "not-an-admission-error";
      }
      return "admitted";
    };
    const live: ProjectionContext = { source: "live", verification: "structural-audit-only" };
    const fixture: ProjectionContext = { source: "fixture", verification: "structural-audit-only" };
    expect(faultOf(() => parseEvent(eventOf(itemWith("authoritative", "structural-audit-only")), { ...options, projection: live }))).toBe("authoritative");
    expect(faultOf(() => parseEvent(eventOf(itemWith("observed", "structural-audit-only")), { ...options, projection: fixture }))).toBe(
      "observed-outside-live",
    );
    expect(faultOf(() => parseEvent(eventOf(itemWith("diagnostic", "unverified-input")), { ...options, projection: fixture }))).toBe("scope-mismatch");
    expect(faultOf(() => parseEvent(eventOf(itemWith("observed", "structural-audit-only")), { ...options, projection: live }))).toBe("admitted");
  });

  test("an unavailable projection admits only unavailable-scope items, and only non-claiming authorities", () => {
    const projection: ProjectionContext = { source: "unavailable", verification: "unavailable" };
    expect(parseEvent(eventOf(itemWith("diagnostic", "unavailable")), { ...options, projection }).kind).toBe("item");
    expect(() => parseEvent(eventOf(itemWith("diagnostic", "unverified-input")), { ...options, projection })).toThrow(AdmissionError);
    expect(() => parseEvent(eventOf(itemWith("observed", "unavailable")), { ...options, projection })).toThrow(AdmissionError);
  });
});
