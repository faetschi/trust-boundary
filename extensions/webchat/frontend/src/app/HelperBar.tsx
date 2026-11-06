// The details card: a small card at the header's right that the Details control pins open or shut. It answers, at a glance, the three
// things an operator of the Pi harness through tbound needs: which harness this is and what state its session is in, how much has
// happened, and what the supervisor has decided (decisions, publication, recovery). Each reading is a row: a mark, its name, its
// words and a state dot (a shape as well as a colour; the words are the worst badge of the panel, and its counts are in the panel). The governance rows open the Governance panel of that name. Everything else
// (identifiers, the source binding, the streams, retention, the read-only notes, the build) is under "Technical details", closed.
// It is presentation only and holds no control that acts.

import type { ReactNode, Ref } from "react";
import { Chip } from "../ui/Chip";
import { cx } from "../ui/cx";
import { Disclosure } from "../ui/Disclosure";
import { DecisionsIcon, PiIcon, PublicationIcon, RecoveryIcon, StreamsIcon, TurnsIcon } from "../ui/Icon";
import { StateDot } from "../ui/StateDot";
import { humanize } from "../core/format";
import { useLive } from "../live/context";
import { COMPOSER_REASON } from "./Composer";
import { chipToneOf, panelBadges, worstBadgeOf, worstToneOf } from "./inspector/panelBadge";
import { NO_PROJECTION, sessionReading, unavailableWords } from "./panels";
import { HEALTH_TONES, LIVE_FOOTNOTE, OTHER_SESSIONS_NOTE, READ_ONLY_NOTE, bindingName, streamText } from "./sessionReadings";
import { statsParts } from "./stats";
import type { PanelKey } from "./uiPrefs";
import { conversationHead } from "./views/chat/identity";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./HelperBar.module.css";

export const HELPER_ID = "helper-bar";

interface RowProps {
  readonly icon: ReactNode;
  readonly label: string;
  readonly words: string;
  readonly dot: ReactNode;
  // Set on a governance row: pressing it opens that panel.
  readonly onOpen?: () => void;
}

function Row({ icon, label, words, dot, onOpen }: RowProps) {
  const content = (
    <>
      <span className={styles.mark} aria-hidden="true">
        {icon}
      </span>
      <span className={styles.label}>{label}</span>
      <span className={styles.words}>{words}</span>
      {dot}
    </>
  );
  return onOpen ? (
    <button type="button" className={styles.row} aria-label={`Open ${label} panel, ${words}`} onClick={onOpen}>
      {content}
    </button>
  ) : (
    <div className={styles.row} role="group" aria-label={`${label}: ${words}`}>
      {content}
    </div>
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
  // The card, so a test or the shell can reach it.
  readonly cardRef?: Ref<HTMLDivElement>;
  // Opens the Governance panel of that name (the shell expands the column or opens the drawer).
  readonly onOpenPanel?: (panel: PanelKey) => void;
}

export function HelperBar({ model, open, version, className, inert = false, cardRef, onOpenPanel }: HelperBarProps) {
  const live = useLive() !== null;
  const head = conversationHead(model.session, model.loaded);
  const stats = statsParts(model);
  const streamsDown = model.streams.filter((stream) => !stream.available || stream.health === "gap").length;
  const status = model.session?.status ? humanize(model.session.status) : "status unavailable";
  // A session status the page cannot verify is drawn as the reported ring, never as a success.
  const sessionTone = model.session && sessionReading(model.session, false).tone === "reported" ? "reported" : "idle";
  const turnsWords = stats[0] === "turns unavailable" ? "unavailable" : (stats[0] ?? "");
  const governanceRow = (key: PanelKey, label: string, icon: ReactNode, words: string) => (
    <Row
      icon={icon}
      label={label}
      words={words}
      dot={<StateDot tone={chipToneOf(worstToneOf(panelBadges(key, model)))} />}
      onOpen={onOpenPanel ? () => onOpenPanel(key) : undefined}
    />
  );

  return (
    <div id={HELPER_ID} ref={cardRef} role="group" aria-label="Session details" className={cx(styles.card, className)} hidden={!open} inert={inert}>
      <div className={styles.head} title={head.status}>
        <span className={styles.mark} aria-hidden="true">
          <PiIcon />
        </span>
        <span className={styles.provider}>{`Pi ${head.piVersion}`}</span>
        <span className={styles.state}>
          <StateDot tone={sessionTone} />
          {status}
        </span>
      </div>
      <div className={styles.rows}>
        <Row
          icon={<TurnsIcon />}
          label="Turns"
          words={`${turnsWords} · ${model.loaded ? `${model.items.length} records` : "records unavailable"}`}
          dot={<StateDot tone="idle" />}
        />
        {governanceRow("decisions", "Decisions", <DecisionsIcon />, worstBadgeOf(panelBadges("decisions", model))?.text ?? "unavailable")}
        {governanceRow("publication", "Publication", <PublicationIcon />, worstBadgeOf(panelBadges("publication", model))?.text ?? "unavailable")}
        {governanceRow("recovery", "Recovery", <RecoveryIcon />, worstBadgeOf(panelBadges("recovery", model))?.text ?? "unavailable")}
        {streamsDown > 0 ? (
          <Row icon={<StreamsIcon />} label="Streams" words={`${streamsDown} unavailable`} dot={<StateDot tone="unknown" />} />
        ) : null}
      </div>
      <div className={styles.technical}>
        <Disclosure summary="Technical details">
          <div className={styles.facts}>
            <p className={styles.fact}>{head.status}</p>
            <p className={styles.fact}>Pi tools: unavailable</p>
            <p className={styles.fact}>
              {bindingName(model.source)} <Chip tone="idle">{model.source}</Chip>
            </p>
            <p className={styles.mono} data-identity="">
              Pi <span>{head.piVersion}</span> · {head.sessionId} · {head.workflowId}
            </p>
            {!model.loaded ? (
              <p className={styles.fact}>{unavailableWords(NO_PROJECTION)}</p>
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
              <p className={styles.fact}>No streams projected.</p>
            )}
            <div role="group" aria-label="Session statistics" className={styles.stats}>
              {stats.map((part) => (
                <span key={part} className={part.includes("unavailable") ? styles.un : undefined}>
                  {part}
                </span>
              ))}
              <span>build {version}</span>
            </div>
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
          </div>
        </Disclosure>
      </div>
    </div>
  );
}
