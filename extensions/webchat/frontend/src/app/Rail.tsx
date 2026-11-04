import type { SessionHeader, StreamHealth, StreamStatus } from "../core/contract";
import { humanize } from "../core/format";
import type { ChipTone } from "../ui/Chip";
import { StateDot } from "../ui/StateDot";
import { useLive } from "../live/context";
import { LiveSessions } from "../live/LiveSessions";
import { VisuallyHidden } from "../ui/VisuallyHidden";
import { countValue, sessionReading } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Rail.module.css";

// A stream that completed is not a verified outcome: the source did not claim completeness. So "complete" is
// neutral in its dot and its words, and never the success tone.
export const HEALTH_TONES: Readonly<Record<StreamHealth, ChipTone>> = {
  complete: "idle",
  observing: "pending",
  gap: "unknown",
  unavailable: "idle",
};

const HEALTH_WORDS: Readonly<Record<StreamHealth, string>> = {
  complete: "complete (source did not claim completeness)",
  observing: "observing",
  gap: "gap",
  unavailable: "unavailable",
};

// The one session this page receives. Other sessions need a list route that does not exist (the legacy client
// says the same, render.mjs). The note states it, and it offers no control for it.
export const OTHER_SESSIONS_NOTE =
  "Other Pi sessions are not listed. This page receives one session from the supervisor. A session list needs the supervisor session service, which is not available in this build.";

// The second read-only footnote. Both are in the details card (HelperBar.tsx), said once and in full.
export const READ_ONLY_NOTE =
  "Read-only projection. No launch, cancel, approve, fork or execute control exists in this client; those are supervisor authority.";

const UNTITLED = "Untitled session unavailable";
const NO_VERSION = "Pi version unavailable";
const NO_TURNS = "turns unavailable";

// A stream's state in words. The dot is decorative; this text carries the meaning.
export function streamText(stream: StreamStatus): string {
  const reason = stream.reason ? ` (${stream.reason})` : "";
  const availability = stream.available ? "" : ", not available";
  return `${stream.label}: ${HEALTH_WORDS[stream.health] ?? stream.health}${reason}${availability}`;
}

// The turn count in words: a stated count, zero included, or unavailable when the session header does not state one.
function turnsWords(value: unknown): string {
  const count = countValue(value);
  if (count === null) return NO_TURNS;
  return `${count} turn${count === 1 ? "" : "s"}`;
}

// The name of the source the page is bound to. The fixture is named as such; any other source is named by its class.
export function bindingName(source: string): string {
  return source === "fixture" ? "Fixture workspace" : `Workspace (${source})`;
}

// The session row. The header is never verified here, so a success status reads "reported, not verified".
function SessionRow({ session }: { readonly session: SessionHeader }) {
  const reading = sessionReading(session, false);
  const status = session.status ? humanize(session.status) : "status unavailable";
  const version = session.pi_version ? `Pi ${session.pi_version}` : NO_VERSION;
  return (
    <li aria-current="true" className={styles.sessionRow}>
      <p className={styles.sessionTitle}>{session.title || UNTITLED}</p>
      <p className={styles.line}>{`${version} · ${turnsWords(session.turn_count)}`}</p>
      <p className={styles.statusLine}>
        <StateDot tone={reading.tone} />
        <span>
          {status}
          {reading.tone === "reported" ? <span className={styles.trust}> · reported, not verified</span> : null}
        </span>
      </p>
    </li>
  );
}

interface RailProps {
  readonly model: WorkspaceModel;
  // At a phone width the rail is a one-row disclosure strip above the workspace.
  readonly phone: boolean;
  // The body is shown. At a wide width, false is the icon column.
  readonly expanded: boolean;
  readonly onToggle: () => void;
  // The governance drawer is open and modal: the rail behind it is inert.
  readonly inert?: boolean;
}

// The rail, in the v2 order: the source binding, the sessions, the observation streams, then the read-only footnotes.
// At a wide width it collapses to an icon column; at a phone width it is a strip that opens the same body.
// The desktop collapse toggle of the rail. The shell draws it in the header's brand row, beside the rail it collapses, so
// it stays anchored to the rail's top edge at every desktop width. At a phone width the strip in the rail is the toggle.
export function RailToggle({ expanded, onToggle }: { readonly expanded: boolean; readonly onToggle: () => void }) {
  const toggleName = expanded ? "Collapse sessions panel" : "Expand sessions panel";
  return (
    <button
      type="button"
      className={styles.toggle}
      aria-expanded={expanded}
      aria-controls="rail-body"
      aria-label={toggleName}
      title={toggleName}
      onClick={onToggle}
    >
      <span aria-hidden="true">{expanded ? "‹" : "›"}</span>
    </button>
  );
}

// In live mode the footnote says what the controls are. They are requests, and the product service decides.
export const LIVE_FOOTNOTE =
  "Live session. Cancel, Publish and Discard are requests to the product service, which decides. This page holds no policy, effect or publication authority.";

export function Rail({ model, phone, expanded, onToggle, inert = false }: RailProps) {
  const live = useLive() !== null;
  const { session } = model;
  const reading = sessionReading(session, false);
  const sessionTitle = session ? session.title || UNTITLED : "no session projection";
  return (
    <nav aria-label="Sessions and streams" className={styles.rail} data-expanded={expanded} inert={inert}>
      {phone ? (
        <h2 id="rail-session" className={styles.heading}>
          <button type="button" className={styles.strip} aria-expanded={expanded} aria-controls="rail-body" onClick={onToggle}>
            <span>{`Sessions · ${sessionTitle}`}</span> <span aria-hidden="true">{expanded ? "▴" : "▾"}</span>
          </button>
        </h2>
      ) : expanded ? null : (
        // The collapsed icon column: the heading for assistive technology and the status dot. The collapse toggle is in the
        // header's brand row (RailToggle, rendered by the shell), so it is never drawn over the rail's content.
        <div className={styles.railHead}>
          <h2 id="rail-session" className={styles.heading}>
            <VisuallyHidden>Sessions</VisuallyHidden>
          </h2>
          {session ? (
            <span role="img" aria-label={`Session: ${session.status ? humanize(session.status) : "status unavailable"}${reading.tone === "reported" ? ", reported" : ""}`}>
              <StateDot tone={reading.tone} />
            </span>
          ) : null}
        </div>
      )}
      <div id="rail-body" className={styles.body} hidden={!expanded}>
        {expanded ? (
          <>
            <section aria-labelledby="rail-sessions-title" className={styles.section}>
              <h2 id="rail-sessions-title" className={styles.heading}>
                Sessions
              </h2>
              {live ? (
                <LiveSessions />
              ) : session ? (
                <ul aria-label="Sessions" className={styles.sessionList}>
                  <SessionRow session={session} />
                </ul>
              ) : (
                <p className={styles.line}>No session projection</p>
              )}
            </section>
          </>
        ) : null}
      </div>
    </nav>
  );
}
