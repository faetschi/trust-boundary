// One tool activity: the request, the policy decision and the result, correlated by tool_call_id
// (core store.ts groupTurn). The card is a native disclosure. Its summary states the tool, the
// bounded summary and the chips; its steps state each record with its reason, digest, timestamp
// and notes. The readings (readings.ts) carry the honesty rules.

import type { ReactNode } from "react";
import { Chip } from "../../../ui/Chip";
import { cx } from "../../../ui/cx";
import { Disclosure } from "../../../ui/Disclosure";
import { VisuallyHidden } from "../../../ui/VisuallyHidden";
import { formatClock, shortDigest, toolGlyph } from "../../../core/format";
import type { ItemView } from "../../../core/contract";
import type { ToolBlock } from "../../../core/store";
import type { Marker } from "./provenance";
import { headlineSummary } from "./headline";
import {
  LOSSY_EVIDENCE_READING,
  LOSSY_READING,
  UNVERIFIED_READING,
  VERIFIED_READING,
  stateReading,
  verdictReading,
} from "./readings";
import styles from "./Chat.module.css";

// Whether an item is verified in its projection: the shared gate (app verification.ts isVerifiedIn). Only a
// verified success is shown as succeeded, and a lossy one never is.
export type Verifier = (item: ItemView) => boolean;

interface ToolCardProps {
  readonly block: ToolBlock;
  readonly verifiedOf: Verifier;
  // The item marker of a record in a mixed view (see provenance.ts). Null where the view states it once.
  readonly markOf: (item: ItemView) => Marker | null;
}

export function ToolCard({ block, verifiedOf, markOf }: ToolCardProps) {
  const { activity, decision, result } = block;
  const primary = result ?? activity ?? decision;
  const stateItem = result ?? activity;
  const tool = primary?.tool ?? "tool";
  const stateReadingOf = stateItem
    ? stateReading(stateItem.state, verifiedOf(stateItem), stateItem.type === "result" && stateItem.lossy === true)
    : null;
  const verdict = decision ? verdictReading(decision.verdict) : null;
  const marker = primary ? markOf(primary) : null;
  // A verified lossy result keeps its evidence marker, neutral: the output is not confirmed, so it is never green.
  const evidence = result?.lossy === true ? LOSSY_EVIDENCE_READING : VERIFIED_READING;
  // The name is drawn once, here; the summary follows it without a repeat of the name.
  const headline = headlineSummary(tool, summaryOf(block));
  // A uniform view states "unverified" once, above the transcript, so the card carries the word for
  // screen-reader users only. A mixed view draws the chip, which the name already includes.
  const unverifiedWord = primary !== null && !verifiedOf(primary) && marker !== "unverified";
  // A refused or denied record (a DENY verdict, or a result in the deny tone) takes the destructive tint of the card.
  const refused = verdict?.tone === "deny" || stateReadingOf?.tone === "deny";

  // One wrapping line: at narrow widths the chips move under the summary, not past the card edge. The headline is the
  // target, bold; the tool and the record are the detail line beneath it, in mono.
  const summary = (
    <span className={styles.head}>
      <span className={styles.glyph} aria-hidden="true">
        {toolGlyph(tool)}
      </span>
      <span className={styles.titles}>
        <span className={styles.summaryText} title={headline}>
          {headline}
        </span>
        <span className={styles.byline}>
          <span className={styles.name}>{tool}</span>
          {/* The record the card is named for: the result, else the request, else the policy decision (sequence as projected). */}
          {primary ? <span className={styles.recordRef}>{`record ${primary.sequence}`}</span> : null}
        </span>
      </span>
      {unverifiedWord ? <VisuallyHidden>{UNVERIFIED_READING.label}</VisuallyHidden> : null}
      <span className={styles.chips}>
        {verdict ? <Chip tone={verdict.tone}>{verdict.label}</Chip> : null}
        {stateReadingOf ? <Chip tone={stateReadingOf.tone}>{stateReadingOf.label}</Chip> : null}
        {marker === "unverified" ? <Chip tone={UNVERIFIED_READING.tone}>{UNVERIFIED_READING.label}</Chip> : null}
        {marker === "verified" ? <Chip tone={evidence.tone}>{evidence.label}</Chip> : null}
      </span>
    </span>
  );

  return (
    <div className={cx(styles.card, refused && styles.refused)} data-anchor={block.key}>
      <Disclosure summary={summary}>
        <div className={styles.steps}>
          {activity ? (
            <Step label="request" at={activity.occurred_at}>
              <span className={styles.mono}>{activity.summary}</span>
            </Step>
          ) : null}
          {decision && verdict ? (
            <Step label="policy" at={decision.occurred_at}>
              <span className={styles.line}>
                <Chip tone={verdict.tone}>{verdict.label}</Chip>
                <span className={styles.mono}>{decision.reason_code || "unknown"}</span>
                <Digest label="policy digest" value={decision.policy_digest} />
              </span>
              <p className={styles.note}>{verdict.note}</p>
              {decision.summary ? <p className={styles.detail}>{decision.summary}</p> : null}
            </Step>
          ) : null}
          {result && stateReadingOf ? (
            <Step label="result" at={result.occurred_at}>
              <span className={styles.line}>
                <Chip tone={stateReadingOf.tone}>{stateReadingOf.label}</Chip>
                {result.lossy === true ? <Chip tone={LOSSY_READING.tone}>{LOSSY_READING.label}</Chip> : null}
                <Digest label="result digest" value={result.digest} />
              </span>
              <p className={styles.note}>{stateReadingOf.note}</p>
              {result.lossy === true ? <p className={styles.note}>{LOSSY_READING.note}</p> : null}
              {result.summary ? <p className={styles.detail}>{result.summary}</p> : null}
              {result.detail ? <p className={styles.detail}>{result.detail}</p> : null}
            </Step>
          ) : null}
          {activity?.detail ? <p className={styles.detail}>{activity.detail}</p> : null}
        </div>
      </Disclosure>
    </div>
  );
}

// The first bounded summary, in the order the legacy transcript used: request, policy, result.
function summaryOf(block: ToolBlock): string | undefined {
  for (const candidate of [block.activity, block.decision, block.result]) {
    if (candidate?.summary) return candidate.summary;
  }
  return undefined;
}

interface StepProps {
  readonly label: string;
  readonly at: string;
  readonly children: ReactNode;
}

function Step({ label, at, children }: StepProps) {
  return (
    <div className={styles.step}>
      <span className={styles.stepLabel}>{label}</span>
      <div className={styles.stepBody}>
        {children}
        <time className={styles.time} dateTime={at}>
          {formatClock(at)}
        </time>
      </div>
    </div>
  );
}

// A digest, shortened on screen. The full value is in the title and in the text a screen reader reads.
function Digest({ label, value }: { readonly label: string; readonly value?: string | undefined }) {
  if (!value) {
    return (
      <span className={styles.mono}>
        —<VisuallyHidden>no {label} projected</VisuallyHidden>
      </span>
    );
  }
  return (
    <span className={styles.digest} title={value}>
      <span aria-hidden="true">{shortDigest(value)}</span>
      <VisuallyHidden>{`${label} ${value}`}</VisuallyHidden>
    </span>
  );
}
