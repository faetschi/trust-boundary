// The Go fixtures the gateway serves (internal/webchat/fixtures) parsed by the TypeScript contract
// validator, plus the negative cases validate.go refuses and the claim boundary on presentation.
//
// The fixtures are read by import, not from disk, so this file needs no Node typings. The JSON is
// untyped at the boundary: each test passes it to a parser that accepts `unknown`.
import { describe, expect, test } from "vitest";

import demoJson from "../../../internal/webchat/fixtures/demo-workspace.json";
import replayJson from "../../../internal/webchat/fixtures/replay-gap.json";
import type { Item, Snapshot } from "./contract";
import { parseJsonText } from "./validate";
import { ContractError, parseDocument, parseEvent, parseReplayGap, parseSnapshot, utf8Length } from "./validate";
import { recordDot, statusText } from "./trajectory";
import { verifyItem } from "./format";

// A fixed clock for occurred_at checks: 2026-10-14, after every fixture timestamp.
const NOW = Date.parse("2026-10-14T00:00:00Z");
const options = { nowMs: NOW };

// A deep copy of a JSON value, so a negative test can change one field without touching the fixture.
function copy(value: unknown): unknown {
  return parseJsonText(JSON.stringify(value));
}

// The value at a path, with every key and index checked at run time.
function setIn(value: unknown, path: readonly (string | number)[], replacement: unknown): unknown {
  const [head, ...rest] = path;
  if (head === undefined) return replacement;
  if (typeof head === "number") {
    if (!Array.isArray(value)) throw new Error(`setIn: expected an array at ${head}`);
    const next = value.slice();
    next[head] = setIn(next[head], rest, replacement);
    return next;
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`setIn: expected an object at ${head}`);
  }
  const record: Record<string, unknown> = { ...value };
  record[head] = setIn(record[head], rest, replacement);
  return record;
}

// The served form of a document: the store assigns sequence 1..N and a cursor that ends at N.
// This mirrors store.go (NewStore), so the served shape can be checked by parseSnapshot.
function served(parsed: { snapshot: Snapshot }): unknown {
  const items = parsed.snapshot.items.map((item, index) => ({ ...item, sequence: index + 1 }));
  const last = items.length;
  return { ...parsed.snapshot, items, cursor: `${parsed.snapshot.epoch}:${last}` };
}

const demo = demoJson;
const replay = replayJson;

describe("the Go fixtures the gateway serves parse through the contract validator", () => {
  test("the --demo fixture is a valid document: 18 items, the fixture epoch and cursor, two warnings", () => {
    const parsed = parseDocument(demo, "fixture", options);
    expect(parsed.schema_version).toBe("tbound-webchat-document/v1");
    expect(parsed.snapshot.items).toHaveLength(18);
    expect(parsed.snapshot.cursor).toBe("fixture-2f1c:18");
    expect(parsed.snapshot.warnings).toHaveLength(2);
    expect(parsed.snapshot.session.status).toBe("completed");
  });

  test("the --demo fixture, served with store sequences, is a valid served snapshot", () => {
    const parsed = parseDocument(demo, "fixture", options);
    const snapshot = parseSnapshot(served(parsed), options);
    expect(snapshot.items.map((item) => item.sequence)).toEqual(Array.from({ length: 18 }, (_, i) => i + 1));
    expect(snapshot.cursor).toBe("fixture-2f1c:18");
  });

  test("the --replay fixture is a valid replay document with its continuity gap", () => {
    const parsed = parseDocument(replay, "replay", options);
    expect(parsed.snapshot.source).toBe("replay");
    expect(parsed.snapshot.gaps?.length ?? 0).toBeGreaterThan(0);
    const served_ = parseSnapshot(served(parsed), options);
    expect(served_.items.length).toBe(parsed.snapshot.items.length);
  });

  test("the documents keep their items' fields exactly, apart from the downgrade", () => {
    const parsed = parseDocument(demo, "fixture", options);
    const original = demo.snapshot.items;
    parsed.snapshot.items.forEach((item: Item, index) => {
      const source = original[index];
      expect(source).toBeDefined();
      expect(item.id).toBe(source?.id);
      expect(item.authority).toBe("diagnostic");
      expect(item.verification_scope).toBe("unverified-input");
    });
  });
});

