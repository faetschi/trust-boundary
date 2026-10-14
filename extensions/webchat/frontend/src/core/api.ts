// api.ts — the transport to the gateway (port of api.mjs). GET-only; the client never sends a
// body and never targets a control route, because none exists.
//
// The browser's fetch and EventSource are not read here. The caller injects them, so the core
// stays free of globals and a test can drive the transport the way the browser would.
//
// Two layers, on purpose:
//   - fetchSnapshot and openEventStream are the legacy transport. They return and forward
//     the decoded JSON unchanged, as api.mjs does. Their tests depend on that.
//   - fetchValidatedSnapshot and openValidatedEventStream are the typed boundary. They parse
//     the JSON with the strict contract validator (validate.ts) before any handler runs. The
//     app wiring uses these; a malformed payload is an error, never a partial value.

import type { ItemEvent, ReplayGap, SessionEvent, Snapshot } from "./contract";
import { ContractError, parseEvent, parseJsonText, parseReplayGap, parseSnapshot } from "./validate";

export interface AbortSignalLike {
  readonly aborted: boolean;
}

export interface FetchResponseLike {
  readonly ok: boolean;
  readonly status: number;
  json(): Promise<unknown>;
}

export interface FetchInit {
  readonly method: "GET";
  readonly headers: { readonly Accept: string };
  readonly cache: "no-store";
  readonly signal?: AbortSignalLike;
}

export type FetchLike = (url: string, init: FetchInit) => Promise<FetchResponseLike>;

// The part of the browser's EventSource this module uses.
export interface EventSourceLike {
  addEventListener(type: string, listener: (event: { readonly data?: string }) => void): void;
  close(): void;
}

export type EventSourceFactory = (url: string) => EventSourceLike;

export interface EventStreamHandle {
  close(): void;
}

// The snapshot request. Returns the decoded JSON, unvalidated (legacy api.mjs behaviour).
export async function fetchSnapshot(transport: FetchLike, signal?: AbortSignalLike): Promise<unknown> {
  const response = await transport("/api/v1/snapshot", {
    method: "GET",
    headers: { Accept: "application/json" },
    cache: "no-store",
    signal,
  });
  if (!response.ok) {
    throw new Error(`snapshot request failed (${response.status})`);
  }
  return response.json();
}

// The snapshot request, parsed by the strict contract validator. This is the snapshot's
// JSON boundary: a payload that fails the contract rejects the promise.
export async function fetchValidatedSnapshot(
  transport: FetchLike,
  nowMs: () => number,
  signal?: AbortSignalLike,
): Promise<Snapshot> {
  const raw = await fetchSnapshot(transport, signal);
  return parseSnapshot(raw, { nowMs: nowMs() });
}

export interface RawEventHandlers {
  readonly cursor?: string;
  onItem(payload: unknown): void;
  onSession(payload: unknown): void;
  onGap(payload: unknown): void;
  onConnection?(value: "open" | "closed"): void;
  onError?(error: unknown): void;
}

// The event stream, with the decoded payloads passed through unvalidated (legacy api.mjs).
export function openEventStream(factory: EventSourceFactory, options: RawEventHandlers): EventStreamHandle {
  const { cursor, onItem, onSession, onGap, onConnection, onError } = options;
  const url = cursor ? `/api/v1/events?cursor=${encodeURIComponent(cursor)}` : "/api/v1/events";
  const source = factory(url);
  const decode = (handler: (payload: unknown) => void) => (event: { readonly data?: string }) => {
    try {
      handler(parseJsonText(String(event.data)));
    } catch (error) {
      if (onError) onError(error);
    }
  };
  source.addEventListener("item", decode(onItem));
  source.addEventListener("session", decode(onSession));
  source.addEventListener("gap", decode(onGap));
  source.addEventListener("open", () => {
    if (onConnection) onConnection("open");
  });
  source.addEventListener("error", () => {
    // Close rather than let the browser retry on its own fixed schedule. The stream
    // controller owns reconnect timing and recovers from a snapshot first.
    source.close();
    if (onConnection) onConnection("closed");
  });
  return {
    close() {
      source.close();
    },
  };
}

export interface ValidatedEventOptions {
  readonly cursor?: string;
  readonly nowMs: () => number;
  onItem(event: ItemEvent): void;
  onSession(event: SessionEvent): void;
  onGap(gap: ReplayGap): void;
  onConnection?(value: "open" | "closed"): void;
  onError?(error: unknown): void;
}

// The event stream with every payload parsed by the strict contract validator. A payload
// that fails is reported to onError, as a payload that does not decode is in the raw path.
export function openValidatedEventStream(factory: EventSourceFactory, options: ValidatedEventOptions): EventStreamHandle {
  return openEventStream(factory, {
    cursor: options.cursor,
    onItem: (payload) => {
      const event = parseEvent(payload, { nowMs: options.nowMs() });
      if (event.kind !== "item") throw new ContractError("item channel carried a non-item event");
      options.onItem(event);
    },
    onSession: (payload) => {
      const event = parseEvent(payload, { nowMs: options.nowMs() });
      if (event.kind !== "session") throw new ContractError("session channel carried a non-session event");
      options.onSession(event);
    },
    onGap: (payload) => options.onGap(parseReplayGap(payload)),
    onConnection: options.onConnection,
    onError: options.onError,
  });
}
