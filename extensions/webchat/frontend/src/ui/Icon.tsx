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

// The Pi mark: the provider of the session, drawn as the letter pi.
export function PiIcon() {
  return (
    <Svg>
      <path d="M3 4.5h10M6 4.5v8M10 4.5v5.5a2 2 0 0 0 2 2" />
    </Svg>
  );
}

// The five governance panels. Each mark is a plain shape; the panel's name is always beside or on the control that holds it.
export function DecisionsIcon() {
  return (
    <Svg>
      <path d="M8 1.75l5 2v4c0 3-2.2 5.2-5 6.5-2.8-1.3-5-3.5-5-6.5v-4z" />
    </Svg>
  );
}

export function ProvenanceIcon() {
  return (
    <Svg>
      <circle cx="4" cy="4" r="1.5" />
      <circle cx="12" cy="8" r="1.5" />
      <circle cx="4" cy="12" r="1.5" />
      <path d="M5.4 4.7l5.2 2.6M5.4 11.3l5.2-2.6" />
    </Svg>
  );
}

export function PublicationIcon() {
  return (
    <Svg>
      <path d="M8 10V2.5M5 5.25l3-3 3 3" />
      <path d="M2.5 10v2.5a1 1 0 0 0 1 1h9a1 1 0 0 0 1-1V10" />
    </Svg>
  );
}

export function RecoveryIcon() {
  return (
    <Svg>
      <path d="M3 8a5 5 0 1 0 1.6-3.7" />
      <path d="M3 2.5v3h3" />
    </Svg>
  );
}

export function QualificationIcon() {
  return (
    <Svg>
      <circle cx="8" cy="8" r="6" />
      <path d="M5.5 8.2l1.8 1.8 3.2-3.5" />
    </Svg>
  );
}