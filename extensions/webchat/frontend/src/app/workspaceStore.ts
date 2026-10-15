// The workspace store: the core state behind React. The core mutates one Workspace in place,
// and React needs values it can compare. So the store publishes an immutable WorkspaceModel
// after every change. The model is replaced, never edited, so useSyncExternalStore sees a
// new value exactly when something changed, and a render reads one consistent state.
//
// The store wires the core stream to injected browser facilities (WorkspaceDeps). It holds
// no DOM reference and calls no global, so the whole lifecycle runs under test with fakes.

import {
  createStream,
  type AbortControllerLike,
  type StreamController,
} from "../core/stream";
import {
  fetchValidatedSnapshot,
  openValidatedEventStream,
  type EventSourceFactory,
  type FetchLike,
} from "../core/api";
import {
  clearRecord as clearRecordState,
  createWorkspace,
  selectRecord as selectRecordState,
  setFilter as setFilterState,
  setGroup as setGroupState,
  setQuery as setQueryState,
  setView as setViewState,
  type ConnectionState,
  type StoredGap,
  type ViewId,
  type WindowGap,
  type Workspace,
} from "../core/store";
import { pendingSince, repin } from "../core/anchor";
import type { FilterId } from "../core/filters";
import type { Item, Panels, SessionHeader, SourceClass, StreamStatus, VerificationScope } from "../core/contract";
import { createAnnouncer, type Announcer } from "../ui/announcer";
import { announcementsBetween, connectionStatus, type ConnectionStatus } from "./status";

export interface WorkspaceDeps {
  readonly fetch: FetchLike;
  readonly openSource: EventSourceFactory;
  readonly isOnline: () => boolean;
  readonly setTimer: (callback: () => void, delayMs: number) => unknown;
  readonly clearTimer: (handle: unknown) => void;
  readonly createAbortController: () => AbortControllerLike;
  readonly nowMs: () => number;
  readonly random?: () => number;
  readonly connectWarnMs?: number;
  readonly stableOpenMs?: number;
  readonly attemptTimeoutMs?: number;
}

export interface WorkspaceActions {
  setView(view: ViewId): void;
  setGroup(group: string): void;
  setQuery(query: string): void;
  setFilter(filter: FilterId): void;
  // Selecting the selected record clears the selection, as the core defines it.
  selectRecord(id: string | null): void;
  clearRecord(): void;
  // The tail pin (core/anchor.ts repin). A reader at the tail, or a store with no pin yet, pins the
  // applied count; a reader away keeps the pin, so records applied meanwhile are counted.
  repinTail(atTail: boolean): void;
}

// The immutable view of the workspace. Items and the id index are copies; the other
// fields are replaced, never mutated, by the core, so they are shared as they are.
export interface WorkspaceModel {
  readonly source: SourceClass;
  readonly verification: VerificationScope;
  readonly epoch: string;
  readonly cursor: string;
  readonly session: SessionHeader | null;
  readonly panels: Panels | null;
  readonly streams: readonly StreamStatus[];
  readonly warnings: readonly string[];
  readonly items: readonly Item[];
  readonly itemsById: ReadonlyMap<string, Item>;
  readonly lastSequence: number;
  readonly applied: number;
  // Records applied since the reader's tail pin: the count the Jump control shows. It is derived
  // from `applied` alone, so gaps, duplicates and snapshot loads never add to it.
  readonly tailPending: number;
  readonly evicted: number;
  readonly loaded: boolean;
  readonly error: string | null;
  readonly gapNotice: StoredGap | null;
  readonly windowGap: WindowGap | null;
  readonly connection: ConnectionState;
  readonly status: ConnectionStatus;
  readonly filter: FilterId;
  readonly view: ViewId;
  readonly group: string;
  readonly query: string;
  readonly selectedId: string | null;
  readonly actions: WorkspaceActions;
}

export interface WorkspaceStore {
  readonly announcer: Announcer;
  subscribe(listener: () => void): () => void;
  getSnapshot(): WorkspaceModel;
  // Opens the stream: snapshot first, then the event stream from the snapshot's cursor.
  start(): void;
  // Closes the stream and abandons any attempt. A stopped store can be started again.
  stop(): void;
  goOnline(): void;
  goOffline(): void;
}

