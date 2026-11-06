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

// The Pi mark, from the Pi press kit (pi.dev/press-kit, the one-colour badge): drawn in the control's ink, so it follows the theme.
export function PiIcon() {
  return (
    <svg viewBox="0 0 560 560" width="14" height="14" fill="currentColor" aria-hidden="true" focusable="false">
      <path d="M420 280H280V140H0V0H420V280Z" />
      <path d="M560 560H420V280H560V560Z" />
      <path d="M140 560H0V140H140V280H280V420H140V560Z" />
    </svg>
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

// A dashed boundary around a point: where the claim boundary stands. It is not a check mark: that panel almost never reads as passed.
export function QualificationIcon() {
  return (
    <Svg>
      <rect x="2.5" y="2.5" width="11" height="11" rx="2" strokeDasharray="2.2 2" />
      <circle cx="8" cy="8" r="1.4" fill="currentColor" stroke="none" />
    </Svg>
  );
}

// A speech bubble: the turns of the session.
export function TurnsIcon() {
  return (
    <Svg>
      <path d="M3 3h10v7H7l-3 3v-3H3z" />
    </Svg>
  );
}

// A stack of lines: the streams the supervisor observes.
export function StreamsIcon() {
  return (
    <Svg>
      <path d="M2.5 4h11M2.5 8h11M2.5 12h7" />
    </Svg>
  );
}

// One mark per kind of tool the supervisor records (the set is closed: read, write, edit, bash), a fallback, and one for a group of them.
// They are plain shapes in the control's ink: a mark never says whether the call succeeded.
function ReadIcon() {
  return (
    <Svg>
      <path d="M3.5 2.5h6l3 3v8h-9z" />
      <path d="M9.5 2.5v3h3M5.5 8.5h5M5.5 11h5" />
    </Svg>
  );
}

function WriteIcon() {
  return (
    <Svg>
      <path d="M3.5 2.5h6l3 3v8h-9z" />
      <path d="M9.5 2.5v3h3M8 7.5v4M6 9.5h4" />
    </Svg>
  );
}

function EditIcon() {
  return (
    <Svg>
      <path d="M3 13l1-3.2 7-7 2.2 2.2-7 7z" />
      <path d="M9.8 4.2l2.2 2.2" />
    </Svg>
  );
}

function BashIcon() {
  return (
    <Svg>
      <rect x="1.75" y="2.75" width="12.5" height="10.5" rx="2" />
      <path d="M4.5 6.5l2 1.5-2 1.5M8 10h3" />
    </Svg>
  );
}

function ToolFallbackIcon() {
  return (
    <Svg>
      <circle cx="8" cy="8" r="5.5" />
      <path d="M6.5 6.4a1.6 1.6 0 1 1 2.2 1.5c-.5.3-.7.6-.7 1.2" />
      <circle cx="8" cy="11.3" r="0.4" fill="currentColor" stroke="none" />
    </Svg>
  );
}

// A group of tool actions: a wrench.
export function ToolsIcon() {
  return (
    <Svg>
      <path d="M10.5 2.5a3 3 0 0 0-2.7 4L2.8 11.5a1.4 1.4 0 0 0 2 2l5-5a3 3 0 0 0 4-2.7l-1.8 1.8-1.7-.4-.4-1.7z" />
    </Svg>
  );
}

// The mark of a tool by its name. Any other name reads as the fallback.
export function toolIcon(tool: string | null | undefined): ReactNode {
  switch (tool) {
    case "read":
      return <ReadIcon />;
    case "write":
      return <WriteIcon />;
    case "edit":
      return <EditIcon />;
    case "bash":
      return <BashIcon />;
    default:
      return <ToolFallbackIcon />;
  }
}