describe("negative: the validator refuses what validate.go refuses", () => {
  const cases: readonly { readonly name: string; readonly build: () => unknown; readonly match: RegExp }[] = [
    {
      name: "an unknown top-level document field",
      build: () => setIn(copy(demo), ["extra"], 1),
      match: /unknown field "extra"/,
    },
    {
      name: "an unknown item field (a verdict on a message)",
      build: () => setIn(copy(demo), ["snapshot", "items", 0, "verdict"], "ALLOW"),
      match: /unknown field "verdict"/,
    },
    {
      name: "an unknown verdict value (not coerced to UNKNOWN)",
      build: () => setIn(copy(demo), ["snapshot", "items", 3, "verdict"], "MAYBE"),
      match: /unsupported value "MAYBE"/,
    },
    {
      name: "an unknown result state (not coerced to success or unknown)",
      build: () => setIn(copy(demo), ["snapshot", "items", 4, "state"], "success"),
      match: /unsupported value "success"/,
    },
    {
      name: "an unknown tool",
      build: () => setIn(copy(demo), ["snapshot", "items", 2, "tool"], "exec"),
      match: /unsupported value "exec"/,
    },
    {
      name: "a lower-case verdict",
      build: () => setIn(copy(demo), ["snapshot", "items", 3, "verdict"], "allow"),
      match: /unsupported value "allow"/,
    },
    {
      name: "a document whose source is live (a live projection cannot come from a file)",
      build: () => setIn(copy(demo), ["snapshot", "source"], "live"),
      match: /does not match the requested source/,
    },
    {
      name: "a document that claims containment",
      build: () => setIn(copy(demo), ["snapshot", "panels", "qualification", "containment"], "established"),
      match: /cannot claim containment qualification/,
    },
    {
      name: "a document that claims an executed protocol",
      build: () => setIn(copy(demo), ["snapshot", "panels", "qualification", "h1"], "executed"),
      match: /cannot claim an executed research protocol/,
    },
    {
      name: "an unknown authority in a document (refused before the downgrade replaces it)",
      build: () => setIn(copy(demo), ["snapshot", "items", 0, "authority"], "verified"),
      match: /unsupported value "verified"/,
    },
    {
      name: "a null where the contract has no null",
      build: () => setIn(copy(demo), ["snapshot", "items", 0, "turn_id"], null),
      match: /must not be null/,
    },
    {
      name: "a control character in transcript text",
      build: () => setIn(copy(demo), ["snapshot", "items", 0, "text"], "hello\u0007world"),
      match: /contains a control character/,
    },
    {
      name: "a reason code that is not lower-case",
      build: () => setIn(copy(demo), ["snapshot", "items", 3, "reason_code"], "Policy_Rule"),
      match: /not a registered reason code/,
    },
    {
      name: "an occurred_at more than five minutes in the future",
      build: () => setIn(copy(demo), ["snapshot", "items", 0, "occurred_at"], "2026-10-14T01:00:00Z"),
      match: /is in the future/,
    },
    {
      name: "the zero time as occurred_at",
      build: () => setIn(copy(demo), ["snapshot", "items", 0, "occurred_at"], "0001-01-01T00:00:00Z"),
      match: /zero time/,
    },
    {
      name: "a duplicate item id",
      build: () => setIn(copy(demo), ["snapshot", "items", 1, "id"], "msg-01-user"),
      match: /duplicate item/,
    },
    {
      name: "a session status outside the closed set",
      build: () => setIn(copy(demo), ["snapshot", "session", "status"], "paused"),
      match: /unsupported value "paused"/,
    },
    {
      name: "a stream that is unavailable and available at once",
      build: () => setIn(copy(demo), ["snapshot", "streams", 1, "available"], true),
      match: /unavailable stream cannot be available/,
    },
  ];

  for (const testCase of cases) {
    test(`refuses ${testCase.name}`, () => {
      expect(() => parseDocument(testCase.build(), "fixture", options)).toThrow(ContractError);
      expect(() => parseDocument(testCase.build(), "fixture", options)).toThrow(testCase.match);
    });
  }

  test("refuses an authoritative authority on a served snapshot, and observed outside a live source", () => {
    const parsed = parseDocument(demo, "fixture", options);
    const base = served(parsed);
    expect(() => parseSnapshot(setIn(base, ["items", 0, "authority"], "authoritative"), options)).toThrow(
      /authoritative authority is unavailable/,
    );
    expect(() => parseSnapshot(setIn(base, ["items", 0, "authority"], "observed"), options)).toThrow(
      /observed authority is refused outside a live projection/,
    );
  });

  test("refuses a served snapshot whose sequences are not 1..N in order, or whose cursor disagrees", () => {
    const parsed = parseDocument(demo, "fixture", options);
    const base = served(parsed);
    expect(() => parseSnapshot(setIn(base, ["items", 1, "sequence"], 3), options)).toThrow(/contiguous from 1/);
    expect(() => parseSnapshot(setIn(base, ["items", 0, "sequence"], 0), options)).toThrow(
      /set for every element or for none|at least 1|contiguous/,
    );
    expect(() => parseSnapshot(setIn(base, ["cursor"], "fixture-2f1c:17"), options)).toThrow(
      /does not match the last item sequence/,
    );
    expect(() => parseSnapshot(setIn(base, ["cursor"], "other-epoch:18"), options)).toThrow(/cursor: is invalid/);
    expect(() => parseSnapshot(setIn(base, ["source"], "unavailable"), options)).toThrow(
      /unavailable snapshot must use the unavailable verification scope/,
    );
  });

  test("refuses a document whose sequences are only partly set", () => {
    const withSequences = setIn(copy(demo), ["snapshot", "items", 0, "sequence"], 1);
    expect(() => parseDocument(withSequences, "fixture", options)).toThrow(/set for every element or for none/);
  });

  test("the byte bound on text counts UTF-8 bytes: 4096 bytes is accepted and one more byte is not", () => {
    const ascii = "a".repeat(4096);
    expect(utf8Length(ascii)).toBe(4096);
    const accepted = setIn(copy(demo), ["snapshot", "items", 0, "text"], ascii);
    expect(() => parseDocument(accepted, "fixture", options)).not.toThrow();

    // "é" is two bytes in UTF-8: 2048 of them are exactly 4096 bytes, 2049 are 4098.
    const twoByte = "é".repeat(2048);
    expect(utf8Length(twoByte)).toBe(4096);
    const refused = setIn(copy(demo), ["snapshot", "items", 0, "text"], `${twoByte}é`);
    expect(() => parseDocument(refused, "fixture", options)).toThrow(/exceeds 4096 bytes/);
  });

  test("the future bound allows five minutes of clock skew and no more", () => {
    const within = setIn(copy(demo), ["snapshot", "items", 0, "occurred_at"], "2026-10-14T00:04:00Z");
    expect(() => parseDocument(within, "fixture", options)).not.toThrow();
    const beyond = setIn(copy(demo), ["snapshot", "items", 0, "occurred_at"], "2026-10-14T00:06:00Z");
    expect(() => parseDocument(beyond, "fixture", options)).toThrow(/is in the future/);
  });
});

