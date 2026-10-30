// poller.ts — the live event stream: a long poll of GET /api/v1/sessions/{id}/events, presented to the stream controller
// (core/stream.ts) as the event source it already drives. The controller keeps its rules: every connection is
// snapshot-first, a gap or an invalid item closes this stream and recovers from a fresh snapshot, and retry timing is its
// own backoff. This module only turns observe windows into handler calls.
//
// Per window: the first poll does not wait (wait=0), so the stream reads "open" as soon as the gateway answers. Later
// polls wait up to the gateway's limit. Each `items[i]` is judged by the same contract validator as a streamed item, in the
// projection the stream opened after, so a live item is never judged by a weaker rule. `events[i]` is read only for
// lifecycle kinds, through onBatch, which tells the controller when the session view must be read again.

import type { AbortSignalLike, EventStreamHandle } from "../core/api";
import { CONTRACT_VERSION, GAP_VERSION, type ItemEvent } from "../core/contract";
import type { AbortControllerLike, StreamEventHandlers } from "../core/stream";
import { parseEvent } from "../core/validate";
import type { LiveClient } from "./client";
import type { LiveEvent } from "./wire";

// The gateway's longest wait (observe_wait_max_ms).
export const POLL_WAIT_MS = 25000;

export interface PollerDeps {
  readonly client: Pick<LiveClient, "observe">;
  readonly sessionId: string;
  readonly nowMs: () => number;
  readonly createAbortController: () => AbortControllerLike;
  // The events of a window that was applied, and the cursor after them. Called once per non-empty window.
  readonly onBatch: (events: readonly LiveEvent[], nextCursor: number) => void;
  readonly waitMs?: number;
}

// The number of events a snapshot cursor `ID:N` has consumed.
export function cursorCount(cursor: string): number | null {
  const digits = cursor.slice(cursor.lastIndexOf(":") + 1);
  return /^[0-9]+$/.test(digits) && Number.isSafeInteger(Number(digits)) ? Number(digits) : null;
}

export function openLongPoll(deps: PollerDeps, handlers: StreamEventHandlers): EventStreamHandle {
  const { client, sessionId } = deps;
  const abort = deps.createAbortController();
  let stopped = false;

  function stop(): void {
    stopped = true;
  }

  async function run(): Promise<void> {
    let cursor = cursorCount(handlers.cursor);
    if (cursor === null) {
      handlers.onError(new Error(`the snapshot cursor ${JSON.stringify(handlers.cursor)} is not a session cursor`));
      handlers.onConnection("closed");
      return;
    }
    let first = true;
    while (!stopped) {
      let answer;
      try {
        answer = await client.observe(sessionId, cursor, first ? 0 : (deps.waitMs ?? POLL_WAIT_MS), abort.signal as AbortSignalLike);
      } catch (error) {
        if (stopped) return;
        handlers.onError(error);
        handlers.onConnection("closed");
        return;
      }
      if (stopped) return;
      if (first) {
        first = false;
        handlers.onConnection("open");
      }
      if (answer.gap) {
        // The service no longer holds some earlier events. The window is not applied: the controller recovers from a snapshot.
        stop();
        handlers.onGap({ schema_version: GAP_VERSION, reason: "history_evicted", epoch: sessionId, latest: answer.nextCursor });
        return;
      }
      for (const [index, item] of answer.items.entries()) {
        let event: ItemEvent;
        try {
          const parsed = parseEvent(
            { schema_version: CONTRACT_VERSION, cursor: `${sessionId}:${cursor + index + 1}`, kind: "item", item },
            { nowMs: deps.nowMs(), projection: handlers.projection },
          );
          if (parsed.kind !== "item") throw new Error("not an item");
          event = parsed;
        } catch (error) {
          stop();
          handlers.onError(error);
          handlers.onInvalid(error);
          return;
        }
        handlers.onItem(event);
        if (stopped) return;
      }
      cursor = answer.nextCursor;
      if (answer.events.length > 0) deps.onBatch(answer.events, cursor);
    }
  }

  void run();
  return {
    close() {
      stop();
      abort.abort();
    },
  };
}
