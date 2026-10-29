// client.ts — the transport to the live gateway. DOM-free: fetch and the random source are injected.
//
// What it guarantees, each tested in client.test.ts:
//   - Same-origin, credentialed, uncached requests, and no CORS. Every POST carries X-TBound-CSRF. The token is read once
//     from GET /api/v1/pair, so a reloaded page recovers it from its cookie.
//   - A request key belongs to an intent. An intent is built once per user action (a prompt, a cancel, a decision, a new
//     session) with a fresh key, and sending it again sends the same body and the same key. The client never builds a
//     second key for a retry, and the gateway never invents one.
//   - A failure says whether the outcome of a mutation is unknown: the connection failed, the answer could not be read,
//     the gateway marked it, or it answered 5xx other than a clean "service unreachable". Only a 4xx or a clean 503
//     proves that nothing ran.

import type { AbortSignalLike, FetchResponseLike } from "../core/api";
import type { LiveSessionView, LiveTurn, Snapshot } from "../core/contract";
import { ContractError, parseSnapshot } from "../core/validate";
import {
  parseCandidate,
  parseCreated,
  parseErrorBody,
  parseEventsAnswer,
  parsePairing,
  parseSessions,
  parseTurnAnswer,
  type Candidate,
  type ErrorBody,
  type EventsAnswer,
} from "./wire";

export const CSRF_HEADER = "X-TBound-CSRF";

export interface LiveFetchInit {
  readonly method: "GET" | "POST";
  readonly headers: Readonly<Record<string, string>>;
  readonly credentials: "same-origin";
  readonly cache: "no-store";
  readonly body?: string;
  readonly signal?: AbortSignalLike;
}

export type LiveFetch = (url: string, init: LiveFetchInit) => Promise<FetchResponseLike>;

export type FailureKind = "service" | "transport" | "protocol";

export class LiveError extends Error {
  readonly kind: FailureKind;
  // The HTTP status, or 0 when no answer arrived.
  readonly status: number;
  // The gateway's stable code, TRANSPORT when no answer arrived, or BAD_RESPONSE when the answer was not the contract's.
  readonly code: string;
  readonly detail: string;
  readonly outcomeUnknown: boolean;

  constructor(fields: { kind: FailureKind; status: number; code: string; detail: string; outcomeUnknown: boolean }) {
    super(fields.detail ? `${fields.code}: ${fields.detail}` : fields.code);
    this.name = "LiveError";
    this.kind = fields.kind;
    this.status = fields.status;
    this.code = fields.code;
    this.detail = fields.detail;
    this.outcomeUnknown = fields.outcomeUnknown;
  }
}

export interface CreateParams {
  readonly projectId: string;
  readonly providerProfile: string;
  readonly publicationMode: string;
  readonly hostCalls: number;
  readonly tokens: number;
  readonly durationSeconds: number;
  readonly readOnly: boolean;
}

export type IntentKind = "create" | "submit" | "cancel" | "resolve";

// One user action, with its request key. Retrying is sending the same intent again.
export interface Intent {
  readonly kind: IntentKind;
  readonly key: string;
  readonly path: string;
  readonly body: Readonly<Record<string, unknown>>;
}

export type IntentResult =
  | { readonly kind: "create"; readonly session: LiveSessionView; readonly replayed: boolean }
  | { readonly kind: "submit" | "cancel" | "resolve"; readonly turn: LiveTurn; readonly replayed: boolean };

export type Decision = "publish" | "discard";

export interface LiveClientDeps {
  readonly fetch: LiveFetch;
  readonly nowMs: () => number;
  // Fills the array with random bytes (crypto.getRandomValues in the browser).
  readonly randomBytes: (bytes: Uint8Array<ArrayBuffer>) => void;
}

export interface LiveClient {
  listSessions(): Promise<readonly LiveSessionView[]>;
  snapshot(sessionId: string, signal?: AbortSignalLike): Promise<Snapshot>;
  observe(sessionId: string, cursor: number, waitMs: number, signal?: AbortSignalLike): Promise<EventsAnswer>;
  candidate(sessionId: string, turnId: string): Promise<Candidate>;
  createIntent(params: CreateParams): Intent;
  submitIntent(sessionId: string, expectedRevision: number, prompt: string): Intent;
  cancelIntent(sessionId: string, turnId: string, expectedRevision: number): Intent;
  resolveIntent(sessionId: string, turnId: string, expectedRevision: number, candidateDigest: string, decision: Decision): Intent;
  send(intent: Intent): Promise<IntentResult>;
}

// The `turn-N` form of a turn ID. The gateway accepts it for the path's own session.
export function shortTurn(turnId: string): string {
  return turnId.slice(turnId.lastIndexOf("/") + 1);
}

function mutationOutcomeUnknown(status: number, body: ErrorBody | null): boolean {
  if (body?.outcomeUnknown) return true;
  if (status < 500) return false;
  return !(status === 503 && body?.code === "SERVICE_UNAVAILABLE");
}

