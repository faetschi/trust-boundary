// The claim gate (verification.ts). Two decisions, both pure: admission (may an element enter a projection,
// given its snapshot's source and scope) and verification (is an admitted element verified in its projection).
// The exhaustive tables below are the whole rule. Each row is the contract's value, not an implementation's.
import { describe, expect, test } from "vitest";
import { AUTHORITIES, SOURCE_CLASSES, VERIFICATION_SCOPES, type Authority } from "./contract";
import type { Item } from "./contract";
import { messageItem, resultItem } from "./testing/builders";
import {
  authorityFault,
  isItemEvidenceOnly,
  isVerifiedIn,
  isVerifiedProjection,
  scopeFault,
  verifyIn,
  type ProjectionState,
} from "./verification";

const LIVE: ProjectionState = { source: "live", verification: "structural-audit-only" };
const FIXTURE: ProjectionState = { source: "fixture", verification: "unverified-input" };

// A structural-audit success as the gateway sends it, with any field overridden. An override may be a value the contract
// does not admit: the fail-closed cases test exactly that.
function observed(overrides: { readonly authority?: string; readonly verification_scope?: string } = {}): Item {
  return { ...resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", authority: "observed", verification_scope: "structural-audit-only" }), ...overrides } as Item;
}

describe("isVerifiedIn: the single verified predicate", () => {
  test("is true exactly when the projection is live with a structural audit and the element is observed and structurally audited", () => {
    for (const source of SOURCE_CLASSES) {
      for (const projectionScope of VERIFICATION_SCOPES) {
        for (const authority of AUTHORITIES) {
          for (const itemScope of VERIFICATION_SCOPES) {
            const expected =
              source === "live" && projectionScope === "structural-audit-only" && authority === "observed" && itemScope === "structural-audit-only";
            const projection = { source, verification: projectionScope };
            const item = { authority, verification_scope: itemScope };
            expect(isVerifiedIn(projection, item), `${source}/${projectionScope} ${authority}/${itemScope}`).toBe(expected);
          }
        }
      }
    }
  });

  test("is true for an observed structural record in a live structural projection, and false for the same record anywhere else", () => {
    const record = observed();
    expect(isVerifiedIn(LIVE, record)).toBe(true);
    expect(isVerifiedIn(FIXTURE, record)).toBe(false);
    expect(isVerifiedIn({ source: "replay", verification: "structural-audit-only" }, record)).toBe(false);
    expect(isVerifiedIn({ source: "live", verification: "unverified-input" }, record)).toBe(false);
  });

  test("fails closed on an absent projection or element, and on any value the contract does not admit", () => {
    expect(isVerifiedIn(null, observed())).toBe(false);
    expect(isVerifiedIn(undefined, observed())).toBe(false);
    expect(isVerifiedIn(LIVE, null)).toBe(false);
    expect(isVerifiedIn(LIVE, undefined)).toBe(false);
    expect(isVerifiedIn({ source: "LIVE", verification: "structural-audit-only" }, observed())).toBe(false);
    expect(isVerifiedIn({ verification: "structural-audit-only" }, observed())).toBe(false);
    expect(isVerifiedIn(LIVE, observed({ authority: "Observed" }))).toBe(false);
    expect(isVerifiedIn(LIVE, observed({ verification_scope: "Structural-Audit-Only" }))).toBe(false);
  });
});

describe("isItemEvidenceOnly: the element's own declaration, without the projection", () => {
  test("is the legacy item-only predicate, and it is blind to the projection", () => {
    // This is the weak judgement. A fixture element that says observed and structural passes it, which is
    // exactly why no view may call it (verification.gate.test.ts) and why isVerifiedIn exists.
    expect(isItemEvidenceOnly(observed())).toBe(true);
    expect(isVerifiedIn(FIXTURE, observed())).toBe(false);
    expect(isItemEvidenceOnly(observed({ authority: "diagnostic" }))).toBe(false);
    expect(isItemEvidenceOnly(null)).toBe(false);
  });
});

describe("isVerifiedProjection", () => {
  test("is true only for a live projection with a structural audit scope", () => {
    for (const source of SOURCE_CLASSES) {
      for (const scope of VERIFICATION_SCOPES) {
        expect(isVerifiedProjection({ source, verification: scope })).toBe(source === "live" && scope === "structural-audit-only");
      }
    }
    expect(isVerifiedProjection(null)).toBe(false);
    expect(isVerifiedProjection(undefined)).toBe(false);
  });
});

describe("admission: the Go validateItem rules, per projection", () => {
  test("authoritative is refused in every source", () => {
    for (const source of SOURCE_CLASSES) {
      expect(authorityFault(source, "authoritative"), source).toBe("authoritative");
    }
  });

  test("observed is admitted only in a live projection", () => {
    for (const source of SOURCE_CLASSES) {
      expect(authorityFault(source, "observed"), source).toBe(source === "live" ? null : "observed-outside-live");
    }
  });

  test("diagnostic and unknown are admitted in every source", () => {
    for (const source of SOURCE_CLASSES) {
      for (const authority of ["diagnostic", "unknown"] as const satisfies readonly Authority[]) {
        expect(authorityFault(source, authority), `${source} ${authority}`).toBeNull();
      }
    }
  });

  test("an element's scope must equal the scope its snapshot declares, and nothing else is admitted", () => {
    for (const expected of VERIFICATION_SCOPES) {
      for (const scope of VERIFICATION_SCOPES) {
        expect(scopeFault(scope, expected), `${scope} in ${expected}`).toBe(scope === expected ? null : "scope-mismatch");
      }
    }
  });
});

describe("verifyIn: the only producer of VerifiedEvidence", () => {
  test("yields evidence for an element verified in its projection, carrying its identity", () => {
    const evidence = verifyIn(LIVE, observed());
    expect(evidence).not.toBeNull();
    expect(evidence?.id).toBe("r1");
    expect(evidence?.sequence).toBe(1);
  });

  test("yields nothing where isVerifiedIn is false", () => {
    expect(verifyIn(FIXTURE, observed())).toBeNull();
    expect(verifyIn(LIVE, observed({ verification_scope: "unverified-input" }))).toBeNull();
    expect(verifyIn(LIVE, null)).toBeNull();
    expect(verifyIn(LIVE, messageItem({ id: "m1", sequence: 2, authority: "observed", verification_scope: "structural-audit-only" }))).not.toBeNull();
  });
});