export function createWorkspaceStore(deps: WorkspaceDeps): WorkspaceStore {
  const state: Workspace = createWorkspace();
  const announcer = createAnnouncer();
  const listeners = new Set<() => void>();
  let stream: StreamController | null = null;
  // Kept here, not in a view, because a view unmounts when the other one is shown.
  let tailPin: number | null = null;

  const actions: WorkspaceActions = {
    setView(view) {
      setViewState(state, view);
      publish();
    },
    setGroup(group) {
      setGroupState(state, group);
      publish();
    },
    setQuery(query) {
      setQueryState(state, query);
      publish();
    },
    setFilter(filter) {
      setFilterState(state, filter);
      publish();
    },
    selectRecord(id) {
      selectRecordState(state, id);
      publish();
    },
    clearRecord() {
      clearRecordState(state);
      publish();
    },
    repinTail(atTail) {
      const next = tailPin === null ? state.applied : repin(state.applied, atTail, tailPin);
      if (next === tailPin) return;
      tailPin = next;
      publish();
    },
  };

  function build(): WorkspaceModel {
    const status = connectionStatus(state.connection, state.connectionWarning, state.error);
    return {
      source: state.source,
      verification: state.verification,
      epoch: state.epoch,
      cursor: state.cursor,
      session: state.session,
      panels: state.panels,
      streams: state.streams,
      warnings: state.warnings,
      items: state.items.slice(),
      itemsById: new Map(state.itemsById),
      lastSequence: state.lastSequence,
      applied: state.applied,
      tailPending: tailPin === null ? 0 : pendingSince(state.applied, tailPin),
      evicted: state.evicted,
      loaded: state.loaded,
      error: state.error,
      gapNotice: state.gapNotice,
      windowGap: state.windowGap,
      connection: state.connection,
      status,
      filter: state.filter,
      view: state.view,
      group: state.group,
      query: state.query,
      selectedId: state.selectedId,
      actions,
    };
  }

  let model: WorkspaceModel = build();

  // The one place a change becomes visible. Messages for the announcer are derived from the
  // change itself, so each is spoken once.
  function publish(): void {
    const previous = model;
    model = build();
    for (const message of announcementsBetween(previous, model)) announcer.announce(message);
    for (const listener of listeners) listener();
  }

  // A stream is current until the store stops it. A stopped stream still runs its own teardown (the core
  // reports the stop as an offline change), and that teardown is not a change the reader can see: it must
  // not be announced or published. Under StrictMode the effect is torn down and started again on purpose.
  function isCurrent(candidate: StreamController): boolean {
    return stream === candidate;
  }

  function start(): void {
    stop();
    const controller = createStream({
      state,
      fetchSnapshot: (signal) => fetchValidatedSnapshot(deps.fetch, deps.nowMs, signal),
      // The core applies a snapshot without a change notice, and it opens the stream right after
      // the snapshot is applied. Publishing here is therefore the notice for the snapshot (and for
      // any gap noted with it), before the stream reports open.
      openEventStream: (handlers) => {
        if (isCurrent(controller)) publish();
        return openValidatedEventStream(deps.openSource, {
          cursor: handlers.cursor,
          nowMs: deps.nowMs,
          projection: handlers.projection,
          onItem: handlers.onItem,
          onSession: handlers.onSession,
          onGap: handlers.onGap,
          onInvalid: handlers.onInvalid,
          onConnection: handlers.onConnection,
          onError: handlers.onError,
        });
      },
      isOnline: deps.isOnline,
      setTimer: deps.setTimer,
      clearTimer: deps.clearTimer,
      createAbortController: deps.createAbortController,
      onChange: () => {
        if (isCurrent(controller)) publish();
      },
      random: deps.random,
      connectWarnMs: deps.connectWarnMs,
      stableOpenMs: deps.stableOpenMs,
      attemptTimeoutMs: deps.attemptTimeoutMs,
    });
    stream = controller;
    void controller.start();
  }

  function stop(): void {
    const current = stream;
    stream = null;
    current?.goOffline();
  }

  return {
    announcer,
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot() {
      return model;
    },
    start,
    stop,
    goOnline() {
      void stream?.goOnline();
    },
    goOffline() {
      stream?.goOffline();
    },
  };
}
