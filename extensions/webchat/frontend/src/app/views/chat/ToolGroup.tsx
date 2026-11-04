// A run of consecutive tool activity. Three or more are drawn as one group: a line with the count, the tools and the findings
// counted in words, over the tool cards. The line is never calmer than its worst finding (toolGroups.ts), and it says how many
// records are unverified, so a closed group hides no outcome and no verification wording. Fewer than three are drawn as the
// cards alone.
//
// The cards sit in the same place of the tree whether the run is a group or not (one wrapper, an optional line, one body), so a
// run that grows to three tools, or shrinks under a filter, does not remount them: an open card stays open and a focused card
// keeps its focus. A group that forms while the reader is looking at the run starts open; a group present when the transcript
// is first drawn starts closed.

import { useId, useState, type ReactNode } from "react";
import { Chip } from "../../../ui/Chip";
import { TRACE_FRAME, TraceIcon, TraceLine, TraceMeta, TraceTitle, TraceToggle } from "../../../ui/Trace";
import { VisuallyHidden } from "../../../ui/VisuallyHidden";
import type { ItemView } from "../../../core/contract";
import type { ToolBlock } from "../../../core/store";
import type { Marker } from "./provenance";
import { UNVERIFIED_READING } from "./readings";
import { GROUP_MIN, summariseToolGroup, type ToolRun as ToolRunData } from "./toolGroups";
import { ToolCard, type Verifier } from "./ToolCard";
import styles from "./Chat.module.css";

interface ToolRunProps {
  readonly run: ToolRunData;
  readonly verifiedOf: Verifier;
  readonly markOf: (item: ItemView) => Marker | null;
}

// The record a block is named for: the result, else the request, else the policy decision.
function primaryOf(block: ToolBlock): ItemView | null {
  return block.result ?? block.activity ?? block.decision;
}

export function ToolRun({ run, verifiedOf, markOf }: ToolRunProps) {
  const grouped = run.blocks.length >= GROUP_MIN;
  const [open, setOpen] = useState(false);
  // A run that becomes a group while it is on screen opens, so the cards the reader is looking at do not collapse under them.
  const [wasGrouped, setWasGrouped] = useState(grouped);
  if (grouped !== wasGrouped) {
    setWasGrouped(grouped);
    if (grouped) setOpen(true);
  }
  const bodyId = useId();
  const tone = grouped ? summariseToolGroup(run.blocks, verifiedOf).tone : undefined;
  return (
    <div
      className={grouped ? TRACE_FRAME : styles.run}
      data-anchor={grouped ? run.key : undefined}
      data-group={grouped ? "" : undefined}
      data-tone={tone}
      data-open={grouped ? open : undefined}
    >
      {grouped ? (
        <TraceToggle line={<GroupLine run={run} verifiedOf={verifiedOf} markOf={markOf} />} open={open} controls={bodyId} onToggle={() => setOpen((current) => !current)} />
      ) : null}
      <div id={bodyId} className={grouped ? styles.group : styles.run} hidden={grouped && !open}>
        {run.blocks.map((block) => (
          <ToolCard key={block.key} block={block} verifiedOf={verifiedOf} markOf={markOf} />
        ))}
      </div>
    </div>
  );
}

function GroupLine({ run, verifiedOf, markOf }: ToolRunProps): ReactNode {
  const summary = summariseToolGroup(run.blocks, verifiedOf);
  const title = `${run.blocks.length} tool actions`;
  const tools = summary.tools.map((tool) => (tool.count > 1 ? `${tool.name} ×${tool.count}` : tool.name)).join(" · ");
  // The verification wording the cards carry: a mixed view marks each unverified record with a chip, and a uniform view states it
  // once above the transcript, so a card there carries the word for screen readers only. The group says the same, once.
  const unverified = run.blocks.filter((block) => {
    const primary = primaryOf(block);
    return primary !== null && !verifiedOf(primary);
  });
  const marked = unverified.filter((block) => {
    const primary = primaryOf(block);
    return primary !== null && markOf(primary) === "unverified";
  });
  return (
    <TraceLine>
      <TraceIcon>{String(run.blocks.length)}</TraceIcon>
      <TraceTitle title={title} detail={<span className={styles.name}>{tools}</span>} />
      {unverified.length > 0 && marked.length === 0 ? <VisuallyHidden>{UNVERIFIED_READING.label}</VisuallyHidden> : null}
      <TraceMeta>
        {summary.findings.map((finding) => (
          <Chip key={`${finding.tone}|${finding.label}|${finding.kind ?? ""}`} tone={finding.tone}>
            {`${finding.count} ${finding.label}${finding.kind === "verdict" ? " verdict" : ""}`}
          </Chip>
        ))}
        {marked.length > 0 ? <Chip tone={UNVERIFIED_READING.tone}>{`${marked.length} ${UNVERIFIED_READING.label}`}</Chip> : null}
      </TraceMeta>
    </TraceLine>
  );
}
