import type { SessionHeader } from "../core/contract";
import { humanize } from "../core/format";
import { StateDot } from "../ui/StateDot";
import { useLive } from "../live/context";
import { LiveSessions } from "../live/LiveSessions";
import { VisuallyHidden } from "../ui/VisuallyHidden";
import { countValue, sessionReading } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Rail.module.css";

const UNTITLED = "Untitled session unavailable";
const NO_VERSION = "Pi version unavailable";
const NO_TURNS = "turns unavailable";

// The turn count in words: a stated count, zero included, or unavailable when the session header does not state one.
function turnsWords(value: unknown): string {
  const count = countValue(value);
  if (count === null) return NO_TURNS;
  return `${count} turn${count === 1 ? "" : "s"}`;
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
