import { useCallback, useEffect, useLayoutEffect, useRef, type RefObject } from "react";
import { isAtTail, maxScrollTop, tailAnnouncement, type ScrollMetrics } from "../../../core/anchor";
import { useAnnouncer } from "../../../ui/LiveRegion";
import type { WorkspaceActions } from "../../workspaceStore";

// A burst of arrivals is announced once it has settled, as the legacy client did.
export const TAIL_SETTLE_MS = 400;

export interface TailInput {
  // The store's pin: a reader at the tail moves it to the applied count, and a reader away keeps it.
  readonly actions: Pick<WorkspaceActions, "repinTail">;
  // The store's count of records applied since the pin. The count of new records comes only from this.
  readonly pending: number;
  // The records the view is showing. Used only to tell the first load from later arrivals.
  readonly count: number;
  readonly ledgerRef: RefObject<HTMLElement | null>;
}

export interface TailWindow {
  // Records applied since the reader last was at the tail. Zero at the tail.
  readonly pending: number;
  // Scrolls to the tail and moves focus to the ledger, which the control was for.
  jump(): void;
}

// The page is the scroller, so the metrics are the document's.
function scrollRoot(): HTMLElement {
  return (document.scrollingElement as HTMLElement | null) ?? document.documentElement;
}

function metrics(): ScrollMetrics {
  const root = scrollRoot();
  return { scrollTop: root.scrollTop, clientHeight: root.clientHeight, scrollHeight: root.scrollHeight };
}

function scrollToTail(): void {
  window.scrollTo(0, maxScrollTop(metrics()));
}

// The tail window of the ledger (core/anchor.ts is the arithmetic). A reader at the tail keeps
// following new records. A reader away from it keeps their place, and the records applied meanwhile
// are counted on the control. The pin lives in the store, so it survives a switch to the chat view.
export function useTailWindow({ actions, pending, count, ledgerRef }: TailInput): TailWindow {
  const announcer = useAnnouncer();
  // The tail as last measured. It is measured before the DOM grows, so it still describes the
  // reader's position when new records arrive.
  const atTailRef = useRef(true);
  // Set once records have been shown. The first load opens at the top, so it does not follow.
  const seenRecords = useRef(false);
  // A count already on screen when the view mounts (after a switch) is not announced again.
  const announced = useRef(pending);

  const settle = useCallback(
    (atTail: boolean) => {
      atTailRef.current = atTail;
      actions.repinTail(atTail);
    },
    [actions],
  );

  useLayoutEffect(() => {
    if (seenRecords.current && atTailRef.current) scrollToTail();
    if (count > 0) seenRecords.current = true;
    settle(isAtTail(metrics()));
  });

  useEffect(() => {
    function onScroll() {
      settle(isAtTail(metrics()));
    }
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, [settle]);

  useEffect(() => {
    if (pending === 0) {
      announced.current = 0;
      return;
    }
    if (pending <= announced.current) return;
    const timer = setTimeout(() => {
      const text = tailAnnouncement(announced.current, pending);
      if (!text) return;
      announced.current = pending;
      announcer.announce(text);
    }, TAIL_SETTLE_MS);
    return () => clearTimeout(timer);
  }, [pending, announcer]);

  const jump = useCallback(() => {
    scrollToTail();
    atTailRef.current = true;
    actions.repinTail(true);
    ledgerRef.current?.focus({ preventScroll: true });
  }, [actions, ledgerRef]);

  return { pending, jump };
}
