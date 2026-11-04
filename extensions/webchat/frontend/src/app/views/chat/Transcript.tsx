// The transcript: turns, messages and tool cards. Assistant text is markdown; user and system text is literal. A
// message keeps its incremental parser for as long as it is mounted, so a streaming message re-renders only the blocks
// whose source changed, and its state goes when it leaves the window. Each message has a label row (its role, its reading
// from the gate, a rule, and the record it came from), its text, and a meta row beneath: its time and an icon-only Copy.

import { useEffect, useMemo, useState } from "react";
import { createIncrementalParser } from "../../../markdown/incremental";
import { Markdown } from "../../../markdown/react";
import { Button } from "../../../ui/Button";
import { Chip } from "../../../ui/Chip";
import { cx } from "../../../ui/cx";
import { useAnnouncer } from "../../../ui/LiveRegion";
import { VisuallyHidden } from "../../../ui/VisuallyHidden";
import type { Item, ItemView, MessageItem } from "../../../core/contract";
import { groupTurn, type Turn } from "../../../core/store";
import { clockHm, COPIED_ANNOUNCEMENT, COPIED_LABEL, COPIED_MS, COPY_LABEL, COPY_TRUNCATED_LABEL, COPY_UNAVAILABLE_ANNOUNCEMENT, COPY_UNAVAILABLE_LABEL, copyMessage, copyName, roleLabel } from "./chrome";
import type { Marker } from "./provenance";
import { messageReading, turnLabel, type ChipReading } from "./readings";
import { ToolCard, type Verifier } from "./ToolCard";
import { ToolGroup } from "./ToolGroup";
import { groupRuns } from "./toolGroups";
import styles from "./Chat.module.css";

// Opens a record in the inspector, from the control that asked. The shell moves focus back to that control on close.
export type RecordOpener = (id: string, returnTo: HTMLElement | null) => void;

interface TurnViewProps {
  readonly turn: Turn<Item>;
  readonly verifiedOf: Verifier;
  // The item marker of a tool record, or null when the view states its provenance once (see provenance.ts).
  readonly markOf: (item: ItemView) => Marker | null;
  readonly onRecord: RecordOpener;
}

export function TurnView({ turn, verifiedOf, markOf, onRecord }: TurnViewProps) {
  const label = turnLabel(turn.id);
  return (
    <section className={styles.turn} aria-label={label}>
      <div className={styles.turnHead}>
        <span className={styles.turnLabel}>{label}</span>
        <span className={styles.turnRule} aria-hidden="true" />
        <span className={styles.turnCount}>{turn.items.length} element(s)</span>
      </div>
      <div className={styles.turnBody}>
        {groupRuns(groupTurn(turn.items)).map((block) =>
          block.kind === "message" ? (
            <MessageView
              key={block.item.id}
              item={block.item}
              reading={messageReading(block.item.verification_scope, verifiedOf(block.item))}
              onRecord={onRecord}
            />
          ) : block.kind === "group" ? (
            <ToolGroup key={block.key} run={block} verifiedOf={verifiedOf} markOf={markOf} />
          ) : (
            <ToolCard key={block.key} block={block} verifiedOf={verifiedOf} markOf={markOf} />
          ),
        )}
      </div>
    </section>
  );
}

interface MessageViewProps {
  readonly item: Item;
  readonly reading: ChipReading;
  readonly onRecord: RecordOpener;
}

function MessageView({ item, reading, onRecord }: MessageViewProps) {
  if (item.type !== "message") return null;
  const truncated = item.text_truncated === true;
  return (
    <div className={styles.msg} data-role={item.role} data-anchor={item.id}>
      <div className={styles.msgHead}>
        <p className={styles.msgRole}>
          <span>{roleLabel(item.role)}</span>
          {item.role === "assistant" ? <VisuallyHidden>, assistant</VisuallyHidden> : null}{" "}
          <Chip tone={reading.tone}>{reading.label}</Chip>
        </p>
        <span className={styles.rule} aria-hidden="true" />
        <Button variant="ghost" size="sm" className={styles.recordButton} onClick={(event) => onRecord(item.id, event.currentTarget)}>
          {`record ${item.sequence}`}
        </Button>
      </div>
      <MessageText item={item} />
      {truncated ? <p className={styles.note}>Transcript text truncated by the projection.</p> : null}
      <div className={styles.meta}>
        <time className={styles.time} dateTime={item.occurred_at}>
          {clockHm(item.occurred_at)}
        </time>
        {item.text ? <CopyButton text={item.text} truncated={truncated} role={item.role} sequence={item.sequence} /> : null}
      </div>
    </div>
  );
}

