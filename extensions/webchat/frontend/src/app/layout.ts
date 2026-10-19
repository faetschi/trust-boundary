import { useCallback, useSyncExternalStore } from "react";

// At or below this width the inspector is an off-canvas drawer. The CSS in Inspector.module.css
// writes the same value as a literal, because a media query cannot read a custom property.
// Inspector.test.tsx checks that the two agree.
export const DRAWER_BREAKPOINT_PX = 1180;
export const DRAWER_QUERY = `(max-width: ${DRAWER_BREAKPOINT_PX}px)`;

// At or below this width the sessions rail is a one-row disclosure strip above the workspace. Rail.module.css
// writes the same value as a literal; Rail.test.tsx checks that the two agree.
export const PHONE_BREAKPOINT_PX = 760;
export const PHONE_QUERY = `(max-width: ${PHONE_BREAKPOINT_PX}px)`;

// Whether a media query matches. Server rendering has no viewport. It renders the wide layout, and the
// client corrects it.
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const list = window.matchMedia(query);
      list.addEventListener("change", onChange);
      return () => list.removeEventListener("change", onChange);
    },
    [query],
  );
  return useSyncExternalStore(
    subscribe,
    () => window.matchMedia(query).matches,
    () => false,
  );
}

// Whether the viewport is at or below the drawer breakpoint.
export function useNarrow(): boolean {
  return useMediaQuery(DRAWER_QUERY);
}

// Whether the viewport is a phone, at or below the phone breakpoint.
export function usePhone(): boolean {
  return useMediaQuery(PHONE_QUERY);
}
