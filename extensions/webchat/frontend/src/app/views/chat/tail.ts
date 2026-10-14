// The tail window of the transcript: a reader at the tail follows new records, and a reader who has
// scrolled away keeps their place. The arithmetic is core/anchor.ts; this hook reads the page and
// writes to it. The page is the scroller (the shell has no inner scroll container), as in the legacy
// client (web-chat.md section 4.5).
//
// The count of new records is the store's `tailPending`: the records the store applied since the
// reader's pin. The pin is kept by the store, so it survives a switch to the trajectory view and back.

import { useCallback, useEffect, useLayoutEffect, useRef, type RefObject } from "react";
import {
  isAtTail,
  maxScrollTop,
  restoredScrollTop,
  tailAnnouncement,
  type ScrollMetrics,
} from "../../../core/anchor";
import type { Announcer } from "../../../ui/announcer";
import type { WorkspaceActions } from "../../workspaceStore";

// A burst of arrivals is announced once, after it settles.
export const TAIL_ANNOUNCE_DELAY_MS = 400;

interface Measure {
  readonly atTail: boolean;
  // The first anchored record that is at least partly visible, and its offset from the viewport top.
  readonly anchorId: string | null;
  readonly offset: number | null;
  readonly scrollTop: number;
}

function pageMetrics(): ScrollMetrics {
  const root = document.scrollingElement ?? document.documentElement;
  return { scrollTop: root.scrollTop, clientHeight: root.clientHeight, scrollHeight: root.scrollHeight };
}

function measure(region: HTMLElement | null): Measure {
  const metrics = pageMetrics();
  let anchorId: string | null = null;
  let offset: number | null = null;
  if (region) {
    for (const node of region.querySelectorAll<HTMLElement>("[data-anchor]")) {
      const rect = node.getBoundingClientRect();
      if (rect.bottom > 0) {
        anchorId = node.dataset.anchor ?? null;
        offset = rect.top;
        break;
      }
    }
  }
  return { atTail: isAtTail(metrics), anchorId, offset, scrollTop: metrics.scrollTop };
}

function anchorOffset(region: HTMLElement | null, id: string): number | null {
  if (!region) return null;
  for (const node of region.querySelectorAll<HTMLElement>("[data-anchor]")) {
    if (node.dataset.anchor === id) return node.getBoundingClientRect().top;
  }
  return null;
}

export interface TailWindow {
  // Records applied since the reader last was at the tail. Zero while they are at the tail.
  readonly pending: number;
  // Moves to the tail, pins the count to zero, and moves focus to the transcript.
  jump(): void;
}

export function useTailWindow(options: {
  readonly region: RefObject<HTMLElement | null>;
  readonly actions: Pick<WorkspaceActions, "repinTail">;
  // The store's count of records applied since the pin.
  readonly pending: number;
  // The records the view is showing. Used only to tell the first load from later arrivals.
  readonly count: number;
  readonly announcer: Announcer;
}): TailWindow {
  const { region, actions, pending, count, announcer } = options;
  const snapshot = useRef<Measure | null>(null);
  // Set once the view has shown records. The first load does not follow: an empty page is short, so the
  // reader is measured at the tail before the records arrive, and the records must not carry them down.
  const shown = useRef(false);
  const announced = useRef(pending);

  // Pins the count to the applied records while the reader is at the tail. A reader away keeps the pin.
  const follow = useCallback(() => actions.repinTail(snapshot.current?.atTail === true), [actions]);

  // After every commit: hold the reader's place, then measure the new position. A reader at the tail
  // moves to the new bottom. A reader away is moved so the first visible record keeps its offset.
  useLayoutEffect(() => {
    const previous = snapshot.current;
    const element = region.current;
    if (previous) {
      const now = pageMetrics();
      if (previous.atTail && shown.current) {
        window.scrollTo(0, maxScrollTop(now));
      } else if (previous.anchorId !== null) {
        const target = restoredScrollTop(
          {
            capturedScrollTop: previous.scrollTop,
            capturedOffset: previous.offset,
            currentScrollTop: now.scrollTop,
            nextOffset: anchorOffset(element, previous.anchorId),
          },
          now,
        );
        if (target !== now.scrollTop) window.scrollTo(0, target);
      }
    }
    if (count > 0) shown.current = true;
    snapshot.current = measure(element);
    follow();
  });

  // A scroll by the reader updates the position, so the next commit holds the record they are reading.
  useEffect(() => {
    const onScroll = () => {
      snapshot.current = measure(region.current);
      follow();
    };
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, [region, follow]);

  // One announcement per burst: the count is spoken once it settles, and not again until it grows. A view
  // that mounts over a count already shown (after a switch) does not speak it again.
  useEffect(() => {
    if (pending === 0) {
      announced.current = 0;
      return;
    }
    if (pending <= announced.current) return;
    const timer = setTimeout(() => {
      const text = tailAnnouncement(announced.current, pending);
      if (text === null) return;
      announced.current = pending;
      announcer.announce(text);
    }, TAIL_ANNOUNCE_DELAY_MS);
    return () => clearTimeout(timer);
  }, [pending, announcer]);

  const jump = useCallback(() => {
    window.scrollTo(0, maxScrollTop(pageMetrics()));
    announced.current = 0;
    snapshot.current = measure(region.current);
    actions.repinTail(true);
    region.current?.focus({ preventScroll: true });
  }, [region, actions]);

  return { pending, jump };
}
