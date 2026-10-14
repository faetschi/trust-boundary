import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import { isAtTail, maxScrollTop, pendingSince, tailAnnouncement, type ScrollMetrics } from "../../../core/anchor";
import { useAnnouncer } from "../../../ui/LiveRegion";

// A burst of arrivals is announced once it has settled, as the legacy client did.
export const TAIL_SETTLE_MS = 400;

export interface TailInput {
  // The records the store has applied in order. The count of new records comes only from this.
  readonly applied: number;
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
// are counted on the control. The pin moves only while the reader is at the tail.
export function useTailWindow({ applied, count, ledgerRef }: TailInput): TailWindow {
  const announcer = useAnnouncer();
  const [pending, setPending] = useState(0);
  const latest = useRef({ applied });
  // The tail as last measured. It is measured before the DOM grows, so it still describes the
  // reader's position when new records arrive.
  const atTailRef = useRef(true);
  // The applied count when the reader was last at the tail. Null until the view first measures.
  const pinned = useRef<number | null>(null);
  // Set once records have been shown. The first load opens at the top, so it does not follow.
  const seenRecords = useRef(false);
  const announced = useRef(0);

  const settle = useCallback((atTail: boolean, appliedNow: number) => {
    atTailRef.current = atTail;
    const pin = atTail || pinned.current === null ? appliedNow : pinned.current;
    pinned.current = pin;
    setPending(atTail ? 0 : pendingSince(appliedNow, pin));
  }, []);

  useLayoutEffect(() => {
    latest.current = { applied };
    if (seenRecords.current && atTailRef.current) scrollToTail();
    if (count > 0) seenRecords.current = true;
    settle(isAtTail(metrics()), applied);
  });

  useEffect(() => {
    function onScroll() {
      settle(isAtTail(metrics()), latest.current.applied);
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
    settle(true, latest.current.applied);
    ledgerRef.current?.focus({ preventScroll: true });
  }, [settle, ledgerRef]);

  return { pending, jump };
}