export function createLiveClient(deps: LiveClientDeps): LiveClient {
  let csrf: Promise<string> | null = null;

  async function call(method: "GET" | "POST", path: string, body?: unknown, signal?: AbortSignalLike): Promise<unknown> {
    const mutation = method === "POST";
    const headers: Record<string, string> = { Accept: "application/json" };
    if (mutation) {
      headers["Content-Type"] = "application/json";
      headers[CSRF_HEADER] = await csrfToken();
    }
    let response: FetchResponseLike;
    try {
      response = await deps.fetch(path, {
        method,
        headers,
        credentials: "same-origin",
        cache: "no-store",
        ...(mutation ? { body: JSON.stringify(body) } : {}),
        signal,
      });
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      throw new LiveError({ kind: "transport", status: 0, code: "TRANSPORT", detail, outcomeUnknown: mutation });
    }
    let payload: unknown;
    let readable = true;
    try {
      payload = await response.json();
    } catch {
      readable = false;
    }
    if (response.ok) {
      if (!readable) {
        throw new LiveError({ kind: "protocol", status: response.status, code: "BAD_RESPONSE", detail: "the answer is not JSON", outcomeUnknown: mutation });
      }
      return payload;
    }
    const error = readable ? parseErrorBody(payload) : null;
    // A refused CSRF token may be stale: read it again on the next attempt.
    if (mutation && response.status === 403) csrf = null;
    throw new LiveError({
      kind: error ? "service" : "protocol",
      status: response.status,
      code: error?.code ?? "BAD_RESPONSE",
      detail: error?.detail ?? `HTTP ${response.status}`,
      outcomeUnknown: mutation && mutationOutcomeUnknown(response.status, error),
    });
  }

  function csrfToken(): Promise<string> {
    if (csrf === null) {
      const pending = parsed(call("GET", "/api/v1/pair"), parsePairing).then((pairing) => pairing.csrfToken);
      csrf = pending;
      // A failed read is not kept, so the next mutation tries again.
      pending.catch(() => {
        if (csrf === pending) csrf = null;
      });
    }
    return csrf;
  }

  // A malformed answer is a failure of the exchange, not a crash of the page. A mutation whose answer cannot be read was
  // answered 200: it ran, so its outcome is not known to this page.
  async function parsed<T>(read: Promise<unknown>, parse: (value: unknown) => T, mutation = false): Promise<T> {
    const value = await read;
    try {
      return parse(value);
    } catch (error) {
      if (error instanceof ContractError) {
        throw new LiveError({ kind: "protocol", status: 200, code: "BAD_RESPONSE", detail: error.message, outcomeUnknown: mutation });
      }
      throw error;
    }
  }

  function newKey(): string {
    const bytes = new Uint8Array(16);
    deps.randomBytes(bytes);
    return `ck-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
  }

  const sessionPath = (sessionId: string) => `/api/v1/sessions/${encodeURIComponent(sessionId)}`;
  const turnPath = (sessionId: string, turnId: string) => `${sessionPath(sessionId)}/turns/${encodeURIComponent(shortTurn(turnId))}`;

  return {
    listSessions: () => parsed(call("GET", "/api/v1/sessions"), parseSessions),
    snapshot: (sessionId, signal) =>
      parsed(call("GET", sessionPath(sessionId), undefined, signal), (value) => {
        const snapshot = parseSnapshot(value, { nowMs: deps.nowMs() });
        if (snapshot.source !== "live" || snapshot.live === undefined) throw new ContractError("snapshot: not a live session projection");
        return snapshot;
      }),
    observe: (sessionId, cursor, waitMs, signal) =>
      parsed(call("GET", `${sessionPath(sessionId)}/events?cursor=${cursor}&wait=${waitMs}`, undefined, signal), (value) =>
        parseEventsAnswer(value, sessionId, cursor),
      ),
    candidate: (sessionId, turnId) =>
      parsed(call("GET", `${turnPath(sessionId, turnId)}/candidate`), (value) => {
        const candidate = parseCandidate(value);
        if (shortTurn(candidate.turnId) !== shortTurn(turnId)) throw new ContractError("candidate: not the requested turn");
        return candidate;
      }),
    createIntent(params) {
      const key = newKey();
      return {
        kind: "create",
        key,
        path: "/api/v1/sessions",
        body: {
          project_id: params.projectId,
          provider_profile: params.providerProfile,
          publication_mode: params.publicationMode,
          budget: { host_calls: params.hostCalls, tokens: params.tokens, duration_seconds: params.durationSeconds },
          ...(params.readOnly ? { read_only: true } : {}),
          request_key: key,
        },
      };
    },
    submitIntent(sessionId, expectedRevision, prompt) {
      const key = newKey();
      return {
        kind: "submit",
        key,
        path: `${sessionPath(sessionId)}/turns`,
        body: { expected_revision: expectedRevision, request_key: key, prompt },
      };
    },
    cancelIntent(sessionId, turnId, expectedRevision) {
      const key = newKey();
      return {
        kind: "cancel",
        key,
        path: `${turnPath(sessionId, turnId)}/cancel`,
        body: { expected_revision: expectedRevision, request_key: key },
      };
    },
    resolveIntent(sessionId, turnId, expectedRevision, candidateDigest, decision) {
      const key = newKey();
      return {
        kind: "resolve",
        key,
        path: `${turnPath(sessionId, turnId)}/resolve`,
        body: { expected_revision: expectedRevision, request_key: key, candidate_digest: candidateDigest, decision },
      };
    },
    async send(intent) {
      const value = await call("POST", intent.path, intent.body);
      const { kind } = intent;
      if (kind === "create") {
        return parsed(Promise.resolve(value), (raw) => ({ kind, ...parseCreated(raw) }), true);
      }
      return parsed(Promise.resolve(value), (raw) => ({ kind, ...parseTurnAnswer(raw) }), true);
    },
  };
}
