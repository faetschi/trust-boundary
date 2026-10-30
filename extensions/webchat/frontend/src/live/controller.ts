// controller.ts — the live session state behind React: the session list, the active session's view, the candidate under
// review, and the one mutation in flight. DOM-free. Like the workspace store, it publishes an immutable model after every
// change, and every browser facility comes in through its deps, so the whole lifecycle runs under test with fakes.
//
// The transcript is not here. The workspace store keeps it, and this controller supplies that store with the session routes
// (workspaceDeps): the snapshot is GET /api/v1/sessions/{id} and the event source is the long poll. The snapshot's `live`
// view is the only place a revision comes from, and it is read again after every lifecycle event, so `expected_revision`
// is the revision the page last displayed.
//
// A mutation is an Intent (client.ts) with its request key. It is built once per user action. A transport failure is retried
// a few times with the same intent, and when that fails the page says so, with the same request key behind a Retry control.
// A prompt that was not sent is never rebuilt with a new key behind the user's back.

import { CONTRACT_VERSION, type LiveSessionView, type Snapshot } from "../core/contract";
import type { StreamEventHandlers } from "../core/stream";
import type { WorkspaceDeps } from "../app/workspaceStore";
import { LiveError, type CreateParams, type Decision, type Intent, type LiveClient } from "./client";
import { cursorCount, openLongPoll } from "./poller";
import { cancelGate, failureText, openTurnOf, retryable, reviewableTurn, sendGate } from "./readings";
import type { Candidate, LiveEvent } from "./wire";
import { utf8Length } from "../core/validate";
import { MAX_PENDING_ACTIONS, MAX_PENDING_BYTES, PENDING_ACTIONS_KEY, readStoredIntents } from "./storedIntent";

export interface LiveStorage {
  get(key: string): string | null;
  set(key: string, value: string): void;
}

export const ACTIVE_SESSION_KEY = "tbound.live.session";
export const cursorKey = (sessionId: string) => `tbound.live.cursor.${sessionId}`;

// Automatic repeats of one intent after a transport failure, and the wait before each. Only the same key is ever repeated.
export const AUTO_RETRY_DELAYS_MS: readonly number[] = [400, 1200];

export type ActionStatus = "idle" | "sending" | "failed" | "unknown";

export interface ActionState {
  readonly status: ActionStatus;
  // The intent in flight or failed. Retrying sends this same object, so the key and the body cannot change.
  readonly intent: Intent | null;
  // Tries made so far, the first included.
  readonly attempt: number;
  readonly error: LiveError | null;
  // The last success, for a notice: the service said it had already recorded this request.
  readonly done: { readonly kind: Intent["kind"]; readonly replayed: boolean } | null;
}

export type CandidateState =
  | { readonly status: "none" }
  | { readonly status: "loading"; readonly turnId: string; readonly digest: string }
  | { readonly status: "failed"; readonly turnId: string; readonly digest: string; readonly message: string }
  // The candidate as it is displayed. Publish and Discard are bound to its digest.
  | { readonly status: "ready"; readonly candidate: Candidate };

export interface LiveModel {
  // The session list, or null until it has been read.
  readonly sessions: readonly LiveSessionView[] | null;
  readonly activeId: string | null;
  // The active session's view as last read, with its revision.
  readonly view: LiveSessionView | null;
  // Events that arrived while this page was closed, when the browser kept a cursor for the session.
  readonly sinceLeft: number;
  readonly candidate: CandidateState;
  readonly action: ActionState;
  // A read that failed (the list, the view), in words. Null when the last one succeeded.
  readonly fault: string | null;
  // The gateway no longer knows this browser: the pairing ended or never happened.
  readonly authLost: boolean;
}

export interface LiveController {
  subscribe(listener: () => void): () => void;
  getSnapshot(): LiveModel;
  // Reads the session list and restores the session the page had open.
  start(): Promise<void>;
  select(sessionId: string | null): void;
  createSession(params: CreateParams): Promise<boolean>;
  // Sends a prompt under a fresh request key. True when the service accepted it.
  submit(prompt: string): Promise<boolean>;
  cancel(): Promise<boolean>;
  // Publish or discard exactly the candidate that is displayed: the digest sent is the candidate's own.
  resolve(candidate: Candidate, decision: Decision): Promise<boolean>;
  // Sends the failed intent again, under its own request key.
  retry(): Promise<boolean>;
  dismiss(): void;
  loadCandidate(): Promise<void>;
  refreshView(): Promise<void>;
  // The deps of a workspace store for one session: its snapshot and its event stream come from the session routes.
  workspaceDeps(sessionId: string): WorkspaceDeps;
}