describe("downgrade: a document can claim less than the supervisor's, never more", () => {
  test("a verified-looking scope in a document is reduced to unverified input, not refused", () => {
    const claimed = setIn(copy(demo), ["snapshot", "items", 4, "verification_scope"], "structural-audit-only");
    const parsed = parseDocument(claimed, "fixture", options);
    expect(parsed.snapshot.items[4]?.verification_scope).toBe("unverified-input");
    expect(verifyItem(parsed.snapshot.items[4])).toBeNull();
  });

  test("provenance availability and the g1 and claim_bearing flags are forced false", () => {
    const claimed = setIn(copy(demo), ["snapshot", "panels", "provenance", "available"], true);
    const forced = setIn(claimed, ["snapshot", "panels", "qualification", "g1"], true);
    const doc = parseDocument(setIn(forced, ["snapshot", "panels", "qualification", "claim_bearing"], true), "fixture", options);
    expect(doc.snapshot.panels.provenance.available).toBe(false);
    expect(doc.snapshot.panels.qualification.g1).toBe(false);
    expect(doc.snapshot.panels.qualification.claim_bearing).toBe(false);
  });

  test("the input document is not mutated by the downgrade", () => {
    const claimed = copy(demo);
    const before = JSON.stringify(claimed);
    parseDocument(claimed, "fixture", options);
    expect(JSON.stringify(claimed)).toBe(before);
  });
});

