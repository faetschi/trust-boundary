// Fake browser facilities for the workspace store: a fetch that answers from a script, an
// EventSource that the test drives, timers the test fires by hand, and a settable online
// flag. Test-only (see dom.tsx).
import type { EventSourceLike, FetchResponseLike } from "../core/api";
import type { Snapshot } from "../core/contract";
import type { WorkspaceDeps } from "../app/workspaceStore";

class FakeSource implements EventSourceLike {
  readonly url: string;
  closed = false;
  private readonly listeners = new Map<string, Array<(event: { readonly data?: string }) => void>>();

  constructor(url: string) {
    this.url = url;
  }

  addEventListener(type: string, listener: (event: { readonly data?: string }) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  close(): void {
    this.closed = true;
  }

  // Delivers an event. A payload is JSON-encoded, as an SSE data line is.
  emit(type: string, payload?: unknown): void {
    const event = { data: payload === undefined ? undefined : JSON.stringify(payload) };
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }

  open(): void {
    this.emit("open");
  }

  fail(): void {
    this.emit("error");
  }
}

interface ScheduledTimer {
  readonly callback: () => void;
  readonly delayMs: number;
  cancelled: boolean;
}

interface FakeFetchReply {
  readonly ok: boolean;
  readonly status: number;
  readonly body: unknown;
}

export interface FakeWorkspace {
  readonly deps: WorkspaceDeps;
  readonly sources: FakeSource[];
  readonly fetchUrls: string[];
  readonly timers: ScheduledTimer[];
  // The next fetch answers with this reply. Defaults to a 200 with the snapshot.
  reply: FakeFetchReply;
  setOnline(value: boolean): void;
  // Runs the earliest timer that is still armed. Returns its delay, or undefined.
  fireTimer(): number | undefined;
  // Returns the pending timers' delays, in arming order, without the cancelled ones.
  armedDelays(): number[];
}

export function fakeWorkspace(snapshot: Snapshot): FakeWorkspace {
  const sources: FakeSource[] = [];
  const fetchUrls: string[] = [];
  const timers: ScheduledTimer[] = [];
  let online = true;
  const workspace: FakeWorkspace = {
    deps: {
      fetch: async (url): Promise<FetchResponseLike> => {
        fetchUrls.push(url);
        const reply = workspace.reply;
        return { ok: reply.ok, status: reply.status, json: async () => reply.body };
      },
      openSource: (url) => {
        const source = new FakeSource(url);
        sources.push(source);
        return source;
      },
      isOnline: () => online,
      setTimer: (callback, delayMs) => {
        const timer: ScheduledTimer = { callback, delayMs, cancelled: false };
        timers.push(timer);
        return timer;
      },
      clearTimer: (handle) => {
        (handle as ScheduledTimer).cancelled = true;
      },
      createAbortController: () => {
        const signal = { aborted: false };
        return {
          signal,
          abort() {
            signal.aborted = true;
          },
        };
      },
      nowMs: () => Date.parse("2026-10-13T09:12:04Z"),
      random: () => 0.5,
    },
    sources,
    fetchUrls,
    timers,
    reply: { ok: true, status: 200, body: JSON.parse(JSON.stringify(snapshot)) },
    setOnline(value) {
      online = value;
    },
    fireTimer() {
      const next = timers.find((timer) => !timer.cancelled);
      if (!next) return undefined;
      next.cancelled = true;
      next.callback();
      return next.delayMs;
    },
    armedDelays() {
      return timers.filter((timer) => !timer.cancelled).map((timer) => timer.delayMs);
    },
  };
  return workspace;
}

// Lets the pending fetch promise and its continuation run.
export async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}