export interface LiveControllerDeps {
  readonly client: LiveClient;
  // The browser facilities of the workspace store: timers, abort controllers, the clock and the online flag.
  readonly base: WorkspaceDeps;
  readonly storage: LiveStorage;
  readonly sleep: (ms: number) => Promise<void>;
}

const IDLE: ActionState = { status: "idle", intent: null, attempt: 0, error: null, done: null };

// The kinds that change the session view (its revision, its turns, its budget). Text, tool calls and results do not.
const LIFECYCLE: ReadonlySet<string> = new Set([
  "turn_admitted",
  "turn_linked",
  "turn_cancel_requested",
  "turn_awaiting_review",
  "turn_resolution_requested",
  "turn_terminal",
]);

function asLiveError(error: unknown): LiveError {
  if (error instanceof LiveError) return error;
  const detail = error instanceof Error ? error.message : String(error);
  return new LiveError({ kind: "protocol", status: 0, code: "BAD_RESPONSE", detail, outcomeUnknown: false });
}

function describeFault(error: unknown): string {
  const failure = asLiveError(error);
  if (failure.code === "CANDIDATE_TOO_LARGE") return failureText(failure);
  return failure.detail ? `${failure.code}: ${failure.detail}` : failure.code;
}

export function createLiveController(deps: LiveControllerDeps): LiveController {
  const { client, storage } = deps;
  const pending = new Map<string | null, ActionState>();
  for (const { sessionId, intent } of readStoredIntents(storage.get(PENDING_ACTIONS_KEY))) {
    pending.set(sessionId, { ...IDLE, status: "unknown", intent, error: new LiveError({
      kind: "transport", status: 0, code: "TRANSPORT", detail: "An unresolved request was restored from this tab.", outcomeUnknown: true,
    }) });
  }
  let model: LiveModel = {
    sessions: null,
    activeId: null,
    view: null,
    sinceLeft: 0,
    candidate: { status: "none" },
    action: pending.get(null) ?? IDLE,
    fault: null,
    authLost: false,
  };
  const listeners = new Set<() => void>();
  // The session whose first snapshot has been compared with the stored cursor. A recovery snapshot does not compare again.
  let comparedFor: string | null = null;
  let refreshing: Promise<void> | null = null;
  let refreshAgain = false;
  // The open event stream's handlers, so a view read can also refresh what the workspace shows of the session (its header and
  // panels), which otherwise change only with a snapshot.
  let feed: { readonly sessionId: string; readonly handlers: StreamEventHandlers } | null = null;

  function set(patch: Partial<LiveModel>): void {
    model = { ...model, ...patch };
    for (const listener of listeners) listener();
  }

  function setAction(action: ActionState, sessionId: string | null): boolean {
    if (action.status === "sending" || action.status === "unknown") pending.set(sessionId, action);
    else pending.delete(sessionId);
    let saved = false;
    try {
      const text = JSON.stringify(Array.from(pending, ([sessionId, action]) => ({ sessionId, intent: action.intent })));
      if (pending.size <= MAX_PENDING_ACTIONS && utf8Length(text) <= MAX_PENDING_BYTES && readStoredIntents(text).length === pending.size) {
        storage.set(PENDING_ACTIONS_KEY, text);
        saved = storage.get(PENDING_ACTIONS_KEY) === text;
      }
    } catch {
      // A failed write must prevent a new request from leaving without its recovery record.
    }
    if (sessionId === null || sessionId === model.activeId) set({ action: pending.get(null) ?? pending.get(model.activeId) ?? action });
    return saved;
  }

  function noteFailure(error: unknown): void {
    const failure = asLiveError(error);
    set({ fault: describeFault(failure), ...(failure.status === 401 ? { authLost: true } : {}) });
  }

  // A view is adopted only if it is not older than the one displayed: revisions only grow.
  function adoptView(view: LiveSessionView): void {
    if (view.id !== model.activeId) return;
    if (model.view !== null && view.revision < model.view.revision) return;
    set({ view, fault: null });
    syncCandidate(view);
  }

  // The candidate shown is the one of the turn awaiting review, at the digest the view names. Anything else is read again.
  function syncCandidate(view: LiveSessionView): void {
    const turn = reviewableTurn(view);
    const current = model.candidate;
    if (turn === null) {
      if (current.status !== "none") set({ candidate: { status: "none" } });
      return;
    }
    const digest = turn.candidate_digest ?? "";
    if (current.status === "ready" && current.candidate.turnId === turn.id && current.candidate.candidateDigest === digest) return;
    if ((current.status === "loading" || current.status === "failed") && current.turnId === turn.id && current.digest === digest) return;
    void fetchCandidate(view.id, turn.id, digest);
  }

  async function fetchCandidate(sessionId: string, turnId: string, digest: string): Promise<void> {
    set({ candidate: { status: "loading", turnId, digest } });
    try {
      const candidate = await client.candidate(sessionId, turnId);
      const current = model.candidate;
      if (model.activeId !== sessionId || current.status !== "loading" || current.turnId !== turnId) return;
      set({ candidate: { status: "ready", candidate } });
    } catch (error) {
      const current = model.candidate;
      if (model.activeId !== sessionId || current.status !== "loading" || current.turnId !== turnId) return;
      set({ candidate: { status: "failed", turnId, digest, message: describeFault(error) } });
      if (asLiveError(error).status === 401) set({ authLost: true });
    }
  }

  async function refreshSessions(): Promise<void> {
    try {
      set({ sessions: await client.listSessions() });
    } catch (error) {
      noteFailure(error);
    }
  }

  // Reads the active session again through the snapshot route, which is the only route that carries the turns. Calls that
  // arrive while one is in flight collapse into one more read after it.
  function refreshView(): Promise<void> {
    if (refreshing !== null) {
      refreshAgain = true;
      return refreshing;
    }
    refreshing = (async () => {
      do {
        refreshAgain = false;
        const id = model.activeId;
        if (id === null) return;
        try {
          const snapshot = await client.snapshot(id);
          if (snapshot.live !== undefined) adoptView(snapshot.live);
          if (feed?.sessionId === id && model.activeId === id) {
            feed.handlers.onSession({
              schema_version: CONTRACT_VERSION,
              cursor: snapshot.cursor,
              kind: "session",
              session: snapshot.session,
              panels: snapshot.panels,
              streams: snapshot.streams,
            });
          }
        } catch (error) {
          noteFailure(error);
        }
      } while (refreshAgain);
    })().finally(() => {
      refreshing = null;
    });
    return refreshing;
  }

  function readCursor(sessionId: string): number | null {
    const text = storage.get(cursorKey(sessionId));
    return text !== null && /^[0-9]+$/.test(text) ? Number(text) : null;
  }

  // The snapshot the workspace store loads is also the freshest view. The first one for a session is compared with the
  // cursor this browser stored, which says how many events arrived while the page was closed.
  function adoptSnapshot(snapshot: Snapshot): void {
    const live = snapshot.live;
    if (live === undefined || live.id !== model.activeId) return;
    const now = cursorCount(snapshot.cursor);
    if (comparedFor !== live.id) {
      comparedFor = live.id;
      const stored = readCursor(live.id);
      set({ sinceLeft: stored !== null && now !== null && now > stored ? now - stored : 0 });
    }
    if (now !== null) storage.set(cursorKey(live.id), String(now));
    adoptView(live);
  }

  function onBatch(sessionId: string, events: readonly LiveEvent[], nextCursor: number): void {
    if (sessionId !== model.activeId) return;
    storage.set(cursorKey(sessionId), String(nextCursor));
    if (events.some((event) => LIFECYCLE.has(event.kind))) {
      void refreshView();
      void refreshSessions();
    }
  }

  function select(sessionId: string | null): void {
    if (sessionId === model.activeId) return;
    storage.set(ACTIVE_SESSION_KEY, sessionId ?? "");
    comparedFor = null;
    set({ activeId: sessionId, view: null, sinceLeft: 0, candidate: { status: "none" }, action: pending.get(null) ?? pending.get(sessionId) ?? IDLE, fault: null });
  }

  // Persist before sending, so even a reload during the exchange can only repeat the same request.
  async function run(intent: Intent): Promise<boolean> {
    const sessionId = intent.kind === "create" ? null : model.activeId;
    const previous = pending.get(sessionId);
    if (previous !== undefined && previous.intent !== intent) return false;
    let unknown = previous?.status === "unknown";
    if (!setAction({ status: "sending", intent, attempt: 1, error: null, done: null }, sessionId)) {
      if (previous !== undefined) pending.set(sessionId, previous);
      else pending.delete(sessionId);
      set({ action: previous ?? IDLE, fault: "Cannot save the request for recovery. No request was sent. Enable tab storage or settle pending requests first." });
      return false;
    }
    for (let attempt = 1; ; attempt++) {
      try {
        const result = await client.send(intent);
        setAction({ ...IDLE, done: { kind: intent.kind, replayed: result.replayed } }, sessionId);
        if (result.kind === "create") {
          await refreshSessions();
          select(result.session.id);
        } else {
          void refreshView();
        }
        return true;
      } catch (error) {
        const failure = asLiveError(error);
        unknown ||= failure.outcomeUnknown;
        const delay = AUTO_RETRY_DELAYS_MS[attempt - 1];
        if (retryable(failure) && delay !== undefined) {
          setAction({ status: "sending", intent, attempt: attempt + 1, error: failure, done: null }, sessionId);
          await deps.sleep(delay);
          continue;
        }
        setAction({ status: unknown ? "unknown" : "failed", intent, attempt, error: failure, done: null }, sessionId);
        if (failure.status === 401) set({ authLost: true });
        if (failure.code === "STALE_REVISION") void refreshView();
        return false;
      }
    }
  }

  function ready(): { id: string; view: LiveSessionView } | null {
    return model.activeId !== null && model.view !== null ? { id: model.activeId, view: model.view } : null;
  }

  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot: () => model,
    async start() {
      await refreshSessions();
      const saved = storage.get(ACTIVE_SESSION_KEY);
      if (saved && model.sessions?.some((session) => session.id === saved)) select(saved);
    },
    select,
    async createSession(params) {
      if (model.action.status === "sending" || model.action.status === "unknown") return false;
      return run(client.createIntent(params));
    },
    async submit(prompt) {
      const target = ready();
      if (target === null || prompt.trim() === "" || !sendGate(target.view, model.action).allowed) return false;
      return run(client.submitIntent(target.id, target.view.revision, prompt));
    },
    async cancel() {
      const target = ready();
      if (target === null || !cancelGate(target.view, model.action).allowed) return false;
      const open = openTurnOf(target.view);
      if (open === null) return false;
      return run(client.cancelIntent(target.id, open.turn.id, target.view.revision));
    },
    async resolve(candidate, decision) {
      const target = ready();
      if (target === null || model.action.status === "sending" || model.action.status === "unknown") return false;
      return run(client.resolveIntent(target.id, candidate.turnId, target.view.revision, candidate.candidateDigest, decision));
    },
    async retry() {
      const { intent, status } = model.action;
      if (intent === null || (status !== "failed" && status !== "unknown")) return false;
      return run(intent);
    },
    dismiss() {
      if (model.action.status === "failed") setAction(IDLE, model.action.intent?.kind === "create" ? null : model.activeId);
    },
    async loadCandidate() {
      const view = model.view;
      const turn = reviewableTurn(view);
      if (view !== null && turn !== null) await fetchCandidate(view.id, turn.id, turn.candidate_digest ?? "");
    },
    refreshView,
    workspaceDeps(sessionId) {
      return {
        ...deps.base,
        loadSnapshot: async (signal) => {
          try {
            const snapshot = await client.snapshot(sessionId, signal);
            adoptSnapshot(snapshot);
            return snapshot;
          } catch (error) {
            // The stream controller retries under its backoff. The page still says why nothing loads.
            if (asLiveError(error).status === 401) set({ authLost: true });
            throw error;
          }
        },
        openEventStream: (handlers) => {
          const mine = { sessionId, handlers };
          feed = mine;
          const stream = openLongPoll(
            {
              client,
              sessionId,
              nowMs: deps.base.nowMs,
              createAbortController: deps.base.createAbortController,
              onBatch: (events, nextCursor) => onBatch(sessionId, events, nextCursor),
            },
            handlers,
          );
          return {
            close() {
              if (feed === mine) feed = null;
              stream.close();
            },
          };
        },
      };
    },
  };
}
