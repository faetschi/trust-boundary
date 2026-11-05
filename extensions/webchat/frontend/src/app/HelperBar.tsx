// The details card: a small floating card at the header's right (the Details control opens it), for what the page is looking at
// and how much of it there is. It holds what the header, the chat head, the rail and the footer used to draw all at once: the
// session and its source, the observed posture, the observation streams, the retention statistics and the read-only notes.
// Each is a disclosure whose summary line already says its worst fact, so closing a section never hides a "gap", an
// "unavailable" or a deny; the sections open to the full reading. It is presentation only and holds no control that acts.

import type { ReactNode } from "react";
import { Chip } from "../ui/Chip";
import { cx } from "../ui/cx";
import { Disclosure } from "../ui/Disclosure";
import { StateDot } from "../ui/StateDot";
import { governanceCounts } from "../core/filters";
import { useLive } from "../live/context";
import { COMPOSER_REASON } from "./Composer";
import { statsParts } from "./Footer";
import { confirmedIn } from "./verification";
import { NO_PROJECTION, unavailableWords } from "./panels";
import { HEALTH_TONES, LIVE_FOOTNOTE, OTHER_SESSIONS_NOTE, READ_ONLY_NOTE, bindingName, streamText } from "./Rail";
import { conversationHead } from "./views/chat/identity";
import { postureEntries } from "./views/chat/readings";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./HelperBar.module.css";

export const HELPER_ID = "helper-bar";

interface SectionProps {
  readonly title: string;
  // The section's worst fact, in words: what a reader sees on the closed line.
  readonly lead: string;
  readonly children: ReactNode;
}

function Section({ title, lead, children }: SectionProps) {
  return (
    <Disclosure
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
}

export function HelperBar({ model, open, version, className }: HelperBarProps) {
  const live = useLive() !== null;
  const head = conversationHead(model.session, model.loaded);
  const confirmedOf = (item: Parameters<typeof confirmedIn>[1]) => confirmedIn(model, item);
  const posture = postureEntries(governanceCounts(model.items, confirmedOf));
  const stats = statsParts(model);
  const streamsDown = model.streams.filter((stream) => !stream.available || stream.health === "gap").length;
  const statsLead = stats[1] + (stats.some((part) => part.includes("unavailable") && part !== stats[1]) ? " · some unavailable" : "");

  return (
    <section id={HELPER_ID} aria-label="Session details" className={cx(styles.card, className)} hidden={!open}>
      <Section title="Session" lead={head.status}>
        <div className={styles.rows}>
          <p className={styles.row}>{head.status}</p>
          <p className={styles.row}>
            {bindingName(model.source)} <Chip tone="idle">{model.source}</Chip>
          </p>
          <p className={styles.mono} data-identity="">
            Pi <span>{head.piVersion}</span> · {head.sessionId} · {head.workflowId}
          </p>
        </div>
      </Section>
      <Section
        title="Observed posture"
        lead={!model.loaded ? unavailableWords(NO_PROJECTION) : posture.length > 0 ? posture.slice(0, 2).map((entry) => entry.text).join(" · ") : "none projected"}
      >
        <div className={styles.posture} role="group" aria-label="Observed posture">
          {!model.loaded ? (
            <span className={styles.row}>{unavailableWords(NO_PROJECTION)}</span>
          ) : posture.length > 0 ? (
            posture.map((entry) => (
              <span key={entry.key} title={entry.hint}>
                <Chip tone={entry.tone}>{entry.text}</Chip>
              </span>
            ))
          ) : (
            <span className={styles.row}>no decisions or results projected</span>
          )}
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
      <Section title="Retention" lead={statsLead}>
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
    </section>
  );
}
