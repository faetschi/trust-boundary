// A fake live gateway for the live tests: a fetch that answers the routes of web-chat-contract.md section 9.3 from a small
// session model, with the same event-to-item mapping the Go gateway uses (live_render.go). Test-only (see dom.tsx).
//
// The model enforces what the product service enforces and the live client depends on: a stale expected_revision is a 409,
// a request key that was seen answers `replayed` without running again, a candidate digest that is not the recorded one is a
// 409, and an observe past the end of the events is a 400. Failures are injected per call with inject().

import type { FetchResponseLike } from "../core/api";
import { CONTRACT_VERSION, type LiveSessionView, type LiveTurn, type Snapshot } from "../core/contract";
import { panelsDefault, TIMESTAMP } from "../core/testing/builders";
import type { LiveFetch, LiveFetchInit } from "../live/client";

export const SESSION_ID = "psess-t1";
export const TURN_1 = `${SESSION_ID}/turn-1`;
export const NOW_MS = Date.parse(TIMESTAMP);

export interface FakeEvent {
  seq: number;
  kind: string;
  turn_id: string;
  text?: string;
  tool?: string;
  tool_call_id?: string;
  verdict?: string;
  reason_code?: string;
  hint?: string;
  outcome?: string;
  reason?: string;
  candidate_digest?: string;
  paths?: string[];
}

export interface Call {
  readonly method: string;
  readonly url: string;
  readonly headers: Readonly<Record<string, string>>;
  readonly init: LiveFetchInit;
  readonly body: unknown;
}

// reject: the call fails before the gateway sees it. lose: the gateway applies it, and the answer is lost on the way back.
export type Reply = { reject: string } | { lose: string } | { status: number; body: unknown; ok?: boolean } | { unreadable: number };

// A turn the fake may change in place, as the service does.
export type MutableTurn = { -readonly [K in keyof LiveTurn]: LiveTurn[K] };

export interface FakeGateway {
  readonly fetch: LiveFetch;
  readonly calls: Call[];
  readonly callsTo: (method: string, pathPart: string) => Call[];
  readonly state: GatewayState;
  // Answers the next `times` calls the matcher accepts with `reply`, before the model sees them.
  inject(match: (call: Call) => boolean, reply: Reply, times?: number): void;
  // Adds events and wakes the polls waiting for them.
  append(events: Omit<FakeEvent, "seq">[]): void;
  // Moves the open turn to awaiting review with a candidate.
  awaitReview(digest: string, changes?: { path: string; op: string; diff: string }[]): void;
  view(): LiveSessionView;
  snapshot(): Snapshot;
}

export interface GatewayState {
  csrf: string;
  revision: number;
  blocked: boolean;
  readOnly: boolean;
  budget: { host_calls: number; tokens: number; duration_seconds: number };
  consumed: { host_calls: number; tokens: number; duration_seconds: number };
  reserved: { host_calls: number; tokens: number; duration_seconds: number };
  turns: MutableTurn[];
  events: FakeEvent[];
  candidate: { digest: string; changes: { path: string; op: string; diff: string; truncated?: boolean }[] } | null;
  sessions: LiveSessionView[];
}

function ok(body: unknown): FetchResponseLike {
  return { ok: true, status: 200, json: async () => body };
}

function failure(status: number, code: string, detail = "", outcomeUnknown = false): FetchResponseLike {
  const error: Record<string, unknown> = { code };
  if (detail) error.detail = detail;
  if (outcomeUnknown) error.outcome_unknown = true;
  return { ok: false, status, json: async () => ({ error }) };
}

const short = (turnId: string) => turnId.slice(turnId.lastIndexOf("/") + 1);

// The Go gateway's mapping of one event to one item.
function itemOf(event: FakeEvent): Record<string, unknown> {
  const base = {
    schema_version: CONTRACT_VERSION,
    sequence: event.seq + 1,
    id: `ev-${event.seq}`,
    turn_id: short(event.turn_id),
    occurred_at: TIMESTAMP,
    authority: "observed",
    verification_scope: "structural-audit-only",
  };
  const system = (text: string) => ({ ...base, type: "message", role: "system", text });
  switch (event.kind) {
    case "turn_admitted":
      return { ...base, type: "message", role: "user", text: event.text ?? "(empty)" };
    case "assistant_message":
      return { ...base, type: "message", role: "assistant", text: event.text ?? "(empty)" };
    case "tool_call":
      return {
        ...base,
        type: "decision",
        tool: event.tool,
        tool_call_id: event.tool_call_id,
        verdict: event.verdict ?? "UNKNOWN",
        reason_code: event.reason_code ?? "unspecified",
        ...(event.hint ? { summary: event.hint } : {}),
      };
    case "tool_result":
      return {
        ...base,
        type: "result",
        tool: event.tool,
        tool_call_id: event.tool_call_id,
        state: event.verdict === "DENY" ? "denied" : "succeeded",
        summary: event.text ?? "",
      };
    case "turn_linked":
      return system("Turn started.");
    case "turn_cancel_requested":
      return system("Cancel requested.");
    case "turn_awaiting_review":
      return system(`Awaiting review. Candidate ${event.candidate_digest}.`);
    case "turn_resolution_requested":
      return system(`Resolution requested: ${event.reason}.`);
    case "turn_terminal":
      return system(`Turn finished: ${event.outcome}.`);
    default:
      return system(`Unmapped event ${JSON.stringify(event.kind)}.`);
  }
}

