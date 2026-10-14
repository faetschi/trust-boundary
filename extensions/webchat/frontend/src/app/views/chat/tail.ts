// The tail window of the transcript: a reader at the tail follows new records, and a reader who has
// scrolled away keeps their place. The arithmetic is core/anchor.ts; this hook reads the page and
// writes to it. The page is the scroller (the shell has no inner scroll container), as in the legacy
// client (web-chat.md section 4.5).
//
// The count of new records is the number of records the store applied since the reader's pin. It
// never moves for gaps, duplicates or snapshot loads, because the store counts applied records only.

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import {
  isAtTail,
  maxScrollTop,
  pendingSince,
  restoredScrollTop,
  tailAnnouncement,
  type ScrollMetrics,
} from "../../../core/anchor";
import type { Announcer } from "../../../ui/announcer";
import type { WorkspaceActions } from "../../workspaceStore";

// The pin of each store's chat view. It is keyed by the store's actions object, which lives as long
// as the store. A return from the trajectory view keeps the count of records applied in between.
const pins = new WeakMap<WorkspaceActions, number>();

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
  // The store's actions object: the key of this store's pin.
  readonly pinKey: WorkspaceActions;
  readonly applied: number;
  readonly announcer: Announcer;
}): TailWindow {
  const { region, pinKey, applied, announcer } = options;
  const appliedRef = useRef(applied);
  const snapshot = useRef<Measure | null>(null);
  const announced = useRef(0);
  const [pending, setPending] = useState(() => pendingSince(applied, pins.get(pinKey) ?? applied));

  // Keeps the pin at the applied count while the reader is at the tail, and reports the count since it.
  const follow = useCallback(() => {
    const current = appliedRef.current;
    if (snapshot.current?.atTail) pins.set(pinKey, current);
    setPending(pendingSince(current, pins.get(pinKey) ?? current));
  }, [pinKey]);

  // After every commit: hold the reader's place, then measure the new position. A reader at the tail
  // moves to the new bottom. A reader away is moved so the first visible record keeps its offset.
  useLayoutEffect(() => {
    appliedRef.current = applied;
    // A view's pin starts when the reader first sees it (web-chat.md section 4.5). A return keeps it.
    if (!pins.has(pinKey)) pins.set(pinKey, applied);
    const previous = snapshot.current;
    const element = region.current;
    if (previous) {
      const now = pageMetrics();
      if (previous.atTail) {
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

  // One announcement per burst: the count is spoken once it settles, and not again until it grows.
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
    follow();
    region.current?.focus({ preventScroll: true });
  }, [region, follow]);

  return { pending, jump };
}
