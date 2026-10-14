import { useRef, type ReactNode } from "react";
import { jumpLabel } from "../../core/anchor";
import type { ItemView } from "../../core/contract";
import { FILTERS, countFilter, filterItems, governanceCounts } from "../../core/filters";
import { turnsOf } from "../../core/store";
import { Chip } from "../../ui/Chip";
import { useAnnouncer } from "../../ui/LiveRegion";
import { ToggleGroup, type ToggleItem } from "../../ui/ToggleGroup";
import { EmptyState, ErrorState, LoadingState } from "../../ui/StateMessage";
import { NO_PROJECTION, unavailableWords } from "../panels";
import { failureText, projectionPhase } from "../status";
import type { WorkspaceModel } from "../workspaceStore";
import { confirmedIn, isVerifiedIn } from "../verification";
import { filterSummaryText, postureEntries } from "./chat/readings";
import { markerFor, provenanceOf } from "./chat/provenance";
import { TurnView } from "./chat/Transcript";
import type { Verifier } from "./chat/ToolCard";
import { useTailWindow } from "./chat/tail";
import styles from "./chat/Chat.module.css";

// Shown at the top of the transcript while a notice applies. The notices themselves are shown once,
// above the tabs, by the shell: they sit above this view, so a reader inside the transcript cannot see
// them. Announcements are the shell's too, so this line is not live.
export const TRANSCRIPT_NOTICE_POINTER = "Notices for this transcript are above the tabs.";

// The Chat view: the posture of the retained window, the finding filters, the transcript, and the
// tail window. Presentation only. Counts come from the store's items and its applied count, and a
// success is shown as succeeded only with verified evidence (web-chat.md sections 4.1, 4.3 and 4.5).
export function ChatView({ model }: { readonly model: WorkspaceModel }) {
  const announcer = useAnnouncer();
  const logRef = useRef<HTMLDivElement | null>(null);
  const phase = projectionPhase(model);
  // The shared gate: a record is verified only in a live, structurally audited projection, and only with
  // its own observed evidence. The posture counts a success as confirmed only when it is verified and not lossy.
  const verifiedOf: Verifier = (item) => isVerifiedIn(model, item);
  const confirmedOf = (item: ItemView) => confirmedIn(model, item);
  const tail = useTailWindow({
    region: logRef,
    actions: model.actions,
    pending: model.tailPending,
    count: model.items.length,
    announcer,
  });

  const visible = filterItems(model.items, model.filter);
  // The provenance is stated once for the view when its records agree; a mixed view marks each record.
  const provenance = provenanceOf(visible, verifiedOf, model.source);
  const markOf = (item: ItemView) => markerFor(provenance, verifiedOf(item));
  const activeFilter = FILTERS.find((entry) => entry.id === model.filter);
  const filterLabel = activeFilter ? activeFilter.label : model.filter;
  const posture = postureEntries(governanceCounts(model.items, confirmedOf));
  const filters: ToggleItem[] = FILTERS.map((entry) => ({
    value: entry.id,
    label: entry.label,
    count: countFilter(model.items, entry.id),
  }));
  const summary = model.loaded
    ? filterSummaryText({
        filter: model.filter,
        label: filterLabel,
        total: model.items.length,
        matches: countFilter(model.items, model.filter),
        shown: visible.length,
        evicted: model.evicted,
      })
    : unavailableWords(NO_PROJECTION);

  function choose(value: string): void {
    const next = FILTERS.find((entry) => entry.id === value);
    if (next) model.actions.setFilter(next.id);
  }

  const hasNotice = model.gapNotice !== null || model.windowGap !== null || model.evicted > 0;

  return (
    <div className={styles.view}>
      <div className={styles.posture} role="group" aria-label="Observed posture">
        {!model.loaded ? (
          <span className={styles.postureEmpty}>{unavailableWords(NO_PROJECTION)}</span>
        ) : posture.length > 0 ? (
          posture.map((entry) => (
            <span key={entry.key} title={entry.hint}>
              <Chip tone={entry.tone}>{entry.text}</Chip>
            </span>
          ))
        ) : (
          <span className={styles.postureEmpty}>no decisions or results projected</span>
        )}
      </div>

      <div className={styles.toolbar}>
        <ToggleGroup label="Timeline filters" items={filters} pressed={model.filter} onPress={choose} />
        <p className={styles.summary}>{summary}</p>
      </div>

      {/* aria-live is off: the single polite announcer lives in the shell (LiveRegion). */}
      <div role="log" aria-label="Transcript" aria-live="off" tabIndex={-1} ref={logRef} className={styles.log}>
        {hasNotice ? <p className={styles.note}>{TRANSCRIPT_NOTICE_POINTER}</p> : null}
        {provenance.kind === "uniform" ? <p className={styles.note}>{provenance.text}</p> : null}
        {phase === "loaded" && model.error !== null ? <ErrorState title="Projection error">{model.error}</ErrorState> : null}
        {transcript()}
      </div>

      {tail.pending > 0 ? (
        <button type="button" className={styles.jump} onClick={tail.jump}>
          {jumpLabel(tail.pending)}
        </button>
      ) : null}
    </div>
  );

  // Loading, failure, empty, no-match and the transcript itself. One state at a time, and each is text,
  // never colour alone. A failure before the first snapshot replaces the loading skeleton.
  function transcript(): ReactNode {
    if (phase === "loading") return <LoadingState label="Loading projection…" />;
    if (phase === "failed") {
      return <ErrorState title="Projection not loaded">{failureText(model.error, model.status)}</ErrorState>;
    }
    if (model.items.length === 0) {
      return (
        <EmptyState title="Nothing to display">
          {model.source === "unavailable"
            ? "Production observations are unavailable. No live Pi/session source is registered."
            : "No transcript or activity has been projected yet."}
        </EmptyState>
      );
    }
    if (visible.length === 0) {
      return (
        <EmptyState title={`No ${filterLabel.toLowerCase()} findings`}>
          No projected element matches this filter. Adjust the filter to review the whole timeline.
        </EmptyState>
      );
    }
    return turnsOf(visible).map((turn) => (
      <TurnView key={turn.id} turn={turn} verifiedOf={verifiedOf} markOf={markOf} />
    ));
  }
}
