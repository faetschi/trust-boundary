// The tail window of the transcript: a reader at the tail follows new records, and a reader who has scrolled away keeps
// their place. The arithmetic is core/anchor.ts; this hook reads the position and writes to it. The scroller is the
// transcript at wide widths, where the Chat view fills its column and the log scrolls inside it, and the page at phone
// widths, where the log is as tall as its content (tailScroller.ts). An anchor's offset is measured from the scroller's
// top edge, so it holds still while the records are added.
//
// The count of new records is the store's `tailPending`: the records the store applied since the reader's pin. The pin is
// kept by the store, so it survives a switch to the trajectory view and back.

import { useCallback, useEffect, useLayoutEffect, useRef, type RefObject } from "react";
import { isAtTail, maxScrollTop, restoredScrollTop, tailAnnouncement } from "../../../core/anchor";
import type { Announcer } from "../../../ui/announcer";
import type { WorkspaceActions } from "../../workspaceStore";
import { metricsOf, onTailScroll, originOf, scrollTailTo, tailScroller } from "../tailScroller";

// A burst of arrivals is announced once, after it settles.
export const TAIL_ANNOUNCE_DELAY_MS = 400;

interface Measure {
  readonly atTail: boolean;
  // The first anchored record that is at least partly visible, and its offset from the scroller's top edge.
  readonly anchorId: string | null;
  readonly offset: number | null;
  readonly scrollTop: number;
}

function measure(region: HTMLElement | null): Measure {
  const scroller = tailScroller(region);
  const metrics = metricsOf(scroller);
  let anchorId: string | null = null;
  let offset: number | null = null;
  if (region) {
    const origin = originOf(scroller);
    for (const node of region.querySelectorAll<HTMLElement>("[data-anchor]")) {
      const rect = node.getBoundingClientRect();
      if (rect.bottom > origin) {
        anchorId = node.dataset.anchor ?? null;
        offset = rect.top - origin;
        break;
      }
    }
  }
  return { atTail: isAtTail(metrics), anchorId, offset, scrollTop: metrics.scrollTop };
}

function anchorOffset(region: HTMLElement | null, id: string): number | null {
  if (!region) return null;
  const origin = originOf(tailScroller(region));
  for (const node of region.querySelectorAll<HTMLElement>("[data-anchor]")) {
    if (node.dataset.anchor === id) return node.getBoundingClientRect().top - origin;
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
    if (previous && element) {
      const scroller = tailScroller(element);
      const now = metricsOf(scroller);
      if (previous.atTail && shown.current) {
        scrollTailTo(scroller, maxScrollTop(now));
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
        if (target !== now.scrollTop) scrollTailTo(scroller, target);
      }
    }
    if (count > 0) shown.current = true;
    snapshot.current = measure(element);
    follow();
  });

  // A scroll by the reader updates the position, so the next commit holds the record they are reading.
  useEffect(
    () =>
      onTailScroll(region.current, () => {
        snapshot.current = measure(region.current);
        follow();
      }),
    [region, follow],
  );

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
    const scroller = tailScroller(region.current);
    scrollTailTo(scroller, maxScrollTop(metricsOf(scroller)));
    announced.current = 0;
    snapshot.current = measure(region.current);
    actions.repinTail(true);
    region.current?.focus({ preventScroll: true });
  }, [region, actions]);

  return { pending, jump };
}
