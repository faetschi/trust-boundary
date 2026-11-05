// The details card: a small floating card at the header's right (the Details control opens it), for what the page is looking at
// and how much of it there is. It holds what the header, the chat head, the rail and the footer used to draw all at once: the
// session and its source, the observation streams, the retention statistics and the read-only notes.
// Each is a disclosure whose summary line already says its worst fact, so closing a section never hides a "gap", an
// "unavailable" or a deny; the sections open to the full reading. It is presentation only and holds no control that acts.

import type { ReactNode, Ref } from "react";
import { Chip } from "../ui/Chip";
import { cx } from "../ui/cx";
import { Disclosure } from "../ui/Disclosure";
import { StateDot } from "../ui/StateDot";
import { useLive } from "../live/context";
import { COMPOSER_REASON } from "./Composer";
import { retentionLead, statsParts } from "./stats";
import { NO_PROJECTION, unavailableWords } from "./panels";
import { HEALTH_TONES, LIVE_FOOTNOTE, OTHER_SESSIONS_NOTE, READ_ONLY_NOTE, bindingName, streamText } from "./sessionReadings";
import { conversationHead } from "./views/chat/identity";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./HelperBar.module.css";

export const HELPER_ID = "helper-bar";

interface SectionProps {
  readonly title: string;
  // The section's worst fact, in words: what a reader sees on the closed line.
  readonly lead: string;
  readonly defaultOpen?: boolean;
  readonly children: ReactNode;
}

function Section({ title, lead, defaultOpen = false, children }: SectionProps) {
  return (
    <Disclosure
      defaultOpen={defaultOpen}
      summary={
        <>
          <span className={styles.sectionTitle}>{title}</span>
          <span className={styles.lead}>{lead}</span>
        </>
      }
    >
      {children}
    </Disclosure>
  );
}

interface HelperBarProps {
  readonly model: WorkspaceModel;
  readonly open: boolean;
  readonly version: string;
  // Placement from the shell, so the card sits beside the governance column.
  readonly className?: string;
  // The governance drawer is open and modal: the card behind it is inert, as the header, the rail and the workspace are.
  readonly inert?: boolean;
  // The card, so the shell can move focus into it when it opens.
  readonly cardRef?: Ref<HTMLDivElement>;
}

export function HelperBar({ model, open, version, className, inert = false, cardRef }: HelperBarProps) {
  const live = useLive() !== null;
  const head = conversationHead(model.session, model.loaded);
  const stats = statsParts(model);
  const streamsDown = model.streams.filter((stream) => !stream.available || stream.health === "gap").length;

  return (
    <div
      id={HELPER_ID}
      ref={cardRef}
      role="group"
      aria-label="Session details"
      tabIndex={-1}
      className={cx(styles.card, className)}
      hidden={!open}
      inert={inert}
    >
      {/* The session opens first: the Pi version, the session id and the workflow id are the audit identifiers. */}
      <Section title="Session" lead={head.status} defaultOpen>
        <div className={styles.rows}>
          <p className={styles.row}>{head.status}</p>
          <p className={styles.row}>Pi tools: unavailable</p>
          <p className={styles.row}>
            {bindingName(model.source)} <Chip tone="idle">{model.source}</Chip>
          </p>
          <p className={styles.mono} data-identity="">
            Pi <span>{head.piVersion}</span> · {head.sessionId} · {head.workflowId}
          </p>
        </div>
      </Section>
      <Section
        title="Observation streams"
        lead={!model.loaded ? "unavailable" : streamsDown > 0 ? `${streamsDown} unavailable or gapped` : `${model.streams.length} projected`}
      >
        {!model.loaded ? (
          <p className={styles.row}>{unavailableWords(NO_PROJECTION)}</p>
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
          <p className={styles.row}>No streams projected.</p>
        )}
      </Section>
      <Section title="Retention" lead={retentionLead(model)}>
        <div role="group" aria-label="Session statistics" className={styles.stats}>
          {stats.map((part) => (
            <span key={part} className={part.includes("unavailable") ? styles.un : undefined}>
              {part}
            </span>
          ))}
          <span>build {version}</span>
        </div>
      </Section>
      <Section title="Read-only view" lead={live ? "live session" : "other sessions not listed"}>
        {live ? (
          <p className={styles.note}>{LIVE_FOOTNOTE}</p>
        ) : (
          <>
            <p role="note" className={styles.note}>
              {OTHER_SESSIONS_NOTE}
            </p>
            <p className={styles.note}>{READ_ONLY_NOTE}</p>
            <p className={styles.note}>{COMPOSER_REASON}</p>
          </>
        )}
      </Section>
    </div>
  );
}