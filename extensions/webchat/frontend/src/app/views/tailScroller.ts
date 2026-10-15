// The scroller a view's tail window reads and writes. At wide widths the view fills its column and its own region
// scrolls inside it (Chat.module.css .log, Trajectory.module.css .ledger). At phone widths the region is as tall as its
// content and the page scrolls (Shell.module.css, at or below PHONE_BREAKPOINT_PX). A tail that measures the element the
// reader does not scroll reads "at the tail" for ever, so the count and the follow would be wrong.

import type { ScrollMetrics } from "../../core/anchor";
import { PHONE_QUERY } from "../layout";

export interface TailScroller {
  // The element whose position is read and written: the view's region, or the page's scrolling element.
  readonly element: HTMLElement;
  readonly page: boolean;
}

// Without a matchMedia (a host with no layout) the layout is treated as wide, so the region is the scroller.
export function tailScroller(region: HTMLElement | null): TailScroller {
  const phone = typeof window.matchMedia === "function" && window.matchMedia(PHONE_QUERY).matches;
  if (region && !phone) return { element: region, page: false };
  return { element: (document.scrollingElement as HTMLElement | null) ?? document.documentElement, page: true };
}

// The line the offsets of the view's anchors are measured from. The page's offsets are measured from the viewport's top
// edge. A region's top edge is stable while the page is still, so its offsets are measured from its own top edge.
export function originOf(scroller: TailScroller): number {
  return scroller.page ? 0 : scroller.element.getBoundingClientRect().top;
}

export function metricsOf(scroller: TailScroller): ScrollMetrics {
  const { element } = scroller;
  return { scrollTop: element.scrollTop, clientHeight: element.clientHeight, scrollHeight: element.scrollHeight };
}

export function scrollTailTo(scroller: TailScroller, top: number): void {
  if (scroller.page) window.scrollTo(0, top);
  else scroller.element.scrollTop = top;
}

// Calls back on a scroll of the page or of the region, whichever the layout makes the scroller. Both are listened to,
// so a change of layout (a resize across the phone breakpoint) needs no new listener.
export function onTailScroll(region: HTMLElement | null, onScroll: () => void): () => void {
  window.addEventListener("scroll", onScroll, { passive: true });
  region?.addEventListener("scroll", onScroll, { passive: true });
  return () => {
    window.removeEventListener("scroll", onScroll);
    region?.removeEventListener("scroll", onScroll);
  };
}
