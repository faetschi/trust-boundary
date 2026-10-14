import { useSyncExternalStore } from "react";

// At or below this width the inspector is an off-canvas drawer. The CSS in Inspector.module.css
// writes the same value as a literal, because a media query cannot read a custom property.
// Inspector.test.tsx checks that the two agree.
export const DRAWER_BREAKPOINT_PX = 1180;
export const DRAWER_QUERY = `(max-width: ${DRAWER_BREAKPOINT_PX}px)`;

function subscribeNarrow(onChange: () => void): () => void {
  const query = window.matchMedia(DRAWER_QUERY);
  query.addEventListener("change", onChange);
  return () => query.removeEventListener("change", onChange);
}

function isNarrow(): boolean {
  return window.matchMedia(DRAWER_QUERY).matches;
}

// Server rendering has no viewport. It renders the wide layout, and the client corrects it.
function serverNarrow(): boolean {
  return false;
}

// Whether the viewport is at or below the drawer breakpoint.
export function useNarrow(): boolean {
  return useSyncExternalStore(subscribeNarrow, isNarrow, serverNarrow);
}
