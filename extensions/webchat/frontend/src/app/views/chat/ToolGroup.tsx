// A run of consecutive tool activity, drawn as one Trace line: the count, the tools, and how many records ended in each outcome,
// counted in words. The line is never calmer than its worst record (toolGroups.ts), so a denied tool in a closed group is still
// seen without opening it. Opened, the group lists each tool card as it was.

import { Chip } from "../../../ui/Chip";
import { Trace, TraceIcon, TraceLine, TraceMeta, TraceTitle } from "../../../ui/Trace";
import type { ItemView } from "../../../core/contract";
import type { Marker } from "./provenance";
import { summariseToolGroup, type ToolRun } from "./toolGroups";
import { ToolCard, type Verifier } from "./ToolCard";
import styles from "./Chat.module.css";

interface ToolGroupProps {
  readonly run: ToolRun;
  readonly verifiedOf: Verifier;
  readonly markOf: (item: ItemView) => Marker | null;
}

export function ToolGroup({ run, verifiedOf, markOf }: ToolGroupProps) {
  const summary = summariseToolGroup(run.blocks, verifiedOf);
  const title = `${run.blocks.length} tool actions`;
  const tools = summary.tools.map((tool) => (tool.count > 1 ? `${tool.name} ×${tool.count}` : tool.name)).join(" · ");
  const line = (
    <TraceLine>
      <TraceIcon>{String(run.blocks.length)}</TraceIcon>
      <TraceTitle title={title} detail={<span className={styles.name}>{tools}</span>} />
      <TraceMeta>
        {summary.outcomes.map((outcome) => (
          <Chip key={`${outcome.tone}|${outcome.label}`} tone={outcome.tone}>
            {`${outcome.count} ${outcome.label}`}
          </Chip>
        ))}
      </TraceMeta>
    </TraceLine>
  );
  return (
    <div data-anchor={run.key} data-group="">
      <Trace tone={summary.tone} line={line}>
        <div className={styles.group}>
          {run.blocks.map((block) => (
            <ToolCard key={block.key} block={block} verifiedOf={verifiedOf} markOf={markOf} />
          ))}
        </div>
      </Trace>
    </div>
  );
}
