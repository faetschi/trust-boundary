// Property: outside a live, structurally audited projection, no rendered model reads a record as verified.
//
// A seeded random sequence of snapshots and streamed events is taken through the same typed boundary the stream
// uses (parseSnapshot / parseDocument / parseEvent, then applySnapshot / applyItem). A refused snapshot or event is
// skipped, as the stream recovers from it. After every step, each model function the views use is asked about each
// retained record, under the projection the workspace holds. The seeds are fixed, so a failure is reproducible.
import { describe, expect, test } from "vitest";
import {
  AUTHORITIES,
  CONTRACT_VERSION,
  DOCUMENT_VERSION,
  SOURCE_CLASSES,
  VERIFICATION_SCOPES,
  type Authority,
  type Item,
  type ItemView,
  type SourceClass,
  type VerificationScope,
} from "../core/contract";
import { applyItem, applySnapshot, createWorkspace, projectionOf, type Workspace } from "../core/store";
import { decisionItem, messageItem, resultItem, snapshotOf } from "../core/testing/builders";
import { recordChips, recordDetail, recordDot, recordOf, statusText, visibleRecords } from "../core/trajectory";
import { ContractError, parseDocument, parseEvent, parseSnapshot } from "../core/validate";
import { railLanes } from "./views/trajectory/rail";
import { stateReading } from "./views/chat/readings";
import { confirmedIn, isVerifiedIn, type ProjectionState } from "./verification";

const NOW = Date.parse("2026-10-14T00:00:00Z");
const options = { nowMs: NOW };
const SEEDS = 160;
const STEPS = 40;

// A small seeded generator (mulberry32). Nothing here is random without a seed.
function generator(seed: number) {
  let state = seed >>> 0;
  const next = (): number => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  return {
    int: (n: number) => Math.floor(next() * n),
    bool: (p = 0.5) => next() < p,
    pick: <T,>(values: readonly T[]): T => values[Math.floor(next() * values.length)] as T,
  };
}

type Random = ReturnType<typeof generator>;

// The wire form: JSON text, decoded with no type information. Every value enters through the boundary.
function wire(value: unknown): unknown {
  return JSON.parse(JSON.stringify(value));
}

// Most elements carry the scope of the projection they arrive in (so most are admitted), and many are observed (so the
// refusal and the verified paths both run). Nothing is chosen without the seed.
function randomItem(random: Random, sequence: number, id: string, projectionScope: VerificationScope): Item {
  const authority: Authority = random.bool(0.35) ? "observed" : random.pick(AUTHORITIES);
  const scope: VerificationScope = random.bool(0.8) ? projectionScope : random.pick(VERIFICATION_SCOPES);
  const kind = random.int(3);
  if (kind === 0) return messageItem({ id, sequence, text: "hello", authority, verification_scope: scope });
  if (kind === 1) {
    const lossy = random.bool(0.2);
    return resultItem({
      id,
      sequence,
      tool: "bash",
      tool_call_id: `c${id}`,
      state: random.pick(["succeeded", "failed", "unknown", "lost", "pending"] as const),
      authority,
      verification_scope: scope,
      ...(lossy ? { lossy: true } : {}),
    });
  }
  return decisionItem({
    id,
    sequence,
    tool: "bash",
    tool_call_id: `c${id}`,
    verdict: random.pick(["ALLOW", "DENY", "UNKNOWN"] as const),
    reason_code: "policy_rule_allow",
    authority,
    verification_scope: scope,
  });
}

interface Tally {
  nonLive: number;
  liveVerified: number;
  refusedSnapshots: number;
  refusedEvents: number;
  acceptedEvents: number;
}

// The findings for one record judged under a projection that is not claimable. Each is a model the views call.
function nonClaimableFindings(item: ItemView, projection: ProjectionState): string[] {
  const findings: string[] = [];
  if (isVerifiedIn(projection, item)) findings.push(`isVerifiedIn holds for ${item.id}`);
  if (recordDot(item, projection) === "ok") findings.push(`recordDot is ok for ${item.id}`);
  if (recordOf(item, projection).dot === "ok") findings.push(`recordOf is ok for ${item.id}`);
  if (statusText(item, projection).includes(", verified")) findings.push(`statusText reads ${item.id} as verified`);
  if (recordChips(item, projection).some((chip) => chip.variant === "succeeded")) findings.push(`a chip reads ${item.id} as verified`);
  if (JSON.stringify(recordDetail(item, projection)).includes('"chip":"succeeded"')) findings.push(`detail reads ${item.id} as verified`);
  if (item.state !== undefined && stateReading(item.state, confirmedIn(projection, item), item.lossy).tone === "ok") {
    findings.push(`a state reading is ok for ${item.id}`);
  }
  return findings;
}

