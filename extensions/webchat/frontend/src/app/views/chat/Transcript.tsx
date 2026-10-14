// The transcript: turns, messages and tool cards. Assistant text is markdown; user and system text
// is literal. A message keeps its incremental parser for as long as it is mounted, so a streaming
// message re-renders only the blocks whose source changed, and its state goes when it leaves the window.

import { useMemo, useState } from "react";
import { createIncrementalParser } from "../../../markdown/incremental";
import { Markdown } from "../../../markdown/react";
import { Chip } from "../../../ui/Chip";
import { cx } from "../../../ui/cx";
import { formatClock } from "../../../core/format";
import type { Item, ItemView, MessageItem } from "../../../core/contract";
import { groupTurn, type Turn } from "../../../core/store";
import type { Marker } from "./provenance";
import { UNVERIFIED_READING, VERIFIED_READING, turnLabel } from "./readings";
import { ToolCard, type Verifier } from "./ToolCard";
import styles from "./Chat.module.css";

interface TurnViewProps {
  readonly turn: Turn<Item>;
  readonly verifiedOf: Verifier;
  // The item marker of a record, or null when the view states its provenance once (see provenance.ts).
  readonly markOf: (item: ItemView) => Marker | null;
}

export function TurnView({ turn, verifiedOf, markOf }: TurnViewProps) {
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
            <MessageView key={block.item.id} item={block.item} marker={markOf(block.item)} />
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
  readonly marker: Marker | null;
}

function MessageView({ item, marker }: MessageViewProps) {
  if (item.type !== "message") return null;
  return (
    <div className={styles.msg} data-role={item.role} data-anchor={item.id}>
      <p className={styles.msgRole}>
        <span>{item.role}</span>
        <span> · </span>
        <time dateTime={item.occurred_at}>{formatClock(item.occurred_at)}</time>
        {marker === null ? null : (
          <>
            <span> · </span>
            {marker === "verified" ? (
              <Chip tone={VERIFIED_READING.tone}>{VERIFIED_READING.label}</Chip>
            ) : (
              <Chip tone={UNVERIFIED_READING.tone}>{UNVERIFIED_READING.label}</Chip>
            )}
          </>
        )}
      </p>
      <MessageText item={item} />
      {item.text_truncated === true ? (
        <p className={styles.note}>Transcript text truncated by the projection.</p>
      ) : null}
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