function MessageText({ item }: { readonly item: MessageItem }) {
  if (item.role !== "assistant" || !item.text) {
    return <p className={styles.msgText}>{item.text || "(empty)"}</p>;
  }
  return <AssistantText text={item.text} />;
}

// The parser lives in component state, so each message keeps its own parsed blocks, and the state
// is released when the message unmounts (the retained window has dropped it).
function AssistantText({ text }: { readonly text: string }) {
  const [parser] = useState(() => createIncrementalParser());
  const { blocks } = useMemo(() => parser.update(text), [parser, text]);
  return (
    <div className={cx(styles.assistant, "md-body")}>
      <Markdown blocks={blocks} />
    </div>
  );
}

type CopyState = "idle" | "copied" | "unavailable";

interface CopyOutcome {
  readonly state: CopyState;
  // Counts the copies, so each one is a new outcome, even when it reads the same as the last: a second copy restarts the timer.
  readonly copies: number;
}

interface CopyButtonProps {
  readonly text: string;
  readonly truncated: boolean;
  readonly role: MessageItem["role"];
  readonly sequence: number;
}

// Copies the projected text. The control is an icon alone: its glyph turns to a tick when copied and to a cross when the page
// or the browser refuses, and both states read for the same second before the glyph returns. Its name carries the state word
// and the message it copies, so a screen reader hears both. Each outcome is announced once, politely, through the shell's announcer.
function CopyButton({ text, truncated, role, sequence }: CopyButtonProps) {
  const announcer = useAnnouncer();
  const [outcome, setOutcome] = useState<CopyOutcome>({ state: "idle", copies: 0 });

  useEffect(() => {
    if (outcome.state === "idle") return undefined;
    const timer = setTimeout(() => setOutcome((current) => ({ state: "idle", copies: current.copies })), COPIED_MS);
    return () => clearTimeout(timer);
  }, [outcome]);

  async function copy(): Promise<void> {
    const clipboard = typeof navigator === "undefined" ? null : navigator.clipboard;
    const done = await copyMessage(text, clipboard);
    setOutcome((current) => ({ state: done ? "copied" : "unavailable", copies: current.copies + 1 }));
    announcer.announce(done ? COPIED_ANNOUNCEMENT : COPY_UNAVAILABLE_ANNOUNCEMENT);
  }

  const label =
    outcome.state === "copied"
      ? COPIED_LABEL
      : outcome.state === "unavailable"
        ? COPY_UNAVAILABLE_LABEL
        : truncated
          ? COPY_TRUNCATED_LABEL
          : COPY_LABEL;
  const name = copyName(label, role, sequence);
  return (
    <Button variant="ghost" size="icon" aria-label={name} title={name} onClick={() => void copy()}>
      <CopyGlyph state={outcome.state} />
    </Button>
  );
}

// The copy mark: two sheets; a tick once copied; a cross when the copy is refused. Drawn in the control's ink, and hidden from
// assistive technology, which reads the control's name.
function CopyGlyph({ state }: { readonly state: CopyState }) {
  return (
    <svg
      viewBox="0 0 16 16"
      width="14"
      height="14"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {state === "copied" ? (
        <path d="M3 8.5l3 3 7-7" />
      ) : state === "unavailable" ? (
        <path d="M4 4l8 8M12 4l-8 8" />
      ) : (
        <>
          <rect x="5.5" y="5.5" width="7.5" height="7.5" rx="1.5" />
          <path d="M10.5 5.5V4A1.5 1.5 0 0 0 9 2.5H4A1.5 1.5 0 0 0 2.5 4v5A1.5 1.5 0 0 0 4 10.5h1.5" />
        </>
      )}
    </svg>
  );
}
