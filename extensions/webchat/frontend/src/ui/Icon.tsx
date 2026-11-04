// A small set of inline SVG icons (no icon package). One 16 px grid, a 1.5 px stroke in currentColor, round caps. Each is decorative
// (aria-hidden): the control that holds it carries the accessible name.

import type { ReactNode } from "react";

function Svg({ children }: { readonly children: ReactNode }) {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
      {children}
    </svg>
  );
}

// A window with a small card at its top right: the details card.
export function DetailsIcon() {
  return (
    <Svg>
      <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="2" />
      <rect x="8.5" y="5" width="4" height="3" rx="0.75" fill="currentColor" stroke="none" />
    </Svg>
  );
}

// A window with a full-height right column: the governance panel.
export function PanelIcon() {
  return (
    <Svg>
      <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="2" />
      <path d="M10.25 2.75v10.5" />
    </Svg>
  );
}

export function SendIcon() {
  return (
    <Svg>
      <path d="M8 13V3M4 7l4-4 4 4" />
    </Svg>
  );
}

export function LockIcon() {
  return (
    <Svg>
      <rect x="3" y="7" width="10" height="6.5" rx="1.5" />
      <path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2" />
    </Svg>
  );
}

export function InfoIcon() {
  return (
    <Svg>
      <circle cx="8" cy="8" r="6.25" />
      <path d="M8 7.5V11" />
      <circle cx="8" cy="5" r="0.5" fill="currentColor" />
    </Svg>
  );
}