export function createFakeGateway(initial: { events?: Omit<FakeEvent, "seq">[]; turns?: LiveTurn[] } = {}): FakeGateway {
  const state: GatewayState = {
    csrf: "csrf-token-1",
    revision: 4,
    blocked: false,
    readOnly: false,
    budget: { host_calls: 10, tokens: 1000, duration_seconds: 60 },
    consumed: { host_calls: 1, tokens: 10, duration_seconds: 2 },
    reserved: { host_calls: 0, tokens: 0, duration_seconds: 0 },
    turns: (initial.turns ?? []).map((turn) => ({ ...turn })),
    events: (initial.events ?? []).map((event, seq) => ({ ...event, seq })),
    candidate: null,
    sessions: [],
  };
  const calls: Call[] = [];
  const injected: { match: (call: Call) => boolean; reply: Reply; times: number }[] = [];
  const waiting: (() => void)[] = [];
  const seenKeys = new Map<string, { readonly body: string; readonly answer: object }>();
  let ordinal = state.turns.length;

  const view = (withTurns = true): LiveSessionView => ({
    id: SESSION_ID,
    project_id: "demo",
    provider_profile: "prof",
    publication_mode: "review",
    ...(state.readOnly ? { read_only: true } : {}),
    revision: state.revision,
    ...(state.blocked ? { blocked: true } : {}),
    budget: { ...state.budget },
    consumed: { ...state.consumed },
    reserved: { ...state.reserved },
    total_turns: state.turns.length,
    ...(withTurns && state.turns.length > 0 ? { turns: state.turns.map((turn) => ({ ...turn })) } : {}),
  });

  const snapshot = (): Snapshot => {
    const items = state.events.map(itemOf);
    return {
      schema_version: CONTRACT_VERSION,
      epoch: SESSION_ID,
      cursor: `${SESSION_ID}:${items.length}`,
      source: "live",
      verification_scope: "structural-audit-only",
      generated_at: TIMESTAMP,
      session: { session_id: SESSION_ID, title: "demo / prof / review", status: "running", profile: "prof", turn_count: state.turns.length },
      panels: { ...panelsDefault(), governance: { allow_count: 0, deny_count: 0, unknown_count: 0 } },
      streams: [{ id: "product", label: "product service observations", health: "observing", available: true }],
      items: items as unknown as Snapshot["items"],
      warnings: ["event times are not recorded by the product protocol; occurred_at is the time the gateway rendered the item"],
      live: view(),
    };
  };

  function wake(): void {
    for (const resume of waiting.splice(0)) resume();
  }

  function append(events: Omit<FakeEvent, "seq">[]): void {
    for (const event of events) state.events.push({ ...event, seq: state.events.length });
    wake();
  }

  function currentTurn(id: string): MutableTurn | undefined {
    return state.turns.find((turn) => turn.id === id || short(turn.id) === id);
  }

  // A mutation under a request key, in the service's order: a key already seen answers its recorded result as replayed when the
  // request is the same, and is a conflict when it is not, before any revision or state is checked. Only then are the checks run.
  function mutate(call: Call, check: () => FetchResponseLike | null, apply: () => object): FetchResponseLike {
    const key = String((call.body as { request_key?: unknown }).request_key);
    const text = JSON.stringify(call.body);
    const seen = seenKeys.get(key);
    if (seen !== undefined) {
      return seen.body === text ? ok({ ...seen.answer, replayed: true }) : failure(409, "REQUEST_CONFLICT", "request key reused with a different request");
    }
    const refused = check();
    if (refused !== null) return refused;
    const answer = apply();
    seenKeys.set(key, { body: text, answer });
    return ok(answer);
  }

  const staleRevision = (body: Record<string, unknown> | undefined) =>
    Number(body?.expected_revision) !== state.revision ? failure(409, "STALE_REVISION", "the session changed") : null;

  async function route(call: Call): Promise<FetchResponseLike> {
    const url = new URL(call.url, "http://127.0.0.1:8791");
    const path = url.pathname;
    const body = call.body as Record<string, unknown> | undefined;
    if (call.method === "POST" && call.headers["X-TBound-CSRF"] !== state.csrf) return failure(403, "FORBIDDEN", "a valid X-TBound-CSRF header is required");
    if (path === "/api/v1/pair") return ok({ csrf_token: state.csrf, expires_at: "2026-10-14T09:12:04Z" });
    if (path === "/api/v1/contract") return ok({ mode: "live", read_only: false });
    if (path === "/api/v1/sessions" && call.method === "GET") return ok({ sessions: [view(false), ...state.sessions] });
    if (path === "/api/v1/sessions" && call.method === "POST") {
      return mutate(call, () => null, () => {
        const session: LiveSessionView = { ...view(false), id: "psess-new", project_id: String(body?.project_id), provider_profile: String(body?.provider_profile), revision: 1, total_turns: 0 };
        state.sessions.push(session);
        return { session };
      });
    }
    const match = /^\/api\/v1\/sessions\/([^/]+)(?:\/(events|turns)(?:\/([^/]+)\/(candidate|cancel|resolve))?)?$/.exec(path);
    if (match === null) return failure(404, "NOT_FOUND", "no such route");
    const [, session, tail, turnSegment, action] = match;
    if (session !== SESSION_ID && !(state.sessions.some((entry) => entry.id === session))) return failure(404, "UNKNOWN_SESSION", "no such session");
    if (tail === undefined) return ok(snapshot());
    if (tail === "events") {
      const cursor = Number(url.searchParams.get("cursor") ?? "0");
      const wait = Number(url.searchParams.get("wait") ?? "0");
      if (cursor > state.events.length) return failure(400, "INVALID_REQUEST", "cursor is past the end");
      if (cursor === state.events.length && wait > 0) {
        await new Promise<void>((resolve) => waiting.push(resolve));
        return route(call);
      }
      const events = state.events.slice(cursor, cursor + 512);
      return ok({
        schema_version: CONTRACT_VERSION,
        session_id: SESSION_ID,
        cursor,
        next_cursor: cursor + events.length,
        gap: false,
        events,
        items: events.map(itemOf),
      });
    }
    if (turnSegment === undefined && tail === "turns") {
      return mutate(call, () => staleRevision(body), () => {
        ordinal += 1;
        const turn: MutableTurn = { id: `${SESSION_ID}/turn-${ordinal}`, ordinal, state: "admitted", prompt: String(body?.prompt) };
        state.turns.push(turn);
        state.revision += 1;
        append([{ kind: "turn_admitted", turn_id: turn.id, text: String(body?.prompt) }]);
        return { turn: { ...turn } };
      });
    }
    const turn = currentTurn(String(turnSegment));
    if (turn === undefined) return failure(404, "UNKNOWN_TURN", "no such turn");
    if (action === "candidate") {
      if (turn.state !== "awaiting_review" || state.candidate === null) return failure(409, "REVIEW_NOT_PENDING", "no review is pending");
      return ok({
        candidate: {
          turn_id: turn.id,
          candidate_digest: state.candidate.digest,
          changes: state.candidate.changes,
        },
      });
    }
    if (action === "cancel") {
      return mutate(call, () => staleRevision(body), () => {
        turn.cancel_requested = true;
        state.revision += 1;
        append([{ kind: "turn_cancel_requested", turn_id: turn.id }]);
        return { turn: { ...turn } };
      });
    }
    if (action === "resolve") {
      return mutate(
        call,
        () => (body?.candidate_digest !== state.candidate?.digest ? failure(409, "CANDIDATE_MISMATCH", "the candidate changed") : staleRevision(body)),
        () => {
          turn.resolution = String(body?.decision);
          state.revision += 1;
          append([{ kind: "turn_resolution_requested", turn_id: turn.id, reason: String(body?.decision) }]);
          return { turn: { ...turn } };
        },
      );
    }
    return failure(404, "NOT_FOUND", "no such route");
  }

  const gateway: FakeGateway = {
    calls,
    state,
    callsTo: (method, pathPart) => calls.filter((call) => call.method === method && call.url.includes(pathPart)),
    fetch: async (url, init) => {
      const call: Call = {
        method: init.method,
        url,
        headers: init.headers,
        init,
        body: init.body === undefined ? undefined : JSON.parse(init.body),
      };
      calls.push(call);
      const index = injected.findIndex((entry) => entry.match(call));
      if (index >= 0) {
        const entry = injected[index];
        if (entry === undefined) throw new Error("unreachable");
        entry.times -= 1;
        if (entry.times <= 0) injected.splice(index, 1);
        const reply = entry.reply;
        if ("reject" in reply) throw new TypeError(reply.reject);
        if ("lose" in reply) {
          await route(call);
          throw new TypeError(reply.lose);
        }
        if ("unreadable" in reply) {
          return {
            ok: reply.unreadable < 400,
            status: reply.unreadable,
            json: async () => {
              throw new SyntaxError("Unexpected token <");
            },
          };
        }
        return { ok: reply.ok ?? reply.status < 400, status: reply.status, json: async () => reply.body };
      }
      return route(call);
    },
    inject(match, reply, times = 1) {
      injected.push({ match, reply, times });
    },
    append,
    awaitReview(digest, changes = [{ path: "a.go", op: "modify", diff: "-a\n+b\n" }]) {
      const turn = state.turns[state.turns.length - 1];
      if (turn === undefined) throw new Error("no turn to review");
      turn.state = "awaiting_review";
      turn.candidate_digest = digest;
      state.candidate = { digest, changes };
      state.revision += 1;
      append([{ kind: "turn_awaiting_review", turn_id: turn.id, candidate_digest: digest, text: `${changes.length} changed paths`, paths: changes.map((change) => change.path) }]);
    },
    view: () => view(),
    snapshot,
  };
  return gateway;
}
