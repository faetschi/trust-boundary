// The transcript: turns, messages and tool cards. Assistant text is markdown; user and system text is literal. A
// message keeps its incremental parser for as long as it is mounted, so a streaming message re-renders only the blocks
// whose source changed, and its state goes when it leaves the window. Each message is labelled by its role, carries its
// reading from the gate, and has a meta row: its time, Copy, and the record it came from.

import { useEffect, useMemo, useState } from "react";
import { createIncrementalParser } from "../../../markdown/incremental";
import { Markdown } from "../../../markdown/react";
import { Chip } from "../../../ui/Chip";
import { cx } from "../../../ui/cx";
import { useAnnouncer } from "../../../ui/LiveRegion";
import { VisuallyHidden } from "../../../ui/VisuallyHidden";
import type { Item, ItemView, MessageItem } from "../../../core/contract";
import { groupTurn, type Turn } from "../../../core/store";
import { clockHm, COPIED_ANNOUNCEMENT, COPIED_LABEL, COPIED_MS, COPY_LABEL, COPY_TRUNCATED_LABEL, COPY_UNAVAILABLE_ANNOUNCEMENT, COPY_UNAVAILABLE_LABEL, copyMessage, roleLabel } from "./chrome";
import type { Marker } from "./provenance";
import { messageReading, turnLabel, type ChipReading } from "./readings";
import { ToolCard, type Verifier } from "./ToolCard";
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
        <span>{label}</span>
        <span>{turn.items.length} element(s)</span>
      </div>
      <div className={styles.turnBody}>
        {groupTurn(turn.items).map((block) =>
          block.kind === "message" ? (
            <MessageView
              key={block.item.id}
              item={block.item}
              reading={messageReading(block.item.verification_scope, verifiedOf(block.item))}
              onRecord={onRecord}
            />
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
      <p className={styles.msgRole}>
        <span>{roleLabel(item.role)}</span>
        {item.role === "assistant" ? <VisuallyHidden>, assistant</VisuallyHidden> : null}{" "}
        <Chip tone={reading.tone}>{reading.label}</Chip>
      </p>
      <MessageText item={item} />
      {truncated ? <p className={styles.note}>Transcript text truncated by the projection.</p> : null}
      <div className={styles.meta}>
        <time className={styles.time} dateTime={item.occurred_at}>
          {clockHm(item.occurred_at)}
        </time>
        {item.text ? <CopyButton text={item.text} truncated={truncated} /> : null}
        <button type="button" className={styles.recordButton} onClick={(event) => onRecord(item.id, event.currentTarget)}>
          {`record ${item.sequence}`}
        </button>
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

// Copies the projected text. The label says what happened: Copied for a second, or Copy unavailable when the page or the
// browser refuses. Each outcome is announced once, politely, through the shell's announcer.
function CopyButton({ text, truncated }: { readonly text: string; readonly truncated: boolean }) {
  const announcer = useAnnouncer();
  const [state, setState] = useState<CopyState>("idle");

  useEffect(() => {
    if (state !== "copied") return undefined;
    const timer = setTimeout(() => setState("idle"), COPIED_MS);
    return () => clearTimeout(timer);
  }, [state]);

  async function copy(): Promise<void> {
    const clipboard = typeof navigator === "undefined" ? null : navigator.clipboard;
    const done = await copyMessage(text, clipboard);
    setState(done ? "copied" : "unavailable");
    announcer.announce(done ? COPIED_ANNOUNCEMENT : COPY_UNAVAILABLE_ANNOUNCEMENT);
  }

  const label =
    state === "copied"
      ? COPIED_LABEL
      : state === "unavailable"
        ? COPY_UNAVAILABLE_LABEL
        : truncated
          ? COPY_TRUNCATED_LABEL
          : COPY_LABEL;
  return (
    <button type="button" className={styles.copy} onClick={() => void copy()}>
      {label}
    </button>
  );
}
