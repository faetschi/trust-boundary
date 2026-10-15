import type { SessionHeader, StreamHealth, StreamStatus } from "../core/contract";
import { humanize } from "../core/format";
import type { ChipTone } from "../ui/Chip";
import { StateDot } from "../ui/StateDot";
import { VisuallyHidden } from "../ui/VisuallyHidden";
import { countValue, NO_PROJECTION, sessionReading, unavailableWords } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Rail.module.css";

// A stream that completed is not a verified outcome: the source did not claim completeness. So "complete" is
// neutral in its dot and its words, and never the success tone.
const HEALTH_TONES: Readonly<Record<StreamHealth, ChipTone>> = {
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
const OTHER_SESSIONS_NOTE =
  "Other Pi sessions are not listed. This page receives one session from the supervisor. A session list needs the supervisor session service, which is not available in this build.";

const UNTITLED = "Untitled session unavailable";
const NO_VERSION = "Pi version unavailable";
const NO_TURNS = "turns unavailable";

// A stream's state in words. The dot is decorative; this text carries the meaning.
function streamText(stream: StreamStatus): string {
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

export function Rail({ model, phone, expanded, onToggle, inert = false }: RailProps) {
  const { session } = model;
  const reading = sessionReading(session, false);
  const toggleName = expanded ? "Collapse sessions panel" : "Expand sessions panel";
  const sessionTitle = session ? session.title || UNTITLED : "no session projection";
  return (
    <nav aria-label="Sessions and streams" className={styles.rail} data-expanded={expanded} inert={inert}>
      {phone ? (
        <h2 id="rail-session" className={styles.heading}>
          <button type="button" className={styles.strip} aria-expanded={expanded} aria-controls="rail-body" onClick={onToggle}>
            <span>{`Sessions · ${sessionTitle}`}</span> <span aria-hidden="true">{expanded ? "▴" : "▾"}</span>
          </button>
        </h2>
      ) : (
        <div className={styles.railHead}>
          <h2 id="rail-session" className={styles.heading}>
            {expanded ? "Sessions" : <VisuallyHidden>Sessions</VisuallyHidden>}
          </h2>
          {!expanded && session ? (
            <span role="img" aria-label={`Session: ${session.status ? humanize(session.status) : "status unavailable"}${reading.tone === "reported" ? ", reported" : ""}`}>
              <StateDot tone={reading.tone} />
            </span>
          ) : null}
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
        </div>
      )}
      <div id="rail-body" className={styles.body} hidden={!expanded}>
        {expanded ? (
          <>
            {session ? (
              <ul aria-label="Sessions" className={styles.sessionList}>
                <SessionRow session={session} />
              </ul>
            ) : (
              <p className={styles.line}>No session projection</p>
            )}
            <p role="note" className={styles.otherNote}>
              {OTHER_SESSIONS_NOTE}
            </p>
            <section aria-labelledby="rail-streams" className={styles.section}>
              <h2 id="rail-streams" className={styles.heading}>
                Observation streams
              </h2>
              {!model.loaded ? (
                <p className={styles.line}>{unavailableWords(NO_PROJECTION)}</p>
              ) : model.streams.length > 0 ? (
                <ul className={styles.streams}>
                  {model.streams.map((stream) => (
                    <li key={stream.id} className={styles.stream}>
                      <StateDot tone={HEALTH_TONES[stream.health] ?? "unknown"} />
                      <span>{streamText(stream)}</span>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className={styles.line}>No streams projected.</p>
              )}
            </section>
            <p className={styles.note}>
              Read-only projection. No launch, cancel, approve, fork or execute control exists in this client; those are
              supervisor authority.
            </p>
          </>
        ) : null}
      </div>
    </nav>
  );
}
