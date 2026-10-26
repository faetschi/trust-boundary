import { describe, expect, it } from "vitest";
import { createFakeGateway, NOW_MS, SESSION_ID, TURN_1 } from "../test-support/liveGateway";
import { ContractError, parseDocument, parseSnapshot } from "./validate";

const options = { nowMs: NOW_MS };

function liveSnapshot(): Record<string, unknown> {
  const gateway = createFakeGateway({
    events: [{ kind: "turn_admitted", turn_id: TURN_1, text: "fix it" }],
    turns: [{ id: TURN_1, ordinal: 1, state: "awaiting_review", prompt: "fix it", candidate_digest: "sha256:cand1", review_timeout_seconds: 600 }],
  });
  return JSON.parse(JSON.stringify(gateway.snapshot())) as Record<string, unknown>;
}

function liveView(snapshot: Record<string, unknown>): Record<string, unknown> {
  return snapshot.live as Record<string, unknown>;
}

describe("validate: the live session view of a live snapshot", () => {
  it("accepts the optional live field in a served live snapshot and keeps it", () => {
    const parsed = parseSnapshot(liveSnapshot(), options);
    expect(parsed.live?.id).toBe(SESSION_ID);
    expect(parsed.live?.revision).toBe(4);
    expect(parsed.live?.turns?.[0]).toMatchObject({ state: "awaiting_review", candidate_digest: "sha256:cand1", review_timeout_seconds: 600 });
  });

  it("still accepts a live snapshot with no live field", () => {
    const snapshot = liveSnapshot();
    delete snapshot.live;
    expect(parseSnapshot(snapshot, options).live).toBeUndefined();
  });

  it("refuses the field on any other source", () => {
    for (const source of ["fixture", "replay"]) {
      const snapshot = liveSnapshot();
      snapshot.source = source;
      snapshot.verification_scope = "unverified-input";
      (snapshot.items as Record<string, unknown>[]).forEach((item) => {
        item.authority = "diagnostic";
        item.verification_scope = "unverified-input";
      });
      expect(() => parseSnapshot(snapshot, options), source).toThrow(/only a live projection may carry a live session view/);
    }
  });

  it("refuses the field in a document, which is never a live projection", () => {
    const envelope = { schema_version: "tbound-webchat-document/v1", snapshot: { ...liveSnapshot(), source: "fixture" } };
    expect(() => parseDocument(envelope, "fixture", options)).toThrow(ContractError);
    expect(() => parseDocument(envelope, "fixture", options)).toThrow(/live session view/);
  });

  it("refuses a view that is not the snapshot's session", () => {
    const snapshot = liveSnapshot();
    liveView(snapshot).id = "psess-other";
    expect(() => parseSnapshot(snapshot, options)).toThrow(/does not match the snapshot epoch/);
  });

  it("is strict: an unknown field, a wrong type and a negative count are refused", () => {
    const unknownField = liveSnapshot();
    liveView(unknownField).principal = "uid-1000";
    expect(() => parseSnapshot(unknownField, options)).toThrow(/unknown field "principal"/);

    const wrongType = liveSnapshot();
    liveView(wrongType).revision = "4";
    expect(() => parseSnapshot(wrongType, options)).toThrow(/revision/);

    const negative = liveSnapshot();
    (liveView(negative).budget as Record<string, unknown>).tokens = -1;
    expect(() => parseSnapshot(negative, options)).toThrow(/tokens/);

    const nulled = liveSnapshot();
    liveView(nulled).blocked = null;
    expect(() => parseSnapshot(nulled, options)).toThrow(/blocked/);
  });

  it("is strict about a turn: an unknown field, a missing state and a malformed digest", () => {
    const extra = liveSnapshot();
    ((liveView(extra).turns as Record<string, unknown>[])[0] as Record<string, unknown>).prompt_digest = "x";
    expect(() => parseSnapshot(extra, options)).toThrow(/unknown field "prompt_digest"/);

    const stateless = liveSnapshot();
    delete ((liveView(stateless).turns as Record<string, unknown>[])[0] as Record<string, unknown>).state;
    expect(() => parseSnapshot(stateless, options)).toThrow(/state/);

    const digest = liveSnapshot();
    ((liveView(digest).turns as Record<string, unknown>[])[0] as Record<string, unknown>).candidate_digest = "has space";
    expect(() => parseSnapshot(digest, options)).toThrow(/candidate_digest/);
  });

  it("keeps prose as data: a prompt with markup or a control character is size-checked, not refused", () => {
    const snapshot = liveSnapshot();
    ((liveView(snapshot).turns as Record<string, unknown>[])[0] as Record<string, unknown>).prompt = "<img src=x onerror=alert(1)>\u0007";
    expect(parseSnapshot(snapshot, options).live?.turns?.[0]?.prompt).toContain("<img");
    const huge = liveSnapshot();
    ((liveView(huge).turns as Record<string, unknown>[])[0] as Record<string, unknown>).prompt = "x".repeat(20000);
    expect(() => parseSnapshot(huge, options)).toThrow(/exceeds/);
  });
});
