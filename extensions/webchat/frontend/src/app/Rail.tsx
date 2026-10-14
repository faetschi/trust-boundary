import { Chip, type ChipTone } from "../ui/Chip";
import { StateDot } from "../ui/StateDot";
import { sessionReading } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";
import { isVerifiedProjection } from "../core/format";
import type { StreamHealth, StreamStatus } from "../core/contract";
import styles from "./Shell.module.css";

const HEALTH_TONES: Readonly<Record<StreamHealth, ChipTone>> = {
  complete: "ok",
  observing: "pending",
  gap: "unknown",
  unavailable: "idle",
};

// A stream's state in words. The dot is decorative; this text carries the meaning.
function streamText(stream: StreamStatus): string {
  const reason = stream.reason ? ` (${stream.reason})` : "";
  const availability = stream.available ? "" : ", not available";
  return `${stream.label}: ${stream.health}${reason}${availability}`;
}

export function Rail({ model }: { readonly model: WorkspaceModel }) {
  const { session } = model;
  const reading = sessionReading(session, isVerifiedProjection(model));
  return (
    <nav aria-label="Sessions and streams" className={styles.rail}>
      <section aria-labelledby="rail-session" className={styles.railSection}>
        <h2 id="rail-session" className={styles.heading}>
          Session
        </h2>
        {session ? (
          <>
            <p className={styles.sessionTitle}>{session.title ?? session.session_id ?? "Untitled session"}</p>
            <p className={styles.line}>
              <Chip tone={reading.tone}>{reading.label}</Chip>
              {reading.trust ? <span className={styles.trust}> {reading.trust}</span> : null}
            </p>
            <p className={styles.line}>Turns: {session.turn_count}</p>
          </>
        ) : (
          <p className={styles.line}>No session projection</p>
        )}
        <p className={styles.note}>
          Read-only projection. No launch, cancel, approve, fork or execute control exists in this client; those are
          supervisor authority.
        </p>
      </section>
      <section aria-labelledby="rail-streams" className={styles.railSection}>
        <h2 id="rail-streams" className={styles.heading}>
          Observation streams
        </h2>
        {model.streams.length > 0 ? (
          <ul className={styles.streams}>
            {model.streams.map((stream) => (
              <li key={stream.id} className={styles.stream}>
                <StateDot tone={HEALTH_TONES[stream.health]} />
                <span>{streamText(stream)}</span>
              </li>
            ))}
          </ul>
        ) : (
          <p className={styles.line}>No streams projected.</p>
        )}
      </section>
    </nav>
  );
}