describe("events and gaps", () => {
  test("an item event and a session event parse, and an item event may not carry a session", () => {
    const first = demo.snapshot.items[0];
    if (first === undefined) throw new Error("the demo fixture has no items");
    const item = { ...first, sequence: 1 };
    const itemEvent = parseEvent(
      { schema_version: "tbound-webchat-observation/v1", cursor: "fixture-2f1c:1", kind: "item", item },
      options,
    );
    expect(itemEvent.kind).toBe("item");

    const sessionEvent = parseEvent(
      {
        schema_version: "tbound-webchat-observation/v1",
        cursor: "fixture-2f1c:18",
        kind: "session",
        session: { status: "completed", turn_count: 1 },
      },
      options,
    );
    expect(sessionEvent.kind).toBe("session");

    expect(() =>
      parseEvent(
        {
          schema_version: "tbound-webchat-observation/v1",
          cursor: "fixture-2f1c:1",
          kind: "item",
          item,
          session: { status: "completed", turn_count: 1 },
        },
        options,
      ),
    ).toThrow(/an item event carries only an item/);
    expect(() =>
      parseEvent({ schema_version: "tbound-webchat-observation/v1", cursor: "fixture-2f1c:1", kind: "panel" }, options),
    ).toThrow(/unsupported value "panel"/);
  });

  test("a replay gap parses with its closed fields; an unknown field or a missing reason is refused", () => {
    const gap = {
      schema_version: "tbound-webchat-stream-gap/v1",
      reason: "subscriber_limit",
      requested_cursor: "e1:4",
      epoch: "e1",
      latest: 9,
    };
    expect(parseReplayGap(gap).reason).toBe("subscriber_limit");
    expect(() => parseReplayGap({ ...gap, extra: true })).toThrow(/unknown field "extra"/);
    expect(() => parseReplayGap({ ...gap, reason: undefined })).toThrow(/is required/);
  });
});

describe("claim boundary on presentation: ALLOW is not success, unverified success is reported, UNKNOWN stays UNKNOWN", () => {
  const parsed = parseDocument(demo, "fixture", options);
  const itemById = (id: string): Item => {
    const found = parsed.snapshot.items.find((item) => item.id === id);
    if (!found) throw new Error(`fixture item ${id} is missing`);
    return found;
  };

  test("an unverified success in the fixture is reported, never ok", () => {
    const edit = itemById("res-02-edit");
    if (edit.type !== "result") throw new Error("res-02-edit is not a result");
    expect(edit.state).toBe("succeeded");
    expect(recordDot(edit)).toBe("reported");
    expect(statusText(edit)).toContain("reported, not independently verified");
  });

  test("a policy ALLOW is a permission: its dot is allow, not ok", () => {
    expect(recordDot(itemById("dec-01-read"))).toBe("allow");
    expect(recordDot(itemById("dec-03-bash-egress"))).toBe("deny");
  });

  test("an UNKNOWN result stays unknown, and a lossy result is never ok", () => {
    expect(recordDot(itemById("res-05-bash-race"))).toBe("unknown");
    const lossySuccess = { ...itemById("res-01-read"), lossy: true };
    expect(recordDot(lossySuccess)).toBe("reported");
  });

  test("only a verified success earns the ok tone, and only with verification evidence", () => {
    const verified = {
      ...itemById("res-01-read"),
      authority: "observed" as const,
      verification_scope: "structural-audit-only" as const,
    };
    expect(verifyItem(verified)).not.toBeNull();
    expect(recordDot(verified)).toBe("ok");
    expect(recordDot({ ...verified, lossy: true })).toBe("unknown");
  });
});