// Asks every model function the views use about the retained window, under the workspace projection.
// Each finding is a string, so one failing seed reports every step it broke, not only the first.
function checkModels(state: Workspace, where: string, tally: Tally, failures: string[]): void {
  const projection = projectionOf(state);
  const claimable = projection.source === "live" && projection.verification === "structural-audit-only";
  const items: readonly ItemView[] = state.items;
  const fail = (what: string) => failures.push(`${where} [${projection.source}/${projection.verification}] ${what}`);

  if (!claimable) {
    for (const item of items) {
      tally.nonLive += 1;
      if (projection.source !== "live" && item.authority === "observed") fail(`observed record ${item.id} is retained outside a live source`);
      for (const finding of nonClaimableFindings(item, projection)) fail(finding);
    }
    // A record the boundary would refuse here (observed and structurally audited, outside a live source) is judged by the
    // models as well. The claim rests on the models too, not only on the boundary that keeps such a record out.
    const probe = resultItem({
      id: "probe",
      sequence: 1,
      tool: "bash",
      tool_call_id: "probe",
      state: "succeeded",
      authority: "observed",
      verification_scope: "structural-audit-only",
    });
    for (const finding of nonClaimableFindings(probe, projection)) fail(`probe: ${finding}`);
    const window: readonly ItemView[] = [...items, probe];
    const tones = railLanes(window, projection).lanes.flatMap((lane) => lane.segments.map((segment) => segment.tone));
    if (tones.includes("ok")) fail("the rail draws a verified segment");
    const visible = visibleRecords({ items: [...window], query: "", group: "turn", projection });
    if (visible.groups.some((group) => group.records.some((view) => view.dot === "ok"))) fail("the ledger draws a verified row");
  } else {
    for (const item of items) if (recordDot(item, projection) === "ok") tally.liveVerified += 1;
  }
}

// One seeded run. It returns the findings and the counts, so the caller can check that the run was not vacuous.
function run(seed: number): { failures: string[]; tally: Tally } {
  const random = generator(seed);
  const state = createWorkspace();
  const failures: string[] = [];
  const tally: Tally = { nonLive: 0, liveVerified: 0, refusedSnapshots: 0, refusedEvents: 0, acceptedEvents: 0 };

  for (let step = 0; step < STEPS; step += 1) {
    const where = `seed ${seed} step ${step}`;
    if (random.bool(0.2)) {
      // A snapshot: served, or a fixture/replay document, from any source (a reconnect may change it).
      const source: SourceClass = random.bool(0.35) ? "live" : random.pick(SOURCE_CLASSES);
      const scope: VerificationScope =
        source === "unavailable" ? "unavailable" : source === "live" && random.bool(0.6) ? "structural-audit-only" : random.pick(["unverified-input", "structural-audit-only"] as const);
      const count = random.int(4);
      const items = Array.from({ length: count }, (_, index) => randomItem(random, index + 1, `s${index + 1}`, scope));
      const snapshot = snapshotOf({ source, verification_scope: scope, items });
      try {
        const accepted =
          (source === "fixture" || source === "replay") && random.bool(0.4)
            ? parseDocument(wire({ schema_version: DOCUMENT_VERSION, snapshot }), source, options).snapshot
            : parseSnapshot(wire(snapshot), options);
        applySnapshot(state, accepted);
      } catch (error) {
        if (!(error instanceof ContractError)) throw error;
        tally.refusedSnapshots += 1;
      }
    } else {
      // A streamed item, judged in the projection the workspace holds now, as the stream judges it.
      const sequence = random.bool(0.9) ? state.lastSequence + 1 : random.int(6) + 1;
      const projectionScope = projectionOf(state).verification;
      const event = wire({ schema_version: CONTRACT_VERSION, cursor: `e1:${sequence}`, kind: "item", item: randomItem(random, sequence, `e${step}`, projectionScope) });
      try {
        const parsed = parseEvent(event, { ...options, projection: projectionOf(state) });
        if (parsed.kind === "item") {
          applyItem(state, parsed.item);
          tally.acceptedEvents += 1;
        }
      } catch (error) {
        if (!(error instanceof ContractError)) throw error;
        tally.refusedEvents += 1;
      }
    }
    checkModels(state, where, tally, failures);
  }
  return { failures, tally };
}

describe("verification property: no rendered model reads a record as verified outside a live structural projection", () => {
  test(`${SEEDS} seeded sequences of snapshots and streamed events`, () => {
    const total: Tally = { nonLive: 0, liveVerified: 0, refusedSnapshots: 0, refusedEvents: 0, acceptedEvents: 0 };
    const failures: string[] = [];
    for (let seed = 1; seed <= SEEDS; seed += 1) {
      const result = run(seed);
      failures.push(...result.failures);
      for (const key of Object.keys(total) as (keyof Tally)[]) total[key] += result.tally[key];
    }
    expect(failures.slice(0, 20)).toEqual([]);
    console.log(`verification property totals: ${JSON.stringify(total)}`);
    // The run must have exercised both sides, or the property holds for the wrong reason.
    expect(total.nonLive).toBeGreaterThan(1000);
    expect(total.liveVerified).toBeGreaterThan(50);
    expect(total.refusedEvents).toBeGreaterThan(50);
    expect(total.refusedSnapshots).toBeGreaterThan(10);
  }, 60_000); // 160 runs take seconds on their own; the default 5-second budget fails under a parallel run.
});
